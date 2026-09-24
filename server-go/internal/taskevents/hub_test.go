package taskevents

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

func TestTaskEventJSON(t *testing.T) {
	msg := "hi"
	ev := model.EventOf(model.StatusOf(model.StateProcessing, msg), model.StageConsuming)
	b, _ := common.MarshalNoEscape(Envelope{Key: "analysis:1:abc", Event: ev})
	want := `{"key":"analysis:1:abc","event":{"state":"PROCESSING","result":null,"message":"hi","stage":"CONSUMING"}}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
	// null stage / html chars are not escaped
	res := "<b>x</b> & y"
	ev = model.EventOf(model.TaskStatus{State: model.StateCompleted, Result: &res}, "")
	b, _ = common.MarshalNoEscape(ev)
	if string(b) != `{"state":"COMPLETED","result":"<b>x</b> & y","message":null,"stage":null}` {
		t.Fatalf("got %s", b)
	}
	if !ev.Terminal() || model.EventOf(model.StatusOf(model.StateQueued, "q"), model.StageQueued).Terminal() {
		t.Fatal("terminal semantics")
	}
}

func TestKey(t *testing.T) {
	k, err := Key(5, TypeTranscription, "", model.ModeGeneral)
	if err != nil || k != "transcription:5:default" {
		t.Fatalf("%s %v", k, err)
	}
	k, _ = Key(5, TypeAnalysis, "总结视频", model.ModeGeneral)
	if k != "analysis:5:d046053a28d99b133aec25c209828bfa3d61c69002b68a60da98d8af0a866384" {
		t.Fatal(k)
	}
	if _, err := Key(5, TypeAnalysis, "  ", model.ModeGeneral); err == nil {
		t.Fatal("blank goal")
	}
}

func TestHubViaRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	h := New(rdb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx, rdb)
	time.Sleep(100 * time.Millisecond)

	sub, err := h.Subscribe(1, TypeAnalysis, "g", model.ModeGeneral, model.StatusOf(model.StateQueued, "q"), model.StageQueued)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	first := <-sub.Events
	if first.State != model.StateQueued {
		t.Fatalf("initial event: %+v", first)
	}
	if err := h.PublishAnalysis(ctx, 1, "g", model.ModeGeneral, model.StatusOf(model.StateProcessing, "p"), model.StageAgentLoop); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.Events:
		if ev.State != model.StateProcessing || *ev.Stage != model.StageAgentLoop {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not delivered through redis pubsub")
	}
}

func TestPublishLocalFallbackWhenNoReceivers(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	h := New(rdb) // no Run(): zero Redis receivers -> local delivery
	sub, _ := h.Subscribe(2, TypeTranscription, "", model.ModeGeneral, model.StatusOf(model.StateQueued, "q"), model.StageQueued)
	<-sub.Events
	h.PublishTranscription(context.Background(), 2, model.StatusCompleted("done"), model.StageCompleted)
	select {
	case ev := <-sub.Events:
		if !ev.Terminal() {
			t.Fatal("expected terminal event")
		}
	case <-time.After(time.Second):
		t.Fatal("local fallback failed")
	}
}
