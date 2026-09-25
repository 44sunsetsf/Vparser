package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"dovideo/server/internal/common"
)

func TestAttemptHeaderEscapesAndTrims(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	name := "  张三\r\nX-Evil: 1  "
	attemptHeader(c, &name)
	got := w.Header().Get("X-Vparser-Attempt")
	if got != "%E5%BC%A0%E4%B8%89%0D%0AX-Evil%3A+1" {
		t.Fatalf("got %q", got)
	}
	attemptHeader(c, nil) // no username: nothing to add, no panic
}

func TestSourceHeaderEscapesAndTrims(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	sourceHeader(c, " https://b23.tv/HhGD6bu?a=1&b=2\r\nX-Evil: 1 ")
	if got := w.Header().Get("X-Vparser-Source"); got != "https%3A%2F%2Fb23.tv%2FHhGD6bu%3Fa%3D1%26b%3D2%0D%0AX-Evil%3A+1" {
		t.Fatalf("got %q", got)
	}
}

func TestFailSetsCodeHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	fail(c, common.Business(common.CodeSourceBlocked, "refused"))
	if w.Code != 422 || w.Header().Get("X-Vparser-Code") != "42202" {
		t.Fatalf("status %d, code header %q", w.Code, w.Header().Get("X-Vparser-Code"))
	}
}
