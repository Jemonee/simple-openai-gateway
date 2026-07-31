package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jemonee/simple-openai-gateway/internal/config"

	"github.com/gin-gonic/gin"
)

func TestSameOriginHost(t *testing.T) {
	tests := []struct {
		origin  string
		request string
		want    bool
	}{
		{origin: "gateway.example.com", request: "gateway.example.com", want: true},
		{origin: "localhost:5173", request: "127.0.0.1:8888", want: true},
		{origin: "evil.example.com", request: "gateway.example.com", want: false},
	}
	for _, test := range tests {
		if got := sameOriginHost(test.origin, test.request); got != test.want {
			t.Errorf("sameOriginHost(%q, %q) = %v, want %v", test.origin, test.request, got, test.want)
		}
	}
}

func TestVerifyOriginRejectsCrossSiteMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "http://gateway.example.com/api/admin/auth/login", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://evil.example.com")
	context.Request = request
	security := &AdminSecurity{}
	security.VerifyOrigin(context)
	if recorder.Code != http.StatusForbidden || !context.IsAborted() {
		t.Fatalf("status = %d, aborted = %v", recorder.Code, context.IsAborted())
	}
}

func TestSessionCookieSecurityAttributes(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	security := &AdminSecurity{configManager: &config.ApplicationConfigManager{}}
	security.setSessionCookie(context, "session-secret")
	cookie := recorder.Header().Get("Set-Cookie")
	for _, attribute := range []string{"gateway_admin_session=session-secret", "Path=/api", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(cookie, attribute) {
			t.Errorf("Set-Cookie %q does not contain %q", cookie, attribute)
		}
	}
}
