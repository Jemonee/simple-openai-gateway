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
	access     *gateway.ClientAccessService
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

var imageGenerationProfiles = map[string]struct{}{
	"gpt-image-2.5-flare":    {},
	"gpt-image-2.5-sunburst": {},
}

var imageGenerationSizes = map[string]struct{}{
	"1K": {},
	"2K": {},
	"4K": {},
}

var imageGenerationQualities = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
}

type imageGenerationRequest struct {
	Model     string `json:"model"`
	ChannelID uint64 `json:"channelId"`
	Prompt    string `json:"prompt"`
	Size      string `json:"size"`
	Quality   string `json:"quality"`
}

type imageGenerationResponse struct {
	RequestID           string   `json:"requestId"`
	Images              []string `json:"images"`
	Model               string   `json:"model"`
	ChannelID           uint64   `json:"channelId"`
	ChannelName         string   `json:"channelName"`
	UpstreamModel       string   `json:"upstreamModel"`
	InputTokens         int64    `json:"inputTokens"`
	OutputTokens        int64    `json:"outputTokens"`
	EstimatedCostMicros int64    `json:"estimatedCostMicros"`
}

func NewWebChatApi(relay *gateway.RelayService, management *gateway.ManagementService, access *gateway.ClientAccessService, security *AdminSecurity) *WebChatApi {
	return &WebChatApi{relay: relay, management: management, access: access, security: security}
}

func newAdminWebChatToken(admin *gateway.AdminUser) *gateway.ClientToken {
	// Preserve regular users' channel ownership while retaining the global
	// scope for administrators, including legacy users with an empty role.
	ownerUserID := admin.ID
	if admin.Role == "" || admin.Role == gateway.ConsoleRoleAdmin {
		ownerUserID = 0
	}
	return &gateway.ClientToken{
		ID:               webChatTokenIDBase + admin.ID,
		OwnerUserID:      ownerUserID,
		Name:             "网页聊天 / " + admin.Username,
		KeyPrefix:        "web-chat",
		Enabled:          true,
		AllowAllModels:   true,
		AllowAllChannels: true,
		RPM:              0,
		MaxConcurrency:   0,
	}
}

func (a *WebChatApi) Register(router *gin.RouterGroup) {
	admin := router.Group("/admin/gateway/web-chat")
	admin.Use(a.security.RequireConsole, a.security.VerifyOrigin)
	admin.POST("/completions", a.complete)
	admin.POST("/images/generations", a.generateImage)
}

func (a *WebChatApi) generateImage(c *gin.Context) {
	principal, ok := currentConsole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
		return
	}
	var input imageGenerationRequest
	if !bindManagementJSON(c, &input) {
		return
	}
	if err := validateImageGenerationRequest(&input); err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, err.Error()))
		return
	}

	body, err := json.Marshal(map[string]any{
		"model": input.Model, "prompt": input.Prompt, "size": input.Size, "quality": input.Quality,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, common.F[any](http.StatusInternalServerError, "生图请求构建失败"))
		return
	}
	payload, err := gateway.ParseRelayPayload(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, common.F[any](http.StatusBadRequest, err.Error()))
		return
	}
	payload.PinnedChannelID = input.ChannelID
	recorder := httptest.NewRecorder()
	headers := make(http.Header)
	headers.Set("X-Client-Name", "simple-openai-gateway-image-generation")
	var token *gateway.ClientToken
	var release func()
	if principal.AccessToken != nil {
		input.ChannelID = 0
		payload.PinnedChannelID = 0
		token = principal.AccessToken
		var acquireErr error
		release, acquireErr = a.access.Acquire(token)
		if acquireErr != nil {
			status, message := http.StatusTooManyRequests, "访问令牌已达到调用限制"
			if errors.Is(acquireErr, gateway.ErrInvalidClientToken) {
				status, message = http.StatusUnauthorized, "访问令牌无效或已停用"
			}
			c.JSON(status, common.F[any](status, message))
			return
		}
		defer release()
	} else {
		token = newAdminWebChatToken(principal.Admin)
	}
	if publicErr := a.relay.Relay(c.Request.Context(), recorder, headers, "", "images/generations", token, payload, body); publicErr != nil {
		c.JSON(publicErr.Status, common.F[any](publicErr.Status, publicErr.Message))
		return
	}
	if recorder.Code < http.StatusOK || recorder.Code >= http.StatusMultipleChoices {
		message := upstreamChatError(recorder.Body.Bytes())
		if message == "" {
			message = "上游图像模型返回错误"
		}
		c.JSON(recorder.Code, common.F[any](recorder.Code, message))
		return
	}
	images, err := imageGenerationURLs(recorder.Body.Bytes())
	if err != nil {
		c.JSON(http.StatusBadGateway, common.F[any](http.StatusBadGateway, err.Error()))
		return
	}
	result := imageGenerationResponse{RequestID: recorder.Header().Get("X-Request-Id"), Images: images, Model: input.Model}
	if principal.AccessToken != nil {
		a.access.Touch(contextWithoutCancel(c), principal.AccessToken.ID)
	}
	if detail, detailErr := a.management.LogDetail(c.Request.Context(), result.RequestID, consoleTokenID(c)); detailErr == nil {
		result.InputTokens, result.OutputTokens, result.EstimatedCostMicros = detail.InputTokens, detail.OutputTokens, detail.EstimatedCost
		for index := len(detail.Attempts) - 1; index >= 0; index-- {
			attempt := detail.Attempts[index]
			if attempt.Success || index == 0 {
				result.ChannelID, result.ChannelName, result.UpstreamModel = attempt.ChannelID, attempt.ChannelName, attempt.UpstreamModel
				break
			}
		}
	}
	c.JSON(http.StatusOK, common.S(&result))
}

func validateImageGenerationRequest(input *imageGenerationRequest) error {
	if input == nil {
		return errors.New("生图请求不能为空")
	}
	input.Model = strings.TrimSpace(input.Model)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.Size = strings.ToUpper(strings.TrimSpace(input.Size))
	input.Quality = strings.ToLower(strings.TrimSpace(input.Quality))
	if _, ok := imageGenerationProfiles[input.Model]; !ok {
		return errors.New("当前仅支持 gpt-image-2.5-flare 和 gpt-image-2.5-sunburst 生图")
	}
	if input.Prompt == "" || len([]rune(input.Prompt)) > 200000 {
		return errors.New("生图提示词不能为空且不能超过 200000 个字符")
	}
	if _, ok := imageGenerationSizes[input.Size]; !ok {
		return errors.New("分辨率仅支持 1K、2K 或 4K")
	}
	if _, ok := imageGenerationQualities[input.Quality]; !ok {
		return errors.New("质量仅支持低、中或高")
	}
	return nil
}

func imageGenerationURLs(body []byte) ([]string, error) {
	var payload struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, errors.New("上游图像响应无法解析")
	}
	images := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if url := strings.TrimSpace(item.URL); url != "" {
			images = append(images, url)
		} else if encoded := strings.TrimSpace(item.B64JSON); encoded != "" {
			images = append(images, "data:image/png;base64,"+encoded)
		}
	}
	if len(images) == 0 {
		return nil, errors.New("上游图像模型没有返回可展示的图片")
	}
	return images, nil
}

func (a *WebChatApi) complete(c *gin.Context) {
	principal, ok := currentConsole(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, common.F[any](http.StatusUnauthorized, "控制台会话无效或已过期"))
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
	var token *gateway.ClientToken
	var release func()
	if principal.AccessToken != nil {
		input.ChannelID = 0
		payload.PinnedChannelID = 0
		token = principal.AccessToken
		var acquireErr error
		release, acquireErr = a.access.Acquire(token)
		if acquireErr != nil {
			status := http.StatusTooManyRequests
			message := "访问令牌已达到调用限制"
			if errors.Is(acquireErr, gateway.ErrInvalidClientToken) {
				status = http.StatusUnauthorized
				message = "访问令牌无效或已停用"
			}
			c.JSON(status, common.F[any](status, message))
			return
		}
		defer release()
	} else {
		admin := principal.Admin
		token = newAdminWebChatToken(admin)
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
	if principal.AccessToken != nil {
		a.access.Touch(contextWithoutCancel(c), principal.AccessToken.ID)
	}
	if detail, detailErr := a.management.LogDetail(c.Request.Context(), result.RequestID, consoleTokenID(c)); detailErr == nil {
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
