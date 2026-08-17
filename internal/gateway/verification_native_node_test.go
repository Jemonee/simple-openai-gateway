package gateway

import (
	"errors"
	"os/exec"
	"testing"
)

func TestResolveNativeProbeBackendPrefersNode(t *testing.T) {
	backend, node, err := resolveNativeProbeBackend(func(name string) (string, error) {
		if name != "node" {
			t.Fatalf("lookPath(%q), want node", name)
		}
		return "/opt/node/bin/node", nil
	})
	if err != nil || backend != nativeProbeBackendNode || node != "/opt/node/bin/node" {
		t.Fatalf("resolveNativeProbeBackend() = %v, %q, %v", backend, node, err)
	}
}

func TestResolveNativeProbeBackendFallsBackWhenNodeIsMissing(t *testing.T) {
	backend, node, err := resolveNativeProbeBackend(func(string) (string, error) {
		return "", exec.ErrNotFound
	})
	if err != nil || backend != nativeProbeBackendGo || node != "" {
		t.Fatalf("resolveNativeProbeBackend() = %v, %q, %v", backend, node, err)
	}
}

func TestResolveNativeProbeBackendDoesNotHideLookupErrors(t *testing.T) {
	want := errors.New("permission denied")
	backend, node, err := resolveNativeProbeBackend(func(string) (string, error) {
		return "", want
	})
	if backend != nativeProbeBackendGo || node != "" || !errors.Is(err, want) {
		t.Fatalf("resolveNativeProbeBackend() = %v, %q, %v", backend, node, err)
	}
}

func TestNativeTransportBackendForMode(t *testing.T) {
	backend, err := nativeTransportBackendForMode(VerificationModeMedium, func(string) (string, error) {
		t.Fatal("medium verification must not inspect Node.js")
		return "", nil
	})
	if err != nil || backend != "" {
		t.Fatalf("medium backend = %q, %v", backend, err)
	}

	backend, err = nativeTransportBackendForMode(VerificationModeHigh, func(string) (string, error) {
		return "/opt/node/bin/node", nil
	})
	if err != nil || backend != verificationTransportNodeMJS {
		t.Fatalf("high backend with Node.js = %q, %v", backend, err)
	}

	backend, err = nativeTransportBackendForMode(VerificationModeHigh, func(string) (string, error) {
		return "", exec.ErrNotFound
	})
	if err != nil || backend != verificationTransportGoFallback {
		t.Fatalf("high backend without Node.js = %q, %v", backend, err)
	}
}

func TestVerificationViewsExposeTransportBackend(t *testing.T) {
	session := verificationSessionView(VerificationSession{NativeTransportBackend: verificationTransportGoFallback})
	if session.NativeTransportBackend != verificationTransportGoFallback {
		t.Fatalf("session backend = %q", session.NativeTransportBackend)
	}
	attempt := verificationAttemptView(VerificationAttempt{TransportBackend: verificationTransportGoFallback})
	if attempt.TransportBackend != verificationTransportGoFallback {
		t.Fatalf("attempt backend = %q", attempt.TransportBackend)
	}
}

func TestNativeNodeTransportAssetIsEmbedded(t *testing.T) {
	data, err := verificationAssets.ReadFile("verification_assets/codex-native-transport.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("embedded Node.js transport is empty")
	}
}
