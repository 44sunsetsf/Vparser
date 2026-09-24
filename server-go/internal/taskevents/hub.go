// Package taskevents is the SSE hub: task events are published on a Redis channel so that every
// gateway instance can push them to its own connections, whichever instance produced the event.
// The channel is dovideo:task-events; both the gateway and the agent publish to it.
package taskevents

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/taskkeys"
)

const (
	TypeAnalysis      = "analysis"
	TypeTranscription = "transcription"
	RedisChannel      = "dovideo:task-events"
	subscriberBuffer  = 64
)

// Envelope is the Pub/Sub payload {"key":..., "event":{...}}.
type Envelope struct {
	Key   string          `json:"key"`
	Event model.TaskEvent `json:"event"`
}

// Subscription delivers events for one SSE stream.
type Subscription struct {
	Events <-chan model.TaskEvent
	ch     chan model.TaskEvent
	hub    *Hub
	key    string
	once   sync.Once
}

// Close unregisters the subscriber (idempotent).
func (s *Subscription) Close() {
	s.once.Do(func() { s.hub.remove(s.key, s) })
}

// Hub fans events out to SSE subscribers.
type Hub struct {
	rdb  redis.Cmdable
	mu   sync.Mutex
	subs map[string]map[*Subscription]struct{}
}

// New creates a Hub; call Run to attach the Redis listener.
func New(rdb redis.Cmdable) *Hub {
	return &Hub{rdb: rdb, subs: map[string]map[*Subscription]struct{}{}}
}

// Key builds "type:mediaId:suffix" (suffix = goalDigest for analysis, "default" otherwise).
func Key(mediaID int64, typ, goal string, mode model.AnalysisMode) (string, error) {
	suffix := "default"
	if typ == TypeAnalysis {
		d, err := taskkeys.GoalDigest(goal, mode)
		if err != nil {
			return "", err
		}
		suffix = d
	}
	return typ + ":" + strconv.FormatInt(mediaID, 10) + ":" + suffix, nil
}

// Subscribe registers a subscriber and queues the initial event.
func (h *Hub) Subscribe(mediaID int64, typ, goal string, mode model.AnalysisMode,
	initial model.TaskStatus, stage model.TaskStage) (*Subscription, error) {
	key, err := Key(mediaID, typ, goal, mode)
	if err != nil {
		return nil, err
	}
	ch := make(chan model.TaskEvent, subscriberBuffer)
	sub := &Subscription{Events: ch, ch: ch, hub: h, key: key}
	h.mu.Lock()
	if h.subs[key] == nil {
		h.subs[key] = map[*Subscription]struct{}{}
	}
	h.subs[key][sub] = struct{}{}
	h.mu.Unlock()
	ch <- model.EventOf(initial, stage)
	return sub, nil
}

func (h *Hub) remove(key string, sub *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.subs[key]; set != nil {
		delete(set, sub)
		if len(set) == 0 {
			delete(h.subs, key)
		}
	}
}

// PublishAnalysis publishes an analysis event.
func (h *Hub) PublishAnalysis(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode,
	status model.TaskStatus, stage model.TaskStage) error {
	key, err := Key(mediaID, TypeAnalysis, goal, mode)
	if err != nil {
		return err
	}
	h.publish(ctx, key, model.EventOf(status, stage))
	return nil
}

// PublishTranscription publishes a transcription event.
func (h *Hub) PublishTranscription(ctx context.Context, mediaID int64, status model.TaskStatus, stage model.TaskStage) {
	key, _ := Key(mediaID, TypeTranscription, "", model.ModeGeneral)
	h.publish(ctx, key, model.EventOf(status, stage))
}

func (h *Hub) publish(ctx context.Context, key string, ev model.TaskEvent) {
	payload, err := common.MarshalNoEscape(Envelope{Key: key, Event: ev})
	if err == nil {
		var receivers int64
		receivers, err = h.rdb.Publish(ctx, RedisChannel, payload).Result()
		if err == nil && receivers > 0 {
			return
		}
	}
	if err != nil {
		slog.Warn("task_event_redis_publish_failed", "key", key, "err", err)
	}
	h.publishLocal(key, ev)
}

func (h *Hub) publishLocal(key string, ev model.TaskEvent) {
	h.mu.Lock()
	set := h.subs[key]
	targets := make([]*Subscription, 0, len(set))
	for s := range set {
		targets = append(targets, s)
	}
	h.mu.Unlock()
	for _, s := range targets {
		select {
		case s.ch <- ev:
		default:
			// slow consumer: drop the stream like a failed emitter.send
			slog.Debug("task_event_stream_closed", "key", key)
			s.Close()
		}
	}
}

// HandleMessage processes a raw Pub/Sub payload.
func (h *Hub) HandleMessage(payload []byte) {
	var env Envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		slog.Warn("task_event_redis_message_invalid", "err", err)
		return
	}
	h.publishLocal(env.Key, env.Event)
}

// Run subscribes to the Redis channel until ctx is cancelled (go-redis reconnects automatically).
func (h *Hub) Run(ctx context.Context, rdb *redis.Client) {
	ps := rdb.Subscribe(ctx, RedisChannel)
	defer ps.Close()
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			h.HandleMessage([]byte(msg.Payload))
		}
	}
}
