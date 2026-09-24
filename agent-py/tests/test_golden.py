"""Golden values for the wire formats: JSON field order, markdown rendering and goalDigest."""
import json

import pytest

from app.errors import InvalidArgumentError
from app.keys import goal_digest, normalize_content_hash
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
    TaskEvent,
    TaskStage,
    TaskStatus,
    TaskState,
    VideoChunk,
    VideoContext,
    VideoSegment,
)


def test_goal_digest_general_is_sha256_of_trimmed_goal():
    expected = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
    assert goal_digest("hello") == expected
    assert goal_digest("  hello \n", AnalysisMode.GENERAL) == expected
    assert goal_digest("hello", None) == expected
    assert goal_digest("分析视频") == "afc1405406f6d65c773fec7341842d9c1b82c7e7e8003ba86d1543d2d29eb1fc"


def test_goal_digest_non_general_includes_mode_and_unit_separator():
    assert goal_digest("hello", AnalysisMode.REVIEW) == \
        "36224327625e237853f90af1554a6e387b3e93cef4baa961bc09ac4493e830b2"
    assert goal_digest(" 分析视频 ", AnalysisMode.LEARNING) == \
        "26c75a3c10e6c494680d2a25f4e55d30d13c01c2122022b50ec99280ea55f108"


@pytest.mark.parametrize("goal", [None, "", "   ", "\t\n"])
def test_goal_digest_rejects_blank(goal):
    with pytest.raises(InvalidArgumentError):
        goal_digest(goal)
    with pytest.raises(InvalidArgumentError):
        goal_digest(goal, AnalysisMode.REVIEW)


def test_goal_digest_trims_only_ascii_control_and_space():
    # only code points <= U+0020 are trimmed; the ideographic space U+3000 is kept
    assert goal_digest("　hello") != goal_digest("hello")


def test_normalize_content_hash():
    assert normalize_content_hash(5, "ABCDEF0123456789abcdef0123456789") == "abcdef0123456789abcdef0123456789"
    assert normalize_content_hash(5, "short") == "media-5"
    assert normalize_content_hash(5, None) == "media-5"


def _result() -> AnalysisResult:
    return AnalysisResult(
        title="  标题 ", conclusions=["结论一", "结论二"],
        evidence=[Evidence(timestamp_ms=65_000, source="ASR", content="内容A", claim="结论一"),
                  Evidence(timestamp_ms=3_725_999, source=None, content=" 内容B ", claim="结论二")],
        suggestions=["建议一"],
        sections=[Section(key="quiz", title="自测题", items=["Q1", "Q2"])])


def test_to_markdown():
    expected = (
        "## 标题\n\n## 核心结论\n- 结论一\n- 结论二\n"
        "\n## 视频证据\n- [01:05] ASR：内容A\n- [62:05] UNKNOWN：内容B\n"
        "\n## 建议\n- 建议一\n"
        "\n## 自测题\n- Q1\n- Q2\n")
    assert _result().to_markdown() == expected


def test_to_markdown_general_without_sections_and_defaults():
    assert AnalysisResult().to_markdown() == "## 未命名分析\n\n## 核心结论\n\n## 视频证据\n\n## 建议\n"


def test_agent_state_json_wire_format():
    state = AgentState(goal=" g ", plan=AgentPlan(understood_goal="u", tasks=["t"]),
                       result=_result(), critique=CriticResult(passed=True), round=1)
    expected = (
        '{"goal":"g","plan":{"understoodGoal":"u","tasks":["t"]},'
        '"result":{"title":"标题","conclusions":["结论一","结论二"],"evidence":['
        '{"timestampMs":65000,"source":"ASR","content":"内容A","claim":"结论一"},'
        '{"timestampMs":3725999,"source":"UNKNOWN","content":"内容B","claim":"结论二"}],'
        '"suggestions":["建议一"],"sections":[{"key":"quiz","title":"自测题","items":["Q1","Q2"]}]},'
        '"critique":{"passed":true,"feedback":[],"missingRequirements":[],"unsupportedClaims":[],'
        '"requiredTimestamps":[]},"round":1}')
    assert state.to_json() == expected
    assert AgentState.model_validate_json(expected) == state


def test_agent_state_nulls_and_validation():
    s = AgentState(goal="g")
    assert s.to_json() == '{"goal":"g","plan":null,"result":null,"critique":null,"round":0}'
    with pytest.raises(InvalidArgumentError):
        AgentState(goal="  ")
    with pytest.raises(InvalidArgumentError):
        AgentState(goal="g", round=-1)


def test_video_context_json():
    ctx = VideoContext(source="a.mp4", user_goal="  ", segments=[
        VideoSegment(start_ms=0, end_ms=1000, transcript=" hi ", ocr_texts=["x"], evidence_frames=["f.jpg"])])
    assert ctx.to_json() == (
        '{"source":"a.mp4","userGoal":"","segments":[{"startMs":0,"endMs":1000,"transcript":"hi",'
        '"ocrTexts":["x"],"evidenceFrames":["f.jpg"]}]}')
    assert ctx.transcript_text() == "hi"
    with pytest.raises(InvalidArgumentError):
        VideoContext(source=" ")
    with pytest.raises(InvalidArgumentError):
        VideoSegment(start_ms=5, end_ms=5)


def test_video_context_defaults_missing_fields():
    ctx = VideoContext.model_validate_json('{"source":"s","segments":[{"startMs":0,"endMs":10}]}')
    assert ctx.user_goal == "" and ctx.segments[0].ocr_texts == [] and ctx.segments[0].transcript == ""


def test_video_chunk_json():
    seg = VideoSegment(start_ms=0, end_ms=1000, transcript="hi")
    chunk = VideoChunk(start_time=0, end_time=300000, segment_summary=" s ", keywords=["k"],
                       raw_segments=[seg], embedding=[0.5, 0.25])
    assert chunk.to_json() == (
        '{"startTime":0,"endTime":300000,"segmentSummary":"s","keywords":["k"],"rawSegments":['
        '{"startMs":0,"endMs":1000,"transcript":"hi","ocrTexts":[],"evidenceFrames":[]}],'
        '"embedding":[0.5,0.25]}')
    assert chunk.start_ms == 0 and chunk.end_ms == 300000


def test_route_decision_and_task_event_json():
    assert RouteDecision(mode=None, reason=" ").to_json() == '{"mode":"GENERAL","reason":"已按通用模式分析"}'
    ev = TaskEvent.of(TaskStatus.of(TaskState.PROCESSING, "m"), TaskStage.PLAN_COMPLETED)
    assert ev.to_json() == '{"state":"PROCESSING","result":null,"message":"m","stage":"PLAN_COMPLETED"}'


def test_task_status_completed_with_and_without_critic_pass():
    passed = AgentState(goal="g", result=_result(), critique=CriticResult(passed=True))
    assert TaskStatus.completed_from_state(passed).to_json() == json.dumps(
        {"state": "COMPLETED", "result": _result().to_markdown(), "message": "任务完成"},
        ensure_ascii=False, separators=(",", ":"))
    warned = TaskStatus.completed_from_state(AgentState(goal="g", result=_result()))
    assert warned.result.startswith("> **结果提示：** 分析已完成，但部分结论未通过 Critic 校验，请结合时间戳证据人工核验。\n\n## 标题")
    assert warned.message == "分析已完成，但部分结论未通过 Critic 校验，请结合时间戳证据人工核验。"


def test_feedback_normalized_and_instant_format():
    fb = AgentFeedback.model_validate_json(
        '{"mediaId":7,"goal":" g ","mode":"review","rating":1,"correctedTasks":[" a ","  ",null],'
        '"createdAt":"2026-01-02T03:04:05.120Z"}')
    n = fb.normalized()
    assert json.loads(n.to_json()) == {
        "mediaId": 7, "goal": "g", "mode": "REVIEW", "rating": 1, "errorType": None, "comment": None,
        "correctedGoal": None, "correctedTasks": ["a"], "evidenceTimestamp": None, "evidenceAccepted": None,
        "createdAt": "2026-01-02T03:04:05.120Z"}
    assert n.to_json().index('"mediaId"') < n.to_json().index('"goal"') < n.to_json().index('"createdAt"')
    assert AgentFeedback(media_id=1, goal="g").normalized().created_at is not None
