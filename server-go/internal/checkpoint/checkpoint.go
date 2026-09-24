// Package checkpoint is the Go side of the shared agent checkpoint store
// (contract §3.3): media context, stage and failure markers. Everything else
// (plan/criticState/result/chunks/revision/feedback) is owned by agent-py.
package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/taskkeys"
)

const cacheTTL = 7 * 24 * time.Hour

// Store reads/writes checkpoints: MySQL is authoritative, Redis a hot cache.
type Store struct {
	rdb  redis.Cmdable
	repo *repo.Checkpoints
}

func New(rdb redis.Cmdable, r *repo.Checkpoints) *Store { return &Store{rdb: rdb, repo: r} }

func mediaKey(mediaID int64) string     { return "agent:checkpoint:" + itoa(mediaID) }
func goalIndexKey(mediaID int64) string { return mediaKey(mediaID) + ":goals" }

// GoalKey is the goal-level Redis hash key.
func GoalKey(mediaID int64, goal string, mode model.AnalysisMode) (string, error) {
	d, err := taskkeys.GoalDigest(goal, mode)
	if err != nil {
		return "", err
	}
	return mediaKey(mediaID) + ":goal:" + d, nil
}

func goalCheckpoint(goal string, mode model.AnalysisMode, field string) (string, error) {
	d, err := taskkeys.GoalDigest(goal, mode)
	if err != nil {
		return "", err
	}
	return "goal:" + d + ":" + field, nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// LoadContext returns the stored VideoContext or nil.
func (s *Store) LoadContext(ctx context.Context, mediaID int64) (*model.VideoContext, error) {
	redisKey := mediaKey(mediaID)
	var payload string
	if cached, err := s.rdb.HGet(ctx, redisKey, "context").Result(); err == nil {
		payload = cached
	} else if !errors.Is(err, redis.Nil) {
		slog.Warn("agent_checkpoint_cache_read_failed", "key", redisKey, "field", "context", "err", err)
		s.rdb.HDel(ctx, redisKey, "context")
	}
	if payload != "" {
		if vc, err := decodeContext(payload); err == nil {
			return vc, nil
		} else {
			slog.Warn("agent_checkpoint_cache_read_failed", "key", redisKey, "field", "context", "err", err)
			s.rdb.HDel(ctx, redisKey, "context")
		}
	}
	p, err := s.repo.FindPayload(ctx, mediaID, "media:context")
	if err != nil {
		return nil, common.Internal("读取 Agent Checkpoint 失败: media:context", err)
	}
	if p == nil {
		return nil, nil
	}
	vc, err := decodeContext(*p)
	if err != nil {
		return nil, common.Internal("读取 Agent Checkpoint 失败: media:context", err)
	}
	stage, _ := s.repo.FindStage(ctx, mediaID, "media:context")
	stageVal := ""
	if stage != nil {
		stageVal = *stage
	}
	s.cacheField(ctx, redisKey, "context", *p, stageVal)
	return vc, nil
}

func decodeContext(payload string) (*model.VideoContext, error) {
	var vc model.VideoContext
	if err := json.Unmarshal([]byte(payload), &vc); err != nil {
		return nil, err
	}
	n, err := vc.Normalize()
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// SaveContext stores context (userGoal cleared) plus stage CONTEXT_COMPLETED.
func (s *Store) SaveContext(ctx context.Context, mediaID int64, vc model.VideoContext) error {
	reusable := model.VideoContext{Source: vc.Source, UserGoal: "", Segments: vc.Segments}
	if reusable.Segments == nil {
		reusable.Segments = []model.VideoSegment{}
	}
	b, err := common.MarshalNoEscape(reusable)
	if err != nil {
		return common.Internal("保存 Agent Checkpoint 失败", err)
	}
	payload := string(b)
	stage := string(model.StageContextCompleted)
	if err := s.repo.UpsertPair(ctx, mediaID, "media:context", "media:stage", stage, payload); err != nil {
		return common.Internal("保存 Agent Checkpoint 失败", err)
	}
	s.cacheField(ctx, mediaKey(mediaID), "context", payload, stage)
	return nil
}

// LoadStage returns the goal-level stage ("" when unknown).
func (s *Store) LoadStage(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (model.TaskStage, error) {
	redisKey, err := GoalKey(mediaID, goal, mode)
	if err != nil {
		return "", err
	}
	name, err := goalCheckpoint(goal, mode, "stage")
	if err != nil {
		return "", err
	}
	if cached, err := s.rdb.HGet(ctx, redisKey, "stage").Result(); err == nil {
		if st := model.TaskStageFrom(cached); st != "" {
			return st, nil
		}
		s.rdb.HDel(ctx, redisKey, "stage")
	} else if !errors.Is(err, redis.Nil) {
		slog.Warn("agent_checkpoint_stage_cache_read_failed", "mediaId", mediaID, "err", err)
	}
	persisted, err := s.repo.FindStage(ctx, mediaID, name)
	if err != nil {
		return "", common.Internal("读取 Agent Checkpoint 失败: "+name, err)
	}
	if persisted == nil {
		return "", nil
	}
	s.cacheStage(ctx, redisKey, *persisted)
	return model.TaskStageFrom(*persisted), nil
}

// SaveStage upserts the stage row, caches it and indexes the goal key.
func (s *Store) SaveStage(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode, stage model.TaskStage) error {
	key, err := GoalKey(mediaID, goal, mode)
	if err != nil {
		return err
	}
	name, err := goalCheckpoint(goal, mode, "stage")
	if err != nil {
		return err
	}
	if err := s.repo.Upsert(ctx, mediaID, name, string(stage), nil); err != nil {
		return common.Internal("保存 Agent Checkpoint 失败", err)
	}
	s.cacheStage(ctx, key, string(stage))
	s.rememberGoalKey(ctx, mediaID, key)
	return nil
}

// SaveFailure marks stage FAILED and records failedStage/errorType on the goal hash.
func (s *Store) SaveFailure(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode,
	failedStage model.TaskStage, cause error) error {
	key, err := GoalKey(mediaID, goal, mode)
	if err != nil {
		return err
	}
	name, err := goalCheckpoint(goal, mode, "stage")
	if err != nil {
		return err
	}
	if err := s.repo.Upsert(ctx, mediaID, name, string(model.StageFailed), nil); err != nil {
		return common.Internal("保存 Agent Checkpoint 失败", err)
	}
	s.cacheStage(ctx, key, string(model.StageFailed))
	if err := s.rdb.HSet(ctx, key, "failedStage", string(failedStage), "errorType", common.CodeOf(cause)).Err(); err != nil {
		return err
	}
	if err := s.rdb.Expire(ctx, key, cacheTTL).Err(); err != nil {
		return err
	}
	s.rememberGoalKey(ctx, mediaID, key)
	return nil
}

// HasResult reports whether a goal-level result checkpoint exists, without decoding it
// (agent-py owns the format; Go only needs to know whether asking it is worthwhile).
func (s *Store) HasResult(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (bool, error) {
	key, err := GoalKey(mediaID, goal, mode)
	if err != nil {
		return false, err
	}
	name, err := goalCheckpoint(goal, mode, "result")
	if err != nil {
		return false, err
	}
	if n, err := s.rdb.HExists(ctx, key, "result").Result(); err == nil && n {
		return true, nil
	} else if err != nil {
		slog.Warn("agent_checkpoint_cache_read_failed", "key", key, "field", "result", "err", err)
	}
	p, err := s.repo.FindPayload(ctx, mediaID, name)
	if err != nil {
		return false, common.Internal("读取 Agent Checkpoint 失败: "+name, err)
	}
	return p != nil, nil
}

func (s *Store) cacheField(ctx context.Context, key, field, payload, stage string) {
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, key, field, payload)
	if stage != "" {
		pipe.HSet(ctx, key, "stage", stage)
	}
	pipe.Expire(ctx, key, cacheTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("agent_checkpoint_cache_write_failed", "key", key, "field", field, "err", err)
		s.rdb.HDel(ctx, key, field, "stage")
	}
}

func (s *Store) cacheStage(ctx context.Context, key, stage string) {
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, key, "stage", stage)
	pipe.Expire(ctx, key, cacheTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("agent_checkpoint_stage_cache_write_failed", "key", key, "err", err)
		s.rdb.HDel(ctx, key, "stage")
	}
}

func (s *Store) rememberGoalKey(ctx context.Context, mediaID int64, key string) {
	idx := goalIndexKey(mediaID)
	pipe := s.rdb.Pipeline()
	pipe.SAdd(ctx, idx, key)
	pipe.Expire(ctx, idx, cacheTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		slog.Warn("agent_checkpoint_index_write_failed", "mediaId", mediaID, "key", key, "err", err)
	}
}
