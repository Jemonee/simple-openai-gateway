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

func TestSaveChatGPTOAuthChannelConfigurationUsesReferencePrices(t *testing.T) {
	store := newTestStore(t)
	models := []GatewayModel{
		{Name: "gpt-5.6-sol", RoutingStrategy: RoutingPriorityWeighted, Enabled: true},
		{Name: "gpt-5.6-sol-custom", RoutingStrategy: RoutingPriorityWeighted, Enabled: true},
	}
	if err := store.db.Create(&models).Error; err != nil {
		t.Fatal(err)
	}
	customMultiplier := int64(25_000)
	customCachePrice := int64(7_000_000)

	created, err := NewManagementService(store).SaveChannelConfiguration(context.Background(), ChannelConfigurationInput{
		Channel: ChannelInput{
			Name: "ChatGPT subscription", AuthType: ChannelTypeChatGPTOAuth, OAuthAccessToken: "oauth-secret",
			Enabled: true, SupportsStreamUsage: true, PriceMultiplierBasisPoints: &customMultiplier,
		},
		Models: []ChannelModelInput{
			{
				ModelID: models[0].ID, UpstreamModel: models[0].Name, Weight: 100,
				InputPriceMicros: 99, OutputPriceMicros: 99, CachedInputPriceMicros: &customCachePrice,
				PriceMultiplierBasisPoints: &customMultiplier, Enabled: true,
			},
			{
				ModelID: models[1].ID, UpstreamModel: models[1].Name, Weight: 100,
				InputPriceMicros: 99, OutputPriceMicros: 99, CachedInputPriceMicros: &customCachePrice,
				PriceMultiplierBasisPoints: &customMultiplier, Enabled: true,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.PriceMultiplierBasisPoints != DefaultPriceMultiplierBasisPoints {
		t.Fatalf("channel multiplier = %d, want %d", created.PriceMultiplierBasisPoints, DefaultPriceMultiplierBasisPoints)
	}
	if len(created.Models) != 2 {
		t.Fatalf("saved mappings = %+v", created.Models)
	}
	mappings := make(map[string]ChannelModel, len(created.Models))
	for _, mapping := range created.Models {
		mappings[mapping.UpstreamModel] = mapping
	}
	priced := mappings[models[0].Name]
	if priced.InputPriceMicros != 5_000_000 || priced.OutputPriceMicros != 30_000_000 || priced.CachedInputPriceMicros == nil || *priced.CachedInputPriceMicros != 500_000 || priced.CacheWritePriceMicros != nil || priced.PriceMultiplierBasisPoints != DefaultPriceMultiplierBasisPoints {
		t.Fatalf("reference-priced OAuth mapping = %+v", priced)
	}
	unlisted := mappings[models[1].Name]
	if unlisted.InputPriceMicros != 0 || unlisted.OutputPriceMicros != 0 || unlisted.CachedInputPriceMicros != nil || unlisted.CacheWritePriceMicros != nil || unlisted.PriceMultiplierBasisPoints != DefaultPriceMultiplierBasisPoints {
		t.Fatalf("unlisted OAuth mapping = %+v", unlisted)
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
