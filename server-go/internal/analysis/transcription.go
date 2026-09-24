package analysis

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/media"
	"dovideo/server/internal/model"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/taskevents"
	"dovideo/server/internal/videocontext"
	"dovideo/server/internal/workerpool"
)

const transcriptionActiveTTL = 2 * time.Hour

// Transcriptions runs standalone transcription tasks on the AI pool and tracks their state in Redis.
type Transcriptions struct {
	MediaRepo   *repo.Media
	Media       *media.Service
	Transcriber *videocontext.Transcriber
	Rdb         redis.Cmdable
	Hub         *taskevents.Hub
	Pool        *workerpool.Pool
}

func activeKey(id int64) string { return "transcription:active:" + strconv.FormatInt(id, 10) }
func stateKey(id int64) string  { return "transcription:state:" + strconv.FormatInt(id, 10) }

// Queue marks the task active; false when one is already running.
func (t *Transcriptions) Queue(ctx context.Context, mediaID int64) (bool, error) {
	ok, err := t.Rdb.SetNX(ctx, activeKey(mediaID), "1", transcriptionActiveTTL).Result()
	if err != nil || !ok {
		return false, err
	}
	t.setState(ctx, mediaID, model.StateQueued, transcriptionActiveTTL)
	t.Hub.PublishTranscription(ctx, mediaID, model.StatusOf(model.StateQueued, "文字提取任务已排队"), model.StageQueued)
	return true, nil
}

// Transcribe dispatches the work to the AI pool; workerpool.ErrRejected means the pool is full.
func (t *Transcriptions) Transcribe(mediaID int64) error {
	return t.Pool.Submit(func() { t.run(context.Background(), mediaID) })
}

func (t *Transcriptions) run(ctx context.Context, mediaID int64) {
	mf, err := t.MediaRepo.FindByID(ctx, mediaID)
	if err != nil || mf == nil {
		t.clearActive(ctx, mediaID)
		return
	}
	defer t.clearActive(ctx, mediaID)
	fail := func(cause error) {
		t.setState(ctx, mediaID, model.StateFailed, time.Hour)
		t.Hub.PublishTranscription(ctx, mediaID, model.StatusOf(model.StateFailed, "文字提取失败，请稍后重试"), model.StageFailed)
		slog.Error("transcription_failed", "mediaId", mediaID, "err", cause)
	}
	t.setState(ctx, mediaID, model.StateProcessing, transcriptionActiveTTL)
	t.Hub.PublishTranscription(ctx, mediaID, model.StatusOf(model.StateProcessing, "正在识别视频语音"), model.StageASR)
	src, err := t.Media.ReadableSource(ctx, mf.FilePath)
	if err != nil {
		fail(err)
		return
	}
	text, err := t.Transcriber.TranscribeVideo(ctx, src)
	if err != nil {
		fail(err)
		return
	}
	if err := t.MediaRepo.UpdateTranscript(ctx, mediaID, text); err != nil {
		fail(err)
		return
	}
	t.Media.InvalidateUserList(ctx, mf.UserID)
	t.setState(ctx, mediaID, model.StateCompleted, 7*24*time.Hour)
	t.Hub.PublishTranscription(ctx, mediaID, model.StatusCompleted(text), model.StageCompleted)
	slog.Info("transcription_completed", "mediaId", mediaID)
}

// RejectQueued rolls back the "queued" marker when dispatch failed.
func (t *Transcriptions) RejectQueued(ctx context.Context, mediaID int64) {
	t.clearActive(ctx, mediaID)
	t.setState(ctx, mediaID, model.StateFailed, 10*time.Minute)
	t.Hub.PublishTranscription(ctx, mediaID, model.StatusOf(model.StateFailed, "任务队列已满，请稍后重试"), model.StageDispatchFailed)
}

// Status reports the transcription state; a stored transcript always wins over runtime state.
func (t *Transcriptions) Status(ctx context.Context, mf *model.MediaFile) (model.TaskStatus, error) {
	if mf.TranscriptText != nil && strings.TrimSpace(*mf.TranscriptText) != "" {
		return model.StatusCompleted(*mf.TranscriptText), nil
	}
	v, err := t.Rdb.Get(ctx, stateKey(mf.ID)).Result()
	if errors.Is(err, redis.Nil) {
		n, err := t.Rdb.Exists(ctx, activeKey(mf.ID)).Result()
		if err != nil {
			return model.TaskStatus{}, err
		}
		if n > 0 {
			return model.StatusOf(model.StateProcessing, "正在提取文字"), nil
		}
		return model.StatusOf(model.StateNotStarted, "尚未提交文字提取任务"), nil
	}
	if err != nil {
		return model.TaskStatus{}, err
	}
	if !model.ValidState(v) {
		slog.Warn("invalid_transcription_state", "mediaId", mf.ID, "state", v)
		return model.StatusOf(model.StateNotStarted, "任务状态不可用"), nil
	}
	switch model.State(v) {
	case model.StateCompleted:
		return model.StatusCompletedNullable(mf.TranscriptText), nil
	case model.StateFailed:
		return model.StatusOf(model.StateFailed, "文字提取失败，请稍后重试"), nil
	case model.StateQueued:
		return model.StatusOf(model.StateQueued, "文字提取任务已排队"), nil
	case model.StateProcessing:
		return model.StatusOf(model.StateProcessing, "正在提取文字"), nil
	default:
		return model.StatusOf(model.StateNotStarted, "尚未提交文字提取任务"), nil
	}
}

func (t *Transcriptions) setState(ctx context.Context, mediaID int64, st model.State, ttl time.Duration) {
	if err := t.Rdb.Set(ctx, stateKey(mediaID), string(st), ttl).Err(); err != nil {
		slog.Warn("transcription_state_write_failed", "mediaId", mediaID, "state", st, "err", err)
	}
}

func (t *Transcriptions) clearActive(ctx context.Context, mediaID int64) {
	if err := t.Rdb.Del(ctx, activeKey(mediaID)).Err(); err != nil {
		slog.Warn("transcription_active_cleanup_failed", "mediaId", mediaID, "err", err)
	}
}
