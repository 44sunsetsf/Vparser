"""The single place where protobuf messages and domain models meet.

Conventions: proto3 scalars cannot be null, so an empty string / zero means "absent" for plain fields;
fields declared ``optional`` in the proto map to ``None`` via HasField; ANALYSIS_MODE_UNSPECIFIED means
GENERAL; unknown enum numbers are rejected as INVALID_ARGUMENT.
"""
from datetime import datetime

from google.protobuf import struct_pb2

from ..errors import InvalidArgumentError
from ..gen.agent.v1 import agent_pb2 as pb
from ..models.dto import (
    AgentFeedback,
    AgentPlan,
    AgentState,
    AnalysisMode,
    AnalysisResult,
    CriticResult,
    Evidence,
    RouteDecision,
    Section,
    TaskStatus,
    VideoEvidenceHit,
    format_instant,
)

_FEEDBACK_OPTIONAL = ("rating", "error_type", "comment", "corrected_goal", "evidence_timestamp",
                      "evidence_accepted")


# ---------- enums ----------
def mode_from_proto(value: int) -> AnalysisMode:
    if value == pb.ANALYSIS_MODE_UNSPECIFIED:
        return AnalysisMode.GENERAL
    try:
        return AnalysisMode(pb.AnalysisMode.Name(value))
    except ValueError:
        raise InvalidArgumentError(f"不支持的分析模式: {value}") from None


def mode_to_proto(mode: AnalysisMode | str | None) -> int:
    if mode is None:
        return pb.GENERAL
    return pb.AnalysisMode.Value(mode.value if isinstance(mode, AnalysisMode) else AnalysisMode(mode).value)


def media_id(value: int, field: str = "media_id") -> int:
    if value <= 0:
        raise InvalidArgumentError(f"{field} is required")
    return value


# ---------- agent state ----------
def plan_to_proto(plan: AgentPlan) -> pb.AgentPlan:
    return pb.AgentPlan(understood_goal=plan.understood_goal, tasks=plan.tasks)


def plan_from_proto(msg: pb.AgentPlan) -> AgentPlan:
    return AgentPlan(understood_goal=msg.understood_goal, tasks=list(msg.tasks))


def result_to_proto(result: AnalysisResult) -> pb.AnalysisResult:
    return pb.AnalysisResult(
        title=result.title, conclusions=result.conclusions,
        evidence=[pb.Evidence(timestamp_ms=e.timestamp_ms, source=e.source, content=e.content, claim=e.claim)
                  for e in result.evidence],
        suggestions=result.suggestions,
        sections=[pb.Section(key=s.key, title=s.title, items=s.items) for s in result.sections])


def result_from_proto(msg: pb.AnalysisResult) -> AnalysisResult:
    return AnalysisResult(
        title=msg.title, conclusions=list(msg.conclusions),
        evidence=[Evidence(timestamp_ms=e.timestamp_ms, source=e.source, content=e.content, claim=e.claim)
                  for e in msg.evidence],
        suggestions=list(msg.suggestions),
        sections=[Section(key=s.key, title=s.title, items=list(s.items)) for s in msg.sections])


def critique_to_proto(critique: CriticResult) -> pb.CriticResult:
    return pb.CriticResult(passed=critique.passed, feedback=critique.feedback,
                           missing_requirements=critique.missing_requirements,
                           unsupported_claims=critique.unsupported_claims,
                           required_timestamps=critique.required_timestamps)


def critique_from_proto(msg: pb.CriticResult) -> CriticResult:
    return CriticResult(passed=msg.passed, feedback=list(msg.feedback),
                        missing_requirements=list(msg.missing_requirements),
                        unsupported_claims=list(msg.unsupported_claims),
                        required_timestamps=list(msg.required_timestamps))


def state_to_proto(state: AgentState) -> pb.AgentState:
    msg = pb.AgentState(goal=state.goal, round=state.round)
    if state.plan is not None:
        msg.plan.CopyFrom(plan_to_proto(state.plan))
    if state.result is not None:
        msg.result.CopyFrom(result_to_proto(state.result))
    if state.critique is not None:
        msg.critique.CopyFrom(critique_to_proto(state.critique))
    return msg


def state_from_proto(msg: pb.AgentState) -> AgentState:
    return AgentState(
        goal=msg.goal, round=msg.round,
        plan=plan_from_proto(msg.plan) if msg.HasField("plan") else None,
        result=result_from_proto(msg.result) if msg.HasField("result") else None,
        critique=critique_from_proto(msg.critique) if msg.HasField("critique") else None)


def task_status_to_proto(status: TaskStatus) -> pb.TaskStatus:
    return pb.TaskStatus(state=status.state.value, result=status.result or "", message=status.message or "")


# ---------- retrieval / routing ----------
def hit_to_proto(hit: VideoEvidenceHit) -> pb.VideoEvidenceHit:
    return pb.VideoEvidenceHit(start_ms=hit.start_ms, end_ms=hit.end_ms, source=hit.source,
                               snippet=hit.snippet, transcript=hit.transcript, ocr_texts=hit.ocr_texts)


def route_to_proto(decision: RouteDecision) -> pb.RouteDecision:
    return pb.RouteDecision(mode=mode_to_proto(decision.mode), reason=decision.reason)


# ---------- feedback ----------
def feedback_from_proto(msg: pb.AgentFeedback) -> AgentFeedback:
    values = {name: getattr(msg, name) if msg.HasField(name) else None for name in _FEEDBACK_OPTIONAL}
    created_at = None
    if msg.created_at:
        try:
            created_at = datetime.fromisoformat(msg.created_at)
        except ValueError:
            raise InvalidArgumentError("created_at must be ISO-8601") from None
    return AgentFeedback(media_id=msg.media_id or None, goal=msg.goal or None,
                         mode=mode_from_proto(msg.mode).value, corrected_tasks=list(msg.corrected_tasks),
                         created_at=created_at, **values)


def feedback_to_proto(feedback: AgentFeedback) -> pb.AgentFeedback:
    msg = pb.AgentFeedback(media_id=feedback.media_id or 0, goal=feedback.goal or "",
                           mode=mode_to_proto(AnalysisMode.from_nullable(feedback.mode)),
                           corrected_tasks=[t for t in feedback.corrected_tasks or [] if t is not None],
                           created_at="" if feedback.created_at is None else format_instant(feedback.created_at))
    for name in _FEEDBACK_OPTIONAL:
        value = getattr(feedback, name)
        if value is not None:
            setattr(msg, name, value)
    return msg


# ---------- telemetry seed / free-form JSON ----------
def seed_from_proto(request: pb.RunRequest) -> dict | None:
    """Gateway telemetry seed in the shape AgentTelemetry.apply_seed consumes."""
    if not request.HasField("telemetry_seed"):
        return None
    seed = request.telemetry_seed
    return {
        "traceId": seed.trace_id or None,
        "stages": {name: {"durationMs": t.duration_ms, "success": t.success} for name, t in seed.stages.items()},
        "counters": dict(seed.counters),
    }


def to_struct(value: dict | None) -> struct_pb2.Struct:
    """JSON object -> Struct. Numbers become doubles, as in any JSON transport."""
    struct = struct_pb2.Struct()
    if value:
        struct.update(value)
    return struct
