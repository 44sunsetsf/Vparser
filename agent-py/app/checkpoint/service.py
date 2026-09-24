"""AgentCheckpointService: plan / context / critic / result / chunks / revision / feedback checkpoints."""
import logging

from pydantic import BaseModel

from ..keys import goal_digest
from ..models.dto import (
    AgentFeedback,
    AgentPlan,
    AgentState,
    AnalysisMode,
    CamelModel,
    TaskStage,
    VideoChunk,
    VideoContext,
)
from .repository import AgentCheckpointRepository

log = logging.getLogger("agent.checkpoint")

MAX_FEEDBACK_SAMPLES = 200
FEEDBACK_TTL_SECONDS = 30 * 24 * 3600
SEVEN_DAYS = 7 * 24 * 3600


class RevisionCheckpoint(CamelModel):
    plan: AgentPlan | None = None
    applied: bool = False


class AgentCheckpointService:
    def __init__(self, redis_client, repository: AgentCheckpointRepository) -> None:
        self._redis = redis_client
        self._repo = repository

    # ---- 读 ----
    def load_context(self, media_id: int) -> VideoContext | None:
        return self._repo.read(media_id, self._media_cp("context"), self._checkpoint_key(media_id),
                               "context", VideoContext)

    def load_result(self, media_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL) -> AgentState | None:
        return self._repo.read(media_id, self._goal_cp(goal, mode, "result"),
                               self._goal_key(media_id, goal, mode), "result", AgentState)

    def load_plan(self, media_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL) -> AgentPlan | None:
        return self._repo.read(media_id, self._goal_cp(goal, mode, "plan"),
                               self._goal_key(media_id, goal, mode), "plan", AgentPlan)

    def load_critic_state(self, media_id: int, goal: str,
                          mode: AnalysisMode = AnalysisMode.GENERAL) -> AgentState | None:
        return self._repo.read(media_id, self._goal_cp(goal, mode, "criticState"),
                               self._goal_key(media_id, goal, mode), "criticState", AgentState)

    def load_stage(self, media_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL) -> TaskStage | None:
        return self._repo.read_stage(media_id, self._goal_cp(goal, mode, "stage"),
                                     self._goal_key(media_id, goal, mode))

    def save_stage(self, media_id: int, goal: str, mode: AnalysisMode, stage: TaskStage) -> None:
        key = self._goal_key(media_id, goal, mode)
        self._repo.write_stage(media_id, self._goal_cp(goal, mode, "stage"), key, stage)
        self._remember_goal_key(media_id, key)

    def load_chunks(self, media_id: int) -> list[VideoChunk] | None:
        return self._repo.read(media_id, self._media_cp("chunks"), self._checkpoint_key(media_id),
                               "chunks", list[VideoChunk])

    # ---- 写 ----
    def save_context(self, media_id: int, context: VideoContext) -> None:
        reusable = VideoContext(source=context.source, user_goal="", segments=context.segments)
        self._repo.write(media_id, self._media_cp("context"), self._media_cp("stage"),
                         self._checkpoint_key(media_id), "context", TaskStage.CONTEXT_COMPLETED, reusable)

    def save_chunks(self, media_id: int, chunks: list[VideoChunk]) -> None:
        self._repo.write(media_id, self._media_cp("chunks"), self._media_cp("stage"),
                         self._checkpoint_key(media_id), "chunks", TaskStage.CHUNKS_COMPLETED, list(chunks))

    def save_result(self, media_id: int, state: AgentState, mode: AnalysisMode = AnalysisMode.GENERAL) -> None:
        stage = (TaskStage.ANALYSIS_COMPLETED if state.critique is not None and state.critique.passed
                 else TaskStage.ANALYSIS_COMPLETED_WITH_WARNINGS)
        key = self._goal_key(media_id, state.goal, mode)
        self._repo.write(media_id, self._goal_cp(state.goal, mode, "result"),
                         self._goal_cp(state.goal, mode, "stage"), key, "result", stage, state)
        self._remember_goal_key(media_id, key)

    def save_plan(self, media_id: int, goal: str, mode: AnalysisMode, plan: AgentPlan) -> None:
        key = self._goal_key(media_id, goal, mode)
        self._repo.write(media_id, self._goal_cp(goal, mode, "plan"), self._goal_cp(goal, mode, "stage"),
                         key, "plan", TaskStage.PLAN_COMPLETED, plan)
        self._remember_goal_key(media_id, key)

    def save_critic_state(self, media_id: int, state: AgentState,
                          mode: AnalysisMode = AnalysisMode.GENERAL) -> None:
        stage = (TaskStage.CRITIC_PASSED if state.critique is not None and state.critique.passed
                 else TaskStage.CRITIC_RETRY_REQUIRED)
        key = self._goal_key(media_id, state.goal, mode)
        self._repo.write(media_id, self._goal_cp(state.goal, mode, "criticState"),
                         self._goal_cp(state.goal, mode, "stage"), key, "criticState", stage, state)
        self._remember_goal_key(media_id, key)

    def save_execution_state(self, media_id: int, state: AgentState,
                             mode: AnalysisMode = AnalysisMode.GENERAL) -> None:
        key = self._goal_key(media_id, state.goal, mode)
        self._repo.write(media_id, self._goal_cp(state.goal, mode, "criticState"),
                         self._goal_cp(state.goal, mode, "stage"), key, "criticState",
                         TaskStage.EXECUTOR_COMPLETED, state)
        self._remember_goal_key(media_id, key)

    # ---- 修订 ----
    def stage_revision(self, media_id: int, goal: str, mode: AnalysisMode, plan: AgentPlan | None) -> None:
        key = self._revision_key(media_id, goal, mode)
        self._repo.write_standalone(media_id, self._revision_cp(goal, mode), key, "revision",
                                    TaskStage.REVISION_PENDING, RevisionCheckpoint(plan=plan, applied=False))
        self._remember_goal_key(media_id, key)

    def begin_staged_revision(self, media_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL) -> bool:
        key = self._revision_key(media_id, goal, mode)
        with self._repo.transaction():
            revision = self._repo.read(media_id, self._revision_cp(goal, mode), key, "revision",
                                       RevisionCheckpoint)
            if revision is None:
                return False
            if revision.applied:
                return True
            self._repo.delete_by_prefix(media_id, self._goal_cp(goal, mode, ""))
            self._redis.delete(self._goal_key(media_id, goal, mode))
            if revision.plan is not None:
                self.save_plan(media_id, goal, mode, revision.plan)
            self._repo.write_standalone(media_id, self._revision_cp(goal, mode), key, "revision",
                                        TaskStage.REVISION_APPLIED,
                                        RevisionCheckpoint(plan=revision.plan, applied=True))
            return True

    def complete_staged_revision(self, media_id: int, goal: str, mode: AnalysisMode) -> None:
        self._repo.delete(media_id, self._revision_cp(goal, mode), self._revision_key(media_id, goal, mode))

    def cancel_staged_revision(self, media_id: int, goal: str, mode: AnalysisMode = AnalysisMode.GENERAL) -> None:
        self._repo.delete(media_id, self._revision_cp(goal, mode), self._revision_key(media_id, goal, mode))

    # ---- 反馈 ----
    def save_feedback(self, feedback: AgentFeedback) -> None:
        from ..errors import AgentError
        try:
            key = self._feedback_key(feedback.media_id)
            self._redis.rpush(key, feedback.normalized().model_dump_json(by_alias=True))
            self._redis.ltrim(key, -MAX_FEEDBACK_SAMPLES, -1)
            self._redis.expire(key, FEEDBACK_TTL_SECONDS)
        except Exception as e:
            raise AgentError("保存 Agent 用户反馈失败") from e

    def load_feedback(self, media_id: int) -> list[AgentFeedback]:
        values = self._redis.lrange(self._feedback_key(media_id), 0, -1)
        out: list[AgentFeedback] = []
        for value in values or []:
            try:
                out.append(AgentFeedback.model_validate_json(value))
            except Exception:
                log.warning("agent_feedback_deserialize_failed mediaId=%s", media_id, exc_info=True)
        return out

    def save_failure(self, media_id: int, goal: str, mode: AnalysisMode, failed_stage: TaskStage,
                     error: BaseException) -> None:
        key = self._goal_key(media_id, goal, mode)
        self._repo.write_stage(media_id, self._goal_cp(goal, mode, "stage"), key, TaskStage.FAILED)
        self._redis.hset(key, "failedStage", failed_stage.name)
        self._redis.hset(key, "errorType", type(error).__name__)
        self._redis.expire(key, SEVEN_DAYS)
        self._remember_goal_key(media_id, key)

    def delete_media(self, media_id: int) -> None:
        self._repo.delete_by_media_id(media_id)
        try:
            goal_keys = self._redis.smembers(self._goal_index_key(media_id))
            keys = [self._checkpoint_key(media_id), self._feedback_key(media_id), self._goal_index_key(media_id)]
            keys.extend(goal_keys or [])
            self._redis.delete(*keys)
        except Exception:
            log.warning("agent_checkpoint_cache_cleanup_failed mediaId=%s", media_id, exc_info=True)

    def _remember_goal_key(self, media_id: int, key: str) -> None:
        try:
            self._redis.sadd(self._goal_index_key(media_id), key)
            self._redis.expire(self._goal_index_key(media_id), SEVEN_DAYS)
        except Exception:
            log.warning("agent_checkpoint_index_write_failed mediaId=%s key=%s", media_id, key, exc_info=True)

    # ---- key ----
    @staticmethod
    def _checkpoint_key(media_id: int) -> str:
        return f"agent:checkpoint:{media_id}"

    def _goal_key(self, media_id: int, goal: str, mode: AnalysisMode) -> str:
        return f"{self._checkpoint_key(media_id)}:goal:{goal_digest(goal, mode)}"

    @staticmethod
    def _feedback_key(media_id: int) -> str:
        return f"agent:feedback:{media_id}"

    def _revision_key(self, media_id: int, goal: str, mode: AnalysisMode) -> str:
        return self._goal_key(media_id, goal, mode) + ":revision"

    @staticmethod
    def _revision_cp(goal: str, mode: AnalysisMode) -> str:
        return f"revision:{goal_digest(goal, mode)}"

    def _goal_index_key(self, media_id: int) -> str:
        return self._checkpoint_key(media_id) + ":goals"

    @staticmethod
    def _media_cp(field: str) -> str:
        return f"media:{field}"

    @staticmethod
    def _goal_cp(goal: str, mode: AnalysisMode, field: str) -> str:
        return f"goal:{goal_digest(goal, mode)}:{field}"
