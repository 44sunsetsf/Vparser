"""AgentService: the agent-side use cases behind the RPC API — run, reuse, evidence search and revisions.
The gateway persists the video context before calling Run; follow-ups live in followup.py."""
import logging
import time

from ..checkpoint.service import AgentCheckpointService
from ..errors import VideoContextNotReadyError
from ..models.dto import (
    AgentFeedback,
    AgentPlan,
    AgentState,
    AnalysisMode,
    TaskStage,
    VideoContext,
    VideoEvidenceHit,
    VideoSegment,
)
from ..textutil import is_blank, trim
from ..retrieval.long_context import LongVideoContextService
from ..telemetry import AgentTelemetry
from .loop import AgentLoopService
from .modes import ModeRegistry

log = logging.getLogger("agent.service")


def reusable_context(target_source: str, source_context: VideoContext) -> VideoContext:
    """把源视频上下文改写到目标素材地址(证据帧改为 target#timestampMs=start)。"""
    return VideoContext(source=target_source, user_goal="", segments=[
        VideoSegment(
            start_ms=s.start_ms, end_ms=s.end_ms, transcript=s.transcript, ocr_texts=s.ocr_texts,
            evidence_frames=[] if not s.evidence_frames else [f"{target_source}#timestampMs={s.start_ms}"])
        for s in source_context.segments])


class AgentService:
    def __init__(self, agent_loop: AgentLoopService, long_context: LongVideoContextService,
                 checkpoint: AgentCheckpointService, telemetry: AgentTelemetry, modes: ModeRegistry) -> None:
        self._loop = agent_loop
        self._long_context = long_context
        self._checkpoint = checkpoint
        self._telemetry = telemetry
        self._modes = modes

    def run(self, media_id: int, goal: str, mode: AnalysisMode, trace_id: str | None = None,
            seed: dict | None = None) -> AgentState:
        """Runs the agent loop on the stored context; the gateway's telemetry seed joins the same trace."""
        trace_id = self._telemetry.start(media_id, goal, mode, trace_id=trace_id)
        self._telemetry.bind(trace_id)
        try:
            self._telemetry.apply_seed(trace_id, seed)
            saved = self._checkpoint.load_context(media_id)
            if saved is None:
                raise VideoContextNotReadyError()
            video_context = VideoContext(source=saved.source, user_goal=goal, segments=saved.segments)
            started = time.monotonic_ns()
            try:
                state = self._loop.run(media_id, video_context, self._modes.of(mode))
                self._telemetry.stage(trace_id, TaskStage.AGENT_LOOP.name, started, True)
            except Exception:
                self._telemetry.stage(trace_id, TaskStage.AGENT_LOOP.name, started, False)
                raise
            log.info("agent_analysis_completed traceId=%s mediaId=%s rounds=%s", trace_id, media_id, state.round)
            return state
        finally:
            self._telemetry.flush(trace_id)
            self._telemetry.clear()

    def search_evidence(self, media_id: int, query: str) -> list[VideoEvidenceHit]:
        context = self._checkpoint.load_context(media_id)
        if context is None:
            raise VideoContextNotReadyError()
        trace_id = self._telemetry.start(media_id, query)
        self._telemetry.bind(trace_id)
        started = time.monotonic_ns()
        try:
            search_context = VideoContext(source=context.source, user_goal=query, segments=context.segments)
            hits = self._long_context.search_evidence(media_id, search_context)
            self._telemetry.stage(trace_id, TaskStage.RETRIEVAL.name, started, True)
            return hits
        except Exception:
            self._telemetry.stage(trace_id, TaskStage.RETRIEVAL.name, started, False)
            raise
        finally:
            self._telemetry.flush(trace_id)
            self._telemetry.clear()

    def stage_revision(self, feedback: AgentFeedback, mode: AnalysisMode | None = AnalysisMode.GENERAL) -> None:
        resolved = AnalysisMode.GENERAL if mode is None else mode
        normalized = feedback.normalized(resolved)
        self._checkpoint.save_feedback(normalized)
        goal = normalized.goal if (normalized.corrected_goal is None or is_blank(normalized.corrected_goal)) \
            else trim(normalized.corrected_goal)
        corrected_plan = None if not normalized.corrected_tasks else AgentPlan(
            understood_goal=goal, tasks=normalized.corrected_tasks)
        self._checkpoint.stage_revision(normalized.media_id, goal, resolved, corrected_plan)

    @staticmethod
    def revision_goal(feedback: AgentFeedback) -> str:
        normalized = feedback.normalized()
        return normalized.goal if (normalized.corrected_goal is None or is_blank(normalized.corrected_goal)) \
            else normalized.corrected_goal

    def begin_staged_revision(self, media_id: int, goal: str, mode: AnalysisMode) -> bool:
        return self._checkpoint.begin_staged_revision(media_id, goal, mode)

    def complete_staged_revision(self, media_id: int, goal: str, mode: AnalysisMode) -> None:
        self._checkpoint.complete_staged_revision(media_id, goal, mode)

    def cancel_staged_revision(self, media_id: int, goal: str, mode: AnalysisMode) -> None:
        self._checkpoint.cancel_staged_revision(media_id, goal, mode)

    def reuse_result(self, media_id: int, source_media_id: int, goal: str, mode: AnalysisMode,
                     target_source: str) -> AgentState | None:
        """载入源 context + result,按目标素材地址改写后落到 target 检查点;源缺失返回 None。"""
        source_context = self._checkpoint.load_context(source_media_id)
        if source_context is None:
            return None
        state = self._checkpoint.load_result(source_media_id, goal, mode)
        if state is None or state.result is None:
            return None
        self._checkpoint.save_context(media_id, reusable_context(target_source, source_context))
        self._checkpoint.save_result(media_id, AgentState(
            goal=state.goal, plan=state.plan, result=state.result, critique=state.critique, round=state.round), mode)
        return state
