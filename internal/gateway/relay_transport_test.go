package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamRequestURLPreservesProviderPrefixAndQuery(t *testing.T) {
	value, err := upstreamRequestURL("https://provider.example/api/v1/", "responses", "stream=true&include=reasoning.encrypted_content")
	if err != nil {
		t.Fatal(err)
	}
	if value != "https://provider.example/api/v1/responses?stream=true&include=reasoning.encrypted_content" {
		t.Fatalf("upstream URL = %q", value)
	}
	if _, err := upstreamRequestURL("ftp://provider.example/v1", "chat", ""); err == nil {
		t.Fatal("non-HTTP upstream URL was accepted")
	}
}

func TestHTTPClientWithProxySupportsSOCKS5(t *testing.T) {
	client, err := httpClientWithProxy("socks5://user:pass@127.0.0.1:1080", 0)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.DialContext == nil || transport.Proxy != nil {
		t.Fatalf("SOCKS5 transport = %#v", client.Transport)
	}
	if _, err := normalizeProxyURL("socks5h://127.0.0.1:1080"); err != nil {
		t.Fatalf("SOCKS5H normalization failed: %v", err)
	}
}

func TestDynamicResponseHeaderTransportAppliesUpdatedTimeout(t *testing.T) {
	var timeoutNanos atomic.Int64
	timeoutNanos.Store(int64(20 * time.Millisecond))
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	transport := &dynamicResponseHeaderTransport{
		base: http.DefaultTransport.(*http.Transport).Clone(),
		timeoutFunc: func() time.Duration {
			return time.Duration(timeoutNanos.Load())
		},
	}
	client := &http.Client{Transport: transport}

	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	if _, err := client.Do(request); err == nil {
		t.Fatal("request with the initial timeout unexpectedly succeeded")
	}

	timeoutNanos.Store(int64(300 * time.Millisecond))
	request, err = http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request after timeout update failed: %v", err)
	}
	response.Body.Close()
}
