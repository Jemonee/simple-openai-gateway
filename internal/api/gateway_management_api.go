package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GatewayManagementApi struct {
	pkgApi.BaseApi
	management *gateway.ManagementService
	security   *AdminSecurity
	estimator  *gateway.TokenEstimator
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

func NewGatewayManagementApi(management *gateway.ManagementService, security *AdminSecurity, estimator *gateway.TokenEstimator) *GatewayManagementApi {
	return &GatewayManagementApi{management: management, security: security, estimator: estimator}
}

func (a *GatewayManagementApi) Register(router *gin.RouterGroup) {
	admin := router.Group("/admin/gateway")
	admin.Use(a.security.RequireAdmin, a.security.VerifyOrigin)

	admin.GET("/dashboard", a.dashboard)
	admin.GET("/logs", a.logs)
	admin.GET("/logs/storage", a.logStorage)
	admin.POST("/logs/clear-payloads", a.clearLogPayloads)
	admin.GET("/logs/:requestId", a.logDetail)
	admin.POST("/token-counts", a.tokenCounts)
	admin.GET("/circuit-records", a.circuitRecords)
	admin.POST("/circuit-records/:id/reopen-mapping", a.reopenCircuitMapping)
	admin.GET("/sessions", a.sessions)
	admin.GET("/active-sessions", a.activeSessions)
	admin.GET("/sessions/detail", a.sessionDetail)
	admin.PUT("/sessions/title", a.renameSession)
	admin.GET("/codex-config", a.codexConfiguration)
	admin.PUT("/codex-config", a.saveCodexConfiguration)

	admin.GET("/channels", a.listChannels)
	admin.POST("/channels", a.createChannel)
	admin.POST("/channels/configuration", a.saveChannelConfiguration)
	admin.PUT("/channels/:id", a.updateChannel)
	admin.DELETE("/channels/:id", a.deleteChannel)
	admin.POST("/channels/:id/reset-circuit", a.resetChannelCircuit)
	admin.PUT("/channels/:id/models", a.replaceChannelModels)
	admin.POST("/channels/discover-models", a.discoverChannelModels)
	admin.POST("/channels/:id/test", a.testChannel)

	admin.GET("/models", a.listModels)
	admin.POST("/models", a.createModel)
	admin.PUT("/models/:id", a.updateModel)
	admin.DELETE("/models/:id", a.deleteModel)

	admin.GET("/tokens", a.listTokens)
	admin.POST("/tokens", a.createToken)
	admin.PUT("/tokens/:id", a.updateToken)
	admin.POST("/tokens/:id/rotate", a.rotateToken)
	admin.DELETE("/tokens/:id", a.deleteToken)
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
	items, err := a.management.ListChannels(c.Request.Context())
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
	item, err := a.management.CreateChannel(c.Request.Context(), input)
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
	item, err := a.management.SaveChannelConfiguration(c.Request.Context(), input)
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
	item, err := a.management.UpdateChannel(c.Request.Context(), id, input)
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
	if err := a.management.DeleteChannel(c.Request.Context(), id); err != nil {
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
	if err := a.management.ResetChannelCircuit(c.Request.Context(), id); err != nil {
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
	items, err := a.management.ReplaceChannelModels(c.Request.Context(), id, inputs)
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
	result, err := a.management.DiscoverChannelModels(c.Request.Context(), input)
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
	latency, status, err := a.management.TestChannel(c.Request.Context(), id)
	if err != nil {
		managementError(c, err)
		return
	}
	view := channelTestView{LatencyMS: latency, Status: status}
	c.JSON(http.StatusOK, common.S(&view))
}

func (a *GatewayManagementApi) listModels(c *gin.Context) {
	items, err := a.management.ListModels(c.Request.Context())
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
	items, err := a.management.ListTokens(c.Request.Context())
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
	item, err := a.management.CreateToken(c.Request.Context(), input)
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
	item, err := a.management.UpdateToken(c.Request.Context(), id, input)
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
	item, err := a.management.RotateToken(c.Request.Context(), id)
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
	if err := a.management.DeleteToken(c.Request.Context(), id); err != nil {
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
	item, err := a.management.Dashboard(c.Request.Context(), days)
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
	item, err := a.management.Logs(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
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
	result, err := a.management.ClearHistoricalLogPayloads(c.Request.Context())
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(result))
}

func (a *GatewayManagementApi) logDetail(c *gin.Context) {
	item, err := a.management.LogDetail(c.Request.Context(), c.Param("requestId"))
	if err != nil {
		managementError(c, err)
		return
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
	item, err := a.management.SessionLogs(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) activeSessions(c *gin.Context) {
	query := gateway.ActiveSessionQuery{
		Session:     c.Query("session"),
		ActiveSince: time.Now().UTC().Add(-30 * time.Minute),
	}
	query.Page, _ = strconv.Atoi(c.Query("page"))
	query.PageSize, _ = strconv.Atoi(c.Query("pageSize"))
	item, err := a.management.ActiveSessions(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
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
	item, err := a.management.SessionLogDetail(c.Request.Context(), query)
	if err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S(item))
}

func (a *GatewayManagementApi) renameSession(c *gin.Context) {
	var input gateway.SessionTitleInput
	if !bindManagementJSON(c, &input) {
		return
	}
	if err := a.management.RenameSession(c.Request.Context(), input); err != nil {
		managementError(c, err)
		return
	}
	c.JSON(http.StatusOK, common.S[any](nil))
}
