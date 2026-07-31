package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

func writeTestCodexFiles(t *testing.T, home string, config string, auth string) {
	t.Helper()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveCodexConfigurationReusesMatchingTokenAndPreservesDocuments(t *testing.T) {
	store := newTestStore(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	secret := "sk-existing-codex-token"
	model := GatewayModel{Name: "gpt-updated", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}
	token := ClientToken{
		Name: "existing", KeyHash: hashSecret(secret), KeyPrefix: visibleKeyPrefix(secret),
		Enabled: true, AllowAllModels: true, RPM: 60, MaxConcurrency: 10,
	}
	if err := store.db.Create(&token).Error; err != nil {
		t.Fatal(err)
	}
	writeTestCodexFiles(t, home, `model_provider = "custom"
model = "old-model"
personality = "pragmatic"

[model_providers.custom]
name = "Existing Custom"
wire_api = "responses"
requires_openai_auth = true
base_url = "http://127.0.0.1:8000/v1"

[projects."/tmp/project"]
trust_level = "trusted"
`, `{"OPENAI_API_KEY":"`+secret+`","tokens":{"access":"preserve"}}`)

	management := NewManagementService(store)
	before, err := management.CodexConfiguration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.ModelProvider != "custom" || before.Model != "old-model" || before.AuthTokenID != token.ID {
		t.Fatalf("initial Codex configuration = %+v", before)
	}

	after, err := management.SaveCodexConfiguration(context.Background(), CodexConfigurationInput{
		ProviderMode: codexProviderModeExisting,
		ProviderName: "custom",
		Model:        "gpt-updated",
		BaseURL:      "http://127.0.0.1:8888/v1/",
		TokenMode:    codexTokenModeExisting,
		TokenID:      token.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Model != "gpt-updated" || after.AuthTokenID != token.ID {
		t.Fatalf("saved Codex configuration = %+v", after)
	}

	configRaw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var configDocument map[string]any
	if err := toml.Unmarshal(configRaw, &configDocument); err != nil {
		t.Fatal(err)
	}
	if codexString(configDocument["personality"]) != "pragmatic" {
		t.Fatalf("personality was not preserved: %s", configRaw)
	}
	projects, _ := codexObject(configDocument["projects"])
	project, _ := codexObject(projects["/tmp/project"])
	if codexString(project["trust_level"]) != "trusted" {
		t.Fatalf("project trust was not preserved: %s", configRaw)
	}
	providers, _ := codexObject(configDocument["model_providers"])
	provider, _ := codexObject(providers["custom"])
	if codexString(provider["name"]) != "Existing Custom" || codexString(provider["base_url"]) != "http://127.0.0.1:8888/v1" {
		t.Fatalf("provider was not updated: %s", configRaw)
	}

	authRaw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var authDocument map[string]any
	if err := json.Unmarshal(authRaw, &authDocument); err != nil {
		t.Fatal(err)
	}
	if codexString(authDocument["OPENAI_API_KEY"]) != secret {
		t.Fatal("existing auth key was replaced")
	}
	preservedTokens, _ := codexObject(authDocument["tokens"])
	if codexString(preservedTokens["access"]) != "preserve" {
		t.Fatalf("unrelated auth fields were not preserved: %s", authRaw)
	}
}

func TestSaveCodexConfigurationCreatesScopedTokenAndNewProvider(t *testing.T) {
	store := newTestStore(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	writeTestCodexFiles(t, home, "personality = \"pragmatic\"\n", `{"refresh_token":"preserve"}`)
	model := GatewayModel{Name: "gpt-codex", RoutingStrategy: RoutingPriorityWeighted, Enabled: true}
	if err := store.db.Create(&model).Error; err != nil {
		t.Fatal(err)
	}

	view, err := NewManagementService(store).SaveCodexConfiguration(context.Background(), CodexConfigurationInput{
		ProviderMode: codexProviderModeNew,
		ProviderName: "local_gateway",
		Model:        model.Name,
		BaseURL:      "http://127.0.0.1:8888/v1",
		TokenMode:    codexTokenModeNew,
		NewTokenName: "local Codex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.ModelProvider != "local_gateway" || view.Model != model.Name || view.AuthTokenID == 0 {
		t.Fatalf("created Codex configuration = %+v", view)
	}

	authRaw, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var authDocument map[string]any
	if err := json.Unmarshal(authRaw, &authDocument); err != nil {
		t.Fatal(err)
	}
	secret := codexString(authDocument["OPENAI_API_KEY"])
	if secret == "" || codexString(authDocument["refresh_token"]) != "preserve" {
		t.Fatalf("auth document = %s", authRaw)
	}
	var token ClientToken
	if err := store.db.First(&token, view.AuthTokenID).Error; err != nil {
		t.Fatal(err)
	}
	if token.Name != "local Codex" || token.KeyHash != hashSecret(secret) || token.AllowAllModels {
		t.Fatalf("created token = %+v", token)
	}
	var link ClientTokenModel
	if err := store.db.Where("token_id = ?", token.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.ModelID != model.ID {
		t.Fatalf("created token model = %+v", link)
	}
}
