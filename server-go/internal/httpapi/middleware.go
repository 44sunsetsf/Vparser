package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"dovideo/server/internal/billing"
	"dovideo/server/internal/common"
	"dovideo/server/internal/obs"
)

const userIDKey = "authenticatedUserId"

func writeJSON(c *gin.Context, status int, res common.Result) {
	b, err := common.MarshalNoEscape(res)
	if err != nil {
		slog.Error("response_encode_failed", "err", err)
		b = []byte(`{"code":50000,"message":"服务暂时不可用","data":null}`)
		status = http.StatusInternalServerError
	}
	c.Data(status, "application/json", b)
}

func ok(c *gin.Context, data any) { writeJSON(c, http.StatusOK, common.Ok(data)) }
func okVoid(c *gin.Context)       { ok(c, nil) }
func accepted(c *gin.Context)     { writeJSON(c, http.StatusAccepted, common.OkVoid()) }

// fail maps any error to its status and envelope and aborts the request.
func fail(c *gin.Context, err error) {
	status, res := common.ToHTTP(err)
	if status == http.StatusInternalServerError {
		slog.Error("unhandled_request_error", "path", c.Request.URL.Path, "err", err)
	}
	writeJSON(c, status, res)
	c.Abort()
}

func badRequest(c *gin.Context) { fail(c, common.InvalidArgument("请求参数不合法")) }

// recovery turns panics into the unified 500 envelope.
func recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic_in_handler", "path", c.Request.URL.Path, "panic", r)
				writeJSON(c, http.StatusInternalServerError, common.Fail(common.CodeInternalError, "服务暂时不可用"))
				c.Abort()
			}
		}()
		c.Next()
	}
}

// cors allows the configured origins (GET/POST/DELETE/OPTIONS, any header, credentials, max-age
// 3600); other cross-origin requests get 403 "Invalid CORS request".
func cors(allowed []string) gin.HandlerFunc {
	set := map[string]bool{}
	for _, o := range allowed {
		set[o] = true
	}
	methods := map[string]bool{"GET": true, "POST": true, "DELETE": true, "OPTIONS": true}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" || sameOrigin(c.Request, origin) {
			c.Next()
			return
		}
		c.Writer.Header().Add("Vary", "Origin")
		preflight := c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != ""
		if preflight {
			c.Writer.Header().Add("Vary", "Access-Control-Request-Method")
			c.Writer.Header().Add("Vary", "Access-Control-Request-Headers")
		}
		if !set[origin] {
			c.String(http.StatusForbidden, "Invalid CORS request")
			c.Abort()
			return
		}
		h := c.Writer.Header()
		if preflight {
			if !methods[strings.ToUpper(c.GetHeader("Access-Control-Request-Method"))] {
				c.String(http.StatusForbidden, "Invalid CORS request")
				c.Abort()
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
			if req := c.GetHeader("Access-Control-Request-Headers"); req != "" {
				h.Set("Access-Control-Allow-Headers", req)
			}
			h.Set("Access-Control-Max-Age", "3600")
			c.Status(http.StatusOK)
			c.Abort()
			return
		}
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Allow-Credentials", "true")
		c.Next()
	}
}

// sameOrigin compares the Origin header with scheme://host[:port] of the request.
func sameOrigin(r *http.Request, origin string) bool {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return strings.EqualFold(origin, scheme+"://"+r.Host)
}

// authRequired resolves the bearer session; OPTIONS passes; failures produce the unified 401 body.
func (a *API) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		id, err := a.Auth.ResolveUser(c.Request.Context(), c.GetHeader("Authorization"))
		if err != nil {
			if common.Has(err, common.KindForbidden) {
				writeJSON(c, http.StatusUnauthorized, common.Fail(common.CodeUnauthorized, err.Error()))
				c.Abort()
				return
			}
			fail(c, err)
			return
		}
		c.Set(userIDKey, id)
		// agent calls made for this request are charged to this user (see billing)
		c.Request = c.Request.WithContext(billing.WithUser(c.Request.Context(), id))
		c.Next()
	}
}

func userID(c *gin.Context) int64 { return c.GetInt64(userIDKey) }

// httpMetrics records http_request_duration_seconds labelled by the matched route template
// (/analysis/agent-plan, /admin/failed-analysis/:id/replay), never the raw path, so ids and query
// strings cannot explode the series count. Unmatched requests share one label.
func httpMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		obs.HTTPDuration.WithLabelValues(route, c.Request.Method, strconv.Itoa(c.Writer.Status())).
			Observe(time.Since(started).Seconds())
	}
}
