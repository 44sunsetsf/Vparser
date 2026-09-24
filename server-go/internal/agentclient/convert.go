package agentclient

import (
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	agentv1 "dovideo/server/gen/agent/v1"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

// MapError translates a gRPC status into the error taxonomy (contract §2), so callers branch on
// meaning (permanent / budget / not ready / transient) and never on transport codes.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return common.Transient("agent call failed", err)
	}
	msg := st.Message()
	switch st.Code() {
	case codes.InvalidArgument:
		return &common.Error{Kind: common.KindInvalidArgument, Msg: msg, Cause: err}
	case codes.FailedPrecondition:
		return &common.Error{Kind: common.KindNotReady, Msg: common.NotReadyMessage, Cause: err}
	case codes.ResourceExhausted:
		var ae *agentError
		if errors.As(err, &ae) && ae.reason == "BUDGET_EXHAUSTED" {
			return common.BudgetExhausted(msg, err)
		}
		return common.Transient("agent overloaded: "+msg, err)
	case codes.Unauthenticated:
		// A token mismatch is a deployment error, not a request error: keep the task retryable so it
		// succeeds once the configuration is fixed, but make it loud.
		slog.Error("agent_rejected_internal_token", "err", msg)
		return common.Transient("agent rejected the internal token", err)
	}
	if strings.TrimSpace(msg) == "" {
		msg = "agent service " + st.Code().String()
	}
	return common.Transient(msg, err)
}

// agentError carries the x-agent-error trailer alongside the status error.
type agentError struct {
	error
	reason string
}

func (e *agentError) Unwrap() error { return e.error }

// GRPCStatus keeps status.FromError / status.Code working on the wrapped error.
func (e *agentError) GRPCStatus() *status.Status {
	st, _ := status.FromError(e.error)
	return st
}

// ---- enums ----

// ModeToProto maps a mode; unknown or empty modes become GENERAL.
func ModeToProto(m model.AnalysisMode) agentv1.AnalysisMode {
	switch m.Resolve() {
	case model.ModeLearning:
		return agentv1.AnalysisMode_LEARNING
	case model.ModeReview:
		return agentv1.AnalysisMode_REVIEW
	case model.ModeCreation:
		return agentv1.AnalysisMode_CREATION
	}
	return agentv1.AnalysisMode_GENERAL
}

// ModeFromProto maps a proto enum to its name; UNSPECIFIED is treated as GENERAL.
func ModeFromProto(m agentv1.AnalysisMode) model.AnalysisMode {
	switch m {
	case agentv1.AnalysisMode_LEARNING:
		return model.ModeLearning
	case agentv1.AnalysisMode_REVIEW:
		return model.ModeReview
	case agentv1.AnalysisMode_CREATION:
		return model.ModeCreation
	}
	return model.ModeGeneral
}

// ---- messages -> client JSON ----

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// TaskStatusFromProto maps {state,result,message}; empty strings become null.
func TaskStatusFromProto(s *agentv1.TaskStatus) model.TaskStatus {
	state := model.State(s.GetState())
	if state == "" {
		state = model.StateCompleted
	}
	return model.TaskStatus{State: state, Result: nonEmpty(s.GetResult()), Message: nonEmpty(s.GetMessage())}
}

// EvidenceHit is the client JSON of one evidence search hit.
type EvidenceHit struct {
	StartMs    int64    `json:"startMs"`
	EndMs      int64    `json:"endMs"`
	Source     string   `json:"source"`
	Snippet    string   `json:"snippet"`
	Transcript string   `json:"transcript"`
	OcrTexts   []string `json:"ocrTexts"`
}

// HitsFromProto always returns a non-nil slice so the client receives [] rather than null.
func HitsFromProto(hits []*agentv1.VideoEvidenceHit) []EvidenceHit {
	out := make([]EvidenceHit, 0, len(hits))
	for _, h := range hits {
		ocr := h.GetOcrTexts()
		if ocr == nil {
			ocr = []string{}
		}
		out = append(out, EvidenceHit{StartMs: h.GetStartMs(), EndMs: h.GetEndMs(), Source: h.GetSource(),
			Snippet: h.GetSnippet(), Transcript: h.GetTranscript(), OcrTexts: ocr})
	}
	return out
}

// Plan is the client JSON of an agent plan.
type Plan struct {
	UnderstoodGoal string   `json:"understoodGoal"`
	Tasks          []string `json:"tasks"`
}

func PlanFromProto(p *agentv1.AgentPlan) *Plan {
	tasks := p.GetTasks()
	if tasks == nil {
		tasks = []string{}
	}
	return &Plan{UnderstoodGoal: p.GetUnderstoodGoal(), Tasks: tasks}
}

// StructToJSON passes a free-form Struct through as a JSON object; an empty struct means "none".
func StructToJSON(s *structpb.Struct) map[string]any {
	if s == nil || len(s.GetFields()) == 0 {
		return nil
	}
	return s.AsMap()
}

// FeedbackFromProto maps stored feedback to client JSON: unset optional fields become null.
func FeedbackFromProto(f *agentv1.AgentFeedback) model.AgentFeedback {
	id := f.GetMediaId()
	goal := f.GetGoal()
	mode := string(ModeFromProto(f.GetMode()))
	tasks := f.GetCorrectedTasks()
	if tasks == nil {
		tasks = []string{}
	}
	out := model.AgentFeedback{
		MediaID: &id, Goal: &goal, Mode: &mode,
		ErrorType: f.ErrorType, Comment: f.Comment, CorrectedGoal: f.CorrectedGoal, CorrectedTasks: tasks,
		EvidenceTimestamp: f.EvidenceTimestamp, EvidenceAccepted: f.EvidenceAccepted, CreatedAt: nonEmpty(f.GetCreatedAt()),
	}
	if f.Rating != nil {
		r := int(*f.Rating)
		out.Rating = &r
	}
	return out
}

// ---- requests ----

// FeedbackToProto maps validated user feedback; nil pointers stay unset.
func FeedbackToProto(f model.AgentFeedback) *agentv1.AgentFeedback {
	out := &agentv1.AgentFeedback{
		ErrorType: f.ErrorType, Comment: f.Comment, CorrectedGoal: f.CorrectedGoal, CorrectedTasks: f.CorrectedTasks,
		EvidenceTimestamp: f.EvidenceTimestamp, EvidenceAccepted: f.EvidenceAccepted,
	}
	if f.MediaID != nil {
		out.MediaId = *f.MediaID
	}
	if f.Goal != nil {
		out.Goal = *f.Goal
	}
	if f.Mode != nil {
		out.Mode = ModeToProto(model.ModeFromNullable(*f.Mode))
	}
	if f.Rating != nil {
		r := int32(*f.Rating)
		out.Rating = &r
	}
	if f.CreatedAt != nil {
		out.CreatedAt = *f.CreatedAt
	}
	return out
}

// SeedToProto converts the context-build telemetry.
func SeedToProto(s *TelemetrySeed) *agentv1.TelemetrySeed {
	if s == nil {
		return nil
	}
	out := &agentv1.TelemetrySeed{TraceId: s.TraceID, Stages: map[string]*agentv1.StageTiming{}, Counters: map[string]int64{}}
	for k, v := range s.Stages {
		out.Stages[k] = &agentv1.StageTiming{DurationMs: v.DurationMs, Success: v.Success}
	}
	for k, v := range s.Counters {
		out.Counters[k] = v
	}
	return out
}
