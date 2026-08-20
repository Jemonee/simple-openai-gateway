package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
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

func TestParseResponsesSSEIgnoresKeepAliveDelta(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","item_id":"SSE-Keep-Alive","delta":"\u200b","SSE-Keep-Alive":true}`,
		`data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"32"}`,
		`data: {"type":"response.completed","response":{"output_text":"32"}}`,
		"",
	}, "\n")
	result, err := parseResponsesSSE([]byte(body))
	if err != nil || result.Answer != "32" {
		t.Fatalf("keep-alive SSE = %#v, %v", result, err)
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
	var requestCount, attemptCount int64
	if err := store.db.Model(&RelayRequestLog{}).Where("codex_session_id = ?", verificationSessionCodexID(session.ID)).Count(&requestCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&RelayAttemptLog{}).Where("request_id IN (SELECT id FROM relay_request_logs WHERE codex_session_id = ?)", verificationSessionCodexID(session.ID)).Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if requestCount != 2 || attemptCount != 0 {
		t.Fatalf("stopped verification relay logs = requests %d, attempts %d; want 2 requests and 0 attempts", requestCount, attemptCount)
	}
}

func TestChannelMetricsPreferRunningThenLatestVerification(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationSession{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	model := GatewayModel{Name: "gpt-5.6-sol", Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	channel := Channel{Name: "验证渠道", BaseURL: "https://verification.example/v1", Enabled: true}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: model.Name, Enabled: true}
	if err := store.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	production := []RelayAttemptLog{
		{RequestID: "production-success", ChannelID: channel.ID, ChannelModelID: mapping.ID, UpstreamModel: model.Name, Success: true, Outcome: RelayOutcomeSuccess, LatencyMS: 10, CreatedAt: now.Add(-10 * time.Minute)},
		{RequestID: "production-failure", ChannelID: channel.ID, ChannelModelID: mapping.ID, UpstreamModel: model.Name, Success: false, Outcome: RelayOutcomeFailed, CreatedAt: now.Add(-5 * time.Minute)},
	}
	if err := store.db.Create(&production).Error; err != nil {
		t.Fatal(err)
	}
	latest := VerificationSession{ID: "verification-latest", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: model.Name, Mode: VerificationModeMedium, Status: "completed", StartedAt: now.Add(-time.Hour), FinishedAt: now.Add(-50 * time.Minute)}
	running := VerificationSession{ID: "verification-running", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: model.Name, Mode: VerificationModeLow, Status: "running", StartedAt: now.Add(-time.Minute)}
	if err := store.db.Create(&[]VerificationSession{latest, running}).Error; err != nil {
		t.Fatal(err)
	}
	attempts := []VerificationAttempt{
		verificationMetricAttempt(latest.ID, 1, RelayOutcomeSuccess, 200, 100, 20, now.Add(-59*time.Minute)),
		verificationMetricAttempt(latest.ID, 2, RelayOutcomeSuccess, 400, 300, 30, now.Add(-58*time.Minute)),
		verificationMetricAttempt(latest.ID, 3, RelayOutcomeFailed, 0, 0, 0, now.Add(-57*time.Minute)),
		verificationMetricAttempt(running.ID, 1, RelayOutcomeSuccess, 600, 80, 8, now.Add(-30*time.Second)),
		verificationMetricAttempt(running.ID, 2, RelayOutcomeFailed, 0, 0, 0, now.Add(-20*time.Second)),
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}

	views, err := NewManagementService(store).ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("channel views = %#v", views)
	}
	metrics := views[0].Metrics
	if metrics.RecentSuccessCount != 1 || metrics.RecentAttemptCount != 2 || metrics.RecentSuccessRate != 0.5 || metrics.AverageLatencyMS != 600 || metrics.InputTokens != 80 || metrics.TokensPerSecondSampleCount != 1 || math.Abs(metrics.AverageTokensPerSecond-(8.0*1000/600)) > 0.000001 {
		t.Fatalf("running verification metrics = %+v", metrics)
	}
	if metrics.TodayAttemptCount != 2 {
		t.Fatalf("today production attempts = %d, want 2", metrics.TodayAttemptCount)
	}

	if err := store.db.Delete(&running).Error; err != nil {
		t.Fatal(err)
	}
	views, err = NewManagementService(store).ListChannels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metrics = views[0].Metrics
	if metrics.RecentSuccessCount != 2 || metrics.RecentAttemptCount != 3 || math.Abs(metrics.RecentSuccessRate-2.0/3.0) > 0.000001 || metrics.AverageLatencyMS != 300 || metrics.InputTokens != 400 || metrics.TokensPerSecondSampleCount != 2 || math.Abs(metrics.AverageTokensPerSecond-62.5) > 0.000001 {
		t.Fatalf("latest verification metrics = %+v", metrics)
	}
}

func verificationMetricAttempt(sessionID string, sequence int, outcome string, latencyMS, inputTokens, cachedTokens int64, finishedAt time.Time) VerificationAttempt {
	return VerificationAttempt{
		ID: fmt.Sprintf("%s-%d", sessionID, sequence), SessionID: sessionID, Sequence: sequence, AttemptNo: 1,
		DetectorName: "metric", Method: "POST", Path: "/responses", StatusCode: 200, Outcome: outcome,
		InputTokens: inputTokens, OutputTokens: inputTokens / 10, CachedTokens: cachedTokens, LatencyMS: latencyMS,
		StartedAt: finishedAt.Add(-time.Duration(latencyMS) * time.Millisecond), FinishedAt: finishedAt,
	}
}

func verificationTestJobs(sessionID string, statuses []string, started time.Time) []VerificationJob {
	jobs := make([]VerificationJob, 0, len(statuses))
	for index, status := range statuses {
		resultJSON := ""
		if status == "succeeded" {
			resultJSON = `{"status":"succeeded"}`
		} else if status == "error" || status == "cancelled" {
			resultJSON = fmt.Sprintf(`{"status":%q,"errorCategory":%q}`, status, status)
		}
		jobs = append(jobs, VerificationJob{ID: fmt.Sprintf("%s-job-%02d", sessionID, index+1), SessionID: sessionID, Sequence: index + 1, ProbeID: fmt.Sprintf("probe-%02d", index+1), RequestFormat: "normal", ContextMode: "no_history", Effort: "medium", Status: status, ManifestJSON: `{}`, ResultJSON: resultJSON, CreatedAt: started, UpdatedAt: started})
	}
	return jobs
}

func TestVerificationSessionLogAggregatesOneSessionAndNormalCosts(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationSession{}, &VerificationJob{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	model := GatewayModel{Name: "gpt-5.6-terra", Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	channel := Channel{Name: "主验证渠道", BaseURL: "https://verification-log.example/v1", Enabled: true}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: model.Name, Enabled: true}
	if err := store.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	finished := started.Add(30 * time.Second)
	session := VerificationSession{ID: "verification-log-session", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: mapping.UpstreamModel, Mode: VerificationModeMedium, Status: "running", PlannedCount: 2, StartedAt: started}
	jobs := verificationTestJobs(session.ID, []string{"succeeded", "succeeded"}, started)
	service := &VerificationService{store: store}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := tx.Create(&jobs).Error; err != nil {
			return err
		}
		return service.createVerificationSessionLog(tx, session, jobs)
	}); err != nil {
		t.Fatal(err)
	}
	var processingCount int64
	if err := store.db.Model(&RelayRequestLog{}).Where("codex_session_id = ? AND outcome = ?", verificationSessionCodexID(session.ID), RelayOutcomeProcessing).Count(&processingCount).Error; err != nil {
		t.Fatal(err)
	}
	if processingCount != 2 {
		t.Fatalf("processing verification requests = %d, want 2", processingCount)
	}
	attempts := []VerificationAttempt{
		{ID: "verification-log-attempt-1", SessionID: session.ID, Sequence: 1, JobID: jobs[0].ID, AttemptNo: 1, ProbeID: jobs[0].ProbeID, DetectorName: jobs[0].ProbeID, Method: "POST", Path: "/responses", Stream: true, RequestBody: `{"input":"one"}`, ResponseBody: `{"output":"one"}`, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 60, OutputTokens: 8, CachedTokens: 20, CacheWriteTokens: 5, EstimatedCostMicros: 80, UpstreamCostMicros: 70, CostSource: CostSourceUpstream, UsageSource: "upstream", LatencyMS: 150, StartedAt: started.Add(time.Second), FinishedAt: started.Add(time.Second + 150*time.Millisecond)},
		{ID: "verification-log-attempt-2", SessionID: session.ID, Sequence: 2, JobID: jobs[1].ID, AttemptNo: 1, ProbeID: jobs[1].ProbeID, DetectorName: jobs[1].ProbeID, Method: "POST", Path: "/responses", Stream: true, RequestBody: `{"input":"two"}`, ResponseBody: `{"output":"two"}`, StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 60, OutputTokens: 10, CachedTokens: 10, CacheWriteTokens: 5, EstimatedCostMicros: 100, UpstreamCostMicros: 80, CostSource: CostSourceUpstream, UsageSource: "upstream", LatencyMS: 250, StartedAt: started.Add(2 * time.Second), FinishedAt: started.Add(2*time.Second + 250*time.Millisecond)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.db.Transaction(func(tx *gorm.DB) error {
			return service.persistVerificationSessionLog(tx, session, finished, "completed")
		}); err != nil {
			t.Fatal(err)
		}
	}

	management := NewManagementService(store)
	page, err := management.SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("verification session page = %+v", page)
	}
	summary := page.Items[0]
	wantTime := started.In(eastEightLocation).Format("2006-01-02 15:04:05")
	if summary.SessionID != verificationSessionCodexID(session.ID) || summary.TokenID != 0 || summary.SessionSource != verificationSessionSource || summary.ThreadSource != verificationSessionSource {
		t.Fatalf("verification session identity = %+v", summary)
	}
	for _, value := range []string{channel.Name, model.Name, "中档", wantTime} {
		if !strings.Contains(summary.SessionName, value) {
			t.Fatalf("verification title %q does not contain %q", summary.SessionName, value)
		}
	}
	detail, err := management.SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: summary.SessionID, TokenID: 0, Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestTotal != 2 || len(detail.Requests) != 2 || detail.Summary.UpstreamCost != 150 || detail.Summary.AttemptCount != 2 {
		t.Fatalf("verification session detail = %+v", detail)
	}
	for _, request := range detail.Requests {
		if len(request.Attempts) != 1 {
			t.Fatalf("verification request attempts = %+v", request)
		}
	}

	dashboard, err := management.Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Requests != 2 || dashboard.EstimatedCost != 180 || dashboard.UpstreamCost != 150 {
		t.Fatalf("dashboard verification costs = %+v", dashboard)
	}
	costs, err := loadChannelCumulativeCosts(context.Background(), store.db, []uint64{channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	if costs[channel.ID] != 150 {
		t.Fatalf("channel cumulative verification cost = %d, want 150", costs[channel.ID])
	}
	var tokenStat TokenDailyStat
	if err := store.db.First(&tokenStat, "date = ? AND token_id = ?", eastEightDate(finished), 0).Error; err != nil {
		t.Fatal(err)
	}
	if tokenStat.RequestCount != 2 || tokenStat.AttemptCount != 2 || tokenStat.UpstreamCost != 150 {
		t.Fatalf("verification daily stat = %+v", tokenStat)
	}
	var channelStat ChannelModelDailyStat
	if err := store.db.First(&channelStat, "date = ? AND channel_id = ? AND channel_model_id = ?", eastEightDate(finished), channel.ID, mapping.ID).Error; err != nil {
		t.Fatal(err)
	}
	if channelStat.RequestCount != 2 || channelStat.AttemptCount != 2 || channelStat.UpstreamCost != 150 {
		t.Fatalf("verification channel daily stat = %+v", channelStat)
	}
}

func TestVerificationSessionSummaryUsesAttemptsAndComputesTPS(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationSession{}, &VerificationJob{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	model := GatewayModel{Name: "gpt-5.6-sol", Enabled: true}
	channel := Channel{Name: "验证汇总渠道", BaseURL: "https://verification-summary.example/v1", Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: model.Name, Enabled: true}
	if err := store.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	session := VerificationSession{ID: "verification-summary-session", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: model.Name, Mode: VerificationModeHigh, Status: "running", PlannedCount: 3, StartedAt: started}
	jobs := verificationTestJobs(session.ID, []string{"succeeded", "succeeded", "error"}, started)
	service := &VerificationService{store: store}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := tx.Create(&jobs).Error; err != nil {
			return err
		}
		return service.createVerificationSessionLog(tx, session, jobs)
	}); err != nil {
		t.Fatal(err)
	}
	attempts := []VerificationAttempt{
		{ID: "verification-summary-1", SessionID: session.ID, Sequence: 1, JobID: jobs[0].ID, AttemptNo: 1, DetectorName: "metric", Method: "POST", Path: "/responses", StatusCode: 200, Outcome: RelayOutcomeSuccess, OutputTokens: 100, LatencyMS: 500, StartedAt: now.Add(-30*time.Second - 500*time.Millisecond), FinishedAt: now.Add(-30 * time.Second)},
		{ID: "verification-summary-2", SessionID: session.ID, Sequence: 2, JobID: jobs[1].ID, AttemptNo: 1, DetectorName: "metric", Method: "POST", Path: "/responses", StatusCode: 200, Outcome: RelayOutcomeSuccess, OutputTokens: 200, LatencyMS: 1000, StartedAt: now.Add(-21 * time.Second), FinishedAt: now.Add(-20 * time.Second)},
		{ID: "verification-summary-3", SessionID: session.ID, Sequence: 3, JobID: jobs[2].ID, AttemptNo: 1, DetectorName: "metric", Method: "POST", Path: "/responses", StatusCode: 502, Outcome: RelayOutcomeFailed, StartedAt: now.Add(-10 * time.Second), FinishedAt: now.Add(-10 * time.Second)},
	}
	if err := store.db.Create(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		return service.persistVerificationSessionLog(tx, session, now, "completed")
	}); err != nil {
		t.Fatal(err)
	}
	page, err := NewManagementService(store).SessionLogs(context.Background(), SessionLogQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("verification session items = %+v", page.Items)
	}
	summary := page.Items[0]
	if summary.RequestCount != 3 || math.Abs(summary.SuccessRate-2.0/3.0) > 0.000001 || summary.VerificationAttemptSuccessCount != 2 || summary.VerificationAttemptFailureCount != 1 {
		t.Fatalf("verification success summary = %+v", summary)
	}
	if summary.TokensPerSecondSampleCount != 2 || math.Abs(summary.AverageTokensPerSecond-200) > 0.000001 {
		t.Fatalf("verification TPS summary = %+v", summary)
	}
	detail, err := NewManagementService(store).SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: verificationSessionCodexID(session.ID), Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestTotal != 3 || math.Abs(detail.Summary.SuccessRate-2.0/3.0) > 0.000001 || detail.Summary.TokensPerSecondSampleCount != 2 || math.Abs(detail.Summary.AverageTokensPerSecond-200) > 0.000001 {
		t.Fatalf("verification detail summary = %+v", detail.Summary)
	}
}

func TestStoppedVerificationKeepsIncurredCosts(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationSession{}, &VerificationJob{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	model := GatewayModel{Name: "gpt-5.6-luna", Enabled: true}
	channel := Channel{Name: "停止验证渠道", BaseURL: "https://stopped-verification.example/v1", Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: model.Name, Enabled: true}
	if err := store.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	session := VerificationSession{ID: "verification-stopped-cost", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: model.Name, Mode: VerificationModeLow, Status: "stopping", PlannedCount: 3, StartedAt: started}
	jobs := verificationTestJobs(session.ID, []string{"succeeded", "succeeded", "cancelled"}, started)
	attempts := []VerificationAttempt{
		{ID: "stopped-success-1", SessionID: session.ID, Sequence: 1, JobID: jobs[0].ID, AttemptNo: 1, DetectorName: jobs[0].ProbeID, Method: "POST", Path: "/responses", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, EstimatedCostMicros: 300, UpstreamCostMicros: 250, CostSource: CostSourceUpstream, UsageSource: "upstream", LatencyMS: 100, StartedAt: now.Add(-30*time.Second - 100*time.Millisecond), FinishedAt: now.Add(-30 * time.Second)},
		{ID: "stopped-success-2", SessionID: session.ID, Sequence: 2, JobID: jobs[1].ID, AttemptNo: 1, DetectorName: jobs[1].ProbeID, Method: "POST", Path: "/responses", StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, EstimatedCostMicros: 300, UpstreamCostMicros: 250, CostSource: CostSourceUpstream, UsageSource: "upstream", LatencyMS: 100, StartedAt: now.Add(-20*time.Second - 100*time.Millisecond), FinishedAt: now.Add(-20 * time.Second)},
		{ID: "stopped-canceled", SessionID: session.ID, Sequence: 3, JobID: jobs[2].ID, AttemptNo: 1, DetectorName: jobs[2].ProbeID, Method: "POST", Path: "/responses", StatusCode: 0, Outcome: RelayOutcomeCanceled, CostSource: CostSourceFailedZero, UsageSource: "estimated_tiktoken", LatencyMS: 50, StartedAt: now.Add(-10*time.Second - 50*time.Millisecond), FinishedAt: now.Add(-10 * time.Second)},
	}
	service := &VerificationService{store: store}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := tx.Create(&jobs).Error; err != nil {
			return err
		}
		if err := service.createVerificationSessionLog(tx, session, jobs); err != nil {
			return err
		}
		if err := tx.Create(&attempts).Error; err != nil {
			return err
		}
		return service.persistVerificationSessionLog(tx, session, now, "stopped")
	}); err != nil {
		t.Fatal(err)
	}
	var canceledRequests int64
	if err := store.db.Model(&RelayRequestLog{}).Where("codex_session_id = ? AND outcome = ?", verificationSessionCodexID(session.ID), RelayOutcomeCanceled).Count(&canceledRequests).Error; err != nil {
		t.Fatal(err)
	}
	if canceledRequests != 1 {
		t.Fatalf("stopped verification canceled requests = %d, want 1", canceledRequests)
	}
	var stat ChannelModelDailyStat
	if err := store.db.First(&stat, "date = ? AND channel_id = ? AND channel_model_id = ?", eastEightDate(now), channel.ID, mapping.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 3 || stat.AttemptCount != 3 || stat.CanceledCount != 1 || stat.UpstreamCost != 500 || stat.UpstreamCostCount != 2 || stat.FailedZeroCostCount != 1 {
		t.Fatalf("stopped verification channel stat = %+v", stat)
	}
	dashboard, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Requests != 3 || dashboard.UpstreamCost != 500 {
		t.Fatalf("stopped verification dashboard = %+v", dashboard)
	}
}

func TestFailedVerificationFinalizesSessionLogAndCosts(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationSession{}, &VerificationJob{}, &VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	model := GatewayModel{Name: "gpt-5.6-sol", Enabled: true}
	channel := Channel{Name: "异常验证渠道", BaseURL: "https://failed-verification.example/v1", Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: model.Name, Enabled: true}
	if err := store.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	session := VerificationSession{ID: "verification-failed-cost", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: model.Name, Mode: VerificationModeLow, Status: "running", PlannedCount: 2, StartedAt: started}
	jobs := verificationTestJobs(session.ID, []string{"succeeded", "pending"}, started)
	service := &VerificationService{store: store}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := tx.Create(&jobs).Error; err != nil {
			return err
		}
		return service.createVerificationSessionLog(tx, session, jobs)
	}); err != nil {
		t.Fatal(err)
	}
	attempt := VerificationAttempt{
		ID: "failed-session-successful-attempt", SessionID: session.ID, Sequence: 1, JobID: jobs[0].ID, AttemptNo: 1, DetectorName: "metric",
		Method: "POST", Path: "/responses", StatusCode: 200, Outcome: RelayOutcomeSuccess,
		InputTokens: 100, OutputTokens: 10, EstimatedCostMicros: 300, UpstreamCostMicros: 250, CostSource: CostSourceUpstream,
		LatencyMS: 120, StartedAt: now.Add(-120 * time.Millisecond), FinishedAt: now,
	}
	if err := store.db.Create(&attempt).Error; err != nil {
		t.Fatal(err)
	}
	service.failSession(session.ID, fmt.Errorf("runtime failure"))

	if err := store.db.First(&session, "id = ?", session.ID).Error; err != nil {
		t.Fatal(err)
	}
	if session.Status != "failed" || session.RequestCount != 2 || session.UpstreamCostMicros != attempt.UpstreamCostMicros {
		t.Fatalf("failed verification session = %+v", session)
	}
	var succeededRequests, failedRequests, attemptCount int64
	if err := store.db.Model(&RelayRequestLog{}).Where("codex_session_id = ? AND outcome = ?", verificationSessionCodexID(session.ID), RelayOutcomeSuccess).Count(&succeededRequests).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&RelayRequestLog{}).Where("codex_session_id = ? AND outcome = ?", verificationSessionCodexID(session.ID), RelayOutcomeFailed).Count(&failedRequests).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&RelayAttemptLog{}).Where("request_id IN (SELECT id FROM relay_request_logs WHERE codex_session_id = ?)", verificationSessionCodexID(session.ID)).Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if succeededRequests != 1 || failedRequests != 1 || attemptCount != 1 {
		t.Fatalf("failed verification hierarchy = success %d, failed %d, attempts %d", succeededRequests, failedRequests, attemptCount)
	}
	dashboard, err := NewManagementService(store).Dashboard(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.UpstreamCost != attempt.UpstreamCostMicros {
		t.Fatalf("failed verification dashboard cost = %d, want %d", dashboard.UpstreamCost, attempt.UpstreamCostMicros)
	}
}

func TestVerificationSessionLogCreatesOneRequestPerLogicalJob(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationSession{}, &VerificationJob{}, &VerificationAttempt{}, &VerificationReport{}); err != nil {
		t.Fatal(err)
	}
	model := GatewayModel{Name: "gpt-5.6-terra", Enabled: true}
	channel := Channel{Name: "49 请求渠道", BaseURL: "https://49-requests.example/v1", Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{ChannelID: channel.ID, ModelID: model.ID, UpstreamModel: model.Name, Enabled: true}
	if err := store.db.Create(&mapping).Error; err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	session := VerificationSession{ID: "verification-49-request-session", ChannelID: channel.ID, ChannelName: channel.Name, ModelID: model.ID, PublicModel: model.Name, UpstreamModel: model.Name, Mode: VerificationModeMedium, Status: "running", PlannedCount: 49, StartedAt: started}
	jobs := make([]VerificationJob, 0, 49)
	for sequence := 1; sequence <= 49; sequence++ {
		jobs = append(jobs, VerificationJob{ID: fmt.Sprintf("job-%02d", sequence), SessionID: session.ID, Sequence: sequence, ProbeID: fmt.Sprintf("probe-%02d", sequence), RequestFormat: "normal", ContextMode: "no_history", Effort: "medium", Status: "succeeded", ManifestJSON: `{}`, ResultJSON: `{"status":"succeeded"}`, CreatedAt: started, UpdatedAt: started})
	}
	if err := store.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		if err := tx.Create(&jobs).Error; err != nil {
			return err
		}
		service := &VerificationService{store: store}
		if err := service.createVerificationSessionLog(tx, session, jobs); err != nil {
			return err
		}
		attempts := make([]VerificationAttempt, 0, 50)
		for sequence, job := range jobs {
			attempts = append(attempts, VerificationAttempt{ID: fmt.Sprintf("attempt-%02d-1", sequence+1), SessionID: session.ID, Sequence: sequence + 1, JobID: job.ID, AttemptNo: 1, ProbeID: job.ProbeID, DetectorName: job.ProbeID, RequestFormat: "normal", Method: "POST", Path: "/responses", Stream: true, RequestBody: fmt.Sprintf(`{"job":%d}`, sequence+1), ResponseBody: fmt.Sprintf("data: {\"job\":%d}", sequence+1), StatusCode: http.StatusOK, Outcome: RelayOutcomeSuccess, InputTokens: 10, OutputTokens: 5, EstimatedCostMicros: 10, UpstreamCostMicros: 9, CostSource: CostSourceUpstream, UsageSource: "upstream", LatencyMS: 100, StartedAt: started.Add(time.Duration(sequence) * time.Millisecond), FinishedAt: started.Add(time.Duration(sequence+1) * time.Millisecond)})
		}
		attempts = append(attempts, VerificationAttempt{ID: "attempt-01-0", SessionID: session.ID, Sequence: 1, JobID: jobs[0].ID, AttemptNo: 0, ProbeID: jobs[0].ProbeID, DetectorName: jobs[0].ProbeID, RequestFormat: "normal", Method: "POST", Path: "/responses", Stream: true, RequestBody: `{"job":1}`, ResponseBody: "upstream failed", StatusCode: http.StatusBadGateway, Outcome: RelayOutcomeFailed, CostSource: CostSourceFailedZero, UsageSource: "estimated_tiktoken", LatencyMS: 80, StartedAt: started.Add(-time.Millisecond), FinishedAt: started})
		if err := tx.Create(&attempts).Error; err != nil {
			return err
		}
		return service.persistVerificationSessionLog(tx, session, started.Add(30*time.Second), "completed")
	}); err != nil {
		t.Fatal(err)
	}
	var requestCount, attemptCount int64
	if err := store.db.Model(&RelayRequestLog{}).Where("codex_session_id = ?", verificationSessionCodexID(session.ID)).Count(&requestCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Model(&RelayAttemptLog{}).Where("request_id IN (SELECT id FROM relay_request_logs WHERE codex_session_id = ?)", verificationSessionCodexID(session.ID)).Count(&attemptCount).Error; err != nil {
		t.Fatal(err)
	}
	if requestCount != 49 || attemptCount != 50 {
		t.Fatalf("verification relay hierarchy = requests %d, attempts %d; want 49 requests and 50 attempts", requestCount, attemptCount)
	}
	detail, err := NewManagementService(store).SessionLogDetail(context.Background(), SessionDetailQuery{SessionID: verificationSessionCodexID(session.ID), Page: 1, PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	if detail.RequestTotal != 49 || len(detail.Requests) != 49 || detail.Summary.RequestCount != 49 || detail.Summary.AttemptCount != 50 || detail.Summary.SuccessCount != 49 {
		t.Fatalf("verification detail hierarchy = total %d requests %d summary=%+v", detail.RequestTotal, len(detail.Requests), detail.Summary)
	}
	var retryRequest RelayRequestView
	for _, request := range detail.Requests {
		if request.AttemptCount == 2 {
			retryRequest = request
			break
		}
	}
	if retryRequest.ID == "" || len(retryRequest.Attempts) != 2 {
		t.Fatalf("retry request detail = %+v", retryRequest)
	}
	fullRequest, err := NewManagementService(store).LogDetail(context.Background(), retryRequest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fullRequest.PayloadLogDetail != "default" || !strings.Contains(fullRequest.RequestBody, `"job"`) || !strings.Contains(fullRequest.ResponseBody, `"job"`) || len(fullRequest.Attempts) != 2 {
		t.Fatalf("verification retained payload detail = %+v", fullRequest)
	}
	if fullRequest.APIPath != "/v1/responses" || fullRequest.ReasoningEffort != "medium" {
		t.Fatalf("verification request metadata = %+v", fullRequest)
	}
	for _, attempt := range fullRequest.Attempts {
		if attempt.RequestBody == "" || attempt.ResponseBody == "" || attempt.SelectionReason != "" || attempt.PreviousChannelID != 0 {
			t.Fatalf("verification attempt should retain payload without routing metadata: %+v", attempt)
		}
	}
	var stat ChannelModelDailyStat
	if err := store.db.First(&stat, "date = ? AND channel_id = ? AND channel_model_id = ?", eastEightDate(started.Add(30*time.Second)), channel.ID, mapping.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stat.RequestCount != 49 || stat.AttemptCount != 50 || stat.UpstreamCost != 49*9 {
		t.Fatalf("verification daily hierarchy = %+v", stat)
	}
}

func TestVerificationAttemptUsesRelayStreamCosting(t *testing.T) {
	store := newTestStore(t)
	if err := store.db.AutoMigrate(&VerificationAttempt{}); err != nil {
		t.Fatal(err)
	}
	mapping := ChannelModel{InputPriceMicros: 2_000_000, OutputPriceMicros: 4_000_000}
	job := VerificationJob{ID: "cost-job", Sequence: 1}
	manifest := verificationJobManifest{ProbeID: "cost-probe", RequestFormat: "normal", ContextMode: "no_history", Effort: "medium"}
	usage := Usage{InputTokens: 100, OutputTokens: 20, Source: "upstream"}
	result := verificationTransportResult{Usage: usage, UpstreamCost: 123, UpstreamCostOK: true, Answer: "ok"}
	service := &VerificationService{store: store}
	service.persistAttempt("cost-session", mapping, job, manifest, 1, result, []byte(`{"input":"hello"}`), []byte("data: done"), verificationTransportMeta{Status: http.StatusOK, LatencyMS: 200}, nil)
	service.persistAttempt("cost-session", mapping, job, manifest, 2, verificationTransportResult{}, []byte(`{"input":"retry"}`), []byte("upstream failed"), verificationTransportMeta{Status: http.StatusBadGateway, LatencyMS: 100}, fmt.Errorf("upstream failed"))
	var attempts []VerificationAttempt
	if err := store.db.Where("session_id = ?", "cost-session").Order("attempt_no ASC").Find(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("verification cost attempts = %d, want 2", len(attempts))
	}
	expectedEstimated, expectedUpstream, expectedSource := streamAttemptCosts(mapping, usage, upstreamCostSnapshot{micros: 123, valid: true}, true)
	if attempts[0].EstimatedCostMicros != expectedEstimated || attempts[0].UpstreamCostMicros != expectedUpstream || attempts[0].CostSource != expectedSource {
		t.Fatalf("successful verification cost = %+v", attempts[0])
	}
	if attempts[1].EstimatedCostMicros != 0 || attempts[1].UpstreamCostMicros != 0 || attempts[1].CostSource != CostSourceFailedZero {
		t.Fatalf("failed verification cost = %+v", attempts[1])
	}
}
