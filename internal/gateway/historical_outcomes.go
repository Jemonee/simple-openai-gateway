package gateway

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"time"

	"gorm.io/gorm"
)

const historicalApplicationOutcomesMigration = "application_outcomes_v3"

type historicalRequestOutcomeRow struct {
	ID                    string
	TokenID               uint64
	Endpoint              string
	RequestedModel        string
	CodexSessionID        string
	Stream                bool
	ResponseBody          string
	ResponseBodyTruncated bool
	EstimatedCost         int64
	UpstreamCost          int64
	CreatedAt             time.Time
}

type dailyOutcomeAdjustment struct {
	successes     int64
	estimatedCost int64
	upstreamCost  int64
}

type dailyOutcomeKey struct {
	date    string
	tokenID uint64
}

func (s *Store) backfillApplicationOutcomes() error {
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", historicalApplicationOutcomesMigration).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		adjustments := make(map[dailyOutcomeKey]dailyOutcomeAdjustment)
		var rows []historicalRequestOutcomeRow
		if err := db.Model(&RelayRequestLog{}).
			Select("id, token_id, endpoint, requested_model, codex_session_id, stream, response_body, response_body_truncated, estimated_cost, upstream_cost, created_at").
			Where("outcome = ? AND status_code BETWEEN 200 AND 299", RelayOutcomeSuccess).
			FindInBatches(&rows, 100, func(_ *gorm.DB, _ int) error {
				for _, row := range rows {
					failure, responseID, inspected := classifyHistoricalResponse(row.Endpoint, row.Stream, row.ResponseBody, row.ResponseBodyTruncated)
					if !inspected || failure == nil {
						continue
					}
					if err := correctHistoricalAttempts(db, row, failure); err != nil {
						return err
					}
					if err := db.Model(&RelayRequestLog{}).Where("id = ?", row.ID).Updates(map[string]any{
						"status_code":    upstreamApplicationErrorStatus,
						"outcome":        RelayOutcomeFailed,
						"error_code":     applicationFailureCode(failure),
						"estimated_cost": 0,
						"upstream_cost":  0,
						"cost_source":    CostSourceFailedZero,
					}).Error; err != nil {
						return err
					}
					if responseID != "" {
						if err := db.Where("response_hash = ?", hashSecret(responseID)).Delete(&ResponseAffinity{}).Error; err != nil {
							return err
						}
					}
					if err := removeHistoricalSessionAffinity(db, row); err != nil {
						return err
					}
					key := dailyOutcomeKey{date: eastEightDate(row.CreatedAt), tokenID: row.TokenID}
					adjustment := adjustments[key]
					adjustment.successes++
					adjustment.estimatedCost += row.EstimatedCost
					adjustment.upstreamCost += row.UpstreamCost
					adjustments[key] = adjustment
				}
				return nil
			}).Error; err != nil {
			return err
		}

		for key, adjustment := range adjustments {
			if err := db.Model(&TokenDailyStat{}).Where("date = ? AND token_id = ?", key.date, key.tokenID).Updates(map[string]any{
				"success_count":  gorm.Expr("CASE WHEN success_count >= ? THEN success_count - ? ELSE 0 END", adjustment.successes, adjustment.successes),
				"estimated_cost": gorm.Expr("CASE WHEN estimated_cost >= ? THEN estimated_cost - ? ELSE 0 END", adjustment.estimatedCost, adjustment.estimatedCost),
				"upstream_cost":  gorm.Expr("CASE WHEN upstream_cost >= ? THEN upstream_cost - ? ELSE 0 END", adjustment.upstreamCost, adjustment.upstreamCost),
			}).Error; err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: historicalApplicationOutcomesMigration, AppliedAt: time.Now()}).Error
	})
}

func removeHistoricalSessionAffinity(db *gorm.DB, request historicalRequestOutcomeRow) error {
	if request.TokenID == 0 || request.CodexSessionID == "" || request.RequestedModel == "" {
		return nil
	}
	var model GatewayModel
	if err := db.Select("id").Where("name = ?", request.RequestedModel).First(&model).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	var affinity SessionAffinity
	err := db.Where("token_id = ? AND model_id = ? AND session_hash = ?", request.TokenID, model.ID, hashSecret(request.CodexSessionID)).First(&affinity).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if affinity.UpdatedAt.Before(request.CreatedAt) {
		return nil
	}
	var laterSuccesses int64
	if err := db.Model(&RelayRequestLog{}).Where(
		"token_id = ? AND codex_session_id = ? AND requested_model = ? AND created_at > ? AND outcome = ?",
		request.TokenID, request.CodexSessionID, request.RequestedModel, request.CreatedAt, RelayOutcomeSuccess,
	).Count(&laterSuccesses).Error; err != nil {
		return err
	}
	if laterSuccesses > 0 {
		return nil
	}
	return db.Delete(&affinity).Error
}

func correctHistoricalAttempts(db *gorm.DB, request historicalRequestOutcomeRow, requestFailure *upstreamApplicationFailure) error {
	var attempts []RelayAttemptLog
	if err := db.Where("request_id = ?", request.ID).Order("created_at ASC, id ASC").Find(&attempts).Error; err != nil {
		return err
	}
	corrected := false
	for _, attempt := range attempts {
		if !attempt.Success && attempt.Outcome != RelayOutcomeSuccess {
			continue
		}
		failure, _, inspected := classifyHistoricalResponse(request.Endpoint, request.Stream, attempt.ResponseBody, attempt.ResponseBodyTruncated)
		if !inspected || failure == nil {
			continue
		}
		if err := markHistoricalAttemptFailed(db, attempt.ID, failure); err != nil {
			return err
		}
		corrected = true
	}
	if corrected {
		return nil
	}
	for index := len(attempts) - 1; index >= 0; index-- {
		if attempts[index].Success || attempts[index].Outcome == RelayOutcomeSuccess {
			return markHistoricalAttemptFailed(db, attempts[index].ID, requestFailure)
		}
	}
	return nil
}

func markHistoricalAttemptFailed(db *gorm.DB, attemptID uint64, failure *upstreamApplicationFailure) error {
	return db.Model(&RelayAttemptLog{}).Where("id = ?", attemptID).Updates(map[string]any{
		"success":        false,
		"outcome":        RelayOutcomeFailed,
		"error_message":  truncateRunes(failure.Error(), 2000),
		"estimated_cost": 0,
		"upstream_cost":  0,
		"cost_source":    CostSourceFailedZero,
	}).Error
}

func classifyHistoricalResponse(endpoint string, stream bool, storedBody string, truncated bool) (*upstreamApplicationFailure, string, bool) {
	body, decoded := decodeStoredPayload(storedBody)
	if !decoded {
		return nil, "", false
	}
	if !stream {
		if strings.TrimSpace(body) == "" && truncated {
			return nil, "", false
		}
		return validateBufferedApplicationResponse(endpoint, []byte(body)), ResponseID([]byte(body)), true
	}

	reader := bufio.NewReader(strings.NewReader(body))
	receivedEvent := false
	terminalSuccess := false
	hasUsableOutput := false
	responseID := ""
	for {
		event, readErr := readSSEEvent(reader)
		if len(event) > 0 {
			receivedEvent = true
			if responseID == "" {
				responseID = responseIDFromSSEEvent(event)
			}
			if failure, failed := sseApplicationError(event); failed {
				return failure, responseID, true
			}
			if sseEventIsTerminalSuccess(event, endpoint) {
				terminalSuccess = true
			}
			if endpoint != "responses" || sseEventHasUsableResponseOutput(event) {
				hasUsableOutput = true
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) && !truncated {
				return newApplicationFailure("historical upstream stream could not be read", "stream_interrupted"), responseID, true
			}
			break
		}
	}
	if terminalSuccess {
		if endpoint == "responses" && !hasUsableOutput {
			return emptyUpstreamResponseFailure(), responseID, true
		}
		return nil, responseID, true
	}
	if truncated {
		return nil, responseID, false
	}
	message := "historical upstream stream ended without a successful terminal event"
	if !receivedEvent {
		message = "historical upstream stream returned no events"
	}
	return newApplicationFailure(message, "stream_interrupted"), responseID, true
}

func responseIDFromSSEEvent(event []byte) string {
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if responseID := ResponseID(data); responseID != "" {
			return responseID
		}
	}
	return ""
}
