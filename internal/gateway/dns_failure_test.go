package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRelayReturnsDNSUnavailableWithoutOpeningCircuits(t *testing.T) {
	store := newTestStore(t)
	token, _, channels, mappings := createRouteFixture(
		t,
		store,
		RoutingPriorityWeighted,
		"http://one.invalid",
		"http://two.invalid",
		"http://three.invalid",
	)
	relay := newTestRelay(store)
	transportCalls := 0
	relay.client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		transportCalls++
		return nil, &net.DNSError{Err: "no such host", Name: request.URL.Hostname(), IsNotFound: true}
	})

	body := []byte(`{"model":"public-model","input":"hello"}`)
	payload, err := ParseRelayPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	publicErr := relay.Relay(context.Background(), httptest.NewRecorder(), nil, "", "responses", token, payload, body)
	if publicErr == nil || publicErr.Status != http.StatusServiceUnavailable || publicErr.Code != "upstream_dns_unavailable" {
		t.Fatalf("Relay() error = %+v", publicErr)
	}
	if transportCalls != 3 {
		t.Fatalf("transport calls = %d, want 3", transportCalls)
	}
	if attempts := relayAttempts(t, store); len(attempts) != 3 {
		t.Fatalf("attempt count = %d, want 3", len(attempts))
	}

	for _, original := range channels {
		var channel Channel
		if err := store.db.First(&channel, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !channel.Enabled || channel.ConsecutiveFailures != 0 || channel.CircuitLevel != CircuitLevelClosed || channel.CircuitOpenUntil != nil {
			t.Fatalf("DNS failure changed channel circuit state: %+v", channel)
		}
	}
	for _, original := range mappings {
		var mapping ChannelModel
		if err := store.db.First(&mapping, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !mapping.Enabled || mapping.CircuitDisabled {
			t.Fatalf("DNS failure disabled mapping: %+v", mapping)
		}
	}
}
