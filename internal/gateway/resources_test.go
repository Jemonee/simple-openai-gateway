package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
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
