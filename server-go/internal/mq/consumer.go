package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/analysis"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/obs"
	"dovideo/server/internal/redislock"
	"dovideo/server/internal/taskkeys"
)

const activeTTL = 6 * time.Hour

// Outcome labels kafka_consume_duration_seconds{outcome}.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeSkipped   Outcome = "skipped" // another worker holds the task, or the media was deleted
	OutcomeRetry     Outcome = "retry"
	OutcomeDLQ       Outcome = "dlq"
	OutcomeBudget    Outcome = "budget_exhausted"
	OutcomePoison    Outcome = "poison"
	OutcomeRedeliver Outcome = "redeliver" // not handled; the same record is attempted again
)

// Analyzer is the orchestration surface the consumer needs.
type Analyzer interface {
	AsyncAnalyze(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) error
	ReuseResult(ctx context.Context, mediaID, sourceMediaID int64, goal string, mode model.AnalysisMode) (*agentclient.ReuseResponse, error)
}

// Agent is the agent surface the consumer needs.
type Agent interface {
	Result(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (*agentclient.ResultResponse, error)
	BeginRevision(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (bool, error)
	CompleteRevision(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) error
}

// Stages persists goal-level stage checkpoints.
type Stages interface {
	SaveStage(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode, stage model.TaskStage) error
}

// Ledger records failed tasks for manual replay.
type Ledger interface {
	Record(ctx context.Context, msg *model.AnalysisTaskMsg, attempts int, cause error) error
}

// MediaOps is the media surface the consumer needs.
type MediaOps interface {
	Exists(ctx context.Context, mediaID int64) (bool, error)
	PurgeRuntimeArtifacts(ctx context.Context, mediaID int64)
}

// Events publishes analysis events.
type Events interface {
	PublishAnalysis(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode, status model.TaskStatus, stage model.TaskStage) error
}

// Publisher writes a record and waits for the acknowledgement.
type Publisher interface {
	Produce(ctx context.Context, rec *kgo.Record) error
}

// Consumer handles analysis task records.
type Consumer struct {
	Rdb       redis.Cmdable
	Locker    *redislock.Locker
	Analyzer  Analyzer
	Agent     Agent
	Stages    Stages
	Publisher Publisher
	Ledger    Ledger
	Media     MediaOps
	Events    Events
	Now       func() time.Time
}

func (c *Consumer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// RejectionReason validates a message; "" means it is acceptable.
func RejectionReason(msg *model.AnalysisTaskMsg) string {
	switch {
	case msg == nil:
		return "消息体为空"
	case msg.MediaID == nil:
		return "缺少 mediaId"
	case msg.UserGoal == nil || strings.TrimSpace(*msg.UserGoal) == "":
		return "缺少分析目标"
	case !msg.HasSupportedAction():
		return "不支持的 action=" + deref(msg.Action)
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return "null"
	}
	return *s
}

func deref0(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Handle processes one delivery and reports how it ended. A non-nil error means the record was not
// handled (for example the retry topic was unreachable) and must be attempted again before its
// offset may be committed.
func (c *Consumer) Handle(ctx context.Context, d Delivery) (Outcome, error) {
	reason := RejectionReason(d.Msg)
	if d.DecodeErr != nil {
		reason = "消息无法解析: " + d.DecodeErr.Error()
	}
	if reason != "" {
		return c.discardPoison(ctx, d, reason)
	}
	msg := d.Msg
	mediaID, goal := *msg.MediaID, *msg.UserGoal
	mode := model.ModeFromNullable(deref0(msg.Mode))
	contentHash := taskkeys.NormalizeContentHash(mediaID, deref0(msg.ContentHash))
	digest, err := taskkeys.GoalDigest(goal, mode)
	if err != nil {
		return c.discardPoison(ctx, d, err.Error())
	}
	activeKey := taskkeys.Active(contentHash, digest)
	completedKey := taskkeys.Completed(contentHash, digest)
	attempt := max(d.Attempt, 1)
	bg := context.WithoutCancel(ctx)

	var lock *redislock.Lock
	defer func() {
		if lock.IsHeld() {
			if err := lock.Unlock(bg); err != nil {
				slog.Warn("analysis_lock_unlock_failed", "mediaId", mediaID, "err", err)
			}
		}
	}()

	outcome, bodyErr := func() (Outcome, error) {
		l, ok, err := c.Locker.TryLock(ctx, taskkeys.Lock(contentHash, digest), 0)
		if err != nil {
			return "", err
		}
		if !ok {
			// Another worker is running this exact task; its outcome covers this duplicate.
			slog.Info("video_analysis_skipped", "mediaId", mediaID, "reason", "lock_held")
			return OutcomeSkipped, nil
		}
		lock = l
		// losing the lease means another worker may start the same task: stop working
		wctx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-l.Lost():
				slog.Error("video_analysis_lock_lost", "mediaId", mediaID)
				cancel()
			case <-wctx.Done():
			}
		}()
		return c.process(wctx, msg, mediaID, goal, mode, completedKey)
	}()

	if bodyErr == nil {
		if lock != nil {
			c.Rdb.Del(bg, activeKey)
		}
		return outcome, nil
	}
	// Budget exhaustion is a terminal business outcome: retrying would exhaust it again.
	if common.Has(bodyErr, common.KindBudgetExhausted) {
		c.Rdb.Del(bg, activeKey)
		c.saveStage(bg, mediaID, goal, mode, model.StageBudgetExhausted)
		c.publish(bg, mediaID, goal, mode, model.StatusOf(model.StateFailed, budgetMessage(bodyErr)), model.StageBudgetExhausted)
		slog.Warn("video_analysis_budget_exhausted", "mediaId", mediaID, "reason", bodyErr.Error())
		return OutcomeBudget, nil
	}

	permanent := common.IsPermanentFailure(bodyErr)
	route := RouteFailure(permanent, attempt)
	firstError := d.FirstError
	if firstError == "" {
		firstError = common.CodeOf(bodyErr) + ": " + *analysis.SanitizeError(common.Describe(bodyErr))
	}
	if route.Topic != TopicDLQ {
		rec := &kgo.Record{Topic: route.Topic, Key: d.Key, Value: d.Value,
			Headers: taskHeaders(attempt+1, c.now().Add(route.Delay), firstError)}
		if err := c.Publisher.Produce(ctx, rec); err != nil {
			slog.Error("video_analysis_retry_forward_failed", "mediaId", mediaID, "topic", route.Topic, "err", err)
			return "", fmt.Errorf("forward to %s: %w", route.Topic, err)
		}
		obs.KafkaRetry.WithLabelValues(route.Topic).Inc()
		// keep `active` alive during retries, otherwise the UI thinks the task ended and resubmits it
		c.Rdb.Expire(bg, activeKey, activeTTL)
		c.saveStage(bg, mediaID, goal, mode, model.StageRetrying)
		c.publish(bg, mediaID, goal, mode, model.StatusOf(model.StateProcessing, "本次执行失败，等待自动重试"), model.StageRetrying)
		slog.Warn("video_analysis_retry_scheduled", "mediaId", mediaID, "attempt", attempt, "next", route.Topic, "err", bodyErr)
		return OutcomeRetry, nil
	}

	if err := c.Ledger.Record(bg, msg, attempt, bodyErr); err != nil {
		slog.Error("failed_analysis_record_write_failed", "mediaId", mediaID, "err", err)
	}
	if err := c.deadLetter(ctx, d, attempt, firstError, route.Reason); err != nil {
		slog.Error("video_analysis_dead_letter_failed", "mediaId", mediaID, "err", err)
		return "", err
	}
	c.Rdb.Del(bg, activeKey)
	c.saveStage(bg, mediaID, goal, mode, model.StageDeadLettered)
	c.publish(bg, mediaID, goal, mode, model.StatusOf(model.StateFailed, "分析失败，已进入人工处理队列"), model.StageDeadLettered)
	slog.Error("video_analysis_dead_lettered", "mediaId", mediaID, "attempts", attempt, "reason", route.Reason, "err", bodyErr)
	return OutcomeDLQ, nil
}

func budgetMessage(err error) string {
	var e *common.Error
	for cur := err; cur != nil; cur = errors.Unwrap(cur) {
		if ce, ok := cur.(*common.Error); ok && ce.Kind == common.KindBudgetExhausted {
			e = ce
			break
		}
	}
	if e == nil || strings.TrimSpace(e.Msg) == "" {
		return "Agent 已达到本次任务预算"
	}
	return e.Msg
}

func (c *Consumer) deadLetter(ctx context.Context, d Delivery, attempt int, firstError, reason string) error {
	rec := &kgo.Record{Topic: TopicDLQ, Key: d.Key, Value: d.Value, Headers: taskHeaders(attempt, time.Time{}, firstError,
		kgo.RecordHeader{Key: HeaderDLQReason, Value: []byte(reason)})}
	if err := c.Publisher.Produce(ctx, rec); err != nil {
		return err
	}
	obs.KafkaDLQ.WithLabelValues(reason).Inc()
	return nil
}

// process runs one attempt while holding the task lock.
func (c *Consumer) process(ctx context.Context, msg *model.AnalysisTaskMsg, mediaID int64, goal string,
	mode model.AnalysisMode, completedKey string) (Outcome, error) {
	exists, err := c.Media.Exists(ctx, mediaID)
	if err != nil {
		return "", err
	}
	if !exists {
		slog.Info("video_analysis_discarded_deleted_media", "mediaId", mediaID)
		return OutcomeSkipped, nil
	}
	if err := c.Events.PublishAnalysis(ctx, mediaID, goal, mode,
		model.StatusOf(model.StateProcessing, "视频分析任务开始执行"), model.StageConsuming); err != nil {
		return "", err
	}
	if msg.IsRevision() {
		begun, err := c.Agent.BeginRevision(ctx, mediaID, goal, mode)
		if err != nil {
			return "", err
		}
		if !begun {
			return "", common.Internal("修订任务状态不存在，等待重试", nil)
		}
		if err := c.Rdb.Del(ctx, completedKey).Err(); err != nil {
			return "", err
		}
	} else {
		completedMediaID, err := c.Rdb.Get(ctx, completedKey).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return "", err
		}
		if err == nil {
			reused, err := c.tryReuse(ctx, mediaID, goal, mode, completedKey, completedMediaID)
			if err != nil {
				return "", err
			}
			if reused {
				return OutcomeSuccess, nil
			}
		}
	}
	c.saveStage(ctx, mediaID, goal, mode, model.StageConsuming)
	if err := c.Analyzer.AsyncAnalyze(ctx, mediaID, goal, mode); err != nil {
		return "", err
	}
	if msg.IsRevision() {
		if err := c.Agent.CompleteRevision(ctx, mediaID, goal, mode); err != nil {
			return "", err
		}
	}
	exists, err = c.Media.Exists(ctx, mediaID)
	if err != nil {
		return "", err
	}
	if !exists {
		c.Media.PurgeRuntimeArtifacts(ctx, mediaID)
		slog.Info("video_analysis_cleanup_after_media_deleted", "mediaId", mediaID)
		return OutcomeSuccess, nil
	}
	if err := c.Rdb.Set(ctx, completedKey, strconv.FormatInt(mediaID, 10), 7*24*time.Hour).Err(); err != nil {
		return "", err
	}
	res, err := c.Agent.Result(ctx, mediaID, goal, mode)
	if err != nil {
		return "", err
	}
	if res.Found && res.TaskStatus != nil {
		return OutcomeSuccess, c.Events.PublishAnalysis(ctx, mediaID, goal, mode, *res.TaskStatus, model.StageCompleted)
	}
	return OutcomeSuccess, nil
}

// tryReuse attaches a finished analysis of the same content; true means the task is done.
func (c *Consumer) tryReuse(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode,
	completedKey, completedMediaID string) (bool, error) {
	sourceID, perr := strconv.ParseInt(completedMediaID, 10, 64)
	if perr != nil {
		c.Rdb.Del(ctx, completedKey)
		slog.Warn("invalid_completed_media_reference", "key", completedKey, "value", completedMediaID)
	} else {
		src, err := c.Agent.Result(ctx, sourceID, goal, mode)
		if err != nil {
			return false, err
		}
		if src.Found {
			reuse, err := c.Analyzer.ReuseResult(ctx, mediaID, sourceID, goal, mode)
			if err != nil {
				return false, err
			}
			if reuse.Reused {
				status := src.TaskStatus
				if reuse.TaskStatus != nil {
					status = reuse.TaskStatus
				}
				if status != nil {
					if err := c.Events.PublishAnalysis(ctx, mediaID, goal, mode, *status, model.StageCompletedReused); err != nil {
						return false, err
					}
				}
				slog.Info("video_analysis_reused", "mediaId", mediaID, "sourceMediaId", sourceID)
				return true, nil
			}
		}
	}
	return false, c.Rdb.Del(ctx, completedKey).Err()
}

func (c *Consumer) saveStage(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode, stage model.TaskStage) {
	if err := c.Stages.SaveStage(ctx, mediaID, goal, mode, stage); err != nil {
		slog.Warn("analysis_stage_checkpoint_failed", "mediaId", mediaID, "stage", stage, "err", err)
	}
}

func (c *Consumer) publish(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode,
	st model.TaskStatus, stage model.TaskStage) {
	if err := c.Events.PublishAnalysis(ctx, mediaID, goal, mode, st, stage); err != nil {
		slog.Warn("analysis_event_publish_failed", "mediaId", mediaID, "stage", stage, "err", err)
	}
}

func describe(msg *model.AnalysisTaskMsg) string {
	if msg == nil {
		return "null"
	}
	id := "null"
	if msg.MediaID != nil {
		id = strconv.FormatInt(*msg.MediaID, 10)
	}
	return fmt.Sprintf("mediaId=%s action=%s contentHash=%s goalLength=%d",
		id, deref(msg.Action), deref(msg.ContentHash), len([]rune(deref0(msg.UserGoal))))
}

// discardPoison parks an unusable message. It is committed as soon as either the ledger row or the
// DLQ record exists (someone can find it); only if both sinks fail is it kept for another attempt,
// because committing then would lose it silently.
func (c *Consumer) discardPoison(ctx context.Context, d Delivery, reason string) (Outcome, error) {
	slog.Error("video_analysis_poison_message", "reason", reason, "payload", describe(d.Msg),
		"topic", d.Topic, "partition", d.Partition, "offset", d.Offset)
	msg := d.Msg
	if msg == nil {
		msg = &model.AnalysisTaskMsg{}
	}
	cause := common.InvalidArgument("invalid video analysis message: " + reason)
	recorded, deadLettered := false, false
	if err := c.Ledger.Record(ctx, msg, 0, cause); err != nil {
		slog.Error("poison_message_record_failed", "payload", describe(msg), "err", err)
	} else {
		recorded = true
	}
	if err := c.deadLetter(ctx, d, d.Attempt, "INVALID_ARGUMENT: "+reason, ReasonPoison); err != nil {
		slog.Error("poison_message_dead_letter_failed", "payload", describe(msg), "err", err)
	} else {
		deadLettered = true
	}
	if !recorded && !deadLettered {
		return "", common.Internal("毒消息无法收敛：失败台账与死信主题均不可用，暂不提交位点", cause)
	}
	c.releasePoisonTaskState(ctx, msg)
	return OutcomePoison, nil
}

func (c *Consumer) releasePoisonTaskState(ctx context.Context, msg *model.AnalysisTaskMsg) {
	if msg.MediaID == nil || msg.UserGoal == nil || strings.TrimSpace(*msg.UserGoal) == "" {
		return
	}
	mode := model.ModeFromNullable(deref0(msg.Mode))
	contentHash := taskkeys.NormalizeContentHash(*msg.MediaID, deref0(msg.ContentHash))
	digest, err := taskkeys.GoalDigest(*msg.UserGoal, mode)
	if err != nil {
		slog.Warn("poison_message_state_release_failed", "payload", describe(msg), "err", err)
		return
	}
	if err := c.Rdb.Del(ctx, taskkeys.Active(contentHash, digest)).Err(); err != nil {
		slog.Warn("poison_message_state_release_failed", "payload", describe(msg), "err", err)
		return
	}
	c.saveStage(ctx, *msg.MediaID, *msg.UserGoal, mode, model.StageDeadLettered)
	c.publish(ctx, *msg.MediaID, *msg.UserGoal, mode, model.StatusOf(model.StateFailed, "任务消息非法，已终止"), model.StageDeadLettered)
}
