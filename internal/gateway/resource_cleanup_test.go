package gateway

import (
	"context"
	"testing"
	"time"
)

func TestCleanupResourcesBeforeAgeDeletesOnlyOlderResourcesAndReferences(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	resources := []GatewayResource{
		{Fingerprint: "old-resource", Kind: "file", LastSeenAt: now.Add(-8 * 24 * time.Hour)},
		{Fingerprint: "recent-resource", Kind: "url", LastSeenAt: now.Add(-6 * 24 * time.Hour)},
		{Fingerprint: "boundary-resource", Kind: "code", LastSeenAt: now.Add(-7 * 24 * time.Hour)},
	}
	if err := store.db.Create(&resources).Error; err != nil {
		t.Fatal(err)
	}
	references := []RequestResource{
		{RequestID: "old-request-1", ResourceID: resources[0].ID, Location: "$.input[0]", Role: "user"},
		{RequestID: "old-request-2", ResourceID: resources[0].ID, Location: "$.input[1]", Role: "user"},
		{RequestID: "recent-request", ResourceID: resources[1].ID, Location: "$.input[0]", Role: "user"},
		{RequestID: "boundary-request", ResourceID: resources[2].ID, Location: "$.input[0]", Role: "user"},
	}
	if err := store.db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}

	result, err := NewManagementService(store).cleanupResourcesBeforeAge(context.Background(), now, 7*24*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourcesDeleted != 1 || result.ReferencesDeleted != 2 {
		t.Fatalf("cleanup result = %+v", result)
	}
	if !result.CutoffAt.Equal(resources[2].LastSeenAt) {
		t.Fatalf("cutoff = %s, want %s", result.CutoffAt, resources[2].LastSeenAt)
	}

	var resourceCount int64
	if err := store.db.Model(&GatewayResource{}).Count(&resourceCount).Error; err != nil || resourceCount != 2 {
		t.Fatalf("resource count = %d, err = %v", resourceCount, err)
	}
	var oldResourceCount int64
	if err := store.db.Model(&GatewayResource{}).Where("id = ?", resources[0].ID).Count(&oldResourceCount).Error; err != nil || oldResourceCount != 0 {
		t.Fatalf("old resource count = %d, err = %v", oldResourceCount, err)
	}
	var referenceCount int64
	if err := store.db.Model(&RequestResource{}).Count(&referenceCount).Error; err != nil || referenceCount != 2 {
		t.Fatalf("reference count = %d, err = %v", referenceCount, err)
	}
	var keptReferenceCount int64
	if err := store.db.Model(&RequestResource{}).Where("resource_id IN ?", []uint64{resources[1].ID, resources[2].ID}).Count(&keptReferenceCount).Error; err != nil || keptReferenceCount != 2 {
		t.Fatalf("kept reference count = %d, err = %v", keptReferenceCount, err)
	}
}

func TestCleanupResourcesBeforeAgeReturnsZeroForEmptySelection(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	resource := GatewayResource{Fingerprint: "kept-resource", Kind: "file", LastSeenAt: now}
	if err := store.db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}

	result, err := NewManagementService(store).cleanupResourcesBeforeAge(context.Background(), now, 7*24*time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourcesDeleted != 0 || result.ReferencesDeleted != 0 {
		t.Fatalf("cleanup result = %+v", result)
	}
}

func TestCleanupResourcesBeforeRejectsNonPositiveDays(t *testing.T) {
	store := newTestStore(t)
	_, err := NewManagementService(store).CleanupResourcesBefore(context.Background(), 0, "")
	if err == nil {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestCleanupResourcesBeforeAgeFiltersByKind(t *testing.T) {
	store := newTestStore(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	resources := []GatewayResource{
		{Fingerprint: "old-file", Kind: "file", LastSeenAt: now.Add(-8 * 24 * time.Hour)},
		{Fingerprint: "old-url", Kind: "url", LastSeenAt: now.Add(-8 * 24 * time.Hour)},
	}
	if err := store.db.Create(&resources).Error; err != nil {
		t.Fatal(err)
	}
	references := []RequestResource{
		{RequestID: "file-request", ResourceID: resources[0].ID, Location: "$.input[0]", Role: "user"},
		{RequestID: "url-request", ResourceID: resources[1].ID, Location: "$.input[0]", Role: "user"},
	}
	if err := store.db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}

	result, err := NewManagementService(store).cleanupResourcesBeforeAge(context.Background(), now, 7*24*time.Hour, " FILE ")
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourcesDeleted != 1 || result.ReferencesDeleted != 1 {
		t.Fatalf("cleanup result = %+v", result)
	}

	var keptResources []GatewayResource
	if err := store.db.Order("id ASC").Find(&keptResources).Error; err != nil {
		t.Fatal(err)
	}
	if len(keptResources) != 1 || keptResources[0].Kind != "url" {
		t.Fatalf("kept resources = %+v", keptResources)
	}
	var keptReferences []RequestResource
	if err := store.db.Find(&keptReferences).Error; err != nil {
		t.Fatal(err)
	}
	if len(keptReferences) != 1 || keptReferences[0].ResourceID != resources[1].ID {
		t.Fatalf("kept references = %+v", keptReferences)
	}
}
