package model

import (
	"encoding/json"
	"strings"
	"time"

	"dovideo/server/internal/common"
)

func strp(s string) *string { return &s }

// TaskStatus is {state,result,message}.
type TaskStatus struct {
	State   State   `json:"state"`
	Result  *string `json:"result"`
	Message *string `json:"message"`
}

// StatusOf is a status with a message and no result.
func StatusOf(state State, message string) TaskStatus {
	return TaskStatus{State: state, Message: strp(message)}
}

// StatusCompleted is a completed status carrying the result.
func StatusCompleted(result string) TaskStatus {
	return TaskStatus{State: StateCompleted, Result: strp(result), Message: strp("任务完成")}
}

// StatusCompletedNullable is completed(result) where result may be null.
func StatusCompletedNullable(result *string) TaskStatus {
	return TaskStatus{State: StateCompleted, Result: result, Message: strp("任务完成")}
}

// TaskEvent is the SSE / Pub-Sub payload {state,result,message,stage}.
type TaskEvent struct {
	State   State      `json:"state"`
	Result  *string    `json:"result"`
	Message *string    `json:"message"`
	Stage   *TaskStage `json:"stage"`
}

// EventOf builds an event; an empty stage becomes null.
func EventOf(status TaskStatus, stage TaskStage) TaskEvent {
	ev := TaskEvent{State: status.State, Result: status.Result, Message: status.Message}
	if stage != "" {
		s := stage
		ev.Stage = &s
	}
	return ev
}

// Terminal reports whether the event closes the stream.
func (e TaskEvent) Terminal() bool { return e.State == StateCompleted || e.State == StateFailed }

// Analysis actions carried in task records.
const (
	ActionStart  = "START_ANALYSIS"
	ActionRevise = "REVISE_ANALYSIS"
)

// AnalysisTaskMsg is the Kafka record value (contract §3). Pointer fields let the consumer tell a
// missing field from a zero value when it validates untrusted records.
type AnalysisTaskMsg struct {
	MediaID     *int64  `json:"mediaId"`
	Action      *string `json:"action"`
	ContentHash *string `json:"contentHash"`
	UserGoal    *string `json:"userGoal"`
	Mode        *string `json:"mode"`
}

func (m *AnalysisTaskMsg) IsRevision() bool { return m.Action != nil && *m.Action == ActionRevise }
func (m *AnalysisTaskMsg) HasSupportedAction() bool {
	return m.Action != nil && IsSupportedAction(*m.Action)
}
func IsSupportedAction(a string) bool { return a == ActionStart || a == ActionRevise }

// NewTaskMsg builds a fully populated message.
func NewTaskMsg(mediaID int64, action, contentHash, goal string, mode AnalysisMode) AnalysisTaskMsg {
	m := string(mode)
	return AnalysisTaskMsg{MediaID: &mediaID, Action: &action, ContentHash: &contentHash, UserGoal: &goal, Mode: &m}
}

func (m *AnalysisTaskMsg) GoalOrEmpty() string {
	if m.UserGoal == nil {
		return ""
	}
	return *m.UserGoal
}

// VideoSegment / VideoContext are built through constructors that validate and normalise, so a
// value decoded from a checkpoint is re-normalised the same way as a freshly built one.
type VideoSegment struct {
	StartMs        int64    `json:"startMs"`
	EndMs          int64    `json:"endMs"`
	Transcript     string   `json:"transcript"`
	OcrTexts       []string `json:"ocrTexts"`
	EvidenceFrames []string `json:"evidenceFrames"`
}

type VideoContext struct {
	Source   string         `json:"source"`
	UserGoal string         `json:"userGoal"`
	Segments []VideoSegment `json:"segments"`
}

// NewVideoSegment validates the range and normalises text and nil slices.
func NewVideoSegment(startMs, endMs int64, transcript string, ocr, frames []string) (VideoSegment, error) {
	if startMs < 0 || endMs <= startMs {
		return VideoSegment{}, common.InvalidArgument("invalid segment range")
	}
	if ocr == nil {
		ocr = []string{}
	}
	if frames == nil {
		frames = []string{}
	}
	return VideoSegment{StartMs: startMs, EndMs: endMs, Transcript: strings.TrimSpace(transcript),
		OcrTexts: ocr, EvidenceFrames: frames}, nil
}

// NewVideoContext validates the source and normalises the goal and nil slices.
func NewVideoContext(source, userGoal string, segments []VideoSegment) (VideoContext, error) {
	if strings.TrimSpace(source) == "" {
		return VideoContext{}, common.InvalidArgument("video source is required")
	}
	if segments == nil {
		segments = []VideoSegment{}
	}
	return VideoContext{Source: source, UserGoal: strings.TrimSpace(userGoal), Segments: segments}, nil
}

// Normalize re-applies constructor rules to a deserialised value.
func (c VideoContext) Normalize() (VideoContext, error) {
	segs := make([]VideoSegment, 0, len(c.Segments))
	for _, s := range c.Segments {
		ns, err := NewVideoSegment(s.StartMs, s.EndMs, s.Transcript, s.OcrTexts, s.EvidenceFrames)
		if err != nil {
			return VideoContext{}, err
		}
		segs = append(segs, ns)
	}
	return NewVideoContext(c.Source, c.UserGoal, segs)
}

// TranscriptText joins non-blank segment transcripts with "\n".
func (c VideoContext) TranscriptText() string {
	parts := make([]string, 0, len(c.Segments))
	for _, s := range c.Segments {
		if strings.TrimSpace(s.Transcript) != "" {
			parts = append(parts, s.Transcript)
		}
	}
	return strings.Join(parts, "\n")
}

// TranscriptSegment is one ASR result.
type TranscriptSegment struct {
	StartMs int64
	EndMs   int64
	Text    string
}

func NewTranscriptSegment(startMs, endMs int64, text string) (TranscriptSegment, error) {
	if startMs < 0 || endMs <= startMs {
		return TranscriptSegment{}, common.InvalidArgument("invalid transcript range")
	}
	return TranscriptSegment{StartMs: startMs, EndMs: endMs, Text: strings.TrimSpace(text)}, nil
}

// AgentFeedback is user feedback on a result; nullable fields are pointers so JSON keeps null.
type AgentFeedback struct {
	MediaID           *int64   `json:"mediaId" validate:"required"`
	Goal              *string  `json:"goal" validate:"notblank,max=500"`
	Mode              *string  `json:"mode"`
	Rating            *int     `json:"rating"`
	ErrorType         *string  `json:"errorType" validate:"omitempty,max=64"`
	Comment           *string  `json:"comment" validate:"omitempty,max=2000"`
	CorrectedGoal     *string  `json:"correctedGoal" validate:"omitempty,max=500"`
	CorrectedTasks    []string `json:"correctedTasks" validate:"omitempty,max=5,dive,max=500"`
	EvidenceTimestamp *int64   `json:"evidenceTimestamp" validate:"omitempty,gte=0"`
	EvidenceAccepted  *bool    `json:"evidenceAccepted"`
	CreatedAt         *string  `json:"createdAt"`
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

// InstantNow is the current UTC time in RFC 3339 with up to nanosecond precision.
func InstantNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.999999999Z") }

// Normalized trims text fields, drops blank corrected tasks, fixes the mode and stamps createdAt.
func (f AgentFeedback) Normalized(mode AnalysisMode) AgentFeedback {
	m := string(mode.Resolve())
	tasks := []string{}
	for _, t := range f.CorrectedTasks {
		if strings.TrimSpace(t) != "" {
			tasks = append(tasks, strings.TrimSpace(t))
		}
	}
	created := f.CreatedAt
	if created == nil {
		c := InstantNow()
		created = &c
	}
	return AgentFeedback{
		MediaID: f.MediaID, Goal: trimPtr(f.Goal), Mode: &m, Rating: f.Rating,
		ErrorType: trimPtr(f.ErrorType), Comment: trimPtr(f.Comment), CorrectedGoal: trimPtr(f.CorrectedGoal),
		CorrectedTasks: tasks, EvidenceTimestamp: f.EvidenceTimestamp, EvidenceAccepted: f.EvidenceAccepted,
		CreatedAt: created,
	}
}

// RevisionGoal is the goal a revision runs under: the corrected goal if given, else the original.
func (f AgentFeedback) RevisionGoal() string {
	n := f.Normalized(ModeFromNullable(deref(f.Mode)))
	if n.CorrectedGoal == nil || strings.TrimSpace(*n.CorrectedGoal) == "" {
		return deref(n.Goal)
	}
	return *n.CorrectedGoal
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// RouteRequest is the body of POST /analysis/route.
type RouteRequest struct {
	Goal *string `json:"goal" validate:"notblank,max=500"`
}

// RouteDecision is returned by the agent (opaque pass-through in Go).
type RouteDecision struct {
	Mode   AnalysisMode `json:"mode"`
	Reason string       `json:"reason"`
}

// NewRouteDecision defaults an empty mode to GENERAL and a blank reason to a generic one.
func NewRouteDecision(mode AnalysisMode, reason string) RouteDecision {
	if mode == "" {
		mode = ModeGeneral
	}
	if strings.TrimSpace(reason) == "" {
		reason = "已按通用模式分析"
	}
	return RouteDecision{Mode: mode, Reason: reason}
}

// AuthRequest is the login/register body.
type AuthRequest struct {
	Username *string `json:"username"`
	Password *string `json:"password"`
	Nickname *string `json:"nickname"`
}

type UserInfo struct {
	ID       int64   `json:"id"`
	Username string  `json:"username"`
	Nickname string  `json:"nickname"`
	Avatar   *string `json:"avatar"`
	Role     string  `json:"role"`
}

// AuthResponse is the internal auth service result (not serialised directly).
type AuthResponse struct {
	Code     int
	Msg      string
	UserInfo *UserInfo
	Token    string
}

// AuthData is the data payload of /user/login and /user/register.
type AuthData struct {
	UserInfo *UserInfo `json:"userInfo"`
	Token    *string   `json:"token"`
}

// MediaSummary is the list/upload view of a media file.
type MediaSummary struct {
	ID         int64    `json:"id"`
	Filename   string   `json:"filename"`
	Status     string   `json:"status"`
	CoverURL   *string  `json:"coverUrl"`
	UploadTime DateTime `json:"uploadTime"`
}

func SummaryOf(m *MediaFile) MediaSummary {
	return MediaSummary{ID: m.ID, Filename: m.Filename, Status: m.Status, CoverURL: m.CoverURL, UploadTime: m.UploadTime}
}

// FailedTaskView is the admin view of a failed_analysis_tasks row.
type FailedTaskView struct {
	ID           int64    `json:"id"`
	MediaID      int64    `json:"mediaId"`
	Action       string   `json:"action"`
	Mode         string   `json:"mode"`
	UserGoal     string   `json:"userGoal"`
	AttemptCount int      `json:"attemptCount"`
	ErrorType    string   `json:"errorType"`
	ErrorMessage *string  `json:"errorMessage"`
	Status       string   `json:"status"`
	CreatedAt    DateTime `json:"createdAt"`
	UpdatedAt    DateTime `json:"updatedAt"`
}

func FailedViewOf(t *FailedAnalysisTask) FailedTaskView {
	return FailedTaskView{ID: t.ID, MediaID: t.MediaID, Action: t.Action, Mode: t.Mode, UserGoal: t.UserGoal,
		AttemptCount: t.AttemptCount, ErrorType: t.ErrorType, ErrorMessage: t.ErrorMessage, Status: t.Status,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

// RawJSON is a convenience alias for pass-through payloads.
type RawJSON = json.RawMessage
