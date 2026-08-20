package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GatewayManagementApi struct {
	pkgApi.BaseApi
	management   *gateway.ManagementService
	oauth        *gateway.ChatGPTOAuthService
	verification *gateway.VerificationService
	security     *AdminSecurity
	access       *gateway.ClientAccessService
	estimator    *gateway.TokenEstimator
}

type channelTestView struct {
	LatencyMS int64 `json:"latencyMs"`
	Status    int   `json:"status"`
}

type tokenCountInput struct {
	Texts []string `json:"texts"`
}

type tokenCountView struct {
	Counts []int64 `json:"counts"`
}

type availableModelView struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

func NewGatewayManagementApi(management *gateway.ManagementService, oauth *gateway.ChatGPTOAuthService, verification *gateway.VerificationService, security *AdminSecurity, access *gateway.ClientAccessService, estimator *gateway.TokenEstimator) *GatewayManagementApi {
	return &GatewayManagementApi{management: management, oauth: oauth, verification: verification, security: security, access: access, estimator: estimator}
}

func (a *GatewayManagementApi) Register(router *gin.RouterGroup) {
	console := router.Group("/admin/gateway")
	console.Use(a.security.RequireConsole, a.security.VerifyOrigin)
	console.GET("/dashboard", a.dashboard)
	console.GET("/logs", a.logs)
	console.GET("/logs/:requestId", a.logDetail)
	console.GET("/sessions", a.sessions)
	console.GET("/active-sessions", a.activeSessions)
	console.GET("/sessions/detail", a.sessionDetail)
	console.PUT("/sessions/title", a.renameSession)
	console.GET("/models/available", a.availableModels)
	console.POST("/chatgpt/oauth/start", a.startChatGPTOAuth)
	console.POST("/chatgpt/oauth/exchange", a.exchangeChatGPTOAuth)
	console.GET("/channels", a.listChannels)
	console.POST("/channels", a.createChannel)
	console.POST("/channels/configuration", a.saveChannelConfiguration)
	console.PUT("/channels/:id", a.updateChannel)
	console.DELETE("/channels/:id", a.deleteChannel)
	console.POST("/channels/:id/reset-circuit", a.resetChannelCircuit)
	console.PUT("/channels/:id/models", a.replaceChannelModels)
	console.POST("/channels/discover-models", a.discoverChannelModels)
	console.POST("/channels/:id/test", a.testChannel)
	console.GET("/models", a.listModels)
	console.GET("/tokens", a.listTokens)
	console.POST("/tokens", a.createToken)
	console.PUT("/tokens/:id", a.updateToken)
	console.POST("/tokens/:id/rotate", a.rotateToken)
	console.DELETE("/tokens/:id", a.deleteToken)

	admin := console.Group("")
	admin.Use(a.security.RequireAdmin)
	admin.GET("/logs/storage", a.logStorage)
	admin.POST("/logs/clear-payloads", a.clearLogPayloads)
	admin.POST("/logs/delete", a.deleteLogs)
	admin.POST("/token-counts", a.tokenCounts)
	admin.GET("/circuit-records", a.circuitRecords)
	admin.POST("/circuit-records/:id/reopen-mapping", a.reopenCircuitMapping)
	admin.GET("/resources", a.resources)
	admin.POST("/resources/cleanup", a.cleanupResources)
	admin.GET("/resources/:id", a.resourceDetail)
	admin.PUT("/resources/:id/sensitive", a.setResourceSensitive)
	admin.GET("/codex-config", a.codexConfiguration)
	admin.PUT("/codex-config", a.saveCodexConfiguration)

	admin.GET("/verifications", a.verificationReports)
	admin.GET("/verification-sessions", a.verificationSessions)
	admin.GET("/verification-sessions/:id", a.verificationSession)
	admin.POST("/verification-sessions/:id/stop", a.stopVerificationSession)
	admin.GET("/verifications/:id", a.verificationReport)
	admin.POST("/verifications", a.verifyChannel)

	admin.POST("/models", a.createModel)
	admin.PUT("/models/:id", a.updateModel)
	admin.DELETE("/models/:id", a.deleteModel)

}

func (a *GatewayManagementApi) startChatGPTOAuth(c *gin.Context) {
	if a.oauth == nil {
		managementError(c, errors.New("ChatGPT OAuth is unavailable"))
		return
	}
	var input gateway.ChatGPTOAuthStartInput
	if !bindManagementJSON(c, &input) {
		return
	}
	view, err := a.oauth.Start(input)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(view))
}

func (a *GatewayManagementApi) exchangeChatGPTOAuth(c *gin.Context) {
	if a.oauth == nil {
		managementError(c, errors.New("ChatGPT OAuth is unavailable"))
		return
	}
	var input gateway.ChatGPTOAuthExchangeInput
	if !bindManagementJSON(c, &input) {
		return
	}
	credential, err := a.oauth.Exchange(c.Request.Context(), input)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(credential))
}

func consoleTokenID(c *gin.Context) uint64 {
	token, ok := currentAccessToken(c)
	if !ok {
		return 0
	}
	return token.ID
}

func consoleUserScope(c *gin.Context) uint64 {
	principal, ok := currentConsole(c)
	if !ok || principal.IsAdmin() {
		return 0
	}
	return principal.UserID()
}

func (a *GatewayManagementApi) consoleScopeTokenIDs(c *gin.Context) ([]uint64, error) {
	if tokenID := consoleTokenID(c); tokenID > 0 {
		return []uint64{tokenID}, nil
	}
	if userID := consoleUserScope(c); userID > 0 {
		return a.management.TokenIDsForUser(c.Request.Context(), userID)
	}
	return nil, nil
}

func containsTokenID(ids []uint64, id uint64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func (a *GatewayManagementApi) availableModels(c *gin.Context) {
	principal, ok := currentConsole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
		return
	}
	var models []gateway.GatewayModel
	var err error
	if principal.AccessToken != nil {
		models, err = a.access.ListModels(c.Request.Context(), principal.AccessToken)
	} else {
		views, listErr := a.management.ListModels(c.Request.Context(), consoleUserScope(c))
		err = listErr
		for _, view := range views {
			models = append(models, view.GatewayModel)
		}
	}
	if err != nil {
		managementError(c, err)
		return
	}
	items := make([]availableModelView, 0, len(models))
	for _, model := range models {
		items = append(items, availableModelView{ID: model.ID, Name: model.Name})
	}
	c.JSON(http.StatusOK, common.S(&items))
}

func (a *GatewayManagementApi) verifyChannel(c *gin.Context) {
	var input gateway.VerificationRequest
	if !bindManagementJSON(c, &input) {
		return
	}
	session, err := a.verification.Start(c.Request.Context(), input)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, common.S(session))
}

func (a *GatewayManagementApi) stopVerificationSession(c *gin.Context) {
	if strings.TrimSpace(c.Param("id")) == "" {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "验证会话 ID 无效"))
		return
	}
	session, err := a.verification.Stop(c.Request.Context(), c.Param("id"))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(session))
}

func (a *GatewayManagementApi) verificationReports(c *gin.Context) {
	query := gateway.VerificationReportQuery{}
	query.ChannelID, _ = strconv.ParseUint(c.Query("channelId"), 10, 64)
	query.ModelID, _ = strconv.ParseUint(c.Query("modelId"), 10, 64)
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	page, err := a.verification.ListReports(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(page))
}

func (a *GatewayManagementApi) verificationSessions(c *gin.Context) {
	query := gateway.VerificationSessionQuery{}
	query.ChannelID, _ = strconv.ParseUint(c.Query("channelId"), 10, 64)
	query.ModelID, _ = strconv.ParseUint(c.Query("modelId"), 10, 64)
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	page, err := a.verification.ListSessions(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(page))
}

func (a *GatewayManagementApi) verificationSession(c *gin.Context) {
	if strings.TrimSpace(c.Param("id")) == "" {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "验证会话 ID 无效"))
		return
	}
	detail, err := a.verification.Session(c.Request.Context(), c.Param("id"))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(detail))
}

func (a *GatewayManagementApi) verificationReport(c *gin.Context) {
	if strings.TrimSpace(c.Param("id")) == "" {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "验证报告 ID 无效"))
		return
	}
	report, err := a.verification.Report(c.Request.Context(), c.Param("id"))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(report))
}

func (a *GatewayManagementApi) codexConfiguration(c *gin.Context) {
	item, err := a.management.CodexConfiguration(c.Request.Context())
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) saveCodexConfiguration(c *gin.Context) {
	var input gateway.CodexConfigurationInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.SaveCodexConfiguration(c.Request.Context(), input)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) tokenCounts(c *gin.Context) {
	var input tokenCountInput
	if !bindManagementJSON(c, &input) {
		return
	}
	if len(input.Texts) > 512 {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "单次最多计算 512 段文本"))
		return
	}
	totalBytes := 0
	for _, value := range input.Texts {
		totalBytes += len(value)
		if totalBytes > 4<<20 {
			c.JSON(http.StatusRequestEntityTooLarge, common.F[any](http.StatusRequestEntityTooLarge, "待计算文本总大小不能超过 4 MiB"))
			return
		}
	}
	counts := make([]int64, len(input.Texts))
	for index, value := range input.Texts {
		counts[index] = a.estimator.EstimateText(value)
	}
	c.JSON(http.StatusOK, common.S(&tokenCountView{Counts: counts}))
}

func parseID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "资源 ID 无效"))
		return 0, false
	}
	return id, true
}

func managementError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status = http.StatusNotFound
	}
	if err == nil {
		status = http.StatusInternalServerError
		err = errors.New("unknown gateway management error")
	}
	c.JSON(status, common.F[any](status, err.Error()))
}

func bindManagementJSON(c *gin.Context, value any) bool {
	if err := c.ShouldBindJSON(value); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "请求参数无效: "+err.Error()))
		return false
	}
	return true
}

func (a *GatewayManagementApi) listChannels(c *gin.Context) {
	items, err := a.management.ListChannels(c.Request.Context(), consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(&items))
}

func (a *GatewayManagementApi) createChannel(c *gin.Context) {
	var input gateway.ChannelInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.CreateChannel(c.Request.Context(), input, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusCreated, common.S(item))
}

func (a *GatewayManagementApi) saveChannelConfiguration(c *gin.Context) {
	var input gateway.ChannelConfigurationInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.SaveChannelConfiguration(c.Request.Context(), input, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) updateChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input gateway.ChannelInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.UpdateChannel(c.Request.Context(), id, input, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) deleteChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := a.management.DeleteChannel(c.Request.Context(), id, consoleUserScope(c)); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *GatewayManagementApi) resetChannelCircuit(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := a.management.ResetChannelCircuit(c.Request.Context(), id, consoleUserScope(c)); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *GatewayManagementApi) circuitRecords(c *gin.Context) {
	query := gateway.CircuitRecordQuery{Status: c.Query("status")}
	query.ChannelID, _ = strconv.ParseUint(c.Query("channelId"), 10, 64)
	query.Level, _ = strconv.Atoi(c.Query("level"))
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	page, err := a.management.CircuitRecords(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(page))
}

func (a *GatewayManagementApi) reopenCircuitMapping(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := a.management.ReopenCircuitMapping(c.Request.Context(), id); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *GatewayManagementApi) replaceChannelModels(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var inputs []gateway.ChannelModelInput
	if !bindManagementJSON(c, &inputs) {
		return
	}
	items, err := a.management.ReplaceChannelModels(c.Request.Context(), id, inputs, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(&items))
}

func (a *GatewayManagementApi) discoverChannelModels(c *gin.Context) {
	var input gateway.ChannelModelDiscoveryInput
	if !bindManagementJSON(c, &input) {
		return
	}
	result, err := a.management.DiscoverChannelModels(c.Request.Context(), input, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(result))
}

func (a *GatewayManagementApi) testChannel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	latency, status, err := a.management.TestChannel(c.Request.Context(), id, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	view := channelTestView{LatencyMS: latency, Status: status}
	c.JSON(http.StatusOK, common.S(&view))
}

func (a *GatewayManagementApi) listModels(c *gin.Context) {
	items, err := a.management.ListModels(c.Request.Context(), consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(&items))
}

func (a *GatewayManagementApi) createModel(c *gin.Context) {
	var input gateway.GatewayModelInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.CreateModel(c.Request.Context(), input)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusCreated, common.S(item))
}

func (a *GatewayManagementApi) updateModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input gateway.GatewayModelInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.UpdateModel(c.Request.Context(), id, input)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) deleteModel(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := a.management.DeleteModel(c.Request.Context(), id); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *GatewayManagementApi) listTokens(c *gin.Context) {
	items, err := a.management.ListTokens(c.Request.Context(), consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(&items))
}

func (a *GatewayManagementApi) createToken(c *gin.Context) {
	var input gateway.ClientTokenInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.CreateToken(c.Request.Context(), input, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusCreated, common.S(item))
}

func (a *GatewayManagementApi) updateToken(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input gateway.ClientTokenInput
	if !bindManagementJSON(c, &input) {
		return
	}
	item, err := a.management.UpdateToken(c.Request.Context(), id, input, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) rotateToken(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	item, err := a.management.RotateToken(c.Request.Context(), id, consoleUserScope(c))
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) deleteToken(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if err := a.management.DeleteToken(c.Request.Context(), id, consoleUserScope(c)); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *GatewayManagementApi) dashboard(c *gin.Context) {
	days := 1
	if value := c.Query("days"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "统计时间范围无效"))
			return
		}
		days = parsed
	}
	tokenIDs, err := a.consoleScopeTokenIDs(c)
	if err != nil {
		managementError(c, err)
		return
	}
	item, err := a.management.Dashboard(c.Request.Context(), days, tokenIDs...)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) logs(c *gin.Context) {
	query := gateway.LogQuery{
		Model:   c.Query("model"),
		Outcome: c.Query("outcome"),
	}
	query.StatusCode, _ = strconv.Atoi(c.Query("status"))
	query.TokenID, _ = strconv.ParseUint(c.Query("tokenId"), 10, 64)
	query.ChannelID, _ = strconv.ParseUint(c.Query("channelId"), 10, 64)
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	if value := c.Query("from"); value != "" {
		query.From, _ = time.Parse(time.RFC3339, value)
		query.From = query.From.UTC()
	}
	if value := c.Query("to"); value != "" {
		query.To, _ = time.Parse(time.RFC3339, value)
		query.To = query.To.UTC()
	}
	tokenIDs, scopeErr := a.consoleScopeTokenIDs(c)
	if scopeErr != nil {
		managementError(c, scopeErr)
		return
	}
	if len(tokenIDs) == 1 {
		query.TokenID = tokenIDs[0]
		query.ChannelID = 0
	} else if len(tokenIDs) > 1 {
		query.TokenIDs = tokenIDs
		query.TokenID = 0
		query.ChannelID = 0
	}
	item, err := a.management.Logs(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	if len(tokenIDs) > 0 {
		gateway.RedactTokenScopedLogPage(item)
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) logStorage(c *gin.Context) {
	result, err := a.management.LogStorageUsage(c.Request.Context())
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(result))
}

func (a *GatewayManagementApi) clearLogPayloads(c *gin.Context) {
	var input gateway.LogCleanupInput
	if !bindManagementJSON(c, &input) {
		return
	}
	result, err := a.management.ClearHistoricalLogPayloadsBefore(c.Request.Context(), input.Days)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(result))
}

func (a *GatewayManagementApi) deleteLogs(c *gin.Context) {
	var input gateway.LogCleanupInput
	if !bindManagementJSON(c, &input) {
		return
	}
	result, err := a.management.DeleteHistoricalLogsBefore(c.Request.Context(), input.Days)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(result))
}

func (a *GatewayManagementApi) logDetail(c *gin.Context) {
	tokenIDs, scopeErr := a.consoleScopeTokenIDs(c)
	if scopeErr != nil {
		managementError(c, scopeErr)
		return
	}
	item, err := a.management.LogDetail(c.Request.Context(), c.Param("requestId"), tokenIDs...)
	if err != nil {
		managementError(c, err)
		return
	}
	if len(tokenIDs) > 0 {
		gateway.RedactTokenScopedLogView(item)
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) sessions(c *gin.Context) {
	query := gateway.SessionLogQuery{
		Session: c.Query("session"),
		Model:   c.Query("model"),
	}
	query.TokenID, _ = strconv.ParseUint(c.Query("tokenId"), 10, 64)
	query.ChannelID, _ = strconv.ParseUint(c.Query("channelId"), 10, 64)
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	if value := c.Query("from"); value != "" {
		query.From, _ = time.Parse(time.RFC3339, value)
		query.From = query.From.UTC()
	}
	if value := c.Query("to"); value != "" {
		query.To, _ = time.Parse(time.RFC3339, value)
		query.To = query.To.UTC()
	}
	tokenIDs, scopeErr := a.consoleScopeTokenIDs(c)
	if scopeErr != nil {
		managementError(c, scopeErr)
		return
	}
	if len(tokenIDs) == 1 {
		query.TokenID = tokenIDs[0]
		query.ChannelID = 0
	} else if len(tokenIDs) > 1 {
		query.TokenIDs = tokenIDs
		query.TokenID = 0
		query.ChannelID = 0
	}
	item, err := a.management.SessionLogs(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	if len(tokenIDs) > 0 {
		gateway.RedactTokenScopedSessionPage(item)
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) activeSessions(c *gin.Context) {
	query := gateway.ActiveSessionQuery{
		Session:     c.Query("session"),
		Sources:     c.QueryArray("source"),
		ActiveSince: time.Now().UTC().Add(-30 * time.Minute),
	}
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	tokenIDs, scopeErr := a.consoleScopeTokenIDs(c)
	if scopeErr != nil {
		managementError(c, scopeErr)
		return
	}
	if len(tokenIDs) == 1 {
		query.TokenID = tokenIDs[0]
	} else if len(tokenIDs) > 1 {
		query.TokenIDs = tokenIDs
	}
	item, err := a.management.ActiveSessions(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	if len(tokenIDs) > 0 {
		gateway.RedactTokenScopedActiveSessionPage(item)
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) sessionDetail(c *gin.Context) {
	query := gateway.SessionDetailQuery{
		SessionID: c.Query("sessionId"),
		RequestID: c.Query("requestId"),
		Status:    c.Query("status"),
	}
	query.TokenID, _ = strconv.ParseUint(c.Query("tokenId"), 10, 64)
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	tokenIDs, scopeErr := a.consoleScopeTokenIDs(c)
	if scopeErr != nil {
		managementError(c, scopeErr)
		return
	}
	if len(tokenIDs) == 1 {
		query.TokenID = tokenIDs[0]
	} else if len(tokenIDs) > 1 {
		if query.TokenID == 0 || !containsTokenID(tokenIDs, query.TokenID) {
			c.JSON(http.StatusNotFound, common.F[any](http.StatusNotFound, "会话不存在"))
			return
		}
	}
	item, err := a.management.SessionLogDetail(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	if len(tokenIDs) > 0 {
		gateway.RedactTokenScopedSessionDetail(item)
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) renameSession(c *gin.Context) {
	var input gateway.SessionTitleInput
	if !bindManagementJSON(c, &input) {
		return
	}
	tokenIDs, scopeErr := a.consoleScopeTokenIDs(c)
	if scopeErr != nil {
		managementError(c, scopeErr)
		return
	}
	if len(tokenIDs) == 1 {
		input.TokenID = tokenIDs[0]
	} else if len(tokenIDs) > 1 && !containsTokenID(tokenIDs, input.TokenID) {
		c.JSON(http.StatusNotFound, common.F[any](http.StatusNotFound, "会话不存在"))
		return
	}
	if err := a.management.RenameSession(c.Request.Context(), input); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}

func (a *GatewayManagementApi) resources(c *gin.Context) {
	query := gateway.ResourceQuery{Search: c.Query("search"), Kind: c.Query("kind"), Page: 1, PageSize: 50}
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	if value := c.Query("sensitive"); value != "" {
		parsed := value == "true"
		query.Sensitive = &parsed
	}
	if value := c.Query("lastSeenBeforeDays"); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil || days <= 0 || days > 36500 {
			c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, "最后发现时间天数必须为正整数"))
			return
		}
		cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
		query.LastSeenBefore = &cutoff
	}
	item, err := a.management.Resources(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) cleanupResources(c *gin.Context) {
	var input gateway.ResourceCleanupInput
	if !bindManagementJSON(c, &input) {
		return
	}
	result, err := a.management.CleanupResourcesBefore(c.Request.Context(), input.Days, input.Kind)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(result))
}

func (a *GatewayManagementApi) resourceDetail(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	item, err := a.management.ResourceDetail(c.Request.Context(), id)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) setResourceSensitive(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		Sensitive bool `json:"sensitive"`
	}
	if !bindManagementJSON(c, &input) {
		return
	}
	if err := a.management.SetResourceSensitive(c.Request.Context(), id, input.Sensitive); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}
