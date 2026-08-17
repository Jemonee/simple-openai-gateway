package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"

	"github.com/gin-gonic/gin"
)

const adminSessionCookie = "gateway_admin_session"

type loginAttempt struct {
	Failures  int
	ExpiresAt time.Time
}

type AdminSecurity struct {
	auth          *gateway.AdminAuthService
	configManager *config.ApplicationConfigManager
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

func NewAdminSecurity(auth *gateway.AdminAuthService, configManager *config.ApplicationConfigManager) *AdminSecurity {
	return &AdminSecurity{
		auth:          auth,
		configManager: configManager,
		loginAttempts: make(map[string]loginAttempt),
	}
}

func (s *AdminSecurity) RequireConsole(c *gin.Context) {
	rawToken, err := c.Cookie(adminSessionCookie)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
		return
	}
	principal, err := s.auth.AuthenticateConsole(rawToken)
	if err != nil {
		s.clearSessionCookie(c)
		c.AbortWithStatusJSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
		return
	}
	c.Set("gatewayConsole", principal)
	if principal.IsAdmin() {
		c.Set("gatewayAdmin", principal.Admin)
	}
	s.refreshSessionCookie(c, rawToken)
	c.Next()
}

func (s *AdminSecurity) RequireAdmin(c *gin.Context) {
	principal, ok := currentConsole(c)
	if !ok {
		rawToken, err := c.Cookie(adminSessionCookie)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
			return
		}
		principal, err = s.auth.AuthenticateConsole(rawToken)
		if err != nil {
			s.clearSessionCookie(c)
			c.AbortWithStatusJSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
			return
		}
		c.Set("gatewayConsole", principal)
	}
	if !principal.IsAdmin() {
		c.AbortWithStatusJSON(http.StatusForbidden, common.F[any](http.StatusForbidden, "访问令牌登录无权访问此功能"))
		return
	}
	c.Set("gatewayAdmin", principal.Admin)
	if rawToken, err := c.Cookie(adminSessionCookie); err == nil {
		s.refreshSessionCookie(c, rawToken)
	}
	c.Next()
}

func (s *AdminSecurity) VerifyOrigin(c *gin.Context) {
	if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
		c.Next()
		return
	}
	origin := strings.TrimSpace(c.GetHeader("Origin"))
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || !sameOriginHost(parsed.Host, c.Request.Host) {
		c.AbortWithStatusJSON(http.StatusForbidden, common.F[any](http.StatusForbidden, "请求来源校验失败"))
		return
	}
	c.Next()
}

func sameOriginHost(originHost string, requestHost string) bool {
	if strings.EqualFold(originHost, requestHost) {
		return true
	}
	originName := hostName(originHost)
	requestName := hostName(requestHost)
	return originName != "" && requestName != "" && isLoopbackHost(originName) && isLoopbackHost(requestName)
}

func hostName(value string) string {
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(value, "[]")
}

func isLoopbackHost(value string) bool {
	if strings.EqualFold(value, "localhost") {
		return true
	}
	ip := net.ParseIP(value)
	return ip != nil && ip.IsLoopback()
}

func (s *AdminSecurity) allowLogin(clientIP string) bool {
	now := time.Now()
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempt, ok := s.loginAttempts[clientIP]
	if !ok || now.After(attempt.ExpiresAt) {
		delete(s.loginAttempts, clientIP)
		return true
	}
	return attempt.Failures < 8
}

func (s *AdminSecurity) recordLoginFailure(clientIP string) {
	now := time.Now()
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempt, ok := s.loginAttempts[clientIP]
	if !ok || now.After(attempt.ExpiresAt) {
		attempt = loginAttempt{ExpiresAt: now.Add(5 * time.Minute)}
	}
	attempt.Failures++
	s.loginAttempts[clientIP] = attempt
}

func (s *AdminSecurity) clearLoginFailures(clientIP string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, clientIP)
	s.loginMu.Unlock()
}

func (s *AdminSecurity) setSessionCookie(c *gin.Context, rawToken string) {
	maxAge := 10 * 365 * 24 * time.Hour
	secure := false
	if s.configManager != nil {
		if cfg := s.configManager.GetConfig(); cfg != nil {
			if hours := config.EffectiveSessionTTLHours(cfg); hours > 0 {
				maxAge = time.Duration(hours) * time.Hour
			}
			secure = cfg.GatewayConfig.SecureCookie
		}
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    rawToken,
		Path:     "/api",
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *AdminSecurity) refreshSessionCookie(c *gin.Context, rawToken string) {
	if strings.TrimSpace(rawToken) != "" {
		s.setSessionCookie(c, rawToken)
	}
}

func (s *AdminSecurity) clearSessionCookie(c *gin.Context) {
	secure := false
	if s.configManager != nil {
		if cfg := s.configManager.GetConfig(); cfg != nil {
			secure = cfg.GatewayConfig.SecureCookie
		}
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     adminSessionCookie,
		Path:     "/api",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func currentAdmin(c *gin.Context) (*gateway.AdminUser, bool) {
	value, ok := c.Get("gatewayAdmin")
	if !ok {
		return nil, false
	}
	user, ok := value.(*gateway.AdminUser)
	return user, ok
}

func currentConsole(c *gin.Context) (*gateway.ConsolePrincipal, bool) {
	value, ok := c.Get("gatewayConsole")
	if !ok {
		return nil, false
	}
	principal, ok := value.(*gateway.ConsolePrincipal)
	return principal, ok
}

func currentAccessToken(c *gin.Context) (*gateway.ClientToken, bool) {
	principal, ok := currentConsole(c)
	if !ok || principal.AccessToken == nil {
		return nil, false
	}
	return principal.AccessToken, true
}
