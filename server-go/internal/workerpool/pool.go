// Package workerpool is a bounded executor: at most `workers` tasks run concurrently, at most
// `workers+queue` are accepted in total, and Submit fails fast beyond that. Failing fast (instead
// of blocking the caller or growing an unbounded queue) turns overload into an immediate 503 the
// client can retry, rather than latency that grows until requests time out.
package workerpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ErrRejected is returned when the pool is saturated.
var ErrRejected = errors.New("worker pool is full")

type Pool struct {
	name     string
	run      chan struct{}
	capacity int64
	inflight atomic.Int64
	wg       sync.WaitGroup
}

// New creates a pool with `workers` concurrent slots and `queue` waiting slots.
func New(name string, workers, queue int) *Pool {
	if workers < 1 {
		workers = 1
	}
	return &Pool{name: name, run: make(chan struct{}, workers), capacity: int64(workers + queue)}
}

func (p *Pool) Name() string { return p.name }

// Submit schedules fn or returns ErrRejected when the pool is saturated.
func (p *Pool) Submit(fn func()) error {
	if p.inflight.Add(1) > p.capacity {
		p.inflight.Add(-1)
		return ErrRejected
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer p.inflight.Add(-1)
		p.run <- struct{}{}
		defer func() { <-p.run }()
		fn()
	}()
	return nil
}

// Shutdown waits for accepted tasks until ctx expires.
func (p *Pool) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
