package mq

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/redislock"
	"dovideo/server/internal/taskkeys"
)

func TestRouteFailure(t *testing.T) {
	tests := []struct {
		name      string
		permanent bool
		attempt   int
		want      Route
	}{
		{"first transient failure waits 10s", false, 1, Route{Topic: TopicRetry10s, Delay: 10 * time.Second}},
		{"missing attempt header counts as first", false, 0, Route{Topic: TopicRetry10s, Delay: 10 * time.Second}},
		{"second transient failure waits 60s", false, 2, Route{Topic: TopicRetry60s, Delay: 60 * time.Second}},
		{"third transient failure is dead-lettered", false, 3, Route{Topic: TopicDLQ, Reason: ReasonExhausted}},
		{"beyond budget is dead-lettered", false, 9, Route{Topic: TopicDLQ, Reason: ReasonExhausted}},
		{"permanent failure skips retries", true, 1, Route{Topic: TopicDLQ, Reason: ReasonPermanent}},
		{"permanent on a retry topic", true, 2, Route{Topic: TopicDLQ, Reason: ReasonPermanent}},
	}
	for _, tt := range tests {
		if got := RouteFailure(tt.permanent, tt.attempt); got != tt.want {
			t.Errorf("%s: got %+v want %+v", tt.name, got, tt.want)
		}
	}
}

func TestRejectionReason(t *testing.T) {
	s := func(v string) *string { return &v }
	i := func(v int64) *int64 { return &v }
	tests := []struct {
		name string
		msg  *model.AnalysisTaskMsg
		want string
	}{
		{"nil", nil, "消息体为空"},
		{"no media id", &model.AnalysisTaskMsg{Action: s("START_ANALYSIS"), UserGoal: s("g")}, "缺少 mediaId"},
		{"no goal", &model.AnalysisTaskMsg{MediaID: i(1), Action: s("START_ANALYSIS")}, "缺少分析目标"},
		{"blank goal", &model.AnalysisTaskMsg{MediaID: i(1), Action: s("START_ANALYSIS"), UserGoal: s("  ")}, "缺少分析目标"},
		{"bad action", &model.AnalysisTaskMsg{MediaID: i(1), Action: s("DELETE"), UserGoal: s("g")}, "不支持的 action=DELETE"},
		{"null action", &model.AnalysisTaskMsg{MediaID: i(1), UserGoal: s("g")}, "不支持的 action=null"},
		{"ok", &model.AnalysisTaskMsg{MediaID: i(1), Action: s("REVISE_ANALYSIS"), UserGoal: s("g")}, ""},
	}
	for _, tt := range tests {
		if got := RejectionReason(tt.msg); got != tt.want {
			t.Errorf("%s: %q want %q", tt.name, got, tt.want)
		}
	}
}

func TestDecodeRecord(t *testing.T) {
	notBefore := time.UnixMilli(1_700_000_000_123)
	rec := &kgo.Record{Topic: TopicRetry60s, Value: []byte(`{"mediaId":5,"action":"START_ANALYSIS","userGoal":"g"}`),
		Headers: taskHeaders(3, notBefore, "TRANSIENT: timeout")}
	d := DecodeRecord(rec)
	if d.Msg == nil || *d.Msg.MediaID != 5 || d.Attempt != 3 || !d.NotBefore.Equal(notBefore) || d.FirstError != "TRANSIENT: timeout" {
		t.Fatalf("decoded %+v", d)
	}
	bad := DecodeRecord(&kgo.Record{Value: []byte("{not json"), Headers: []kgo.RecordHeader{{Key: HeaderAttempt, Value: []byte("x")}}})
	if bad.Msg != nil || bad.DecodeErr == nil || bad.Attempt != 1 || !bad.NotBefore.IsZero() {
		t.Fatalf("malformed record must decode as poison, first attempt, due now: %+v", bad)
	}
	if got := truncateBytes("目录目录", 7); got != "目录" {
		t.Fatalf("truncate must keep valid UTF-8: %q", got)
	}
}

// ---- fakes ----

type fakes struct {
	mu          sync.Mutex
	analyzeErr  error
	analyzeCnt  int
	produced    []*kgo.Record
	produceErr  error
	recorded    []int // attempts
	recordErr   error
	errorTypes  []string
	stages      []model.TaskStage
	events      []model.TaskStage
	eventStatus []model.State
	resultFound bool
	beginOK     bool
	exists      bool
}

func (f *fakes) AsyncAnalyze(context.Context, int64, string, model.AnalysisMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.analyzeCnt++
	return f.analyzeErr
}
func (f *fakes) ReuseResult(context.Context, int64, int64, string, model.AnalysisMode) (*agentclient.ReuseResponse, error) {
	return &agentclient.ReuseResponse{Reused: false}, nil
}
func (f *fakes) Result(context.Context, int64, string, model.AnalysisMode) (*agentclient.ResultResponse, error) {
	if !f.resultFound {
		return &agentclient.ResultResponse{}, nil
	}
	st := model.StatusCompleted("md")
	return &agentclient.ResultResponse{Found: true, TaskStatus: &st}, nil
}
func (f *fakes) BeginRevision(context.Context, int64, string, model.AnalysisMode) (bool, error) {
	return f.beginOK, nil
}
func (f *fakes) CompleteRevision(context.Context, int64, string, model.AnalysisMode) error {
	return nil
}
func (f *fakes) SaveStage(_ context.Context, _ int64, _ string, _ model.AnalysisMode, s model.TaskStage) error {
	f.mu.Lock()
	f.stages = append(f.stages, s)
	f.mu.Unlock()
	return nil
}
func (f *fakes) Produce(_ context.Context, rec *kgo.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.produceErr != nil {
		return f.produceErr
	}
	f.produced = append(f.produced, rec)
	return nil
}
func (f *fakes) Record(_ context.Context, _ *model.AnalysisTaskMsg, attempts int, cause error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, attempts)
	f.errorTypes = append(f.errorTypes, common.CodeOf(cause))
	return f.recordErr
}
func (f *fakes) Exists(context.Context, int64) (bool, error)  { return f.exists, nil }
func (f *fakes) PurgeRuntimeArtifacts(context.Context, int64) {}
func (f *fakes) PublishAnalysis(_ context.Context, _ int64, _ string, _ model.AnalysisMode, st model.TaskStatus, stage model.TaskStage) error {
	f.mu.Lock()
	f.events = append(f.events, stage)
	f.eventStatus = append(f.eventStatus, st.State)
	f.mu.Unlock()
	return nil
}

func (f *fakes) topics() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.produced))
	for _, r := range f.produced {
		out = append(out, r.Topic)
	}
	return out
}

type env struct {
	f   *fakes
	c   *Consumer
	mr  *miniredis.Miniredis
	d   Delivery
	act string
	lck string
	now time.Time
}

func newEnv(t *testing.T, action string) *env {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	f := &fakes{exists: true, beginOK: true}
	now := time.Unix(1_700_000_000, 0)
	c := &Consumer{Rdb: rdb, Locker: redislock.New(rdb), Analyzer: f, Agent: f, Stages: f, Publisher: f, Ledger: f,
		Media: f, Events: f, Now: func() time.Time { return now }}
	msg := model.NewTaskMsg(5, action, "media-5", "goal", model.ModeGeneral)
	value, _ := json.Marshal(msg)
	rec := &kgo.Record{Topic: TopicAnalysis, Key: []byte("media-5"), Value: value, Headers: taskHeaders(1, time.Time{}, "")}
	digest, _ := taskkeys.GoalDigest("goal", model.ModeGeneral)
	return &env{f: f, c: c, mr: mr, d: DecodeRecord(rec), now: now,
		act: taskkeys.Active("media-5", digest), lck: taskkeys.Lock("media-5", digest)}
}

// forward replays the last produced record as the next delivery, like the retry topic would.
func (e *env) forward(t *testing.T) {
	t.Helper()
	last := e.f.produced[len(e.f.produced)-1]
	e.d = DecodeRecord(last)
}

func TestConsumerSuccessCleansKeys(t *testing.T) {
	e := newEnv(t, model.ActionStart)
	e.f.resultFound = true
	e.mr.Set(e.act, "5")
	out, err := e.c.Handle(context.Background(), e.d)
	if err != nil || out != OutcomeSuccess {
		t.Fatal(out, err)
	}
	if e.f.analyzeCnt != 1 || e.mr.Exists(e.act) || e.mr.Exists(e.lck) {
		t.Fatalf("cleanup: analyze=%d act=%v lock=%v", e.f.analyzeCnt, e.mr.Exists(e.act), e.mr.Exists(e.lck))
	}
	completed, _ := e.mr.Get(taskkeys.Completed("media-5", mustDigest("goal")))
	if completed != "5" {
		t.Fatalf("completed marker = %q", completed)
	}
	if last := e.f.events[len(e.f.events)-1]; last != model.StageCompleted {
		t.Fatalf("events %v", e.f.events)
	}
}

func mustDigest(goal string) string {
	d, _ := taskkeys.GoalDigest(goal, model.ModeGeneral)
	return d
}

func TestConsumerTransientFailureWalksRetryTopicsThenDeadLetters(t *testing.T) {
	e := newEnv(t, model.ActionStart)
	e.f.analyzeErr = common.Internal("AI analysis failed", common.Transient("agent unavailable", errors.New("conn refused")))
	e.mr.Set(e.act, "5")
	ctx := context.Background()

	wantHops := []struct {
		topic string
		delay time.Duration
	}{{TopicRetry10s, 10 * time.Second}, {TopicRetry60s, 60 * time.Second}}
	for i, hop := range wantHops {
		out, err := e.c.Handle(ctx, e.d)
		if err != nil || out != OutcomeRetry {
			t.Fatalf("attempt %d: %v %v", i+1, out, err)
		}
		e.forward(t)
		if e.d.Topic != hop.topic || e.d.Attempt != i+2 || !e.d.NotBefore.Equal(e.now.Add(hop.delay)) {
			t.Fatalf("attempt %d forwarded as %+v", i+1, e.d)
		}
		if e.d.FirstError != "TRANSIENT: AI analysis failed: agent unavailable: conn refused" || string(e.d.Key) != "media-5" {
			t.Fatalf("first error / key must travel with the task: %q %q", e.d.FirstError, e.d.Key)
		}
		if !e.mr.Exists(e.act) || e.mr.Exists(e.lck) {
			t.Fatalf("attempt %d: active must survive, lock must be released", i+1)
		}
		if len(e.f.recorded) != 0 {
			t.Fatal("no ledger row before the retry budget is used up")
		}
	}
	// third attempt: dead-lettered, ledger written with a readable code
	out, err := e.c.Handle(ctx, e.d)
	if err != nil || out != OutcomeDLQ {
		t.Fatalf("third attempt: %v %v", out, err)
	}
	e.forward(t)
	if e.d.Topic != TopicDLQ || e.d.Attempt != 3 || len(e.f.recorded) != 1 || e.f.recorded[0] != 3 || e.f.errorTypes[0] != "TRANSIENT" {
		t.Fatalf("dead letter: %+v recorded=%v types=%v", e.d, e.f.recorded, e.f.errorTypes)
	}
	if reason, _ := header(e.f.produced[2], HeaderDLQReason); reason != ReasonExhausted {
		t.Fatalf("dlq reason %q", reason)
	}
	if e.mr.Exists(e.act) {
		t.Fatal("dead-lettered task must release active")
	}
	if e.f.stages[len(e.f.stages)-1] != model.StageDeadLettered || e.f.events[len(e.f.events)-1] != model.StageDeadLettered {
		t.Fatalf("stages %v events %v", e.f.stages, e.f.events)
	}
}

func TestConsumerPermanentFailureDeadLettersImmediately(t *testing.T) {
	e := newEnv(t, model.ActionStart)
	e.f.analyzeErr = common.Internal("AI analysis failed", common.InvalidArgument("bad goal"))
	out, err := e.c.Handle(context.Background(), e.d)
	if err != nil || out != OutcomeDLQ {
		t.Fatal(out, err)
	}
	if got := e.f.topics(); len(got) != 1 || got[0] != TopicDLQ || e.f.recorded[0] != 1 || e.f.errorTypes[0] != "INVALID_ARGUMENT" {
		t.Fatalf("produced=%v recorded=%v types=%v", got, e.f.recorded, e.f.errorTypes)
	}
}

func TestConsumerForwardFailureIsNotCommitted(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		e := newEnv(t, model.ActionStart)
		e.f.analyzeErr = common.Transient("x", nil)
		if permanent {
			e.f.analyzeErr = common.InvalidArgument("bad")
		}
		e.f.produceErr = errors.New("broker down")
		e.mr.Set(e.act, "5")
		if _, err := e.c.Handle(context.Background(), e.d); err == nil {
			t.Fatalf("permanent=%v: must not report handled when the next topic is unreachable", permanent)
		}
		if !e.mr.Exists(e.act) {
			t.Fatal("active must be kept so the re-attempt can converge")
		}
	}
}

func TestConsumerBudgetExhaustedIsTerminal(t *testing.T) {
	e := newEnv(t, model.ActionStart)
	e.f.analyzeErr = common.BudgetExhausted("token budget", nil)
	e.mr.Set(e.act, "5")
	out, err := e.c.Handle(context.Background(), e.d)
	if err != nil || out != OutcomeBudget {
		t.Fatal(out, err)
	}
	if len(e.f.produced) != 0 || len(e.f.recorded) != 0 {
		t.Fatal("budget exhaustion is neither retried nor dead-lettered")
	}
	if e.f.stages[len(e.f.stages)-1] != model.StageBudgetExhausted ||
		e.f.eventStatus[len(e.f.eventStatus)-1] != model.StateFailed || e.mr.Exists(e.act) {
		t.Fatalf("stages=%v events=%v", e.f.stages, e.f.events)
	}
}

func TestConsumerSkipsWhenLockHeld(t *testing.T) {
	e := newEnv(t, model.ActionStart)
	e.mr.Set(e.lck, "other-worker")
	e.mr.Set(e.act, "5")
	out, err := e.c.Handle(context.Background(), e.d)
	if err != nil || out != OutcomeSkipped || e.f.analyzeCnt != 0 || !e.mr.Exists(e.act) {
		t.Fatal("a task whose lock is held must be skipped without touching keys")
	}
}

func TestConsumerDeletedMediaDiscarded(t *testing.T) {
	e := newEnv(t, model.ActionStart)
	e.f.exists = false
	if out, err := e.c.Handle(context.Background(), e.d); err != nil || out != OutcomeSkipped || e.f.analyzeCnt != 0 {
		t.Fatalf("out=%v err=%v analyze=%d", out, err, e.f.analyzeCnt)
	}
}

func TestConsumerRevisionRequiresStagedState(t *testing.T) {
	e := newEnv(t, model.ActionRevise)
	e.f.beginOK = false
	out, err := e.c.Handle(context.Background(), e.d)
	if err != nil || out != OutcomeRetry {
		t.Fatalf("missing staged revision must be retried: %v %v", out, err)
	}
	if e.f.analyzeCnt != 0 {
		t.Fatal("analysis must not run without a staged revision")
	}
}

func TestConsumerPoisonMessage(t *testing.T) {
	e := newEnv(t, "BOGUS")
	e.mr.Set(e.act, "5")
	out, err := e.c.Handle(context.Background(), e.d)
	if err != nil || out != OutcomePoison {
		t.Fatalf("poison is committed once recorded or dead-lettered: %v %v", out, err)
	}
	if got := e.f.topics(); len(got) != 1 || got[0] != TopicDLQ || len(e.f.recorded) != 1 || e.f.recorded[0] != 0 || e.f.analyzeCnt != 0 {
		t.Fatalf("produced=%v recorded=%v", got, e.f.recorded)
	}
	if e.mr.Exists(e.act) {
		t.Fatal("poison task state must be released")
	}

	tests := []struct {
		name               string
		recordErr, sendErr error
		wantHandled        bool
	}{
		{"ledger down, DLQ ok", errors.New("db"), nil, true},
		{"DLQ down, ledger ok", nil, errors.New("kafka"), true},
		{"both down: keep it", errors.New("db"), errors.New("kafka"), false},
	}
	for _, tt := range tests {
		e = newEnv(t, "BOGUS")
		e.f.recordErr, e.f.produceErr = tt.recordErr, tt.sendErr
		_, err := e.c.Handle(context.Background(), e.d)
		if (err == nil) != tt.wantHandled {
			t.Fatalf("%s: err=%v", tt.name, err)
		}
	}

	// undecodable payload: raw bytes go to the DLQ untouched
	e = newEnv(t, model.ActionStart)
	e.d = DecodeRecord(&kgo.Record{Topic: TopicAnalysis, Value: []byte("garbage")})
	if out, err := e.c.Handle(context.Background(), e.d); err != nil || out != OutcomePoison || string(e.f.produced[0].Value) != "garbage" {
		t.Fatalf("undecodable: %v %v", out, err)
	}
}

func TestTaskHeadersRoundTrip(t *testing.T) {
	hs := taskHeaders(2, time.UnixMilli(42), "")
	rec := &kgo.Record{Headers: hs}
	if v, _ := header(rec, HeaderAttempt); v != "2" {
		t.Fatal(v)
	}
	if v, _ := header(rec, HeaderNotBefore); v != strconv.Itoa(42) {
		t.Fatal(v)
	}
	if _, ok := header(rec, HeaderFirstError); ok {
		t.Fatal("empty first error is omitted")
	}
}
