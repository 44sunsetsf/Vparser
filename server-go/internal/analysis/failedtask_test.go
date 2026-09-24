package analysis

import (
	"strings"
	"testing"

	"dovideo/server/internal/model"
)

func TestSanitizeError(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Authorization: Bearer abcdefghijklmnop failed", "Authorization: Bearer **** failed"},
		{"api_key=sk-abcdefghijklmnopqrstuvwxyz1234 boom", "api_key=**** boom"},
		{"token: 12345678 end", "token: **** end"},
		{"secret=short", "secret=short"},
		{"key sk-ABCDEFGHIJKLMNOPQRSTUV here", "key **** here"},
		{"plain message", "plain message"},
	}
	for _, tt := range tests {
		if got := *SanitizeError(tt.in); got != tt.want {
			t.Errorf("%q => %q want %q", tt.in, got, tt.want)
		}
	}
	long := strings.Repeat("x", 1500)
	if got := *SanitizeError(long); len(got) != 1000 {
		t.Fatalf("len %d", len(got))
	}
}

func TestColumnAndPlaceholder(t *testing.T) {
	if column("", "UNKNOWN", 32) != "UNKNOWN" || column("   ", "d", 5) != "d" {
		t.Fatal("fallback")
	}
	if got := column(strings.Repeat("目", 600), "x", 500); len([]rune(got)) != 500 {
		t.Fatalf("truncate: %d", len([]rune(got)))
	}
	if !isPlaceholderRecord(&model.FailedAnalysisTask{MediaID: -1, Action: "START_ANALYSIS"}) ||
		!isPlaceholderRecord(&model.FailedAnalysisTask{MediaID: 1, Action: "UNKNOWN"}) ||
		isPlaceholderRecord(&model.FailedAnalysisTask{MediaID: 1, Action: "REVISE_ANALYSIS"}) {
		t.Fatal("placeholder detection")
	}
}

func TestStatusMessage(t *testing.T) {
	tests := map[model.TaskStage]string{
		"": "任务已排队", model.StageQueued: "任务已排队", model.StageVideoContext: "正在解析视频语音和关键画面",
		model.StagePlanCompleted: "Planner 已完成任务拆解", model.StageCriticRetryRequired: "正在根据 Critic 反馈补充证据",
		model.StageRetrying: "任务执行异常，正在自动重试", model.StageConsuming: "正在分析视频", model.StageAgentLoop: "正在分析视频",
	}
	for stage, want := range tests {
		if got := StatusMessage(stage); got != want {
			t.Errorf("%q => %q", stage, got)
		}
	}
}
