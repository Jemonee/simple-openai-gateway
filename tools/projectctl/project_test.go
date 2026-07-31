package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validManifest() ProjectManifest {
	return ProjectManifest{
		GoModule:    "example.com/acme/app",
		BinaryName:  "acme-app",
		AppName:     "acme-app",
		DisplayName: "Acme App",
		Description: "An example application.",
		Version:     "1.2.3",
		TokenPrefix: "acme",
		FaviconPath: "/favicon.svg",
	}
}

func TestValidateManifest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProjectManifest)
	}{
		{name: "module", mutate: func(m *ProjectManifest) { m.GoModule = "bad module" }},
		{name: "binary", mutate: func(m *ProjectManifest) { m.BinaryName = "Bad Binary" }},
		{name: "app", mutate: func(m *ProjectManifest) { m.AppName = "Bad App" }},
		{name: "display", mutate: func(m *ProjectManifest) { m.DisplayName = "" }},
		{name: "description", mutate: func(m *ProjectManifest) { m.Description = "" }},
		{name: "version", mutate: func(m *ProjectManifest) { m.Version = "v1" }},
		{name: "token", mutate: func(m *ProjectManifest) { m.TokenPrefix = "Bad-Prefix" }},
		{name: "favicon", mutate: func(m *ProjectManifest) { m.FaviconPath = "favicon.svg" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if err := validateManifest(manifest); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestPlanGoImportChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "internal", "config", "config.go")
	content := `package main

import (
	"example.com/old/app/internal/config"
	"example.com/old/application"
)
`
	writeFixture(t, path, content)
	runtimeConfig := filepath.Join(root, "config", "config.go")
	writeFixture(t, runtimeConfig, content)
	changes, err := planGoImportChanges(root, "example.com/old/app", "example.com/new/app")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 {
		t.Fatalf("expected one change, got %d", len(changes))
	}
	result := string(changes[0].content)
	if !strings.Contains(result, `"example.com/new/app/internal/config"`) {
		t.Fatalf("internal import was not updated:\n%s", result)
	}
	if !strings.Contains(result, `"example.com/old/application"`) {
		t.Fatalf("similar external import was changed:\n%s", result)
	}
	ignored, err := os.ReadFile(runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(ignored) != content {
		t.Fatal("top-level runtime config directory should not be rewritten")
	}
}

func TestGenerateAndCheckProject(t *testing.T) {
	root := t.TempDir()
	manifest := validManifest()
	writeFixture(t, filepath.Join(root, "go.mod"), "module "+manifest.GoModule+"\n\ngo 1.25.4\n")
	writeFixture(t, filepath.Join(root, "frontend/public/favicon.svg"), "<svg></svg>\n")
	if err := saveManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	if err := generateProjectMetadata(root, manifest); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(root, generatedGoFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := generateProjectMetadata(root, manifest); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(root, generatedGoFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("generation is not idempotent")
	}
	if err := checkProject(root, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "frontend/public/favicon.svg")); err != nil {
		t.Fatal(err)
	}
	if err := checkProject(root, manifest); err == nil || !strings.Contains(err.Error(), "favicon") {
		t.Fatalf("expected missing favicon error, got %v", err)
	}
}

func TestFindIdentityReferences(t *testing.T) {
	root := t.TempDir()
	oldManifest := validManifest()
	if err := saveManifest(root, oldManifest); err != nil {
		t.Fatal(err)
	}
	nextManifest := oldManifest
	nextManifest.DisplayName = "New App"
	writeFixture(t, filepath.Join(root, "README.md"), "Legacy: "+oldManifest.DisplayName+"\n")
	references, err := findIdentityReferences(root, oldManifest, nextManifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 1 || !strings.Contains(references[0], "README.md") {
		t.Fatalf("unexpected references: %v", references)
	}
}

func TestFindIdentityReferencesAllowsRetainedValues(t *testing.T) {
	root := t.TempDir()
	oldManifest := validManifest()
	nextManifest := oldManifest
	nextManifest.GoModule = "example.com/acme/app/v2"
	writeFixture(t, filepath.Join(root, "README.md"), oldManifest.GoModule+"\n")

	references, err := findIdentityReferences(root, oldManifest, nextManifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) != 0 {
		t.Fatalf("retained identity should not be reported: %v", references)
	}
}

func writeFixture(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
