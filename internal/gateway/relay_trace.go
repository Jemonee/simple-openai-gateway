package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Jemonee/simple-openai-gateway/pkg/until"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	RelayStageAccessControl        = "access_control"
	RelayStageRequestBodyRead      = "request_body_read"
	RelayStagePayloadParse         = "payload_parse"
	RelayStageRequestLogStart      = "request_log_start"
	RelayStageSessionResolution    = "session_resolution"
	RelayStageTokenEstimation      = "token_estimation"
	RelayStageRoutePlanning        = "route_planning"
	RelayStageRetryPolicy          = "retry_policy"
	RelayStagePayloadTransform     = "payload_transform"
	RelayStageCredentialDecrypt    = "credential_decrypt"
	RelayStageUpstreamRequestBuild = "upstream_request_build"
	RelayStageUpstreamWaitHeaders  = "upstream_wait_headers"
	RelayStageResponseBodyRead     = "response_body_read"
	RelayStageStreamResponse       = "stream_response"
	RelayStageResponseAnalysis     = "response_analysis"
	RelayStageResponseWrite        = "response_write"
	RelayStageAttemptLogPrepare    = "attempt_log_prepare"
	RelayStageAffinityUpdate       = "affinity_update"
	RelayStageRequestFinalize      = "request_finalize"
	RelayStageRequestLogPersist    = "request_log_persist"

	RelayStepCategoryGateway    = "gateway"
	RelayStepCategoryUpstream   = "upstream"
	RelayStepCategoryDownstream = "downstream"
	RelayStepCategoryStorage    = "storage"
)

// RelayTrace correlates fine-grained timing stages across the HTTP ingress and relay layers.
// Steps are accumulated in memory and persisted in a batch when the request is finalized.
type RelayTrace struct {
	requestID string
	startedAt time.Time
	mu        sync.Mutex
	steps     []RelayStepLog
}

func NewRelayTrace() *RelayTrace {
	return &RelayTrace{
		requestID: uuid.NewString(),
		startedAt: time.Now(),
		steps:     make([]RelayStepLog, 0, 20),
	}
}

func (t *RelayTrace) RequestID() string {
	if t == nil {
		return ""
	}
	return t.requestID
}

func (t *RelayTrace) StartedAt() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.startedAt
}

func (t *RelayTrace) Record(stage string, category string, attempt int, started time.Time, stageErr error, detail string) {
	if t == nil || started.IsZero() {
		return
	}
	finished := time.Now()
	outcome := RelayOutcomeSuccess
	if stageErr != nil {
		outcome = RelayOutcomeFailed
		if errors.Is(stageErr, context.Canceled) {
			outcome = RelayOutcomeCanceled
		}
	}
	step := RelayStepLog{
		RequestID:       t.requestID,
		Stage:           truncateRunes(strings.TrimSpace(stage), 64),
		Category:        truncateRunes(strings.TrimSpace(category), 24),
		Attempt:         max(attempt, 0),
		StartedOffsetUS: max(started.Sub(t.startedAt).Microseconds(), int64(0)),
		DurationUS:      max(finished.Sub(started).Microseconds(), int64(0)),
		Outcome:         outcome,
		Detail:          truncateRunes(strings.TrimSpace(detail), 512),
		CreatedAt:       started.UTC(),
	}
	t.mu.Lock()
	t.steps = append(t.steps, step)
	t.mu.Unlock()
}

func (t *RelayTrace) stepsFor(requestID string) []RelayStepLog {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	steps := make([]RelayStepLog, len(t.steps))
	copy(steps, t.steps)
	for index := range steps {
		steps[index].RequestID = requestID
	}
	return steps
}

// LogCompletion emits one structured application-log event per stage. It is called after
// request processing so log formatting and I/O do not contaminate the measured stage timings.
func (t *RelayTrace) LogCompletion(status int, errorCode string, persistErr error) {
	if t == nil {
		return
	}
	for _, step := range t.stepsFor(t.requestID) {
		fields := logrus.Fields{
			"request_id":        t.requestID,
			"stage":             step.Stage,
			"category":          step.Category,
			"attempt":           step.Attempt,
			"started_offset_us": step.StartedOffsetUS,
			"duration_us":       step.DurationUS,
			"outcome":           step.Outcome,
			"status":            status,
		}
		if step.Detail != "" {
			fields["detail"] = step.Detail
		}
		if errorCode != "" {
			fields["error_code"] = errorCode
		}
		until.Log.WithFields(fields).Info("API relay stage completed")
	}
	if persistErr != nil {
		until.Log.WithFields(logrus.Fields{
			"request_id": t.requestID,
			"status":     status,
			"error_code": errorCode,
		}).WithError(persistErr).Error("API relay timing persistence failed")
	}
}
