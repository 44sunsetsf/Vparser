// Package agentclient is the gRPC client of the agent service (contract §2).
//
// Cross-cutting policy lives in one unary interceptor rather than at each call site: every call
// gets a deadline chosen by method, the internal token, and a latency metric. Transport concerns
// (tracing, retries of idempotent reads, keepalive) are declared on the connection.
package agentclient

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	agentv1 "dovideo/server/gen/agent/v1"
	"dovideo/server/internal/model"
	"dovideo/server/internal/obs"
)

// TokenHeader carries the shared internal token.
const TokenHeader = "x-internal-token"

// AgentErrorTrailer refines a status code. RESOURCE_EXHAUSTED alone means the agent is overloaded
// (its concurrency limit rejected the call) and is retryable; only with this trailer set to
// BUDGET_EXHAUSTED does it mean the task itself ran out of budget.
const AgentErrorTrailer = "x-agent-error"

// DefaultTimeout applies to every call without a more specific budget.
const DefaultTimeout = 10 * time.Second

// retryServiceConfig retries only side-effect-free reads, and only on UNAVAILABLE (the request
// provably did not run). Run and all writes are retried by the Kafka retry chain instead, which has
// the idempotency keys and the attempt accounting that a blind RPC retry would bypass.
const retryServiceConfig = `{
  "methodConfig": [{
    "name": [
      {"service": "dovideo.agent.v1.AgentService", "method": "GetResult"},
      {"service": "dovideo.agent.v1.AgentService", "method": "GetPlan"},
      {"service": "dovideo.agent.v1.AgentService", "method": "GetTrace"},
      {"service": "dovideo.agent.v1.AgentService", "method": "GetEvaluation"},
      {"service": "dovideo.agent.v1.AgentService", "method": "ListFeedback"},
      {"service": "dovideo.agent.v1.AgentService", "method": "SearchEvidence"}
    ],
    "retryPolicy": {
      "maxAttempts": 4,
      "initialBackoff": "0.2s",
      "maxBackoff": "2s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// Timeouts chooses the per-call deadline.
type Timeouts struct {
	Run         time.Duration // Run: the agent budget plus headroom
	Interactive time.Duration // FollowUp, ClassifyMode, SearchEvidence: a user is waiting
}

// For returns the deadline budget of a full gRPC method name.
func (t Timeouts) For(fullMethod string) time.Duration {
	switch fullMethod {
	case agentv1.AgentService_Run_FullMethodName:
		return t.Run
	case agentv1.AgentService_FollowUp_FullMethodName, agentv1.AgentService_ClassifyMode_FullMethodName,
		agentv1.AgentService_SearchEvidence_FullMethodName:
		return t.Interactive
	}
	return DefaultTimeout
}

// Client wraps the generated stub with domain-typed methods.
type Client struct {
	conn *grpc.ClientConn
	api  agentv1.AgentServiceClient
}

// Dial creates a lazily connecting client (grpc.NewClient does no I/O; the first call connects),
// so the gateway can start before the agent is up.
func Dial(addr, token string, timeouts Timeouts, extra ...grpc.DialOption) (*Client, error) {
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()), // internal network only (contract §1)
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithDefaultServiceConfig(retryServiceConfig),
		grpc.WithChainUnaryInterceptor(Interceptor(token, timeouts)),
		// Pings keep NAT/conntrack entries alive during long, silent Run calls and detect a dead
		// peer within Time+Timeout instead of waiting for the full deadline.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: 60 * time.Second, Timeout: 20 * time.Second, PermitWithoutStream: true,
		}),
	}, extra...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("agent grpc client: %w", err)
	}
	return &Client{conn: conn, api: agentv1.NewAgentServiceClient(conn)}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Interceptor applies the per-method deadline, attaches the token and records latency by code.
func Interceptor(token string, timeouts Timeouts) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		ctx, cancel := context.WithTimeout(ctx, timeouts.For(method))
		defer cancel()
		ctx = metadata.AppendToOutgoingContext(ctx, TokenHeader, token)
		var trailer metadata.MD
		opts = append(opts, grpc.Trailer(&trailer))
		started := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		if v := trailer.Get(AgentErrorTrailer); err != nil && len(v) > 0 {
			err = &agentError{error: err, reason: v[0]}
		}
		obs.AgentGRPCDuration.WithLabelValues(shortMethod(method), status.Code(err).String()).
			Observe(time.Since(started).Seconds())
		return err
	}
}

func shortMethod(full string) string {
	for i := len(full) - 1; i >= 0; i-- {
		if full[i] == '/' {
			return full[i+1:]
		}
	}
	return full
}

func goalRef(mediaID int64, goal string, mode model.AnalysisMode) *agentv1.GoalRef {
	return &agentv1.GoalRef{MediaId: mediaID, Goal: goal, Mode: ModeToProto(mode)}
}

// ---- analysis lifecycle ----

// StageSeed is one entry of TelemetrySeed.Stages.
type StageSeed struct {
	DurationMs int64
	Success    bool
}

// TelemetrySeed carries the context-build timings collected before Run, so one analysis ends up
// as one telemetry record on the agent side.
type TelemetrySeed struct {
	TraceID  string
	Stages   map[string]StageSeed
	Counters map[string]int64
}

// RunRequest starts the agent workflow.
type RunRequest struct {
	MediaID       int64
	Goal          string
	Mode          model.AnalysisMode
	TelemetrySeed *TelemetrySeed
}

// RunResult is the part of RunResponse the gateway uses.
type RunResult struct {
	Markdown   string
	Round      int
	TaskStatus model.TaskStatus
}

func (c *Client) Run(ctx context.Context, req RunRequest) (*RunResult, error) {
	res, err := c.api.Run(ctx, &agentv1.RunRequest{
		MediaId: req.MediaID, Goal: req.Goal, Mode: ModeToProto(req.Mode), TelemetrySeed: SeedToProto(req.TelemetrySeed),
	})
	if err != nil {
		return nil, MapError(err)
	}
	return &RunResult{Markdown: res.GetMarkdown(), Round: int(res.GetState().GetRound()),
		TaskStatus: TaskStatusFromProto(res.GetTaskStatus())}, nil
}

// ResultResponse reports a finished result, if any.
type ResultResponse struct {
	Found      bool
	Markdown   *string
	TaskStatus *model.TaskStatus
}

func (c *Client) Result(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (*ResultResponse, error) {
	res, err := c.api.GetResult(ctx, goalRef(mediaID, goal, mode))
	if err != nil {
		return nil, MapError(err)
	}
	out := &ResultResponse{Found: res.GetFound()}
	if out.Found {
		md := res.GetMarkdown()
		out.Markdown = &md
		if res.GetTaskStatus() != nil {
			st := TaskStatusFromProto(res.GetTaskStatus())
			out.TaskStatus = &st
		}
	}
	return out, nil
}

// ReuseResponse reports whether another media's result was attached.
type ReuseResponse struct {
	Reused     bool
	Markdown   *string
	TaskStatus *model.TaskStatus
}

func (c *Client) Reuse(ctx context.Context, mediaID, sourceMediaID int64, goal string, mode model.AnalysisMode,
	targetSource string) (*ReuseResponse, error) {
	res, err := c.api.ReuseResult(ctx, &agentv1.ReuseRequest{MediaId: mediaID, SourceMediaId: sourceMediaID,
		Goal: goal, Mode: ModeToProto(mode), TargetSource: targetSource})
	if err != nil {
		return nil, MapError(err)
	}
	r := &ReuseResponse{Reused: res.GetReused()}
	if r.Reused {
		md := res.GetMarkdown()
		r.Markdown = &md
		if res.GetTaskStatus() != nil {
			st := TaskStatusFromProto(res.GetTaskStatus())
			r.TaskStatus = &st
		}
	}
	return r, nil
}

// Purge removes every agent-side artifact of a media file.
func (c *Client) Purge(ctx context.Context, mediaID int64) error {
	_, err := c.api.Purge(ctx, &agentv1.MediaRef{MediaId: mediaID})
	return MapError(err)
}

// ---- interactive ----

func (c *Client) FollowUp(ctx context.Context, mediaID int64, goal *string, question string, mode model.AnalysisMode) (string, error) {
	res, err := c.api.FollowUp(ctx, &agentv1.FollowUpRequest{MediaId: mediaID, Goal: goal, Question: question,
		Mode: ModeToProto(mode)})
	if err != nil {
		return "", MapError(err)
	}
	return res.GetAnswer(), nil
}

func (c *Client) EvidenceSearch(ctx context.Context, mediaID int64, query string) ([]EvidenceHit, error) {
	res, err := c.api.SearchEvidence(ctx, &agentv1.SearchEvidenceRequest{MediaId: mediaID, Query: query})
	if err != nil {
		return nil, MapError(err)
	}
	return HitsFromProto(res.GetHits()), nil
}

func (c *Client) ClassifyMode(ctx context.Context, goal string) (model.RouteDecision, error) {
	res, err := c.api.ClassifyMode(ctx, &agentv1.ClassifyModeRequest{Goal: goal})
	if err != nil {
		return model.RouteDecision{}, MapError(err)
	}
	return model.NewRouteDecision(ModeFromProto(res.GetMode()), res.GetReason()), nil
}

// ---- revisions and feedback ----

// StageRevision returns the goal the revision will run under.
func (c *Client) StageRevision(ctx context.Context, fb model.AgentFeedback, mode model.AnalysisMode) (string, error) {
	res, err := c.api.StageRevision(ctx, &agentv1.StageRevisionRequest{Feedback: FeedbackToProto(fb), Mode: ModeToProto(mode)})
	if err != nil {
		return "", MapError(err)
	}
	return res.GetGoal(), nil
}

func (c *Client) BeginRevision(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (bool, error) {
	res, err := c.api.BeginRevision(ctx, goalRef(mediaID, goal, mode))
	if err != nil {
		return false, MapError(err)
	}
	return res.GetBegun(), nil
}

func (c *Client) CompleteRevision(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) error {
	_, err := c.api.CompleteRevision(ctx, goalRef(mediaID, goal, mode))
	return MapError(err)
}

func (c *Client) CancelRevision(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) error {
	_, err := c.api.CancelRevision(ctx, goalRef(mediaID, goal, mode))
	return MapError(err)
}

func (c *Client) SaveFeedback(ctx context.Context, fb model.AgentFeedback) error {
	_, err := c.api.SaveFeedback(ctx, FeedbackToProto(fb))
	return MapError(err)
}

func (c *Client) LoadFeedback(ctx context.Context, mediaID int64) ([]model.AgentFeedback, error) {
	res, err := c.api.ListFeedback(ctx, &agentv1.MediaRef{MediaId: mediaID})
	if err != nil {
		return nil, MapError(err)
	}
	out := make([]model.AgentFeedback, 0, len(res.GetItems()))
	for _, it := range res.GetItems() {
		out = append(out, FeedbackFromProto(it))
	}
	return out, nil
}

// ---- inspection ----

// Plan returns the plan as client JSON, or nil when none exists yet.
func (c *Client) Plan(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (*Plan, error) {
	res, err := c.api.GetPlan(ctx, goalRef(mediaID, goal, mode))
	if err != nil {
		return nil, MapError(err)
	}
	if !res.GetFound() || res.GetPlan() == nil {
		return nil, nil
	}
	return PlanFromProto(res.GetPlan()), nil
}

// Trace returns the agent trace as a JSON object, or nil when there is none.
func (c *Client) Trace(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (map[string]any, error) {
	res, err := c.api.GetTrace(ctx, goalRef(mediaID, goal, mode))
	if err != nil {
		return nil, MapError(err)
	}
	return StructToJSON(res), nil
}

// Evaluation returns the evaluation report as a JSON object, or nil when there is none.
func (c *Client) Evaluation(ctx context.Context, mediaID int64, goal string, mode model.AnalysisMode) (map[string]any, error) {
	res, err := c.api.GetEvaluation(ctx, goalRef(mediaID, goal, mode))
	if err != nil {
		return nil, MapError(err)
	}
	return StructToJSON(res), nil
}
