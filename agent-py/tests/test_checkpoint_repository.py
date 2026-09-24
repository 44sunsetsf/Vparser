"""Redis/MySQL 双写契约(§6):用假 MySQL(dict)+ fakeredis 验证 key/字段/TTL/读回填。"""
from contextlib import contextmanager

import fakeredis
import pytest

from app.checkpoint.repository import AgentCheckpointRepository, SQL_UPSERT
from app.checkpoint.service import AgentCheckpointService
from app.keys import goal_digest
from app.models.dto import (
    AgentFeedback, AgentPlan, AgentState, AnalysisMode, AnalysisResult, CriticResult, Evidence, TaskStage,
    VideoContext, VideoSegment)


class FakeEngine:
    def __init__(self):
        self.commits = 0
        self.fail = False

    @contextmanager
    def begin(self):
        yield object()
        self.commits += 1


class FakeDbRepo(AgentCheckpointRepository):
    def __init__(self, redis, engine):
        super().__init__(redis, engine)
        self.rows: dict[tuple[int, str], tuple[str, str | None]] = {}

    def _find_payload(self, media_id, key):
        row = self.rows.get((media_id, key))
        return None if row is None else row[1]

    def _find_stage(self, media_id, key):
        row = self.rows.get((media_id, key))
        return None if row is None else row[0]

    def _upsert(self, media_id, key, stage, payload):
        self.rows[(media_id, key)] = (stage, payload)

    def delete_by_prefix(self, media_id, prefix):
        for k in [k for k in self.rows if k[0] == media_id and k[1].startswith(prefix)]:
            del self.rows[k]

    def delete_by_media_id(self, media_id):
        for k in [k for k in self.rows if k[0] == media_id]:
            del self.rows[k]

    def delete(self, media_id, name, redis_key):
        self.rows.pop((media_id, name), None)
        self._redis.delete(redis_key)


@pytest.fixture
def env():
    redis = fakeredis.FakeRedis(decode_responses=True)
    repo = FakeDbRepo(redis, FakeEngine())
    return redis, repo, AgentCheckpointService(redis, repo)


def _state(goal="目标", passed=True):
    return AgentState(goal=goal, plan=AgentPlan(understood_goal="u", tasks=["t"]),
                      result=AnalysisResult(title="T", conclusions=["c"], evidence=[
                          Evidence(timestamp_ms=1, source="ASR", content="x", claim="c")]),
                      critique=CriticResult(passed=passed), round=1)


def test_upsert_sql_matches_mapper():
    assert "ON DUPLICATE KEY UPDATE stage = VALUES(stage), payload = VALUES(payload), updated_at = CURRENT_TIMESTAMP(3)" in SQL_UPSERT


def test_save_result_writes_mysql_rows_and_redis_hash_with_ttl(env):
    redis, repo, svc = env
    svc.save_result(9, _state(), AnalysisMode.REVIEW)
    d = goal_digest("目标", AnalysisMode.REVIEW)
    stage, payload = repo.rows[(9, f"goal:{d}:result")]
    assert stage == "ANALYSIS_COMPLETED" and payload.startswith('{"goal":"目标"')
    assert repo.rows[(9, f"goal:{d}:stage")] == ("ANALYSIS_COMPLETED", None)
    key = f"agent:checkpoint:9:goal:{d}"
    assert redis.hget(key, "result") == payload and redis.hget(key, "stage") == "ANALYSIS_COMPLETED"
    assert 0 < redis.ttl(key) <= 7 * 24 * 3600
    assert redis.sismember("agent:checkpoint:9:goals", key)
    assert redis.ttl("agent:checkpoint:9:goals") > 0


def test_read_prefers_redis_then_backfills_from_db(env):
    redis, repo, svc = env
    svc.save_result(9, _state(passed=False))
    d = goal_digest("目标")
    assert repo.rows[(9, f"goal:{d}:result")][0] == "ANALYSIS_COMPLETED_WITH_WARNINGS"
    redis.delete(f"agent:checkpoint:9:goal:{d}")
    assert svc.load_result(9, "目标") == _state(passed=False)
    assert redis.hget(f"agent:checkpoint:9:goal:{d}", "result") is not None  # 回填
    assert svc.load_stage(9, "目标") == TaskStage.ANALYSIS_COMPLETED_WITH_WARNINGS
    assert svc.load_result(9, "别的目标") is None


def test_media_level_keys_and_context_saved_without_user_goal(env):
    redis, repo, svc = env
    ctx = VideoContext(source="s", user_goal="secret goal", segments=[VideoSegment(start_ms=0, end_ms=5)])
    svc.save_context(3, ctx)
    assert repo.rows[(3, "media:context")][0] == "CONTEXT_COMPLETED"
    assert repo.rows[(3, "media:stage")] == ("CONTEXT_COMPLETED", None)
    assert redis.hget("agent:checkpoint:3", "context") == \
        '{"source":"s","userGoal":"","segments":[{"startMs":0,"endMs":5,"transcript":"","ocrTexts":[],"evidenceFrames":[]}]}'
    assert svc.load_context(3).user_goal == ""
    svc.save_chunks(3, [])
    assert repo.rows[(3, "media:chunks")] == ("CHUNKS_COMPLETED", "[]")
    assert svc.load_chunks(3) == []


def test_critic_state_and_plan_stages(env):
    redis, repo, svc = env
    svc.save_plan(1, "g", AnalysisMode.GENERAL, AgentPlan(understood_goal="u", tasks=["a"]))
    d = goal_digest("g")
    assert repo.rows[(1, f"goal:{d}:plan")][0] == "PLAN_COMPLETED"
    svc.save_execution_state(1, AgentState(goal="g", round=1))
    assert repo.rows[(1, f"goal:{d}:criticState")][0] == "EXECUTOR_COMPLETED"
    svc.save_critic_state(1, _state("g", passed=False))
    assert repo.rows[(1, f"goal:{d}:criticState")][0] == "CRITIC_RETRY_REQUIRED"
    assert svc.load_plan(1, "g") == AgentPlan(understood_goal="u", tasks=["a"])


def test_revision_lifecycle(env):
    redis, repo, svc = env
    plan = AgentPlan(understood_goal="u", tasks=["a"])
    assert svc.begin_staged_revision(1, "g", AnalysisMode.REVIEW) is False
    svc.save_result(1, _state("g"), AnalysisMode.REVIEW)
    svc.stage_revision(1, "g", AnalysisMode.REVIEW, plan)
    d = goal_digest("g", AnalysisMode.REVIEW)
    assert repo.rows[(1, f"revision:{d}")][1] == '{"plan":{"understoodGoal":"u","tasks":["a"]},"applied":false}'
    assert svc.begin_staged_revision(1, "g", AnalysisMode.REVIEW) is True
    assert (1, f"goal:{d}:result") not in repo.rows  # 旧结果被清掉
    assert (1, f"goal:{d}:plan") in repo.rows
    assert '"applied":true' in repo.rows[(1, f"revision:{d}")][1]
    assert svc.begin_staged_revision(1, "g", AnalysisMode.REVIEW) is True  # 幂等
    svc.complete_staged_revision(1, "g", AnalysisMode.REVIEW)
    assert (1, f"revision:{d}") not in repo.rows


def test_feedback_list_trim_and_ttl(env):
    redis, repo, svc = env
    for i in range(205):
        svc.save_feedback(AgentFeedback(media_id=4, goal="g", comment=str(i)))
    assert redis.llen("agent:feedback:4") == 200
    assert 29 * 24 * 3600 < redis.ttl("agent:feedback:4") <= 30 * 24 * 3600
    items = svc.load_feedback(4)
    assert items[0].comment == "5" and items[-1].comment == "204" and items[0].mode == "GENERAL"


def test_save_failure_and_delete_media(env):
    redis, repo, svc = env
    svc.save_result(2, _state("g"))
    svc.save_feedback(AgentFeedback(media_id=2, goal="g"))
    svc.save_failure(2, "g", AnalysisMode.GENERAL, TaskStage.AGENT_LOOP, RuntimeError("x"))
    d = goal_digest("g")
    key = f"agent:checkpoint:2:goal:{d}"
    assert redis.hget(key, "stage") == "FAILED" and redis.hget(key, "failedStage") == "AGENT_LOOP"
    assert redis.hget(key, "errorType") == "RuntimeError"
    assert repo.rows[(2, f"goal:{d}:stage")] == ("FAILED", None)
    svc.delete_media(2)
    assert not repo.rows and not redis.keys("agent:*")
