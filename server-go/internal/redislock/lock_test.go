package redislock

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return mr, rdb
}

func TestExclusiveAndCompareAndDelete(t *testing.T) {
	mr, rdb := setup(t)
	l := New(rdb)
	ctx := context.Background()
	a, ok, err := l.TryLock(ctx, "lock:x", 0)
	if err != nil || !ok {
		t.Fatalf("first TryLock = %v, %v", ok, err)
	}
	if _, ok, _ := l.TryLock(ctx, "lock:x", 0); ok {
		t.Fatal("second owner must not acquire")
	}
	// another owner's token must not be deleted by a stale Unlock
	if err := mr.Set("lock:x", "someone-else"); err != nil {
		t.Fatal(err)
	}
	if err := a.Unlock(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := mr.Get("lock:x"); v != "someone-else" {
		t.Fatalf("stale unlock removed foreign lock, value=%q", v)
	}
	mr.Del("lock:x")
	b, ok, _ := l.TryLock(ctx, "lock:x", 0)
	if !ok {
		t.Fatal("lock should be free after delete")
	}
	if !b.IsHeld() {
		t.Fatal("IsHeld should be true")
	}
	_ = b.Unlock(ctx)
	if b.IsHeld() || mr.Exists("lock:x") {
		t.Fatal("unlock must release")
	}
	if err := b.Unlock(ctx); err != nil {
		t.Fatalf("double unlock should be a no-op: %v", err)
	}
}

func TestTryLockWaits(t *testing.T) {
	mr, rdb := setup(t)
	l := New(rdb, Options{PollInterval: 10 * time.Millisecond})
	ctx := context.Background()
	holder, _, _ := l.TryLock(ctx, "lock:w", 0)
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = holder.Unlock(ctx)
	}()
	start := time.Now()
	lk, ok, err := l.TryLock(ctx, "lock:w", 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("waiting TryLock = %v, %v", ok, err)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("must have waited for the holder")
	}
	_ = lk.Unlock(ctx)
	// wait expires -> false
	_, _, _ = l.TryLock(ctx, "lock:w2", 0)
	if _, ok, _ := l.TryLock(ctx, "lock:w2", 50*time.Millisecond); ok {
		t.Fatal("should time out")
	}
	_ = mr
}

func TestWatchdogRenews(t *testing.T) {
	mr, rdb := setup(t)
	l := New(rdb, Options{TTL: 200 * time.Millisecond, RenewInterval: 40 * time.Millisecond})
	ctx := context.Background()
	lk, ok, _ := l.TryLock(ctx, "lock:r", 0)
	if !ok {
		t.Fatal("acquire")
	}
	// miniredis TTLs only advance via FastForward; simulate elapsed lease and verify the watchdog re-arms it.
	deadline := time.Now().Add(1 * time.Second)
	renewals := 0
	for time.Now().Before(deadline) && renewals < 3 {
		mr.FastForward(150 * time.Millisecond)
		time.Sleep(60 * time.Millisecond)
		if mr.Exists("lock:r") {
			renewals++
		}
	}
	if renewals < 3 || !lk.IsHeld() {
		t.Fatalf("watchdog did not keep the lease (renewals=%d)", renewals)
	}
	_ = lk.Unlock(ctx)
}

func TestLostWhenStolen(t *testing.T) {
	mr, rdb := setup(t)
	l := New(rdb, Options{TTL: time.Second, RenewInterval: 20 * time.Millisecond})
	ctx := context.Background()
	lk, _, _ := l.TryLock(ctx, "lock:l", 0)
	_ = mr.Set("lock:l", "thief")
	select {
	case <-lk.Lost():
	case <-time.After(time.Second):
		t.Fatal("Lost() should close when renewal detects a foreign owner")
	}
	if lk.IsHeld() {
		t.Fatal("IsHeld must be false after loss")
	}
	_ = lk.Unlock(ctx)
	if v, _ := mr.Get("lock:l"); v != "thief" {
		t.Fatal("unlock after loss must not delete foreign lock")
	}
}
