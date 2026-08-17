package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Jemonee/simple-openai-gateway/pkg/until"
)

type nativeProbeBackend uint8

const (
	nativeProbeBackendGo nativeProbeBackend = iota
	nativeProbeBackendNode

	verificationTransportGoHTTP     = "go_http"
	verificationTransportNodeMJS    = "node_mjs"
	verificationTransportGoFallback = "go_fallback"
)

func resolveNativeProbeBackend(lookPath func(string) (string, error)) (nativeProbeBackend, string, error) {
	node, err := lookPath("node")
	if err == nil {
		return nativeProbeBackendNode, node, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nativeProbeBackendGo, "", nil
	}
	return nativeProbeBackendGo, "", fmt.Errorf("locate Node.js for Native Codex verification: %w", err)
}

func nativeTransportBackendForMode(mode string, lookPath func(string) (string, error)) (string, error) {
	if mode != VerificationModeHigh {
		return "", nil
	}
	backend, _, err := resolveNativeProbeBackend(lookPath)
	if err != nil {
		return "", err
	}
	if backend == nativeProbeBackendGo {
		return verificationTransportGoFallback, nil
	}
	return verificationTransportNodeMJS, nil
}

func logNativeGoFallback(sessionID, model, reason string) {
	until.Log.Warnf("IMPORTANT: Native Codex verification is using the pure Go fallback; TLS transport fingerprint differs from the default Node.js path: session_id=%s model=%s reason=%s", sessionID, model, reason)
}

func (s *VerificationService) markNativeGoFallback(sessionID, model, reason string) {
	update := s.store.db.Model(&VerificationSession{}).
		Where("id = ? AND native_transport_backend <> ?", sessionID, verificationTransportGoFallback).
		Update("native_transport_backend", verificationTransportGoFallback)
	if update.Error != nil {
		until.Log.WithError(update.Error).Warnf("IMPORTANT: Native Codex verification is using the pure Go fallback, but the session marker could not be persisted: session_id=%s model=%s reason=%s", sessionID, model, reason)
		return
	}
	if update.RowsAffected > 0 {
		logNativeGoFallback(sessionID, model, reason)
	}
}

func (s *VerificationService) markNativeNodeTransport(sessionID string) {
	_ = s.store.db.Model(&VerificationSession{}).
		Where("id = ? AND native_transport_backend = ''", sessionID).
		Update("native_transport_backend", verificationTransportNodeMJS).Error
}

func (s *VerificationService) performNativeProbe(ctx context.Context, baseURL, apiKey, model, sessionID, preferredBackend string, manifest verificationJobManifest) (verificationTransportResult, []byte, []byte, verificationTransportMeta, error) {
	if preferredBackend == verificationTransportGoFallback {
		return s.performNativeProbeGo(ctx, baseURL, apiKey, model, sessionID, manifest)
	}
	backend, node, err := resolveNativeProbeBackend(exec.LookPath)
	if err != nil {
		return verificationTransportResult{}, nil, nil, verificationTransportMeta{Path: "/responses", Stream: true, RequestFormat: "native_codex", TransportBackend: preferredBackend}, err
	}
	if backend == nativeProbeBackendGo {
		s.markNativeGoFallback(sessionID, model, "node executable disappeared after the verification session started")
		return s.performNativeProbeGo(ctx, baseURL, apiKey, model, sessionID, manifest)
	}
	s.markNativeNodeTransport(sessionID)
	return s.performNativeProbeNode(ctx, node, baseURL, apiKey, model, manifest)
}

func (s *VerificationService) materializeNativeAssets() (string, error) {
	s.assetsOnce.Do(func() {
		dir, err := os.MkdirTemp("", "simple-openai-gateway-gpt56-")
		if err != nil {
			s.assetsErr = err
			return
		}
		for _, name := range []string{"codex-native-transport.mjs", "fixed_32k_history.json", "native-0.147.0.raw"} {
			data, err := verificationAssets.ReadFile("verification_assets/" + name)
			if err != nil {
				s.assetsErr = err
				return
			}
			if name == "native-0.147.0.raw" && !bytes.Contains(data, []byte("\r\n\r\n")) {
				data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				s.assetsErr = err
				return
			}
		}
		s.assetsDir = dir
	})
	return s.assetsDir, s.assetsErr
}

func (s *VerificationService) performNativeProbeNode(ctx context.Context, node, baseURL, apiKey, model string, manifest verificationJobManifest) (verificationTransportResult, []byte, []byte, verificationTransportMeta, error) {
	meta := verificationTransportMeta{Path: "/responses", Stream: true, RequestFormat: "native_codex", TransportBackend: verificationTransportNodeMJS}
	dir, err := s.materializeNativeAssets()
	if err != nil {
		return verificationTransportResult{}, nil, nil, meta, err
	}
	messages, _ := json.Marshal(manifest.Messages)
	args := []string{filepath.Join(dir, "codex-native-transport.mjs"), "send-native", "--profile", filepath.Join(dir, "native-0.147.0.raw"), "--url", baseURL + "/responses", "--model", model, "--timeout", "300000", "--input-json", "true", "--effort", manifest.Effort, "--capture-sanitized-request", "false"}
	if manifest.ContextMode == "fixed_32k_history" {
		args = append(args, "--history-file", filepath.Join(dir, "fixed_32k_history.json"))
	}
	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Stdin = bytes.NewReader(messages)
	cmd.Env = verificationCommandEnv(apiKey)
	started := time.Now()
	output, err := cmd.Output()
	meta.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		return verificationTransportResult{}, messages, output, meta, fmt.Errorf("native Codex transport failed: %w", err)
	}
	var report struct {
		Status  int                 `json:"status"`
		Body    string              `json:"body"`
		Headers map[string][]string `json:"headers"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		return verificationTransportResult{}, messages, output, meta, err
	}
	meta.Status = report.Status
	raw := []byte(report.Body)
	if values := report.Headers["content-type"]; len(values) > 0 {
		meta.ContentType = values[0]
	}
	if report.Status < 200 || report.Status >= 300 {
		return verificationTransportResult{}, messages, raw, meta, fmt.Errorf("upstream returned HTTP %d", report.Status)
	}
	parsed, err := parseResponsesSSE(raw)
	if err != nil {
		return verificationTransportResult{}, messages, raw, meta, err
	}
	parsed.UpstreamCost, parsed.UpstreamCostOK = ParseUpstreamCostMicros(raw)
	return parsed, messages, raw, meta, nil
}

func verificationCommandEnv(apiKey string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GPT56_NATIVE_AUTHORIZATION=") {
			env = append(env, entry)
		}
	}
	return append(env, "GPT56_NATIVE_AUTHORIZATION="+apiKey)
}
