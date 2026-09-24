package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/analysis"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/ratelimit"
	"dovideo/server/internal/taskevents"
)

const streamTimeout = 30 * time.Minute

func (a *API) requireVideoContext(c *gin.Context, mediaID int64) error {
	vc, err := a.CP.LoadContext(c.Request.Context(), mediaID)
	if err != nil {
		return err
	}
	if vc == nil {
		return common.NotReady()
	}
	return nil
}

// tryRouteQuota charges the per-user then global route bucket; a limiter failure means "busy".
func (a *API) tryRouteQuota(c *gin.Context, uid int64) bool {
	ok, err := a.Limiter.TakeUserThenGlobal(c.Request.Context(), ratelimit.ScopeRoute, uid, ratelimit.RouteUser, ratelimit.RouteGlobal)
	if err != nil {
		slog.Warn("route_rate_limiter_unavailable", "userId", uid, "err", err)
		return false
	}
	return ok
}

func (a *API) route(c *gin.Context) {
	var req model.RouteRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, err)
		return
	}
	if err := validateStruct(req); err != nil {
		fail(c, err)
		return
	}
	goal, err := normalizeText(*req.Goal, "分析目标")
	if err != nil {
		fail(c, err)
		return
	}
	if !a.tryRouteQuota(c, userID(c)) {
		ok(c, model.NewRouteDecision(model.ModeGeneral, "自动路由当前繁忙,已按通用模式分析"))
		return
	}
	dec, err := a.Agent.ClassifyMode(c.Request.Context(), goal)
	if err != nil {
		// routing failure must never break analysis: fall back to GENERAL
		slog.Warn("route_classification_failed", "goalLength", len(goal), "err", err)
		ok(c, model.NewRouteDecision(model.ModeGeneral, "意图识别暂不可用,已按通用模式分析"))
		return
	}
	ok(c, dec)
}

func submissionResponse(c *gin.Context, res analysis.SubmissionResult) {
	switch res {
	case analysis.Accepted:
		accepted(c)
	case analysis.RateLimited:
		fail(c, common.Business(common.CodeRateLimited, "系统繁忙，请稍后再试"))
	case analysis.Duplicate:
		fail(c, common.Business(common.CodeConflict, "相同视频和分析目标正在处理中"))
	default:
		fail(c, common.Business(common.CodeInternalError, "任务提交失败"))
	}
}

func (a *API) aiAnalyze(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	goal, err := normalizeText(stringWithDefault(c, "goal", "理解视频核心内容并生成结构化分析报告"), "分析目标")
	if err != nil {
		fail(c, err)
		return
	}
	mode, err := modeParam(c)
	if err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	mf, err := a.Media.RequireOwnedMedia(ctx, id, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	res, err := a.Status.LoadResult(ctx, id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	if res != nil {
		// reusable result exists: "done" (200), not "accepted" (202)
		okVoid(c)
		return
	}
	sub, err := a.Dispatcher.Submit(ctx, mf, goal, nil, mode)
	if err != nil {
		fail(c, err)
		return
	}
	submissionResponse(c, sub)
}

func (a *API) followUp(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	q, err := requiredString(c, "question")
	if err != nil {
		fail(c, err)
		return
	}
	goalRaw, _ := optionalString(c, "goal")
	question, err := normalizeText(q, "追问内容")
	if err != nil {
		fail(c, err)
		return
	}
	var goal *string
	if strings.TrimSpace(goalRaw) != "" {
		g, err := normalizeText(goalRaw, "原始分析目标")
		if err != nil {
			fail(c, err)
			return
		}
		goal = &g
	}
	ctx := c.Request.Context()
	if _, err := a.Media.RequireOwnedMedia(ctx, id, userID(c)); err != nil {
		fail(c, err)
		return
	}
	if err := a.requireVideoContext(c, id); err != nil {
		fail(c, err)
		return
	}
	if err := a.Dispatcher.RequireAiQuota(ctx, userID(c)); err != nil {
		fail(c, err)
		return
	}
	mode, err := modeParam(c)
	if err != nil {
		fail(c, err)
		return
	}
	answer, err := runInteractive(c, a, func(ctx2 context.Context) (string, error) {
		return a.Agent.FollowUp(ctx2, id, goal, question, mode)
	})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, answer)
}

func (a *API) evidenceSearch(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	q, err := requiredString(c, "query")
	if err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if _, err := a.Media.RequireOwnedMedia(ctx, id, userID(c)); err != nil {
		fail(c, err)
		return
	}
	if err := a.requireVideoContext(c, id); err != nil {
		fail(c, err)
		return
	}
	if err := a.Dispatcher.RequireAiQuota(ctx, userID(c)); err != nil {
		fail(c, err)
		return
	}
	query, err := normalizeText(q, "检索问题")
	if err != nil {
		fail(c, err)
		return
	}
	hits, err := runInteractive(c, a, func(ctx2 context.Context) ([]agentclient.EvidenceHit, error) {
		return a.Agent.EvidenceSearch(ctx2, id, query)
	})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, hits)
}

func ensureRating(fb *model.AgentFeedback) error {
	if fb.Rating != nil && *fb.Rating != -1 && *fb.Rating != 1 {
		return common.Business(common.CodeInvalidArgument, "rating 只能是 -1 或 1")
	}
	return nil
}

func (a *API) saveFeedback(c *gin.Context) {
	var fb model.AgentFeedback
	if err := decodeBody(c, &fb); err != nil {
		fail(c, err)
		return
	}
	if err := validateStruct(fb); err != nil {
		fail(c, err)
		return
	}
	if err := ensureRating(&fb); err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if _, err := a.Media.RequireOwnedMedia(ctx, *fb.MediaID, userID(c)); err != nil {
		fail(c, err)
		return
	}
	mode, err := model.ModeFromRequest(deref(fb.Mode))
	if err != nil {
		fail(c, err)
		return
	}
	if err := a.Agent.SaveFeedback(ctx, fb.Normalized(mode)); err != nil {
		fail(c, err)
		return
	}
	okVoid(c)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *API) reviseAgentResult(c *gin.Context) {
	var fb model.AgentFeedback
	if err := decodeBody(c, &fb); err != nil {
		fail(c, err)
		return
	}
	if err := validateStruct(fb); err != nil {
		fail(c, err)
		return
	}
	if err := ensureRating(&fb); err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	mf, err := a.Media.RequireOwnedMedia(ctx, *fb.MediaID, userID(c))
	if err != nil {
		fail(c, err)
		return
	}
	revisedGoal := fb.RevisionGoal()
	mode, err := modeParam(c)
	if err != nil {
		fail(c, err)
		return
	}
	res, err := a.Dispatcher.Submit(ctx, mf, revisedGoal, &fb, mode)
	if err != nil {
		fail(c, err)
		return
	}
	submissionResponse(c, res)
}

func (a *API) loadFeedback(c *gin.Context) {
	id, err := requiredInt64(c, "id")
	if err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if _, err := a.Media.RequireOwnedMedia(ctx, id, userID(c)); err != nil {
		fail(c, err)
		return
	}
	items, err := a.Agent.LoadFeedback(ctx, id)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, items)
}

// goalTarget parses the common (id, goal, mode) triple; ownership is checked before the goal is
// validated so a stranger learns nothing about validation rules of media they do not own.
func (a *API) goalTarget(c *gin.Context) (id int64, goal string, mode model.AnalysisMode, err error) {
	if id, err = requiredInt64(c, "id"); err != nil {
		return
	}
	var goalRaw string
	if goalRaw, err = requiredString(c, "goal"); err != nil {
		return
	}
	if _, err = a.Media.RequireOwnedMedia(c.Request.Context(), id, userID(c)); err != nil {
		return
	}
	if goal, err = normalizeText(goalRaw, "分析目标"); err != nil {
		return
	}
	mode, err = modeParam(c)
	return
}

func (a *API) agentPlan(c *gin.Context) {
	id, goal, mode, err := a.goalTarget(c)
	if err != nil {
		fail(c, err)
		return
	}
	plan, err := a.Agent.Plan(c.Request.Context(), id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	if plan == nil {
		ok(c, nil)
		return
	}
	ok(c, plan)
}

func (a *API) agentEvaluation(c *gin.Context) {
	id, goal, mode, err := a.goalTarget(c)
	if err != nil {
		fail(c, err)
		return
	}
	doc, err := a.Agent.Evaluation(c.Request.Context(), id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	okDocument(c, doc)
}

func (a *API) agentTrace(c *gin.Context) {
	id, goal, mode, err := a.goalTarget(c)
	if err != nil {
		fail(c, err)
		return
	}
	doc, err := a.Agent.Trace(c.Request.Context(), id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	okDocument(c, doc)
}

// okDocument writes a free-form document, or null when there is none.
func okDocument(c *gin.Context, doc map[string]any) {
	if doc == nil {
		ok(c, nil)
		return
	}
	ok(c, doc)
}

func (a *API) analysisStatus(c *gin.Context) {
	id, goal, mode, err := a.goalTarget(c)
	if err != nil {
		fail(c, err)
		return
	}
	st, err := a.Status.Current(c.Request.Context(), id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, st)
}

func (a *API) analysisEvents(c *gin.Context) {
	id, goal, mode, err := a.goalTarget(c)
	if err != nil {
		fail(c, err)
		return
	}
	ctx := c.Request.Context()
	st, err := a.Status.Current(ctx, id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	stage, err := a.Status.Stage(ctx, id, goal, mode)
	if err != nil {
		fail(c, err)
		return
	}
	sub, err := a.Hub.Subscribe(id, taskevents.TypeAnalysis, goal, mode, st, stage)
	if err != nil {
		fail(c, err)
		return
	}
	a.streamEvents(c, sub)
}

// streamEvents writes SSE frames ("event:task-status\ndata:<json>") until a terminal event,
// the 30 minute timeout, client disconnect or server shutdown.
func (a *API) streamEvents(c *gin.Context, sub *taskevents.Subscription) {
	defer sub.Close()
	w := c.Writer
	h := w.Header()
	h.Set("Content-Type", "text/event-stream;charset=UTF-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	w.Flush()
	timer := time.NewTimer(streamTimeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-sub.Events:
			b, err := common.MarshalNoEscape(ev)
			if err != nil {
				return
			}
			if _, err := w.Write([]byte("event:task-status\ndata:" + string(b) + "\n\n")); err != nil {
				return
			}
			w.Flush()
			if ev.Terminal() {
				return
			}
		case <-timer.C:
			return
		case <-c.Request.Context().Done():
			return
		case <-a.ShutdownCh:
			return
		}
	}
}

// ---- admin ----

func (a *API) requireAdmin(c *gin.Context) bool {
	if err := a.Auth.RequireAdmin(c.Request.Context(), userID(c)); err != nil {
		fail(c, err)
		return false
	}
	return true
}

func (a *API) failedLatest(c *gin.Context) {
	if !a.requireAdmin(c) {
		return
	}
	tasks, err := a.FailedTasks.Latest(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]model.FailedTaskView, 0, len(tasks))
	for i := range tasks {
		out = append(out, model.FailedViewOf(&tasks[i]))
	}
	ok(c, out)
}

func (a *API) failedReplay(c *gin.Context) {
	if !a.requireAdmin(c) {
		return
	}
	id, err := parsePathID(c.Param("id"))
	if err != nil {
		fail(c, err)
		return
	}
	if err := a.FailedTasks.Replay(c.Request.Context(), id); err != nil {
		fail(c, err)
		return
	}
	accepted(c)
}
