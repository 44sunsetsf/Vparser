package agentclient

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"

	agentv1 "dovideo/server/gen/agent/v1"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
)

func TestMapError(t *testing.T) {
	tests := []struct {
		name      string
		code      codes.Code
		kind      common.Kind
		permanent bool
		reason    string
	}{
		{"invalid argument is permanent", codes.InvalidArgument, common.KindInvalidArgument, true, "INVALID_ARGUMENT"},
		{"failed precondition means context not built", codes.FailedPrecondition, common.KindNotReady, false, "NOT_READY"},
		{"resource exhausted without trailer is overload", codes.ResourceExhausted, common.KindTransient, false, "TRANSIENT"},
		{"unavailable is transient", codes.Unavailable, common.KindTransient, false, "TRANSIENT"},
		{"deadline is transient", codes.DeadlineExceeded, common.KindTransient, false, "TRANSIENT"},
		{"internal is transient", codes.Internal, common.KindTransient, false, "TRANSIENT"},
		{"bad token is retried after config fix", codes.Unauthenticated, common.KindTransient, false, "TRANSIENT"},
		{"unknown codes default to transient", codes.Unknown, common.KindTransient, false, "TRANSIENT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := MapError(status.Error(tt.code, "boom"))
			if !common.Has(err, tt.kind) || common.IsPermanentFailure(err) != tt.permanent || common.CodeOf(err) != tt.reason {
				t.Fatalf("got %#v (permanent=%v code=%s)", err, common.IsPermanentFailure(err), common.CodeOf(err))
			}
		})
	}
	budget := MapError(&agentError{error: status.Error(codes.ResourceExhausted, "tokens"), reason: "BUDGET_EXHAUSTED"})
	if !common.Has(budget, common.KindBudgetExhausted) || common.CodeOf(budget) != "BUDGET_EXHAUSTED" {
		t.Fatalf("budget trailer: %v", budget)
	}
	if MapError(nil) != nil {
		t.Fatal("nil stays nil")
	}
	// the NOT_READY mapping surfaces as HTTP 409
	if st, _ := common.ToHTTP(MapError(status.Error(codes.FailedPrecondition, "x"))); st != 409 {
		t.Fatalf("status %d", st)
	}
}

func TestTimeoutsFor(t *testing.T) {
	to := Timeouts{Run: 16 * time.Minute, Interactive: 3 * time.Minute}
	tests := []struct {
		method string
		want   time.Duration
	}{
		{agentv1.AgentService_Run_FullMethodName, 16 * time.Minute},
		{agentv1.AgentService_FollowUp_FullMethodName, 3 * time.Minute},
		{agentv1.AgentService_ClassifyMode_FullMethodName, 3 * time.Minute},
		{agentv1.AgentService_SearchEvidence_FullMethodName, 3 * time.Minute},
		{agentv1.AgentService_GetResult_FullMethodName, DefaultTimeout},
		{agentv1.AgentService_Purge_FullMethodName, DefaultTimeout},
	}
	for _, tt := range tests {
		if got := to.For(tt.method); got != tt.want {
			t.Errorf("%s: %v want %v", tt.method, got, tt.want)
		}
	}
}

func TestFeedbackJSON(t *testing.T) {
	rating, ts := int32(-1), int64(9_007_199_254_740_993) // beyond float64 precision: must stay exact
	full := FeedbackFromProto(&agentv1.AgentFeedback{MediaId: 7, Goal: "g", Mode: agentv1.AnalysisMode_REVIEW,
		Rating: &rating, EvidenceTimestamp: &ts, CreatedAt: "2026-01-01T00:00:00Z"})
	b, _ := common.MarshalNoEscape(full)
	want := `{"mediaId":7,"goal":"g","mode":"REVIEW","rating":-1,"errorType":null,"comment":null,"correctedGoal":null,` +
		`"correctedTasks":[],"evidenceTimestamp":9007199254740993,"evidenceAccepted":null,"createdAt":"2026-01-01T00:00:00Z"}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	empty, _ := common.MarshalNoEscape(FeedbackFromProto(&agentv1.AgentFeedback{MediaId: 1, Goal: "g"}))
	if want := `{"mediaId":1,"goal":"g","mode":"GENERAL","rating":null,"errorType":null,"comment":null,"correctedGoal":null,` +
		`"correctedTasks":[],"evidenceTimestamp":null,"evidenceAccepted":null,"createdAt":null}`; string(empty) != want {
		t.Fatalf("unset optionals must be null: %s", empty)
	}
	// round trip keeps unset optionals unset
	back := FeedbackToProto(FeedbackFromProto(&agentv1.AgentFeedback{MediaId: 1, Goal: "g", Rating: &rating}))
	if back.Comment != nil || back.EvidenceAccepted != nil || back.GetRating() != -1 || back.GetMode() != agentv1.AnalysisMode_GENERAL {
		t.Fatalf("round trip %+v", back)
	}
}

func TestMessageConversions(t *testing.T) {
	hits, _ := common.MarshalNoEscape(HitsFromProto([]*agentv1.VideoEvidenceHit{{StartMs: 522000, EndMs: 582000,
		Source: "ASR+OCR", Snippet: "s"}}))
	if string(hits) != `[{"startMs":522000,"endMs":582000,"source":"ASR+OCR","snippet":"s","transcript":"","ocrTexts":[]}]` {
		t.Fatalf("hits %s", hits)
	}
	if none, _ := common.MarshalNoEscape(HitsFromProto(nil)); string(none) != `[]` {
		t.Fatalf("no hits must be [] not null: %s", none)
	}
	plan, _ := common.MarshalNoEscape(PlanFromProto(&agentv1.AgentPlan{UnderstoodGoal: "u"}))
	if string(plan) != `{"understoodGoal":"u","tasks":[]}` {
		t.Fatalf("plan %s", plan)
	}
	st := TaskStatusFromProto(&agentv1.TaskStatus{State: "COMPLETED", Result: "# md"})
	if b, _ := common.MarshalNoEscape(st); string(b) != `{"state":"COMPLETED","result":"# md","message":null}` {
		t.Fatalf("status %s", b)
	}
	s, _ := structpb.NewStruct(map[string]any{"rounds": 2, "stages": []any{"plan"}})
	if b, _ := json.Marshal(StructToJSON(s)); string(b) != `{"rounds":2,"stages":["plan"]}` {
		t.Fatalf("struct %s", b)
	}
	if StructToJSON(&structpb.Struct{}) != nil {
		t.Fatal("empty struct means no document")
	}
	for _, m := range []model.AnalysisMode{model.ModeGeneral, model.ModeLearning, model.ModeReview, model.ModeCreation} {
		if ModeFromProto(ModeToProto(m)) != m {
			t.Fatalf("mode %s", m)
		}
	}
	if ModeToProto("") != agentv1.AnalysisMode_GENERAL || ModeFromProto(agentv1.AnalysisMode_ANALYSIS_MODE_UNSPECIFIED) != model.ModeGeneral {
		t.Fatal("unspecified mode is GENERAL")
	}
	seed := SeedToProto(&TelemetrySeed{TraceID: "t", Stages: map[string]StageSeed{"VIDEO_CONTEXT": {12, true}},
		Counters: map[string]int64{"ocrCalls": 3}})
	if seed.GetStages()["VIDEO_CONTEXT"].GetDurationMs() != 12 || seed.GetCounters()["ocrCalls"] != 3 || SeedToProto(nil) != nil {
		t.Fatalf("seed %+v", seed)
	}
}

// fakeAgent records what reached the server.
type fakeAgent struct {
	agentv1.UnimplementedAgentServiceServer
	resultCalls atomic.Int32
	token       atomic.Value
	deadline    atomic.Int64 // remaining ms seen by the server
}

func (f *fakeAgent) GetResult(ctx context.Context, _ *agentv1.GoalRef) (*agentv1.ResultResponse, error) {
	if f.resultCalls.Add(1) < 3 {
		return nil, status.Error(codes.Unavailable, "warming up")
	}
	return &agentv1.ResultResponse{Found: true, Markdown: "# hi",
		TaskStatus: &agentv1.TaskStatus{State: "COMPLETED", Result: "# hi", Message: "任务完成"}}, nil
}

func (f *fakeAgent) Run(ctx context.Context, req *agentv1.RunRequest) (*agentv1.RunResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.token.Store(md.Get(TokenHeader))
	if dl, ok := ctx.Deadline(); ok {
		f.deadline.Store(time.Until(dl).Milliseconds())
	}
	if req.GetTelemetrySeed().GetTraceId() != "trace-1" {
		return nil, status.Error(codes.InvalidArgument, "seed missing")
	}
	_ = grpc.SetTrailer(ctx, metadata.Pairs(AgentErrorTrailer, "BUDGET_EXHAUSTED"))
	return nil, status.Error(codes.ResourceExhausted, "token budget exhausted")
}

func (f *fakeAgent) FollowUp(context.Context, *agentv1.FollowUpRequest) (*agentv1.FollowUpResponse, error) {
	return nil, status.Error(codes.ResourceExhausted, "too many concurrent rpcs")
}

func (f *fakeAgent) BeginRevision(context.Context, *agentv1.GoalRef) (*agentv1.BeginRevisionResponse, error) {
	return nil, status.Error(codes.Unavailable, "down")
}

func TestClientAgainstServer(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	fake := &fakeAgent{}
	agentv1.RegisterAgentServiceServer(srv, fake)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, err := Dial("passthrough:///bufnet", "secret", Timeouts{Run: 7 * time.Second, Interactive: time.Second},
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ctx := context.Background()

	// read-only call: UNAVAILABLE is retried by the service config
	res, err := c.Result(ctx, 1, "g", model.ModeGeneral)
	if err != nil || !res.Found || *res.Markdown != "# hi" || res.TaskStatus.State != model.StateCompleted {
		t.Fatalf("result %+v %v", res, err)
	}
	if n := fake.resultCalls.Load(); n != 3 {
		t.Fatalf("expected 2 transparent retries, got %d calls", n)
	}

	// Run: token + deadline attached, budget exhaustion mapped, no RPC-level retry
	_, err = c.Run(ctx, RunRequest{MediaID: 1, Goal: "g", Mode: model.ModeLearning,
		TelemetrySeed: &TelemetrySeed{TraceID: "trace-1"}})
	if !common.Has(err, common.KindBudgetExhausted) {
		t.Fatalf("run error %v", err)
	}
	if tok, _ := fake.token.Load().([]string); len(tok) != 1 || tok[0] != "secret" {
		t.Fatalf("token %v", tok)
	}
	if ms := fake.deadline.Load(); ms <= 0 || ms > 7000 {
		t.Fatalf("run deadline %dms", ms)
	}

	// overload (no trailer) is retryable, not a budget verdict
	if _, err := c.FollowUp(ctx, 1, nil, "q", model.ModeGeneral); !common.Has(err, common.KindTransient) {
		t.Fatalf("overload %v", err)
	}

	// writes are not retried at the RPC layer and surface as transient
	if _, err := c.BeginRevision(ctx, 1, "g", model.ModeGeneral); !common.Has(err, common.KindTransient) {
		t.Fatalf("begin revision %v", err)
	}
}
