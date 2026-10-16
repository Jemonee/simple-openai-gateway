package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDetectResourcesFindsReferencesAndStableFingerprints(t *testing.T) {
	body := []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":[{"type":"text","text":"see https://example.com/report.pdf"},{"type":"image_url","image_url":{"url":"https://cdn.example.com/a.png"}},{"type":"image_url","image_url":{"url":"https://cdn.example.com/a.png"}}]}],"file_id":"file_123"}`)
	items := detectResources(body)
	if len(items) != 4 {
		t.Fatalf("detected resources = %d, want 4", len(items))
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Fingerprint]++
	}
	foundRepeated := false
	for _, count := range counts {
		if count == 2 {
			foundRepeated = true
		}
	}
	if !foundRepeated {
		t.Fatal("repeated resource did not produce a stable fingerprint")
	}
}

func TestDetectResourcesKeepsImagePreviewData(t *testing.T) {
	items := detectResources([]byte(`{"model":"gpt-4.1","input":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}`))
	if len(items) != 1 || items[0].Kind != "image" || items[0].PreviewData == "" {
		t.Fatalf("image resource = %+v", items)
	}
}

func TestDetectResourcesKeepsCompleteLargeImageDataURI(t *testing.T) {
	decoded := bytes.Repeat([]byte{0xff}, 1800)
	value := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(decoded)
	items := detectResources([]byte(`{"input":[{"type":"input_image","image_url":"` + value + `"}]}`))
	if len(items) != 1 {
		t.Fatalf("detected resources = %d, want 1", len(items))
	}
	if len(items[0].URI) >= len(value) {
		t.Fatalf("URI should remain bounded: got %d, want less than %d", len(items[0].URI), len(value))
	}
	if items[0].PreviewData != value {
		t.Fatalf("preview data was truncated: got %d bytes, want %d", len(items[0].PreviewData), len(value))
	}
	if items[0].SizeBytes != int64(len(decoded)) {
		t.Fatalf("decoded size = %d, want %d", items[0].SizeBytes, len(decoded))
	}
}

func TestDetectResourcesExtractsCleanURLFromEmbeddedJSONText(t *testing.T) {
	body, err := json.Marshal(map[string]string{"text": `https://example.com/report.pdf\"},{\"type\":\"image_url\"`})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 1 {
		t.Fatalf("detected resources = %d, want 1: %+v", len(items), items)
	}
	if items[0].Kind != "url" || items[0].URI != "https://example.com/report.pdf" || items[0].Name != "report.pdf" {
		t.Fatalf("resource = %+v", items[0])
	}
}

func TestDetectResourcesKeepsExplicitLocalPathsAndFileIdentifiers(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": `# Files mentioned by the user:

## resources.go: /Users/dudu/work/project/internal/gateway/resources.go

Inspect <file path="C:\workspace\frontend\src\App.vue">, "/Users/dudu/My Project/main.go:42", and "My Project/local file.ts".
The API endpoint is /v1/responses and the public reference is https://example.com/report.pdf.`},
			map[string]any{"type": "function_call", "path": "main.go", "file_id": "file_123"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	fileCount := 0
	for _, item := range items {
		if item.Kind == "file" {
			fileCount++
		}
	}
	if fileCount != 3 {
		t.Fatalf("explicit local references produced %d file resources: %+v", fileCount, items)
	}
	if len(items) != 4 {
		t.Fatalf("detected resources = %+v", items)
	}
	urlFound := false
	for _, item := range items {
		if item.Kind == "url" && item.URI == "https://example.com/report.pdf" {
			urlFound = true
		}
	}
	if !urlFound {
		t.Fatalf("public URL was lost: %+v", items)
	}
}

func TestDetectResourcesKeepsNonBase64DataURI(t *testing.T) {
	items := detectResources([]byte(`{"input":[{"type":"input_image","image_url":"data:image/svg+xml,%3Csvg%20xmlns%3D%22http%3A%2F%2Fwww.w3.org%2F2000%2Fsvg%22%3E%3C%2Fsvg%3E"}]}`))
	if len(items) != 1 {
		t.Fatalf("detected resources = %+v", items)
	}
	item := items[0]
	if item.Kind != "image" || item.MIMEType != "image/svg+xml" || item.SHA256 == "" || item.SizeBytes == 0 || item.PreviewData == "" {
		t.Fatalf("non-base64 image resource = %+v", item)
	}
}

func TestDetectResourcesKeepsInlineSourceFilePreview(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{
				"path":    "internal/sample.go",
				"content": "package sample\n\nimport \"strings\"\n\nfunc Normalize(value string) string {\n\treturn strings.TrimSpace(value)\n}",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	for _, item := range items {
		if item.Kind != "file" || item.URI != "internal/sample.go" {
			continue
		}
		if item.MIMEType != "text/x-go" || !strings.HasPrefix(item.Preview, "package sample") || item.SHA256 == "" || item.SizeBytes == 0 {
			t.Fatalf("source file resource = %+v", item)
		}
		return
	}
	t.Fatalf("source file resource not found: %+v", items)
}

func TestDetectResourcesKeepsInlineFileData(t *testing.T) {
	content := []byte("{\n  \"enabled\": true\n}")
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{
				"type":      "input_file",
				"filename":  "config.json",
				"file_data": "data:application/json;base64," + base64.StdEncoding.EncodeToString(content),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 1 {
		t.Fatalf("detected resources = %d, want 1: %+v", len(items), items)
	}
	item := items[0]
	if item.Kind != "file" || item.Name != "config.json" || item.MIMEType != "application/json" || item.SHA256 == "" || item.SizeBytes != int64(len(content)) || item.Preview != string(content) {
		t.Fatalf("inline file resource = %+v", item)
	}
}

func TestDetectResourcesRecognizesCodexSkillInvocationWithoutCatalogEntries(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": "$agent-orchestrator add a project member"},
			map[string]any{"role": "user", "content": "<skill>\n<name>agent-orchestrator</name>\n<path>/tmp/skills/agent-orchestrator/SKILL.md</path>\n</skill>"},
			map[string]any{"role": "developer", "content": "<skills_instructions>\n### Available skills\n- imagegen (file: /tmp/imagegen/SKILL.md)\n- openai-docs (file: /tmp/openai-docs/SKILL.md)\n</skills_instructions>"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 1 {
		t.Fatalf("detected resources = %d, want 1: %+v", len(items), items)
	}
	item := items[0]
	if item.Kind != "skill" || item.Name != "$agent-orchestrator" || item.URI != "/tmp/skills/agent-orchestrator/SKILL.md" || item.MIMEType != "application/x-codex-skill" {
		t.Fatalf("skill resource = %+v", item)
	}
}

func TestDetectResourcesRecognizesMarkdownLinkSkillInvocation(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "[$frontend-dev](/opt/codex/frontend-dev/SKILL.md) update the resource page"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 1 || items[0].Kind != "skill" || items[0].Name != "$frontend-dev" || items[0].URI != "/opt/codex/frontend-dev/SKILL.md" {
		t.Fatalf("skill resource = %+v", items)
	}
}

func TestDetectResourcesRecognizesInjectedMarkdownDocumentContent(t *testing.T) {
	documentContent := "# Repository Guidelines\n\nUse https://example.com only in tests.\n\n```go\npackage sample\n\nfunc Example() {}\n```"
	payload := "# AGENTS.md instructions for /Users/dudu/work/project\n\n<INSTRUCTIONS>\n" + documentContent + "\n</INSTRUCTIONS>"
	body, err := json.Marshal(map[string]any{
		"input": []any{map[string]any{"role": "developer", "content": payload}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 1 {
		t.Fatalf("detected resources = %d, want 1: %+v", len(items), items)
	}
	item := items[0]
	if item.Kind != "file" || item.Name != "AGENTS.md" || item.URI != "/Users/dudu/work/project/AGENTS.md" || item.MIMEType != "text/markdown" || item.SHA256 == "" || item.SizeBytes != int64(len([]byte(documentContent))) || item.Preview != documentContent {
		t.Fatalf("injected Markdown resource = %+v", item)
	}
}

func TestDetectResourcesKeepsSkillInvocationSeparateFromMarkdownFiles(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": "$frontend-dev update the resource page"},
			map[string]any{"role": "user", "content": "<skill>\n<name>frontend-dev</name>\n<path>/opt/codex/frontend-dev/SKILL.md</path>\n</skill>"},
			map[string]any{"role": "developer", "content": "# SKILL.md instructions for /opt/codex/frontend-dev/SKILL.md\n\n<INSTRUCTIONS>\n---\nname: frontend-dev\n---\n# Frontend Development\n\nUse the existing component patterns.\n</INSTRUCTIONS>"},
			map[string]any{"role": "developer", "content": "# AGENTS.md instructions for /workspace\n\n<INSTRUCTIONS>\n# Repository Guidelines\n\nKeep changes scoped and run focused tests.\n</INSTRUCTIONS>"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 3 {
		t.Fatalf("detected resources = %d, want one skill and two files: %+v", len(items), items)
	}

	var skill *detectedResource
	files := make(map[string]detectedResource)
	for index := range items {
		item := &items[index]
		switch item.Kind {
		case "skill":
			skill = item
		case "file":
			files[item.Name] = *item
		}
	}
	if skill == nil || skill.Name != "$frontend-dev" || skill.URI != "/opt/codex/frontend-dev/SKILL.md" {
		t.Fatalf("skill invocation resource = %+v", skill)
	}
	skillDocument, hasSkillDocument := files["SKILL.md"]
	agentsDocument, hasAgentsDocument := files["AGENTS.md"]
	if !hasSkillDocument || skillDocument.MIMEType != "text/markdown" || skillDocument.SHA256 == "" || skillDocument.Fingerprint == skill.Fingerprint {
		t.Fatalf("SKILL.md file resource = %+v", skillDocument)
	}
	if !hasAgentsDocument || agentsDocument.MIMEType != "text/markdown" || agentsDocument.SHA256 == "" {
		t.Fatalf("AGENTS.md file resource = %+v", agentsDocument)
	}
}

func TestDetectResourcesDoesNotRecognizeMarkdownInstructionPathWithoutContent(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{map[string]any{"role": "developer", "content": "AGENTS.md instructions for /Users/dudu/work/project"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if items := detectResources(body); len(items) != 0 {
		t.Fatalf("path-only instruction produced resources: %+v", items)
	}
}

func TestNormalizeCodeLanguageSupportsCommonFencedLanguages(t *testing.T) {
	tests := []struct {
		language string
		mimeType string
	}{
		{"php", "text/x-php"},
		{"kotlin", "text/x-kotlin"},
		{"swift", "text/x-swift"},
		{"scss", "text/x-scss"},
		{"dockerfile", "text/x-dockerfile"},
		{"json", "application/json"},
	}
	for _, test := range tests {
		_, mimeType := normalizeCodeLanguage(test.language, "sample")
		if mimeType != test.mimeType {
			t.Errorf("language %q MIME type = %q, want %q", test.language, mimeType, test.mimeType)
		}
	}
}

func TestPersistRequestResourcesEnrichesExistingSourcePreview(t *testing.T) {
	store := newTestStore(t)
	sourceBody := []byte(`{"path":"internal/sample.go","content":"package sample\n\nimport (\"strings\")\n\nfunc Normalize(value string) string { return strings.TrimSpace(value) }"}`)
	sourceResources := detectResources(sourceBody)
	if len(sourceResources) != 1 {
		t.Fatalf("source resources = %+v", sourceResources)
	}
	legacy := sourceResources[0].GatewayResource
	legacy.MIMEType = ""
	legacy.Preview = legacy.URI
	legacy.SHA256 = ""
	legacy.SizeBytes = 0
	legacy.FirstSeenAt = time.Now().UTC().Add(-time.Hour)
	legacy.LastSeenAt = legacy.FirstSeenAt
	if err := store.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}

	if err := persistRequestResources(store.db, "source-preview-request", 0, "", sourceBody, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var enriched GatewayResource
	if err := store.db.First(&enriched, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if enriched.MIMEType != "text/x-go" || !strings.HasPrefix(enriched.Preview, "package sample") || enriched.SHA256 == "" || enriched.SizeBytes == 0 {
		t.Fatalf("enriched source resource = %+v", enriched)
	}

	changedBody := []byte(`{"path":"internal/sample.go","content":"package sample\n\nfunc Normalize(value string) string { return \"changed:\" + value }"}`)
	if err := persistRequestResources(store.db, "changed-source-request", 0, "", changedBody, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var changed GatewayResource
	if err := store.db.First(&changed, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if changed.SHA256 == enriched.SHA256 || !strings.Contains(changed.Preview, "changed:") {
		t.Fatalf("changed source content was not refreshed: before=%+v after=%+v", enriched, changed)
	}
}

func TestResourceQueriesKeepPathOnlyFilesVisible(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	request := RelayRequestLog{ID: "resource-filter-request", CreatedAt: now}
	if err := store.db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	resources := []GatewayResource{
		{Fingerprint: "legacy-path-only", Kind: "file", Name: "legacy.go", URI: "/tmp/legacy.go", FirstSeenAt: now, LastSeenAt: now},
		{Fingerprint: "confirmed-content", Kind: "file", Name: "actual.go", URI: "/tmp/actual.go", SHA256: "content-hash", SizeBytes: 42, Preview: "package actual", FirstSeenAt: now, LastSeenAt: now},
	}
	if err := store.db.Create(&resources).Error; err != nil {
		t.Fatal(err)
	}
	references := []RequestResource{
		{RequestID: request.ID, ResourceID: resources[0].ID, Location: "$.legacy", Role: "user"},
		{RequestID: request.ID, ResourceID: resources[1].ID, Location: "$.actual", Role: "user"},
	}
	if err := store.db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}

	requestResources, err := loadRequestResources(store.db, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(requestResources) != 2 {
		t.Fatalf("request resources = %+v", requestResources)
	}
	page, err := NewManagementService(store).Resources(context.Background(), ResourceQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("resource page = %+v, total = %d", page.Items, page.Total)
	}
	if _, err := NewManagementService(store).ResourceDetail(context.Background(), resources[0].ID); err != nil {
		t.Fatalf("path-only resource detail = %v", err)
	}
}

func TestResourceQueriesCanSkipPayloadPreviewHydration(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	body := []byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,aGVsbG8="}]}`)
	detected := detectResources(body)
	if len(detected) != 1 {
		t.Fatalf("detected resources = %d, want 1", len(detected))
	}
	request := RelayRequestLog{ID: "resource-preview-request", TokenID: 7, CodexSessionID: "resource-preview-session", Endpoint: "/v1/responses", RequestedModel: "gpt-test", CreatedAt: now}
	if err := store.db.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	resource := GatewayResource{Fingerprint: detected[0].Fingerprint, Kind: detected[0].Kind, FirstSeenAt: now, LastSeenAt: now}
	if err := store.db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RequestResource{RequestID: request.ID, ResourceID: resource.ID, Location: "$.input[0]", Role: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&RelayRequestPayload{RequestID: request.ID, RequestBody: string(body)}).Error; err != nil {
		t.Fatal(err)
	}

	withoutPreview, err := loadSessionResources(store.db, request.TokenID, request.CodexSessionID, "", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(withoutPreview) != 1 || withoutPreview[0].PreviewData != "" {
		t.Fatalf("metadata-only resources = %+v", withoutPreview)
	}

	withPreview, err := loadSessionResources(store.db, request.TokenID, request.CodexSessionID, "", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(withPreview) != 1 || withPreview[0].PreviewData == "" {
		t.Fatalf("hydrated resources = %+v", withPreview)
	}
}

func TestResourceDetailKeepsSessionLookupIdentity(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	resource := GatewayResource{
		Fingerprint: "resource-detail-session-identity",
		Kind:        "url",
		URI:         "https://example.com/detail",
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
	if err := store.db.Create(&resource).Error; err != nil {
		t.Fatal(err)
	}
	requests := []RelayRequestLog{
		{ID: "identified-resource-request", TokenID: 7, CodexSessionID: "session-1", SessionName: "Identified session", Endpoint: "/v1/responses", RequestedModel: "gpt-test", CreatedAt: now},
		{ID: "unidentified-resource-request", TokenID: 9, Endpoint: "/v1/responses", RequestedModel: "gpt-test", CreatedAt: now.Add(-time.Minute)},
	}
	if err := store.db.Create(&requests).Error; err != nil {
		t.Fatal(err)
	}
	references := []RequestResource{
		{RequestID: requests[0].ID, ResourceID: resource.ID, Location: "$.input", Role: "user"},
		{RequestID: requests[1].ID, ResourceID: resource.ID, Location: "$.input", Role: "user"},
	}
	if err := store.db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}

	detail, err := NewManagementService(store).ResourceDetail(context.Background(), resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Sessions) != 2 {
		t.Fatalf("resource sessions = %+v, want 2", detail.Sessions)
	}
	byToken := make(map[uint64]ResourceSessionRef, len(detail.Sessions))
	for _, session := range detail.Sessions {
		byToken[session.TokenID] = session
	}
	if identified := byToken[7]; identified.SessionID != "session-1" || identified.FallbackRequestID != "" {
		t.Fatalf("identified session lookup = %+v", identified)
	}
	if unidentified := byToken[9]; unidentified.SessionID != "" || unidentified.FallbackRequestID != requests[1].ID || unidentified.SessionName != requests[1].ID {
		t.Fatalf("unidentified session lookup = %+v", unidentified)
	}
}

func TestDetectResourcesFindsFencedAndToolOutputCode(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{"role": "user", "content": "```go\npackage sample\n\nfunc Run() error {\n\treturn nil\n}\n```"},
			map[string]any{"type": "function_call_output", "output": "package gateway\n\nimport (\n\t\"strings\"\n)\n\nfunc normalize(value string) string {\n\treturn strings.TrimSpace(value)\n}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	codes := make([]detectedResource, 0)
	for _, item := range items {
		if item.Kind == "code" {
			codes = append(codes, item)
		}
	}
	if len(codes) != 2 {
		t.Fatalf("code resources = %d, want 2: %+v", len(codes), codes)
	}
	for _, item := range codes {
		if item.Name != "Go代码片段" || item.MIMEType != "text/x-go" {
			t.Fatalf("code metadata = %+v", item)
		}
		if item.SHA256 == "" || item.SizeBytes == 0 || item.Preview == "" || !strings.HasPrefix(item.URI, "code:") {
			t.Fatalf("incomplete code resource = %+v", item)
		}
	}
}

func TestDetectResourcesDoesNotTreatProseOrRoutesAsFilesOrCode(t *testing.T) {
	items := detectResources([]byte(`{"input":"Use /v1/responses for package documentation and return a short status message."}`))
	if len(items) != 0 {
		t.Fatalf("detected resources = %+v, want none", items)
	}
}

func TestDetectResourcesIgnoresLinksEmbeddedInToolOutputAndCode(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{
				"type":   "function_call_output",
				"output": "package sample\n\nconst fixture = `https://cdn.example.com/a.png`\n\nfunc Run() string { return fixture }",
			},
			map[string]any{
				"role":    "user",
				"content": "Read https://platform.openai.com/docs and inspect:\n```go\nconst fixture = \"https://example.com/report.pdf\"\n```\nDo not treat `https://example.com/inline.png` as a resource.",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	urls := make([]detectedResource, 0)
	for _, item := range items {
		if item.Kind == "url" || item.Kind == "image" {
			urls = append(urls, item)
		}
	}
	if len(urls) != 1 || urls[0].URI != "https://platform.openai.com/docs" {
		t.Fatalf("detected URL resources = %+v", urls)
	}
}

func TestDetectResourcesKeepsStructuredLinkInsideToolOutput(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{
				"type": "function_call_output",
				"output": map[string]any{
					"image_url": map[string]any{"url": "https://assets.openai.com/image.png"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := detectResources(body)
	if len(items) != 1 || items[0].Kind != "image" || items[0].URI != "https://assets.openai.com/image.png" {
		t.Fatalf("structured image resource = %+v", items)
	}
}

func TestResourcesFiltersByLastSeenBefore(t *testing.T) {
	store := newTestStore(t)
	now := time.Now().UTC()
	old := GatewayResource{Fingerprint: "old", Kind: "url", URI: "https://example.com/old", FirstSeenAt: now.Add(-10 * 24 * time.Hour), LastSeenAt: now.Add(-10 * 24 * time.Hour)}
	recent := GatewayResource{Fingerprint: "recent", Kind: "url", URI: "https://example.com/recent", FirstSeenAt: now.Add(-2 * 24 * time.Hour), LastSeenAt: now.Add(-2 * 24 * time.Hour)}
	if err := store.db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&recent).Error; err != nil {
		t.Fatal(err)
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	page, err := NewManagementService(store).Resources(context.Background(), ResourceQuery{LastSeenBefore: &cutoff, Page: 1, PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].Fingerprint != old.Fingerprint {
		t.Fatalf("filtered resources = %+v, total = %d", page.Items, page.Total)
	}
}
