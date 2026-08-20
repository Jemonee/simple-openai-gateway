package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

type tokenLoginRequest struct {
	AccessToken string `json:"accessToken" binding:"required"`
}

type adminPasswordRequest struct {
	CurrentPassword string `json:"currentPassword" binding:"required"`
	NewPassword     string `json:"newPassword" binding:"required"`
}

type adminCreateUserRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role"`
}

type adminResetPasswordRequest struct {
	NewPassword string `json:"newPassword" binding:"required"`
}

type adminSessionView struct {
	ID        uint64 `json:"id"`
	Username  string `json:"username"`
	LoginType string `json:"loginType"`
	Role      string `json:"role"`
	TokenID   uint64 `json:"tokenId,omitempty"`
}

func NewAdminApi(auth *gateway.AdminAuthService, security *AdminSecurity) *AdminApi {
	return &AdminApi{auth: auth, security: security}
}

func (a *AdminApi) Register(router *gin.RouterGroup) {
	auth := router.Group("/admin/auth")
	auth.Use(a.security.VerifyOrigin)
	auth.POST("/login", a.login)
	auth.POST("/token-login", a.tokenLogin)
	auth.POST("/logout", a.security.RequireConsole, a.logout)
	auth.GET("/session", a.security.RequireConsole, a.session)
	auth.PUT("/password", a.security.RequireAdmin, a.changePassword)
	admin := auth.Group("/users")
	admin.Use(a.security.RequireAdmin)
	admin.GET("", a.listUsers)
	admin.POST("", a.createUser)
	admin.POST("/:id/reset-password", a.resetUserPassword)
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
	c.JSON(http.StatusOK, common.S(adminView(&gateway.ConsolePrincipal{LoginType: gateway.ConsoleLoginAdmin, Admin: user})))
}

func (a *AdminApi) tokenLogin(c *gin.Context) {
	if !a.security.allowLogin(c.ClientIP()) {
		c.JSON(http.StatusTooManyRequests, common.F[any](http.StatusTooManyRequests, "登录尝试过多，请稍后再试"))
		return
	}
	var request tokenLoginRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "访问令牌不能为空"))
		return
	}
	rawToken, token, err := a.auth.LoginWithAccessToken(strings.TrimSpace(request.AccessToken))
	if err != nil {
		a.security.recordLoginFailure(c.ClientIP())
		status := http.StatusUnauthorized
		if !errors.Is(err, gateway.ErrInvalidCredentials) {
			status = http.StatusInternalServerError
		}
		c.JSON(status, common.F[any](status, "访问令牌无效或已停用"))
		return
	}
	a.security.clearLoginFailures(c.ClientIP())
	a.security.setSessionCookie(c, rawToken)
	c.JSON(http.StatusOK, common.S(adminView(&gateway.ConsolePrincipal{LoginType: gateway.ConsoleLoginAccessToken, AccessToken: token})))
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
	principal, ok := currentConsole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
		return
	}
	c.JSON(http.StatusOK, common.S(adminView(principal)))
}

func adminView(principal *gateway.ConsolePrincipal) *adminSessionView {
	if principal != nil && principal.AccessToken != nil {
		return &adminSessionView{
			ID:        principal.AccessToken.ID,
			Username:  principal.AccessToken.Name,
			LoginType: gateway.ConsoleLoginAccessToken,
			Role:      gateway.ConsoleRoleUser,
			TokenID:   principal.AccessToken.ID,
		}
	}
	if principal != nil && principal.Admin != nil {
		role := principal.Admin.Role
		if role == "" {
			role = gateway.ConsoleRoleAdmin
		}
		return &adminSessionView{ID: principal.Admin.ID, Username: principal.Admin.Username, LoginType: gateway.ConsoleLoginAdmin, Role: role}
	}
	return &adminSessionView{}
}

func (a *AdminApi) listUsers(c *gin.Context) {
	users, err := a.auth.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, common.F[any](http.StatusInternalServerError, err.Error()))
		return
	}
	c.JSON(http.StatusOK, common.S(&users))
}

func (a *AdminApi) createUser(c *gin.Context) {
	var input adminCreateUserRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "用户名和密码不能为空"))
		return
	}
	user, err := a.auth.CreateUser(input.Username, input.Password, input.Role)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			status = http.StatusConflict
		}
		c.JSON(status, common.F[any](status, err.Error()))
		return
	}
	c.JSON(http.StatusCreated, common.S(user))
}

func (a *AdminApi) resetUserPassword(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "账号 ID 无效"))
		return
	}
	var input adminResetPasswordRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "新密码不能为空"))
		return
	}
	if err := a.auth.ResetPassword(id, input.NewPassword); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, gateway.ErrInvalidSession) || errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusNotFound
		}
		c.JSON(status, common.F[any](status, err.Error()))
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
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
