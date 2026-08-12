package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Jemonee/simple-openai-gateway/internal/gateway"
	pkgApi "github.com/Jemonee/simple-openai-gateway/pkg/api"
	"github.com/Jemonee/simple-openai-gateway/pkg/common"

	"github.com/gin-gonic/gin"
)

const webChatTokenIDBase uint64 = 4_294_000_000

type WebChatApi struct {
	pkgApi.BaseApi
	relay      *gateway.RelayService
	management *gateway.ManagementService
	security   *AdminSecurity
}

type webChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type webChatRequest struct {
	SessionID string           `json:"sessionId"`
	Model     string           `json:"model"`
	ChannelID uint64           `json:"channelId"`
	Messages  []webChatMessage `json:"messages"`
}

type webChatResponse struct {
	RequestID           string `json:"requestId"`
	Content             string `json:"content"`
	Model               string `json:"model"`
	ChannelID           uint64 `json:"channelId"`
	ChannelName         string `json:"channelName"`
	UpstreamModel       string `json:"upstreamModel"`
	InputTokens         int64  `json:"inputTokens"`
	OutputTokens        int64  `json:"outputTokens"`
	CachedTokens        int64  `json:"cachedTokens"`
	EstimatedCostMicros int64  `json:"estimatedCostMicros"`
}

func NewWebChatApi(relay *gateway.RelayService, management *gateway.ManagementService, security *AdminSecurity) *WebChatApi {
	return &WebChatApi{relay: relay, management: management, security: security}
}

func (a *WebChatApi) Register(router *gin.RouterGroup) {
	admin := router.Group("/admin/gateway/web-chat")
	admin.Use(a.security.RequireAdmin, a.security.VerifyOrigin)
	admin.POST("/completions", a.complete)
}

func (a *WebChatApi) complete(c *gin.Context) {
	admin, ok := currentAdmin(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "管理员会话无效或已过期"))
		return
	}
	var input webChatRequest
	if !bindManagementJSON(c, &input) {
		return
	}
	if err := validateWebChatRequest(&input); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, err.Error()))
		return
	}

	body, err := json.Marshal(map[string]any{
		"model":    strings.TrimSpace(input.Model),
		"messages": input.Messages,
		"stream":   false,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, common.F[any](http.StatusInternalServerError, "网页对话请求构建失败"))
		return
	}
	payload, err := gateway.ParseRelayPayload(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, err.Error()))
		return
	}
	payload.ConfigureSession(input.SessionID, "web_chat", "web_chat", "user")
	payload.PinnedChannelID = input.ChannelID

	recorder := httptest.NewRecorder()
	headers := make(http.Header)
	headers.Set("X-Client-Name", "simple-openai-gateway-web-chat")
	token := &gateway.ClientToken{
		ID:             webChatTokenIDBase + admin.ID,
		Name:           "网页聊天 / " + admin.Username,
		KeyPrefix:      "web-chat",
		Enabled:        true,
		AllowAllModels: true,
		RPM:            0,
		MaxConcurrency: 0,
	}
	publicErr := a.relay.Relay(c.Request.Context(), recorder, headers, "", "chat", token, payload, body)
	if publicErr != nil {
		c.JSON(publicErr.Status, common.F[any](publicErr.Status, publicErr.Message))
		return
	}
	if recorder.Code < http.StatusOK || recorder.Code >= http.StatusMultipleChoices {
		message := upstreamChatError(recorder.Body.Bytes())
		if message == "" {
			message = "上游模型返回错误"
		}
		c.JSON(recorder.Code, common.F[any](recorder.Code, message))
		return
	}
	content, err := chatCompletionContent(recorder.Body.Bytes())
	if err != nil {
		c.JSON(http.StatusBadGateway, common.F[any](http.StatusBadGateway, err.Error()))
		return
	}

	result := webChatResponse{
		RequestID: recorder.Header().Get("X-Request-Id"),
		Content:   content,
		Model:     strings.TrimSpace(input.Model),
	}
	if detail, detailErr := a.management.LogDetail(c.Request.Context(), result.RequestID); detailErr == nil {
		result.InputTokens = detail.InputTokens
		result.OutputTokens = detail.OutputTokens
		result.CachedTokens = detail.CachedTokens
		result.EstimatedCostMicros = detail.EstimatedCost
		for index := len(detail.Attempts) - 1; index >= 0; index-- {
			attempt := detail.Attempts[index]
			if !attempt.Success && index > 0 {
				continue
			}
			result.ChannelID = attempt.ChannelID
			result.ChannelName = attempt.ChannelName
			result.UpstreamModel = attempt.UpstreamModel
			break
		}
	}
	c.JSON(http.StatusOK, common.S(&result))
}

func validateWebChatRequest(input *webChatRequest) error {
	if input == nil {
		return errors.New("网页对话请求不能为空")
	}
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Model = strings.TrimSpace(input.Model)
	if input.SessionID == "" || len([]rune(input.SessionID)) > 512 {
		return errors.New("网页对话会话标识无效")
	}
	if input.Model == "" {
		return errors.New("请选择模型")
	}
	if len(input.Messages) == 0 || len(input.Messages) > 200 {
		return errors.New("网页对话消息数量必须在 1 到 200 条之间")
	}
	for index := range input.Messages {
		message := &input.Messages[index]
		message.Role = strings.ToLower(strings.TrimSpace(message.Role))
		message.Content = strings.TrimSpace(message.Content)
		if message.Role != "system" && message.Role != "user" && message.Role != "assistant" {
			return errors.New("网页对话包含不支持的消息角色")
		}
		if message.Content == "" {
			return errors.New("网页对话消息内容不能为空")
		}
	}
	return nil
}

func chatCompletionContent(body []byte) (string, error) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return "", errors.New("上游模型返回了无法解析的响应")
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		return "", errors.New("上游模型没有返回可展示的回复")
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	content := chatContentText(message["content"])
	if content == "" {
		content = strings.TrimSpace(stringValue(message["refusal"]))
	}
	if content == "" {
		return "", errors.New("上游模型没有返回可展示的回复")
	}
	return content, nil
}

func chatContentText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	parts, _ := value.([]any)
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		item, _ := part.(map[string]any)
		text := strings.TrimSpace(stringValue(item["text"]))
		if text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func upstreamChatError(body []byte) string {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	errorValue, _ := payload["error"].(map[string]any)
	return strings.TrimSpace(stringValue(errorValue["message"]))
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
