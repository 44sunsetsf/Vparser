"""Lightweight follow-up: retrieval + one grounded model call, no Planner / Critic loop.

The answer is Markdown that cites ``[mm:ss]`` / ``[h:mm:ss]`` timestamps (the web client turns them into
seek links). Model output is untrusted, so every citation is checked in code against the segments that
were actually retrieved; a citation outside them is un-linked and flagged instead of being shown as proof.
"""
import json
import logging
import re
import time
from dataclasses import dataclass

from ..checkpoint.service import AgentCheckpointService
from ..errors import InvalidArgumentError, VideoContextNotReadyError
from ..llm import prompts as P
from ..llm.deepseek import DeepSeekClient
from ..models.dto import AnalysisMode, TaskStage, VideoContext, VideoSegment
from ..observability import span
from ..retrieval.long_context import LongVideoContextService
from ..telemetry import AgentTelemetry
from ..textutil import is_blank, trim

log = logging.getLogger("agent.followup")

MAX_PREVIOUS_CHARS = 4_000
MAX_QUESTION_CHARS = 2_000
UNVERIFIED_NOTE = "> 注：部分时间戳无法在检索到的视频片段中核验，已取消跳转链接。"

# [mm:ss] or [h:mm:ss], optionally already linked as [mm:ss](#video-t=123).
_CITATION = re.compile(r"\[((?:\d{1,2}:)?\d{1,2}:\d{2})\](\(#video-t=\d+\))?")


def format_timestamp(ms: int) -> str:
    seconds = max(0, ms) // 1000
    hours, rest = divmod(seconds, 3600)
    if hours:
        return f"{hours}:{rest // 60:02d}:{rest % 60:02d}"
    return f"{rest // 60:02d}:{rest % 60:02d}"


def parse_timestamp(text: str) -> int:
    """'01:05' -> 65, '1:02:03' -> 3723 (seconds)."""
    parts = [int(p) for p in text.split(":")]
    if len(parts) == 3:
        return parts[0] * 3600 + parts[1] * 60 + parts[2]
    return parts[0] * 60 + parts[1]


def _covers(segment: VideoSegment, second: int) -> bool:
    # Citations have one-second resolution: accept any second the half-open [start, end) range touches.
    return segment.start_ms // 1000 <= second <= (segment.end_ms - 1) // 1000


@dataclass
class CitationCheck:
    answer: str
    verified: int
    rejected: int


def verify_citations(answer: str, segments: list[VideoSegment]) -> CitationCheck:
    """Keeps citations that fall inside a retrieved segment (normalised to plain ``[mm:ss]``) and
    un-links the rest as ``mm:ss（未核验）``; appends a note when anything was rejected."""
    verified = rejected = 0

    def replace(match: re.Match) -> str:
        nonlocal verified, rejected
        stamp = match.group(1)
        if any(_covers(s, parse_timestamp(stamp)) for s in segments):
            verified += 1
            return f"[{stamp}]"
        rejected += 1
        return f"{stamp}（未核验）"

    checked = _CITATION.sub(replace, answer)
    if rejected:
        checked = checked.rstrip() + "\n\n" + UNVERIFIED_NOTE
    return CitationCheck(checked, verified, rejected)


def _segments_json(segments: list[VideoSegment]) -> str:
    """Segments with human-readable bounds, so the model cites in the same format it reads."""
    return json.dumps([{"start": format_timestamp(s.start_ms), "end": format_timestamp(s.end_ms),
                        "transcript": s.transcript, "ocrTexts": s.ocr_texts} for s in segments],
                      ensure_ascii=False, separators=(",", ":"))


def build_prompt(question: str, segments: list[VideoSegment], goal: str | None = None,
                 previous_summary: str | None = None) -> str:
    prompt = P.FOLLOW_UP_HEAD + question
    if goal is not None and previous_summary is not None:
        prompt += P.FOLLOW_UP_GOAL + goal + P.FOLLOW_UP_PREVIOUS + previous_summary[:MAX_PREVIOUS_CHARS]
    return prompt + P.FOLLOW_UP_CONTEXT + _segments_json(segments)


class FollowUpService:
    def __init__(self, deepseek: DeepSeekClient, long_context: LongVideoContextService,
                 checkpoint: AgentCheckpointService, telemetry: AgentTelemetry) -> None:
        self._deepseek = deepseek
        self._long_context = long_context
        self._checkpoint = checkpoint
        self._telemetry = telemetry

    def answer(self, media_id: int, goal: str | None, question: str, mode: AnalysisMode) -> str:
        if question is None or is_blank(question):
            raise InvalidArgumentError("追问内容不能为空")
        question = trim(question)
        if len(question) > MAX_QUESTION_CHARS:
            raise InvalidArgumentError(f"追问内容不能超过 {MAX_QUESTION_CHARS} 字")
        context = self._checkpoint.load_context(media_id)
        if context is None:
            raise VideoContextNotReadyError()
        goal = None if goal is None or is_blank(goal) else trim(goal)

        trace_id = self._telemetry.start(media_id, question, mode)
        self._telemetry.bind(trace_id)
        try:
            segments = self._retrieve(trace_id, media_id, context, question)
            previous = None
            if goal is not None:
                state = self._checkpoint.load_result(media_id, goal, mode)
                previous = None if state is None or state.result is None else state.result.to_markdown()
            prompt = build_prompt(question, segments, goal, previous)
            with span("agent.follow_up", **{"agent.stage": "FOLLOW_UP", "agent.segments": len(segments)}) as s:
                raw = self._deepseek.answer_follow_up(prompt)
                checked = verify_citations(trim(raw), segments)
                s.set_attribute("agent.citations_verified", checked.verified)
                s.set_attribute("agent.citations_rejected", checked.rejected)
            self._telemetry.increment(trace_id, "followUpCitationsVerified", checked.verified)
            self._telemetry.increment(trace_id, "followUpCitationsRejected", checked.rejected)
            if checked.rejected:
                log.info("follow_up_citations_rejected traceId=%s mediaId=%s count=%s",
                         trace_id, media_id, checked.rejected)
            return checked.answer
        finally:
            self._telemetry.flush(trace_id)
            self._telemetry.clear()

    def _retrieve(self, trace_id: str, media_id: int, context: VideoContext, question: str) -> list[VideoSegment]:
        started = time.monotonic_ns()
        try:
            query = VideoContext(source=context.source, user_goal=question, segments=context.segments)
            segments = self._long_context.select_relevant(media_id, query).segments
            self._telemetry.stage(trace_id, TaskStage.RETRIEVAL.name, started, True)
            return segments
        except Exception:
            self._telemetry.stage(trace_id, TaskStage.RETRIEVAL.name, started, False)
            raise
