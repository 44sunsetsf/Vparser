package workerpool

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestRejectsWhenFull(t *testing.T) {
	p := New("t", 1, 1) // 1 running + 1 queued
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	if err := p.Submit(func() { started.Done(); <-release }); err != nil {
		t.Fatal(err)
	}
	started.Wait()
	if err := p.Submit(func() { <-release }); err != nil {
		t.Fatalf("queued task must be accepted: %v", err)
	}
	if err := p.Submit(func() {}); err != ErrRejected {
		t.Fatalf("third task must be rejected, got %v", err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.Shutdown(ctx)
	if err := p.Submit(func() {}); err != nil {
		t.Fatalf("capacity must be released after completion: %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	p := New("t", 2, 10)
	var mu sync.Mutex
	running, peak := 0, 0
	for i := 0; i < 6; i++ {
		_ = p.Submit(func() {
			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.Shutdown(ctx)
	if peak > 2 {
		t.Fatalf("peak concurrency %d exceeds 2", peak)
	}
}
