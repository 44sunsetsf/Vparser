package repo

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"dovideo/server/internal/model"
)

// Users is the users table.
type Users struct{ db *gorm.DB }

func NewUsers(db *gorm.DB) *Users { return &Users{db: db} }

func (r *Users) FindByUsername(ctx context.Context, username string) (*model.User, error) {
	var u model.User
	err := r.db.WithContext(ctx).Where("username = ?", username).Limit(1).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &u, err
}

func (r *Users) FindByID(ctx context.Context, id int64) (*model.User, error) {
	var u model.User
	err := r.db.WithContext(ctx).Take(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &u, err
}

func (r *Users) Count(ctx context.Context, username string) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.User{}).Where("username = ?", username).Count(&n).Error
	return n, err
}

// Create inserts a user; ErrDuplicate is returned for unique-key violations.
func (r *Users) Create(ctx context.Context, u *model.User) error {
	err := r.db.WithContext(ctx).Create(u).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrDuplicate
	}
	return err
}

func (r *Users) UpdatePassword(ctx context.Context, id int64, hashed string) error {
	return r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Update("password", hashed).Error
}

// ErrDuplicate reports a unique-key violation.
var ErrDuplicate = errors.New("duplicate key")

// Media is the media_files table.
type Media struct{ db *gorm.DB }

func NewMedia(db *gorm.DB) *Media { return &Media{db: db} }

func (r *Media) FindByID(ctx context.Context, id int64) (*model.MediaFile, error) {
	var m model.MediaFile
	err := r.db.WithContext(ctx).Take(&m, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &m, err
}

func (r *Media) Insert(ctx context.Context, m *model.MediaFile) error {
	return r.db.WithContext(ctx).Create(m).Error
}

// ListByUser selects the list columns only, newest id first.
func (r *Media) ListByUser(ctx context.Context, userID int64) ([]model.MediaFile, error) {
	var out []model.MediaFile
	err := r.db.WithContext(ctx).
		Select("id", "filename", "status", "cover_url", "upload_time").
		Where("user_id = ?", userID).Order("id DESC").Find(&out).Error
	return out, err
}

func (r *Media) DeleteByID(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Delete(&model.MediaFile{}, id).Error
}

// UpdateAnalysis writes ai_summary and, when non-nil, transcript_text (map => zero values kept).
func (r *Media) UpdateAnalysis(ctx context.Context, id int64, aiSummary string, transcript *string) error {
	upd := map[string]any{"ai_summary": aiSummary}
	if transcript != nil {
		upd["transcript_text"] = *transcript
	}
	return r.db.WithContext(ctx).Model(&model.MediaFile{}).Where("id = ?", id).Updates(upd).Error
}

// FindByContentHash returns up to 5 stored copies of a content hash (served by idx_media_content_hash).
func (r *Media) FindByContentHash(ctx context.Context, contentHash string) ([]model.MediaFile, error) {
	var out []model.MediaFile
	err := r.db.WithContext(ctx).Select("id", "file_path", "content_hash").
		Where("content_hash = ?", contentHash).Order("id DESC").Limit(5).Find(&out).Error
	return out, err
}

// CountFileReferences counts rows pointing at filePath. Rows sharing an object always share the
// content hash (instant upload copies both), so the lookup stays on idx_media_content_hash instead
// of scanning the unindexed file_path column.
func (r *Media) CountFileReferences(ctx context.Context, contentHash *string, filePath string) (int64, error) {
	q := r.db.WithContext(ctx).Model(&model.MediaFile{})
	if contentHash != nil && *contentHash != "" {
		q = q.Where("content_hash = ?", *contentHash)
	}
	var n int64
	err := q.Where("file_path = ?", filePath).Count(&n).Error
	return n, err
}

func (r *Media) UpdateTranscript(ctx context.Context, id int64, transcript string) error {
	return r.db.WithContext(ctx).Model(&model.MediaFile{}).Where("id = ?", id).
		Update("transcript_text", transcript).Error
}

// FailedTasks is the failed_analysis_tasks ledger.
type FailedTasks struct{ db *gorm.DB }

func NewFailedTasks(db *gorm.DB) *FailedTasks { return &FailedTasks{db: db} }

func (r *FailedTasks) Insert(ctx context.Context, t *model.FailedAnalysisTask) error {
	return r.db.WithContext(ctx).Create(t).Error
}

func (r *FailedTasks) Latest(ctx context.Context) ([]model.FailedAnalysisTask, error) {
	var out []model.FailedAnalysisTask
	err := r.db.WithContext(ctx).Order("id DESC").Limit(100).Find(&out).Error
	return out, err
}

func (r *FailedTasks) FindByID(ctx context.Context, id int64) (*model.FailedAnalysisTask, error) {
	var t model.FailedAnalysisTask
	err := r.db.WithContext(ctx).Take(&t, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &t, err
}

// MarkRequeued updates status/updated_at and returns the affected row count.
func (r *FailedTasks) MarkRequeued(ctx context.Context, id int64) (int64, error) {
	res := r.db.WithContext(ctx).Model(&model.FailedAnalysisTask{}).Where("id = ?", id).
		Updates(map[string]any{"status": "REQUEUED", "updated_at": model.LocalNow()})
	return res.RowsAffected, res.Error
}

// Checkpoints covers the agent_checkpoints rows the gateway owns (context, stage, failure).
type Checkpoints struct{ db *gorm.DB }

func NewCheckpoints(db *gorm.DB) *Checkpoints { return &Checkpoints{db: db} }

func (r *Checkpoints) FindPayload(ctx context.Context, mediaID int64, key string) (*string, error) {
	var rows []model.AgentCheckpoint
	err := r.db.WithContext(ctx).Select("payload").
		Where("media_id = ? AND checkpoint_key = ?", mediaID, key).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0].Payload, nil
}

func (r *Checkpoints) FindStage(ctx context.Context, mediaID int64, key string) (*string, error) {
	var rows []model.AgentCheckpoint
	err := r.db.WithContext(ctx).Select("stage").
		Where("media_id = ? AND checkpoint_key = ?", mediaID, key).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	s := rows[0].Stage
	return &s, nil
}

// upsertClause is ON DUPLICATE KEY UPDATE stage=VALUES(stage), payload=VALUES(payload), updated_at=CURRENT_TIMESTAMP(3).
func upsertClause() clause.OnConflict {
	return clause.OnConflict{
		Columns: []clause.Column{{Name: "media_id"}, {Name: "checkpoint_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"stage":      gorm.Expr("VALUES(stage)"),
			"payload":    gorm.Expr("VALUES(payload)"),
			"updated_at": gorm.Expr("CURRENT_TIMESTAMP(3)"),
		}),
	}
}

func upsert(tx *gorm.DB, mediaID int64, key, stage string, payload *string) error {
	row := model.AgentCheckpoint{MediaID: mediaID, CheckpointKey: key, Stage: stage, Payload: payload}
	return tx.Omit("updated_at").Clauses(upsertClause()).Create(&row).Error
}

func (r *Checkpoints) Upsert(ctx context.Context, mediaID int64, key, stage string, payload *string) error {
	return upsert(r.db.WithContext(ctx), mediaID, key, stage, payload)
}

// UpsertPair writes the payload row and the stage row in one transaction.
func (r *Checkpoints) UpsertPair(ctx context.Context, mediaID int64, key, stageKey, stage, payload string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := upsert(tx, mediaID, key, stage, &payload); err != nil {
			return err
		}
		return upsert(tx, mediaID, stageKey, stage, nil)
	})
}
