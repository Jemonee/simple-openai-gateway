package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	defaultEnvFileName       = ".env"
	GatewayMasterKeyEnvKey   = "GATEWAY_MASTER_KEY"
	GatewayAdminUsernameKey  = "GATEWAY_ADMIN_USERNAME"
	GatewayAdminPasswordKey  = "GATEWAY_ADMIN_PASSWORD"
	OpenBrowserOnStartEnvKey = "GATEWAY_OPEN_BROWSER"
)

var requiredRuntimeEnvKeys = []string{
	GatewayMasterKeyEnvKey,
	GatewayAdminUsernameKey,
	GatewayAdminPasswordKey,
}

// LoadRuntimeEnv loads optional process configuration from the working directory.
// Existing environment variables take precedence over values declared in .env.
func LoadRuntimeEnv() error {
	if err := loadOrCreateRuntimeEnv(filepath.Join(".", defaultEnvFileName)); err != nil {
		return err
	}
	_, err := OpenBrowserOnStart()
	return err
}

func loadOrCreateRuntimeEnv(path string) error {
	if err := loadEnvFile(path); err != nil {
		return err
	}
	generated := make(map[string]string)
	for _, key := range requiredRuntimeEnvKeys {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			continue
		}
		value, err := generateRuntimeEnvValue(key)
		if err != nil {
			return fmt.Errorf("generate %s: %w", key, err)
		}
		generated[key] = value
	}
	if len(generated) == 0 {
		return nil
	}
	if err := appendEnvValues(path, generated); err != nil {
		return err
	}
	for _, key := range requiredRuntimeEnvKeys {
		if value, ok := generated[key]; ok {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}
	}
	return nil
}

func generateRuntimeEnvValue(key string) (string, error) {
	size := 32
	if key == GatewayAdminUsernameKey {
		size = 6
	}
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	switch key {
	case GatewayMasterKeyEnvKey:
		return base64.StdEncoding.EncodeToString(raw), nil
	case GatewayAdminUsernameKey:
		return "admin_" + base64.RawURLEncoding.EncodeToString(raw), nil
	case GatewayAdminPasswordKey:
		return "gw_" + base64.RawURLEncoding.EncodeToString(raw), nil
	default:
		return base64.RawURLEncoding.EncodeToString(raw), nil
	}
}

func appendEnvValues(path string, values map[string]string) error {
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var addition strings.Builder
	if len(content) > 0 && content[len(content)-1] != '\n' {
		addition.WriteByte('\n')
	}
	addition.WriteString("# Automatically generated missing startup values.\n")
	for _, key := range requiredRuntimeEnvKeys {
		if value, ok := values[key]; ok {
			addition.WriteString(key)
			addition.WriteByte('=')
			addition.WriteString(value)
			addition.WriteByte('\n')
		}
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create environment directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("restrict %s: %w", path, err)
	}
	if _, err := file.WriteString(addition.String()); err != nil {
		_ = file.Close()
		return fmt.Errorf("append %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

// OpenBrowserOnStart reports whether startup should open the local interface.
// The setting defaults to true and accepts the boolean forms supported by strconv.ParseBool.
func OpenBrowserOnStart() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(OpenBrowserOnStartEnvKey))
	if raw == "" {
		return true, nil
	}
	enabled, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true, false, 1, or 0: %w", OpenBrowserOnStartEnvKey, err)
	}
	return enabled, nil
}

func loadEnvFile(path string) error {
	if err := godotenv.Load(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("load %s: %w", path, err)
	}
	return nil
}
