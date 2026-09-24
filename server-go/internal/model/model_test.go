package model

import (
	"encoding/json"
	"testing"
	"time"

	"dovideo/server/internal/common"
)

func TestModeParsing(t *testing.T) {
	tests := []struct {
		in       string
		nullable AnalysisMode
		request  AnalysisMode
		reqErr   bool
	}{
		{"", ModeGeneral, ModeGeneral, false},
		{"  ", ModeGeneral, ModeGeneral, false},
		{"learning", ModeLearning, ModeLearning, false},
		{" Review ", ModeReview, ModeReview, false},
		{"CREATION", ModeCreation, ModeCreation, false},
		{"bogus", ModeGeneral, "", true},
	}
	for _, tt := range tests {
		if got := ModeFromNullable(tt.in); got != tt.nullable {
			t.Errorf("nullable %q: %q", tt.in, got)
		}
		got, err := ModeFromRequest(tt.in)
		if (err != nil) != tt.reqErr || got != tt.request {
			t.Errorf("request %q: %q %v", tt.in, got, err)
		}
	}
	_, err := ModeFromRequest("x")
	if err == nil || err.Error() != "不支持的分析模式: x" || !common.IsPermanentFailure(err) {
		t.Fatalf("%v", err)
	}
}

func TestTaskStageFrom(t *testing.T) {
	if TaskStageFrom("CONTEXT_COMPLETED") != StageContextCompleted || TaskStageFrom("nope") != "" || TaskStageFrom(" ") != "" {
		t.Fatal("stage parsing")
	}
}

func TestVideoContextJSONShape(t *testing.T) {
	seg, err := NewVideoSegment(0, 60000, "  hi ", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	vc, _ := NewVideoContext("http://x/media/a.mp4", "  goal ", []VideoSegment{seg})
	b, _ := common.MarshalNoEscape(vc)
	want := `{"source":"http://x/media/a.mp4","userGoal":"goal","segments":[{"startMs":0,"endMs":60000,"transcript":"hi","ocrTexts":[],"evidenceFrames":[]}]}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	if _, err := NewVideoSegment(5, 5, "", nil, nil); err == nil {
		t.Fatal("empty range must fail")
	}
	if _, err := NewVideoContext(" ", "", nil); err == nil {
		t.Fatal("blank source must fail")
	}
	var back VideoContext
	_ = json.Unmarshal([]byte(`{"source":"s","userGoal":"","segments":[{"startMs":0,"endMs":1,"transcript":"a"}]}`), &back)
	n, err := back.Normalize()
	if err != nil || n.Segments[0].OcrTexts == nil || len(n.Segments[0].EvidenceFrames) != 0 {
		t.Fatalf("normalize: %+v %v", n, err)
	}
	if got := (VideoContext{Segments: []VideoSegment{{Transcript: "a"}, {Transcript: " "}, {Transcript: "b"}}}).TranscriptText(); got != "a\nb" {
		t.Fatalf("%q", got)
	}
}

func TestTaskMsgJSON(t *testing.T) {
	b, _ := json.Marshal(NewTaskMsg(7, ActionRevise, "media-7", "goal", ModeLearning))
	if string(b) != `{"mediaId":7,"action":"REVISE_ANALYSIS","contentHash":"media-7","userGoal":"goal","mode":"LEARNING"}` {
		t.Fatalf("%s", b)
	}
	var m AnalysisTaskMsg
	_ = json.Unmarshal([]byte(`{"mediaId":1,"action":"START_ANALYSIS","userGoal":"g"}`), &m)
	if !m.HasSupportedAction() || m.IsRevision() || m.ContentHash != nil {
		t.Fatal("decode")
	}
}

func TestTaskStatus(t *testing.T) {
	b, _ := common.MarshalNoEscape(StatusOf(StateQueued, "任务已排队"))
	if string(b) != `{"state":"QUEUED","result":null,"message":"任务已排队"}` {
		t.Fatalf("%s", b)
	}
	b, _ = common.MarshalNoEscape(StatusCompleted("md"))
	if string(b) != `{"state":"COMPLETED","result":"md","message":"任务完成"}` {
		t.Fatalf("%s", b)
	}
}

func TestAgentFeedbackNormalized(t *testing.T) {
	s := func(v string) *string { return &v }
	id := int64(3)
	fb := AgentFeedback{MediaID: &id, Goal: s("  g "), CorrectedGoal: s(" cg "), CorrectedTasks: []string{" a ", " ", "b"}}
	n := fb.Normalized(ModeReview)
	if *n.Goal != "g" || *n.Mode != "REVIEW" || *n.CorrectedGoal != "cg" || len(n.CorrectedTasks) != 2 ||
		n.CorrectedTasks[0] != "a" || n.CreatedAt == nil {
		t.Fatalf("%+v", n)
	}
	if _, err := time.Parse(time.RFC3339Nano, *n.CreatedAt); err != nil {
		t.Fatalf("createdAt %q: %v", *n.CreatedAt, err)
	}
	if fb.RevisionGoal() != "cg" {
		t.Fatal("corrected goal wins")
	}
	fb.CorrectedGoal = s("  ")
	if fb.RevisionGoal() != "g" {
		t.Fatal("blank corrected goal falls back to goal")
	}
	if (AgentFeedback{}).Normalized("").CorrectedTasks == nil {
		t.Fatal("tasks must be a non-nil empty list")
	}
}

func TestLocalDateTimeJSON(t *testing.T) {
	ts := DateTime{time.Date(2024, 5, 1, 10, 0, 0, 123_000_000, time.UTC)}
	b, _ := json.Marshal(ts)
	if string(b) != `"2024-05-01T10:00:00.123"` {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(DateTime{time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC)})
	if string(b) != `"2024-05-01T10:00:00"` {
		t.Fatal(string(b))
	}
	var back DateTime
	if err := json.Unmarshal([]byte(`"2024-05-01T10:00:00.5"`), &back); err != nil || back.Nanosecond() != 500_000_000 {
		t.Fatal(err)
	}
}

func TestRouteDecisionDefaults(t *testing.T) {
	d := NewRouteDecision("", " ")
	if d.Mode != ModeGeneral || d.Reason != "已按通用模式分析" {
		t.Fatalf("%+v", d)
	}
}
