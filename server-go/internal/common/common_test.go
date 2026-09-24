package common

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestErrorCodes(t *testing.T) {
	tests := []struct {
		c      ErrorCode
		code   int
		status int
	}{
		{CodeInvalidArgument, 40000, 400}, {CodeValidationFailed, 40001, 400}, {CodeUnauthorized, 40100, 401},
		{CodeForbidden, 40300, 403}, {CodeNotFound, 40400, 404}, {CodeConflict, 40900, 409}, {CodeUnprocessable, 42200, 422},
		{CodeRateLimited, 42900, 429}, {CodeInternalError, 50000, 500}, {CodeServiceUnavailable, 50300, 503},
	}
	for _, tt := range tests {
		if tt.c.Code != tt.code || tt.c.Status != tt.status {
			t.Errorf("%+v != %d/%d", tt.c, tt.code, tt.status)
		}
	}
}

func TestResultJSON(t *testing.T) {
	b, _ := MarshalNoEscape(Ok("UP"))
	if string(b) != `{"code":0,"message":"success","data":"UP"}` {
		t.Fatal(string(b))
	}
	b, _ = MarshalNoEscape(OkVoid())
	if string(b) != `{"code":0,"message":"success","data":null}` {
		t.Fatal(string(b))
	}
	b, _ = MarshalNoEscape(Fail(CodeConflict, "a<b>&c"))
	if string(b) != `{"code":40900,"message":"a<b>&c","data":null}` {
		t.Fatal(string(b))
	}
}

func TestToHTTP(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		status  int
		code    int
		message string
	}{
		{"business", Business(CodeRateLimited, "系统繁忙，请稍后再试"), 429, 42900, "系统繁忙，请稍后再试"},
		{"business blank", Business(CodeConflict, " "), 409, 40900, "请求处理失败"},
		{"invalid argument", InvalidArgument("分析目标不能为空且不能超过 500 字"), 400, 40000, "分析目标不能为空且不能超过 500 字"},
		{"invalid argument blank", InvalidArgument(""), 400, 40000, "请求参数不合法"},
		{"not found", NotFound("文件不存在"), 404, 40400, "文件不存在"},
		{"forbidden", Forbidden("无权访问该文件"), 403, 40300, "无权访问该文件"},
		{"forbidden blank", Forbidden(""), 403, 40300, "无访问权限"},
		{"budget", BudgetExhausted("超预算", nil), 422, 42200, "超预算"},
		{"context not ready", NotReady(), 409, 40900, NotReadyMessage},
		{"validation", Validation("goal: 分析目标不能为空"), 400, 40001, "goal: 分析目标不能为空"},
		{"validation blank", Validation(""), 400, 40001, "请求参数校验失败"},
		{"internal hides details", Internal("secret db password", nil), 500, 50000, "服务暂时不可用"},
		{"unknown", errors.New("boom"), 500, 50000, "服务暂时不可用"},
		{"wrapped business", fmt.Errorf("ctx: %w", Business(CodeConflict, "x")), 409, 40900, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, res := ToHTTP(tt.err)
			if status != tt.status || res.Code != tt.code || res.Message != tt.message || res.Data != nil {
				t.Fatalf("got %d %+v", status, res)
			}
		})
	}
	if CodeInternalError.Status != http.StatusInternalServerError {
		t.Fatal("status constants")
	}
}

func TestIsPermanentFailure(t *testing.T) {
	deep := Internal("a", Internal("b", NotFound("gone")))
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"invalid argument", InvalidArgument("x"), true},
		{"forbidden", Forbidden("x"), true},
		{"not found", NotFound("x"), true},
		{"nested cause", deep, true},
		{"wrapped via fmt", fmt.Errorf("wrap: %w", InvalidArgument("x")), true},
		{"internal only", Internal("x", errors.New("io timeout")), false},
		{"budget is not permanent", BudgetExhausted("x", nil), false},
		{"context not ready", NotReady(), false},
		{"plain error", errors.New("x"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		if got := IsPermanentFailure(tt.err); got != tt.want {
			t.Errorf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
	// the chain walk is bounded at 16 causes
	var chain error = InvalidArgument("deep")
	for i := 0; i < 16; i++ {
		chain = Internal("layer", chain)
	}
	if IsPermanentFailure(chain) {
		t.Fatal("cause beyond depth 16 must not be inspected")
	}
}

func TestCodeOfAndRoot(t *testing.T) {
	root := errors.New("leaf")
	err := Internal("top", fmt.Errorf("mid: %w", root))
	if RootCause(err) != root {
		t.Fatal("root cause")
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"internal", err, "INTERNAL"},
		{"invalid argument", InvalidArgument(""), "INVALID_ARGUMENT"},
		{"budget", BudgetExhausted("", nil), "BUDGET_EXHAUSTED"},
		{"not ready", NotReady(), "NOT_READY"},
		{"innermost wins", Internal("analysis failed", Transient("agent unavailable", nil)), "TRANSIENT"},
		{"deadline", Internal("x", context.DeadlineExceeded), "DEADLINE_EXCEEDED"},
		{"plain", root, "INTERNAL"},
		{"nil", nil, "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := CodeOf(tt.err); got != tt.want {
			t.Errorf("%s: got %s want %s", tt.name, got, tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	err := Internal("analysis failed", Transient("agent unavailable", fmt.Errorf("dial: %w", errors.New("refused"))))
	if got := Describe(err); got != "analysis failed: agent unavailable: dial: refused" {
		t.Fatal(got)
	}
	if Describe(nil) != "" || Describe(errors.New("x")) != "x" {
		t.Fatal("trivial chains")
	}
}
