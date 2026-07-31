package gateway

import (
	"context"
	"testing"
)

func TestSaveChannelConfigurationCommitsChannelAndMappingsTogether(t *testing.T) {
	store := newTestStore(t)
	model := GatewayModel{Name: "atomic-public-model", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}

	management := NewManagementService(store)
	created, err := management.SaveChannelConfiguration(context.Background(), ChannelConfigurationInput{
		Channel: ChannelInput{
			Name: "atomic channel", BaseURL: "http://atomic.invalid/v1", APIKey: "secret",
			Enabled: true, SupportsStreamUsage: true,
		},
		Models: []ChannelModelInput{{
			ModelID: model.ID, UpstreamModel: "atomic-upstream-model", Weight: 100, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || len(created.Models) != 1 || created.Models[0].ChannelID != created.ID {
		t.Fatalf("saved channel configuration = %+v", created)
	}

	channels, err := management.ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 || channels[0].ID != created.ID || len(channels[0].Models) != 1 || channels[0].Models[0].ModelID != model.ID {
		t.Fatalf("immediately listed channel configuration = %+v", channels)
	}
}

func TestSaveChannelConfigurationRollsBackChannelWhenMappingFails(t *testing.T) {
	store := newTestStore(t)
	management := NewManagementService(store)

	_, err := management.SaveChannelConfiguration(context.Background(), ChannelConfigurationInput{
		Channel: ChannelInput{
			Name: "rolled back channel", BaseURL: "http://rollback.invalid/v1", APIKey: "secret", Enabled: true,
		},
		Models: []ChannelModelInput{{
			ModelID: 999999, UpstreamModel: "missing-model", Weight: 100, Enabled: true,
		}},
	})
	if err == nil {
		t.Fatal("configuration with a missing public model was accepted")
	}

	var channelCount int64
	if err := store.db.Model(&Channel{}).Where("name = ?", "rolled back channel").Count(&channelCount).Error; err != nil {
		t.Fatal(err)
	}
	if channelCount != 0 {
		t.Fatalf("rolled back channel count = %d, want 0", channelCount)
	}
}

func TestSaveChannelConfigurationRollsBackChannelUpdateWhenMappingFails(t *testing.T) {
	store := newTestStore(t)
	model := GatewayModel{Name: "existing-public-model", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	management := NewManagementService(store)
	created, err := management.SaveChannelConfiguration(context.Background(), ChannelConfigurationInput{
		Channel: ChannelInput{
			Name: "original channel", BaseURL: "http://original.invalid/v1", APIKey: "secret", Enabled: true,
		},
		Models: []ChannelModelInput{{
			ModelID: model.ID, UpstreamModel: "existing-upstream-model", Weight: 100, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = management.SaveChannelConfiguration(context.Background(), ChannelConfigurationInput{
		ID: created.ID,
		Channel: ChannelInput{
			Name: "uncommitted channel", BaseURL: "http://changed.invalid/v1", Enabled: false,
		},
		Models: []ChannelModelInput{{
			ModelID: 999999, UpstreamModel: "missing-model", Weight: 100, Enabled: true,
		}},
	})
	if err == nil {
		t.Fatal("channel update with a missing public model was accepted")
	}

	var stored Channel
	if err := store.db.First(&stored, created.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Name != "original channel" || stored.BaseURL != "http://original.invalid/v1" || !stored.Enabled {
		t.Fatalf("channel update was not rolled back: %+v", stored)
	}
	var mappings []ChannelModel
	if err := store.db.Where("channel_id = ?", created.ID).Find(&mappings).Error; err != nil {
		t.Fatal(err)
	}
	if len(mappings) != 1 || mappings[0].ModelID != model.ID {
		t.Fatalf("channel mappings after rollback = %+v", mappings)
	}
}
