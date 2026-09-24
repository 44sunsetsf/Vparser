package taskkeys

import (
	"testing"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

// Shared test vectors (contract §4): agent-py asserts the same digests, so both services derive
// identical Redis keys for the same goal.
func TestGoalDigest(t *testing.T) {
	tests := []struct {
		name string
		goal string
		mode model.AnalysisMode
		want string
	}{
		{"general", "总结视频", model.ModeGeneral, "d046053a28d99b133aec25c209828bfa3d61c69002b68a60da98d8af0a866384"},
		{"empty mode is general", "总结视频", "", "d046053a28d99b133aec25c209828bfa3d61c69002b68a60da98d8af0a866384"},
		{"trimmed", "  summarize\n", model.ModeGeneral, "bae9264d6d972b80f4fe23b4a22b599a1585c7faa7473232694978240159f3fe"},
		{"learning uses U+241F", "总结视频", model.ModeLearning, "6eba2a25bf839c3c0340afd7a1b4e990994257eec8098994f054d79f3ea8ba50"},
		{"creation trimmed", "  extract  ", model.ModeCreation, "6e41ca467f4b3355686c7eaee553fb727bf6627d0aad33d222a654dc0fc16db8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GoalDigest(tt.goal, tt.mode)
			if err != nil || got != tt.want {
				t.Fatalf("GoalDigest(%q,%q) = %q, %v; want %q", tt.goal, tt.mode, got, err, tt.want)
			}
		})
	}
}

func TestGoalDigestBlank(t *testing.T) {
	for _, mode := range []model.AnalysisMode{model.ModeGeneral, model.ModeReview, ""} {
		for _, goal := range []string{"", "   ", "\n\t"} {
			_, err := GoalDigest(goal, mode)
			if err == nil || !common.IsPermanentFailure(err) {
				t.Fatalf("blank goal %q mode %q must be a permanent invalid-argument error, got %v", goal, mode, err)
			}
		}
	}
}

func TestNormalizeContentHash(t *testing.T) {
	tests := []struct {
		id   int64
		hash string
		want string
	}{
		{7, "D41D8CD98F00B204E9800998ECF8427E", "d41d8cd98f00b204e9800998ecf8427e"},
		{7, "d41d8cd98f00b204e9800998ecf8427e", "d41d8cd98f00b204e9800998ecf8427e"},
		{7, "", "media-7"},
		{7, "short", "media-7"},
		{9, "d41d8cd98f00b204e9800998ecf8427e0", "media-9"},
		{9, "g41d8cd98f00b204e9800998ecf8427e", "media-9"},
		{9, "d41d8cd98f00b204e9800998ecf8427e\n", "media-9"},
	}
	for _, tt := range tests {
		if got := NormalizeContentHash(tt.id, tt.hash); got != tt.want {
			t.Errorf("NormalizeContentHash(%d,%q)=%q want %q", tt.id, tt.hash, got, tt.want)
		}
	}
}

func TestKeys(t *testing.T) {
	if Active("h", "d") != "analysis:active:h:d" || Lock("h", "d") != "lock:analysis:h:d" ||
		Completed("h", "d") != "analysis:completed:h:d" ||
		ContextOwner("h") != "analysis:context-owner:h" || ContextLock("h") != "lock:analysis-context:h" {
		t.Fatal("key format drifted from contract §4")
	}
}
