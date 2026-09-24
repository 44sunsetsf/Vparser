package mq

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

// recordingHandler wraps the real consumer and records when each attempt ran.
type recordingHandler struct {
	inner *Consumer
	mu    sync.Mutex
	seen  []Delivery
	at    []time.Time
}

func (h *recordingHandler) Handle(ctx context.Context, d Delivery) (Outcome, error) {
	h.mu.Lock()
	h.seen = append(h.seen, d)
	h.at = append(h.at, time.Now())
	h.mu.Unlock()
	return h.inner.Handle(ctx, d)
}

// TestRetryChainOverKafka runs the full retry routing against an in-process Kafka: a task that
// always fails transiently must go main -> retry.10s -> retry.60s -> dlq, each retry must wait for
// its not-before time, and every offset must end up committed.
func TestRetryChainOverKafka(t *testing.T) {
	saved := map[string]time.Duration{}
	for k, v := range RetryDelays {
		saved[k] = v
	}
	RetryDelays[TopicRetry10s], RetryDelays[TopicRetry60s] = 300*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() {
		for k, v := range saved {
			RetryDelays[k] = v
		}
	})

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	brokers := cluster.ListenAddrs()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admCl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatal(err)
	}
	defer admCl.Close()
	adm := kadm.NewClient(admCl)
	for i := 0; i < 2; i++ { // idempotent: the second run finds every topic already there
		if err := EnsureTopics(ctx, adm, Topics, 10*time.Second); err != nil {
			t.Fatalf("ensure topics (run %d): %v", i+1, err)
		}
	}
	details, err := adm.ListTopics(ctx, TopicAnalysis, TopicDLQ)
	if err != nil || len(details[TopicAnalysis].Partitions) != 6 || len(details[TopicDLQ].Partitions) != 1 {
		t.Fatalf("topic layout: %v %v", details, err)
	}

	producer, err := NewProducer(brokers)
	if err != nil {
		t.Fatal(err)
	}
	defer producer.Close()

	e := newEnv(t, model.ActionStart)
	e.f.analyzeErr = common.Transient("agent unavailable", nil)
	e.c.Publisher, e.c.Now = producer, time.Now
	h := &recordingHandler{inner: e.c}

	runner, err := NewRunner(brokers, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.RedeliverBackoff = 50 * time.Millisecond
	pollCtx, stopPolling := context.WithCancel(ctx)
	pollDone := make(chan struct{})
	go func() { runner.Run(pollCtx); close(pollDone) }()

	if err := producer.PublishTask(ctx, model.NewTaskMsg(5, model.ActionStart, "media-5", "goal", model.ModeGeneral)); err != nil {
		t.Fatal(err)
	}

	dlq := readOne(ctx, t, brokers, TopicDLQ)
	if v, _ := header(dlq, HeaderAttempt); v != "3" {
		t.Fatalf("dlq attempt %q", v)
	}
	if v, _ := header(dlq, HeaderDLQReason); v != ReasonExhausted {
		t.Fatalf("dlq reason %q", v)
	}
	if string(dlq.Key) != "media-5" {
		t.Fatalf("key must be preserved: %q", dlq.Key)
	}

	h.mu.Lock()
	seen, at := append([]Delivery(nil), h.seen...), append([]time.Time(nil), h.at...)
	h.mu.Unlock()
	wantTopics := []string{TopicAnalysis, TopicRetry10s, TopicRetry60s}
	if len(seen) != len(wantTopics) {
		t.Fatalf("attempts %d: %+v", len(seen), seen)
	}
	for i, want := range wantTopics {
		if seen[i].Topic != want || seen[i].Attempt != i+1 {
			t.Fatalf("attempt %d: topic %s attempt %d", i+1, seen[i].Topic, seen[i].Attempt)
		}
		if i > 0 && at[i].Before(seen[i].NotBefore) {
			t.Fatalf("attempt %d ran %v before its not-before time", i+1, seen[i].NotBefore.Sub(at[i]))
		}
	}
	if len(e.f.recorded) != 1 || e.f.recorded[0] != 3 {
		t.Fatalf("ledger %v", e.f.recorded)
	}

	stopPolling()
	<-pollDone
	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, 5*time.Second)
	defer cancelShutdown()
	runner.Shutdown(shutdownCtx)

	// every consumed record was committed after it was handled
	offsets, err := adm.FetchOffsets(ctx, ConsumerGroup)
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range ConsumedTopics {
		committed := int64(0)
		offsets.Each(func(o kadm.OffsetResponse) {
			if o.Topic == topic {
				committed += o.At
			}
		})
		if committed != 1 {
			t.Fatalf("%s: committed offsets sum to %d, want 1", topic, committed)
		}
	}
}

func readOne(ctx context.Context, t *testing.T, brokers []string, topic string) *kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("no record on %s: %v", topic, ctx.Err())
		}
		if recs := fetches.Records(); len(recs) > 0 {
			return recs[0]
		}
	}
}
