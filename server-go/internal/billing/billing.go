// Package billing caps how much model spend a user can cause per day.
//
// The agent service knows the tokens, so it adds each model call's estimated cost (CNY) to a Redis
// counter per user and day; this side only reads that counter before starting new AI work, and tags
// every outgoing agent call with the user (gRPC metadata Header) so the agent knows whom to charge.
// A limit of 0 turns the whole thing off, which is the default outside the public demo server.
package billing

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
)

// Header is the gRPC metadata key carrying the user id to the agent service.
const Header = "x-billing-user"

// Day boundaries follow Beijing time, the currency the limit is expressed in. agent-py uses the same
// zone and key format (agent-py/app/billing.py); keep them in step.
var zone = time.FixedZone("UTC+8", 8*3600)

// Key is the Redis key holding user uid's spend on the day containing now.
func Key(uid int64, now time.Time) string {
	return "billing:spend:" + strconv.FormatInt(uid, 10) + ":" + now.In(zone).Format("2006-01-02")
}

type ctxKey struct{}

// WithUser marks ctx as acting for user uid; agentclient forwards it to the agent.
func WithUser(ctx context.Context, uid int64) context.Context {
	return context.WithValue(ctx, ctxKey{}, uid)
}

// UserFrom returns the user set by WithUser.
func UserFrom(ctx context.Context) (int64, bool) {
	uid, ok := ctx.Value(ctxKey{}).(int64)
	return uid, ok
}

// Ledger checks users against the daily limit.
type Ledger struct {
	Rdb       redis.Cmdable
	Limit     float64          // CNY per user per day; 0 disables the check
	Unlimited map[int64]bool   // e.g. the owner's account
	Now       func() time.Time // nil means time.Now
}

// Limited reports whether uid is subject to the daily limit.
func (l *Ledger) Limited(uid int64) bool {
	return l != nil && l.Limit > 0 && !l.Unlimited[uid]
}

// Spent returns uid's spend today in CNY.
func (l *Ledger) Spent(ctx context.Context, uid int64) (float64, error) {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	v, err := l.Rdb.Get(ctx, Key(uid, now())).Float64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return v, err
}

// Check returns a 429 business error once uid has used up today's allowance. If Redis cannot be read
// the request is let through: the per-minute rate limits still apply, and a demo that breaks because
// of the ledger is worse than a few cents over the cap.
func (l *Ledger) Check(ctx context.Context, uid int64) error {
	if !l.Limited(uid) {
		return nil
	}
	spent, err := l.Spent(ctx, uid)
	if err != nil {
		slog.Warn("billing_ledger_unavailable", "userId", uid, "err", err)
		return nil
	}
	if spent >= l.Limit {
		return common.Business(common.CodeQuotaExhausted, "这个账号今天的 AI 额度已用完，明天再来，或联系作者获取内测账号")
	}
	return nil
}
