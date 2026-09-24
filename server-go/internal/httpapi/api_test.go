package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"dovideo/server/internal/auth"
	"dovideo/server/internal/common"
	"dovideo/server/internal/model"
	"dovideo/server/internal/taskevents"
)

func newTestAPI(t *testing.T) (*API, *gin.Engine, string) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	a := &API{
		Auth: auth.New(rdb, nil), Hub: taskevents.New(rdb), Rdb: rdb,
		CORSOrigins: []string{"http://localhost:5173"},
	}
	token, err := a.Auth.CreateSession(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	return a, a.Router(), token
}

func do(r http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) common.Result {
	t.Helper()
	var res common.Result
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return res
}

func TestHealth(t *testing.T) {
	_, r, _ := newTestAPI(t)
	w := do(r, "GET", "/health", "", nil)
	if w.Code != 200 || w.Body.String() != `{"code":0,"message":"success","data":"UP"}` {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
}

func TestAuthInterceptor(t *testing.T) {
	_, r, token := newTestAPI(t)
	tests := []struct {
		name    string
		method  string
		path    string
		auth    string
		message string
	}{
		{"no header", "GET", "/media/list", "", "请先登录"},
		{"short token", "GET", "/analysis/analysis-status", "Bearer short", "无效的登录凭证"},
		{"unknown session", "GET", "/admin/failed-analysis", "Bearer " + strings.Repeat("z", 43), "登录状态已失效"},
		{"logout needs auth", "POST", "/user/logout", "", "请先登录"},
	}
	for _, tt := range tests {
		h := map[string]string{}
		if tt.auth != "" {
			h["Authorization"] = tt.auth
		}
		w := do(r, tt.method, tt.path, "", h)
		res := decode(t, w)
		if w.Code != 401 || res.Code != 40100 || res.Message != tt.message {
			t.Errorf("%s: %d %+v", tt.name, w.Code, res)
		}
	}
	// valid session reaches the handler (missing param -> 400, not 401)
	w := do(r, "GET", "/media/playback", "", map[string]string{"Authorization": "Bearer " + token})
	if res := decode(t, w); w.Code != 400 || res.Code != 40000 || res.Message != "请求参数不合法" {
		t.Fatalf("%d %+v", w.Code, res)
	}
	// logout revokes the session
	if w := do(r, "POST", "/user/logout", "", map[string]string{"Authorization": "Bearer " + token}); w.Code != 200 {
		t.Fatalf("logout %d %s", w.Code, w.Body.String())
	}
	if w := do(r, "GET", "/media/list", "", map[string]string{"Authorization": "Bearer " + token}); w.Code != 401 {
		t.Fatalf("revoked token must be rejected, got %d", w.Code)
	}
}

func TestValidationMessages(t *testing.T) {
	_, r, token := newTestAPI(t)
	bearer := map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json"}
	tests := []struct {
		name, path, body, message string
	}{
		{"register short", "/user/register", `{"username":"ab","password":"short"}`, "username: 账号需为 3-32 位; password: 密码需至少 8 位"},
		{"register blank", "/user/register", `{"username":" ","password":""}`, "username: 账号不能为空; password: 密码不能为空"},
		{"register long nickname", "/user/register",
			`{"username":"abc","password":"password1","nickname":"` + strings.Repeat("n", 51) + `"}`, "nickname: 昵称不能超过 50 个字符"},
		{"login long username", "/user/login", `{"username":"` + strings.Repeat("a", 33) + `","password":"x"}`, "username: 账号不能超过 32 位"},
		{"login missing password", "/user/login", `{"username":"abc"}`, "password: 密码不能为空"},
		{"route blank goal", "/analysis/route", `{"goal":"  "}`, "goal: 分析目标不能为空"},
		{"route long goal", "/analysis/route", `{"goal":"` + strings.Repeat("g", 501) + `"}`, "goal: 分析目标不能超过 500 字"},
		{"feedback missing media", "/analysis/agent-feedback", `{"goal":"g"}`, "mediaId: mediaId 不能为空"},
		{"feedback too many tasks", "/analysis/agent-feedback",
			`{"mediaId":1,"goal":"g","correctedTasks":["a","b","c","d","e","f"]}`, "correctedTasks: 修正任务最多 5 条"},
		{"feedback negative ts", "/analysis/agent-feedback", `{"mediaId":1,"goal":"g","evidenceTimestamp":-1}`, "evidenceTimestamp: 证据时间戳不能为负数"},
	}
	for _, tt := range tests {
		w := do(r, "POST", tt.path, tt.body, bearer)
		res := decode(t, w)
		if w.Code != 400 || res.Code != 40001 || res.Message != tt.message {
			t.Errorf("%s: %d %+v", tt.name, w.Code, res)
		}
	}
	// unreadable body -> 40000
	w := do(r, "POST", "/user/login", `not json`, bearer)
	if res := decode(t, w); w.Code != 400 || res.Code != 40000 || res.Message != "请求参数不合法" {
		t.Fatalf("%d %+v", w.Code, res)
	}
	// the rating rule is a business rejection (INVALID_ARGUMENT)
	w = do(r, "POST", "/analysis/agent-feedback", `{"mediaId":1,"goal":"g","rating":5}`, bearer)
	if res := decode(t, w); w.Code != 400 || res.Code != 40000 || res.Message != "rating 只能是 -1 或 1" {
		t.Fatalf("%d %+v", w.Code, res)
	}
}

func TestRoutingErrors(t *testing.T) {
	_, r, _ := newTestAPI(t)
	if w := do(r, "GET", "/nope", "", nil); w.Code != 404 || decode(t, w).Code != 40400 {
		t.Fatalf("404: %d %s", w.Code, w.Body.String())
	}
	w := do(r, "GET", "/user/login", "", nil)
	if res := decode(t, w); w.Code != 405 || res.Code != 40000 || res.Message != "请求方法不被支持" {
		t.Fatalf("405: %d %+v", w.Code, res)
	}
}

func TestCORS(t *testing.T) {
	_, r, _ := newTestAPI(t)
	// allowed preflight
	w := do(r, "OPTIONS", "/media/upload", "", map[string]string{
		"Origin": "http://localhost:5173", "Access-Control-Request-Method": "POST",
		"Access-Control-Request-Headers": "authorization,content-type",
	})
	h := w.Header()
	if w.Code != 200 || h.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		h.Get("Access-Control-Allow-Credentials") != "true" || h.Get("Access-Control-Allow-Methods") != "GET,POST,DELETE,OPTIONS" ||
		h.Get("Access-Control-Allow-Headers") != "authorization,content-type" || h.Get("Access-Control-Max-Age") != "3600" {
		t.Fatalf("preflight: %d %v", w.Code, h)
	}
	// disallowed method in preflight
	w = do(r, "OPTIONS", "/media/upload", "", map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "PUT"})
	if w.Code != 403 {
		t.Fatalf("PUT preflight should be 403, got %d", w.Code)
	}
	// disallowed origin
	w = do(r, "GET", "/health", "", map[string]string{"Origin": "http://evil.example"})
	if w.Code != 403 || w.Body.String() != "Invalid CORS request" {
		t.Fatalf("evil origin: %d %s", w.Code, w.Body.String())
	}
	// allowed actual request
	w = do(r, "GET", "/health", "", map[string]string{"Origin": "http://localhost:5173"})
	if w.Code != 200 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("actual: %d %v", w.Code, w.Header())
	}
	// same-origin requests behind the nginx proxy carry an Origin header but are not CORS
	req := httptest.NewRequest("GET", "http://app.example/health", nil)
	req.Header.Set("Origin", "http://app.example")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("same-origin: %d %v", rec.Code, rec.Header())
	}
	// preflight bypasses auth (OPTIONS on a protected route)
	if w := do(r, "OPTIONS", "/analysis/route", "", map[string]string{"Origin": "http://localhost:5173", "Access-Control-Request-Method": "POST"}); w.Code != 200 {
		t.Fatalf("preflight needs no auth: %d", w.Code)
	}
}

func TestStreamEventsFormat(t *testing.T) {
	a, _, _ := newTestAPI(t)
	ch := make(chan struct{})
	a.ShutdownCh = ch
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/sse", func(c *gin.Context) {
		sub, err := a.Hub.Subscribe(1, taskevents.TypeAnalysis, "g", model.ModeGeneral,
			model.StatusOf(model.StateProcessing, "working"), model.StageConsuming)
		if err != nil {
			t.Error(err)
			return
		}
		a.Hub.PublishTranscription(c.Request.Context(), 99, model.StatusCompleted("x"), model.StageCompleted) // unrelated key
		if err := a.Hub.PublishAnalysis(c.Request.Context(), 1, "g", model.ModeGeneral, model.StatusCompleted("done"), model.StageCompleted); err != nil {
			t.Error(err)
		}
		a.streamEvents(c, sub)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/sse", nil))
	want := "event:task-status\ndata:{\"state\":\"PROCESSING\",\"result\":null,\"message\":\"working\",\"stage\":\"CONSUMING\"}\n\n" +
		"event:task-status\ndata:{\"state\":\"COMPLETED\",\"result\":\"done\",\"message\":\"任务完成\",\"stage\":\"COMPLETED\"}\n\n"
	if w.Body.String() != want {
		t.Fatalf("got:\n%q\nwant:\n%q", w.Body.String(), want)
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/event-stream;charset=UTF-8" {
		t.Fatalf("content-type %q", ct)
	}
}

func TestFormURLEncode(t *testing.T) {
	tests := map[string]string{
		"audio.mp3": "audio.mp3",
		"我的 视频.mp3": "%E6%88%91%E7%9A%84+%E8%A7%86%E9%A2%91.mp3",
		"a~b*c_d-e": "a%7Eb*c_d-e",
		"x&y=z/1":   "x%26y%3Dz%2F1",
	}
	for in, want := range tests {
		if got := formURLEncode(in); got != want {
			t.Errorf("%q => %q want %q", in, got, want)
		}
	}
}

func TestNormalizeText(t *testing.T) {
	if got, err := normalizeText("  hi  ", "分析目标"); err != nil || got != "hi" {
		t.Fatalf("%q %v", got, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("x", 501)} {
		_, err := normalizeText(bad, "追问内容")
		if err == nil || err.Error() != "追问内容不能为空且不能超过 500 字" {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if _, err := normalizeText(strings.Repeat("目", 500), "x"); err != nil {
		t.Fatal("500 chars is allowed")
	}
}

func TestMetricsUseRouteTemplates(t *testing.T) {
	_, r, _ := newTestAPI(t)
	do(r, "GET", "/health", "", nil)
	do(r, "POST", "/admin/failed-analysis/12345/replay", "", nil) // 401, but matched a template
	do(r, "GET", "/no/such/path/987", "", nil)
	w := do(r, "GET", "/metrics", "", nil)
	body := w.Body.String()
	for _, want := range []string{
		`http_request_duration_seconds_count{method="GET",route="/health",status="200"}`,
		`http_request_duration_seconds_count{method="POST",route="/admin/failed-analysis/:id/replay",status="401"}`,
		`route="unmatched"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %s", want)
		}
	}
	// Only inspect route labels: the rest of the exposition (runtime/memory gauges) may contain any digits.
	for _, line := range strings.Split(body, "\n") {
		i := strings.Index(line, `route="`)
		if i < 0 {
			continue
		}
		route := line[i+len(`route="`):]
		route = route[:strings.IndexByte(route, '"')]
		if strings.Contains(route, "12345") || strings.Contains(route, "987") {
			t.Fatalf("raw path leaked into route label: %q", route)
		}
	}
}
