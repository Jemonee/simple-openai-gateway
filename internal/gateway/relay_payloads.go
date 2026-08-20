package gateway

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const relayPayloadMigrationBatchSize = 25

func requestPayloadFromLog(log RelayRequestLog) RelayRequestPayload {
	return RelayRequestPayload{
		RequestID:             log.ID,
		RequestParametersJSON: log.RequestParametersJSON,
		RequestBody:           log.RequestBody,
		RequestBodyTruncated:  log.RequestBodyTruncated,
		ResponseBody:          log.ResponseBody,
		ResponseBodyTruncated: log.ResponseBodyTruncated,
	}
}

func attemptPayloadFromLog(log RelayAttemptLog) RelayAttemptPayload {
	return RelayAttemptPayload{
		AttemptID:             log.ID,
		RequestID:             log.RequestID,
		RequestBody:           log.RequestBody,
		RequestBodyTruncated:  log.RequestBodyTruncated,
		ResponseBody:          log.ResponseBody,
		ResponseBodyTruncated: log.ResponseBodyTruncated,
	}
}

func clearRequestLogPayload(log *RelayRequestLog) {
	log.RequestParametersJSON = ""
	log.RequestBody = ""
	log.RequestBodyTruncated = false
	log.ResponseBody = ""
	log.ResponseBodyTruncated = false
}

func clearAttemptLogPayload(log *RelayAttemptLog) {
	log.RequestBody = ""
	log.RequestBodyTruncated = false
	log.ResponseBody = ""
	log.ResponseBodyTruncated = false
}

func requestPayloadEmpty(payload RelayRequestPayload) bool {
	return payload.RequestParametersJSON == "" && payload.RequestBody == "" && !payload.RequestBodyTruncated && payload.ResponseBody == "" && !payload.ResponseBodyTruncated
}

func attemptPayloadEmpty(payload RelayAttemptPayload) bool {
	return payload.RequestBody == "" && !payload.RequestBodyTruncated && payload.ResponseBody == "" && !payload.ResponseBodyTruncated
}

func saveRelayRequestLog(db *gorm.DB, log *RelayRequestLog, onConflict bool) error {
	payload := requestPayloadFromLog(*log)
	metadata := *log
	clearRequestLogPayload(&metadata)
	query := db
	if onConflict {
		query = query.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			UpdateAll: true,
		})
	}
	if err := query.Create(&metadata).Error; err != nil {
		return err
	}
	return upsertRelayRequestPayload(db, payload)
}

func saveRelayAttemptLogs(db *gorm.DB, logs []RelayAttemptLog) ([]RelayAttemptLog, error) {
	if len(logs) == 0 {
		return logs, nil
	}
	metadata := append([]RelayAttemptLog(nil), logs...)
	for index := range metadata {
		clearAttemptLogPayload(&metadata[index])
	}
	if err := db.CreateInBatches(&metadata, len(metadata)).Error; err != nil {
		return nil, err
	}
	payloads := make([]RelayAttemptPayload, 0, len(metadata))
	for index := range metadata {
		logs[index].ID = metadata[index].ID
		payload := attemptPayloadFromLog(logs[index])
		if !attemptPayloadEmpty(payload) {
			payloads = append(payloads, payload)
		}
	}
	if len(payloads) > 0 {
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "attempt_id"}},
			UpdateAll: true,
		}).CreateInBatches(&payloads, len(payloads)).Error; err != nil {
			return nil, err
		}
	}
	return logs, nil
}

func upsertRelayRequestPayload(db *gorm.DB, payload RelayRequestPayload) error {
	if requestPayloadEmpty(payload) {
		return db.Where("request_id = ?", payload.RequestID).Delete(&RelayRequestPayload{}).Error
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "request_id"}},
		UpdateAll: true,
	}).Create(&payload).Error
}

func hydrateRelayRequestPayloads(db *gorm.DB, logs []RelayRequestLog, includeBodies bool) error {
	if len(logs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(logs))
	positions := make(map[string]int, len(logs))
	legacy := make(map[string]RelayRequestPayload, len(logs))
	for index := range logs {
		ids = append(ids, logs[index].ID)
		positions[logs[index].ID] = index
		legacy[logs[index].ID] = requestPayloadFromLog(logs[index])
		if !includeBodies {
			logs[index].RequestBody = ""
			logs[index].RequestBodyTruncated = false
			logs[index].ResponseBody = ""
			logs[index].ResponseBodyTruncated = false
		}
	}
	var payloads []RelayRequestPayload
	query := db.Where("request_id IN ?", ids)
	if !includeBodies {
		query = query.Select("request_id, request_parameters_json")
	}
	if err := query.Find(&payloads).Error; err != nil {
		return err
	}
	for _, payload := range payloads {
		index, ok := positions[payload.RequestID]
		if !ok {
			continue
		}
		logs[index].RequestParametersJSON = payload.RequestParametersJSON
		if includeBodies {
			logs[index].RequestBody = payload.RequestBody
			logs[index].RequestBodyTruncated = payload.RequestBodyTruncated
			logs[index].ResponseBody = payload.ResponseBody
			logs[index].ResponseBodyTruncated = payload.ResponseBodyTruncated
		}
	}
	found := make(map[string]struct{}, len(payloads))
	for _, payload := range payloads {
		found[payload.RequestID] = struct{}{}
	}
	missingIDs := make([]string, 0)
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missingIDs = append(missingIDs, id)
		}
	}
	if len(missingIDs) > 0 {
		var legacyRows []RelayRequestLog
		if err := db.Model(&RelayRequestLog{}).Select("id, request_parameters_json, request_body, request_body_truncated, response_body, response_body_truncated").Where("id IN ?", missingIDs).Find(&legacyRows).Error; err != nil {
			return err
		}
		for _, row := range legacyRows {
			payload := requestPayloadFromLog(row)
			if original := legacy[row.ID]; original.RequestParametersJSON != "" || original.RequestBody != "" || original.ResponseBody != "" {
				if original.RequestParametersJSON != "" {
					payload.RequestParametersJSON = original.RequestParametersJSON
				}
				if original.RequestBody != "" {
					payload.RequestBody = original.RequestBody
				}
				if original.ResponseBody != "" {
					payload.ResponseBody = original.ResponseBody
				}
			}
			payload.RequestBodyTruncated = payload.RequestBodyTruncated || legacy[row.ID].RequestBodyTruncated
			payload.ResponseBodyTruncated = payload.ResponseBodyTruncated || legacy[row.ID].ResponseBodyTruncated
			index := positions[row.ID]
			logs[index].RequestParametersJSON = payload.RequestParametersJSON
			if includeBodies {
				logs[index].RequestBody = payload.RequestBody
				logs[index].RequestBodyTruncated = payload.RequestBodyTruncated
				logs[index].ResponseBody = payload.ResponseBody
				logs[index].ResponseBodyTruncated = payload.ResponseBodyTruncated
			}
		}
	}
	return nil
}

func hydrateRelayAttemptPayloads(db *gorm.DB, attempts []RelayAttemptLog) error {
	if len(attempts) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(attempts))
	positions := make(map[uint64]int, len(attempts))
	legacy := make(map[uint64]RelayAttemptPayload, len(attempts))
	for index := range attempts {
		ids = append(ids, attempts[index].ID)
		positions[attempts[index].ID] = index
		legacy[attempts[index].ID] = attemptPayloadFromLog(attempts[index])
	}
	var payloads []RelayAttemptPayload
	if err := db.Where("attempt_id IN ?", ids).Find(&payloads).Error; err != nil {
		return err
	}
	for _, payload := range payloads {
		index, ok := positions[payload.AttemptID]
		if !ok {
			continue
		}
		attempts[index].RequestBody = payload.RequestBody
		attempts[index].RequestBodyTruncated = payload.RequestBodyTruncated
		attempts[index].ResponseBody = payload.ResponseBody
		attempts[index].ResponseBodyTruncated = payload.ResponseBodyTruncated
	}
	found := make(map[uint64]struct{}, len(payloads))
	for _, payload := range payloads {
		found[payload.AttemptID] = struct{}{}
	}
	missingIDs := make([]uint64, 0)
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			missingIDs = append(missingIDs, id)
		}
	}
	if len(missingIDs) > 0 {
		var legacyRows []RelayAttemptLog
		if err := db.Model(&RelayAttemptLog{}).Select("id, request_id, request_body, request_body_truncated, response_body, response_body_truncated").Where("id IN ?", missingIDs).Find(&legacyRows).Error; err != nil {
			return err
		}
		for _, row := range legacyRows {
			payload := attemptPayloadFromLog(row)
			if original := legacy[row.ID]; original.RequestBody != "" || original.ResponseBody != "" {
				if original.RequestBody != "" {
					payload.RequestBody = original.RequestBody
				}
				if original.ResponseBody != "" {
					payload.ResponseBody = original.ResponseBody
				}
			}
			payload.RequestBodyTruncated = payload.RequestBodyTruncated || legacy[row.ID].RequestBodyTruncated
			payload.ResponseBodyTruncated = payload.ResponseBodyTruncated || legacy[row.ID].ResponseBodyTruncated
			index := positions[row.ID]
			attempts[index].RequestBody = payload.RequestBody
			attempts[index].RequestBodyTruncated = payload.RequestBodyTruncated
			attempts[index].ResponseBody = payload.ResponseBody
			attempts[index].ResponseBodyTruncated = payload.ResponseBodyTruncated
		}
	}
	return nil
}

func (s *Store) migrateRelayPayloadTables() error {
	const migrationName = "relay_payload_tables_v1"
	for {
		migrated, err := s.migrateRequestPayloadBatch()
		if err != nil {
			return err
		}
		if !migrated {
			break
		}
	}
	for {
		migrated, err := s.migrateAttemptPayloadBatch()
		if err != nil {
			return err
		}
		if !migrated {
			break
		}
	}
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now().UTC()}).Error
	})
}

func (s *Store) migrateRequestPayloadBatch() (bool, error) {
	var rows []RelayRequestLog
	err := s.db.Transaction(func(db *gorm.DB) error {
		if err := db.Select("id, request_parameters_json, request_body, request_body_truncated, response_body, response_body_truncated").
			Where("request_parameters_json <> '' OR request_body <> '' OR response_body <> '' OR request_body_truncated = ? OR response_body_truncated = ?", true, true).
			Order("id ASC").Limit(relayPayloadMigrationBatchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		payloads := make([]RelayRequestPayload, 0, len(rows))
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			payloads = append(payloads, requestPayloadFromLog(row))
			ids = append(ids, row.ID)
		}
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "request_id"}},
			UpdateAll: true,
		}).Create(&payloads).Error; err != nil {
			return err
		}
		if err := verifyRequestPayloadBatch(db, payloads); err != nil {
			return err
		}
		return db.Model(&RelayRequestLog{}).Where("id IN ?", ids).Updates(map[string]any{
			"request_parameters_json": "",
			"request_body":            "",
			"request_body_truncated":  false,
			"response_body":           "",
			"response_body_truncated": false,
		}).Error
	})
	return len(rows) > 0, err
}

func (s *Store) migrateAttemptPayloadBatch() (bool, error) {
	var rows []RelayAttemptLog
	err := s.db.Transaction(func(db *gorm.DB) error {
		if err := db.Select("id, request_id, request_body, request_body_truncated, response_body, response_body_truncated").
			Where("request_body <> '' OR response_body <> '' OR request_body_truncated = ? OR response_body_truncated = ?", true, true).
			Order("id ASC").Limit(relayPayloadMigrationBatchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		payloads := make([]RelayAttemptPayload, 0, len(rows))
		ids := make([]uint64, 0, len(rows))
		for _, row := range rows {
			payloads = append(payloads, attemptPayloadFromLog(row))
			ids = append(ids, row.ID)
		}
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "attempt_id"}},
			UpdateAll: true,
		}).Create(&payloads).Error; err != nil {
			return err
		}
		if err := verifyAttemptPayloadBatch(db, payloads); err != nil {
			return err
		}
		return db.Model(&RelayAttemptLog{}).Where("id IN ?", ids).Updates(map[string]any{
			"request_body":            "",
			"request_body_truncated":  false,
			"response_body":           "",
			"response_body_truncated": false,
		}).Error
	})
	return len(rows) > 0, err
}

func verifyRequestPayloadBatch(db *gorm.DB, expected []RelayRequestPayload) error {
	ids := make([]string, 0, len(expected))
	for _, payload := range expected {
		ids = append(ids, payload.RequestID)
	}
	var stored []RelayRequestPayload
	if err := db.Where("request_id IN ?", ids).Find(&stored).Error; err != nil {
		return err
	}
	byID := make(map[string]RelayRequestPayload, len(stored))
	for _, payload := range stored {
		byID[payload.RequestID] = payload
	}
	for _, payload := range expected {
		if storedPayload, ok := byID[payload.RequestID]; !ok || storedPayload != payload {
			return fmt.Errorf("verify migrated request payload %s", payload.RequestID)
		}
	}
	return nil
}

func verifyAttemptPayloadBatch(db *gorm.DB, expected []RelayAttemptPayload) error {
	ids := make([]uint64, 0, len(expected))
	for _, payload := range expected {
		ids = append(ids, payload.AttemptID)
	}
	var stored []RelayAttemptPayload
	if err := db.Where("attempt_id IN ?", ids).Find(&stored).Error; err != nil {
		return err
	}
	byID := make(map[uint64]RelayAttemptPayload, len(stored))
	for _, payload := range stored {
		byID[payload.AttemptID] = payload
	}
	for _, payload := range expected {
		if storedPayload, ok := byID[payload.AttemptID]; !ok || storedPayload != payload {
			return fmt.Errorf("verify migrated attempt payload %d", payload.AttemptID)
		}
	}
	return nil
}
