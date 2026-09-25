package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
)

func TestKeyUsesBeijingDay(t *testing.T) {
	// 17:00 UTC is already the next day in Beijing
	now := time.Date(2026, 9, 25, 17, 0, 0, 0, time.UTC)
	if got := Key(7, now); got != "billing:spend:7:2026-09-26" {
		t.Fatalf("got %s", got)
	}
}

func TestCheck(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	now := time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC)
	l := &Ledger{Rdb: rdb, Limit: 1.5, Unlimited: map[int64]bool{3: true}, Now: func() time.Time { return now }}
	ctx := context.Background()

	if err := l.Check(ctx, 2); err != nil {
		t.Fatalf("nothing spent yet: %v", err)
	}
	mr.Set(Key(2, now), "1.49")
	if err := l.Check(ctx, 2); err != nil {
		t.Fatalf("under the limit: %v", err)
	}
	mr.Set(Key(2, now), "1.5")
	err := l.Check(ctx, 2)
	var be *common.Error
	if !errors.As(err, &be) || be.Code != common.CodeQuotaExhausted {
		t.Fatalf("at the limit want quota error, got %v", err)
	}
	mr.Set(Key(3, now), "99")
	if err := l.Check(ctx, 3); err != nil {
		t.Fatalf("unlimited user must pass: %v", err)
	}
	if err := (&Ledger{Rdb: rdb}).Check(ctx, 2); err != nil {
		t.Fatalf("zero limit disables the check: %v", err)
	}
	var nilLedger *Ledger
	if err := nilLedger.Check(ctx, 2); err != nil {
		t.Fatalf("nil ledger disables the check: %v", err)
	}
	mr.Close()
	if err := l.Check(ctx, 2); err != nil {
		t.Fatalf("an unreachable ledger must not block the demo: %v", err)
	}
}

func TestUserContext(t *testing.T) {
	if _, ok := UserFrom(context.Background()); ok {
		t.Fatal("no user on a bare context")
	}
	if uid, ok := UserFrom(WithUser(context.Background(), 9)); !ok || uid != 9 {
		t.Fatalf("got %d %v", uid, ok)
	}
}
