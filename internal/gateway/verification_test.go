package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"time"
)

func TestVerificationPresetsMatchV410LogicalRequestCounts(t *testing.T) {
	tests := []struct {
		mode string
		want int
	}{
		{VerificationModeLow, 14},
		{VerificationModeMedium, 64},
		{VerificationModeHigh, 202},
	}
	for _, test := range tests {
		jobs, err := buildVerificationJobs(test.mode)
		if err != nil {
			t.Fatalf("buildVerificationJobs(%q): %v", test.mode, err)
		}
		if got := len(jobs); got != test.want {
			t.Errorf("buildVerificationJobs(%q) = %d, want %d", test.mode, got, test.want)
		}
	}
}

func TestJuiceClassificationHonorsSharedV410Signatures(t *testing.T) {
	manifest := verificationJobManifest{ProbeID: "juice_low", Effort: "low"}
	result := classifyVerificationResult(manifest, "gpt-5.6-sol", "8")
	if result.Classification != "current_success" || !containsString(result.SharedWithModels, "gpt-5.6-luna") {
		t.Fatalf("shared low signature = %#v", result)
	}
	manifest.Effort = "high"
	result = classifyVerificationResult(manifest, "gpt-5.6-sol", "48")
	if result.Classification != "mixed" || !containsString(result.MixedModels, "gpt-5.6-luna") {
		t.Fatalf("Luna high signature = %#v", result)
	}
}

func TestOutputAndCoverageHardAnomalies(t *testing.T) {
	output := classifyVerificationResult(verificationJobManifest{ProbeID: "output_luna_48", Expected: "48"}, "gpt-5.6-sol", "40085")
	if output.Classification != "output_rewrite_40_prefix" || !output.HardAnomaly {
		t.Fatalf("output anomaly = %#v", output)
	}
	coverage := classifyVerificationResult(verificationJobManifest{ProbeID: "juice_coverage", SyntheticValue: 54321}, "gpt-5.6-sol", "40")
	if coverage.Classification != "explicit_hidden_override" || !coverage.HardAnomaly {
		t.Fatalf("coverage anomaly = %#v", coverage)
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
	got, err = normalizeVerificationBaseURL("https://provider.example")
	if err != nil || got != "https://provider.example/v1" {
		t.Fatalf("normalizeVerificationBaseURL(root) = %q, %v", got, err)
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

func TestFingerprintBaselineIdentityAndCells(t *testing.T) {
	data, err := verificationAssets.ReadFile("verification_assets/trusted_fingerprint_v3.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != verificationBaselineSHA256 {
		t.Fatalf("baseline file hash = %s, want %s", got, verificationBaselineSHA256)
	}
	baseline, err := loadFingerprintBaseline()
	if err != nil {
		t.Fatal(err)
	}
	if baseline.BaselineID != "stage-c-trusted-fingerprint-v3" || baseline.ContentSHA256 != "f4df50f1601c7ac4a81635c24032756a235f5c7cb2695d046cee30d2b482ed90" {
		t.Fatalf("unexpected baseline identity: %#v", baseline)
	}
	if len(baseline.Cells) != 12 {
		t.Fatalf("baseline cells = %d, want 12", len(baseline.Cells))
	}
}

func TestFingerprintCompletionGateUsesNinetyPercent(t *testing.T) {
	manifests, results := syntheticMediumFingerprint(18, 9)
	summary, err := scoreFingerprint(VerificationModeMedium, manifests, results)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.OfficialEligible {
		t.Fatalf("90%% completion should be eligible: %#v", summary.UnclearReasons)
	}
	if summary.Cells["rand_country|normal+no_history"].Minimum != 18 || summary.Cells["b80_letter_count|normal+no_history"].Minimum != 9 {
		t.Fatalf("unexpected completion minima: %#v", summary.Cells)
	}

	manifests, results = syntheticMediumFingerprint(17, 9)
	summary, err = scoreFingerprint(VerificationModeMedium, manifests, results)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OfficialEligible || !containsString(summary.UnclearReasons, "candidate_samples_incomplete") {
		t.Fatalf("below 90%% completion must be ineligible: %#v", summary)
	}
}

func TestFingerprintThresholdIsStrictlyGreater(t *testing.T) {
	thresholds := map[string]float64{"gpt-5.6-sol": .70, "gpt-5.6-terra": .75, "gpt-5.6-luna": .90}
	matches := map[string]float64{"gpt-5.6-sol": .70, "gpt-5.6-terra": .20, "gpt-5.6-luna": .10}
	if winners := fingerprintThresholdWinners(matches, thresholds, true); len(winners) != 0 {
		t.Fatalf("match equal to threshold must not win: %#v", winners)
	}
	matches["gpt-5.6-sol"] = .7000001
	if winners := fingerprintThresholdWinners(matches, thresholds, true); len(winners) != 1 || winners[0] != "gpt-5.6-sol" {
		t.Fatalf("match above threshold should win: %#v", winners)
	}
	if winners := fingerprintThresholdWinners(matches, thresholds, false); len(winners) != 0 {
		t.Fatalf("ineligible fingerprint must not win: %#v", winners)
	}
}

func syntheticMediumFingerprint(countryCount, b80Count int) (map[string]verificationJobManifest, map[string]verificationJobResult) {
	manifests := map[string]verificationJobManifest{}
	results := map[string]verificationJobResult{}
	add := func(prefix, probe, category string, count int) {
		for index := 0; index < count; index++ {
			id := prefix + string(rune('a'+index))
			manifests[id] = verificationJobManifest{ProbeID: probe, RequestFormat: "normal", ContextMode: "no_history"}
			results[id] = verificationJobResult{Status: "ok", Classification: "category", Category: category}
		}
	}
	add("country-", "rand_country", "uruguay", countryCount)
	add("b80-", "b80_letter_count", "exact_3", b80Count)
	return manifests, results
}

func TestParseResponsesSSEContracts(t *testing.T) {
	completed := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"48"}`,
		`data: {"type":"response.completed","response":{"output_text":"48","usage":{"input_tokens":12,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}}}`,
		"",
	}, "\n")
	result, err := parseResponsesSSE([]byte(completed))
	if err != nil || result.Answer != "48" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 2 || result.Usage.CachedTokens != 3 {
		t.Fatalf("completed SSE = %#v, %v", result, err)
	}

	tests := []struct {
		name string
		body string
		want string
	}{
		{"failed", `data: {"type":"response.failed","response":{}}`, "response.failed"},
		{"incomplete", `data: {"type":"response.incomplete","response":{}}`, "response.incomplete"},
		{"missing terminal", `data: {"type":"response.output_text.delta","delta":"48"}`, "without a terminal event"},
		{"delta conflict", strings.Join([]string{`data: {"type":"response.output_text.delta","delta":"48"}`, `data: {"type":"response.completed","response":{"output_text":"32"}}`}, "\n"), "conflicts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseResponsesSSE([]byte(test.body))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseResponsesSSE() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReadVerificationSSEReturnsAtTerminalWithoutEOF(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	result := make(chan struct {
		body []byte
		err  error
	}, 1)
	go func() {
		body, err := readVerificationSSE(reader)
		result <- struct {
			body []byte
			err  error
		}{body: body, err: err}
	}()
	if _, err := io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"output_text\":\"48\"}}\n\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		parsed, err := parseResponsesSSE(got.body)
		if err != nil || parsed.Answer != "48" {
			t.Fatalf("terminal read = %#v, %v", parsed, err)
		}
	case <-time.After(time.Second):
		t.Fatal("reader waited for EOF after terminal event")
	}
}

func TestCancelRemainingJobsFinalizesIdempotently(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationReport{}, &VerificationSession{}, &VerificationJob{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := VerificationSession{ID: "session-idempotent", ChannelID: 1, ChannelName: "QA", ModelID: 2, PublicModel: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", Mode: VerificationModeMedium, Status: "stopping", PlannedCount: 2, StopRequestedAt: &now, StartedAt: now.Add(-time.Minute)}
	if err := store.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	jobs := []VerificationJob{
		{ID: "job-pending", SessionID: session.ID, Sequence: 1, ProbeID: "rand_country", RequestFormat: "normal", ContextMode: "no_history", Effort: "low", Status: "pending", ManifestJSON: `{}`, CreatedAt: now, UpdatedAt: now},
		{ID: "job-running", SessionID: session.ID, Sequence: 2, ProbeID: "rand_country", RequestFormat: "normal", ContextMode: "no_history", Effort: "low", Status: "running", ManifestJSON: `{}`, CreatedAt: now, UpdatedAt: now},
	}
	if err := store.db.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}
	report := VerificationReport{ID: "report-existing", SessionID: session.ID, ChannelID: session.ChannelID, ChannelName: session.ChannelName, ModelID: session.ModelID, PublicModel: session.PublicModel, UpstreamModel: session.UpstreamModel, Mode: session.Mode, Verdict: "juice_pass_fingerprint_unclear", Summary: "existing", ChecksJSON: `[]`, StartedAt: session.StartedAt, FinishedAt: now}
	if err := store.db.Create(&report).Error; err != nil {
		t.Fatal(err)
	}
	service := &VerificationService{store: store}
	if err := service.cancelRemainingJobsAndFinalize(session.ID, session.PublicModel); err != nil {
		t.Fatal(err)
	}
	if err := service.finalizeSession(session.ID, session.PublicModel); err != nil {
		t.Fatal(err)
	}
	var reportCount int64
	if err := store.db.Model(&VerificationReport{}).Where("session_id = ?", session.ID).Count(&reportCount).Error; err != nil {
		t.Fatal(err)
	}
	if reportCount != 1 {
		t.Fatalf("report count = %d, want 1", reportCount)
	}
	var cancelledCount int64
	if err := store.db.Model(&VerificationJob{}).Where("session_id = ? AND status = ?", session.ID, "cancelled").Count(&cancelledCount).Error; err != nil {
		t.Fatal(err)
	}
	if cancelledCount != 2 {
		t.Fatalf("cancelled jobs = %d, want 2", cancelledCount)
	}
	if err := store.db.First(&session, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if session.Status != "stopped" || session.CompletedCount != 2 || session.Verdict != report.Verdict {
		t.Fatalf("finalized session = %#v", session)
	}
}
