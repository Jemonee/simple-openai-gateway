package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
	"github.com/Jemonee/simple-openai-gateway/internal/gateway"

	"github.com/gin-gonic/gin"
)

type OpenAIRelayApi struct {
	access        *gateway.ClientAccessService
	relay         *gateway.RelayService
	configManager *config.ApplicationConfigManager
}

type openAIErrorEnvelope struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

type openAIModelList struct {
	Object string            `json:"object"`
	Data   []openAIModelView `json:"data"`
}

type openAIModelView struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func NewOpenAIRelayApi(access *gateway.ClientAccessService, relay *gateway.RelayService, configManager *config.ApplicationConfigManager) *OpenAIRelayApi {
	return &OpenAIRelayApi{access: access, relay: relay, configManager: configManager}
}

func (a *OpenAIRelayApi) RegisterRoot(router *gin.Engine) {
	v1 := router.Group("/v1")
	v1.GET("/models", a.models)
	v1.POST("/chat/completions", a.chatCompletions)
	v1.POST("/responses", a.responses)
}

func (a *OpenAIRelayApi) models(c *gin.Context) {
	token, release, publicErr := a.authorize(c)
	if publicErr != nil {
		writeOpenAIError(c, publicErr)
		return
	}
	defer release()
	models, err := a.access.ListModels(c.Request.Context(), token)
	if err != nil {
		writeOpenAIError(c, &gateway.PublicError{Status: http.StatusInternalServerError, Message: "The gateway could not list models.", Type: "api_error", Code: "gateway_error"})
		return
	}
	data := make([]openAIModelView, 0, len(models))
	for _, model := range models {
		created := model.CreatedAt.Unix()
		if model.CreatedAt.IsZero() {
			created = time.Now().Unix()
		}
		data = append(data, openAIModelView{ID: model.Name, Object: "model", Created: created, OwnedBy: "gateway"})
	}
	a.access.Touch(contextWithoutCancel(c), token.ID)
	c.JSON(http.StatusOK, openAIModelList{Object: "list", Data: data})
}

func (a *OpenAIRelayApi) chatCompletions(c *gin.Context) {
	a.handleRelay(c, "chat")
}

func (a *OpenAIRelayApi) responses(c *gin.Context) {
	a.handleRelay(c, "responses")
}

func (a *OpenAIRelayApi) handleRelay(c *gin.Context, endpoint string) {
	trace := gateway.NewRelayTrace()
	c.Header("X-Request-Id", trace.RequestID())
	authorizeStarted := time.Now()
	token, release, publicErr := a.authorize(c)
	trace.Record(gateway.RelayStageAccessControl, gateway.RelayStepCategoryGateway, 0, authorizeStarted, publicErr, "")
	if publicErr != nil {
		writeOpenAIError(c, publicErr)
		trace.LogCompletion(publicErr.Status, publicErr.Code, nil)
		return
	}
	defer release()

	limit := int64(32 << 20)
	if cfg := a.configManager.GetConfig(); cfg != nil && cfg.GatewayConfig.RequestBodyLimitMB > 0 {
		limit = int64(cfg.GatewayConfig.RequestBodyLimitMB) << 20
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
	bodyReadStarted := time.Now()
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			publicErr = &gateway.PublicError{Status: http.StatusRequestEntityTooLarge, Message: "The request body is too large.", Type: "invalid_request_error", Code: "request_too_large"}
			trace.Record(gateway.RelayStageRequestBodyRead, gateway.RelayStepCategoryGateway, 0, bodyReadStarted, err, "request_too_large")
			writeOpenAIError(c, publicErr)
			trace.LogCompletion(publicErr.Status, publicErr.Code, nil)
			return
		}
		publicErr = &gateway.PublicError{Status: http.StatusBadRequest, Message: "The request body could not be read.", Type: "invalid_request_error", Code: "invalid_request"}
		trace.Record(gateway.RelayStageRequestBodyRead, gateway.RelayStepCategoryGateway, 0, bodyReadStarted, err, "body_read_failed")
		writeOpenAIError(c, publicErr)
		trace.LogCompletion(publicErr.Status, publicErr.Code, nil)
		return
	}
	trace.Record(gateway.RelayStageRequestBodyRead, gateway.RelayStepCategoryGateway, 0, bodyReadStarted, nil, "bytes="+strconv.Itoa(len(body)))
	payloadParseStarted := time.Now()
	payload, err := gateway.ParseRelayPayload(body)
	if err != nil {
		publicErr = &gateway.PublicError{Status: http.StatusBadRequest, Message: err.Error(), Type: "invalid_request_error", Code: "invalid_request"}
		trace.Record(gateway.RelayStagePayloadParse, gateway.RelayStepCategoryGateway, 0, payloadParseStarted, err, "invalid_payload")
		writeOpenAIError(c, publicErr)
		trace.LogCompletion(publicErr.Status, publicErr.Code, nil)
		return
	}
	trace.Record(gateway.RelayStagePayloadParse, gateway.RelayStepCategoryGateway, 0, payloadParseStarted, nil, "endpoint="+endpoint)
	publicErr = a.relay.RelayWithTrace(c.Request.Context(), c.Writer, c.Request.Header, c.Request.URL.RawQuery, endpoint, token, payload, body, trace)
	if publicErr != nil && !c.Writer.Written() {
		writeOpenAIError(c, publicErr)
	}
}

func (a *OpenAIRelayApi) authorize(c *gin.Context) (*gateway.ClientToken, func(), *gateway.PublicError) {
	token, err := a.access.Authenticate(c.Request.Context(), c.GetHeader("Authorization"))
	if err != nil {
		return nil, nil, &gateway.PublicError{Status: http.StatusUnauthorized, Message: "Incorrect API key provided.", Type: "invalid_request_error", Code: "invalid_api_key"}
	}
	release, err := a.access.Acquire(token)
	if err != nil {
		code := "rate_limit_exceeded"
		message := "Rate limit reached for this API token."
		if errors.Is(err, gateway.ErrConcurrencyLimit) {
			code = "concurrency_limit_exceeded"
			message = "The API token concurrency limit has been reached."
		}
		return nil, nil, &gateway.PublicError{Status: http.StatusTooManyRequests, Message: message, Type: "rate_limit_error", Code: code}
	}
	return token, release, nil
}

func writeOpenAIError(c *gin.Context, publicErr *gateway.PublicError) {
	c.Header("Content-Type", "application/json")
	c.JSON(publicErr.Status, openAIErrorEnvelope{Error: openAIError{
		Message: publicErr.Message,
		Type:    publicErr.Type,
		Code:    publicErr.Code,
	}})
}

func contextWithoutCancel(c *gin.Context) context.Context {
	return context.WithoutCancel(c.Request.Context())
}
