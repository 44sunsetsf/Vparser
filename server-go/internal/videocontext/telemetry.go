package videocontext

import (
	"sync"

	"dovideo/server/internal/agentclient"
)

// Counters is the subset of telemetry Go accumulates; nil is allowed everywhere.
type Counters interface {
	Increment(name string, delta int64)
}

// Seed accumulates the telemetry seed sent with Run; the agent merges it into its own record.
type Seed struct {
	mu   sync.Mutex
	seed agentclient.TelemetrySeed
}

func NewSeed(traceID string) *Seed {
	return &Seed{seed: agentclient.TelemetrySeed{
		TraceID: traceID, Stages: map[string]agentclient.StageSeed{}, Counters: map[string]int64{},
	}}
}

func (s *Seed) Increment(name string, delta int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.seed.Counters[name] += delta
	s.mu.Unlock()
}

// Stage records a finished stage (duration + success).
func (s *Seed) Stage(name string, durationMs int64, success bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.seed.Stages[name] = agentclient.StageSeed{DurationMs: durationMs, Success: success}
	s.mu.Unlock()
}

// Snapshot returns a copy safe to marshal.
func (s *Seed) Snapshot() *agentclient.TelemetrySeed {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := agentclient.TelemetrySeed{TraceID: s.seed.TraceID,
		Stages: map[string]agentclient.StageSeed{}, Counters: map[string]int64{}}
	for k, v := range s.seed.Stages {
		out.Stages[k] = v
	}
	for k, v := range s.seed.Counters {
		out.Counters[k] = v
	}
	return &out
}

func inc(c Counters, name string) {
	if c != nil {
		c.Increment(name, 1)
	}
}
