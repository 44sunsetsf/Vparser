// Package ratelimit is a Redis token bucket shared by every gateway instance.
//
// A token bucket (rather than a fixed or sliding window) allows short bursts up to the capacity
// while enforcing the long-run rate, and its whole state is two numbers, so each decision is a
// single atomic Lua call with O(1) memory per key.
package ratelimit

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bucket is a capacity plus a steady refill rate.
type Bucket struct {
	Capacity int
	Refill   int           // tokens added per Period
	Period   time.Duration // refill period
}

func perMinute(n int) Bucket { return Bucket{Capacity: n, Refill: n, Period: time.Minute} }

// Buckets used by the gateway (contract §5).
var (
	AnalysisUser   = perMinute(5)
	AnalysisGlobal = perMinute(30)
	RouteUser      = perMinute(10)
	RouteGlobal    = perMinute(60)
)

// Key scopes.
const (
	ScopeAnalysis = "analysis"
	ScopeRoute    = "route"
)

// UserKey is ratelimit:{scope}:user:{id}.
func UserKey(scope string, userID int64) string {
	return "ratelimit:" + scope + ":user:" + strconv.FormatInt(userID, 10)
}

// GlobalKey is ratelimit:{scope}:global.
func GlobalKey(scope string) string { return "ratelimit:" + scope + ":global" }

// takeScript refills by elapsed time (capped at capacity), then tries to take one token.
// ts only moves forward, so a gateway with a slightly lagging clock cannot mint extra tokens.
// The key expires once the bucket would be full again: an idle bucket is indistinguishable
// from a missing one, so there is no reason to keep it.
var takeScript = redis.NewScript(`
local capacity = tonumber(ARGV[1])
local rate = tonumber(ARGV[2]) / tonumber(ARGV[3])
local now = tonumber(ARGV[4])
local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(state[1])
local ts = tonumber(state[2])
if tokens == nil or ts == nil then
  tokens = capacity
  ts = now
end
if now > ts then
  tokens = math.min(capacity, tokens + (now - ts) * rate)
  ts = now
end
local allowed = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
end
redis.call('HSET', KEYS[1], 'tokens', tostring(tokens), 'ts', tostring(ts))
local ttl = math.ceil((capacity - tokens) / rate)
if ttl < 1 then ttl = 1 end
redis.call('PEXPIRE', KEYS[1], ttl)
return allowed`)

// Limiter evaluates token buckets stored in Redis.
type Limiter struct {
	rdb redis.Scripter
	now func() time.Time
}

func New(rdb redis.Scripter) *Limiter { return &Limiter{rdb: rdb, now: time.Now} }

// Take consumes one token from the bucket at key.
func (l *Limiter) Take(ctx context.Context, key string, b Bucket) (bool, error) {
	res, err := takeScript.Run(ctx, l.rdb, []string{key},
		b.Capacity, b.Refill, b.Period.Milliseconds(), l.now().UnixMilli()).Int64()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

// TakeUserThenGlobal charges the user bucket first so one noisy user is throttled before it can
// drain the shared budget. A token already taken from the user bucket is not refunded when the
// global bucket is empty: refunds would need a second round-trip and would let a user probe the
// global limit for free.
func (l *Limiter) TakeUserThenGlobal(ctx context.Context, scope string, userID int64, user, global Bucket) (bool, error) {
	ok, err := l.Take(ctx, UserKey(scope, userID), user)
	if err != nil || !ok {
		return false, err
	}
	return l.Take(ctx, GlobalKey(scope), global)
}
