// Package mq runs the analysis task pipeline on Kafka (contract §3).
//
// Delivery is at-least-once: an offset is committed only after its record has been fully handled
// (succeeded, forwarded to a retry topic, or dead-lettered), and duplicates are absorbed by the
// business-level idempotency keys and the per-task distributed lock.
//
// Retries use dedicated delay topics instead of redelivering in place. A failing record is
// re-published with an incremented attempt and a not-before timestamp, its offset is committed,
// and the partition moves on, so one slow or failing task never blocks the tasks behind it.
package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
)

// Topics and consumer group (contract §3).
const (
	TopicAnalysis = "video.analysis"
	TopicRetry10s = "video.analysis.retry.10s"
	TopicRetry60s = "video.analysis.retry.60s"
	TopicDLQ      = "video.analysis.dlq"
	ConsumerGroup = "video-analysis-worker"
)

// TopicSpec is a topic and its partition count.
type TopicSpec struct {
	Name       string
	Partitions int32
}

// Topics are created at startup. The main topic has the most partitions because it carries all
// first attempts; partitioning by contentHash keeps one video on one partition, which already
// serialises most same-content work before the distributed lock is even consulted.
var Topics = []TopicSpec{
	{TopicAnalysis, 6},
	{TopicRetry10s, 3},
	{TopicRetry60s, 3},
	{TopicDLQ, 1},
}

// ConsumedTopics are the topics the worker group subscribes to (the DLQ is for humans).
var ConsumedTopics = []string{TopicAnalysis, TopicRetry10s, TopicRetry60s}

// EnsureTopics creates any missing topic, retrying until the broker answers or maxWait elapses.
// Creating an existing topic is not an error, so every instance may run this concurrently.
// Replication factor -1 defers to the broker default, which is 1 on a dev broker and 3 in prod.
func EnsureTopics(ctx context.Context, adm *kadm.Client, specs []TopicSpec, maxWait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, maxWait)
	defer cancel()
	backoff := 500 * time.Millisecond
	for {
		err := createMissing(ctx, adm, specs)
		if err == nil {
			return nil
		}
		slog.Warn("kafka_topics_not_ready", "err", err, "retryIn", backoff)
		select {
		case <-ctx.Done():
			return fmt.Errorf("ensure kafka topics: %w", err)
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

func createMissing(ctx context.Context, adm *kadm.Client, specs []TopicSpec) error {
	for _, s := range specs {
		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		res, err := adm.CreateTopic(reqCtx, s.Partitions, -1, nil, s.Name)
		cancel()
		if err == nil {
			err = res.Err
		}
		if err != nil && !errors.Is(err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("create %s: %w", s.Name, err)
		}
	}
	return nil
}
