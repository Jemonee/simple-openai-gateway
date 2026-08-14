package gateway

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

type ResourceCleanupInput struct {
	Days int    `json:"days"`
	Kind string `json:"kind"`
}

type ResourceCleanupResult struct {
	CutoffAt          time.Time `json:"cutoffAt"`
	ResourcesDeleted  int64     `json:"resourcesDeleted"`
	ReferencesDeleted int64     `json:"referencesDeleted"`
}

func (s *ManagementService) CleanupResourcesBefore(ctx context.Context, days int, kind string) (*ResourceCleanupResult, error) {
	if days <= 0 {
		return nil, errors.New("资源清理天数必须为正整数")
	}
	return s.cleanupResourcesBeforeAge(ctx, time.Now().UTC(), time.Duration(days)*24*time.Hour, kind)
}

func (s *ManagementService) cleanupResourcesBeforeAge(ctx context.Context, now time.Time, age time.Duration, kind string) (*ResourceCleanupResult, error) {
	cutoff := now.UTC().Add(-age)
	kind = strings.ToLower(strings.TrimSpace(kind))
	result := &ResourceCleanupResult{CutoffAt: cutoff}
	err := s.store.db.WithContext(ctx).Transaction(func(db *gorm.DB) error {
		resourceIDs := db.Model(&GatewayResource{}).Select("id").Where("last_seen_at < ?", cutoff)
		if kind != "" {
			resourceIDs = resourceIDs.Where("kind = ?", kind)
		}

		var count int64
		if err := db.Model(&RequestResource{}).Where("resource_id IN (?)", resourceIDs).Count(&count).Error; err != nil {
			return err
		}
		result.ReferencesDeleted = count
		if err := db.Where("resource_id IN (?)", resourceIDs).Delete(&RequestResource{}).Error; err != nil {
			return err
		}

		resources := db.Model(&GatewayResource{}).Where("last_seen_at < ?", cutoff)
		if kind != "" {
			resources = resources.Where("kind = ?", kind)
		}
		if err := resources.Count(&count).Error; err != nil {
			return err
		}
		result.ResourcesDeleted = count
		resources = db.Where("last_seen_at < ?", cutoff)
		if kind != "" {
			resources = resources.Where("kind = ?", kind)
		}
		return resources.Delete(&GatewayResource{}).Error
	})
	if err != nil {
		return nil, err
	}
	s.store.checkpointSQLiteWAL()
	return result, nil
}
