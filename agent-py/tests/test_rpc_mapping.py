"""Unit tests for the RPC adapters: exception -> status code, deadline -> budget, proto <-> domain."""
from datetime import UTC, datetime

import grpc
import httpx
import openai
import pytest
import redis.exceptions
import sqlalchemy.exc

from app.errors import (
    AgentError,
    BudgetExceededError,
    DeadlineExceededError,
    InvalidArgumentError,
    RetriableModelError,
    VideoContextNotReadyError,
)
from app.gen.agent.v1 import agent_pb2 as pb
from app.models.dto import (
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
)
from app.rpc import convert as cv
from app.rpc.servicer import execution_budget_ms
from app.rpc.status import status_for


def wrapped(inner: BaseException, outer: BaseException) -> BaseException:
    try:
        try:
            raise inner
        except BaseException as e:
            raise outer from e
    except BaseException as e:
        return e


S = grpc.StatusCode


@pytest.mark.parametrize("error,code", [
    (BudgetExceededError("b"), S.RESOURCE_EXHAUSTED),
    (wrapped(DeadlineExceededError("d"), BudgetExceededError("b")), S.RESOURCE_EXHAUSTED),
    (VideoContextNotReadyError(), S.FAILED_PRECONDITION),
    (DeadlineExceededError("d"), S.DEADLINE_EXCEEDED),
    (InvalidArgumentError("bad"), S.INVALID_ARGUMENT),
    (wrapped(InvalidArgumentError("root"), AgentError("wrapped")), S.INVALID_ARGUMENT),
    (wrapped(RetriableModelError("pool busy"), AgentError("max attempts")), S.UNAVAILABLE),
    (wrapped(openai.APIStatusError("503", response=httpx.Response(
        503, request=httpx.Request("POST", "http://x")), body=None), AgentError("x")), S.UNAVAILABLE),
    (redis.exceptions.ConnectionError("down"), S.UNAVAILABLE),
    (sqlalchemy.exc.OperationalError("SELECT 1", {}, Exception("gone")), S.UNAVAILABLE),
    (httpx.ConnectError("refused"), S.UNAVAILABLE),
    (TimeoutError(), S.UNAVAILABLE),
    (AgentError("planner returned garbage"), S.INTERNAL),
    (RuntimeError("boom"), S.INTERNAL),
])
def test_status_mapping(error, code):
    assert status_for(error) == code


@pytest.mark.parametrize("remaining,expected", [
    (None, (120_000, False)),          # no deadline: configured budget
    (1e12, (120_000, False)),          # "infinite" deadline
    (300.0, (120_000, False)),         # deadline looser than the budget
    (32.0, (30_000, True)),            # capped at remaining - 2s
    (2.5, (500, True)),
    (2.0, (0, True)),                  # <= 2s left: fail fast
    (0.5, (-1_500, True)),
])
def test_deadline_caps_budget(remaining, expected):
    assert execution_budget_ms(120_000, remaining) == expected


# ---------- conversion ----------
STATE = AgentState(
    goal="g", round=2, plan=AgentPlan(understood_goal="u", tasks=["t1", "t2"]),
    result=AnalysisResult(title="T", conclusions=["c"], suggestions=["s"],
                          evidence=[Evidence(timestamp_ms=65_000, source="ASR", content="x", claim="c")],
                          sections=[Section(key="quiz", title="自测题", items=["Q1"])]),
    critique=CriticResult(passed=False, feedback=["f"], missing_requirements=["m"],
                          unsupported_claims=["u"], required_timestamps=[1000, 2000]))


def test_state_round_trip():
    msg = cv.state_to_proto(STATE)
    assert cv.state_from_proto(pb.AgentState.FromString(msg.SerializeToString())) == STATE


def test_state_absent_submessages_stay_absent():
    msg = cv.state_to_proto(AgentState(goal="g"))
    assert not msg.HasField("plan") and not msg.HasField("result") and not msg.HasField("critique")
    assert cv.state_from_proto(msg) == AgentState(goal="g")


def test_modes():
    assert cv.mode_from_proto(pb.ANALYSIS_MODE_UNSPECIFIED) == AnalysisMode.GENERAL
    for mode in AnalysisMode:
        assert cv.mode_from_proto(cv.mode_to_proto(mode)) == mode
    with pytest.raises(InvalidArgumentError):
        cv.mode_from_proto(99)


def test_feedback_round_trip_with_optional_fields():
    full = AgentFeedback(media_id=7, goal="g", mode="REVIEW", rating=-1, error_type="E", comment="",
                         corrected_goal="cg", corrected_tasks=["a"], evidence_timestamp=0,
                         evidence_accepted=False, created_at=datetime(2026, 1, 2, 3, 4, 5, 120_000, tzinfo=UTC))
    msg = cv.feedback_to_proto(full)
    # zero / empty / false values of optional fields are still "present"
    for name in ("rating", "comment", "evidence_timestamp", "evidence_accepted"):
        assert msg.HasField(name)
    assert msg.created_at == "2026-01-02T03:04:05.120Z"
    assert cv.feedback_from_proto(pb.AgentFeedback.FromString(msg.SerializeToString())) == full


def test_feedback_unset_optionals_are_none():
    back = cv.feedback_from_proto(pb.AgentFeedback(media_id=1, goal="g"))
    assert back == AgentFeedback(media_id=1, goal="g", mode="GENERAL", corrected_tasks=[])
    assert not cv.feedback_to_proto(back).HasField("rating")


def test_seed_hits_route_status_and_struct():
    req = pb.RunRequest(media_id=1, goal="g", telemetry_seed=pb.TelemetrySeed(
        trace_id="T", stages={"VIDEO_CONTEXT": pb.StageTiming(duration_ms=12, success=True)},
        counters={"asrSegments": 3}))
    assert cv.seed_from_proto(req) == {"traceId": "T", "stages": {"VIDEO_CONTEXT": {"durationMs": 12, "success": True}},
                                       "counters": {"asrSegments": 3}}
    assert cv.seed_from_proto(pb.RunRequest(media_id=1, goal="g")) is None

    hit = cv.hit_to_proto(VideoEvidenceHit(start_ms=1, end_ms=2, source="OCR", snippet="s", transcript="",
                                           ocr_texts=["a", "b"]))
    assert list(hit.ocr_texts) == ["a", "b"] and hit.source == "OCR"
    assert cv.route_to_proto(RouteDecision(mode=AnalysisMode.CREATION, reason="r")).mode == pb.CREATION
    status = cv.task_status_to_proto(TaskStatus.completed("md"))
    assert (status.state, status.result, status.message) == ("COMPLETED", "md", "任务完成")

    struct = cv.to_struct({"counters": {"a": 1}, "values": {"x": 0.5}, "missing": None, "list": [1, "s"]})
    assert struct["counters"]["a"] == 1 and struct["values"]["x"] == 0.5 and struct["missing"] is None
    assert list(struct["list"]) == [1, "s"]
    assert len(cv.to_struct({})) == 0
