package mq

import (
	"context"
	"encoding/json"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"dovideo/server/internal/model"
)

// Producer publishes analysis tasks.
type Producer struct{ cl *kgo.Client }

// ProducerOptions configure a durable producer: acks=all plus the idempotent producer (on by
// default in franz-go, so it is deliberately not disabled) means a broker-side retry can neither
// lose an acknowledged record nor write it twice.
func ProducerOptions(brokers []string, hooks ...kgo.Hook) []kgo.Opt {
	return []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.ClientID("server-go"),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordDeliveryTimeout(15 * time.Second), // bounds how long a 202 can be delayed by a sick broker
		kgo.ProducerLinger(0),                       // submissions are latency-sensitive and low-volume
		kgo.WithHooks(hooks...),
	}
}

// NewProducer creates the producer client.
func NewProducer(brokers []string, hooks ...kgo.Hook) (*Producer, error) {
	cl, err := kgo.NewClient(ProducerOptions(brokers, hooks...)...)
	if err != nil {
		return nil, err
	}
	return &Producer{cl: cl}, nil
}

// NewProducerFromClient wraps an existing client (tests).
func NewProducerFromClient(cl *kgo.Client) *Producer { return &Producer{cl: cl} }

// Produce writes one record and waits for the broker acknowledgement. The record carries ctx so
// the tracing hook injects the caller's span as traceparent.
func (p *Producer) Produce(ctx context.Context, rec *kgo.Record) error {
	rec.Context = ctx
	return p.cl.ProduceSync(ctx, rec).FirstErr()
}

// PublishTask publishes a first attempt to the main topic, keyed by contentHash.
func (p *Producer) PublishTask(ctx context.Context, msg model.AnalysisTaskMsg) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	key := ""
	if msg.ContentHash != nil {
		key = *msg.ContentHash
	}
	return p.Produce(ctx, &kgo.Record{Topic: TopicAnalysis, Key: []byte(key), Value: body,
		Headers: taskHeaders(1, time.Time{}, "")})
}

// Close flushes and closes the client.
func (p *Producer) Close() { p.cl.Close() }
