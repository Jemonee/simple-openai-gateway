package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRouterPlanWithOptionsPinsRequestedChannel(t *testing.T) {
	store := newTestStore(t)
	token, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	router := NewRouter(store, NewClientAccessService(store), nil)

	plan, err := router.PlanWithOptions(context.Background(), token, model.Name, 10, 10, "", "web-chat-session", RouteOptions{PinnedChannelID: channels[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Mapping.ID != mappings[1].ID {
		t.Fatalf("pinned candidates = %+v", plan.Candidates)
	}
	if plan.InitialSelection.Reason != SelectionReasonManualPin || plan.InitialSelection.Detail != channels[1].Name {
		t.Fatalf("pinned selection = %+v", plan.InitialSelection)
	}

	if err := store.db.Model(&ChannelModel{}).Where("id = ?", mappings[1].ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	_, err = router.PlanWithOptions(context.Background(), token, model.Name, 10, 10, "", "web-chat-session", RouteOptions{PinnedChannelID: channels[1].ID})
	if !errors.Is(err, ErrPinnedChannelUnavailable) {
		t.Fatalf("disabled pinned channel error = %v", err)
	}
}

func TestRouterPlanEnforcesTokenChannelScopeAcrossRoutingModes(t *testing.T) {
	store := newTestStore(t)
	token, model, channels, mappings := createRouteFixture(t, store, RoutingPriorityWeighted, "http://one.invalid", "http://two.invalid")
	token.AllowAllChannels = false
	if err := store.db.Save(token).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&ClientTokenChannel{TokenID: token.ID, ChannelID: channels[1].ID}).Error; err != nil {
		t.Fatal(err)
	}
	router := NewRouter(store, NewClientAccessService(store), nil)

	plan, err := router.Plan(context.Background(), token, model.Name, 10, 10, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Channel.ID != channels[1].ID {
		t.Fatalf("channel-scoped candidates = %+v", plan.Candidates)
	}

	_, err = router.PlanWithOptions(context.Background(), token, model.Name, 10, 10, "", "", RouteOptions{PinnedChannelID: channels[0].ID})
	if !errors.Is(err, ErrPinnedChannelUnavailable) {
		t.Fatalf("disallowed pinned channel error = %v", err)
	}

	affinity := ResponseAffinity{ResponseHash: hashSecret("resp-disallowed"), ChannelModelID: mappings[0].ID, ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.db.Create(&affinity).Error; err != nil {
		t.Fatal(err)
	}
	_, err = router.Plan(context.Background(), token, model.Name, 10, 10, "resp-disallowed", "")
	if !errors.Is(err, ErrAffinityUnavailable) {
		t.Fatalf("disallowed response affinity error = %v", err)
	}
}

func TestRelayPayloadConfigureSessionPreservesWebChatIdentity(t *testing.T) {
	payload, err := ParseRelayPayload([]byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	payload.ConfigureSession("web-chat-1", "web_chat", "web_chat", "user")
	resolution, err := (&Store{}).resolveAgentSession(context.Background(), agentSessionRequest{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	applyAgentSessionResolution(payload, resolution)
	if payload.SessionKey != "web-chat-1" || payload.LogSessionKey != "web-chat-1" || payload.ClientKind != agentClientKindWebChat || payload.ThreadSource != codexThreadSourceUser {
		t.Fatalf("web chat payload = %+v", payload)
	}
}
