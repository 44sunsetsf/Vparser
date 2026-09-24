import json

import fakeredis
import pytest

from app.agent.evidence import EvidenceVerificationService
from app.agent.loop import AgentLoopService
from app.agent.modes import ModeRegistry, ModeRouter
from app.agent.service import AgentService
from app.checkpoint.service import AgentCheckpointService
from app.errors import BudgetExceededError, VideoContextNotReadyError
from app.events import REDIS_CHANNEL, TaskEventPublisher, event_key
from app.keys import goal_digest
from app.models.dto import (
    AgentPlan, AgentState, AnalysisMode, AnalysisResult, CriticResult, Evidence, ModeClassification,
    VideoContext, VideoSegment)
from app.retrieval.long_context import LongVideoContextService
from app.telemetry import AgentTelemetry
from tests.test_checkpoint_repository import FakeDbRepo, FakeEngine

CTX = VideoContext(source="v.mp4", user_goal="总结", segments=[
    VideoSegment(start_ms=0, end_ms=60_000, transcript="今天讲二叉树前序遍历", ocr_texts=["前序遍历"],
                 evidence_frames=["f.jpg"])])
GOOD = AnalysisResult(title="T", conclusions=["讲了前序遍历"], evidence=[
    Evidence(timestamp_ms=1000, source="ASR", content="二叉树前序遍历", claim="讲了前序遍历")])


class FakeDeepSeek:
    def __init__(self, critiques):
        self.critiques = list(critiques)
        self.calls = []

    def plan(self, ctx, instr=""):
        self.calls.append("plan")
        return AgentPlan(understood_goal="u", tasks=["t1"])

    def repair_plan(self, ctx, plan, instr=""):
        self.calls.append("repair")
        return AgentPlan(understood_goal="u", tasks=["t1"])

    def replan(self, ctx, plan, critique, instr=""):
        self.calls.append("replan")
        return AgentPlan(understood_goal="u2", tasks=["t1", "t2"])

    def execute(self, ctx, plan, prev, instr=""):
        self.calls.append("execute")
        return GOOD

    def critique(self, ctx, plan, result, instr=""):
        self.calls.append("critique")
        return self.critiques.pop(0)

    def classify_mode(self, goal):
        return ModeClassification(mode=" review ", reason=None)


class NoRetrieval:
    def retrieve(self, *a):
        raise AssertionError("short video must not retrieve")


def build(critiques, max_rounds=2, max_tokens=50_000):
    redis = fakeredis.FakeRedis(decode_responses=True)
    repo = FakeDbRepo(redis, FakeEngine())
    ckpt = AgentCheckpointService(redis, repo)
    tel = AgentTelemetry(redis)
    ds = FakeDeepSeek(critiques)
    lc = LongVideoContextService(tel, ckpt, None, NoRetrieval())
    loop = AgentLoopService(ds, lc, ckpt, tel, EvidenceVerificationService(), TaskEventPublisher(redis),
                            max_rounds, 120_000, max_tokens, 0)
    return redis, repo, ckpt, tel, ds, loop


def test_passes_first_round_persists_checkpoints_and_publishes_events():
    redis, repo, ckpt, tel, ds, loop = build([CriticResult(passed=True)])
    pubsub = redis.pubsub()
    pubsub.subscribe(REDIS_CHANNEL)
    pubsub.get_message()
    tid = tel.start(1, "总结")
    state = loop.run(1, CTX, ModeRegistry().of(AnalysisMode.GENERAL))
    assert state.critique.passed and state.round == 1
    assert ds.calls == ["plan", "execute", "critique"]
    assert ckpt.load_result(1, "总结") == state
    d = goal_digest("总结")
    assert repo.rows[(1, f"goal:{d}:result")][0] == "ANALYSIS_COMPLETED"
    counters = tel.latest(1, "总结")["counters"]
    assert counters["criticRounds"] == 1 and counters["criticPassed"] == 1
    msgs = []
    while (m := pubsub.get_message(timeout=0.1)) is not None:
        msgs.append(json.loads(m["data"]))
    assert [m["event"]["stage"] for m in msgs] == [
        "PLAN_COMPLETED", "EXECUTOR_STARTED", "EXECUTOR_COMPLETED", "CRITIC_STARTED", "CRITIC_PASSED"]
    assert msgs[0]["key"] == event_key(1, "analysis", "总结", AnalysisMode.GENERAL) == f"analysis:1:{d}"
    assert list(msgs[0]["event"]) == ["state", "result", "message", "stage"]


def test_second_run_hits_terminal_checkpoint():
    redis, repo, ckpt, tel, ds, loop = build([CriticResult(passed=True)])
    tel.start(1, "总结")
    loop.run(1, CTX)
    ds.calls.clear()
    tel.start(1, "总结")
    loop.run(1, CTX)
    assert ds.calls == []
    assert tel.latest(1, "总结")["counters"]["terminalCheckpointHits"] == 1


def test_critic_retry_replans_when_requirements_missing_then_warns_at_max_rounds():
    bad = CriticResult(passed=False, missing_requirements=["缺X"])
    redis, repo, ckpt, tel, ds, loop = build([bad, bad])
    tel.start(1, "总结")
    state = loop.run(1, CTX)
    assert not state.critique.passed and state.round == 2
    assert ds.calls == ["plan", "execute", "critique", "replan", "execute", "critique"]
    c = tel.latest(1, "总结")["counters"]
    assert c["planRevisions"] == 1 and c["criticEvidenceRefreshes"] == 1
    assert repo.rows[(1, f"goal:{goal_digest('总结')}:result")][0] == "ANALYSIS_COMPLETED_WITH_WARNINGS"


def test_unverifiable_evidence_forces_failure_and_required_timestamp():
    fake = AnalysisResult(title="T", conclusions=["c"], evidence=[
        Evidence(timestamp_ms=1000, source="ASR", content="完全编造", claim="c")])
    redis, repo, ckpt, tel, ds, loop = build([CriticResult(passed=True)] * 2, max_rounds=1)
    ds.execute = lambda *a, **k: fake
    tel.start(1, "总结")
    state = loop.run(1, CTX)
    assert state.critique.passed is False
    assert state.critique.required_timestamps == [1000]
    assert state.critique.unsupported_claims == ["c", "证据无法在原始 ASR/OCR 中核验: 1000"]
    assert state.critique.feedback == ["为每条结论重新检索并绑定可核验的时间戳证据"]


def test_required_sections_enforced_for_mode():
    redis, repo, ckpt, tel, ds, loop = build([CriticResult(passed=True)], max_rounds=1)
    tel.start(1, "总结")
    with pytest.raises(Exception, match="Executor 未生成完整结构化结果"):
        loop.run(1, CTX, ModeRegistry().of(AnalysisMode.LEARNING))
    critique = ckpt.load_critic_state(1, "总结", AnalysisMode.LEARNING).critique
    assert critique.feedback == ["补充当前分析模式要求的结构化段落: outline, keypoints, quiz, pitfalls"]


def test_token_budget_terminates():
    redis, repo, ckpt, tel, ds, loop = build([CriticResult(passed=True)], max_tokens=1)
    tid = tel.start(1, "总结")
    tel.increment(tid, "inputTokensEstimated", 10)
    with pytest.raises(BudgetExceededError, match="Planner 后终止：Agent 超过最大 Token 预算 1"):
        loop.run(1, CTX)
    assert tel.latest(1, "总结")["counters"]["budgetTerminations"] == 1


def test_agent_service_run_merges_seed_and_needs_context():
    redis, repo, ckpt, tel, ds, loop = build([CriticResult(passed=True)])
    svc = AgentService(loop, loop._long_context, ckpt, tel, ModeRegistry())
    with pytest.raises(VideoContextNotReadyError):
        svc.run(1, "总结", AnalysisMode.GENERAL)
    ckpt.save_context(1, CTX)
    seed = {"traceId": "T-1", "stages": {"VIDEO_CONTEXT": {"durationMs": 1234, "success": True}},
            "counters": {"contextCheckpointHits": 1}}
    state = svc.run(1, "总结", AnalysisMode.GENERAL, trace_id="T-1", seed=seed)
    snap = json.loads(redis.get("agent:trace:T-1"))
    assert snap["stageDurationMs"]["VIDEO_CONTEXT"] == 1234 and snap["counters"]["VIDEO_CONTEXTCalls"] == 1
    assert snap["counters"]["contextCheckpointHits"] == 1 and "AGENT_LOOP" in snap["stageDurationMs"]
    assert redis.get(f"agent:trace:task:1:{goal_digest('总结')}") == "T-1"
    assert redis.sismember("agent:trace:task:1:goals", f"agent:trace:task:1:{goal_digest('总结')}")
    assert state.result == GOOD


def test_reuse_rewrites_evidence_frames_and_copies_result():
    redis, repo, ckpt, tel, ds, loop = build([])
    svc = AgentService(loop, loop._long_context, ckpt, tel, ModeRegistry())
    assert svc.reuse_result(2, 1, "总结", AnalysisMode.GENERAL, "t.mp4") is None
    ckpt.save_context(1, CTX)
    assert svc.reuse_result(2, 1, "总结", AnalysisMode.GENERAL, "t.mp4") is None
    ckpt.save_result(1, AgentState(goal="总结", result=GOOD, critique=CriticResult(passed=True), round=1))
    assert svc.reuse_result(2, 1, "总结", AnalysisMode.GENERAL, "t.mp4") is not None
    assert ckpt.load_context(2).source == "t.mp4"
    assert ckpt.load_context(2).segments[0].evidence_frames == ["t.mp4#timestampMs=0"]
    assert ckpt.load_result(2, "总结").result == GOOD


def test_mode_router():
    d = ModeRouter(FakeDeepSeek([])).route("找问题")
    assert d.mode == AnalysisMode.REVIEW and d.reason == "目标偏向查错与观点核验,已选审查模式"
    assert ModeRouter(FakeDeepSeek([])).route("  ").reason == "未提供分析目标,已按通用模式分析"
