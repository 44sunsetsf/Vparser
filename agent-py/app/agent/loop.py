"""AgentLoopService: resume from checkpoints -> Planner -> Executor -> Critic -> evidence checks and
targeted re-retrieval, bounded by time / token / cost budgets."""
import logging
import time
from contextlib import contextmanager

from ..checkpoint.service import AgentCheckpointService
from ..errors import (
    BudgetExceededError,
    DeadlineExceededError,
    InvalidArgumentError,
    AgentError,
    cause_chain,
)
from ..events import TaskEventPublisher
from ..textutil import is_blank, trim
from ..llm.deepseek import DeepSeekClient
from ..models.dto import (
    AgentPlan,
    AgentState,
    AnalysisMode,
    AnalysisResult,
    CriticResult,
    Evidence,
    TaskStage,
    TaskState,
    TaskStatus,
    VideoContext,
)
from ..observability import BUDGET_TERMINATIONS, CRITIC_ROUNDS, span
from ..retrieval.long_context import LongVideoContextService
from ..telemetry import AgentTelemetry
from . import budget
from .evidence import EvidenceVerificationService
from .modes import ModeProfile

log = logging.getLogger("agent.loop")

MAX_PLAN_TASKS = 5


def _safe_list(values):
    return [] if values is None else values


def _mode_of(profile: ModeProfile | None) -> AnalysisMode:
    return AnalysisMode.GENERAL if profile is None else profile.mode


def _plan_instruction(profile) -> str:
    return "" if profile is None else profile.plan_instruction


def _execute_instruction(profile) -> str:
    return "" if profile is None else profile.execute_instruction


def _critic_instruction(profile) -> str:
    return "" if profile is None else profile.critic_instruction


def _find_deadline(error: BaseException) -> DeadlineExceededError | None:
    for current in cause_chain(error, 16):
        if isinstance(current, DeadlineExceededError):
            return current
    return None


class AgentLoopService:
    def __init__(self, deepseek: DeepSeekClient, long_context: LongVideoContextService,
                 checkpoint: AgentCheckpointService, telemetry: AgentTelemetry,
                 evidence_verification: EvidenceVerificationService, events: TaskEventPublisher,
                 max_rounds: int, max_duration_ms: int, max_estimated_tokens: int,
                 max_estimated_cost: float) -> None:
        if max_rounds < 1 or max_duration_ms < 1 or max_estimated_tokens < 1 or max_estimated_cost < 0:
            raise InvalidArgumentError("Agent 终止预算配置无效")
        self._deepseek = deepseek
        self._long_context = long_context
        self._checkpoint = checkpoint
        self._telemetry = telemetry
        self._evidence = evidence_verification
        self._events = events
        self.max_rounds = max_rounds
        self.max_duration_ms = max_duration_ms
        self.max_estimated_tokens = max_estimated_tokens
        self.max_estimated_cost = max_estimated_cost

    def run(self, media_id: int | None, context: VideoContext, profile: ModeProfile | None = None) -> AgentState:
        """profile 为空时等价于 GENERAL——三段模式指令均为空串,prompt 与引入模式体系前一致。"""
        self._validate_context(context)
        try:
            with budget.open_budget(self.max_duration_ms):
                return self._run_within_budget(media_id, context, profile)
        except BudgetExceededError:
            raise
        except Exception as e:
            deadline = _find_deadline(e)
            if deadline is None:
                raise
            self._telemetry.increment_current("budgetTerminations", 1)
            BUDGET_TERMINATIONS.labels("duration").inc()
            raise BudgetExceededError(str(deadline)) from e

    @contextmanager
    def _stage_span(self, name: str, stage: str, round_: int | None = None):
        """Span for one Planner / Executor / Critic step, tagged with the running token estimate."""
        with span(name, **{"agent.stage": stage, "agent.round": round_}) as s:
            try:
                yield s
            finally:
                s.set_attribute("agent.estimated_tokens", self._telemetry.current_usage().estimated_tokens)

    def _run_within_budget(self, media_id, context: VideoContext, profile) -> AgentState:
        run_started = time.monotonic_ns()
        mode = _mode_of(profile)
        saved = None if media_id is None else self._checkpoint.load_critic_state(media_id, context.user_goal, mode)
        terminal = (saved is not None and saved.result is not None and saved.critique is not None
                    and (saved.round >= self.max_rounds or saved.critique.passed))
        if terminal and self._is_plan_valid(saved.plan) and self._is_result_valid(saved.result, profile):
            self._checkpoint.save_result(media_id, saved, mode)
            self._telemetry.increment_current("terminalCheckpointHits", 1)
            return saved
        if terminal:
            self._telemetry.increment_current("invalidTerminalCheckpointRepairs", 1)
            saved = AgentState(goal=saved.goal, plan=saved.plan, result=saved.result,
                               critique=saved.critique, round=0)

        relevant = self._long_context.select_relevant(media_id, context)
        plan = self._resolve_plan(media_id, relevant, saved, profile)
        self._check_budget(run_started, "Planner")
        if media_id is not None:
            self._events.publish_analysis(media_id, relevant.user_goal, mode,
                                          TaskStatus.of(TaskState.PROCESSING, "Planner 已完成任务拆解"),
                                          TaskStage.PLAN_COMPLETED)
        state = AgentState(goal=relevant.user_goal, plan=plan, result=None, critique=None, round=0) \
            if saved is None else saved
        if state.critique is not None and not state.critique.passed:
            relevant = self._context_for_retry(media_id, context, relevant, state.critique, profile)
            plan = self._revise_plan_for_retry(media_id, relevant, plan, state.critique, profile)

        # 若 Executor 草稿已落盘(上次在 Critic 前中断),重试直接从 Critic 接着走,避免重复生成整份产物。
        if state.result is not None and state.critique is None and state.round > 0:
            self._telemetry.increment_current("criticCheckpointResumes", 1)
            self._check_budget(run_started, "Executor Checkpoint")
            state = self._critique_round(media_id, relevant, plan, state.result, state.round, profile)
            if not state.critique.passed and state.round < self.max_rounds:
                relevant = self._context_for_retry(media_id, context, relevant, state.critique, profile)
                plan = self._revise_plan_for_retry(media_id, relevant, plan, state.critique, profile)

        for round_ in range(state.round + 1, self.max_rounds + 1):
            self._check_budget(run_started, f"Agent Round {round_}")
            state = self._execute_round(media_id, relevant, plan, state.critique, round_, run_started, profile)
            if state.critique.passed:
                break
            if round_ < self.max_rounds:
                relevant = self._context_for_retry(media_id, context, relevant, state.critique, profile)
                plan = self._revise_plan_for_retry(media_id, relevant, plan, state.critique, profile)
        self._validate_result(state.result, profile)
        if media_id is not None:
            self._checkpoint.save_result(media_id, state, mode)
        return state

    def _resolve_plan(self, media_id, context: VideoContext, saved: AgentState | None, profile) -> AgentPlan:
        plan = None if media_id is None else self._checkpoint.load_plan(media_id, context.user_goal,
                                                                        _mode_of(profile))
        if plan is None and saved is not None:
            plan = saved.plan
        should_persist = False
        if plan is None:
            with self._stage_span("agent.planner", "PLANNER"):
                plan = self._deepseek.plan(context, _plan_instruction(profile))
            should_persist = True
        if not self._is_plan_valid(plan):
            with self._stage_span("agent.planner", "PLANNER_REPAIR"):
                plan = self._deepseek.repair_plan(context, plan, _plan_instruction(profile))
            self._telemetry.increment_current("planStructureRepairs", 1)
            should_persist = True
        self._validate_plan(plan)
        if media_id is not None and should_persist:
            self._checkpoint.save_plan(media_id, context.user_goal, _mode_of(profile), plan)
        return plan

    def _execute_round(self, media_id, context: VideoContext, plan: AgentPlan,
                       previous_critique: CriticResult | None, round_: int, run_started: int,
                       profile) -> AgentState:
        mode = _mode_of(profile)
        self._publish_stage(media_id, context.user_goal, mode, "Executor 正在按计划生成结构化产物",
                            TaskStage.EXECUTOR_STARTED)
        with self._stage_span("agent.executor", "EXECUTOR", round_):
            result = self._deepseek.execute(context, plan, previous_critique, _execute_instruction(profile))
        draft = AgentState(goal=context.user_goal, plan=plan, result=result, critique=None, round=round_)
        if media_id is not None:
            self._checkpoint.save_execution_state(media_id, draft, mode)
            self._publish_stage(media_id, context.user_goal, mode, "Executor 草稿已保存，开始校验证据",
                                TaskStage.EXECUTOR_COMPLETED)
        self._check_budget(run_started, "Executor")
        return self._critique_round(media_id, context, plan, result, round_, profile)

    def _critique_round(self, media_id, context: VideoContext, plan: AgentPlan, result: AnalysisResult,
                        round_: int, profile) -> AgentState:
        mode = _mode_of(profile)
        self._publish_stage(media_id, context.user_goal, mode, "Critic 正在核验目标覆盖与时间戳证据",
                            TaskStage.CRITIC_STARTED)
        with self._stage_span("agent.critic", "CRITIC", round_) as s:
            critique = self._normalize_critique(
                self._deepseek.critique(context, plan, result, _critic_instruction(profile)))
            critique = self._enforce_structure_bounds(result, critique, profile)
            critique = self._enforce_evidence_bounds(context, result, critique)
            s.set_attribute("agent.critic_passed", critique.passed)
        CRITIC_ROUNDS.labels("true" if critique.passed else "false").inc()
        self._telemetry.increment_current("criticRounds", 1)
        if critique.passed:
            self._telemetry.increment_current("criticPassed", 1)

        state = AgentState(goal=context.user_goal, plan=plan, result=result, critique=critique, round=round_)
        if media_id is not None:
            self._checkpoint.save_critic_state(media_id, state, mode)
            if critique.passed:
                message, stage = "Critic 校验通过，正在整理结构化结果", TaskStage.CRITIC_PASSED
            elif round_ >= self.max_rounds:
                message, stage = ("Critic 达到最大校验轮次，正在保留警告并生成结果",
                                  TaskStage.ANALYSIS_COMPLETED_WITH_WARNINGS)
            elif self._requires_evidence_refresh(critique):
                message, stage = "Critic 发现证据缺口，正在定向补充证据", TaskStage.CRITIC_RETRY_REQUIRED
            else:
                message, stage = "Critic 发现目标覆盖或结构问题，正在按反馈重写", TaskStage.CRITIC_RETRY_REQUIRED
            self._publish_stage(media_id, context.user_goal, mode, message, stage)
        return state

    # ---- 校验 ----
    @staticmethod
    def _validate_context(context: VideoContext | None) -> None:
        if (context is None or context.user_goal is None or is_blank(context.user_goal)
                or not context.segments or any(s is None for s in context.segments)):
            raise InvalidArgumentError("Agent 需要目标和至少一个视频片段")

    def _validate_plan(self, plan) -> None:
        if not self._is_plan_valid(plan):
            raise AgentError("Planner 返回了无效任务列表")

    @staticmethod
    def _is_plan_valid(plan: AgentPlan | None) -> bool:
        return (plan is not None and plan.understood_goal is not None and not is_blank(plan.understood_goal)
                and plan.tasks is not None and len(plan.tasks) > 0 and len(plan.tasks) <= MAX_PLAN_TASKS
                and all(not (t is None or is_blank(t) or len(t) > 500) for t in plan.tasks))

    def _validate_result(self, result, profile) -> None:
        if not self._is_result_valid(result, profile):
            raise AgentError("Executor 未生成完整结构化结果")

    @staticmethod
    def _is_result_valid(result: AnalysisResult | None, profile: ModeProfile | None) -> bool:
        common_valid = (result is not None and result.title is not None and not is_blank(result.title)
                        and result.conclusions is not None and len(result.conclusions) > 0
                        and result.evidence is not None and len(result.evidence) > 0)
        if not common_valid or profile is None or not profile.required_section_keys:
            return common_valid
        present = list(dict.fromkeys(
            trim(s.key) for s in result.sections
            if s is not None and s.key is not None and not is_blank(s.key) and s.items))
        return all(k in present for k in profile.required_section_keys)

    def _enforce_evidence_bounds(self, context: VideoContext, result: AnalysisResult | None,
                                 critique: CriticResult) -> CriticResult:
        critique = self._normalize_critique(critique)
        has_declared = bool(critique.feedback or critique.missing_requirements
                            or critique.unsupported_claims or critique.required_timestamps)
        if critique.passed and has_declared:
            critique = CriticResult(passed=False, feedback=critique.feedback,
                                    missing_requirements=critique.missing_requirements,
                                    unsupported_claims=critique.unsupported_claims,
                                    required_timestamps=critique.required_timestamps)
        if (not critique.passed and not critique.feedback and not critique.missing_requirements
                and not critique.unsupported_claims and not critique.required_timestamps):
            critique = CriticResult(passed=False, feedback=["重新检查目标覆盖、结构完整性和证据绑定"])
        if result is None or result.evidence is None or not result.evidence:
            return critique
        invalid_evidence: list[Evidence] = [e for e in result.evidence
                                            if not self._evidence.supported(context, e)]
        unsupported_claims = [c for c in result.conclusions
                              if not any(self._evidence.supports_claim(context, c, e) for e in result.evidence)]
        if not invalid_evidence and not unsupported_claims:
            return critique

        unsupported = list(critique.unsupported_claims)
        for claim in unsupported_claims:
            if claim not in unsupported:
                unsupported.append(claim)
        for e in invalid_evidence:
            unsupported.append(f"证据无法在原始 ASR/OCR 中核验: {e.timestamp_ms}")
        feedback = list(critique.feedback)
        feedback.append("为每条结论重新检索并绑定可核验的时间戳证据")
        required = list(critique.required_timestamps)
        for e in invalid_evidence:
            if e.timestamp_ms not in required:
                required.append(e.timestamp_ms)
        return CriticResult(passed=False, feedback=feedback, missing_requirements=critique.missing_requirements,
                            unsupported_claims=unsupported, required_timestamps=required)

    def _enforce_structure_bounds(self, result: AnalysisResult | None, critique: CriticResult,
                                  profile) -> CriticResult:
        critique = self._normalize_critique(critique)
        feedback = list(critique.feedback)
        if result is None or result.title is None or is_blank(result.title):
            feedback.append("补充明确的产物标题")
        if result is None or result.conclusions is None or not result.conclusions:
            feedback.append("补充覆盖 Planner 任务的核心结论")
        if result is None or result.evidence is None or not result.evidence:
            feedback.append("为核心结论补充带时间戳的 ASR 或 OCR 证据")
        missing_sections = self._missing_section_keys(result, profile)
        if missing_sections:
            feedback.append("补充当前分析模式要求的结构化段落: " + ", ".join(missing_sections))
        if feedback == critique.feedback:
            return critique
        return CriticResult(passed=False, feedback=feedback, missing_requirements=critique.missing_requirements,
                            unsupported_claims=critique.unsupported_claims,
                            required_timestamps=critique.required_timestamps)

    def _context_for_retry(self, media_id, full_context: VideoContext, selected: VideoContext,
                           critique: CriticResult, profile) -> VideoContext:
        if not self._requires_evidence_refresh(critique):
            self._telemetry.increment_current("criticRewriteOnlyRetries", 1)
            return selected
        self._telemetry.increment_current("criticEvidenceRefreshes", 1)
        refined = self._long_context.refine_for_critique(media_id, full_context, selected, critique)
        self._publish_stage(media_id, full_context.user_goal, _mode_of(profile), "已按 Critic 反馈补充定向证据",
                            TaskStage.EVIDENCE_REFRESHED)
        return refined

    @staticmethod
    def _requires_evidence_refresh(critique: CriticResult | None) -> bool:
        return critique is not None and bool(
            _safe_list(critique.required_timestamps) or _safe_list(critique.missing_requirements)
            or _safe_list(critique.unsupported_claims))

    def _revise_plan_for_retry(self, media_id, context: VideoContext, current_plan: AgentPlan,
                               critique: CriticResult | None, profile) -> AgentPlan:
        if critique is None or not _safe_list(critique.missing_requirements):
            return current_plan
        try:
            with self._stage_span("agent.planner", "REPLANNER"):
                revised = self._deepseek.replan(context, current_plan, critique, _plan_instruction(profile))
            self._validate_plan(revised)
            self._telemetry.increment_current("planRevisions", 1)
            if media_id is not None:
                self._checkpoint.save_plan(media_id, context.user_goal, _mode_of(profile), revised)
                self._events.publish_analysis(
                    media_id, context.user_goal, _mode_of(profile),
                    TaskStatus.of(TaskState.PROCESSING, "Planner 根据 Critic 反馈补充了遗漏任务"),
                    TaskStage.PLAN_COMPLETED)
            return revised
        except Exception:
            self._telemetry.increment_current("planRevisionFallbacks", 1)
            log.warning("agent_replan_failed mediaId=%s, fallback to current plan", media_id, exc_info=True)
            return current_plan

    def _publish_stage(self, media_id, goal: str, mode: AnalysisMode, message: str, stage: TaskStage) -> None:
        if media_id is None:
            return
        self._events.publish_analysis(media_id, goal, mode, TaskStatus.of(TaskState.PROCESSING, message), stage)

    @staticmethod
    def _missing_section_keys(result: AnalysisResult | None, profile: ModeProfile | None) -> list[str]:
        if profile is None or not profile.required_section_keys:
            return []
        present = [] if result is None or result.sections is None else list(dict.fromkeys(
            trim(s.key) for s in result.sections
            if s is not None and s.key is not None and not is_blank(s.key)
            and s.items is not None and s.items))
        return [k for k in profile.required_section_keys if k not in present]

    def _check_budget(self, started_nanos: int, completed_stage: str) -> None:
        budget.check(completed_stage)
        elapsed_ms = (time.monotonic_ns() - started_nanos) // 1_000_000
        usage = self._telemetry.current_usage()
        reason = kind = None
        if elapsed_ms > self.max_duration_ms:
            reason, kind = f"Agent 超过最大执行时长 {self.max_duration_ms}ms", "duration"
        elif usage.estimated_tokens > self.max_estimated_tokens:
            reason, kind = f"Agent 超过最大 Token 预算 {self.max_estimated_tokens}", "tokens"
        elif self.max_estimated_cost > 0 and usage.estimated_cost > self.max_estimated_cost:
            reason, kind = f"Agent 超过最大成本预算 {_format_float(self.max_estimated_cost)}", "cost"
        if reason is None:
            return
        self._telemetry.increment_current("budgetTerminations", 1)
        BUDGET_TERMINATIONS.labels(kind).inc()
        raise BudgetExceededError(f"{completed_stage} 后终止：{reason}")

    @staticmethod
    def _normalize_critique(critique: CriticResult | None) -> CriticResult:
        if critique is None:
            return CriticResult(passed=False, feedback=["Critic 未返回有效结果"])
        return CriticResult(passed=critique.passed, feedback=_safe_list(critique.feedback),
                            missing_requirements=_safe_list(critique.missing_requirements),
                            unsupported_claims=_safe_list(critique.unsupported_claims),
                            required_timestamps=_safe_list(critique.required_timestamps))


def _format_float(value: float) -> str:
    """Shortest round-trip form; whole numbers keep a trailing .0 (e.g. 2.0)."""
    return repr(float(value))
