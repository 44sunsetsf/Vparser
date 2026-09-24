from google.protobuf import struct_pb2 as _struct_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class AnalysisMode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ANALYSIS_MODE_UNSPECIFIED: _ClassVar[AnalysisMode]
    GENERAL: _ClassVar[AnalysisMode]
    LEARNING: _ClassVar[AnalysisMode]
    REVIEW: _ClassVar[AnalysisMode]
    CREATION: _ClassVar[AnalysisMode]
ANALYSIS_MODE_UNSPECIFIED: AnalysisMode
GENERAL: AnalysisMode
LEARNING: AnalysisMode
REVIEW: AnalysisMode
CREATION: AnalysisMode

class Empty(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class MediaRef(_message.Message):
    __slots__ = ("media_id",)
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    def __init__(self, media_id: _Optional[int] = ...) -> None: ...

class GoalRef(_message.Message):
    __slots__ = ("media_id", "goal", "mode")
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    GOAL_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    goal: str
    mode: AnalysisMode
    def __init__(self, media_id: _Optional[int] = ..., goal: _Optional[str] = ..., mode: _Optional[_Union[AnalysisMode, str]] = ...) -> None: ...

class AgentPlan(_message.Message):
    __slots__ = ("understood_goal", "tasks")
    UNDERSTOOD_GOAL_FIELD_NUMBER: _ClassVar[int]
    TASKS_FIELD_NUMBER: _ClassVar[int]
    understood_goal: str
    tasks: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, understood_goal: _Optional[str] = ..., tasks: _Optional[_Iterable[str]] = ...) -> None: ...

class Evidence(_message.Message):
    __slots__ = ("timestamp_ms", "source", "content", "claim")
    TIMESTAMP_MS_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    CONTENT_FIELD_NUMBER: _ClassVar[int]
    CLAIM_FIELD_NUMBER: _ClassVar[int]
    timestamp_ms: int
    source: str
    content: str
    claim: str
    def __init__(self, timestamp_ms: _Optional[int] = ..., source: _Optional[str] = ..., content: _Optional[str] = ..., claim: _Optional[str] = ...) -> None: ...

class Section(_message.Message):
    __slots__ = ("key", "title", "items")
    KEY_FIELD_NUMBER: _ClassVar[int]
    TITLE_FIELD_NUMBER: _ClassVar[int]
    ITEMS_FIELD_NUMBER: _ClassVar[int]
    key: str
    title: str
    items: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, key: _Optional[str] = ..., title: _Optional[str] = ..., items: _Optional[_Iterable[str]] = ...) -> None: ...

class AnalysisResult(_message.Message):
    __slots__ = ("title", "conclusions", "evidence", "suggestions", "sections")
    TITLE_FIELD_NUMBER: _ClassVar[int]
    CONCLUSIONS_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_FIELD_NUMBER: _ClassVar[int]
    SUGGESTIONS_FIELD_NUMBER: _ClassVar[int]
    SECTIONS_FIELD_NUMBER: _ClassVar[int]
    title: str
    conclusions: _containers.RepeatedScalarFieldContainer[str]
    evidence: _containers.RepeatedCompositeFieldContainer[Evidence]
    suggestions: _containers.RepeatedScalarFieldContainer[str]
    sections: _containers.RepeatedCompositeFieldContainer[Section]
    def __init__(self, title: _Optional[str] = ..., conclusions: _Optional[_Iterable[str]] = ..., evidence: _Optional[_Iterable[_Union[Evidence, _Mapping]]] = ..., suggestions: _Optional[_Iterable[str]] = ..., sections: _Optional[_Iterable[_Union[Section, _Mapping]]] = ...) -> None: ...

class CriticResult(_message.Message):
    __slots__ = ("passed", "feedback", "missing_requirements", "unsupported_claims", "required_timestamps")
    PASSED_FIELD_NUMBER: _ClassVar[int]
    FEEDBACK_FIELD_NUMBER: _ClassVar[int]
    MISSING_REQUIREMENTS_FIELD_NUMBER: _ClassVar[int]
    UNSUPPORTED_CLAIMS_FIELD_NUMBER: _ClassVar[int]
    REQUIRED_TIMESTAMPS_FIELD_NUMBER: _ClassVar[int]
    passed: bool
    feedback: _containers.RepeatedScalarFieldContainer[str]
    missing_requirements: _containers.RepeatedScalarFieldContainer[str]
    unsupported_claims: _containers.RepeatedScalarFieldContainer[str]
    required_timestamps: _containers.RepeatedScalarFieldContainer[int]
    def __init__(self, passed: _Optional[bool] = ..., feedback: _Optional[_Iterable[str]] = ..., missing_requirements: _Optional[_Iterable[str]] = ..., unsupported_claims: _Optional[_Iterable[str]] = ..., required_timestamps: _Optional[_Iterable[int]] = ...) -> None: ...

class AgentState(_message.Message):
    __slots__ = ("goal", "plan", "result", "critique", "round")
    GOAL_FIELD_NUMBER: _ClassVar[int]
    PLAN_FIELD_NUMBER: _ClassVar[int]
    RESULT_FIELD_NUMBER: _ClassVar[int]
    CRITIQUE_FIELD_NUMBER: _ClassVar[int]
    ROUND_FIELD_NUMBER: _ClassVar[int]
    goal: str
    plan: AgentPlan
    result: AnalysisResult
    critique: CriticResult
    round: int
    def __init__(self, goal: _Optional[str] = ..., plan: _Optional[_Union[AgentPlan, _Mapping]] = ..., result: _Optional[_Union[AnalysisResult, _Mapping]] = ..., critique: _Optional[_Union[CriticResult, _Mapping]] = ..., round: _Optional[int] = ...) -> None: ...

class TaskStatus(_message.Message):
    __slots__ = ("state", "result", "message")
    STATE_FIELD_NUMBER: _ClassVar[int]
    RESULT_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    state: str
    result: str
    message: str
    def __init__(self, state: _Optional[str] = ..., result: _Optional[str] = ..., message: _Optional[str] = ...) -> None: ...

class VideoEvidenceHit(_message.Message):
    __slots__ = ("start_ms", "end_ms", "source", "snippet", "transcript", "ocr_texts")
    START_MS_FIELD_NUMBER: _ClassVar[int]
    END_MS_FIELD_NUMBER: _ClassVar[int]
    SOURCE_FIELD_NUMBER: _ClassVar[int]
    SNIPPET_FIELD_NUMBER: _ClassVar[int]
    TRANSCRIPT_FIELD_NUMBER: _ClassVar[int]
    OCR_TEXTS_FIELD_NUMBER: _ClassVar[int]
    start_ms: int
    end_ms: int
    source: str
    snippet: str
    transcript: str
    ocr_texts: _containers.RepeatedScalarFieldContainer[str]
    def __init__(self, start_ms: _Optional[int] = ..., end_ms: _Optional[int] = ..., source: _Optional[str] = ..., snippet: _Optional[str] = ..., transcript: _Optional[str] = ..., ocr_texts: _Optional[_Iterable[str]] = ...) -> None: ...

class RouteDecision(_message.Message):
    __slots__ = ("mode", "reason")
    MODE_FIELD_NUMBER: _ClassVar[int]
    REASON_FIELD_NUMBER: _ClassVar[int]
    mode: AnalysisMode
    reason: str
    def __init__(self, mode: _Optional[_Union[AnalysisMode, str]] = ..., reason: _Optional[str] = ...) -> None: ...

class AgentFeedback(_message.Message):
    __slots__ = ("media_id", "goal", "mode", "rating", "error_type", "comment", "corrected_goal", "corrected_tasks", "evidence_timestamp", "evidence_accepted", "created_at")
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    GOAL_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    RATING_FIELD_NUMBER: _ClassVar[int]
    ERROR_TYPE_FIELD_NUMBER: _ClassVar[int]
    COMMENT_FIELD_NUMBER: _ClassVar[int]
    CORRECTED_GOAL_FIELD_NUMBER: _ClassVar[int]
    CORRECTED_TASKS_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_TIMESTAMP_FIELD_NUMBER: _ClassVar[int]
    EVIDENCE_ACCEPTED_FIELD_NUMBER: _ClassVar[int]
    CREATED_AT_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    goal: str
    mode: AnalysisMode
    rating: int
    error_type: str
    comment: str
    corrected_goal: str
    corrected_tasks: _containers.RepeatedScalarFieldContainer[str]
    evidence_timestamp: int
    evidence_accepted: bool
    created_at: str
    def __init__(self, media_id: _Optional[int] = ..., goal: _Optional[str] = ..., mode: _Optional[_Union[AnalysisMode, str]] = ..., rating: _Optional[int] = ..., error_type: _Optional[str] = ..., comment: _Optional[str] = ..., corrected_goal: _Optional[str] = ..., corrected_tasks: _Optional[_Iterable[str]] = ..., evidence_timestamp: _Optional[int] = ..., evidence_accepted: _Optional[bool] = ..., created_at: _Optional[str] = ...) -> None: ...

class TelemetrySeed(_message.Message):
    __slots__ = ("trace_id", "stages", "counters")
    class StagesEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: StageTiming
        def __init__(self, key: _Optional[str] = ..., value: _Optional[_Union[StageTiming, _Mapping]] = ...) -> None: ...
    class CountersEntry(_message.Message):
        __slots__ = ("key", "value")
        KEY_FIELD_NUMBER: _ClassVar[int]
        VALUE_FIELD_NUMBER: _ClassVar[int]
        key: str
        value: int
        def __init__(self, key: _Optional[str] = ..., value: _Optional[int] = ...) -> None: ...
    TRACE_ID_FIELD_NUMBER: _ClassVar[int]
    STAGES_FIELD_NUMBER: _ClassVar[int]
    COUNTERS_FIELD_NUMBER: _ClassVar[int]
    trace_id: str
    stages: _containers.MessageMap[str, StageTiming]
    counters: _containers.ScalarMap[str, int]
    def __init__(self, trace_id: _Optional[str] = ..., stages: _Optional[_Mapping[str, StageTiming]] = ..., counters: _Optional[_Mapping[str, int]] = ...) -> None: ...

class StageTiming(_message.Message):
    __slots__ = ("duration_ms", "success")
    DURATION_MS_FIELD_NUMBER: _ClassVar[int]
    SUCCESS_FIELD_NUMBER: _ClassVar[int]
    duration_ms: int
    success: bool
    def __init__(self, duration_ms: _Optional[int] = ..., success: _Optional[bool] = ...) -> None: ...

class RunRequest(_message.Message):
    __slots__ = ("media_id", "goal", "mode", "telemetry_seed")
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    GOAL_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    TELEMETRY_SEED_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    goal: str
    mode: AnalysisMode
    telemetry_seed: TelemetrySeed
    def __init__(self, media_id: _Optional[int] = ..., goal: _Optional[str] = ..., mode: _Optional[_Union[AnalysisMode, str]] = ..., telemetry_seed: _Optional[_Union[TelemetrySeed, _Mapping]] = ...) -> None: ...

class RunResponse(_message.Message):
    __slots__ = ("state", "markdown", "task_status")
    STATE_FIELD_NUMBER: _ClassVar[int]
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    TASK_STATUS_FIELD_NUMBER: _ClassVar[int]
    state: AgentState
    markdown: str
    task_status: TaskStatus
    def __init__(self, state: _Optional[_Union[AgentState, _Mapping]] = ..., markdown: _Optional[str] = ..., task_status: _Optional[_Union[TaskStatus, _Mapping]] = ...) -> None: ...

class ResultResponse(_message.Message):
    __slots__ = ("found", "state", "markdown", "task_status")
    FOUND_FIELD_NUMBER: _ClassVar[int]
    STATE_FIELD_NUMBER: _ClassVar[int]
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    TASK_STATUS_FIELD_NUMBER: _ClassVar[int]
    found: bool
    state: AgentState
    markdown: str
    task_status: TaskStatus
    def __init__(self, found: _Optional[bool] = ..., state: _Optional[_Union[AgentState, _Mapping]] = ..., markdown: _Optional[str] = ..., task_status: _Optional[_Union[TaskStatus, _Mapping]] = ...) -> None: ...

class ReuseRequest(_message.Message):
    __slots__ = ("media_id", "source_media_id", "goal", "mode", "target_source")
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    SOURCE_MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    GOAL_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    TARGET_SOURCE_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    source_media_id: int
    goal: str
    mode: AnalysisMode
    target_source: str
    def __init__(self, media_id: _Optional[int] = ..., source_media_id: _Optional[int] = ..., goal: _Optional[str] = ..., mode: _Optional[_Union[AnalysisMode, str]] = ..., target_source: _Optional[str] = ...) -> None: ...

class ReuseResponse(_message.Message):
    __slots__ = ("reused", "markdown", "task_status")
    REUSED_FIELD_NUMBER: _ClassVar[int]
    MARKDOWN_FIELD_NUMBER: _ClassVar[int]
    TASK_STATUS_FIELD_NUMBER: _ClassVar[int]
    reused: bool
    markdown: str
    task_status: TaskStatus
    def __init__(self, reused: _Optional[bool] = ..., markdown: _Optional[str] = ..., task_status: _Optional[_Union[TaskStatus, _Mapping]] = ...) -> None: ...

class FollowUpRequest(_message.Message):
    __slots__ = ("media_id", "goal", "question", "mode")
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    GOAL_FIELD_NUMBER: _ClassVar[int]
    QUESTION_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    goal: str
    question: str
    mode: AnalysisMode
    def __init__(self, media_id: _Optional[int] = ..., goal: _Optional[str] = ..., question: _Optional[str] = ..., mode: _Optional[_Union[AnalysisMode, str]] = ...) -> None: ...

class FollowUpResponse(_message.Message):
    __slots__ = ("answer",)
    ANSWER_FIELD_NUMBER: _ClassVar[int]
    answer: str
    def __init__(self, answer: _Optional[str] = ...) -> None: ...

class SearchEvidenceRequest(_message.Message):
    __slots__ = ("media_id", "query")
    MEDIA_ID_FIELD_NUMBER: _ClassVar[int]
    QUERY_FIELD_NUMBER: _ClassVar[int]
    media_id: int
    query: str
    def __init__(self, media_id: _Optional[int] = ..., query: _Optional[str] = ...) -> None: ...

class SearchEvidenceResponse(_message.Message):
    __slots__ = ("hits",)
    HITS_FIELD_NUMBER: _ClassVar[int]
    hits: _containers.RepeatedCompositeFieldContainer[VideoEvidenceHit]
    def __init__(self, hits: _Optional[_Iterable[_Union[VideoEvidenceHit, _Mapping]]] = ...) -> None: ...

class ClassifyModeRequest(_message.Message):
    __slots__ = ("goal",)
    GOAL_FIELD_NUMBER: _ClassVar[int]
    goal: str
    def __init__(self, goal: _Optional[str] = ...) -> None: ...

class StageRevisionRequest(_message.Message):
    __slots__ = ("feedback", "mode")
    FEEDBACK_FIELD_NUMBER: _ClassVar[int]
    MODE_FIELD_NUMBER: _ClassVar[int]
    feedback: AgentFeedback
    mode: AnalysisMode
    def __init__(self, feedback: _Optional[_Union[AgentFeedback, _Mapping]] = ..., mode: _Optional[_Union[AnalysisMode, str]] = ...) -> None: ...

class StageRevisionResponse(_message.Message):
    __slots__ = ("goal",)
    GOAL_FIELD_NUMBER: _ClassVar[int]
    goal: str
    def __init__(self, goal: _Optional[str] = ...) -> None: ...

class BeginRevisionResponse(_message.Message):
    __slots__ = ("begun",)
    BEGUN_FIELD_NUMBER: _ClassVar[int]
    begun: bool
    def __init__(self, begun: _Optional[bool] = ...) -> None: ...

class FeedbackList(_message.Message):
    __slots__ = ("items",)
    ITEMS_FIELD_NUMBER: _ClassVar[int]
    items: _containers.RepeatedCompositeFieldContainer[AgentFeedback]
    def __init__(self, items: _Optional[_Iterable[_Union[AgentFeedback, _Mapping]]] = ...) -> None: ...

class PlanResponse(_message.Message):
    __slots__ = ("found", "plan")
    FOUND_FIELD_NUMBER: _ClassVar[int]
    PLAN_FIELD_NUMBER: _ClassVar[int]
    found: bool
    plan: AgentPlan
    def __init__(self, found: _Optional[bool] = ..., plan: _Optional[_Union[AgentPlan, _Mapping]] = ...) -> None: ...
