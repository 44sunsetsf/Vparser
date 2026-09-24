// Package model contains entities and DTOs; JSON field names are the contract with the web client.
package model

import (
	"strings"

	"dovideo/server/internal/common"
)

// AnalysisMode selects the analysis template.
type AnalysisMode string

const (
	ModeGeneral  AnalysisMode = "GENERAL"
	ModeLearning AnalysisMode = "LEARNING"
	ModeReview   AnalysisMode = "REVIEW"
	ModeCreation AnalysisMode = "CREATION"
)

func validMode(s string) (AnalysisMode, bool) {
	switch m := AnalysisMode(strings.ToUpper(strings.TrimSpace(s))); m {
	case ModeGeneral, ModeLearning, ModeReview, ModeCreation:
		return m, true
	}
	return "", false
}

// ModeFromNullable falls back to GENERAL for null/blank/unknown values.
func ModeFromNullable(value string) AnalysisMode {
	if strings.TrimSpace(value) == "" {
		return ModeGeneral
	}
	if m, ok := validMode(value); ok {
		return m
	}
	return ModeGeneral
}

// ModeFromRequest rejects unknown modes with an invalid-argument error.
func ModeFromRequest(value string) (AnalysisMode, error) {
	if strings.TrimSpace(value) == "" {
		return ModeGeneral, nil
	}
	if m, ok := validMode(value); ok {
		return m, nil
	}
	return "", common.InvalidArgument("不支持的分析模式: " + value)
}

// Resolve maps an empty mode to GENERAL.
func (m AnalysisMode) Resolve() AnalysisMode {
	if m == "" {
		return ModeGeneral
	}
	return m
}

// TaskStage is the fine-grained progress of a task; the empty string means "unknown" (JSON null).
type TaskStage string

const (
	StageQueued                    TaskStage = "QUEUED"
	StageConsuming                 TaskStage = "CONSUMING"
	StageVideoContext              TaskStage = "VIDEO_CONTEXT"
	StageContextCompleted          TaskStage = "CONTEXT_COMPLETED"
	StageChunksCompleted           TaskStage = "CHUNKS_COMPLETED"
	StageRetrieval                 TaskStage = "RETRIEVAL"
	StageAgentLoop                 TaskStage = "AGENT_LOOP"
	StagePlanCompleted             TaskStage = "PLAN_COMPLETED"
	StageExecutorStarted           TaskStage = "EXECUTOR_STARTED"
	StageExecutorCompleted         TaskStage = "EXECUTOR_COMPLETED"
	StageCriticStarted             TaskStage = "CRITIC_STARTED"
	StageCriticPassed              TaskStage = "CRITIC_PASSED"
	StageCriticRetryRequired       TaskStage = "CRITIC_RETRY_REQUIRED"
	StageEvidenceRefreshed         TaskStage = "EVIDENCE_REFRESHED"
	StageAnalysisCompleted         TaskStage = "ANALYSIS_COMPLETED"
	StageAnalysisCompletedWarnings TaskStage = "ANALYSIS_COMPLETED_WITH_WARNINGS"
	StageBudgetExhausted           TaskStage = "BUDGET_EXHAUSTED"
	StageRetrying                  TaskStage = "RETRYING"
	StageCompleted                 TaskStage = "COMPLETED"
	StageCompletedReused           TaskStage = "COMPLETED_REUSED"
	StageFailed                    TaskStage = "FAILED"
	StageDeadLettered              TaskStage = "DEAD_LETTERED"
	StageManualReplay              TaskStage = "MANUAL_REPLAY"
	StageRevisionPending           TaskStage = "REVISION_PENDING"
	StageRevisionApplied           TaskStage = "REVISION_APPLIED"
	StageTranscription             TaskStage = "TRANSCRIPTION"
	StageASR                       TaskStage = "ASR"
	StageDispatchFailed            TaskStage = "DISPATCH_FAILED"
)

var allStages = map[TaskStage]bool{}

func init() {
	for _, s := range []TaskStage{StageQueued, StageConsuming, StageVideoContext, StageContextCompleted,
		StageChunksCompleted, StageRetrieval, StageAgentLoop, StagePlanCompleted, StageExecutorStarted,
		StageExecutorCompleted, StageCriticStarted, StageCriticPassed, StageCriticRetryRequired,
		StageEvidenceRefreshed, StageAnalysisCompleted, StageAnalysisCompletedWarnings, StageBudgetExhausted,
		StageRetrying, StageCompleted, StageCompletedReused, StageFailed, StageDeadLettered, StageManualReplay,
		StageRevisionPending, StageRevisionApplied, StageTranscription, StageASR, StageDispatchFailed} {
		allStages[s] = true
	}
}

// TaskStageFrom parses a stage name; unknown or blank values yield "" (null).
func TaskStageFrom(value string) TaskStage {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if s := TaskStage(value); allStages[s] {
		return s
	}
	return ""
}

// State is the coarse task state shown to users.
type State string

const (
	StateNotStarted State = "NOT_STARTED"
	StateQueued     State = "QUEUED"
	StateProcessing State = "PROCESSING"
	StateCompleted  State = "COMPLETED"
	StateFailed     State = "FAILED"
)

// ValidState reports whether s is a known TaskStatus.State value.
func ValidState(s string) bool {
	switch State(s) {
	case StateNotStarted, StateQueued, StateProcessing, StateCompleted, StateFailed:
		return true
	}
	return false
}
