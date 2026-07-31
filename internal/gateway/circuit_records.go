package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	CircuitResolutionAutomaticRecovery = "automatic_recovery"
	CircuitResolutionEscalated         = "escalated"
	CircuitResolutionManualReopen      = "manual_reopen"
	CircuitResolutionMappingRemoved    = "mapping_removed"
	CircuitResolutionManualReset       = "manual_reset"
)

type CircuitRecordQuery struct {
	ChannelID uint64
	Level     int
	Status    string
	Page      int
	PageSize  int
}

type CircuitRecordView struct {
	CircuitRecord
	MappingExists          bool `gorm:"column:mapping_exists" json:"mappingExists"`
	MappingEnabled         bool `gorm:"column:mapping_enabled" json:"mappingEnabled"`
	MappingCircuitDisabled bool `gorm:"column:mapping_circuit_disabled" json:"mappingCircuitDisabled"`
}

type CircuitRecordPage struct {
	Items         []CircuitRecordView `json:"items"`
	Total         int64               `json:"total"`
	PendingManual int64               `json:"pendingManual"`
	Page          int                 `json:"page"`
	PageSize      int                 `json:"pageSize"`
}

func createCircuitRecord(db *gorm.DB, channel Channel, channelModelID uint64, level int, failureCount int, immediate bool, openUntil *time.Time, message string) error {
	var mapping ChannelModel
	if err := db.Where("id = ? AND channel_id = ?", channelModelID, channel.ID).First(&mapping).Error; err != nil {
		return err
	}
	var model GatewayModel
	if err := db.First(&model, mapping.ModelID).Error; err != nil {
		return err
	}
	return db.Create(&CircuitRecord{
		ChannelID:      channel.ID,
		ChannelModelID: mapping.ID,
		ModelID:        mapping.ModelID,
		ChannelName:    channel.Name,
		ModelName:      model.Name,
		UpstreamModel:  mapping.UpstreamModel,
		Level:          level,
		FailureCount:   failureCount,
		Immediate:      immediate,
		Message:        truncateRunes(message, 2000),
		OpenUntil:      openUntil,
		CreatedAt:      time.Now(),
	}).Error
}

func resolveCircuitRecords(db *gorm.DB, channelID uint64, channelModelID uint64, level int, resolution string, resolvedAt time.Time) error {
	query := db.Model(&CircuitRecord{}).Where("resolved_at IS NULL")
	if channelID > 0 {
		query = query.Where("channel_id = ?", channelID)
	}
	if channelModelID > 0 {
		query = query.Where("channel_model_id = ?", channelModelID)
	}
	if level > 0 {
		query = query.Where("level = ?", level)
	}
	return query.Updates(map[string]any{"resolved_at": resolvedAt, "resolution": resolution}).Error
}

func (s *ManagementService) CircuitRecords(ctx context.Context, query CircuitRecordQuery) (*CircuitRecordPage, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 || query.PageSize > 200 {
		query.PageSize = 50
	}
	filtered := func() *gorm.DB {
		db := s.store.db.WithContext(ctx).Table("circuit_records AS records")
		if query.ChannelID > 0 {
			db = db.Where("records.channel_id = ?", query.ChannelID)
		}
		if query.Level >= CircuitLevelTemporary && query.Level <= CircuitLevelManual {
			db = db.Where("records.level = ?", query.Level)
		}
		switch strings.TrimSpace(query.Status) {
		case "pending":
			db = db.Where("records.resolved_at IS NULL")
		case "resolved":
			db = db.Where("records.resolved_at IS NOT NULL")
		}
		return db
	}
	var total int64
	if err := filtered().Count(&total).Error; err != nil {
		return nil, err
	}
	var pendingManual int64
	if err := s.store.db.WithContext(ctx).Model(&CircuitRecord{}).
		Where("level = ? AND resolved_at IS NULL", CircuitLevelManual).
		Count(&pendingManual).Error; err != nil {
		return nil, err
	}
	var items []CircuitRecordView
	if err := filtered().Select("records.*, CASE WHEN mappings.id IS NULL THEN 0 ELSE 1 END AS mapping_exists, COALESCE(mappings.enabled, 0) AS mapping_enabled, COALESCE(mappings.circuit_disabled, 0) AS mapping_circuit_disabled").
		Joins("LEFT JOIN channel_models AS mappings ON mappings.id = records.channel_model_id").
		Order("records.created_at DESC, records.id DESC").
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).
		Scan(&items).Error; err != nil {
		return nil, err
	}
	if items == nil {
		items = []CircuitRecordView{}
	}
	return &CircuitRecordPage{Items: items, Total: total, PendingManual: pendingManual, Page: query.Page, PageSize: query.PageSize}, nil
}

func (s *ManagementService) ReopenCircuitMapping(ctx context.Context, recordID uint64) error {
	return s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		var record CircuitRecord
		if err := db.First(&record, recordID).Error; err != nil {
			return err
		}
		if record.Level != CircuitLevelManual {
			return errors.New("only level-three circuit records can reopen a mapping")
		}
		if record.ResolvedAt != nil {
			return nil
		}
		var mapping ChannelModel
		if err := db.Where("id = ? AND channel_id = ?", record.ChannelModelID, record.ChannelID).First(&mapping).Error; err != nil {
			return err
		}
		if !mapping.CircuitDisabled {
			return errors.New("mapping is not disabled by the circuit breaker")
		}
		if err := db.Model(&mapping).Updates(map[string]any{"enabled": true, "circuit_disabled": false}).Error; err != nil {
			return err
		}
		return resolveCircuitRecords(db, record.ChannelID, record.ChannelModelID, CircuitLevelManual, CircuitResolutionManualReopen, time.Now())
	})
}
