// Package analysis holds the analysis orchestration: submission (idempotency, quota, enqueue),
// status, the failed-task ledger, the per-task pipeline and transcription tasks.
package analysis

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/billing"
	"dovideo/server/internal/common"
	"dovideo/server/internal/media"
	"dovideo/server/internal/model"
	"dovideo/server/internal/obs"
	"dovideo/server/internal/ratelimit"
	"dovideo/server/internal/taskevents"
	"dovideo/server/internal/taskkeys"
)

const activeTTL = 6 * time.Hour

// Sender enqueues a first attempt of an analysis task.
type Sender interface {
	PublishTask(ctx context.Context, msg model.AnalysisTaskMsg) error
}

// SubmissionResult is the outcome of Submit.
type SubmissionResult int

const (
	Accepted SubmissionResult = iota
	RateLimited
	Duplicate
	Failed
)

// Dispatcher accepts analysis submissions.
type Dispatcher struct {
	Media   *media.Service
	Rdb     redis.Cmdable
	Sender  Sender
	Limiter *ratelimit.Limiter
	Hub     *taskevents.Hub
	Agent   *agentclient.Client
	Billing *billing.Ledger // nil or a zero limit: no daily spend cap
}

// Submit enqueues an analysis (or revision) task with idempotency + quota checks.
func (d *Dispatcher) Submit(ctx context.Context, mf *model.MediaFile, goal string, revision *model.AgentFeedback, mode model.AnalysisMode) (res SubmissionResult, err error) {
	defer func() { obs.AnalysisSubmit.WithLabelValues(submitLabel(res, err)).Inc() }()
	if err := d.Billing.Check(ctx, mf.UserID); err != nil {
		return Failed, err
	}
	mode = mode.Resolve()
	mediaID := mf.ID
	action := model.ActionStart
	var contentHash string
	if revision != nil {
		action = model.ActionRevise
		contentHash = "media-" + strconv.FormatInt(mediaID, 10)
	} else {
		if contentHash, err = d.contentHash(ctx, mediaID); err != nil {
			return Failed, err
		}
	}
	digest, err := taskkeys.GoalDigest(goal, mode)
	if err != nil {
		return Failed, err
	}
	activeKey := taskkeys.Active(contentHash, digest)
	accepted, err := d.Rdb.SetNX(ctx, activeKey, strconv.FormatInt(mediaID, 10), activeTTL).Result()
	if err != nil {
		return Failed, err
	}
	if !accepted {
		return Duplicate, nil
	}
	fail := func(cause error) (SubmissionResult, error) {
		d.Rdb.Del(context.WithoutCancel(ctx), activeKey)
		if revision != nil {
			if cerr := d.Agent.CancelRevision(context.WithoutCancel(ctx), mediaID, goal, mode); cerr != nil {
				slog.Warn("cancel_staged_revision_failed", "mediaId", mediaID, "err", cerr)
			}
		}
		slog.Error("analysis_dispatch_failed", "mediaId", mediaID, "userId", mf.UserID, "err", cause)
		return Failed, nil
	}
	ok, err := d.tryAcquireQuota(ctx, mf.UserID)
	if err != nil {
		return fail(err)
	}
	if !ok {
		d.Rdb.Del(ctx, activeKey)
		return RateLimited, nil
	}
	// Keep the old result until a worker really takes over: if enqueueing fails the user still has one.
	if revision != nil {
		if _, err := d.Agent.StageRevision(ctx, revision.Normalized(mode), mode); err != nil {
			return fail(err)
		}
	}
	// Synchronous produce: 202 is returned only after the broker acknowledged the task (acks=all).
	if err := d.Sender.PublishTask(ctx, model.NewTaskMsg(mediaID, action, contentHash, goal, mode)); err != nil {
		return fail(err)
	}
	if err := d.Hub.PublishAnalysis(ctx, mediaID, goal, mode,
		model.StatusOf(model.StateQueued, "任务已进入异步分析队列"), model.StageQueued); err != nil {
		// Kafka already accepted the task; a failed notification must not masquerade as a dispatch failure.
		slog.Warn("analysis_queued_event_failed", "mediaId", mediaID, "userId", mf.UserID, "err", err)
	}
	return Accepted, nil
}

// IsActive checks both the content-hash and media-id scoped active keys.
func (d *Dispatcher) IsActive(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (bool, error) {
	digest, err := taskkeys.GoalDigest(goal, mode)
	if err != nil {
		return false, err
	}
	hash, err := d.contentHash(ctx, mediaID)
	if err != nil {
		return false, err
	}
	n, err := d.Rdb.Exists(ctx, taskkeys.Active(hash, digest), taskkeys.Active("media-"+strconv.FormatInt(mediaID, 10), digest)).Result()
	return n > 0, err
}

// RequireAiQuota consumes one AI permit or returns 429/503 business errors.
func (d *Dispatcher) RequireAiQuota(ctx context.Context, userID int64) error {
	if err := d.Billing.Check(ctx, userID); err != nil {
		return err
	}
	ok, err := d.tryAcquireQuota(ctx, userID)
	if err != nil {
		slog.Warn("ai_rate_limiter_unavailable", "userId", userID, "err", err)
		return common.Business(common.CodeServiceUnavailable, "AI 服务限流器暂不可用，请稍后再试")
	}
	if !ok {
		return common.Business(common.CodeRateLimited, "AI 请求过于频繁，请稍后再试")
	}
	return nil
}

// tryAcquireQuota charges the per-user analysis bucket, then the global one.
func (d *Dispatcher) tryAcquireQuota(ctx context.Context, userID int64) (bool, error) {
	return d.Limiter.TakeUserThenGlobal(ctx, ratelimit.ScopeAnalysis, userID, ratelimit.AnalysisUser, ratelimit.AnalysisGlobal)
}

func submitLabel(res SubmissionResult, err error) string {
	switch {
	case err != nil || res == Failed:
		return "error"
	case res == Duplicate:
		return "duplicate"
	case res == RateLimited:
		return "rate_limited"
	}
	return "accepted"
}

func (d *Dispatcher) contentHash(ctx context.Context, mediaID int64) (string, error) {
	h, err := d.Media.ContentHash(ctx, mediaID)
	if err != nil {
		return "", err
	}
	return taskkeys.NormalizeContentHash(mediaID, h), nil
}
