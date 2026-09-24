package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newLimiter(t *testing.T) (*Limiter, *miniredis.Miniredis, *time.Time) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	l := New(rdb)
	clock := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return clock }
	return l, mr, &clock
}

func TestTokenBucketBurstThenRefill(t *testing.T) {
	l, mr, clock := newLimiter(t)
	ctx := context.Background()
	key := UserKey(ScopeAnalysis, 1)
	b := AnalysisUser // 5 tokens, 5 per minute => one token every 12s

	steps := []struct {
		name    string
		advance time.Duration
		key     string
		want    bool
	}{
		{"burst 1", 0, key, true},
		{"burst 2", 0, key, true},
		{"burst 3", 0, key, true},
		{"burst 4", 0, key, true},
		{"burst 5", 0, key, true},
		{"bucket empty", 0, key, false},
		{"other user has its own bucket", 0, UserKey(ScopeAnalysis, 2), true},
		{"not yet refilled after 11s", 11 * time.Second, key, false},
		{"one token after 12s", time.Second, key, true},
		{"and only one", 0, key, false},
		{"idle long: capped at capacity", time.Hour, key, true},
	}
	for _, s := range steps {
		*clock = clock.Add(s.advance)
		got, err := l.Take(ctx, s.key, b)
		if err != nil || got != s.want {
			t.Fatalf("%s: got %v, %v want %v", s.name, got, err, s.want)
		}
	}
	// capacity 5 after a long idle: 4 left after the take above, then empty
	for i := 0; i < 4; i++ {
		if ok, _ := l.Take(ctx, key, b); !ok {
			t.Fatalf("burst after idle %d denied", i)
		}
	}
	if ok, _ := l.Take(ctx, key, b); ok {
		t.Fatal("capacity must cap the refill")
	}
	// TTL = time to refill a full bucket from empty (5 tokens * 12s)
	if ttl := mr.TTL(key); ttl <= 59*time.Second || ttl > 60*time.Second {
		t.Fatalf("ttl %v", ttl)
	}
}

func TestClockGoingBackwardsMintsNothing(t *testing.T) {
	l, _, clock := newLimiter(t)
	ctx := context.Background()
	b := Bucket{Capacity: 1, Refill: 1, Period: time.Minute}
	if ok, _ := l.Take(ctx, "k", b); !ok {
		t.Fatal("first take")
	}
	*clock = clock.Add(-time.Hour)
	if ok, _ := l.Take(ctx, "k", b); ok {
		t.Fatal("a lagging clock must not refill")
	}
}

func TestUserThenGlobal(t *testing.T) {
	l, mr, _ := newLimiter(t)
	ctx := context.Background()
	user := Bucket{Capacity: 3, Refill: 3, Period: time.Minute}
	global := Bucket{Capacity: 2, Refill: 2, Period: time.Minute}
	want := []bool{true, true, false}
	for i, w := range want {
		got, err := l.TakeUserThenGlobal(ctx, ScopeRoute, 7, user, global)
		if err != nil || got != w {
			t.Fatalf("call %d: %v %v", i, got, err)
		}
	}
	// the third call consumed a user token even though the global bucket rejected it
	if v := mr.HGet(UserKey(ScopeRoute, 7), "tokens"); v != "0" {
		t.Fatalf("user tokens %q", v)
	}
	if !mr.Exists(GlobalKey(ScopeRoute)) || GlobalKey(ScopeAnalysis) != "ratelimit:analysis:global" {
		t.Fatal("key layout")
	}
}
