package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"

	"github.com/gin-gonic/gin"
)

type AdminApi struct {
	pkgApi.BaseApi
	auth     *gateway.AdminAuthService
	security *AdminSecurity
}

type adminLoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type adminPasswordRequest struct {
	CurrentPassword string `json:"currentPassword" binding:"required"`
	NewPassword     string `json:"newPassword" binding:"required"`
}

type adminSessionView struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
}

func NewAdminApi(auth *gateway.AdminAuthService, security *AdminSecurity) *AdminApi {
	return &AdminApi{auth: auth, security: security}
}

func (a *AdminApi) Register(router *gin.RouterGroup) {
	auth := router.Group("/admin/auth")
	auth.Use(a.security.VerifyOrigin)
	auth.POST("/login", a.login)
	auth.POST("/logout", a.security.RequireAdmin, a.logout)
	auth.GET("/session", a.security.RequireAdmin, a.session)
	auth.PUT("/password", a.security.RequireAdmin, a.changePassword)
}

func (a *AdminApi) login(c *gin.Context) {
	if !a.security.allowLogin(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, common.F[any](http.StatusTooManyRequests, "登录尝试过多，请稍后再试"))
		return
	}
	var request adminLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "用户名和密码不能为空"))
		return
	}
	rawToken, user, err := a.auth.Login(strings.TrimSpace(request.Username), request.Password)
	if err != nil {
		a.security.recordLoginFailure(c.ClientIP())
		status := http.StatusUnauthorized
		if !errors.Is(err, gateway.ErrInvalidCredentials) {
			status = http.StatusInternalServerError
		}
		c.JSON(status, common.F[any](status, "用户名或密码错误"))
		return
	}
	a.security.clearLoginFailures(c.ClientIP())
	a.security.setSessionCookie(c, rawToken)
	view := adminSessionView{ID: user.ID, Username: user.Username}
	c.JSON(http.StatusOK, common.S(&view))
}

func (a *AdminApi) logout(c *gin.Context) {
	rawToken, _ := c.Cookie(adminSessionCookie)
	if err := a.auth.Logout(rawToken); err != nil {
		c.JSON(http.StatusInternalServerError, common.F[any](http.StatusInternalServerError, "退出登录失败"))
		return
	}
	a.security.clearSessionCookie(c)
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *AdminApi) session(c *gin.Context) {
	user, ok := currentAdmin(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "管理员会话无效或已过期"))
		return
	}
	view := adminSessionView{ID: user.ID, Username: user.Username}
	c.JSON(http.StatusOK, common.S(&view))
}

func (a *AdminApi) changePassword(c *gin.Context) {
	user, ok := currentAdmin(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "管理员会话无效或已过期"))
		return
	}
	var request adminPasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "当前密码和新密码不能为空"))
		return
	}
	if err := a.auth.ChangePassword(user.ID, request.CurrentPassword, request.NewPassword); err != nil {
		status := http.StatusBadRequest
		if !errors.Is(err, gateway.ErrInvalidCredentials) && !strings.Contains(err.Error(), "at least 12") {
			status = http.StatusInternalServerError
		}
		c.JSON(status, common.F[any](status, err.Error()))
		return
	}
	a.security.clearSessionCookie(c)
	c.JSON(http.StatusOK, common.S[any](nil))
}
