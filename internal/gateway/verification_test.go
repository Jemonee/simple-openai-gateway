package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestVerificationPresetsMatchV411LogicalRequestCounts(t *testing.T) {
	tests := []struct {
		mode string
		want int
	}{
		{VerificationModeLow, 19},
		{VerificationModeMedium, 49},
		{VerificationModeHigh, 158},
	}
	for _, test := range tests {
		jobs, err := buildVerificationJobs(test.mode)
		if err != nil {
			t.Fatalf("buildVerificationJobs(%q): %v", test.mode, err)
		}
		if got := len(jobs); got != test.want {
			t.Errorf("buildVerificationJobs(%q) = %d, want %d", test.mode, got, test.want)
		}
		manifests := make(map[string]verificationJobManifest, len(jobs))
		for index, job := range jobs {
			manifests[fmt.Sprintf("job-%03d", index)] = job
		}
		if !matchesOfficialFingerprintPolicy(test.mode, manifests) {
			t.Errorf("buildVerificationJobs(%q) does not match the v4.1.1 runtime policy", test.mode)
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
	manifests, results := syntheticFingerprint(t, VerificationModeMedium, map[string]int{"rand_country": 9, "rand_bird": 9, "b80_letter_count": 9})
	summary, err := scoreFingerprint(VerificationModeMedium, manifests, results)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.OfficialEligible {
		t.Fatalf("90%% completion should be eligible: %#v", summary.UnclearReasons)
	}
	if summary.Cells["rand_country|normal+no_history"].Minimum != 9 || summary.Cells["rand_bird|normal+no_history"].Minimum != 9 || summary.Cells["b80_letter_count|normal+no_history"].Minimum != 9 {
		t.Fatalf("unexpected completion minima: %#v", summary.Cells)
	}

	manifests, results = syntheticFingerprint(t, VerificationModeMedium, map[string]int{"rand_country": 8, "rand_bird": 9, "b80_letter_count": 9})
	summary, err = scoreFingerprint(VerificationModeMedium, manifests, results)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OfficialEligible || !containsString(summary.UnclearReasons, "candidate_samples_incomplete") {
		t.Fatalf("below 90%% completion must be ineligible: %#v", summary)
	}
}

func TestFingerprintThresholdIsStrictlyGreater(t *testing.T) {
	thresholds := fingerprintThresholds(VerificationModeMedium)
	matches := map[string]float64{"gpt-5.6-sol": .82, "gpt-5.6-terra": .10, "gpt-5.6-luna": .08}
	if winners := fingerprintThresholdWinners(matches, thresholds, true); len(winners) != 0 {
		t.Fatalf("match equal to threshold must not win: %#v", winners)
	}
	matches["gpt-5.6-sol"] = .8200001
	if winners := fingerprintThresholdWinners(matches, thresholds, true); len(winners) != 1 || winners[0] != "gpt-5.6-sol" {
		t.Fatalf("match above threshold should win: %#v", winners)
	}
	if winners := fingerprintThresholdWinners(matches, thresholds, false); len(winners) != 0 {
		t.Fatalf("ineligible fingerprint must not win: %#v", winners)
	}
}

func syntheticFingerprint(t *testing.T, mode string, completed map[string]int) (map[string]verificationJobManifest, map[string]verificationJobResult) {
	t.Helper()
	jobs, err := buildVerificationJobs(mode)
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[string]verificationJobManifest{}
	results := map[string]verificationJobResult{}
	seen := map[string]int{}
	for index, manifest := range jobs {
		id := fmt.Sprintf("job-%03d", index)
		manifests[id] = manifest
		if _, ok := completed[manifest.ProbeID]; !ok || seen[manifest.ProbeID] >= completed[manifest.ProbeID] {
			continue
		}
		seen[manifest.ProbeID]++
		category := "uruguay"
		if manifest.ProbeID == "rand_bird" {
			category = "robin"
		} else if manifest.ProbeID == "b80_letter_count" {
			category = "exact_3"
		}
		results[id] = verificationJobResult{Status: "ok", Classification: "category", Category: category}
	}
	return manifests, results
}

func TestFingerprintThresholdsMatchV411Policy(t *testing.T) {
	want := map[string]map[string]float64{
		VerificationModeLow:    {"gpt-5.6-sol": .54, "gpt-5.6-terra": .58, "gpt-5.6-luna": .77},
		VerificationModeMedium: {"gpt-5.6-sol": .82, "gpt-5.6-terra": .84, "gpt-5.6-luna": .97},
		VerificationModeHigh:   {"gpt-5.6-sol": .98, "gpt-5.6-terra": .97, "gpt-5.6-luna": .99},
	}
	for mode, expected := range want {
		got := fingerprintThresholds(mode)
		for model, threshold := range expected {
			if got[model] != threshold {
				t.Errorf("%s threshold for %s = %v, want %v", mode, model, got[model], threshold)
			}
		}
	}
}

func TestLowPresetUsesOfficialFingerprintPolicy(t *testing.T) {
	manifests, results := syntheticFingerprint(t, VerificationModeLow, map[string]int{"rand_country": 3, "rand_bird": 3, "b80_letter_count": 3})
	summary, err := scoreFingerprint(VerificationModeLow, manifests, results)
	if err != nil {
		t.Fatal(err)
	}
	if !summary.OfficialEligible || summary.PolicyVersion != verificationPolicyVersion {
		t.Fatalf("low preset should use official v4.1.1 policy: %#v", summary)
	}
}

func TestFingerprintPolicyRejectsLegacyRuntimeShape(t *testing.T) {
	manifests, results := syntheticFingerprint(t, VerificationModeHigh, map[string]int{"rand_country": 40, "rand_bird": 40, "b80_letter_count": 10})
	manifests["legacy-extra"] = verificationJobManifest{ProbeID: "juice_high", RequestFormat: "normal", ContextMode: "no_history", Effort: "high"}
	summary, err := scoreFingerprint(VerificationModeHigh, manifests, results)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OfficialEligible || !containsString(summary.UnclearReasons, "no_exact_runtime_policy") {
		t.Fatalf("legacy runtime shape must be reference-only: %#v", summary)
	}
}

func TestFingerprintPolicyRejectsModifiedPrompt(t *testing.T) {
	jobs, err := buildVerificationJobs(VerificationModeLow)
	if err != nil {
		t.Fatal(err)
	}
	manifests := make(map[string]verificationJobManifest, len(jobs))
	for index, job := range jobs {
		if job.ProbeID == "rand_country" {
			job.Messages[0].Content = "Name any country."
		}
		manifests[fmt.Sprintf("job-%03d", index)] = job
	}
	if matchesOfficialFingerprintPolicy(VerificationModeLow, manifests) {
		t.Fatal("modified fingerprint prompt must not match the official runtime policy")
	}
}

func TestVerificationNativeUserAgent(t *testing.T) {
	got, err := verificationNativeUserAgent()
	if err != nil {
		t.Fatal(err)
	}
	want := "Codex Desktop/0.147.0-alpha.1.2 (Windows 10.0.26200; x86_64) unknown (codex_exec; 0.147.0-alpha.1.2)"
	if got != want {
		t.Fatalf("native User-Agent = %q, want %q", got, want)
	}
}

func TestFingerprintClaimMismatchIsFailedCheck(t *testing.T) {
	summary := fingerprintSummary{Status: "strong_match", Model: "gpt-5.6-luna"}
	if got := fingerprintLayerStatus(summary, true); got != "failed" {
		t.Fatalf("mismatch layer status = %q, want failed", got)
	}
	if text := verificationFingerprintSummary(summary, "gpt-5.6-sol"); !strings.Contains(text, "Sol") || !strings.Contains(text, "Luna") {
		t.Fatalf("mismatch summary = %q", text)
	}
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
