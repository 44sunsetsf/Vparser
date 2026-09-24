"""VideoEvidenceRetrievalService: hybrid ranking of semantic similarity, transcript and OCR keyword hits."""
import math
import re
from dataclasses import dataclass

from ..textutil import is_blank, trim
from ..llm.deepseek import DeepSeekClient
from ..llm.embedding import EmbeddingClient
from ..models.dto import VideoChunk, VideoEvidenceHit, VideoRetrievalIntent, VideoSegment
from ..observability import VECTOR_FALLBACKS, span
from ..telemetry import AgentTelemetry
from .qdrant import QdrantVectorStore

TOP_K = 3
MAX_USER_HITS = 8
MAX_SNIPPET_LENGTH = 180

_WS = re.compile(r"[ \t\n\x0b\f\r]+")  # ASCII whitespace
_SPLIT = re.compile(r"[ \t\n\x0b\f\r，。！？、,.;:：；!?]+")


@dataclass
class _ScoredChunk:
    chunk: VideoChunk
    score: float


@dataclass
class _ScoredSegment:
    segment: VideoSegment
    score: float
    transcript_score: float
    visual_score: float


def _normalize(value: str | None) -> str:
    return "" if value is None else _WS.sub("", value.lower())


def _normalized_ocr_texts(segment: VideoSegment) -> list[str]:
    out: list[str] = []
    for t in segment.ocr_texts:
        if t is None:
            continue
        t = trim(t)
        if is_blank(t) or t in out:
            continue
        out.append(t)
    return out


def _term_score(terms: list[str], content: str) -> float:
    normalized_content = _normalize(content)
    normalized_terms: list[str] = []
    for t in terms:
        n = _normalize(t)
        if not is_blank(n) and n not in normalized_terms:
            normalized_terms.append(n)
    matched = sum(1 for t in normalized_terms if t in normalized_content)
    return 0.0 if not normalized_terms else matched / len(normalized_terms)


def _cosine(left: list[float], right: list[float]) -> float:
    if len(left) != len(right) or not left:
        return 0.0
    dot = left_len = right_len = 0.0
    for a, b in zip(left, right):
        dot += a * b
        left_len += a * a
        right_len += b * b
    if left_len == 0 or right_len == 0:
        return 0.0
    return dot / (math.sqrt(left_len) * math.sqrt(right_len))


def _fallback_terms(query: str | None) -> list[str]:
    if query is None or is_blank(query):
        return []
    terms: list[str] = []
    for part in _SPLIT.split(trim(query)):
        part = trim(part)
        if len(part) >= 2 and part not in terms:
            terms.append(part)
            if len(terms) >= 8:
                break
    return terms if terms else [trim(query)]


def _abbreviate(value: str | None) -> str:
    normalized = "" if value is None else trim(_WS.sub(" ", value))
    return normalized if len(normalized) <= MAX_SNIPPET_LENGTH else normalized[:MAX_SNIPPET_LENGTH] + "..."


class VideoEvidenceRetrievalService:
    def __init__(self, deepseek: DeepSeekClient, embedding: EmbeddingClient,
                 vector_store: QdrantVectorStore, telemetry: AgentTelemetry) -> None:
        self._deepseek = deepseek
        self._embedding = embedding
        self._vector_store = vector_store
        self._telemetry = telemetry

    def retrieve(self, media_id: int | None, goal: str, chunks: list[VideoChunk]) -> list[VideoSegment]:
        return [r.segment for r in self._rank(media_id, goal, chunks)]

    def search(self, media_id: int | None, query: str, chunks: list[VideoChunk]) -> list[VideoEvidenceHit]:
        return [self._to_hit(r) for r in self._rank(media_id, query, chunks)[:MAX_USER_HITS]]

    def _rank(self, media_id: int | None, goal: str, chunks: list[VideoChunk]) -> list[_ScoredSegment]:
        with span("retrieval.rank", **{"agent.stage": "RETRIEVAL", "retrieval.chunks": len(chunks)}) as s:
            ranked = self._rank_chunks(media_id, goal, chunks)
            s.set_attribute("retrieval.segments", len(ranked))
            return ranked

    def _rank_chunks(self, media_id: int | None, goal: str, chunks: list[VideoChunk]) -> list[_ScoredSegment]:
        intent = self._retrieval_intent(goal)
        query_embedding = self._embed(intent.semantic_query)
        vector_scores = self._vector_scores(media_id, query_embedding)

        scored = [_ScoredChunk(c, self._score(intent, query_embedding, vector_scores, c)) for c in chunks]
        ranked = sorted(scored, key=lambda s: -s.score)[:TOP_K]  # stable sort: equal scores keep chunk order
        if ranked:
            self._telemetry.value_current("retrievalTopScore", ranked[0].score)
            self._telemetry.increment_current("retrievalChunks", len(ranked))
        segments = [self._score_segment(intent, c.score, s) for c in ranked for s in c.chunk.raw_segments]
        return sorted(segments, key=lambda r: (-r.score, r.segment.start_ms))

    def index(self, media_id: int, chunks: list[VideoChunk]) -> None:
        try:
            self._vector_store.upsert(media_id, chunks)
            self._telemetry.increment_current("vectorStoreWrites", len(chunks))
        except Exception:
            # 向量库挂了仍可走内存向量和关键词,别让检索基础设施拖垮分析主链路。
            self._telemetry.increment_current("vectorStoreFallbacks", 1)
            VECTOR_FALLBACKS.inc()

    def _score(self, intent, query_embedding, vector_scores, chunk: VideoChunk) -> float:
        remote = vector_scores.get(f"{chunk.start_time}:{chunk.end_time}")
        semantic = _cosine(query_embedding, chunk.embedding) if remote is None else remote
        return (semantic * 0.6
                + _term_score(intent.keywords, self._searchable_text(chunk)) * 0.25
                + _term_score(intent.visual_keywords, self._visual_text(chunk)) * 0.15)

    def _vector_scores(self, media_id: int | None, query_embedding: list[float]) -> dict[str, float]:
        scores: dict[str, float] = {}
        if media_id is None or not query_embedding:
            return scores
        try:
            for hit in self._vector_store.search(media_id, query_embedding, TOP_K * 2):
                scores[f"{hit.start_ms}:{hit.end_ms}"] = hit.score
        except Exception:
            self._telemetry.increment_current("vectorStoreFallbacks", 1)
            VECTOR_FALLBACKS.inc()
        return scores

    @staticmethod
    def _score_segment(intent, chunk_score: float, segment: VideoSegment) -> _ScoredSegment:
        transcript_score = _term_score(intent.keywords, segment.transcript)
        visual_score = _term_score(intent.visual_keywords, " ".join(_normalized_ocr_texts(segment)))
        return _ScoredSegment(segment, chunk_score * 0.55 + transcript_score * 0.25 + visual_score * 0.20,
                              transcript_score, visual_score)

    @staticmethod
    def _to_hit(result: _ScoredSegment) -> VideoEvidenceHit:
        segment = result.segment
        ocr_texts = _normalized_ocr_texts(segment)
        has_transcript = not is_blank(segment.transcript)
        has_ocr = bool(ocr_texts)
        source = ("ASR+OCR" if has_transcript and has_ocr else "OCR" if has_ocr
                  else "ASR" if has_transcript else "时间片段")
        ocr_text = " ".join(ocr_texts)
        preferred = ocr_text if result.visual_score > result.transcript_score else segment.transcript
        if is_blank(preferred):
            preferred = ocr_text if has_ocr else segment.transcript
        if is_blank(preferred):
            preferred = "该时间段暂无可展示文本"
        return VideoEvidenceHit(start_ms=segment.start_ms, end_ms=segment.end_ms, source=source,
                                snippet=_abbreviate(preferred), transcript=segment.transcript,
                                ocr_texts=ocr_texts)

    @staticmethod
    def _searchable_text(chunk: VideoChunk) -> str:
        return " ".join([chunk.segment_summary, " ".join(chunk.keywords),
                         " ".join(s.transcript for s in chunk.raw_segments)])

    @staticmethod
    def _visual_text(chunk: VideoChunk) -> str:
        return " ".join(t for s in chunk.raw_segments for t in _normalized_ocr_texts(s))

    def _retrieval_intent(self, goal: str) -> VideoRetrievalIntent:
        try:
            intent = self._deepseek.plan_retrieval(goal)
            if not is_blank(intent.semantic_query):
                return intent
        except Exception:
            self._telemetry.increment_current("retrievalIntentFallbacks", 1)
        return VideoRetrievalIntent(semantic_query=goal, keywords=_fallback_terms(goal),
                                    visual_keywords=_fallback_terms(goal))

    def _embed(self, text: str) -> list[float]:
        try:
            return self._embedding.embed(text)
        except Exception:
            self._telemetry.increment_current("embeddingFallbacks", 1)
            return []
