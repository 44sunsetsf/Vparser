import fakeredis
import pytest

from app.agent.followup import (
    UNVERIFIED_NOTE,
    FollowUpService,
    build_prompt,
    format_timestamp,
    parse_timestamp,
    verify_citations,
)
from app.checkpoint.service import AgentCheckpointService
from app.errors import InvalidArgumentError, VideoContextNotReadyError
from app.llm import prompts as P
from app.models.dto import AgentState, AnalysisMode, AnalysisResult, CriticResult, Evidence, VideoContext, VideoSegment
from app.retrieval.long_context import LongVideoContextService
from app.telemetry import AgentTelemetry
from tests.test_checkpoint_repository import FakeDbRepo, FakeEngine

SEGMENTS = [
    VideoSegment(start_ms=0, end_ms=30_000, transcript="开场介绍二叉树"),
    VideoSegment(start_ms=65_500, end_ms=90_000, transcript="前序遍历先访问根节点", ocr_texts=["preorder"]),
]
CTX = VideoContext(source="v.mp4", user_goal="", segments=SEGMENTS)


def test_timestamp_format_and_parse():
    assert format_timestamp(65_999) == "01:05"
    assert format_timestamp(3_723_000) == "1:02:03"
    assert parse_timestamp("01:05") == 65 and parse_timestamp("1:02:03") == 3723


def test_citations_inside_retrieved_segments_are_kept():
    checked = verify_citations("根节点优先 [01:05]，开场 [00:00] 和 [00:29]。", SEGMENTS)
    assert checked.answer == "根节点优先 [01:05]，开场 [00:00] 和 [00:29]。"
    assert (checked.verified, checked.rejected) == (3, 0)


def test_citations_outside_segments_are_unlinked_and_flagged():
    checked = verify_citations("见 [00:30]、[01:30] 与 [1:00:00]，另见 [01:10](#video-t=70)。", SEGMENTS)
    assert checked.answer == ("见 00:30（未核验）、01:30（未核验） 与 1:00:00（未核验），另见 [01:10]。"
                              "\n\n" + UNVERIFIED_NOTE)
    assert (checked.verified, checked.rejected) == (1, 3)


def test_prompt_includes_previous_summary_only_with_goal():
    bare = build_prompt("问题", SEGMENTS)
    assert bare.startswith(P.FOLLOW_UP_HEAD + "问题" + P.FOLLOW_UP_CONTEXT)
    assert '"start":"01:05","end":"01:30"' in bare and P.FOLLOW_UP_PREVIOUS not in bare
    full = build_prompt("问题", SEGMENTS, "原目标", "x" * 5000)
    assert P.FOLLOW_UP_GOAL + "原目标" + P.FOLLOW_UP_PREVIOUS + "x" * 4000 + P.FOLLOW_UP_CONTEXT in full
    assert "x" * 4001 not in full


class FakeModel:
    def __init__(self, answer):
        self.answer = answer
        self.prompts = []

    def answer_follow_up(self, prompt):
        self.prompts.append(prompt)
        return self.answer


class NoRetrieval:
    def retrieve(self, *a):
        raise AssertionError("short video must not use chunk retrieval")


def build(answer):
    redis = fakeredis.FakeRedis(decode_responses=True)
    ckpt = AgentCheckpointService(redis, FakeDbRepo(redis, FakeEngine()))
    tel = AgentTelemetry(redis)
    model = FakeModel(answer)
    svc = FollowUpService(model, LongVideoContextService(tel, ckpt, None, NoRetrieval()), ckpt, tel)
    return svc, ckpt, tel, model


def test_answer_grounds_on_retrieved_segments_and_previous_result():
    svc, ckpt, tel, model = build("  先访问根节点 [01:10]，也提到了 [05:00]。 ")
    ckpt.save_context(1, CTX)
    previous = AnalysisResult(title="旧分析", conclusions=["c"],
                              evidence=[Evidence(timestamp_ms=70_000, source="ASR", content="x", claim="c")])
    ckpt.save_result(1, AgentState(goal="总结", result=previous, critique=CriticResult(passed=True), round=1),
                     AnalysisMode.LEARNING)

    answer = svc.answer(1, "总结", " 前序遍历怎么走？ ", AnalysisMode.LEARNING)

    assert answer == "先访问根节点 [01:10]，也提到了 05:00（未核验）。\n\n" + UNVERIFIED_NOTE
    prompt = model.prompts[0]
    assert prompt.startswith(P.FOLLOW_UP_HEAD + "前序遍历怎么走？" + P.FOLLOW_UP_GOAL + "总结")
    assert "## 旧分析" in prompt and "前序遍历先访问根节点" in prompt
    counters = tel.latest(1, "前序遍历怎么走？", AnalysisMode.LEARNING)["counters"]
    assert counters["followUpCitationsVerified"] == 1 and counters["followUpCitationsRejected"] == 1
    assert counters["RETRIEVALCalls"] == 1


def test_goal_without_result_sends_no_previous_analysis():
    svc, ckpt, _, model = build("没有相关信息。")
    ckpt.save_context(1, CTX)
    assert svc.answer(1, "总结", "q", AnalysisMode.GENERAL) == "没有相关信息。"
    assert P.FOLLOW_UP_PREVIOUS not in model.prompts[0]


def test_rejects_blank_question_and_missing_context():
    svc, ckpt, _, _ = build("x")
    with pytest.raises(InvalidArgumentError):
        svc.answer(1, None, "  ", AnalysisMode.GENERAL)
    with pytest.raises(VideoContextNotReadyError):
        svc.answer(1, None, "q", AnalysisMode.GENERAL)
