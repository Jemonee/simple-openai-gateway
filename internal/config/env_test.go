package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearRequiredRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, key := range requiredRuntimeEnvKeys {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("os.Unsetenv(%s) error = %v", key, err)
		}
	}
}

func TestLoadEnvFileLoadsMissingEnvironmentValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GATEWAY_ENV_TEST=file-value\n"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	t.Setenv("GATEWAY_ENV_TEST", "")
	if err := os.Unsetenv("GATEWAY_ENV_TEST"); err != nil {
		t.Fatalf("os.Unsetenv() error = %v", err)
	}

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}
	if got := os.Getenv("GATEWAY_ENV_TEST"); got != "file-value" {
		t.Fatalf("GATEWAY_ENV_TEST = %q, want %q", got, "file-value")
	}
}

func TestLoadEnvFilePreservesExistingEnvironmentValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("GATEWAY_ENV_TEST=file-value\n"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	t.Setenv("GATEWAY_ENV_TEST", "process-value")

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}
	if got := os.Getenv("GATEWAY_ENV_TEST"); got != "process-value" {
		t.Fatalf("GATEWAY_ENV_TEST = %q, want %q", got, "process-value")
	}
}

func TestLoadEnvFileAllowsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile() error = %v", err)
	}
}

func TestLoadEnvFileRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("INVALID LINE\n"), 0600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	if err := loadEnvFile(path); err == nil {
		t.Fatal("loadEnvFile() error = nil, want parse error")
	}
}

func TestOpenBrowserOnStart(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		unset     bool
		want      bool
		wantError bool
	}{
		{name: "unset defaults enabled", unset: true, want: true},
		{name: "empty defaults enabled", value: "", want: true},
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "invalid", value: "disabled", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(OpenBrowserOnStartEnvKey, test.value)
			if test.unset {
				if err := os.Unsetenv(OpenBrowserOnStartEnvKey); err != nil {
					t.Fatalf("os.Unsetenv() error = %v", err)
				}
			}
			got, err := OpenBrowserOnStart()
			if test.wantError {
				if err == nil {
					t.Fatal("OpenBrowserOnStart() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("OpenBrowserOnStart() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("OpenBrowserOnStart() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLoadOrCreateRuntimeEnvGeneratesMissingCriticalValues(t *testing.T) {
	clearRequiredRuntimeEnv(t)
	path := filepath.Join(t.TempDir(), ".env")

	if err := loadOrCreateRuntimeEnv(path); err != nil {
		t.Fatalf("loadOrCreateRuntimeEnv() error = %v", err)
	}
	masterKey, err := base64.StdEncoding.DecodeString(os.Getenv(GatewayMasterKeyEnvKey))
	if err != nil || len(masterKey) != 32 {
		t.Fatalf("generated master key is invalid: bytes=%d error=%v", len(masterKey), err)
	}
	if username := os.Getenv(GatewayAdminUsernameKey); !strings.HasPrefix(username, "admin_") {
		t.Fatalf("generated username = %q", username)
	}
	if password := os.Getenv(GatewayAdminPasswordKey); len(password) < 12 {
		t.Fatalf("generated password length = %d", len(password))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range requiredRuntimeEnvKeys {
		if !strings.Contains(string(content), key+"=") {
			t.Fatalf("generated .env does not contain %s", key)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated .env permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadOrCreateRuntimeEnvPreservesExistingAndProcessValues(t *testing.T) {
	clearRequiredRuntimeEnv(t)
	directory := t.TempDir()
	path := filepath.Join(directory, ".env")
	existingKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	content := "GATEWAY_MASTER_KEY=" + existingKey + "\nCUSTOM_SETTING=kept\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(GatewayAdminUsernameKey, "process-admin")

	if err := loadOrCreateRuntimeEnv(path); err != nil {
		t.Fatalf("loadOrCreateRuntimeEnv() error = %v", err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	if !strings.Contains(text, "CUSTOM_SETTING=kept") || !strings.Contains(text, "GATEWAY_MASTER_KEY="+existingKey) {
		t.Fatalf("existing .env content was not preserved:\n%s", text)
	}
	if strings.Contains(text, "GATEWAY_ADMIN_USERNAME=process-admin") {
		t.Fatalf("process value was persisted to .env:\n%s", text)
	}
	if !strings.Contains(text, GatewayAdminPasswordKey+"=") {
		t.Fatalf("missing password was not appended:\n%s", text)
	}
}
