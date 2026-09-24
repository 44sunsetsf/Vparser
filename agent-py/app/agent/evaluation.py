"""Result quality metrics (per media) and the offline golden-task evaluation run at startup."""
import json
import logging
from pathlib import Path

from ..checkpoint.service import AgentCheckpointService
from ..textutil import is_blank
from ..models.dto import AgentFeedback, AgentState, AnalysisMode, AnalysisResult, VideoContext
from .evidence import EvidenceVerificationService

log = logging.getLogger("agent.evaluation")


class AgentEvaluationService:
    def __init__(self, checkpoint: AgentCheckpointService, evidence: EvidenceVerificationService) -> None:
        self._checkpoint = checkpoint
        self._evidence = evidence

    def evaluate_media(self, media_id: int, goal: str, mode: AnalysisMode | None = AnalysisMode.GENERAL) -> dict:
        resolved = AnalysisMode.GENERAL if mode is None else mode
        context = self._checkpoint.load_context(media_id)
        state = self._checkpoint.load_result(media_id, goal, resolved)
        if state is None:
            state = self._checkpoint.load_critic_state(media_id, goal, resolved)

        feedback = [f for f in self._checkpoint.load_feedback(media_id)
                    if (goal == f.goal or goal == f.corrected_goal)
                    and AnalysisMode.from_nullable(f.mode) == resolved]
        metrics = dict(self.evaluate(context, state))
        metrics["userAcceptanceRate"] = self._user_acceptance_rate(feedback)
        metrics["feedbackSamples"] = len(feedback)
        return metrics

    def evaluate(self, context: VideoContext | None, state: AgentState | None) -> dict:
        result = None if state is None else state.result
        return {
            "structuredValid": self._structured_valid(result),
            "timestampCoverageRate": self._timestamp_coverage_rate(context, result),
            "evidenceSupportRate": self._evidence_support_rate(context, result),
            "claimEvidenceSupportRate": self._claim_evidence_support_rate(context, result),
            "criticPassed": state is not None and state.critique is not None and state.critique.passed,
        }

    @staticmethod
    def _structured_valid(result: AnalysisResult | None) -> bool:
        return (result is not None and result.title is not None and not is_blank(result.title)
                and bool(result.conclusions) and bool(result.evidence))

    def _evidence_support_rate(self, context, result) -> float:
        if context is None or result is None or not result.evidence:
            return 0.0
        supported = sum(1 for e in result.evidence if self._evidence.supported(context, e))
        return supported / len(result.evidence)

    def _timestamp_coverage_rate(self, context, result) -> float:
        if context is None or result is None or not result.evidence:
            return 0.0
        covered = sum(1 for e in result.evidence if self._evidence.timestamp_covered(context, e))
        return covered / len(result.evidence)

    def _claim_evidence_support_rate(self, context, result) -> float:
        if context is None or result is None or not result.conclusions:
            return 0.0
        supported = sum(1 for claim in result.conclusions
                        if any(self._evidence.supports_claim(context, claim, e) for e in result.evidence))
        return supported / len(result.conclusions)

    @staticmethod
    def _user_acceptance_rate(feedback: list[AgentFeedback]) -> float:
        rated = [f for f in feedback if f.rating is not None]
        if not rated:
            return 0.0
        return sum(1 for f in rated if f.rating > 0) / len(rated)


GOLDEN_PATH = Path(__file__).resolve().parent.parent / "evaluation" / "golden-video-tasks.json"


def _keyword_coverage(output: str, expected: list[str]) -> float:
    if not expected:
        return 1.0
    normalized = output.lower()
    return sum(1 for k in expected if k.lower() in normalized) / len(expected)


def run_offline_evaluation(agent_loop, evaluation: AgentEvaluationService, telemetry) -> int:
    """Runs every golden task through the agent loop, logs metrics and returns how many passed."""
    tasks = json.loads(GOLDEN_PATH.read_text(encoding="utf-8"))
    passed = 0
    for index, raw in enumerate(tasks):
        name = raw.get("name")
        if name is None or is_blank(name):
            raise ValueError("task name is required")
        if raw.get("context") is None:
            raise ValueError("task context is required")
        context = VideoContext.model_validate(raw["context"])
        expected = raw.get("expectedKeywords") or []
        trace_id = telemetry.start(-1 - index, context.user_goal)
        telemetry.bind(trace_id)
        try:
            state = agent_loop.run(None, context, None)
            metrics = evaluation.evaluate(context, state)
            coverage = _keyword_coverage(state.result.to_markdown(), expected)
            success = (metrics["structuredValid"] is True and metrics["claimEvidenceSupportRate"] >= 0.8
                       and coverage >= 0.8)
            if success:
                passed += 1
            log.info("offline_agent_evaluation name=%s success=%s keywordCoverage=%s metrics=%s",
                     name, success, coverage, metrics)
        except Exception:
            log.warning("offline_agent_evaluation_failed name=%s", name, exc_info=True)
        finally:
            telemetry.flush(trace_id)
            telemetry.clear()
    log.info("offline_agent_evaluation_completed passed=%s total=%s", passed, len(tasks))
    return passed
