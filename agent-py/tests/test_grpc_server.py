"""End-to-end tests of the real gRPC server (in process, ephemeral port) over fake domain services."""
from types import SimpleNamespace

import grpc
import pytest
from grpc_health.v1 import health_pb2, health_pb2_grpc

from app.agent import budget
from app.config import Settings
from app.errors import BudgetExceededError, InvalidArgumentError, RetriableModelError, VideoContextNotReadyError
from app.gen.agent.v1 import agent_pb2 as pb
from app.gen.agent.v1 import agent_pb2_grpc
from app.models.dto import AgentState, AnalysisMode, AnalysisResult, CriticResult, Evidence, RouteDecision
from app.server import SERVICE_NAME, build_server

TOKEN = (("x-internal-token", "tok"),)
STATE = AgentState(goal="g", round=1, critique=CriticResult(passed=True), result=AnalysisResult(
    title="T", conclusions=["c"], evidence=[Evidence(timestamp_ms=1000, source="ASR", content="x", claim="c")]))


class FakeFollowUp:
    def __init__(self):
        self.error = None
        self.calls = []

    def answer(self, media_id, goal, question, mode):
        self.calls.append((media_id, goal, question, mode, budget.remaining_millis()))
        if self.error:
            raise self.error
        return f"answer [00:01] {mode.value}"


class FakeAgent:
    def __init__(self):
        self.error = None

    def run(self, media_id, goal, mode, trace_id=None, seed=None):
        if self.error:
            raise self.error
        self.last = (media_id, goal, mode, trace_id, seed)
        return STATE


@pytest.fixture(scope="module")
def server():
    container = SimpleNamespace(
        agent=FakeAgent(), follow_up=FakeFollowUp(),
        router=SimpleNamespace(route=lambda goal: RouteDecision(mode=AnalysisMode.LEARNING, reason="r")),
        telemetry=SimpleNamespace(latest=lambda m, g, mode: {"traceId": "T", "counters": {"modelCalls": 2}}))
    settings = Settings(internal_token="tok", agent_max_duration_ms=120_000, agent_grpc_max_workers=4)
    srv, health, port = build_server(container, settings, port=0)
    srv.start()
    channel = grpc.insecure_channel(f"127.0.0.1:{port}")
    yield SimpleNamespace(container=container, health=health, channel=channel,
                          stub=agent_pb2_grpc.AgentServiceStub(channel))
    channel.close()
    srv.stop(None)


@pytest.fixture(autouse=True)
def reset(server):
    server.container.agent.error = None
    server.container.follow_up.error = None
    server.container.follow_up.calls.clear()


def code_of(call) -> grpc.StatusCode:
    with pytest.raises(grpc.RpcError) as e:
        call()
    return e.value.code()


def test_health_needs_no_token_and_starts_not_serving(server):
    stub = health_pb2_grpc.HealthStub(server.channel)
    assert stub.Check(health_pb2.HealthCheckRequest(service=SERVICE_NAME), timeout=5).status == \
        health_pb2.HealthCheckResponse.NOT_SERVING
    server.health.set("", health_pb2.HealthCheckResponse.SERVING)
    assert stub.Check(health_pb2.HealthCheckRequest(), timeout=5).status == health_pb2.HealthCheckResponse.SERVING


def test_missing_or_wrong_token_is_unauthenticated(server):
    req = pb.ClassifyModeRequest(goal="x")
    assert code_of(lambda: server.stub.ClassifyMode(req, timeout=5)) == grpc.StatusCode.UNAUTHENTICATED
    assert code_of(lambda: server.stub.ClassifyMode(
        req, timeout=5, metadata=(("x-internal-token", "nope"),))) == grpc.StatusCode.UNAUTHENTICATED


def test_classify_mode(server):
    decision = server.stub.ClassifyMode(pb.ClassifyModeRequest(goal="x"), timeout=5, metadata=TOKEN)
    assert decision.mode == pb.LEARNING and decision.reason == "r"


def test_run_returns_state_markdown_and_status(server):
    req = pb.RunRequest(media_id=3, goal="g", mode=pb.REVIEW, telemetry_seed=pb.TelemetrySeed(trace_id="T-1"))
    resp = server.stub.Run(req, timeout=200, metadata=TOKEN)
    assert resp.state.round == 1 and resp.state.critique.passed and resp.markdown.startswith("## T")
    assert resp.task_status.state == "COMPLETED"
    assert server.container.agent.last[:4] == (3, "g", AnalysisMode.REVIEW, "T-1")


def test_follow_up_optional_goal_and_budget_capped_by_deadline(server):
    fu = server.container.follow_up
    resp = server.stub.FollowUp(pb.FollowUpRequest(media_id=5, question="q", mode=pb.CREATION),
                                timeout=10, metadata=TOKEN)
    assert resp.answer == "answer [00:01] CREATION"
    media_id, goal, question, mode, remaining_ms = fu.calls[-1]
    assert (media_id, goal, question, mode) == (5, None, "q", AnalysisMode.CREATION)
    assert 7_000 < remaining_ms <= 8_200  # 10s deadline - 2s safety margin (timeout encoding rounds up)
    server.stub.FollowUp(pb.FollowUpRequest(media_id=5, goal="", question="q"), timeout=10, metadata=TOKEN)
    assert fu.calls[-1][1] == ""  # present-but-empty is passed through; the service treats it as absent


def test_deadline_too_short_fails_fast(server):
    assert code_of(lambda: server.stub.FollowUp(pb.FollowUpRequest(media_id=5, question="q"),
                                                timeout=1.5, metadata=TOKEN)) == grpc.StatusCode.DEADLINE_EXCEEDED
    assert server.container.follow_up.calls == []


@pytest.mark.parametrize("error,code", [
    (InvalidArgumentError("bad"), grpc.StatusCode.INVALID_ARGUMENT),
    (VideoContextNotReadyError(), grpc.StatusCode.FAILED_PRECONDITION),
    (RetriableModelError("模型调用线程池繁忙"), grpc.StatusCode.UNAVAILABLE),
    (RuntimeError("boom"), grpc.StatusCode.INTERNAL),
])
def test_errors_become_status_codes(server, error, code):
    server.container.follow_up.error = error
    assert code_of(lambda: server.stub.FollowUp(pb.FollowUpRequest(media_id=5, question="q"),
                                                timeout=10, metadata=TOKEN)) == code


def test_budget_exhausted_on_run_with_trailer(server):
    server.container.agent.error = BudgetExceededError("Planner 后终止")
    with pytest.raises(grpc.RpcError) as e:
        server.stub.Run(pb.RunRequest(media_id=3, goal="g"), timeout=200, metadata=TOKEN)
    assert e.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED
    assert ("x-agent-error", "BUDGET_EXHAUSTED") in tuple(e.value.trailing_metadata())


def test_invalid_requests(server):
    assert code_of(lambda: server.stub.Run(pb.RunRequest(goal="g"), timeout=200, metadata=TOKEN)) == \
        grpc.StatusCode.INVALID_ARGUMENT
    assert code_of(lambda: server.stub.Run(pb.RunRequest(media_id=1, goal=" "), timeout=200, metadata=TOKEN)) == \
        grpc.StatusCode.INVALID_ARGUMENT
    assert code_of(lambda: server.stub.Run(pb.RunRequest(media_id=1, goal="g", mode=42), timeout=200,
                                           metadata=TOKEN)) == grpc.StatusCode.INVALID_ARGUMENT


def test_get_trace_is_a_struct(server):
    trace = server.stub.GetTrace(pb.GoalRef(media_id=1, goal="g"), timeout=10, metadata=TOKEN)
    assert trace["traceId"] == "T" and trace["counters"]["modelCalls"] == 2
