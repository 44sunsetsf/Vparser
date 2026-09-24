"""AgentService gRPC servicer: request validation, deadline -> budget, error -> status code.

Business logic lives in the domain services; every RPC here is a thin adapter run through ``_handle``.
"""
import logging
from collections.abc import Callable
from typing import TypeVar

import grpc

from ..agent import budget
from ..config import Settings
from ..container import Container
from ..errors import InvalidArgumentError
from ..gen.agent.v1 import agent_pb2 as pb
from ..gen.agent.v1 import agent_pb2_grpc
from ..models.dto import AnalysisMode, TaskStatus
from ..observability import AGENT_RUN_DURATION, Stopwatch
from ..textutil import is_blank
from . import convert as cv
from .status import status_for

log = logging.getLogger("agent.rpc")

T = TypeVar("T")

# The agent gives up this long before the caller's deadline, so the caller always receives our status
# (and never waits on work nobody will read).
DEADLINE_SAFETY_SECONDS = 2.0
# grpc reports "no deadline" as None or as an effectively infinite number, depending on the version.
_NO_DEADLINE = 1e9
_EXPECTED_CODES = {grpc.StatusCode.INVALID_ARGUMENT, grpc.StatusCode.FAILED_PRECONDITION,
                   grpc.StatusCode.RESOURCE_EXHAUSTED}
_BUDGET_TRAILER = (("x-agent-error", "BUDGET_EXHAUSTED"),)


def execution_budget_ms(max_duration_ms: int, time_remaining: float | None) -> tuple[int, bool]:
    """Returns (budget_ms, capped_by_deadline): min(AGENT_MAX_DURATION_MS, remaining - safety margin).
    A result <= 0 means the call cannot finish in time."""
    if time_remaining is None or time_remaining >= _NO_DEADLINE:
        return max_duration_ms, False
    deadline_ms = int((time_remaining - DEADLINE_SAFETY_SECONDS) * 1000)
    if deadline_ms < max_duration_ms:
        return deadline_ms, True
    return max_duration_ms, False


def _mode_label(value: int) -> str:
    try:
        return cv.mode_from_proto(value).value
    except InvalidArgumentError:
        return "INVALID"


def _goal(value: str, field: str = "goal") -> str:
    if is_blank(value):
        raise InvalidArgumentError(f"{field} is required")
    return value


class AgentServicer(agent_pb2_grpc.AgentServiceServicer):
    def __init__(self, container: Container, settings: Settings) -> None:
        self._c = container
        self._max_duration_ms = settings.agent_max_duration_ms

    # ---------- plumbing ----------
    def _handle(self, context: grpc.ServicerContext, fn: Callable[[], T], run_mode: str | None = None) -> T:
        remaining = context.time_remaining()
        budget_ms, capped = execution_budget_ms(self._max_duration_ms, remaining)
        if budget_ms <= 0:
            context.abort(grpc.StatusCode.DEADLINE_EXCEEDED,
                          f"deadline leaves less than {DEADLINE_SAFETY_SECONDS:g}s to work with")
        watch = Stopwatch()
        outcome = "ok"
        try:
            with budget.open_budget(budget_ms):
                return fn()
        except Exception as e:
            code = status_for(e)
            if code == grpc.StatusCode.RESOURCE_EXHAUSTED and capped:
                # The time budget was the caller's deadline, not ours: report it as a (retryable) deadline.
                code = grpc.StatusCode.DEADLINE_EXCEEDED
            outcome = code.name.lower()
            if code in _EXPECTED_CODES:
                log.info("rpc_rejected code=%s error=%s", code.name, e)
            else:
                log.error("rpc_failed code=%s", code.name, exc_info=e)
            if code == grpc.StatusCode.RESOURCE_EXHAUSTED:
                # Server overload also yields RESOURCE_EXHAUSTED; this trailer marks a real budget stop.
                context.set_trailing_metadata(_BUDGET_TRAILER)
            details = str(e) or type(e).__name__
        finally:
            if run_mode is not None:
                AGENT_RUN_DURATION.labels(run_mode, outcome).observe(watch.seconds())
        context.abort(code, details)

    # ---------- analysis ----------
    def Run(self, request: pb.RunRequest, context) -> pb.RunResponse:
        def run():
            mode = cv.mode_from_proto(request.mode)
            seed = cv.seed_from_proto(request)
            state = self._c.agent.run(cv.media_id(request.media_id), _goal(request.goal), mode,
                                      trace_id=None if seed is None else seed["traceId"], seed=seed)
            return pb.RunResponse(state=cv.state_to_proto(state), markdown=state.result.to_markdown(),
                                  task_status=cv.task_status_to_proto(TaskStatus.completed_from_state(state)))
        return self._handle(context, run, run_mode=_mode_label(request.mode))

    def GetResult(self, request: pb.GoalRef, context) -> pb.ResultResponse:
        def get():
            state = self._c.checkpoint.load_result(cv.media_id(request.media_id), _goal(request.goal),
                                                   cv.mode_from_proto(request.mode))
            if state is None or state.result is None:
                return pb.ResultResponse(found=False)
            return pb.ResultResponse(found=True, state=cv.state_to_proto(state), markdown=state.result.to_markdown(),
                                     task_status=cv.task_status_to_proto(TaskStatus.completed_from_state(state)))
        return self._handle(context, get)

    def ReuseResult(self, request: pb.ReuseRequest, context) -> pb.ReuseResponse:
        def reuse():
            if not request.target_source:
                raise InvalidArgumentError("target_source is required")
            state = self._c.agent.reuse_result(
                cv.media_id(request.media_id), cv.media_id(request.source_media_id, "source_media_id"),
                _goal(request.goal), cv.mode_from_proto(request.mode), request.target_source)
            if state is None:
                return pb.ReuseResponse(reused=False)
            return pb.ReuseResponse(reused=True, markdown=state.result.to_markdown(),
                                    task_status=cv.task_status_to_proto(TaskStatus.completed_from_state(state)))
        return self._handle(context, reuse)

    def FollowUp(self, request: pb.FollowUpRequest, context) -> pb.FollowUpResponse:
        def follow_up():
            goal = request.goal if request.HasField("goal") else None
            answer = self._c.follow_up.answer(cv.media_id(request.media_id), goal, request.question,
                                              cv.mode_from_proto(request.mode))
            return pb.FollowUpResponse(answer=answer)
        return self._handle(context, follow_up)

    def SearchEvidence(self, request: pb.SearchEvidenceRequest, context) -> pb.SearchEvidenceResponse:
        def search():
            hits = self._c.agent.search_evidence(cv.media_id(request.media_id), _goal(request.query, "query"))
            return pb.SearchEvidenceResponse(hits=[cv.hit_to_proto(h) for h in hits])
        return self._handle(context, search)

    def ClassifyMode(self, request: pb.ClassifyModeRequest, context) -> pb.RouteDecision:
        return self._handle(context, lambda: cv.route_to_proto(self._c.router.route(request.goal)))

    # ---------- revisions ----------
    def StageRevision(self, request: pb.StageRevisionRequest, context) -> pb.StageRevisionResponse:
        def stage():
            if not request.HasField("feedback"):
                raise InvalidArgumentError("feedback is required")
            feedback = cv.feedback_from_proto(request.feedback)
            cv.media_id(feedback.media_id or 0)
            _goal(feedback.goal or "")
            self._c.agent.stage_revision(feedback, cv.mode_from_proto(request.mode))
            return pb.StageRevisionResponse(goal=self._c.agent.revision_goal(feedback))
        return self._handle(context, stage)

    def BeginRevision(self, request: pb.GoalRef, context) -> pb.BeginRevisionResponse:
        return self._handle(context, lambda: pb.BeginRevisionResponse(begun=self._c.agent.begin_staged_revision(
            *self._goal_ref(request))))

    def CompleteRevision(self, request: pb.GoalRef, context) -> pb.Empty:
        return self._handle(context, lambda: self._empty(
            self._c.agent.complete_staged_revision(*self._goal_ref(request))))

    def CancelRevision(self, request: pb.GoalRef, context) -> pb.Empty:
        return self._handle(context, lambda: self._empty(
            self._c.agent.cancel_staged_revision(*self._goal_ref(request))))

    # ---------- feedback ----------
    def SaveFeedback(self, request: pb.AgentFeedback, context) -> pb.Empty:
        def save():
            feedback = cv.feedback_from_proto(request)
            cv.media_id(feedback.media_id or 0)
            self._c.checkpoint.save_feedback(feedback.normalized(AnalysisMode(feedback.mode)))
            return pb.Empty()
        return self._handle(context, save)

    def ListFeedback(self, request: pb.MediaRef, context) -> pb.FeedbackList:
        return self._handle(context, lambda: pb.FeedbackList(items=[
            cv.feedback_to_proto(f) for f in self._c.checkpoint.load_feedback(cv.media_id(request.media_id))]))

    # ---------- inspection ----------
    def GetPlan(self, request: pb.GoalRef, context) -> pb.PlanResponse:
        def get():
            plan = self._c.checkpoint.load_plan(*self._goal_ref(request))
            return pb.PlanResponse(found=False) if plan is None else pb.PlanResponse(
                found=True, plan=cv.plan_to_proto(plan))
        return self._handle(context, get)

    def GetTrace(self, request: pb.GoalRef, context):
        return self._handle(context, lambda: cv.to_struct(self._c.telemetry.latest(*self._goal_ref(request))))

    def GetEvaluation(self, request: pb.GoalRef, context):
        return self._handle(context, lambda: cv.to_struct(
            self._c.evaluation.evaluate_media(*self._goal_ref(request))))

    def Purge(self, request: pb.MediaRef, context) -> pb.Empty:
        def purge():
            media_id = cv.media_id(request.media_id)
            self._c.checkpoint.delete_media(media_id)
            self._c.telemetry.delete_task(media_id)
            self._c.vector_store.delete_media(media_id)
            return pb.Empty()
        return self._handle(context, purge)

    # ---------- helpers ----------
    @staticmethod
    def _goal_ref(request: pb.GoalRef) -> tuple[int, str, AnalysisMode]:
        return cv.media_id(request.media_id), _goal(request.goal), cv.mode_from_proto(request.mode)

    @staticmethod
    def _empty(_: object = None) -> pb.Empty:
        return pb.Empty()
