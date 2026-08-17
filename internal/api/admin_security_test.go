package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"github.com/Jemonee/simple-openai-gateway/internal/gateway"

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
	parsed := recorder.Result().Cookies()
	if len(parsed) != 1 || parsed[0].MaxAge < 10*365*24*60*60 {
		t.Fatalf("permanent session cookie max age = %+v", parsed)
	}
}

func TestRequireAdminRejectsAccessTokenPrincipal(t *testing.T) {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("gatewayConsole", &gateway.ConsolePrincipal{
		LoginType:   gateway.ConsoleLoginAccessToken,
		AccessToken: &gateway.ClientToken{ID: 7, Name: "limited", Enabled: true},
	})
	security := &AdminSecurity{}
	security.RequireAdmin(context)
	if recorder.Code != http.StatusForbidden || !context.IsAborted() {
		t.Fatalf("status = %d, aborted = %v", recorder.Code, context.IsAborted())
	}
}

func TestAdminSessionViewIncludesLoginType(t *testing.T) {
	admin := adminView(&gateway.ConsolePrincipal{LoginType: gateway.ConsoleLoginAdmin, Admin: &gateway.AdminUser{ID: 3, Username: "admin"}})
	if admin.LoginType != gateway.ConsoleLoginAdmin || admin.TokenID != 0 || admin.Username != "admin" {
		t.Fatalf("admin view = %+v", admin)
	}
	token := adminView(&gateway.ConsolePrincipal{LoginType: gateway.ConsoleLoginAccessToken, AccessToken: &gateway.ClientToken{ID: 9, Name: "reader"}})
	if token.LoginType != gateway.ConsoleLoginAccessToken || token.TokenID != 9 || token.Username != "reader" {
		t.Fatalf("token view = %+v", token)
	}
}
