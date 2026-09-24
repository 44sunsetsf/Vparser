package analysis

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/taskevents"
	"dovideo/server/internal/taskkeys"
)

const unknownMediaID = int64(-1)

var (
	bearerSecret   = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`)
	namedSecret    = regexp.MustCompile(`(?i)((?:api[-_ ]?key|token|secret)\s*[=:]\s*)[^\s,;]{8,}`)
	prefixedSecret = regexp.MustCompile(`(?i)sk-[A-Za-z0-9_-]{16,}`)
)

// FailedTasks is the failed-task ledger: dead-lettered tasks are recorded here for triage and
// can be replayed by an administrator.
type FailedTasks struct {
	Repo   *repo.FailedTasks
	Sender Sender
	Rdb    redis.Cmdable
	Hub    *taskevents.Hub
}

// Record writes a ledger row, padding missing fields and truncating to column widths. errorType is
// the reason code of the failure (INVALID_ARGUMENT, TRANSIENT, ...), the message is secret-masked.
func (f *FailedTasks) Record(ctx context.Context, msg *model.AnalysisTaskMsg, attempts int, cause error) error {
	root := common.RootCause(cause)
	t := &model.FailedAnalysisTask{
		MediaID:      unknownMediaID,
		Action:       column(deref(msg.Action), "UNKNOWN", 32),
		Mode:         string(model.ModeFromNullable(deref(msg.Mode))),
		ContentHash:  column(deref(msg.ContentHash), "unknown", 128),
		UserGoal:     column(deref(msg.UserGoal), "(消息缺少分析目标)", 500),
		AttemptCount: attempts,
		ErrorType:    column(common.CodeOf(cause), "UNKNOWN", 128),
		Status:       "FAILED",
		CreatedAt:    model.LocalNow(),
		UpdatedAt:    model.LocalNow(),
	}
	if msg.MediaID != nil {
		t.MediaID = *msg.MediaID
	}
	if root != nil {
		t.ErrorMessage = SanitizeError(root.Error())
	}
	return f.Repo.Insert(ctx, t)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func column(value, fallback string, maxLen int) string {
	text := value
	if strings.TrimSpace(text) == "" {
		text = fallback
	}
	return truncateUTF16(text, maxLen)
}

func truncateUTF16(s string, maxLen int) string {
	if len(utf16.Encode([]rune(s))) <= maxLen {
		return s
	}
	n := 0
	var b strings.Builder
	for _, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > maxLen {
			break
		}
		n += w
		b.WriteRune(r)
	}
	return b.String()
}

// SanitizeError masks bearer tokens, named secrets and sk- keys, then truncates to 1000 chars.
func SanitizeError(value string) *string {
	s := bearerSecret.ReplaceAllString(value, "${1}****")
	s = namedSecret.ReplaceAllString(s, "${1}****")
	s = prefixedSecret.ReplaceAllString(s, "****")
	s = truncateUTF16(s, 1000)
	return &s
}

// Latest returns the newest 100 ledger rows.
func (f *FailedTasks) Latest(ctx context.Context) ([]model.FailedAnalysisTask, error) {
	return f.Repo.Latest(ctx)
}

func isPlaceholderRecord(t *model.FailedAnalysisTask) bool {
	return t.MediaID == unknownMediaID || !model.IsSupportedAction(t.Action)
}

// Replay re-publishes a FAILED ledger row to the main topic as a fresh first attempt.
func (f *FailedTasks) Replay(ctx context.Context, id int64) error {
	task, err := f.Repo.FindByID(ctx, id)
	if err != nil {
		return err
	}
	if task == nil {
		return common.NotFound("失败任务不存在")
	}
	if task.Status != "FAILED" {
		return common.InvalidArgument("该失败任务已经重放")
	}
	// Poison-message rows carry placeholders; replaying them would be rejected again forever.
	if isPlaceholderRecord(task) {
		return common.InvalidArgument("该记录来自非法任务消息，缺少可重放的原始参数")
	}
	mode := model.ModeFromNullable(task.Mode)
	contentHash := taskkeys.NormalizeContentHash(task.MediaID, task.ContentHash)
	digest, err := taskkeys.GoalDigest(task.UserGoal, mode)
	if err != nil {
		return err
	}
	activeKey := taskkeys.Active(contentHash, digest)
	ok, err := f.Rdb.SetNX(ctx, activeKey, strconv.FormatInt(task.MediaID, 10), activeTTL).Result()
	if err != nil {
		return err
	}
	if !ok {
		return common.InvalidArgument("相同任务正在处理中")
	}
	dispatched := false
	fail := func(cause error) error {
		if !dispatched {
			f.Rdb.Del(context.WithoutCancel(ctx), activeKey)
		} else {
			// message is out: keep the idempotency key so a bookkeeping failure cannot cause a duplicate replay
			slog.Error("failed_analysis_replay_bookkeeping_failed", "taskId", id, "mediaId", task.MediaID, "err", cause)
		}
		return cause
	}
	if err := f.Sender.PublishTask(ctx, model.NewTaskMsg(task.MediaID, task.Action, contentHash, task.UserGoal, mode)); err != nil {
		return fail(err)
	}
	dispatched = true
	n, err := f.Repo.MarkRequeued(ctx, id)
	if err != nil {
		return fail(err)
	}
	if n != 1 {
		return fail(common.Internal("失败任务重放台账更新失败", nil))
	}
	if err := f.Hub.PublishAnalysis(ctx, task.MediaID, task.UserGoal, mode,
		model.StatusOf(model.StateQueued, "失败任务已由管理员重新入队"), model.StageManualReplay); err != nil {
		slog.Warn("failed_analysis_replay_event_failed", "taskId", id, "mediaId", task.MediaID, "err", err)
	}
	return nil
}
