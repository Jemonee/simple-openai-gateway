package gateway

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

var contextWindowProfileLocks sync.Map

const (
	contextWindowAutoCompactionTrigger = "auto"
	contextWindowSourceSession         = "session_compaction"
	contextWindowSourceAgentModel      = "agent_model"
	contextWindowIgnoredModel          = "codex-auto-review"
	// Codex starts automatic compaction with roughly eight percent of its effective window reserved.
	contextWindowCompactionRatio  = 0.92
	contextWindowSampleLimit      = 32
	minimumCompactionSampleTokens = 1024
)

func updateSessionContextWindow(db *gorm.DB, log RelayRequestLog, clientFingerprint string, newCompaction bool, now time.Time) error {
	clientFingerprint = strings.TrimSpace(clientFingerprint)
	if log.TokenID == 0 || strings.TrimSpace(log.CodexSessionID) == "" {
		return nil
	}

	primaryModel, err := sessionPrimaryModel(db, log.TokenID, log.CodexSessionID)
	if err != nil {
		return err
	}
	if primaryModel == "" {
		return nil
	}

	var state RelaySessionState
	if err := db.Where("token_id = ? AND session_id = ?", log.TokenID, log.CodexSessionID).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if clientFingerprint == "" {
		clientFingerprint = state.ClientFingerprint
	}
	if state.PrimaryModel != primaryModel {
		if err := db.Model(&RelaySessionState{}).
			Where("token_id = ? AND session_id = ?", state.TokenID, state.SessionID).
			Updates(map[string]any{
				"primary_model":          primaryModel,
				"context_window_tokens":  0,
				"context_window_source":  "",
				"context_window_samples": 0,
				"context_samples_json":   "",
			}).Error; err != nil {
			return err
		}
		state.PrimaryModel = primaryModel
		state.ContextWindowTokens = 0
		state.ContextWindowSource = ""
		state.ContextWindowSamples = 0
		state.ContextSamplesJSON = ""
	}

	isAutomaticSample := newCompaction && log.IsCompaction &&
		log.CompactionTrigger == contextWindowAutoCompactionTrigger &&
		log.Outcome == RelayOutcomeSuccess && log.InputTokens >= minimumCompactionSampleTokens &&
		strings.EqualFold(log.RequestedModel, primaryModel)
	if isAutomaticSample && clientFingerprint != "" {
		profileLock := modelAgentContextWindowLock(log.TokenID, clientFingerprint, primaryModel)
		profileLock.Lock()
		defer profileLock.Unlock()

		profile, err := appendModelAgentContextSample(db, log.TokenID, clientFingerprint, primaryModel, log.InputTokens, now)
		if err != nil {
			return err
		}
		sessionSamplesJSON := state.ContextSamplesJSON
		sessionSampleCount := state.ContextWindowSamples
		if state.ContextWindowSource != contextWindowSourceSession {
			sessionSamplesJSON = ""
			sessionSampleCount = 0
		}
		windowTokens, _, samplesJSON, sampleCount := appendContextWindowSample(sessionSamplesJSON, sessionSampleCount, log.InputTokens)
		if err := db.Model(&RelaySessionState{}).
			Where("token_id = ? AND session_id = ?", state.TokenID, state.SessionID).
			Updates(map[string]any{
				"context_window_tokens":  windowTokens,
				"context_window_source":  contextWindowSourceSession,
				"context_window_samples": sampleCount,
				"context_samples_json":   samplesJSON,
			}).Error; err != nil {
			return err
		}
		return propagateModelAgentContextWindow(db, profile)
	}

	if state.ContextWindowSource == contextWindowSourceSession || clientFingerprint == "" {
		return nil
	}
	return inheritModelAgentContextWindow(db, &state, clientFingerprint, primaryModel)
}

func modelAgentContextWindowLock(tokenID uint64, clientFingerprint string, model string) *sync.Mutex {
	lockKey := strings.Join([]string{model, clientFingerprint, strconv.FormatUint(tokenID, 10)}, "\x00")
	lockValue, _ := contextWindowProfileLocks.LoadOrStore(lockKey, &sync.Mutex{})
	return lockValue.(*sync.Mutex)
}

func sessionPrimaryModel(db *gorm.DB, tokenID uint64, sessionID string) (string, error) {
	type modelUsage struct {
		Model        string
		RequestCount int64
		LatestUnix   int64
	}
	var usage modelUsage
	err := db.Model(&RelayRequestLog{}).
		Select("requested_model AS model, COUNT(*) AS request_count, MAX(unixepoch(created_at)) AS latest_unix").
		Where("token_id = ? AND codex_session_id = ?", tokenID, sessionID).
		Where("LOWER(requested_model) NOT LIKE ?", contextWindowIgnoredModel+"%").
		Where("codex_session_source NOT IN ?", codexAuxiliarySessionSources).
		Group("requested_model").
		Order("request_count DESC, latest_unix DESC, requested_model ASC").
		Limit(1).Scan(&usage).Error
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(usage.Model), nil
}

func appendModelAgentContextSample(db *gorm.DB, tokenID uint64, clientFingerprint string, model string, sample int64, now time.Time) (ModelAgentContextWindow, error) {
	var profile ModelAgentContextWindow
	err := db.Where("token_id = ? AND client_fingerprint = ? AND model = ?", tokenID, clientFingerprint, model).First(&profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		profile = ModelAgentContextWindow{
			TokenID: tokenID, ClientFingerprint: clientFingerprint, Model: model,
			CreatedAt: now,
		}
	} else if err != nil {
		return profile, err
	}
	profile.ContextWindowTokens, profile.CompactionThresholdTokens, profile.SamplesJSON, profile.SampleCount =
		appendContextWindowSample(profile.SamplesJSON, profile.SampleCount, sample)
	profile.UpdatedAt = now
	if profile.CreatedAt.IsZero() {
		profile.CreatedAt = now
	}
	err = db.Save(&profile).Error
	return profile, err
}

func appendContextWindowSample(samplesJSON string, totalCount int64, sample int64) (int64, int64, string, int64) {
	samples := make([]int64, 0, contextWindowSampleLimit)
	_ = json.Unmarshal([]byte(samplesJSON), &samples)
	valid := samples[:0]
	for _, value := range samples {
		if value >= minimumCompactionSampleTokens {
			valid = append(valid, value)
		}
	}
	samples = append(valid, sample)
	if len(samples) > contextWindowSampleLimit {
		samples = samples[len(samples)-contextWindowSampleLimit:]
	}
	ordered := append([]int64(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	// The upper quartile resists early compactions while avoiding a single oversized request becoming the estimate.
	index := (3*len(ordered)+3)/4 - 1
	threshold := ordered[index]
	window := int64(math.Round((float64(threshold)/contextWindowCompactionRatio)/1000.0) * 1000)
	encoded, _ := json.Marshal(samples)
	return max(window, threshold), threshold, string(encoded), max(totalCount, 0) + 1
}

func propagateModelAgentContextWindow(db *gorm.DB, profile ModelAgentContextWindow) error {
	return db.Model(&RelaySessionState{}).
		Where("token_id = ? AND client_fingerprint = ? AND primary_model = ?", profile.TokenID, profile.ClientFingerprint, profile.Model).
		Where("context_window_source = '' OR context_window_source = ?", contextWindowSourceAgentModel).
		Updates(map[string]any{
			"context_window_tokens":  profile.ContextWindowTokens,
			"context_window_source":  contextWindowSourceAgentModel,
			"context_window_samples": profile.SampleCount,
			"context_samples_json":   "",
		}).Error
}

func inheritModelAgentContextWindow(db *gorm.DB, state *RelaySessionState, clientFingerprint string, primaryModel string) error {
	var profile ModelAgentContextWindow
	err := db.Where("token_id = ? AND client_fingerprint = ? AND model = ?", state.TokenID, clientFingerprint, primaryModel).First(&profile).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return db.Model(&RelaySessionState{}).
		Where("token_id = ? AND session_id = ?", state.TokenID, state.SessionID).
		Updates(map[string]any{
			"context_window_tokens":  profile.ContextWindowTokens,
			"context_window_source":  contextWindowSourceAgentModel,
			"context_window_samples": profile.SampleCount,
			"context_samples_json":   "",
		}).Error
}

func (s *Store) backfillModelAgentContextWindows() error {
	const migrationName = "model_agent_context_windows_v1"
	return s.db.Transaction(func(db *gorm.DB) error {
		var migration GatewayMigration
		err := db.First(&migration, "name = ?", migrationName).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var compactions []RelayRequestLog
		if err := db.Select("id, request_body").Where("is_compaction = ? AND request_body <> ''", true).Find(&compactions).Error; err != nil {
			return err
		}
		for _, log := range compactions {
			body := []byte(decompressStoredPayload(log.RequestBody))
			_, trigger := codexCompactionMetadataFromBody(body)
			if trigger != "" {
				if err := db.Model(&RelayRequestLog{}).Where("id = ?", log.ID).UpdateColumn("compaction_trigger", trigger).Error; err != nil {
					return err
				}
			}
		}

		var states []RelaySessionState
		if err := db.Find(&states).Error; err != nil {
			return err
		}
		for index := range states {
			primaryModel, err := sessionPrimaryModel(db, states[index].TokenID, states[index].SessionID)
			if err != nil {
				return err
			}
			states[index].PrimaryModel = primaryModel
			if primaryModel != "" {
				if err := db.Model(&RelaySessionState{}).
					Where("token_id = ? AND session_id = ?", states[index].TokenID, states[index].SessionID).
					UpdateColumn("primary_model", primaryModel).Error; err != nil {
					return err
				}
			}
		}

		var samples []RelayRequestLog
		if err := db.Where("is_compaction = ? AND compaction_trigger = ? AND outcome = ? AND input_tokens >= ? AND codex_session_id <> ''",
			true, contextWindowAutoCompactionTrigger, RelayOutcomeSuccess, minimumCompactionSampleTokens).
			Order("created_at ASC, id ASC").Find(&samples).Error; err != nil {
			return err
		}
		for _, sample := range samples {
			var state RelaySessionState
			if err := db.Where("token_id = ? AND session_id = ?", sample.TokenID, sample.CodexSessionID).First(&state).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if err := updateSessionContextWindow(db, sample, state.ClientFingerprint, true, sample.CreatedAt); err != nil {
				return err
			}
		}

		if err := db.Find(&states).Error; err != nil {
			return err
		}
		for index := range states {
			if states[index].ContextWindowSource == contextWindowSourceSession || states[index].PrimaryModel == "" || states[index].ClientFingerprint == "" {
				continue
			}
			if err := inheritModelAgentContextWindow(db, &states[index], states[index].ClientFingerprint, states[index].PrimaryModel); err != nil {
				return err
			}
		}
		return db.Create(&GatewayMigration{Name: migrationName, AppliedAt: time.Now().UTC()}).Error
	})
}
