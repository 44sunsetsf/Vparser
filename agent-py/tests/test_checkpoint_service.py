from unittest.mock import MagicMock

from app.checkpoint.service import AgentCheckpointService, RevisionCheckpoint
from app.models.dto import AgentPlan, AnalysisMode, TaskStage


def test_persists_revision_before_replacing_goal_checkpoints():
    redis = MagicMock()
    repo = MagicMock()
    repo.transaction.return_value.__exit__.return_value = False
    service = AgentCheckpointService(redis, repo)
    plan = AgentPlan(understood_goal="重新审查", tasks=["核验第一条结论"])

    service.stage_revision(7, "审查视频", AnalysisMode.REVIEW, plan)

    args = repo.write_standalone.call_args.args
    assert args[0] == 7 and args[3] == "revision" and args[4] == TaskStage.REVISION_PENDING
    revision = args[5]
    assert revision == RevisionCheckpoint(plan=plan, applied=False)
    repo.read.return_value = revision

    assert service.begin_staged_revision(7, "审查视频", AnalysisMode.REVIEW)
    repo.delete_by_prefix.assert_called_once()
    repo.write.assert_called_once()
    w = repo.write.call_args.args
    assert w[0] == 7 and w[4] == "plan" and w[5] == TaskStage.PLAN_COMPLETED and w[6] == plan
    last = repo.write_standalone.call_args.args
    assert last[3] == "revision" and last[4] == TaskStage.REVISION_APPLIED and last[5].applied is True
