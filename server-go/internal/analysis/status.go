package analysis

import (
	"context"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/checkpoint"
	"dovideo/server/internal/model"
)

// StatusService answers "where is my analysis" from the result, the stage checkpoint and the
// active marker, in that order.
type StatusService struct {
	CP         *checkpoint.Store
	Agent      *agentclient.Client
	Dispatcher *Dispatcher
}

// LoadResult asks agent-py for the completed result; agent-py is only consulted when a result
// checkpoint exists, so status polling of running tasks does not depend on it.
func (s *StatusService) LoadResult(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (*agentclient.ResultResponse, error) {
	has, err := s.CP.HasResult(ctx, mediaID, goal, mode)
	if err != nil || !has {
		return nil, err
	}
	res, err := s.Agent.Result(ctx, mediaID, goal, mode)
	if err != nil {
		return nil, err
	}
	if !res.Found {
		return nil, nil
	}
	return res, nil
}

// Current is the user-facing status of (media, goal, mode).
func (s *StatusService) Current(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (model.TaskStatus, error) {
	res, err := s.LoadResult(ctx, mediaID, goal, mode)
	if err != nil {
		return model.TaskStatus{}, err
	}
	if res != nil && res.TaskStatus != nil {
		return *res.TaskStatus, nil
	}
	stage, err := s.CP.LoadStage(ctx, mediaID, goal, mode)
	if err != nil {
		return model.TaskStatus{}, err
	}
	active, err := s.Dispatcher.IsActive(ctx, mediaID, goal, mode)
	if err != nil {
		return model.TaskStatus{}, err
	}
	if active {
		state := model.StateProcessing
		if stage == "" {
			state = model.StateQueued
		}
		return model.StatusOf(state, StatusMessage(stage)), nil
	}
	if stage == model.StageBudgetExhausted {
		return model.StatusOf(model.StateFailed, "Agent 已达到本次任务预算，请调整目标后重试"), nil
	}
	if stage == model.StageFailed || stage == model.StageDeadLettered {
		return model.StatusOf(model.StateFailed, "分析失败，请稍后重试"), nil
	}
	return model.StatusOf(model.StateNotStarted, "尚未提交分析任务"), nil
}

// Stage is the last recorded stage.
func (s *StatusService) Stage(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (model.TaskStage, error) {
	return s.CP.LoadStage(ctx, mediaID, goal, mode)
}

// StatusMessage is the progress text shown for a stage.
func StatusMessage(stage model.TaskStage) string {
	switch stage {
	case "", model.StageQueued:
		return "任务已排队"
	case model.StageVideoContext, model.StageContextCompleted:
		return "正在解析视频语音和关键画面"
	case model.StageChunksCompleted:
		return "正在检索与目标相关的视频证据"
	case model.StagePlanCompleted:
		return "Planner 已完成任务拆解"
	case model.StageExecutorStarted, model.StageExecutorCompleted:
		return "Executor 正在生成结构化产物"
	case model.StageCriticStarted:
		return "Critic 正在核验结论和证据"
	case model.StageCriticRetryRequired, model.StageEvidenceRefreshed:
		return "正在根据 Critic 反馈补充证据"
	case model.StageRetrying:
		return "任务执行异常，正在自动重试"
	default:
		return "正在分析视频"
	}
}
