package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

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
