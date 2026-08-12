package gateway

import (
	"strings"
	"testing"
)

func TestVerificationScoreSkipsChecksAndCapsCritical(t *testing.T) {
	results := []VerificationCheckResult{
		{Status: "passed", Score: 100, Weight: 20},
		{Status: "skipped", Score: 0, Weight: 80},
	}
	score, verdict := verificationScore(results)
	if score != 100 || verdict != "passed" {
		t.Fatalf("verificationScore() = %.1f, %q, want 100, passed", score, verdict)
	}

	results[0] = VerificationCheckResult{Status: "failed", Score: 0, Weight: 20, Critical: true}
	score, verdict = verificationScore(results)
	if score != 0 || verdict != "failed" {
		t.Fatalf("critical verificationScore() = %.1f, %q, want 0, failed", score, verdict)
	}

	results = []VerificationCheckResult{
		{Status: "passed", Score: 100, Weight: 80},
		{Status: "failed", Score: 0, Weight: 20, Critical: true},
	}
	score, verdict = verificationScore(results)
	if score != 80 || verdict != "marginal" {
		t.Fatalf("critical cap verificationScore() = %.1f, %q, want 80, marginal", score, verdict)
	}
}

func TestVerificationDetectorsByMode(t *testing.T) {
	tests := []struct {
		mode string
		want int
	}{
		{VerificationModeQuick, 2},
		{VerificationModeStandard, 4},
		{VerificationModeFull, 6},
	}
	for _, test := range tests {
		if got := len(verificationDetectors(test.mode)); got != test.want {
			t.Errorf("verificationDetectors(%q) = %d, want %d", test.mode, got, test.want)
		}
	}
}

func TestUsageNumbers(t *testing.T) {
	in, out, total, ok := usageNumbers(map[string]any{
		"prompt_tokens":     float64(12),
		"completion_tokens": float64(5),
		"total_tokens":      float64(17),
	})
	if !ok || in != 12 || out != 5 || total != 17 {
		t.Fatalf("usageNumbers() = %d, %d, %d, %t", in, out, total, ok)
	}

	in, out, total, ok = usageNumbers(map[string]any{
		"input_tokens":  float64(20),
		"output_tokens": float64(4),
		"total_tokens":  float64(24),
	})
	if !ok || in != 20 || out != 4 || total != 24 {
		t.Fatalf("alternate usageNumbers() = %d, %d, %d, %t", in, out, total, ok)
	}
}

func TestNormalizeVerificationBaseURL(t *testing.T) {
	got, err := normalizeVerificationBaseURL(" https://provider.example/v1/// ")
	if err != nil || got != "https://provider.example/v1" {
		t.Fatalf("normalizeVerificationBaseURL() = %q, %v", got, err)
	}
	if _, err := normalizeVerificationBaseURL("provider.example/v1"); err == nil {
		t.Fatal("normalizeVerificationBaseURL() accepted a URL without a scheme")
	}
}

func TestBoundVerificationBody(t *testing.T) {
	short, truncated := boundVerificationBody([]byte(`{"ok":true}`))
	if truncated || short != `{"ok":true}` {
		t.Fatalf("short body = %q, truncated=%t", short, truncated)
	}
	long, truncated := boundVerificationBody([]byte(strings.Repeat("x", int(verificationAttemptBodyBytes)+10)))
	if !truncated || !strings.HasSuffix(long, "[truncated]") {
		t.Fatalf("long body was not marked truncated")
	}
}
