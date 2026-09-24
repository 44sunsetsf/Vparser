// Package redislock is a lease-based distributed lock: SET NX PX with a random owner token, a
// watchdog that keeps renewing the lease while the owner is alive, and a Lua compare-and-delete
// unlock so an owner whose lease expired can never delete a successor's lock.
//
// A short lease plus renewal (instead of one long TTL) bounds how long a crashed owner blocks
// others, while still supporting multi-minute critical sections such as an agent run.
package redislock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/obs"
)

// LockName is the lock family used as a metric label: the key without its lock: prefix and without
// per-entity suffixes (lock:analysis:{hash}:{digest} -> analysis, lock:upload:merge:{id} -> upload).
func LockName(key string) string {
	parts := strings.SplitN(strings.TrimPrefix(key, "lock:"), ":", 2)
	return parts[0]
}

const (
	DefaultTTL           = 30 * time.Second
	DefaultRenewInterval = 10 * time.Second
	DefaultPollInterval  = 100 * time.Millisecond
)

var unlockScript = redis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('del', KEYS[1])
end
return 0`)

var renewScript = redis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('pexpire', KEYS[1], ARGV[2])
end
return 0`)

// Options tunes timings (tests shorten them).
type Options struct {
	TTL           time.Duration
	RenewInterval time.Duration
	PollInterval  time.Duration
}

// Locker creates locks against a Redis client.
type Locker struct {
	rdb  redis.Scripter
	opts Options
}

// New builds a Locker (30s lease, renewed every 10s: two renewals may fail before the lease lapses).
func New(rdb redis.Scripter, opts ...Options) *Locker {
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.TTL <= 0 {
		o.TTL = DefaultTTL
	}
	if o.RenewInterval <= 0 {
		o.RenewInterval = DefaultRenewInterval
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultPollInterval
	}
	return &Locker{rdb: rdb, opts: o}
}

// Lock is a held lock. Lost() closes when the lease can no longer be guaranteed.
type Lock struct {
	l     *Locker
	key   string
	token string

	stop     chan struct{}
	lost     chan struct{}
	mu       sync.Mutex
	released bool
	isLost   bool
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TryLock polls until the lock is acquired or wait elapses; wait <= 0 is a single attempt.
// The boolean reports acquisition. Every call is counted in redis_lock_acquire_total.
func (l *Locker) TryLock(ctx context.Context, key string, wait time.Duration) (lk *Lock, ok bool, err error) {
	defer func() {
		result := "acquired"
		switch {
		case err != nil:
			result = "error"
		case !ok:
			result = "contended"
		}
		obs.RedisLockAcquire.WithLabelValues(LockName(key), result).Inc()
	}()
	token := randomToken()
	deadline := time.Now().Add(wait)
	for {
		ok, err := l.acquire(ctx, key, token)
		if err != nil {
			return nil, false, err
		}
		if ok {
			lk := &Lock{l: l, key: key, token: token, stop: make(chan struct{}), lost: make(chan struct{})}
			go lk.watchdog()
			return lk, true, nil
		}
		remaining := time.Until(deadline)
		if wait <= 0 || remaining <= 0 {
			return nil, false, nil
		}
		sleep := l.opts.PollInterval
		if sleep > remaining {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

func (l *Locker) acquire(ctx context.Context, key, token string) (bool, error) {
	cmd, ok := l.rdb.(redis.Cmdable)
	if !ok {
		panic("redislock: client must implement redis.Cmdable")
	}
	return cmd.SetNX(ctx, key, token, l.opts.TTL).Result()
}

func (lk *Lock) watchdog() {
	ticker := time.NewTicker(lk.l.opts.RenewInterval)
	defer ticker.Stop()
	lastOK := time.Now()
	for {
		select {
		case <-lk.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), lk.l.opts.RenewInterval)
			n, err := renewScript.Run(ctx, lk.l.rdb, []string{lk.key}, lk.token, lk.l.opts.TTL.Milliseconds()).Int64()
			cancel()
			switch {
			case err == nil && n == 1:
				lastOK = time.Now()
			case err == nil:
				lk.markLost() // key expired or owned by someone else
				return
			default:
				// transient Redis error: keep trying until the lease certainly expired
				if time.Since(lastOK) >= lk.l.opts.TTL {
					lk.markLost()
					return
				}
			}
		}
	}
}

func (lk *Lock) markLost() {
	lk.mu.Lock()
	defer lk.mu.Unlock()
	if lk.isLost || lk.released {
		return
	}
	lk.isLost = true
	close(lk.lost)
}

// Lost is closed when renewal failed and the lock must be considered gone.
func (lk *Lock) Lost() <-chan struct{} { return lk.lost }

// IsHeld reports whether this owner still believes it holds the lock.
func (lk *Lock) IsHeld() bool {
	if lk == nil {
		return false
	}
	lk.mu.Lock()
	defer lk.mu.Unlock()
	return !lk.released && !lk.isLost
}

// Unlock stops the watchdog and deletes the key only if we still own it.
func (lk *Lock) Unlock(ctx context.Context) error {
	if lk == nil {
		return nil
	}
	lk.mu.Lock()
	if lk.released {
		lk.mu.Unlock()
		return nil
	}
	lk.released = true
	close(lk.stop)
	lk.mu.Unlock()
	return unlockScript.Run(ctx, lk.l.rdb, []string{lk.key}, lk.token).Err()
}
