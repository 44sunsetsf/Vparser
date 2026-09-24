package checkpoint

import (
	"testing"

	"dovideo/server/internal/model"
)

func TestKeyLayoutMatchesContract(t *testing.T) {
	const digest = "d046053a28d99b133aec25c209828bfa3d61c69002b68a60da98d8af0a866384" // sha256("总结视频")
	key, err := GoalKey(12, "  总结视频 ", model.ModeGeneral)
	if err != nil || key != "agent:checkpoint:12:goal:"+digest {
		t.Fatalf("%q %v", key, err)
	}
	if mediaKey(12) != "agent:checkpoint:12" || goalIndexKey(12) != "agent:checkpoint:12:goals" {
		t.Fatal("media-level keys")
	}
	name, _ := goalCheckpoint("总结视频", model.ModeGeneral, "stage")
	if name != "goal:"+digest+":stage" {
		t.Fatal(name)
	}
	learning, _ := goalCheckpoint("总结视频", model.ModeLearning, "result")
	if learning != "goal:6eba2a25bf839c3c0340afd7a1b4e990994257eec8098994f054d79f3ea8ba50:result" {
		t.Fatal(learning)
	}
	if _, err := GoalKey(1, "  ", model.ModeGeneral); err == nil {
		t.Fatal("blank goal must fail")
	}
}

func TestDecodeContextValidates(t *testing.T) {
	vc, err := decodeContext(`{"source":"s","userGoal":" x ","segments":[{"startMs":0,"endMs":60000,"transcript":" t ","ocrTexts":["a"],"evidenceFrames":[]}]}`)
	if err != nil || vc.UserGoal != "x" || vc.Segments[0].Transcript != "t" {
		t.Fatalf("%+v %v", vc, err)
	}
	if _, err := decodeContext(`{"source":"","segments":[]}`); err == nil {
		t.Fatal("blank source must be rejected like the record constructor")
	}
	if _, err := decodeContext(`{"source":"s","segments":[{"startMs":5,"endMs":5}]}`); err == nil {
		t.Fatal("invalid range must be rejected")
	}
}
