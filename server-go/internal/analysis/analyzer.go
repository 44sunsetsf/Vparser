package analysis

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/billing"
	"dovideo/server/internal/checkpoint"
	"dovideo/server/internal/common"
	"dovideo/server/internal/media"
	"dovideo/server/internal/model"
	"dovideo/server/internal/obs"
	"dovideo/server/internal/redislock"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/taskevents"
	"dovideo/server/internal/taskkeys"
	"dovideo/server/internal/videocontext"
)

const (
	contextLockWait = 300 * time.Second
	contextOwnerTTL = 7 * 24 * time.Hour
)

// ContextBuilder builds a VideoContext from a media source (videocontext.Service).
type ContextBuilder interface {
	Build(ctx context.Context, videoPath, userGoal string, counters videocontext.Counters) (model.VideoContext, error)
	DeleteEvidenceFrames(ctx context.Context, vc *model.VideoContext)
}

// Analyzer runs one analysis task: reuse a finished result if possible, otherwise make sure the
// video context exists (building it at most once per content hash), then hand over to the agent.
type Analyzer struct {
	MediaRepo *repo.Media
	Media     *media.Service
	CP        *checkpoint.Store
	Hub       *taskevents.Hub
	Rdb       redis.Cmdable
	Locker    *redislock.Locker
	Builder   ContextBuilder
	Agent     *agentclient.Client
}

// AsyncAnalyze runs one analysis: result checkpoint -> video context -> agent Run -> persist.
func (a *Analyzer) AsyncAnalyze(ctx context.Context, mediaID int64, userGoal string, mode model.AnalysisMode) (err error) {
	mode = mode.Resolve()
	traceID := uuid.NewString()
	seed := videocontext.NewSeed(traceID)
	mf, ferr := a.MediaRepo.FindByID(ctx, mediaID)
	if ferr != nil {
		return ferr
	}
	if mf == nil {
		return common.InvalidArgument("media does not exist: " + strconv.FormatInt(mediaID, 10))
	}
	currentStage := model.StageVideoContext
	defer func() {
		if err == nil {
			return
		}
		if cerr := a.CP.SaveFailure(context.WithoutCancel(ctx), mediaID, userGoal, mode, currentStage, err); cerr != nil {
			slog.Error("agent_failure_checkpoint_write_failed", "traceId", traceID, "mediaId", mediaID, "err", cerr)
		}
		slog.Error("agent_analysis_failed", "traceId", traceID, "mediaId", mediaID, "err", err)
		if be, ok := err.(*common.Error); ok && be.Kind == common.KindBudgetExhausted {
			return // BudgetExceeded is re-thrown unchanged
		}
		err = common.Internal("AI analysis failed", err)
	}()

	// existing result checkpoint (agent-py owns the format)
	has, err := a.CP.HasResult(ctx, mediaID, userGoal, mode)
	if err != nil {
		return err
	}
	if has {
		res, err := a.Agent.Result(ctx, mediaID, userGoal, mode)
		if err != nil {
			return err
		}
		if res.Found {
			if err := a.persistResult(ctx, mf, deref(res.Markdown), nil); err != nil {
				return err
			}
			seed.Increment("checkpointHits", 1)
			return nil
		}
	}

	vc, err := a.resolveContext(ctx, mf, userGoal, seed, mode)
	if err != nil {
		return err
	}
	transcript := vc.TranscriptText()
	currentStage = model.StageAgentLoop
	if perr := a.Hub.PublishAnalysis(ctx, mediaID, userGoal, mode,
		model.StatusOf(model.StateProcessing, "多模态上下文已就绪，Agent 开始分析"), model.StageAgentLoop); perr != nil {
		return perr
	}
	// the model spend of this run counts against the owner of the video
	run, err := a.Agent.Run(billing.WithUser(ctx, mf.UserID), agentclient.RunRequest{
		MediaID: mediaID, Goal: userGoal, Mode: mode, TelemetrySeed: seed.Snapshot(),
	})
	if err != nil {
		return err
	}
	if err := a.persistResult(ctx, mf, run.Markdown, &transcript); err != nil {
		return err
	}
	slog.Info("agent_analysis_completed", "traceId", traceID, "mediaId", mediaID, "rounds", run.Round)
	return nil
}

func (a *Analyzer) resolveContext(ctx context.Context, mf *model.MediaFile, userGoal string,
	seed *videocontext.Seed, mode model.AnalysisMode) (model.VideoContext, error) {
	if cp, err := a.CP.LoadContext(ctx, mf.ID); err != nil {
		return model.VideoContext{}, err
	} else if cp != nil {
		seed.Increment("contextCheckpointHits", 1)
		return model.NewVideoContext(cp.Source, userGoal, cp.Segments)
	}
	hash, err := a.Media.ContentHash(ctx, mf.ID)
	if err != nil {
		return model.VideoContext{}, err
	}
	contentHash := taskkeys.NormalizeContentHash(mf.ID, hash)
	if reused, err := a.reuseContentContext(ctx, mf, userGoal, seed, contentHash); err != nil || reused != nil {
		if err != nil {
			return model.VideoContext{}, err
		}
		return *reused, nil
	}
	// Only one consumer per video runs ASR/OCR; the others wait and then reuse.
	lock, locked, err := a.Locker.TryLock(ctx, taskkeys.ContextLock(contentHash), contextLockWait)
	if err != nil {
		return model.VideoContext{}, common.Internal("等待视频上下文构建锁被中断", err)
	}
	defer func() {
		if locked && lock.IsHeld() {
			if uerr := lock.Unlock(context.WithoutCancel(ctx)); uerr != nil {
				slog.Warn("context_lock_unlock_failed", "contentHash", contentHash, "err", uerr)
			}
		}
	}()
	// Own checkpoint first: a concurrent goal for the same media may have just registered itself as owner.
	if own, err := a.CP.LoadContext(ctx, mf.ID); err != nil {
		return model.VideoContext{}, err
	} else if own != nil {
		seed.Increment("contextCheckpointHits", 1)
		return model.NewVideoContext(own.Source, userGoal, own.Segments)
	}
	if reused, err := a.reuseContentContext(ctx, mf, userGoal, seed, contentHash); err != nil {
		return model.VideoContext{}, err
	} else if reused != nil {
		return *reused, nil
	}
	if !locked {
		seed.Increment("contextLockContentions", 1)
		slog.Warn("context_build_in_progress", "mediaId", mf.ID, "contentHash", contentHash,
			"waitedSeconds", int(contextLockWait.Seconds()))
		return model.VideoContext{}, common.Internal("同一视频的上下文正在构建中，稍后重试", nil)
	}
	return a.buildContext(ctx, mf, userGoal, seed, contentHash, mode, lock)
}

func (a *Analyzer) reuseContentContext(ctx context.Context, mf *model.MediaFile, userGoal string,
	seed *videocontext.Seed, contentHash string) (*model.VideoContext, error) {
	owner := a.contextOwner(ctx, contentHash)
	if owner == nil || *owner == mf.ID {
		return nil, nil
	}
	ownerCtx, err := a.CP.LoadContext(ctx, *owner)
	if err != nil {
		return nil, err
	}
	if ownerCtx == nil {
		// stale owner index: drop it and build normally
		if err := a.Rdb.Del(ctx, taskkeys.ContextOwner(contentHash)).Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	localized, err := ReusableContext(mf.FilePath, *ownerCtx)
	if err != nil {
		return nil, err
	}
	if err := a.CP.SaveContext(ctx, mf.ID, localized); err != nil {
		return nil, err
	}
	seed.Increment("contextContentReuses", 1)
	slog.Info("video_context_reused", "mediaId", mf.ID, "sourceMediaId", *owner, "contentHash", contentHash)
	out, err := model.NewVideoContext(localized.Source, userGoal, localized.Segments)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *Analyzer) buildContext(ctx context.Context, mf *model.MediaFile, userGoal string, seed *videocontext.Seed,
	contentHash string, mode model.AnalysisMode, lock *redislock.Lock) (model.VideoContext, error) {
	// losing the lease means somebody else may be building too: abort our build
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-lock.Lost():
			cancel()
		case <-bctx.Done():
		}
	}()
	if err := a.Hub.PublishAnalysis(ctx, mf.ID, userGoal, mode,
		model.StatusOf(model.StateProcessing, "正在并行提取语音与关键帧"), model.StageVideoContext); err != nil {
		return model.VideoContext{}, err
	}
	started := time.Now()
	bctx, span := obs.Tracer().Start(bctx, "video_context.build",
		trace.WithAttributes(attribute.Int64("media.id", mf.ID), attribute.String("content.hash", contentHash)))
	vc, err := a.Builder.Build(bctx, mf.FilePath, userGoal, seed)
	obs.VideoContextBuild.Observe(time.Since(started).Seconds())
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "build failed")
	}
	span.End()
	if err != nil {
		seed.Stage(string(model.StageVideoContext), time.Since(started).Milliseconds(), false)
		return model.VideoContext{}, err
	}
	if err := a.CP.SaveContext(ctx, mf.ID, vc); err != nil {
		a.Builder.DeleteEvidenceFrames(ctx, &vc)
		seed.Stage(string(model.StageVideoContext), time.Since(started).Milliseconds(), false)
		return model.VideoContext{}, err
	}
	// persist first, then register ownership: a registered owner is always readable
	a.rememberContextOwner(ctx, contentHash, mf.ID)
	seed.Stage(string(model.StageVideoContext), time.Since(started).Milliseconds(), true)
	return vc, nil
}

func (a *Analyzer) contextOwner(ctx context.Context, contentHash string) *int64 {
	v, err := a.Rdb.Get(ctx, taskkeys.ContextOwner(contentHash)).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		// reuse is only an optimisation: Redis trouble falls back to a normal build
		slog.Warn("context_owner_read_failed", "contentHash", contentHash, "err", err)
		return nil
	}
	id, perr := strconv.ParseInt(v, 10, 64)
	if perr != nil {
		a.Rdb.Del(ctx, taskkeys.ContextOwner(contentHash))
		return nil
	}
	return &id
}

func (a *Analyzer) rememberContextOwner(ctx context.Context, contentHash string, mediaID int64) {
	if err := a.Rdb.Set(ctx, taskkeys.ContextOwner(contentHash), strconv.FormatInt(mediaID, 10), contextOwnerTTL).Err(); err != nil {
		slog.Warn("context_owner_write_failed", "contentHash", contentHash, "mediaId", mediaID, "err", err)
	}
}

// ReusableContext rewrites a source context for another media file: source = target,
// userGoal cleared, evidence frames replaced by a single "<target>#timestampMs=<start>" reference.
func ReusableContext(targetSource string, src model.VideoContext) (model.VideoContext, error) {
	segs := make([]model.VideoSegment, 0, len(src.Segments))
	for _, s := range src.Segments {
		frames := []string{}
		if len(s.EvidenceFrames) > 0 {
			frames = []string{targetSource + "#timestampMs=" + strconv.FormatInt(s.StartMs, 10)}
		}
		ns, err := model.NewVideoSegment(s.StartMs, s.EndMs, s.Transcript, s.OcrTexts, frames)
		if err != nil {
			return model.VideoContext{}, err
		}
		segs = append(segs, ns)
	}
	return model.NewVideoContext(targetSource, "", segs)
}

func (a *Analyzer) persistResult(ctx context.Context, mf *model.MediaFile, markdown string, transcript *string) error {
	if err := a.MediaRepo.UpdateAnalysis(ctx, mf.ID, markdown, transcript); err != nil {
		return err
	}
	a.Media.InvalidateUserList(ctx, mf.UserID)
	return nil
}

// ReuseResult attaches another media's finished analysis of the same content to mediaID.
func (a *Analyzer) ReuseResult(ctx context.Context, mediaID, sourceMediaID int64, goal string, mode model.AnalysisMode) (*agentclient.ReuseResponse, error) {
	mf, err := a.MediaRepo.FindByID(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	if mf == nil {
		return nil, common.InvalidArgument("media does not exist: " + strconv.FormatInt(mediaID, 10))
	}
	res, err := a.Agent.Reuse(ctx, mediaID, sourceMediaID, goal, mode, mf.FilePath)
	if err != nil || !res.Reused {
		return res, err
	}
	if err := a.persistResult(ctx, mf, deref(res.Markdown), nil); err != nil {
		return nil, err
	}
	return res, nil
}
