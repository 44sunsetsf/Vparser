package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// DateTime is a zone-less local timestamp: stored as DATETIME in DB_TIMEZONE, serialised as
// "2006-01-02T15:04:05.999999999" (the format the web client parses as local time).
type DateTime struct{ time.Time }

const localDateTimeLayout = "2006-01-02T15:04:05.999999999"

func (t *DateTime) Scan(v any) error {
	switch x := v.(type) {
	case time.Time:
		t.Time = x
	case nil:
		t.Time = time.Time{}
	default:
		return fmt.Errorf("cannot scan %T into DateTime", v)
	}
	return nil
}

func (t DateTime) Value() (driver.Value, error) { return t.Time, nil }

func (t DateTime) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(t.Time.Format(localDateTimeLayout))
}

func (t *DateTime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		t.Time = time.Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseInLocation(localDateTimeLayout, s, time.Local)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

// LocalNow is the current local time.
func LocalNow() DateTime { return DateTime{time.Now()} }

// User maps `users`.
type User struct {
	ID       int64   `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	Username string  `gorm:"column:username" json:"username"`
	Password string  `gorm:"column:password" json:"-"`
	Nickname string  `gorm:"column:nickname" json:"nickname"`
	Avatar   *string `gorm:"column:avatar" json:"avatar"`
	Role     string  `gorm:"column:role" json:"role"`
}

func (User) TableName() string { return "users" }

// MediaFile maps `media_files`.
type MediaFile struct {
	ID             int64    `gorm:"column:id;primaryKey;autoIncrement" json:"id"`
	UserID         int64    `gorm:"column:user_id" json:"userId"`
	Filename       string   `gorm:"column:filename" json:"filename"`
	Status         string   `gorm:"column:status" json:"status"`
	FilePath       string   `gorm:"column:file_path" json:"filePath"`
	ContentHash    *string  `gorm:"column:content_hash" json:"contentHash"`
	AISummary      *string  `gorm:"column:ai_summary" json:"aiSummary"`
	TranscriptText *string  `gorm:"column:transcript_text" json:"transcriptText"`
	CoverURL       *string  `gorm:"column:cover_url" json:"coverUrl"`
	UploadTime     DateTime `gorm:"column:upload_time" json:"uploadTime"`
}

func (MediaFile) TableName() string { return "media_files" }

// FailedAnalysisTask maps `failed_analysis_tasks`.
type FailedAnalysisTask struct {
	ID           int64    `gorm:"column:id;primaryKey;autoIncrement"`
	MediaID      int64    `gorm:"column:media_id"`
	Action       string   `gorm:"column:action"`
	Mode         string   `gorm:"column:mode"`
	ContentHash  string   `gorm:"column:content_hash"`
	UserGoal     string   `gorm:"column:user_goal"`
	AttemptCount int      `gorm:"column:attempt_count"`
	ErrorType    string   `gorm:"column:error_type"`
	ErrorMessage *string  `gorm:"column:error_message"`
	Status       string   `gorm:"column:status"`
	CreatedAt    DateTime `gorm:"column:created_at"`
	UpdatedAt    DateTime `gorm:"column:updated_at"`
}

func (FailedAnalysisTask) TableName() string { return "failed_analysis_tasks" }

// AgentCheckpoint maps `agent_checkpoints` (only the Go-owned rows are touched).
type AgentCheckpoint struct {
	MediaID       int64    `gorm:"column:media_id;primaryKey"`
	CheckpointKey string   `gorm:"column:checkpoint_key;primaryKey"`
	Stage         string   `gorm:"column:stage"`
	Payload       *string  `gorm:"column:payload"`
	UpdatedAt     DateTime `gorm:"column:updated_at"`
}

func (AgentCheckpoint) TableName() string { return "agent_checkpoints" }
