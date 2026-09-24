package mq

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
	"go.opentelemetry.io/otel/trace"

	"dovideo/server/internal/obs"
)

// Handler processes one delivery; a non-nil error means "not handled, try the same record again".
type Handler interface {
	Handle(ctx context.Context, d Delivery) (Outcome, error)
}

// Runner is the consumer-group poll loop.
//
// Each assigned partition gets its own worker goroutine, so a multi-minute analysis on one
// partition never stalls the others. Flow control is done with fetch pausing: after a batch is
// handed to a worker its partition is paused, and the worker resumes it once the batch is done.
// The same mechanism implements delayed retries: a worker whose head record carries a future
// x-not-before simply waits (partition still paused, nothing committed) instead of spinning or
// blocking the poll loop.
type Runner struct {
	cl     *kgo.Client
	h      Handler
	tracer *kotel.Tracer
	now    func() time.Time

	// RedeliverBackoff is the first wait before re-handling a record whose handling failed.
	RedeliverBackoff time.Duration

	procCtx    context.Context // parent of every Handle call; cancelled only on hard shutdown
	cancelProc context.CancelFunc

	mu      sync.Mutex
	workers map[topicPartition]*partitionWorker
	wg      sync.WaitGroup
}

type topicPartition struct {
	topic     string
	partition int32
}

// NewRunner builds the group consumer. tracer may be nil.
func NewRunner(brokers []string, h Handler, tracer *kotel.Tracer, hooks ...kgo.Hook) (*Runner, error) {
	r := &Runner{h: h, tracer: tracer, now: time.Now, RedeliverBackoff: time.Second,
		workers: map[topicPartition]*partitionWorker{}}
	r.procCtx, r.cancelProc = context.WithCancel(context.Background())
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID("server-go"),
		kgo.ConsumerGroup(ConsumerGroup),
		kgo.ConsumeTopics(ConsumedTopics...),
		// Offsets are committed per record by the worker after the record is fully handled.
		kgo.DisableAutoCommit(),
		// A brand-new group starts from the earliest offset so tasks accepted before the first
		// worker ever joined are not skipped.
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		// Rebalance callbacks run only between polls, never while a batch is being dispatched, so
		// pausing a partition can never race with its revocation.
		kgo.BlockRebalanceOnPoll(),
		kgo.OnPartitionsRevoked(r.onRevoked),
		kgo.OnPartitionsLost(r.onRevoked),
		kgo.WithHooks(hooks...),
	)
	if err != nil {
		return nil, err
	}
	r.cl = cl
	return r, nil
}

// Run polls until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	for {
		fetches := r.cl.PollRecords(ctx, 256)
		if fetches.IsClientClosed() || ctx.Err() != nil {
			return
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			if !errors.Is(err, context.Canceled) {
				slog.Warn("kafka_fetch_error", "topic", topic, "partition", partition, "err", err)
			}
		})
		fetches.EachPartition(func(p kgo.FetchTopicPartition) {
			if len(p.Records) > 0 {
				r.dispatch(p.Topic, p.Partition, p.Records)
			}
		})
		r.cl.AllowRebalance()
	}
}

// Shutdown stops fetching, lets in-flight records finish until ctx expires (their offsets are
// committed by the workers), then leaves the group. Records that were queued but not started stay
// uncommitted and are redelivered to whichever instance takes the partition over.
func (r *Runner) Shutdown(ctx context.Context) {
	r.mu.Lock()
	for tp, w := range r.workers {
		w.stop()
		delete(r.workers, tp)
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("kafka_consumer_shutdown_timeout", "action", "cancel_in_flight")
		r.cancelProc()
		<-done
	}
	r.cancelProc()
	r.cl.CloseAllowingRebalance()
}

func (r *Runner) dispatch(topic string, partition int32, recs []*kgo.Record) {
	tp := topicPartition{topic, partition}
	r.mu.Lock()
	w := r.workers[tp]
	if w == nil {
		w = &partitionWorker{r: r, tp: tp, wake: make(chan struct{}, 1), done: make(chan struct{})}
		r.workers[tp] = w
		r.wg.Add(1)
		go w.run()
	}
	r.mu.Unlock()
	// pause before the next poll so at most one batch per partition is outstanding
	r.cl.PauseFetchPartitions(map[string][]int32{topic: {partition}})
	w.enqueue(recs)
}

// onRevoked detaches workers of partitions this member no longer owns. In-flight records finish
// (their commits may fail, which at-least-once tolerates); queued ones are dropped for the new owner.
// The partitions are resumed so a later re-assignment to this member fetches them again.
func (r *Runner) onRevoked(_ context.Context, cl *kgo.Client, revoked map[string][]int32) {
	r.mu.Lock()
	for topic, parts := range revoked {
		for _, p := range parts {
			tp := topicPartition{topic, p}
			if w := r.workers[tp]; w != nil {
				w.stop()
				delete(r.workers, tp)
			}
		}
	}
	r.mu.Unlock()
	cl.ResumeFetchPartitions(revoked)
}

type partitionWorker struct {
	r    *Runner
	tp   topicPartition
	mu   sync.Mutex
	q    []*kgo.Record
	wake chan struct{}
	done chan struct{}
	once sync.Once
}

func (w *partitionWorker) enqueue(recs []*kgo.Record) {
	w.mu.Lock()
	w.q = append(w.q, recs...)
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *partitionWorker) stop() { w.once.Do(func() { close(w.done) }) }

func (w *partitionWorker) stopped() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

func (w *partitionWorker) next() (*kgo.Record, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.q) == 0 {
		if !w.stopped() {
			w.r.cl.ResumeFetchPartitions(map[string][]int32{w.tp.topic: {w.tp.partition}})
		}
		return nil, false
	}
	rec := w.q[0]
	w.q = w.q[1:]
	return rec, true
}

func (w *partitionWorker) run() {
	defer w.r.wg.Done()
	for {
		for {
			if w.stopped() {
				return
			}
			rec, ok := w.next()
			if !ok {
				break
			}
			if !w.handle(rec) {
				return
			}
		}
		select {
		case <-w.wake:
		case <-w.done:
			return
		}
	}
}

// sleep waits for d unless the worker is stopped; false means stopped.
func (w *partitionWorker) sleep(d time.Duration) bool {
	if d <= 0 {
		return !w.stopped()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-w.done:
		return false
	}
}

// handle processes one record to completion and commits it; false means the worker was stopped
// before the record was handled (it stays uncommitted).
func (w *partitionWorker) handle(rec *kgo.Record) bool {
	d := DecodeRecord(rec)
	if !d.NotBefore.IsZero() && !w.sleep(d.NotBefore.Sub(w.r.now())) {
		return false
	}
	backoff := w.r.RedeliverBackoff
	for {
		ctx, span := w.span(rec)
		started := time.Now()
		outcome, err := w.r.h.Handle(ctx, d)
		span.End()
		if err == nil {
			obs.KafkaConsumeDuration.WithLabelValues(rec.Topic, string(outcome)).Observe(time.Since(started).Seconds())
			w.commit(rec)
			return true
		}
		obs.KafkaConsumeDuration.WithLabelValues(rec.Topic, string(OutcomeRedeliver)).Observe(time.Since(started).Seconds())
		slog.Error("kafka_record_not_handled", "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset,
			"retryIn", backoff, "err", err)
		if !w.sleep(backoff) {
			return false
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// span starts the process span under the producer's trace (extracted from traceparent by kotel)
// but parented on the runner's lifetime context, so a hard shutdown still cancels the work.
func (w *partitionWorker) span(rec *kgo.Record) (context.Context, trace.Span) {
	if w.r.tracer == nil {
		return w.r.procCtx, trace.SpanFromContext(w.r.procCtx)
	}
	_, span := w.r.tracer.WithProcessSpan(rec)
	return trace.ContextWithSpan(w.r.procCtx, span), span
}

func (w *partitionWorker) commit(rec *kgo.Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.r.cl.CommitRecords(ctx, rec); err != nil {
		// At-least-once: the next owner re-handles the record and the idempotency keys absorb it.
		slog.Warn("kafka_commit_failed", "topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset, "err", err)
	}
}
