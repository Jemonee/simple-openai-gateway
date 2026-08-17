package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestConfigureIFrameEmbedding(t *testing.T) {
	tests := []struct {
		name       string
		disabled   bool
		wantHeader string
	}{
		{name: "allowed by default"},
		{name: "disabled", disabled: true, wantHeader: "DENY"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			configureIFrameEmbedding(engine, test.disabled)
			engine.GET("/", func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if got := response.Header().Get("X-Frame-Options"); got != test.wantHeader {
				t.Fatalf("X-Frame-Options = %q, want %q", got, test.wantHeader)
			}
		})
	}
}
