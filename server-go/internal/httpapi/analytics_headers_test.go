package httpapi

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
