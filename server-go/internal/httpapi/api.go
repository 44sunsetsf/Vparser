// Package httpapi holds the gin router, middleware and all controllers.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

	"dovideo/server/internal/agentclient"
	"dovideo/server/internal/analysis"
	"dovideo/server/internal/auth"
	"dovideo/server/internal/checkpoint"
	"dovideo/server/internal/common"
	"dovideo/server/internal/media"
	"dovideo/server/internal/obs"
	"dovideo/server/internal/ratelimit"
	"dovideo/server/internal/repo"
	"dovideo/server/internal/taskevents"
	"dovideo/server/internal/upload"
	"dovideo/server/internal/workerpool"
)

const (
	routesPerMinuteUser   = 10
	routesPerMinuteGlobal = 60
	maxMultipartBytes     = 2048 << 20
)

// API bundles the controller dependencies.
type API struct {
	Auth          *auth.Service
	Media         *media.Service
	Upload        *upload.Service
	Instant       *upload.Instant
	Dispatcher    *analysis.Dispatcher
	Status        *analysis.StatusService
	FailedTasks   *analysis.FailedTasks
	Transcription *analysis.Transcriptions
	Hub           *taskevents.Hub
	CP            *checkpoint.Store
	Agent         *agentclient.Client
	Limiter       *ratelimit.Limiter
	Rdb           redis.Cmdable
	AIPool        *workerpool.Pool
	Users         *repo.Users

	// public demo account shown on the login form, and its daily AI allowance (CNY, 0 = none)
	DemoUsername   string
	DemoPassword   string
	DemoDailyLimit float64

	CORSOrigins        []string
	InteractiveTimeout time.Duration
	ShutdownCh         <-chan struct{}
}

// Router builds the gin engine. Middleware order: tracing first (so every later step is inside the
// request span), then metrics (labelled by route template to keep label cardinality bounded),
// then panic recovery and CORS.
func (a *API) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.HandleMethodNotAllowed = true
	r.MaxMultipartMemory = 32 << 20
	r.RedirectTrailingSlash = false
	r.Use(otelgin.Middleware("server-go", otelgin.WithFilter(untraced)), httpMetrics(), recovery(), cors(a.CORSOrigins))

	r.NoRoute(func(c *gin.Context) {
		writeJSON(c, http.StatusNotFound, common.Fail(common.CodeNotFound, "资源不存在"))
	})
	r.NoMethod(func(c *gin.Context) {
		writeJSON(c, http.StatusMethodNotAllowed, common.Fail(common.CodeInvalidArgument, "请求方法不被支持"))
	})

	r.GET("/health", func(c *gin.Context) { ok(c, "UP") })
	r.GET("/metrics", gin.WrapH(obs.Handler()))

	user := r.Group("/user")
	user.GET("/auth-config", a.authConfig)
	user.POST("/register", a.register)
	user.POST("/login", a.login)
	user.POST("/logout", a.authRequired(), a.logout)

	m := r.Group("/media", a.authRequired(), limitBody(maxMultipartBytes))
	m.POST("/init-upload", a.initUpload)
	m.GET("/upload-status", a.uploadStatus)
	m.POST("/upload-chunk", a.uploadChunk)
	m.POST("/complete-upload", a.completeUpload)
	m.POST("/upload", a.uploadFile)
	m.POST("/upload-url", a.uploadURL)
	m.POST("/instant-upload/challenge", a.instantUploadChallenge)
	m.POST("/instant-upload", a.instantUpload)
	m.GET("/list", a.listMedia)
	m.GET("/playback", a.playback)
	m.DELETE("/delete", a.deleteMedia)

	an := r.Group("/analysis", a.authRequired())
	an.POST("/route", a.route)
	an.POST("/ai", a.aiAnalyze)
	an.POST("/follow-up", a.followUp)
	an.GET("/evidence-search", a.evidenceSearch)
	an.POST("/agent-feedback", a.saveFeedback)
	an.POST("/agent-revise", a.reviseAgentResult)
	an.GET("/agent-feedback", a.loadFeedback)
	an.GET("/agent-plan", a.agentPlan)
	an.GET("/analysis-status", a.analysisStatus)
	an.GET("/analysis-events", a.analysisEvents)
	an.GET("/agent-evaluation", a.agentEvaluation)
	an.GET("/agent-trace", a.agentTrace)
	an.POST("/transcribe", a.transcribe)
	an.GET("/transcription-status", a.transcriptionStatus)
	an.GET("/transcription-events", a.transcriptionEvents)
	an.GET("/download", a.download)

	admin := r.Group("/admin/failed-analysis", a.authRequired())
	admin.GET("", a.failedLatest)
	admin.POST("/:id/replay", a.failedReplay)
	return r
}

// untraced keeps probes and scrapes out of the trace backend.
func untraced(r *http.Request) bool {
	return r.URL.Path != "/health" && r.URL.Path != "/metrics"
}

func limitBody(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodPost {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, max)
		}
		c.Next()
	}
}

// runInteractive runs fn on the AI pool with the interactive timeout (AI_INTERACTIVE_TIMEOUT_MS).
func runInteractive[T any](c *gin.Context, a *API, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(c.Request.Context(), a.InteractiveTimeout)
	defer cancel()
	type outcome struct {
		v   T
		err error
	}
	ch := make(chan outcome, 1)
	if err := a.AIPool.Submit(func() {
		v, err := fn(ctx)
		ch <- outcome{v, err}
	}); err != nil {
		return zero, common.Business(common.CodeServiceUnavailable, "AI 请求较多，请稍后再试")
	}
	select {
	case o := <-ch:
		return o.v, o.err
	case <-ctx.Done():
		return zero, common.Business(common.CodeServiceUnavailable, "AI 请求处理超时，请稍后再试")
	}
}
