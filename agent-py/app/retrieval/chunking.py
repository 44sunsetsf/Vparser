"""VideoChunkingService: groups segments into five-minute chunks with an LLM summary and an embedding."""
from ..textutil import is_blank, trim
from ..llm.deepseek import DeepSeekClient
from ..llm.embedding import EmbeddingClient
from ..models.dto import ChunkSummary, VideoChunk, VideoSegment
from ..telemetry import AgentTelemetry

CHUNK_MS = 5 * 60 * 1000


def normalize_texts(values: list[str] | None) -> list[str]:
    if values is None:
        return []
    out: list[str] = []
    for v in values:
        if v is None:
            continue
        v = trim(v)
        if is_blank(v) or v in out:
            continue
        out.append(v)
    return out


class VideoChunkingService:
    def __init__(self, deepseek: DeepSeekClient, embedding: EmbeddingClient, telemetry: AgentTelemetry) -> None:
        self._deepseek = deepseek
        self._embedding = embedding
        self._telemetry = telemetry

    def build(self, segments: list[VideoSegment]) -> list[VideoChunk]:
        if not segments:
            return []
        ordered = sorted((s for s in segments if s is not None), key=lambda s: s.start_ms)
        if not ordered:
            return []
        chunks: list[VideoChunk] = []
        start = 0
        while start <= ordered[-1].start_ms:
            end = start + CHUNK_MS
            raw = [s for s in ordered if start <= s.start_ms < end]
            if raw:
                summary = self._summarize(raw)
                keywords = normalize_texts(summary.keywords)
                embedding_text = summary.segment_summary + "\n" + " ".join(keywords)
                chunks.append(VideoChunk(
                    start_time=start, end_time=end, segment_summary=summary.segment_summary,
                    keywords=keywords, raw_segments=raw, embedding=self._embed(embedding_text)))
            start += CHUNK_MS
        return chunks

    def _summarize(self, segments: list[VideoSegment]) -> ChunkSummary:
        try:
            return self._deepseek.summarize_chunk(segments)
        except Exception:
            self._telemetry.increment_current("summaryFallbacks", 1)
            texts = [s.transcript + " " + " ".join(normalize_texts(s.ocr_texts)) for s in segments]
            raw_text = " ".join(t for t in texts if not is_blank(t))
            summary = raw_text if len(raw_text) <= 500 else raw_text[:500]
            return ChunkSummary(segment_summary=summary, keywords=[])

    def _embed(self, text: str) -> list[float]:
        try:
            return self._embedding.embed(text)
        except Exception:
            self._telemetry.increment_current("embeddingFallbacks", 1)
            return []
