"""LongVideoContextService: chunked retrieval for long videos, trimmed to a 24k-character context budget."""
from ..checkpoint.service import AgentCheckpointService
from ..models.dto import CriticResult, VideoChunk, VideoContext, VideoEvidenceHit, VideoSegment
from ..telemetry import AgentTelemetry
from .chunking import VideoChunkingService
from .evidence_retrieval import VideoEvidenceRetrievalService

CHUNK_MS = 5 * 60 * 1000
MAX_CONTEXT_CHARS = 24_000


def _segment_key(segment: VideoSegment) -> str:
    return f"{segment.start_ms}:{segment.end_ms}"


def _near_segment(timestamp: int, segment: VideoSegment) -> bool:
    margin = max(60_000, segment.end_ms - segment.start_ms)
    return max(0, segment.start_ms - margin) <= timestamp < segment.end_ms + margin


class LongVideoContextService:
    def __init__(self, telemetry: AgentTelemetry, checkpoint: AgentCheckpointService,
                 chunking: VideoChunkingService, retrieval: VideoEvidenceRetrievalService) -> None:
        self._telemetry = telemetry
        self._checkpoint = checkpoint
        self._chunking = chunking
        self._retrieval = retrieval

    def select_relevant(self, media_id: int | None, context: VideoContext) -> VideoContext:
        if not context.segments or context.segments[-1].end_ms <= CHUNK_MS:
            return self._within_budget(context, context.segments)
        chunks = self._resolve_chunks(media_id, context.segments)
        selected = self._retrieval.retrieve(media_id, context.user_goal, chunks)
        return self._within_budget(context, selected)

    def search_evidence(self, media_id: int, context: VideoContext) -> list[VideoEvidenceHit]:
        if not context.segments:
            return []
        chunks = self._resolve_chunks(media_id, context.segments)
        return self._retrieval.search(media_id, context.user_goal, chunks)

    def refine_for_critique(self, media_id: int | None, full_context: VideoContext,
                            selected_context: VideoContext, critique: CriticResult | None) -> VideoContext:
        segments: dict[str, VideoSegment] = {}
        required = [] if critique is None else critique.required_timestamps
        for segment in full_context.segments:
            if any(_near_segment(ts, segment) for ts in required):
                segments[_segment_key(segment)] = segment

        query = self._critique_query(full_context.user_goal, critique)
        retry_context = self.select_relevant(
            media_id, VideoContext(source=full_context.source, user_goal=query, segments=full_context.segments))
        for segment in retry_context.segments:
            segments.setdefault(_segment_key(segment), segment)
        for segment in selected_context.segments:
            segments.setdefault(_segment_key(segment), segment)
        return self._within_budget(full_context, list(segments.values()))

    @staticmethod
    def _critique_query(goal: str, critique: CriticResult | None) -> str:
        if critique is None:
            return goal
        return "\n".join([goal, " ".join(critique.feedback or []),
                          " ".join(critique.missing_requirements or []),
                          " ".join(critique.unsupported_claims or [])])

    def _within_budget(self, context: VideoContext, candidates: list[VideoSegment]) -> VideoContext:
        selected: list[VideoSegment] = []
        used_chars = 0
        for segment in candidates:
            segment_chars = len(segment.transcript) + sum(len(t) for t in segment.ocr_texts)
            if selected and used_chars + segment_chars > MAX_CONTEXT_CHARS:
                continue
            selected.append(segment)
            used_chars += segment_chars
        self._telemetry.increment_current("contextSegmentsDropped", len(candidates) - len(selected))
        self._telemetry.value_current("contextChars", used_chars)
        selected.sort(key=lambda s: s.start_ms)
        return VideoContext(source=context.source, user_goal=context.user_goal, segments=selected)

    def _resolve_chunks(self, media_id: int | None, segments: list[VideoSegment]) -> list[VideoChunk]:
        chunks = None if media_id is None else self._checkpoint.load_chunks(media_id)
        if chunks:
            self._telemetry.increment_current("chunkCheckpointHits", 1)
            return chunks
        chunks = self._chunking.build(segments)
        if media_id is not None:
            self._checkpoint.save_chunks(media_id, chunks)
            self._retrieval.index(media_id, chunks)
        return chunks
