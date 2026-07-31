package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Jemonee/simple-openai-gateway/internal/config"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	statusClientClosedRequest      = 499
	maxDetailedPayloadBytes        = 4 << 20
	upstreamApplicationErrorStatus = http.StatusBadGateway
	circuitFailureThreshold        = 3
	temporaryCircuitDuration       = time.Minute
	extendedCircuitDuration        = 5 * time.Minute
)

type PublicError struct {
	Status  int
	Message string
	Type    string
	Code    string
}

func (e *PublicError) Error() string {
	return e.Message
}

type RelayService struct {
	store         *Store
	router        *Router
	estimator     *TokenEstimator
	configManager *config.ApplicationConfigManager
	client        *http.Client
	circuitLocks  sync.Map
}

type relayExecution struct {
	requestID                string
	token                    *ClientToken
	modelID                  uint64
	sessionAffinityMappingID uint64
	refreshSessionAffinity   bool
	endpoint                 string
	payload                  *RelayPayload
	rawBody                  []byte
	inputTokens              int64
	startedAt                time.Time
	attempts                 int
	gatewayPreparationMS     int64
	usage                    Usage
	normalInputTokens        int64
	sentTokens               int64
	estimatedCost            int64
	upstreamCost             int64
	usageSources             map[string]struct{}
	costSources              map[string]struct{}
	firstTokenMS             int64
	latencyMS                int64
	durationMS               int64
	responseBody             []byte
	responseBodyTruncated    bool
	payloadLogDetail         string
	attemptLogs              []RelayAttemptLog
	trace                    *RelayTrace
}

type attemptResult struct {
	response         *http.Response
	body             []byte
	requestBody      []byte
	bodyTruncated    bool
	usage            Usage
	sentTokens       int64
	estimatedCost    int64
	upstreamCost     int64
	costSource       string
	firstTokenMS     int64
	latencyMS        int64
	durationMS       int64
	streamError      error
	outcome          string
	retryReason      string
	retryDetail      string
	circuitOpenUntil *time.Time
}

func NewRelayService(store *Store, router *Router, estimator *TokenEstimator, configManager *config.ApplicationConfigManager) *RelayService {
	headerTimeout := 120 * time.Second
	if cfg := configManager.GetConfig(); cfg != nil && cfg.GatewayConfig.ResponseHeaderTimeoutSeconds > 0 {
		headerTimeout = time.Duration(cfg.GatewayConfig.ResponseHeaderTimeoutSeconds) * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = headerTimeout
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	return &RelayService{
		store:         store,
		router:        router,
		estimator:     estimator,
		configManager: configManager,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (s *RelayService) Relay(ctx context.Context, writer http.ResponseWriter, headers http.Header, rawQuery string, endpoint string, token *ClientToken, payload *RelayPayload, rawBody []byte) *PublicError {
	return s.RelayWithTrace(ctx, writer, headers, rawQuery, endpoint, token, payload, rawBody, NewRelayTrace())
}

func (s *RelayService) RelayWithTrace(ctx context.Context, writer http.ResponseWriter, headers http.Header, rawQuery string, endpoint string, token *ClientToken, payload *RelayPayload, rawBody []byte, trace *RelayTrace) *PublicError {
	if trace == nil {
		trace = NewRelayTrace()
	}
	startedAt := trace.StartedAt()
	execution := &relayExecution{
		requestID:        trace.RequestID(),
		token:            token,
		endpoint:         endpoint,
		payload:          payload,
		rawBody:          rawBody,
		startedAt:        startedAt,
		usageSources:     make(map[string]struct{}),
		costSources:      make(map[string]struct{}),
		payloadLogDetail: s.currentPayloadLogDetail(),
		trace:            trace,
	}
	writer.Header().Set("X-Request-Id", execution.requestID)
	s.recordRequestStarted(context.WithoutCancel(ctx), execution)

	sessionStarted := time.Now()
	resolution, resolutionErr := s.store.resolveAgentSession(ctx, agentSessionRequest{
		TokenID: token.ID, Endpoint: endpoint, Headers: headers,
		Payload: payload, Body: rawBody, Now: startedAt.UTC(),
	})
	applyAgentSessionResolution(payload, resolution)
	sessionDetail := "identified=false"
	if payload.SessionKey != "" {
		sessionDetail = fmt.Sprintf("identified=true client=%s source=%s", payload.ClientKind, payload.SessionSource)
	}
	trace.Record(RelayStageSessionResolution, RelayStepCategoryGateway, 0, sessionStarted, resolutionErr, sessionDetail)
	tokenEstimateStarted := time.Now()
	execution.inputTokens = s.estimator.EstimateValue(payload.values)
	if execution.inputTokens == 0 {
		execution.inputTokens = s.estimator.EstimateJSON(rawBody)
	}
	trace.Record(RelayStageTokenEstimation, RelayStepCategoryGateway, 0, tokenEstimateStarted, nil, fmt.Sprintf("input_tokens=%d", execution.inputTokens))

	routeStarted := time.Now()
	plan, err := s.router.Plan(ctx, token, payload.Model, execution.inputTokens, payload.DeclaredMaxOutput, payload.PreviousResponseID, payload.SessionKey)
	if err != nil {
		trace.Record(RelayStageRoutePlanning, RelayStepCategoryGateway, 0, routeStarted, err, "model="+payload.Model)
		publicErr := routePublicError(err)
		execution.responseBody = publicErrorBody(publicErr)
		s.recordRequest(context.WithoutCancel(ctx), execution, publicErr.Status, publicErr.Code)
		return publicErr
	}
	trace.Record(RelayStageRoutePlanning, RelayStepCategoryGateway, 0, routeStarted, nil,
		fmt.Sprintf("strategy=%s candidates=%d selection=%s", plan.Model.RoutingStrategy, len(plan.Candidates), plan.InitialSelection.Reason))
	execution.modelID = plan.Model.ID
	execution.sessionAffinityMappingID = plan.SessionAffinityMappingID
	execution.refreshSessionAffinity = plan.RefreshSessionAffinity

	retryPolicyStarted := time.Now()
	maxAttempts := 3
	if cfg := s.configManager.GetConfig(); cfg != nil && cfg.GatewayConfig.MaxAttempts > 0 {
		maxAttempts = min(cfg.GatewayConfig.MaxAttempts, 3)
	}
	maxAttempts = min(maxAttempts, len(plan.Candidates))
	trace.Record(RelayStageRetryPolicy, RelayStepCategoryGateway, 0, retryPolicyStarted, nil, fmt.Sprintf("max_attempts=%d", maxAttempts))
	var lastNetworkError error
	selection := plan.InitialSelection

	for index := 0; index < maxAttempts; index++ {
		candidate := plan.Candidates[index]
		execution.attempts++
		result, attemptErr := s.performAttempt(ctx, writer, headers, rawQuery, execution, candidate, selection, index == maxAttempts-1, plan.Affinity)
		s.addCanceledAttemptUsage(execution, result)
		if attemptErr == nil {
			return nil
		}
		lastNetworkError = attemptErr
		if ctx.Err() != nil || errors.Is(attemptErr, context.Canceled) {
			lastNetworkError = context.Canceled
			break
		}
		if plan.Affinity {
			publicErr := &PublicError{Status: http.StatusServiceUnavailable, Message: "The channel associated with previous_response_id is unavailable.", Type: "api_error", Code: "response_affinity_unavailable"}
			execution.responseBody = publicErrorBody(publicErr)
			s.recordRequest(context.WithoutCancel(ctx), execution, publicErr.Status, publicErr.Code)
			return publicErr
		}
		selection = retrySelection(candidate, result)
	}

	message := "All available upstream channels failed."
	status := http.StatusBadGateway
	code := "upstream_unavailable"
	if errors.Is(lastNetworkError, context.Canceled) {
		message = "The request was canceled."
		status = statusClientClosedRequest
		code = "request_canceled"
	} else {
		var applicationFailure *upstreamApplicationFailure
		if errors.As(lastNetworkError, &applicationFailure) && applicationFailureCode(applicationFailure) == "upstream_empty_response" {
			message = applicationFailure.Message
			code = applicationFailureCode(applicationFailure)
		}
	}
	publicErr := &PublicError{Status: status, Message: message, Type: "api_error", Code: code}
	execution.responseBody = publicErrorBody(publicErr)
	s.recordRequest(context.WithoutCancel(ctx), execution, publicErr.Status, publicErr.Code)
	return publicErr
}

func (s *RelayService) currentPayloadLogDetail() string {
	if s.configManager == nil {
		return config.PayloadLogDetailDefault
	}
	return config.EffectivePayloadLogDetail(s.configManager.GetConfig())
}

func (s *RelayService) addCanceledAttemptUsage(execution *relayExecution, result *attemptResult) {
	if result == nil || result.outcome != RelayOutcomeCanceled {
		return
	}
	s.addUsage(execution, result.usage, result.estimatedCost, result.upstreamCost, result.costSource, true)
}

func routePublicError(err error) *PublicError {
	switch {
	case errors.Is(err, ErrModelNotAllowed):
		return &PublicError{Status: http.StatusForbidden, Message: "The API token is not allowed to use this model.", Type: "invalid_request_error", Code: "model_not_allowed"}
	case errors.Is(err, ErrModelNotFound):
		return &PublicError{Status: http.StatusNotFound, Message: "The requested model does not exist or is not available.", Type: "invalid_request_error", Code: "model_not_found"}
	case errors.Is(err, ErrAffinityUnavailable):
		return &PublicError{Status: http.StatusServiceUnavailable, Message: "The channel associated with previous_response_id is unavailable.", Type: "api_error", Code: "response_affinity_unavailable"}
	case errors.Is(err, ErrNoAvailableChannel):
		return &PublicError{Status: http.StatusServiceUnavailable, Message: "No upstream channel is currently available for this model.", Type: "api_error", Code: "no_available_channel"}
	default:
		return &PublicError{Status: http.StatusInternalServerError, Message: "The gateway could not route this request.", Type: "api_error", Code: "gateway_error"}
	}
}

func (s *RelayService) performAttempt(ctx context.Context, writer http.ResponseWriter, incomingHeaders http.Header, rawQuery string, execution *relayExecution, candidate RouteCandidate, selection RouteSelection, lastAttempt bool, affinity bool) (*attemptResult, error) {
	execution.latencyMS = 0
	execution.durationMS = 0
	attempt := execution.attempts
	payloadTransformStarted := time.Now()
	body, err := execution.payload.UpstreamBody(candidate.Mapping.UpstreamModel, execution.endpoint, candidate.Channel.SupportsStreamUsage)
	execution.trace.Record(RelayStagePayloadTransform, RelayStepCategoryGateway, attempt, payloadTransformStarted, err,
		fmt.Sprintf("channel=%s model=%s", candidate.Channel.Name, candidate.Mapping.UpstreamModel))
	if err != nil {
		return s.recordPreparationFailure(ctx, execution, candidate, selection, "payload_transform", nil, err)
	}
	credentialStarted := time.Now()
	apiKey, err := s.store.secretBox.Decrypt(candidate.Channel.APIKeyCipher)
	execution.trace.Record(RelayStageCredentialDecrypt, RelayStepCategoryGateway, attempt, credentialStarted, err, "channel="+candidate.Channel.Name)
	if err != nil {
		return s.recordPreparationFailure(ctx, execution, candidate, selection, "credential_decrypt", body, err)
	}
	requestBuildStarted := time.Now()
	upstreamURL := candidate.Channel.BaseURL + "/" + endpointPath(execution.endpoint)
	if rawQuery != "" {
		upstreamURL += "?" + rawQuery
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		execution.trace.Record(RelayStageUpstreamRequestBuild, RelayStepCategoryGateway, attempt, requestBuildStarted, err, "channel="+candidate.Channel.Name)
		return s.recordPreparationFailure(ctx, execution, candidate, selection, "request_build", body, err)
	}
	copyUpstreamRequestHeaders(request.Header, incomingHeaders)
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gateway-Request-Id", execution.requestID)
	execution.trace.Record(RelayStageUpstreamRequestBuild, RelayStepCategoryGateway, attempt, requestBuildStarted, nil,
		fmt.Sprintf("channel=%s bytes=%d", candidate.Channel.Name, len(body)))

	sentTokens := execution.inputTokens
	execution.sentTokens += sentTokens
	started := time.Now()
	if execution.attempts == 1 {
		execution.gatewayPreparationMS = elapsedMilliseconds(execution.startedAt, started)
	}
	response, requestErr := s.client.Do(request)
	responseReceivedAt := time.Now()
	latency := elapsedMilliseconds(started, responseReceivedAt)
	statusDetail := "channel=" + candidate.Channel.Name
	if response != nil {
		statusDetail = fmt.Sprintf("channel=%s status=%d", candidate.Channel.Name, response.StatusCode)
	}
	execution.trace.Record(RelayStageUpstreamWaitHeaders, RelayStepCategoryUpstream, attempt, started, requestErr, statusDetail)
	logCtx := context.WithoutCancel(ctx)
	if requestErr != nil {
		execution.durationMS = elapsedMilliseconds(execution.startedAt, responseReceivedAt)
		result := &attemptResult{
			requestBody: body,
			sentTokens:  sentTokens,
			latencyMS:   latency,
			durationMS:  latency,
			retryReason: SelectionReasonTransportError,
			retryDetail: "upstream_request",
		}
		if ctx.Err() != nil || errors.Is(requestErr, context.Canceled) {
			result.outcome = RelayOutcomeCanceled
			result.usage = Usage{InputTokens: execution.inputTokens, Source: "estimated_tiktoken"}
			result.estimatedCost = CalculateCostMicros(candidate.Mapping, result.usage)
			result.upstreamCost = result.estimatedCost
			result.costSource = CostSourceFallback
		}
		if ctx.Err() == nil && !errors.Is(requestErr, context.Canceled) {
			result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, requestErr.Error())
		}
		s.recordAttempt(logCtx, execution, candidate, selection, *result, 0, false, requestErr)
		return result, requestErr
	}
	execution.latencyMS = elapsedMilliseconds(execution.startedAt, responseReceivedAt)

	response.Body = s.withIdleTimeout(response.Body)
	if execution.payload.Stream && isEventStream(response.Header) && response.StatusCode >= 200 && response.StatusCode < 300 {
		return s.streamResponse(ctx, writer, execution, candidate, selection, response, started, latency, sentTokens, body)
	}

	responseReadStarted := time.Now()
	responseBody, readErr := io.ReadAll(response.Body)
	responseFinishedAt := time.Now()
	_ = response.Body.Close()
	execution.trace.Record(RelayStageResponseBodyRead, RelayStepCategoryUpstream, attempt, responseReadStarted, readErr,
		fmt.Sprintf("status=%d bytes=%d", response.StatusCode, len(responseBody)))
	execution.durationMS = elapsedMilliseconds(execution.startedAt, responseFinishedAt)
	responseAnalysisStarted := time.Now()
	usage, hasUsage := ParseUsage(responseBody)
	channelFailure, channelFailureDetail := upstreamChannelFailure(response.StatusCode, responseBody)
	execution.trace.Record(RelayStageResponseAnalysis, RelayStepCategoryGateway, attempt, responseAnalysisStarted, nil,
		fmt.Sprintf("usage=%t channel_failure=%t", hasUsage, channelFailure))
	if shouldRetryStatus(response.StatusCode) || channelFailure {
		result := &attemptResult{
			response: response, body: responseBody, requestBody: body, usage: usage, sentTokens: sentTokens,
			costSource: CostSourceFailedZero, latencyMS: latency, durationMS: elapsedMilliseconds(started, responseFinishedAt),
			retryReason: SelectionReasonRetryableStatus, retryDetail: fmt.Sprintf("HTTP %d", response.StatusCode),
		}
		attemptErr := readErr
		failureMessage := fmt.Sprintf("HTTP %d", response.StatusCode)
		if attemptErr == nil {
			attemptErr = fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
		}
		if channelFailure {
			failureMessage = channelFailureDetail
			result.retryDetail = truncateRunes(channelFailureDetail, 512)
			attemptErr = errors.New(channelFailureDetail)
		}
		if readErr != nil {
			result.retryReason = SelectionReasonResponseError
			result.retryDetail = "response_body_read"
		}
		if channelFailure {
			result.circuitOpenUntil = s.recordChannelUnavailable(logCtx, candidate.Channel.ID, candidate.Mapping.ID, failureMessage)
		} else {
			result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, failureMessage)
		}
		s.recordAttempt(logCtx, execution, candidate, selection, *result, response.StatusCode, false, attemptErr)
		if readErr != nil {
			return result, readErr
		}
		if lastAttempt && !affinity {
			s.addUsage(execution, usage, 0, 0, CostSourceFailedZero, false)
			execution.responseBody = responseBody
			s.writeBufferedResponse(execution, writer, response, responseBody)
			s.recordRequest(logCtx, execution, response.StatusCode, upstreamErrorCode(responseBody))
			return nil, nil
		}
		return result, attemptErr
	}

	if appErr := validateBufferedApplicationResponse(execution.endpoint, responseBody); appErr != nil && response.StatusCode >= 200 && response.StatusCode < 300 && readErr == nil {
		result := &attemptResult{
			response: response, body: responseBody, requestBody: body, usage: usage, sentTokens: sentTokens,
			costSource: CostSourceFailedZero, latencyMS: latency, durationMS: elapsedMilliseconds(started, responseFinishedAt),
			retryReason: SelectionReasonUpstreamApplicationError, retryDetail: truncateRunes(appErr.Message, 512),
		}
		if appErr.penalizesChannel() {
			result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, appErr.Error())
		} else {
			s.recordChannelResponsive(logCtx, candidate.Channel.ID)
		}
		s.recordAttempt(logCtx, execution, candidate, selection, *result, response.StatusCode, false, appErr)
		if !appErr.shouldRetry() {
			s.addUsage(execution, usage, 0, 0, CostSourceFailedZero, false)
			execution.responseBody = responseBody
			s.writeBufferedResponse(execution, writer, response, responseBody)
			s.recordRequest(logCtx, execution, upstreamApplicationErrorStatus, applicationFailureCode(appErr))
			return nil, nil
		}
		return result, appErr
	}
	if !hasUsage && response.StatusCode >= 200 && response.StatusCode < 300 {
		usage = Usage{InputTokens: execution.inputTokens, OutputTokens: s.estimator.EstimateJSON(responseBody), Source: "estimated_tiktoken"}
	}
	success := response.StatusCode >= 200 && response.StatusCode < 300 && readErr == nil
	estimatedCost, upstreamCost, costSource := attemptCosts(candidate.Mapping, usage, responseBody, success)
	result := &attemptResult{response: response, body: responseBody, requestBody: body, usage: usage, sentTokens: sentTokens, estimatedCost: estimatedCost, upstreamCost: upstreamCost, costSource: costSource, latencyMS: latency, durationMS: elapsedMilliseconds(started, responseFinishedAt)}
	if readErr != nil {
		result.retryReason = SelectionReasonResponseError
		result.retryDetail = "response_body_read"
		result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, readErr.Error())
	} else if success {
		s.recordChannelSuccess(logCtx, candidate.Channel.ID, latency)
	} else if !shouldRetryStatus(response.StatusCode) {
		s.recordChannelResponsive(logCtx, candidate.Channel.ID)
	}
	s.recordAttempt(logCtx, execution, candidate, selection, *result, response.StatusCode, success, readErr)
	if readErr != nil {
		return result, readErr
	}
	s.addUsage(execution, usage, estimatedCost, upstreamCost, costSource, success)
	code := upstreamErrorCode(responseBody)
	execution.responseBody = responseBody
	s.writeBufferedResponse(execution, writer, response, responseBody)
	if execution.endpoint == "responses" && success {
		affinityStarted := time.Now()
		s.router.RecordAffinity(logCtx, ResponseID(responseBody), candidate.Mapping.ID)
		execution.trace.Record(RelayStageAffinityUpdate, RelayStepCategoryStorage, execution.attempts, affinityStarted, nil, "response_affinity")
	}
	if success {
		affinityStarted := time.Now()
		s.recordSessionAffinityAfterSuccess(logCtx, execution, candidate.Mapping.ID)
		execution.trace.Record(RelayStageAffinityUpdate, RelayStepCategoryStorage, execution.attempts, affinityStarted, nil, "session_affinity")
	}
	s.recordRequest(logCtx, execution, response.StatusCode, code)
	return nil, nil
}

func (s *RelayService) streamResponse(ctx context.Context, writer http.ResponseWriter, execution *relayExecution, candidate RouteCandidate, selection RouteSelection, response *http.Response, started time.Time, latency int64, sentTokens int64, requestBody []byte) (*attemptResult, error) {
	streamStarted := time.Now()
	streamStageRecorded := false
	recordStreamStage := func(stageErr error, receivedEvent bool, terminalSuccess bool) {
		if streamStageRecorded {
			return
		}
		streamStageRecorded = true
		if ctx.Err() != nil {
			stageErr = context.Canceled
		}
		execution.trace.Record(RelayStageStreamResponse, RelayStepCategoryUpstream, execution.attempts, streamStarted, stageErr,
			fmt.Sprintf("events_received=%t terminal=%t", receivedEvent, terminalSuccess))
	}
	reader := bufio.NewReader(response.Body)
	flusher, _ := writer.(http.Flusher)
	usage := Usage{}
	upstreamCost := upstreamCostSnapshot{}
	outputEstimate := int64(0)
	responseID := ""
	terminalSuccess := false
	hasUsableOutput := false
	result := attemptResult{response: response, requestBody: requestBody, sentTokens: sentTokens, latencyMS: latency}
	capture := payloadCapture{}
	committed := false
	commit := func(event []byte) error {
		if committed {
			return nil
		}
		copyUpstreamResponseHeaders(writer.Header(), response.Header, true)
		writer.WriteHeader(response.StatusCode)
		committed = true
		if len(event) > 0 {
			if _, err := writer.Write(event); err != nil {
				return err
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	streamErr := error(nil)
	downstreamError := false
	receivedEvent := false
	for {
		event, readErr := readSSEEvent(reader)
		if len(event) > 0 {
			receivedEvent = true
			capture.Write(event)
			consumeSSEEvent(event, s.estimator, &usage, &upstreamCost, &outputEstimate, &responseID)
			hasOutput := sseEventHasOutputToken(event)
			if execution.endpoint == "responses" && sseEventHasUsableResponseOutput(event) {
				hasUsableOutput = true
			}
			appErr, hasApplicationError := sseApplicationError(event)
			if sseEventIsTerminalSuccess(event, execution.endpoint) {
				terminalSuccess = true
			}

			if !committed {
				if hasApplicationError && !hasOutput && appErr.shouldRetry() {
					finishedAt := time.Now()
					_ = response.Body.Close()
					execution.durationMS = elapsedMilliseconds(execution.startedAt, finishedAt)
					result.body, result.bodyTruncated = capture.Snapshot()
					result.usage = usage
					result.durationMS = elapsedMilliseconds(started, finishedAt)
					result.costSource = CostSourceFailedZero
					result.retryReason = SelectionReasonUpstreamApplicationError
					result.retryDetail = truncateRunes(appErr.Message, 512)
					logCtx := context.WithoutCancel(ctx)
					result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, appErr.Error())
					recordStreamStage(appErr, receivedEvent, terminalSuccess)
					s.recordAttempt(logCtx, execution, candidate, selection, result, response.StatusCode, false, appErr)
					return &result, appErr
				}
				if hasOutput {
					recordFirstToken(event, &result, execution, started)
				}
				if writeErr := commit(event); writeErr != nil {
					streamErr = writeErr
					downstreamError = true
					break
				}
				if hasApplicationError {
					streamErr = appErr
					break
				}
			} else {
				recordFirstToken(event, &result, execution, started)
				if _, writeErr := writer.Write(event); writeErr != nil {
					streamErr = writeErr
					downstreamError = true
					break
				}
				if flusher != nil {
					flusher.Flush()
				}
				if hasApplicationError {
					streamErr = appErr
					break
				}
			}
			if terminalSuccess {
				break
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				streamErr = readErr
			}
			break
		}
	}

	_ = response.Body.Close()
	finishedAt := time.Now()
	execution.durationMS = elapsedMilliseconds(execution.startedAt, finishedAt)
	result.durationMS = elapsedMilliseconds(started, finishedAt)
	result.body, result.bodyTruncated = capture.Snapshot()
	if !terminalSuccess && streamErr == nil && receivedEvent {
		streamErr = io.ErrUnexpectedEOF
	}
	if terminalSuccess && streamErr == nil && execution.endpoint == "responses" && !hasUsableOutput {
		streamErr = emptyUpstreamResponseFailure()
	}
	if !committed && streamErr != nil {
		if !receivedEvent {
			result.retryDetail = "stream_first_event"
		} else {
			result.retryDetail = "stream_before_first_token"
		}
		result.usage = usage
		clientCanceled := ctx.Err() != nil || errors.Is(streamErr, context.Canceled) || downstreamError
		if clientCanceled {
			result.outcome = RelayOutcomeCanceled
			if result.usage.Source == "" {
				result.usage = Usage{InputTokens: execution.inputTokens, OutputTokens: outputEstimate, Source: "estimated_tiktoken"}
			}
			result.estimatedCost = CalculateCostMicros(candidate.Mapping, result.usage)
			result.upstreamCost = result.estimatedCost
			result.costSource = CostSourceFallback
			if upstreamCost.valid {
				result.upstreamCost = upstreamCost.micros
				result.costSource = CostSourceUpstream
			}
		} else {
			result.costSource = CostSourceFailedZero
		}
		result.retryReason = SelectionReasonResponseError
		logCtx := context.WithoutCancel(ctx)
		if ctx.Err() == nil && !errors.Is(streamErr, context.Canceled) {
			result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, streamErr.Error())
		}
		recordStreamStage(streamErr, receivedEvent, terminalSuccess)
		s.recordAttempt(logCtx, execution, candidate, selection, result, response.StatusCode, false, streamErr)
		return &result, streamErr
	}
	if !committed {
		if !receivedEvent {
			err := io.ErrUnexpectedEOF
			result.retryReason = SelectionReasonResponseError
			result.retryDetail = "stream_first_event"
			logCtx := context.WithoutCancel(ctx)
			result.circuitOpenUntil = s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, err.Error())
			recordStreamStage(err, receivedEvent, terminalSuccess)
			s.recordAttempt(logCtx, execution, candidate, selection, result, response.StatusCode, false, err)
			return &result, err
		}
		if writeErr := commit(nil); writeErr != nil {
			streamErr = writeErr
			downstreamError = true
		}
	}
	recordStreamStage(streamErr, receivedEvent, terminalSuccess)
	s.finishStream(ctx, execution, candidate, selection, result, usage, upstreamCost, outputEstimate, responseID, streamErr, downstreamError, terminalSuccess)
	return nil, nil
}

func (s *RelayService) finishStream(ctx context.Context, execution *relayExecution, candidate RouteCandidate, selection RouteSelection, result attemptResult, usage Usage, upstreamSnapshot upstreamCostSnapshot, outputEstimate int64, responseID string, streamErr error, downstreamError bool, terminalSuccess bool) {
	if usage.Source == "" {
		usage = Usage{InputTokens: execution.inputTokens, OutputTokens: outputEstimate, Source: "estimated_tiktoken"}
	}
	logCtx := context.WithoutCancel(ctx)
	var appErr *upstreamApplicationFailure
	applicationFailed := errors.As(streamErr, &appErr)
	if terminalSuccess && !downstreamError && !applicationFailed {
		streamErr = nil
	}
	clientCanceled := downstreamError || (!terminalSuccess && (ctx.Err() != nil || errors.Is(streamErr, context.Canceled)))
	success := terminalSuccess && streamErr == nil && !clientCanceled
	if clientCanceled {
		result.outcome = RelayOutcomeCanceled
	} else if success {
		result.outcome = RelayOutcomeSuccess
	} else {
		result.outcome = RelayOutcomeFailed
	}
	estimatedCost := int64(0)
	upstreamCost := int64(0)
	costSource := CostSourceFailedZero
	if success || clientCanceled {
		estimatedCost = CalculateCostMicros(candidate.Mapping, usage)
		upstreamCost = estimatedCost
		costSource = CostSourceFallback
		if upstreamSnapshot.valid {
			upstreamCost = upstreamSnapshot.micros
			costSource = CostSourceUpstream
		}
	}
	result.usage = usage
	result.estimatedCost = estimatedCost
	result.upstreamCost = upstreamCost
	result.costSource = costSource
	result.streamError = streamErr
	if streamErr == nil || clientCanceled {
		s.recordChannelSuccess(logCtx, candidate.Channel.ID, result.latencyMS)
	} else if errors.As(streamErr, &appErr) && !appErr.penalizesChannel() {
		s.recordChannelResponsive(logCtx, candidate.Channel.ID)
	} else {
		s.recordChannelFailure(logCtx, candidate.Channel.ID, candidate.Mapping.ID, streamErr.Error())
	}
	s.recordAttempt(logCtx, execution, candidate, selection, result, result.response.StatusCode, success, streamErr)
	s.addUsage(execution, usage, estimatedCost, upstreamCost, costSource, success || clientCanceled)
	requestStatus := result.response.StatusCode
	errorCode := ""
	if clientCanceled {
		requestStatus = statusClientClosedRequest
		errorCode = "request_canceled"
	} else if streamErr != nil {
		requestStatus = upstreamApplicationErrorStatus
		errorCode = "stream_interrupted"
		if errors.As(streamErr, &appErr) {
			errorCode = applicationFailureCode(appErr)
		}
	}
	execution.responseBody = result.body
	execution.responseBodyTruncated = result.bodyTruncated
	if execution.endpoint == "responses" && success {
		affinityStarted := time.Now()
		s.router.RecordAffinity(logCtx, responseID, candidate.Mapping.ID)
		execution.trace.Record(RelayStageAffinityUpdate, RelayStepCategoryStorage, execution.attempts, affinityStarted, nil, "response_affinity")
	}
	if success {
		affinityStarted := time.Now()
		s.recordSessionAffinityAfterSuccess(logCtx, execution, candidate.Mapping.ID)
		execution.trace.Record(RelayStageAffinityUpdate, RelayStepCategoryStorage, execution.attempts, affinityStarted, nil, "session_affinity")
	}
	s.recordRequest(logCtx, execution, requestStatus, errorCode)
}

func (s *RelayService) recordSessionAffinityAfterSuccess(ctx context.Context, execution *relayExecution, successfulChannelModelID uint64) {
	if execution.refreshSessionAffinity {
		s.router.RecordSessionAffinity(ctx, execution.token.ID, execution.modelID, execution.payload.SessionKey, successfulChannelModelID)
		return
	}
	s.router.RecordSessionAffinityAfterSuccess(ctx, execution.token.ID, execution.modelID, execution.payload.SessionKey, execution.sessionAffinityMappingID, successfulChannelModelID)
}

func elapsedMilliseconds(started time.Time, finished time.Time) int64 {
	elapsed := finished.Sub(started).Milliseconds()
	return max(elapsed, int64(1))
}

func durationAfterLatency(totalMS int64, latencyMS int64) int64 {
	return max(totalMS-latencyMS, int64(0))
}

func firstTokenAfterLatency(firstTokenMS int64, latencyMS int64) int64 {
	if firstTokenMS <= 0 {
		return 0
	}
	return max(firstTokenMS-latencyMS, int64(1))
}

func recordFirstToken(event []byte, result *attemptResult, execution *relayExecution, attemptStarted time.Time) {
	if result.firstTokenMS > 0 || !sseEventHasOutputToken(event) {
		return
	}
	now := time.Now()
	result.firstTokenMS = elapsedMilliseconds(attemptStarted, now)
	if execution.firstTokenMS == 0 {
		execution.firstTokenMS = elapsedMilliseconds(execution.startedAt, now)
	}
}

func endpointPath(endpoint string) string {
	if endpoint == "responses" {
		return "responses"
	}
	return "chat/completions"
}

func shouldRetryStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
}

func upstreamChannelFailure(status int, body []byte) (bool, string) {
	category := ""
	switch status {
	case http.StatusUnauthorized:
		category = "upstream authentication failed"
	case http.StatusPaymentRequired:
		category = "upstream account balance unavailable"
	case http.StatusBadRequest, http.StatusForbidden:
		normalized := strings.ToLower(string(body))
		markers := []string{
			"预扣费额度失败", "余额不足", "余额额度", "需要预扣费额度", "账户余额",
			"insufficient_quota", "insufficient quota", "insufficient balance", "insufficient credit",
			"credit balance", "credits exhausted", "out of credits", "billing hard limit",
			"billing_hard_limit", "payment required", "quota exceeded", "quota_exceeded",
			"invalid api key", "invalid_api_key", "api key is invalid", "api key disabled",
			"authentication failed", "account disabled", "key disabled", "账号已停用",
		}
		for _, marker := range markers {
			if strings.Contains(normalized, marker) {
				category = "upstream account or credential unavailable"
				break
			}
		}
	}
	if category == "" {
		return false, ""
	}
	message := ""
	if appErr, failed := upstreamApplicationError(body); failed {
		message = strings.TrimSpace(appErr.Message)
	}
	if message == "" {
		message = strings.Join(strings.Fields(string(body)), " ")
	}
	if message == "" {
		return true, fmt.Sprintf("%s (HTTP %d)", category, status)
	}
	return true, fmt.Sprintf("%s (HTTP %d): %s", category, status, truncateRunes(message, 512))
}

func retrySelection(candidate RouteCandidate, result *attemptResult) RouteSelection {
	selection := RouteSelection{
		PreviousChannelID:   candidate.Channel.ID,
		PreviousChannelName: candidate.Channel.Name,
		Reason:              SelectionReasonTransportError,
		Detail:              "upstream_request",
	}
	if result == nil {
		return selection
	}
	if result.circuitOpenUntil != nil {
		selection.Reason = SelectionReasonCircuitOpened
		selection.Detail = result.circuitOpenUntil.UTC().Format(time.RFC3339)
		return selection
	}
	if result.retryReason != "" {
		selection.Reason = result.retryReason
		selection.Detail = result.retryDetail
	}
	return selection
}

func (s *RelayService) recordPreparationFailure(ctx context.Context, execution *relayExecution, candidate RouteCandidate, selection RouteSelection, detail string, requestBody []byte, cause error) (*attemptResult, error) {
	execution.durationMS = elapsedMilliseconds(execution.startedAt, time.Now())
	result := &attemptResult{
		requestBody: requestBody,
		retryReason: SelectionReasonGatewayPreparationError,
		retryDetail: detail,
	}
	s.recordAttempt(
		context.WithoutCancel(ctx),
		execution,
		candidate,
		selection,
		*result,
		0,
		false,
		fmt.Errorf("gateway preparation failed: %s", detail),
	)
	return result, cause
}

func isEventStream(header http.Header) bool {
	return strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream")
}

type upstreamApplicationFailure struct {
	Message         string
	Code            string
	nonRetryable    bool
	preserveChannel bool
}

func (e *upstreamApplicationFailure) Error() string {
	if e.Code == "" {
		return "upstream application error: " + e.Message
	}
	return fmt.Sprintf("upstream application error (%s): %s", e.Code, e.Message)
}

func (e *upstreamApplicationFailure) shouldRetry() bool {
	return e != nil && !e.nonRetryable
}

func (e *upstreamApplicationFailure) penalizesChannel() bool {
	return e != nil && !e.preserveChannel
}

func applicationFailureCode(failure *upstreamApplicationFailure) string {
	if failure != nil && strings.TrimSpace(failure.Code) != "" {
		return truncateRunes(strings.TrimSpace(failure.Code), 80)
	}
	return "upstream_application_error"
}

func newApplicationFailure(message string, code string) *upstreamApplicationFailure {
	failure := &upstreamApplicationFailure{Message: truncateRunes(strings.TrimSpace(message), 2000), Code: truncateRunes(strings.TrimSpace(code), 80)}
	switch strings.ToLower(failure.Code) {
	case "bad_request", "content_filter", "content_policy_violation", "context_length_exceeded", "invalid_request", "invalid_request_error", "max_output_tokens", "response_cancelled", "unsupported_value":
		failure.nonRetryable = true
		failure.preserveChannel = true
	}
	return failure
}

func incompleteApplicationFailure(reason string) *upstreamApplicationFailure {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		failure := newApplicationFailure("upstream response incomplete", "response_incomplete")
		failure.nonRetryable = true
		failure.preserveChannel = true
		return failure
	}
	failure := newApplicationFailure("upstream response incomplete: "+reason, reason)
	switch strings.ToLower(reason) {
	case "connection_error", "server_error", "timeout", "upstream_interrupted":
		failure.nonRetryable = false
		failure.preserveChannel = false
	default:
		failure.nonRetryable = true
		failure.preserveChannel = true
	}
	return failure
}

func upstreamApplicationError(data []byte) (*upstreamApplicationFailure, bool) {
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		message := strings.TrimSpace(string(data))
		lower := strings.ToLower(message)
		if strings.Contains(lower, "model is at capacity") || strings.Contains(lower, "try a different model") {
			return newApplicationFailure(message, "model_at_capacity"), true
		}
		return nil, false
	}

	typeName, _ := payload["type"].(string)
	if value, exists := payload["error"]; exists && value != nil {
		if failure := applicationFailureDetails(value); failure != nil {
			return failure, true
		}
		return newApplicationFailure("upstream application error", "upstream_error"), true
	}
	response := payload
	if nested, ok := payload["response"].(map[string]any); ok {
		response = nested
	}
	responseStatus, _ := response["status"].(string)
	if typeName == "response.failed" || strings.EqualFold(responseStatus, "failed") {
		if failure := applicationFailureDetails(response["error"]); failure != nil {
			return failure, true
		}
		return newApplicationFailure("upstream response failed", "response_failed"), true
	}
	if typeName == "response.incomplete" || strings.EqualFold(responseStatus, "incomplete") {
		reason := ""
		if details, ok := response["incomplete_details"].(map[string]any); ok {
			reason, _ = details["reason"].(string)
			reason = strings.TrimSpace(reason)
		}
		return incompleteApplicationFailure(reason), true
	}
	if typeName == "response.cancelled" || strings.EqualFold(responseStatus, "cancelled") || strings.EqualFold(responseStatus, "canceled") {
		return newApplicationFailure("upstream response cancelled", "response_cancelled"), true
	}
	if typeName == "error" {
		if failure := applicationFailureDetails(payload); failure != nil {
			return failure, true
		}
		return newApplicationFailure("upstream application error", "upstream_error"), true
	}
	return nil, false
}

func validateBufferedApplicationResponse(endpoint string, data []byte) *upstreamApplicationFailure {
	if failure, failed := upstreamApplicationError(data); failed {
		return failure
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil || payload == nil {
		return newApplicationFailure("upstream returned an invalid JSON response", "invalid_upstream_response")
	}
	switch endpoint {
	case "responses":
		status, _ := payload["status"].(string)
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "completed":
			if !responsesPayloadHasUsableOutput(payload) {
				return emptyUpstreamResponseFailure()
			}
			return nil
		case "in_progress", "queued":
			return nil
		default:
			return newApplicationFailure("upstream response is missing a valid status", "invalid_upstream_response")
		}
	case "chat":
		if _, ok := payload["choices"].([]any); !ok {
			return newApplicationFailure("upstream chat response is missing choices", "invalid_upstream_response")
		}
	}
	return nil
}

func emptyUpstreamResponseFailure() *upstreamApplicationFailure {
	return newApplicationFailure("upstream response completed without any usable output", "upstream_empty_response")
}

func responsesPayloadHasUsableOutput(payload map[string]any) bool {
	response := payload
	if nested, ok := payload["response"].(map[string]any); ok {
		response = nested
	}
	output, _ := response["output"].([]any)
	for _, item := range output {
		if responseOutputItemHasUsableOutput(item) {
			return true
		}
	}
	return false
}

func responseOutputItemHasUsableOutput(value any) bool {
	item, ok := value.(map[string]any)
	if !ok {
		return false
	}
	typeName := strings.ToLower(strings.TrimSpace(stringValue(item["type"])))
	switch typeName {
	case "message":
		content, _ := item["content"].([]any)
		for _, part := range content {
			if responseOutputItemHasUsableOutput(part) {
				return true
			}
		}
	case "output_text", "text":
		return strings.TrimSpace(stringValue(item["text"])) != ""
	case "refusal":
		return strings.TrimSpace(stringValue(item["refusal"])) != "" || strings.TrimSpace(stringValue(item["text"])) != ""
	default:
		if strings.HasSuffix(typeName, "_call") && !strings.HasSuffix(typeName, "_call_output") {
			for _, key := range []string{"name", "arguments", "input", "call_id", "id"} {
				if strings.TrimSpace(stringValue(item[key])) != "" {
					return true
				}
			}
		}
	}
	return false
}

func applicationFailureDetails(value any) *upstreamApplicationFailure {
	switch typed := value.(type) {
	case string:
		if message := strings.TrimSpace(typed); message != "" {
			return newApplicationFailure(message, "")
		}
	case map[string]any:
		message, _ := typed["message"].(string)
		code, _ := typed["code"].(string)
		if code == "" {
			code, _ = typed["type"].(string)
		}
		message = strings.TrimSpace(message)
		if message == "" {
			if nested, exists := typed["error"]; exists {
				return applicationFailureDetails(nested)
			}
			message = code
		}
		if message != "" {
			return newApplicationFailure(message, code)
		}
	}
	return nil
}

func sseApplicationError(event []byte) (*upstreamApplicationFailure, bool) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if failure, ok := upstreamApplicationError(data); ok {
			return failure, true
		}
	}
	return nil, false
}

func sseEventIsTerminalSuccess(event []byte, endpoint string) bool {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(data, []byte("[DONE]")) {
			return endpoint != "responses"
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			continue
		}
		if endpoint == "responses" {
			if payload["type"] == "response.completed" {
				return true
			}
			continue
		}
		if choices, ok := payload["choices"].([]any); ok {
			for _, item := range choices {
				choice, _ := item.(map[string]any)
				if choice["finish_reason"] != nil {
					return true
				}
			}
		}
	}
	return false
}

type payloadCapture struct {
	buffer    bytes.Buffer
	truncated bool
}

func (c *payloadCapture) Write(data []byte) {
	remaining := maxDetailedPayloadBytes - c.buffer.Len()
	if remaining <= 0 {
		c.truncated = c.truncated || len(data) > 0
		return
	}
	if len(data) > remaining {
		_, _ = c.buffer.Write(data[:remaining])
		c.truncated = true
		return
	}
	_, _ = c.buffer.Write(data)
}

func (c *payloadCapture) Snapshot() ([]byte, bool) {
	return bytes.Clone(c.buffer.Bytes()), c.truncated
}

func storedPayload(data []byte, alreadyTruncated bool) (string, bool) {
	return storedPayloadWithLimit(data, alreadyTruncated, maxDetailedPayloadBytes)
}

func storedPayloadWithLimit(data []byte, alreadyTruncated bool, limit int) (string, bool) {
	truncated := alreadyTruncated || len(data) > limit
	if len(data) > limit {
		data = data[:limit]
	}
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
		truncated = true
	}
	return compressStoredPayload(data), truncated
}

func publicErrorBody(publicErr *PublicError) []byte {
	body, _ := json.Marshal(struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    string  `json:"code"`
		} `json:"error"`
	}{Error: struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    string  `json:"code"`
	}{Message: publicErr.Message, Type: publicErr.Type, Code: publicErr.Code}})
	return body
}

func requestSessionName(body []byte) string {
	if _, titleRequest := codexTitleRequestPrompt(body); titleRequest {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	if messages, ok := payload["messages"].([]any); ok {
		if text := latestUserMessageText(messages); text != "" {
			return truncateRunes(normalizeCodexPrompt(text), 10)
		}
	}
	if input, exists := payload["input"]; exists {
		if text := latestResponsesInputText(input); text != "" {
			return truncateRunes(normalizeCodexPrompt(text), 10)
		}
	}
	return ""
}

func agentRequestSessionTitle(endpoint string, clientKind string, body []byte) (string, bool) {
	if clientKind == copilotClientKind {
		return copilotSessionTitle(endpoint, body)
	}
	return requestSessionName(body), false
}

func latestUserMessageText(messages []any) string {
	for index := len(messages) - 1; index >= 0; index-- {
		item := messages[index]
		message, _ := item.(map[string]any)
		role, _ := message["role"].(string)
		if role != "user" {
			continue
		}
		if text := contentText(message["content"]); text != "" {
			return text
		}
	}
	return ""
}

func latestResponsesInputText(input any) string {
	if text, ok := input.(string); ok {
		return text
	}
	items, _ := input.([]any)
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		switch typed := item.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				return typed
			}
		case map[string]any:
			role, hasRole := typed["role"].(string)
			if hasRole && role != "user" {
				continue
			}
			if text := contentText(typed["content"]); text != "" {
				return text
			}
			if !hasRole {
				if text := contentText(typed); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := contentText(item); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		contentType, _ := typed["type"].(string)
		if contentType != "" && contentType != "text" && contentType != "input_text" && contentType != "output_text" {
			return ""
		}
		if text, exists := typed["text"]; exists {
			return contentText(text)
		}
		if content, exists := typed["content"]; exists {
			return contentText(content)
		}
		if value, exists := typed["value"]; exists && (contentType == "text" || contentType == "input_text") {
			return contentText(value)
		}
	}
	return ""
}

func normalizeSessionName(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func readSSEEvent(reader *bufio.Reader) ([]byte, error) {
	var event bytes.Buffer
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			event.Write(line)
			if bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n")) {
				return event.Bytes(), nil
			}
		}
		if err != nil {
			return event.Bytes(), err
		}
	}
}

func sseEventHasOutputToken(event []byte) bool {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			continue
		}
		if delta, ok := payload["delta"]; ok && generatedDeltaHasContent(delta) {
			return true
		}
		choices, _ := payload["choices"].([]any)
		for _, item := range choices {
			choice, _ := item.(map[string]any)
			if delta, ok := choice["delta"]; ok && generatedDeltaHasContent(delta) {
				return true
			}
		}
	}
	return false
}

func sseEventHasUsableResponseOutput(event []byte) bool {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(data, &payload) != nil {
			continue
		}
		if responsesPayloadHasUsableOutput(payload) || responseOutputItemHasUsableOutput(payload["item"]) {
			return true
		}
		typeName := strings.ToLower(strings.TrimSpace(stringValue(payload["type"])))
		switch typeName {
		case "response.output_text.delta", "response.refusal.delta":
			if strings.TrimSpace(stringValue(payload["delta"])) != "" {
				return true
			}
		}
		if strings.Contains(typeName, "function_call") || strings.Contains(typeName, "custom_tool_call") || strings.Contains(typeName, "computer_call") || strings.Contains(typeName, "shell_call") || strings.Contains(typeName, "mcp_call") {
			for _, key := range []string{"delta", "name", "arguments", "input", "call_id"} {
				if strings.TrimSpace(stringValue(payload[key])) != "" {
					return true
				}
			}
		}
	}
	return false
}

func generatedDeltaHasContent(value any) bool {
	switch typed := value.(type) {
	case string:
		return typed != ""
	case []any:
		for _, item := range typed {
			if generatedDeltaHasContent(item) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			if key == "role" || key == "index" || key == "type" {
				continue
			}
			if generatedDeltaHasContent(item) {
				return true
			}
		}
	}
	return false
}

type upstreamCostSnapshot struct {
	micros int64
	valid  bool
}

func consumeSSEEvent(event []byte, estimator *TokenEstimator, usage *Usage, upstreamCost *upstreamCostSnapshot, outputEstimate *int64, responseID *string) {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if parsed, ok := ParseUsage(data); ok {
			*usage = parsed
		}
		if micros, ok := ParseUpstreamCostMicros(data); ok {
			upstreamCost.micros = micros
			upstreamCost.valid = true
		}
		*outputEstimate += estimator.EstimateJSON(data)
		if *responseID == "" {
			*responseID = ResponseID(data)
		}
	}
}

func copyUpstreamRequestHeaders(destination http.Header, source http.Header) {
	for key, values := range source {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if requestHeaderBlocked(canonical) {
			continue
		}
		for _, value := range values {
			destination.Add(canonical, value)
		}
	}
}

func requestHeaderBlocked(key string) bool {
	switch key {
	case "Authorization", "Cookie", "Content-Length", "Connection", "Proxy-Authorization", "Proxy-Connection", "Transfer-Encoding", "Upgrade", "Host", "Origin", "Referer", "Accept-Encoding", "Forwarded", copilotIntegrationHeader:
		return true
	}
	return strings.HasPrefix(key, "X-Forwarded-")
}

func copyUpstreamResponseHeaders(destination http.Header, source http.Header, streaming bool) {
	for key, values := range source {
		canonical := textproto.CanonicalMIMEHeaderKey(key)
		if canonical == "Connection" || canonical == "Transfer-Encoding" || canonical == "Keep-Alive" || canonical == "Proxy-Authenticate" || canonical == "Trailer" || canonical == "Upgrade" || canonical == "Set-Cookie" || canonical == "Set-Cookie2" || (streaming && canonical == "Content-Length") {
			continue
		}
		for _, value := range values {
			destination.Add(canonical, value)
		}
	}
}

func (s *RelayService) writeBufferedResponse(execution *relayExecution, writer http.ResponseWriter, response *http.Response, body []byte) {
	started := time.Now()
	copyUpstreamResponseHeaders(writer.Header(), response.Header, false)
	writer.WriteHeader(response.StatusCode)
	_, writeErr := writer.Write(body)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
	execution.trace.Record(RelayStageResponseWrite, RelayStepCategoryDownstream, execution.attempts, started, writeErr,
		fmt.Sprintf("status=%d bytes=%d", response.StatusCode, len(body)))
}

func (s *RelayService) withIdleTimeout(body io.ReadCloser) io.ReadCloser {
	timeout := 300 * time.Second
	if cfg := s.configManager.GetConfig(); cfg != nil && cfg.GatewayConfig.StreamIdleTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.GatewayConfig.StreamIdleTimeoutSeconds) * time.Second
	}
	return &idleReadCloser{ReadCloser: body, timeout: timeout}
}

type idleReadCloser struct {
	io.ReadCloser
	timeout time.Duration
}

type readResult struct {
	n   int
	err error
}

func (r *idleReadCloser) Read(buffer []byte) (int, error) {
	result := make(chan readResult, 1)
	go func() {
		n, err := r.ReadCloser.Read(buffer)
		result <- readResult{n: n, err: err}
	}()
	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case completed := <-result:
		return completed.n, completed.err
	case <-timer.C:
		_ = r.ReadCloser.Close()
		completed := <-result
		if completed.n > 0 {
			return completed.n, nil
		}
		return 0, errors.New("upstream stream idle timeout")
	}
}

func attemptCosts(mapping ChannelModel, usage Usage, responseBody []byte, success bool) (int64, int64, string) {
	if !success {
		return 0, 0, CostSourceFailedZero
	}
	estimatedCost := CalculateCostMicros(mapping, usage)
	if upstreamCost, ok := ParseUpstreamCostMicros(responseBody); ok {
		return estimatedCost, upstreamCost, CostSourceUpstream
	}
	return estimatedCost, estimatedCost, CostSourceFallback
}

func (s *RelayService) addUsage(execution *relayExecution, usage Usage, estimatedCost int64, upstreamCost int64, costSource string, billable bool) {
	execution.usage.InputTokens += usage.InputTokens
	execution.normalInputTokens += normalInputTokens(usage)
	execution.usage.OutputTokens += usage.OutputTokens
	execution.usage.CachedTokens += usage.CachedTokens
	execution.usage.CacheWriteTokens += usage.CacheWriteTokens
	if usage.Source != "" {
		execution.usageSources[usage.Source] = struct{}{}
	}
	if billable {
		execution.estimatedCost += estimatedCost
		execution.upstreamCost += upstreamCost
		if costSource != "" {
			execution.costSources[costSource] = struct{}{}
		}
	}
}

func (s *RelayService) recordAttempt(_ context.Context, execution *relayExecution, candidate RouteCandidate, selection RouteSelection, result attemptResult, status int, success bool, attemptErr error) {
	started := time.Now()
	message := ""
	if attemptErr != nil {
		message = truncateRunes(attemptErr.Error(), 2000)
	}
	outcome := result.outcome
	if outcome == "" {
		if success {
			outcome = RelayOutcomeSuccess
		} else {
			outcome = RelayOutcomeFailed
		}
	}
	if outcome == RelayOutcomeFailed {
		result.estimatedCost = 0
		result.upstreamCost = 0
		result.costSource = CostSourceFailedZero
	}
	compactedRequestBody := compactAttemptPayload(result.requestBody, execution.rawBody, execution.requestID)
	requestBody, requestBodyTruncated := retainLoggedPayload(execution.payloadLogDetail, compactedRequestBody, len(result.requestBody) > maxDetailedPayloadBytes)
	responseBody, responseBodyTruncated := retainLoggedPayload(execution.payloadLogDetail, result.body, result.bodyTruncated)
	routeDecisionJSON := ""
	if selection.Decision != nil {
		if encoded, err := json.Marshal(selection.Decision); err == nil {
			routeDecisionJSON = string(encoded)
		}
	}
	log := RelayAttemptLog{
		RequestID:             execution.requestID,
		ChannelID:             candidate.Channel.ID,
		ChannelName:           candidate.Channel.Name,
		ChannelBaseURL:        candidate.Channel.BaseURL,
		ChannelModelID:        candidate.Mapping.ID,
		UpstreamModel:         candidate.Mapping.UpstreamModel,
		PreviousChannelID:     selection.PreviousChannelID,
		PreviousChannelName:   truncateRunes(selection.PreviousChannelName, 120),
		SelectionReason:       truncateRunes(selection.Reason, 48),
		SelectionDetail:       truncateRunes(selection.Detail, 512),
		RouteDecisionJSON:     routeDecisionJSON,
		PayloadLogDetail:      execution.payloadLogDetail,
		RequestBody:           requestBody,
		RequestBodyTruncated:  requestBodyTruncated,
		ResponseBody:          responseBody,
		ResponseBodyTruncated: responseBodyTruncated,
		StatusCode:            status,
		Outcome:               outcome,
		InputTokens:           result.usage.InputTokens,
		NormalInputTokens:     normalInputTokens(result.usage),
		OutputTokens:          result.usage.OutputTokens,
		CachedTokens:          result.usage.CachedTokens,
		CacheWriteTokens:      result.usage.CacheWriteTokens,
		SentTokens:            result.sentTokens,
		EstimatedCost:         result.estimatedCost,
		UpstreamCost:          result.upstreamCost,
		CostSource:            result.costSource,
		UsageSource:           result.usage.Source,
		FirstTokenMS:          firstTokenAfterLatency(result.firstTokenMS, result.latencyMS),
		LatencyMS:             result.latencyMS,
		DurationMS:            durationAfterLatency(result.durationMS, result.latencyMS),
		Success:               success,
		ErrorMessage:          message,
		CreatedAt:             time.Now().UTC(),
	}
	execution.attemptLogs = append(execution.attemptLogs, log)
	execution.trace.Record(RelayStageAttemptLogPrepare, RelayStepCategoryGateway, execution.attempts, started, nil,
		fmt.Sprintf("channel=%s outcome=%s", candidate.Channel.Name, outcome))
}

func (s *RelayService) recordRequestStarted(ctx context.Context, execution *relayExecution) {
	started := time.Now()
	createdAt := execution.startedAt.UTC()
	if execution.startedAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	log := RelayRequestLog{
		ID:                    execution.requestID,
		TokenID:               execution.token.ID,
		TokenName:             execution.token.Name,
		TokenKeyPrefix:        execution.token.KeyPrefix,
		Endpoint:              execution.endpoint,
		RequestedModel:        execution.payload.Model,
		ClientKind:            execution.payload.ClientKind,
		CodexSessionID:        truncateRunes(execution.payload.LogSessionKey, 512),
		CodexSessionSource:    execution.payload.LogSessionSource,
		RequestParametersJSON: execution.payload.RequestParametersJSON,
		PayloadLogDetail:      execution.payloadLogDetail,
		Outcome:               RelayOutcomeProcessing,
		Stream:                execution.payload.Stream,
		CreatedAt:             createdAt,
	}
	err := s.store.db.WithContext(ctx).Create(&log).Error
	execution.trace.Record(RelayStageRequestLogStart, RelayStepCategoryStorage, 0, started, err, "")
}

func (s *RelayService) recordRequest(ctx context.Context, execution *relayExecution, status int, errorCode string) {
	finalizeStarted := time.Now()
	outcome := relayRequestOutcome(status, errorCode)
	usageSource := ""
	if len(execution.usageSources) == 1 {
		for source := range execution.usageSources {
			usageSource = source
		}
	} else if len(execution.usageSources) > 1 {
		usageSource = "mixed"
	}
	costSource := CostSourceFailedZero
	if len(execution.costSources) == 1 {
		for source := range execution.costSources {
			costSource = source
		}
	} else if len(execution.costSources) > 1 {
		costSource = CostSourceMixed
	}
	estimatedCost := execution.estimatedCost
	upstreamCost := execution.upstreamCost
	if outcome == RelayOutcomeFailed {
		estimatedCost = 0
		upstreamCost = 0
		costSource = CostSourceFailedZero
	}
	now := time.Now().UTC()
	createdAt := execution.startedAt.UTC()
	if execution.startedAt.IsZero() {
		createdAt = now
	}
	durationMS := execution.durationMS
	if durationMS == 0 {
		durationMS = elapsedMilliseconds(execution.startedAt, now)
	}
	firstTokenMS := firstTokenAfterLatency(execution.firstTokenMS, execution.latencyMS)
	durationMS = durationAfterLatency(durationMS, execution.latencyMS)
	responseBody, responseBodyTruncated := retainLoggedPayload(execution.payloadLogDetail, execution.responseBody, execution.responseBodyTruncated)
	sessionName, renamedSession := agentRequestSessionTitle(execution.endpoint, execution.payload.ClientKind, execution.rawBody)
	loggedSessionID, loggedSessionSource := execution.payload.LogSessionKey, execution.payload.LogSessionSource
	codexPromptHash, codexTitleRequest, codexGeneratedTitle := "", false, ""
	if execution.payload.ClientKind != copilotClientKind {
		loggedSessionID, loggedSessionSource = loggedCodexSessionIdentity(loggedSessionID, loggedSessionSource)
		codexPromptHash, codexTitleRequest, codexGeneratedTitle = codexLogPayloadMetadata(execution.rawBody, execution.responseBody)
	}
	requestParametersJSON := execution.payload.RequestParametersJSON
	if execution.payloadLogDetail == config.PayloadLogDetailNone {
		requestParametersJSON = ""
	}
	log := RelayRequestLog{
		ID:                    execution.requestID,
		TokenID:               execution.token.ID,
		TokenName:             execution.token.Name,
		TokenKeyPrefix:        execution.token.KeyPrefix,
		Endpoint:              execution.endpoint,
		RequestedModel:        execution.payload.Model,
		ClientKind:            execution.payload.ClientKind,
		CodexSessionID:        truncateRunes(loggedSessionID, 512),
		CodexSessionSource:    loggedSessionSource,
		SessionName:           sessionName,
		CodexPromptHash:       codexPromptHash,
		CodexTitleRequest:     codexTitleRequest,
		CodexGeneratedTitle:   codexGeneratedTitle,
		IsCompaction:          execution.payload.IsCompactionRequest,
		RequestParametersJSON: requestParametersJSON,
		PayloadLogDetail:      execution.payloadLogDetail,
		ResponseBody:          responseBody,
		ResponseBodyTruncated: responseBodyTruncated,
		StatusCode:            status,
		Outcome:               outcome,
		InputTokens:           execution.usage.InputTokens,
		NormalInputTokens:     execution.normalInputTokens,
		OutputTokens:          execution.usage.OutputTokens,
		CachedTokens:          execution.usage.CachedTokens,
		CacheWriteTokens:      execution.usage.CacheWriteTokens,
		SentTokens:            execution.sentTokens,
		EstimatedCost:         estimatedCost,
		UpstreamCost:          upstreamCost,
		CostSource:            costSource,
		UsageSource:           usageSource,
		AttemptCount:          execution.attempts,
		GatewayPreparationMS:  execution.gatewayPreparationMS,
		FirstTokenMS:          firstTokenMS,
		LatencyMS:             execution.latencyMS,
		DurationMS:            durationMS,
		Stream:                execution.payload.Stream,
		ErrorCode:             errorCode,
		CreatedAt:             createdAt,
	}
	successCount := int64(0)
	if outcome == RelayOutcomeSuccess {
		successCount = 1
	}
	canceledCount := int64(0)
	if outcome == RelayOutcomeCanceled {
		canceledCount = 1
	}
	firstTokenSamples := int64(0)
	if log.FirstTokenMS > 0 {
		firstTokenSamples = 1
	}
	latencySamples := int64(0)
	if log.LatencyMS > 0 {
		latencySamples = 1
	}
	stat := TokenDailyStat{
		Date:              eastEightDate(now),
		TokenID:           execution.token.ID,
		RequestCount:      1,
		SuccessCount:      successCount,
		CanceledCount:     canceledCount,
		InputTokens:       log.InputTokens,
		NormalInputTokens: log.NormalInputTokens,
		OutputTokens:      log.OutputTokens,
		CachedTokens:      log.CachedTokens,
		CacheWriteTokens:  log.CacheWriteTokens,
		SentTokens:        log.SentTokens,
		EstimatedCost:     log.EstimatedCost,
		UpstreamCost:      log.UpstreamCost,
		FirstTokenMS:      log.FirstTokenMS,
		FirstTokenSamples: firstTokenSamples,
		LatencyMS:         log.LatencyMS,
		LatencySamples:    latencySamples,
		DurationMS:        log.DurationMS,
		AttemptCount:      int64(log.AttemptCount),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	var responseForManifest []byte
	if outcome == RelayOutcomeSuccess {
		responseForManifest = execution.responseBody
	}
	execution.trace.Record(RelayStageRequestFinalize, RelayStepCategoryGateway, 0, finalizeStarted, nil,
		fmt.Sprintf("status=%d outcome=%s attempts=%d", status, outcome, execution.attempts))
	steps := execution.trace.stepsFor(execution.requestID)
	persistStarted := time.Now()
	persistErr := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var previousLog RelayRequestLog
		if err := db.Select("is_compaction").Where("id = ?", log.ID).First(&previousLog).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Model(&ClientToken{}).Where("id = ?", execution.token.ID).Update("last_used_at", now).Error; err != nil {
			return err
		}
		mergedTitle := ""
		if execution.payload.ClientKind != copilotClientKind {
			var err error
			mergedTitle, err = s.store.mergePrecedingCodexTitleRequest(db, &log, execution.rawBody, execution.startedAt.UTC())
			if err != nil {
				return err
			}
		}
		effectiveSessionName := sessionName
		if mergedTitle != "" {
			effectiveSessionName = mergedTitle
			log.SessionName = mergedTitle
		}
		compactedRequestBody := compactSessionPayload(
			db,
			execution.token.ID,
			loggedSessionID,
			execution.requestID,
			effectiveSessionName,
			execution.payload.ThreadSource,
			execution.rawBody,
			responseForManifest,
			now,
		)
		if err := persistAgentSessionIdentity(
			db, execution.token.ID, loggedSessionID, loggedSessionSource,
			execution.payload.ClientKind, execution.payload.ClientFingerprint,
		); err != nil {
			return err
		}
		if renamedSession && sessionName != "" {
			if err := db.Model(&RelaySessionState{}).
				Where("token_id = ? AND session_id = ? AND title_customized = ?", execution.token.ID, loggedSessionID, false).
				Update("title", sessionName).Error; err != nil {
				return err
			}
			log.SessionName = sessionName
		}
		log.RequestBody, log.RequestBodyTruncated = retainLoggedPayload(execution.payloadLogDetail, compactedRequestBody, len(execution.rawBody) > maxDetailedPayloadBytes)
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		}).Create(&log).Error; err != nil {
			return err
		}
		if len(execution.attemptLogs) > 0 {
			if err := db.CreateInBatches(execution.attemptLogs, len(execution.attemptLogs)).Error; err != nil {
				return err
			}
		}
		if len(steps) > 0 {
			if err := db.CreateInBatches(steps, len(steps)).Error; err != nil {
				return err
			}
		}
		if execution.payload.ClientKind != copilotClientKind {
			canonicalSessionID, title, err := s.store.mergeFollowingCodexTitleRequest(db, &log, execution.startedAt.UTC())
			if err != nil {
				return err
			}
			if canonicalSessionID != "" {
				log.CodexSessionID = canonicalSessionID
				log.CodexSessionSource = codexTitleSessionSource
				log.SessionName = title
			}
		}
		if log.IsCompaction && !previousLog.IsCompaction && log.CodexSessionID != "" {
			if err := incrementSessionCompactionCount(db, log.TokenID, log.CodexSessionID, now); err != nil {
				return err
			}
		}
		return db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "date"}, {Name: "token_id"}},
			DoUpdates: clause.Assignments(map[string]any{
				"request_count":       gorm.Expr("request_count + excluded.request_count"),
				"success_count":       gorm.Expr("success_count + excluded.success_count"),
				"canceled_count":      gorm.Expr("canceled_count + excluded.canceled_count"),
				"input_tokens":        gorm.Expr("input_tokens + excluded.input_tokens"),
				"normal_input_tokens": gorm.Expr("normal_input_tokens + excluded.normal_input_tokens"),
				"output_tokens":       gorm.Expr("output_tokens + excluded.output_tokens"),
				"cached_tokens":       gorm.Expr("cached_tokens + excluded.cached_tokens"),
				"cache_write_tokens":  gorm.Expr("cache_write_tokens + excluded.cache_write_tokens"),
				"sent_tokens":         gorm.Expr("sent_tokens + excluded.sent_tokens"),
				"estimated_cost":      gorm.Expr("estimated_cost + excluded.estimated_cost"),
				"upstream_cost":       gorm.Expr("upstream_cost + excluded.upstream_cost"),
				"first_token_ms":      gorm.Expr("first_token_ms + excluded.first_token_ms"),
				"first_token_samples": gorm.Expr("first_token_samples + excluded.first_token_samples"),
				"latency_ms":          gorm.Expr("latency_ms + excluded.latency_ms"),
				"latency_samples":     gorm.Expr("latency_samples + excluded.latency_samples"),
				"duration_ms":         gorm.Expr("duration_ms + excluded.duration_ms"),
				"attempt_count":       gorm.Expr("attempt_count + excluded.attempt_count"),
				"updated_at":          now,
			}),
		}).Create(&stat).Error
	})
	execution.trace.Record(RelayStageRequestLogPersist, RelayStepCategoryStorage, 0, persistStarted, persistErr,
		fmt.Sprintf("steps=%d attempts=%d", len(steps), len(execution.attemptLogs)))
	allSteps := execution.trace.stepsFor(execution.requestID)
	if len(allSteps) > len(steps) {
		if err := s.store.db.WithContext(ctx).Create(&allSteps[len(allSteps)-1]).Error; err != nil {
			persistErr = errors.Join(persistErr, err)
		}
	}
	execution.trace.LogCompletion(status, errorCode, persistErr)
}

func relayRequestOutcome(status int, errorCode string) string {
	if errorCode == "request_canceled" || status == statusClientClosedRequest {
		return RelayOutcomeCanceled
	}
	if status >= 200 && status < 300 && errorCode == "" {
		return RelayOutcomeSuccess
	}
	return RelayOutcomeFailed
}

func (s *RelayService) recordChannelFailure(ctx context.Context, channelID uint64, channelModelID uint64, message string) *time.Time {
	return s.recordChannelFailureState(ctx, channelID, channelModelID, message, false)
}

func (s *RelayService) recordChannelUnavailable(ctx context.Context, channelID uint64, channelModelID uint64, message string) *time.Time {
	return s.recordChannelFailureState(ctx, channelID, channelModelID, message, true)
}

func (s *RelayService) recordChannelFailureState(ctx context.Context, channelID uint64, channelModelID uint64, message string, immediate bool) *time.Time {
	message = truncateRunes(message, 2000)
	now := time.Now()
	lock := s.channelCircuitLock(channelID)
	lock.Lock()
	defer lock.Unlock()

	var activeOpenUntil *time.Time
	err := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var channel Channel
		if err := db.Select("id", "name", "enabled", "consecutive_failures", "circuit_level", "circuit_open_until").First(&channel, channelID).Error; err != nil {
			return err
		}
		if channel.CircuitLevel >= CircuitLevelManual {
			return db.Model(&Channel{}).Where("id = ?", channelID).Updates(map[string]any{
				"last_error":     message,
				"last_health_at": now,
			}).Error
		}

		failures := channel.ConsecutiveFailures + 1
		failureCount := failures
		level := max(channel.CircuitLevel, CircuitLevelClosed)
		var openUntil *time.Time
		if immediate || failures >= circuitFailureThreshold {
			previousLevel := level
			targetLevel := min(level+1, CircuitLevelManual)
			failures = 0
			switch targetLevel {
			case CircuitLevelTemporary:
				value := now.Add(temporaryCircuitDuration)
				openUntil = &value
				level = targetLevel
			case CircuitLevelExtended:
				value := now.Add(extendedCircuitDuration)
				openUntil = &value
				level = targetLevel
			default:
				var mapping ChannelModel
				if err := db.Where("id = ? AND channel_id = ?", channelModelID, channelID).First(&mapping).Error; err != nil {
					return err
				}
				if err := db.Model(&mapping).Updates(map[string]any{"enabled": false, "circuit_disabled": true}).Error; err != nil {
					return err
				}
				level = CircuitLevelClosed
			}
			if previousLevel > CircuitLevelClosed {
				if err := resolveCircuitRecords(db, channelID, 0, previousLevel, CircuitResolutionEscalated, now); err != nil {
					return err
				}
			}
			if err := createCircuitRecord(db, channel, channelModelID, targetLevel, failureCount, immediate, openUntil, message); err != nil {
				return err
			}
		} else if channel.CircuitOpenUntil != nil && channel.CircuitOpenUntil.After(now) {
			openUntil = channel.CircuitOpenUntil
		}
		if err := db.Model(&Channel{}).Where("id = ?", channelID).Updates(map[string]any{
			"consecutive_failures": failures,
			"circuit_level":        level,
			"circuit_open_until":   openUntil,
			"last_error":           message,
			"last_health_at":       now,
		}).Error; err != nil {
			return err
		}
		if openUntil != nil && openUntil.After(now) {
			activeOpenUntil = openUntil
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return activeOpenUntil
}

func (s *RelayService) recordChannelSuccess(ctx context.Context, channelID uint64, latencyMS int64) {
	s.recordChannelRecovery(ctx, channelID, map[string]any{
		"consecutive_failures": 0,
		"latency_ewma": gorm.Expr(
			"CASE WHEN latency_ewma <= 0 THEN ? ELSE latency_ewma * 0.8 + ? * 0.2 END",
			latencyMS, latencyMS,
		),
	})
}

func (s *RelayService) recordChannelResponsive(ctx context.Context, channelID uint64) {
	s.recordChannelRecovery(ctx, channelID, map[string]any{
		"consecutive_failures": 0,
	})
}

func (s *RelayService) recordChannelRecovery(ctx context.Context, channelID uint64, updates map[string]any) {
	lock := s.channelCircuitLock(channelID)
	lock.Lock()
	defer lock.Unlock()

	now := time.Now()
	_ = s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var channel Channel
		if err := db.Select("circuit_level").First(&channel, channelID).Error; err != nil || channel.CircuitLevel >= CircuitLevelManual {
			return err
		}
		level := max(channel.CircuitLevel-1, CircuitLevelClosed)
		updates["circuit_level"] = level
		updates["circuit_open_until"] = nil
		updates["last_health_at"] = now
		if level == CircuitLevelClosed {
			updates["last_error"] = ""
		}
		if channel.CircuitLevel > CircuitLevelClosed {
			if err := resolveCircuitRecords(db, channelID, 0, channel.CircuitLevel, CircuitResolutionAutomaticRecovery, now); err != nil {
				return err
			}
		}
		return db.Model(&Channel{}).Where("id = ?", channelID).Updates(updates).Error
	})
}

func (s *RelayService) channelCircuitLock(channelID uint64) *sync.Mutex {
	lock, _ := s.circuitLocks.LoadOrStore(channelID, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func upstreamErrorCode(body []byte) string {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		return payload.Error.Code
	}
	return ""
}
