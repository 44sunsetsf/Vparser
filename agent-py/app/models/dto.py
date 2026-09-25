"""Domain DTOs. JSON form is camelCase with explicit nulls; field order is part of the stored checkpoint
format. Validators normalise input (trim strings, default missing lists) so every instance is canonical."""
from datetime import UTC, datetime
from enum import Enum
from typing import Annotated, Any

from pydantic import (
    BaseModel,
    BeforeValidator,
    ConfigDict,
    Field,
    field_serializer,
    model_validator,
)
from pydantic.alias_generators import to_camel

from ..errors import InvalidArgumentError
from .. import language
from ..textutil import is_blank, trim


class CamelModel(BaseModel):
    model_config = ConfigDict(alias_generator=to_camel, populate_by_name=True)

    def to_json(self) -> str:
        return self.model_dump_json(by_alias=True)


# ---------- 归一化辅助 ----------

def _trim_or(default: str):
    def fn(v: Any) -> Any:
        return default if v is None else (trim(v) if isinstance(v, str) else v)
    return fn


def _keep_or(default: str):
    def fn(v: Any) -> Any:
        return default if v is None else v
    return fn


def _list_or_empty(v: Any) -> Any:
    return [] if v is None else v


def _to_long(v: Any) -> Any:
    # missing/null -> 0; floats are truncated toward zero
    if v is None:
        return 0
    if isinstance(v, float) and v == v:
        return int(v)
    return v


TrimStr = Annotated[str, BeforeValidator(_trim_or(""))]
KeepStr = Annotated[str, BeforeValidator(_keep_or(""))]
def _as_text(v: Any) -> Any:
    """One list item as text. Models sometimes answer a list of strings with objects, e.g. a self-test item as
    {"question": ..., "answer": ...}; keep the content instead of failing the whole analysis."""
    if v is None or isinstance(v, str):
        return v
    if isinstance(v, bool):
        return str(v).lower()
    if isinstance(v, (int, float)):
        return str(v)
    if isinstance(v, dict):
        parts = [_as_text(x) for x in v.values()]
        return " — ".join(p for p in parts if isinstance(p, str) and p.strip())
    if isinstance(v, (list, tuple)):
        parts = [_as_text(x) for x in v]
        return "；".join(p for p in parts if isinstance(p, str) and p.strip())
    return v


def _text_list(v: Any) -> Any:
    v = _list_or_empty(v)
    return [_as_text(x) for x in v] if isinstance(v, list) else v


StrList = Annotated[list[str], BeforeValidator(_text_list)]
Int64 = Annotated[int, BeforeValidator(_to_long)]
LongList = Annotated[list[Int64], BeforeValidator(_list_or_empty)]


def format_instant(dt: datetime) -> str:
    """ISO-8601 UTC with the shortest of 0/3/6 fractional digits, e.g. 2026-01-02T03:04:05.120Z."""
    dt = dt.astimezone(UTC) if dt.tzinfo else dt.replace(tzinfo=UTC)
    base = dt.strftime("%Y-%m-%dT%H:%M:%S")
    us = dt.microsecond
    if us == 0:
        frac = ""
    elif us % 1000 == 0:
        frac = f".{us // 1000:03d}"
    else:
        frac = f".{us:06d}"
    return base + frac + "Z"


# ---------- 枚举 ----------

class AnalysisMode(str, Enum):
    GENERAL = "GENERAL"
    LEARNING = "LEARNING"
    REVIEW = "REVIEW"
    CREATION = "CREATION"

    @classmethod
    def from_nullable(cls, value: str | None) -> "AnalysisMode":
        if value is None or is_blank(value):
            return cls.GENERAL
        try:
            return cls(trim(value).upper())
        except ValueError:
            return cls.GENERAL

    @classmethod
    def from_request(cls, value: str | None) -> "AnalysisMode":
        if value is None or is_blank(value):
            return cls.GENERAL
        try:
            return cls(trim(value).upper())
        except ValueError:
            raise InvalidArgumentError(f"不支持的分析模式: {value}") from None


class TaskStage(str, Enum):
    QUEUED = "QUEUED"
    CONSUMING = "CONSUMING"
    VIDEO_CONTEXT = "VIDEO_CONTEXT"
    CONTEXT_COMPLETED = "CONTEXT_COMPLETED"
    CHUNKS_COMPLETED = "CHUNKS_COMPLETED"
    RETRIEVAL = "RETRIEVAL"
    AGENT_LOOP = "AGENT_LOOP"
    PLAN_COMPLETED = "PLAN_COMPLETED"
    EXECUTOR_STARTED = "EXECUTOR_STARTED"
    EXECUTOR_COMPLETED = "EXECUTOR_COMPLETED"
    CRITIC_STARTED = "CRITIC_STARTED"
    CRITIC_PASSED = "CRITIC_PASSED"
    CRITIC_RETRY_REQUIRED = "CRITIC_RETRY_REQUIRED"
    EVIDENCE_REFRESHED = "EVIDENCE_REFRESHED"
    ANALYSIS_COMPLETED = "ANALYSIS_COMPLETED"
    ANALYSIS_COMPLETED_WITH_WARNINGS = "ANALYSIS_COMPLETED_WITH_WARNINGS"
    BUDGET_EXHAUSTED = "BUDGET_EXHAUSTED"
    RETRYING = "RETRYING"
    COMPLETED = "COMPLETED"
    COMPLETED_REUSED = "COMPLETED_REUSED"
    FAILED = "FAILED"
    DEAD_LETTERED = "DEAD_LETTERED"
    MANUAL_REPLAY = "MANUAL_REPLAY"
    REVISION_PENDING = "REVISION_PENDING"
    REVISION_APPLIED = "REVISION_APPLIED"
    TRANSCRIPTION = "TRANSCRIPTION"
    ASR = "ASR"
    DISPATCH_FAILED = "DISPATCH_FAILED"

    @classmethod
    def from_(cls, value: str | None) -> "TaskStage | None":
        if value is None or is_blank(value):
            return None
        try:
            return cls(value)
        except ValueError:
            return None


class TaskState(str, Enum):
    NOT_STARTED = "NOT_STARTED"
    QUEUED = "QUEUED"
    PROCESSING = "PROCESSING"
    COMPLETED = "COMPLETED"
    FAILED = "FAILED"


# ---------- VideoContext ----------

class VideoSegment(CamelModel):
    start_ms: Int64 = 0
    end_ms: Int64 = 0
    transcript: TrimStr = Field(default=None, validate_default=True)
    ocr_texts: StrList = Field(default_factory=list)
    evidence_frames: StrList = Field(default_factory=list)

    @model_validator(mode="after")
    def _check(self) -> "VideoSegment":
        if self.start_ms < 0 or self.end_ms <= self.start_ms:
            raise InvalidArgumentError("invalid segment range")
        return self


class VideoContext(CamelModel):
    source: str = Field(default=None, validate_default=True)
    user_goal: TrimStr = Field(default=None, validate_default=True)
    segments: Annotated[list[VideoSegment], BeforeValidator(_list_or_empty)] = Field(default_factory=list)

    @model_validator(mode="before")
    @classmethod
    def _check_source(cls, data: Any) -> Any:
        src = data.get("source") if isinstance(data, dict) else getattr(data, "source", None)
        if src is None or (isinstance(src, str) and is_blank(src)):
            raise InvalidArgumentError("video source is required")
        return data

    def transcript_text(self) -> str:
        return "\n".join(s.transcript for s in self.segments if not is_blank(s.transcript))


class VideoChunk(CamelModel):
    start_time: Int64 = 0
    end_time: Int64 = 0
    segment_summary: TrimStr = Field(default=None, validate_default=True)
    keywords: StrList = Field(default_factory=list)
    raw_segments: Annotated[list[VideoSegment], BeforeValidator(_list_or_empty)] = Field(default_factory=list)
    embedding: Annotated[list[float], BeforeValidator(_list_or_empty)] = Field(default_factory=list)

    @model_validator(mode="after")
    def _check(self) -> "VideoChunk":
        if self.start_time < 0 or self.end_time <= self.start_time:
            raise InvalidArgumentError("invalid chunk range")
        return self

    # Convenience aliases; not serialised.
    @property
    def start_ms(self) -> int:
        return self.start_time

    @property
    def end_ms(self) -> int:
        return self.end_time


class ChunkSummary(CamelModel):
    segment_summary: TrimStr = Field(default=None, validate_default=True)
    keywords: StrList = Field(default_factory=list)


class VideoEvidenceHit(CamelModel):
    start_ms: Int64 = 0
    end_ms: Int64 = 0
    source: KeepStr = Field(default=None, validate_default=True)
    snippet: KeepStr = Field(default=None, validate_default=True)
    transcript: KeepStr = Field(default=None, validate_default=True)
    ocr_texts: StrList = Field(default_factory=list)


class VideoRetrievalIntent(CamelModel):
    semantic_query: TrimStr = Field(default=None, validate_default=True)
    keywords: list[str] = Field(default=None, validate_default=True)
    visual_keywords: list[str] = Field(default=None, validate_default=True)

    @staticmethod
    def _normalize_terms(terms: Any) -> list[str]:
        if terms is None:
            return []
        out: list[str] = []
        for t in terms:
            if t is None:
                continue
            t = trim(t)
            if is_blank(t) or t in out:
                continue
            out.append(t)
            if len(out) >= 16:
                break
        return out

    @model_validator(mode="before")
    @classmethod
    def _norm(cls, data: Any) -> Any:
        if isinstance(data, dict):
            data = dict(data)
            for key, alias in (("keywords", "keywords"), ("visual_keywords", "visualKeywords")):
                name = alias if alias in data else key
                data[name] = cls._normalize_terms(data.get(name))
        return data


# ---------- AnalysisResult ----------

class Evidence(CamelModel):
    timestamp_ms: Int64 = 0
    source: TrimStr = Field(default=None, validate_default=True)
    content: TrimStr = Field(default=None, validate_default=True)
    claim: TrimStr = Field(default=None, validate_default=True)

    @model_validator(mode="before")
    @classmethod
    def _defaults(cls, data: Any) -> Any:
        if isinstance(data, dict):
            data = dict(data)
            if data.get("source") is None:
                data["source"] = "UNKNOWN"
        return data

    @model_validator(mode="after")
    def _check(self) -> "Evidence":
        if self.timestamp_ms < 0:
            raise InvalidArgumentError("evidence timestamp cannot be negative")
        return self


class Section(CamelModel):
    key: TrimStr = Field(default=None, validate_default=True)
    title: TrimStr = Field(default=None, validate_default=True)
    items: StrList = Field(default_factory=list)


def _format_time(timestamp_ms: int) -> str:
    seconds = timestamp_ms // 1000
    return f"{seconds // 60:02d}:{seconds % 60:02d}"


class AnalysisResult(CamelModel):
    title: Annotated[str, BeforeValidator(_trim_or("未命名分析"))] = Field(default=None, validate_default=True)
    conclusions: StrList = Field(default_factory=list)
    evidence: Annotated[list[Evidence], BeforeValidator(_list_or_empty)] = Field(default_factory=list)
    suggestions: StrList = Field(default_factory=list)
    sections: Annotated[list[Section], BeforeValidator(_list_or_empty)] = Field(default_factory=list)

    def to_markdown(self, lang: str = "zh") -> str:
        """Headings follow the report language (see app.language); Chinese output is unchanged."""
        words = language.labels(lang)
        out = ["## ", self.title, f"\n\n## {words['conclusions']}\n"]
        for item in self.conclusions:
            out.append(f"- {item}\n")
        out.append(f"\n## {words['evidence']}\n")
        for e in self.evidence:
            out.append(f"- [{_format_time(e.timestamp_ms)}] {e.source}{words['colon']}{e.content}\n")
        out.append(f"\n## {words['suggestions']}\n")
        for item in self.suggestions:
            out.append(f"- {item}\n")
        for section in self.sections:
            out.append(f"\n## {section.title}\n")
            for item in section.items:
                out.append(f"- {item}\n")
        return "".join(out)


# ---------- AgentState ----------

class AgentPlan(CamelModel):
    understood_goal: TrimStr = Field(default=None, validate_default=True)
    tasks: StrList = Field(default_factory=list)


class CriticResult(CamelModel):
    passed: bool = False
    feedback: StrList = Field(default_factory=list)
    missing_requirements: StrList = Field(default_factory=list)
    unsupported_claims: StrList = Field(default_factory=list)
    required_timestamps: LongList = Field(default_factory=list)


class AgentState(CamelModel):
    goal: str = Field(default=None, validate_default=True)
    plan: AgentPlan | None = None
    result: AnalysisResult | None = None
    critique: CriticResult | None = None
    round: Int64 = 0

    @model_validator(mode="before")
    @classmethod
    def _check_goal(cls, data: Any) -> Any:
        goal = data.get("goal") if isinstance(data, dict) else getattr(data, "goal", None)
        if goal is None or (isinstance(goal, str) and is_blank(goal)):
            raise InvalidArgumentError("agent goal is required")
        return data

    @model_validator(mode="after")
    def _normalize(self) -> "AgentState":
        if self.round < 0:
            raise InvalidArgumentError("agent round cannot be negative")
        object.__setattr__(self, "goal", trim(self.goal))
        return self


# ---------- 路由 / 反馈 / 状态 ----------

class RouteDecision(CamelModel):
    mode: AnalysisMode = Field(default=None, validate_default=True)
    reason: str = Field(default=None, validate_default=True)

    @model_validator(mode="before")
    @classmethod
    def _defaults(cls, data: Any) -> Any:
        if isinstance(data, dict):
            data = dict(data)
            if data.get("mode") is None:
                data["mode"] = AnalysisMode.GENERAL
            r = data.get("reason")
            if r is None or is_blank(r):
                data["reason"] = "已按通用模式分析"
        return data


class ModeClassification(CamelModel):
    mode: str | None = None
    reason: str | None = None


class AgentFeedback(CamelModel):
    media_id: int | None = None
    goal: str | None = None
    mode: str | None = None
    rating: int | None = None
    error_type: str | None = None
    comment: str | None = None
    corrected_goal: str | None = None
    corrected_tasks: list[str | None] | None = None
    evidence_timestamp: int | None = None
    evidence_accepted: bool | None = None
    created_at: datetime | None = None

    @field_serializer("created_at")
    def _ser_created_at(self, v: datetime | None) -> str | None:
        return None if v is None else format_instant(v)

    def normalized(self, analysis_mode: AnalysisMode | None = None) -> "AgentFeedback":
        mode = analysis_mode if analysis_mode is not None else AnalysisMode.from_nullable(self.mode)
        return AgentFeedback(
            media_id=self.media_id,
            goal=None if self.goal is None else trim(self.goal),
            mode=(mode or AnalysisMode.GENERAL).name,
            rating=self.rating,
            error_type=None if self.error_type is None else trim(self.error_type),
            comment=None if self.comment is None else trim(self.comment),
            corrected_goal=None if self.corrected_goal is None else trim(self.corrected_goal),
            corrected_tasks=[] if self.corrected_tasks is None else [
                trim(t) for t in self.corrected_tasks if t is not None and not is_blank(t)],
            evidence_timestamp=self.evidence_timestamp,
            evidence_accepted=self.evidence_accepted,
            created_at=self.created_at if self.created_at is not None else datetime.now(UTC),
        )


class TaskStatus(CamelModel):
    state: TaskState
    result: str | None = None
    message: str | None = None

    @staticmethod
    def of(state: TaskState, message: str) -> "TaskStatus":
        return TaskStatus(state=state, result=None, message=message)

    @staticmethod
    def completed(result: str) -> "TaskStatus":
        return TaskStatus(state=TaskState.COMPLETED, result=result, message="任务完成")

    @staticmethod
    def completed_from_state(agent_state: AgentState) -> "TaskStatus":
        lang = language.detect(agent_state.goal)
        words = language.labels(lang)
        markdown = agent_state.result.to_markdown(lang)
        if agent_state.critique is not None and agent_state.critique.passed:
            return TaskStatus.completed(markdown)
        warning = words["warning"]
        return TaskStatus(
            state=TaskState.COMPLETED,
            result=f"> **{words['note']}{words['colon'].strip() or ':'}** " + warning + "\n\n" + markdown,
            message=warning,
        )


class TaskEvent(CamelModel):
    state: TaskState
    result: str | None = None
    message: str | None = None
    stage: TaskStage | None = None

    @staticmethod
    def of(status: TaskStatus, stage: TaskStage | None) -> "TaskEvent":
        return TaskEvent(state=status.state, result=status.result, message=status.message, stage=stage)

    def terminal(self) -> bool:
        return self.state in (TaskState.COMPLETED, TaskState.FAILED)
