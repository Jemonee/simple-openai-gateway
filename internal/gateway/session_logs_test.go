package gateway

import (
	"math"
	"testing"
)

func TestFinishSessionSummaryUsesUpstreamAttemptsForSuccessRate(t *testing.T) {
	summary := SessionLogSummary{
		RequestCount: 94,
		SuccessCount: 94,
		AttemptCount: 97,
	}

	finishSessionSummary(&summary, 0, 0, 0)

	want := float64(94) / float64(97)
	if math.Abs(summary.SuccessRate-want) > 1e-12 {
		t.Fatalf("success rate = %v, want %v", summary.SuccessRate, want)
	}
}

func TestFinishSessionSummaryLeavesSuccessRateZeroWithoutAttempts(t *testing.T) {
	summary := SessionLogSummary{
		RequestCount: 1,
		SuccessCount: 1,
	}

	finishSessionSummary(&summary, 0, 0, 0)

	if summary.SuccessRate != 0 {
		t.Fatalf("success rate = %v, want 0", summary.SuccessRate)
	}
}
