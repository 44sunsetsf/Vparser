package repo

import (
	"strings"
	"testing"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"

	"dovideo/server/internal/model"
)

func dryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		DSN: "u:p@tcp(127.0.0.1:1)/db?parseTime=true", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true, TranslateError: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSchemaMapping(t *testing.T) {
	db := dryRunDB(t)
	tests := []struct {
		model   any
		table   string
		columns []string
	}{
		{&model.User{}, "users", []string{"id", "username", "password", "nickname", "avatar", "role"}},
		{&model.MediaFile{}, "media_files", []string{"id", "user_id", "filename", "status", "file_path", "content_hash", "ai_summary", "transcript_text", "cover_url", "upload_time"}},
		{&model.FailedAnalysisTask{}, "failed_analysis_tasks", []string{"id", "media_id", "action", "mode", "content_hash", "user_goal", "attempt_count", "error_type", "error_message", "status", "created_at", "updated_at"}},
		{&model.AgentCheckpoint{}, "agent_checkpoints", []string{"media_id", "checkpoint_key", "stage", "payload", "updated_at"}},
	}
	for _, tt := range tests {
		stmt := &gorm.Statement{DB: db}
		if err := stmt.Parse(tt.model); err != nil {
			t.Fatal(err)
		}
		if stmt.Schema.Table != tt.table {
			t.Errorf("table %s want %s", stmt.Schema.Table, tt.table)
		}
		got := map[string]bool{}
		for _, f := range stmt.Schema.Fields {
			got[f.DBName] = true
		}
		for _, c := range tt.columns {
			if !got[c] {
				t.Errorf("%s: column %s not mapped", tt.table, c)
			}
		}
		if len(got) != len(tt.columns) {
			t.Errorf("%s: %d columns mapped, want %d", tt.table, len(got), len(tt.columns))
		}
	}
}

func TestUpsertSQL(t *testing.T) {
	db := dryRunDB(t)
	payload := "{}"
	row := model.AgentCheckpoint{MediaID: 7, CheckpointKey: "media:context", Stage: "CONTEXT_COMPLETED", Payload: &payload}
	res := db.Omit("updated_at").Clauses(upsertClause()).Create(&row)
	sql := res.Statement.SQL.String()
	want := "INSERT INTO `agent_checkpoints` (`media_id`,`checkpoint_key`,`stage`,`payload`) VALUES (?,?,?,?) " +
		"ON DUPLICATE KEY UPDATE `payload`=VALUES(payload),`stage`=VALUES(stage),`updated_at`=CURRENT_TIMESTAMP(3)"
	if sql != want {
		t.Fatalf("got  %s\nwant %s", sql, want)
	}
}

func TestUpdateAnalysisKeepsZeroValues(t *testing.T) {
	db := dryRunDB(t)
	empty := ""
	upd := map[string]any{"ai_summary": "", "transcript_text": empty}
	res := db.Model(&model.MediaFile{}).Where("id = ?", 1).Updates(upd)
	sql := res.Statement.SQL.String()
	if !strings.Contains(sql, "`ai_summary`=?") || !strings.Contains(sql, "`transcript_text`=?") {
		t.Fatalf("map updates must keep empty strings: %s", sql)
	}
}
