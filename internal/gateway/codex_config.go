package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
	"gorm.io/gorm"
)

const (
	codexProviderModeExisting = "existing"
	codexProviderModeNew      = "new"
	codexTokenModeExisting    = "existing"
	codexTokenModeNew         = "new"
)

var codexConfigMu sync.Mutex

type CodexProviderView struct {
	Name               string `json:"name"`
	DisplayName        string `json:"displayName"`
	BaseURL            string `json:"baseUrl"`
	WireAPI            string `json:"wireApi"`
	RequiresOpenAIAuth bool   `json:"requiresOpenAIAuth"`
}

type CodexConfigurationView struct {
	ConfigPath     string              `json:"configPath"`
	AuthPath       string              `json:"authPath"`
	ConfigExists   bool                `json:"configExists"`
	AuthExists     bool                `json:"authExists"`
	ModelProvider  string              `json:"modelProvider"`
	Model          string              `json:"model"`
	Providers      []CodexProviderView `json:"providers"`
	AuthConfigured bool                `json:"authConfigured"`
	AuthKeyPrefix  string              `json:"authKeyPrefix"`
	AuthTokenID    uint64              `json:"authTokenId"`
}

type CodexConfigurationInput struct {
	ProviderMode string `json:"providerMode"`
	ProviderName string `json:"providerName"`
	Model        string `json:"model"`
	BaseURL      string `json:"baseUrl"`
	TokenMode    string `json:"tokenMode"`
	TokenID      uint64 `json:"tokenId"`
	NewTokenName string `json:"newTokenName"`
}

type codexFiles struct {
	home       string
	configPath string
	authPath   string
}

type codexDocument struct {
	value  map[string]any
	raw    []byte
	exists bool
}

func resolveCodexFiles() (codexFiles, error) {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return codexFiles{}, fmt.Errorf("定位用户主目录: %w", err)
		}
		home = filepath.Join(userHome, ".codex")
	}
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return codexFiles{}, fmt.Errorf("解析 Codex 配置目录: %w", err)
	}
	return codexFiles{
		home:       absoluteHome,
		configPath: filepath.Join(absoluteHome, "config.toml"),
		authPath:   filepath.Join(absoluteHome, "auth.json"),
	}, nil
}

func readCodexTOML(path string) (codexDocument, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return codexDocument{value: map[string]any{}}, nil
	}
	if err != nil {
		return codexDocument{}, err
	}
	value := map[string]any{}
	if err := toml.Unmarshal(raw, &value); err != nil {
		return codexDocument{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	return codexDocument{value: value, raw: raw, exists: true}, nil
}

func readCodexJSON(path string) (codexDocument, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return codexDocument{value: map[string]any{}}, nil
	}
	if err != nil {
		return codexDocument{}, err
	}
	value := map[string]any{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return codexDocument{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	return codexDocument{value: value, raw: raw, exists: true}, nil
}

func codexObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok
}

func codexString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func codexBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func codexProviders(document map[string]any) []CodexProviderView {
	providerValues, _ := codexObject(document["model_providers"])
	providers := make([]CodexProviderView, 0, len(providerValues))
	for name, value := range providerValues {
		provider, ok := codexObject(value)
		if !ok {
			continue
		}
		providers = append(providers, CodexProviderView{
			Name:               name,
			DisplayName:        codexString(provider["name"]),
			BaseURL:            codexString(provider["base_url"]),
			WireAPI:            codexString(provider["wire_api"]),
			RequiresOpenAIAuth: codexBool(provider["requires_openai_auth"]),
		})
	}
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].Name < providers[j].Name
	})
	return providers
}

func (s *ManagementService) CodexConfiguration(ctx context.Context) (*CodexConfigurationView, error) {
	files, err := resolveCodexFiles()
	if err != nil {
		return nil, err
	}
	configDocument, err := readCodexTOML(files.configPath)
	if err != nil {
		return nil, err
	}
	authDocument, err := readCodexJSON(files.authPath)
	if err != nil {
		return nil, err
	}
	apiKey := codexString(authDocument.value["OPENAI_API_KEY"])
	view := &CodexConfigurationView{
		ConfigPath:     files.configPath,
		AuthPath:       files.authPath,
		ConfigExists:   configDocument.exists,
		AuthExists:     authDocument.exists,
		ModelProvider:  codexString(configDocument.value["model_provider"]),
		Model:          codexString(configDocument.value["model"]),
		Providers:      codexProviders(configDocument.value),
		AuthConfigured: apiKey != "",
	}
	if apiKey != "" {
		view.AuthKeyPrefix = visibleKeyPrefix(apiKey)
		var token ClientToken
		if err := s.store.db.WithContext(ctx).Where("key_hash = ?", hashSecret(apiKey)).First(&token).Error; err == nil {
			view.AuthTokenID = token.ID
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	return view, nil
}

func validateCodexConfigurationInput(input CodexConfigurationInput) (CodexConfigurationInput, error) {
	input.ProviderMode = strings.TrimSpace(input.ProviderMode)
	input.ProviderName = strings.TrimSpace(input.ProviderName)
	input.Model = strings.TrimSpace(input.Model)
	input.TokenMode = strings.TrimSpace(input.TokenMode)
	input.NewTokenName = strings.TrimSpace(input.NewTokenName)
	if input.ProviderMode != codexProviderModeExisting && input.ProviderMode != codexProviderModeNew {
		return input, errors.New("请选择修改现有供应商或新增供应商")
	}
	if input.ProviderName == "" || len(input.ProviderName) > 64 {
		return input, errors.New("供应商标识不能为空且不能超过 64 个字符")
	}
	for _, character := range input.ProviderName {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return input, errors.New("供应商标识只能包含字母、数字、下划线和连字符")
	}
	if input.Model == "" || len(input.Model) > 200 {
		return input, errors.New("请选择 Codex 模型")
	}
	baseURL, err := normalizeBaseURL(input.BaseURL)
	if err != nil {
		return input, errors.New("服务地址必须是有效的 HTTP 或 HTTPS 地址")
	}
	input.BaseURL = baseURL
	if input.TokenMode != codexTokenModeExisting && input.TokenMode != codexTokenModeNew {
		return input, errors.New("请选择现有访问令牌或新增访问令牌")
	}
	if input.TokenMode == codexTokenModeExisting && input.TokenID == 0 {
		return input, errors.New("请选择可复用的访问令牌")
	}
	if input.TokenMode == codexTokenModeNew && input.NewTokenName == "" {
		return input, errors.New("请输入新访问令牌名称")
	}
	return input, nil
}

func encodeCodexTOML(value map[string]any) ([]byte, error) {
	raw, err := toml.Marshal(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func encodeCodexJSON(value map[string]any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func atomicWriteCodexFile(path string, raw []byte, defaultMode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	mode := defaultMode
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".codex-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func restoreCodexDocument(path string, document codexDocument, mode os.FileMode) error {
	if document.exists {
		return atomicWriteCodexFile(path, document.raw, mode)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func writeCodexDocuments(files codexFiles, configDocument codexDocument, authDocument codexDocument) error {
	configRaw, err := encodeCodexTOML(configDocument.value)
	if err != nil {
		return err
	}
	authRaw, err := encodeCodexJSON(authDocument.value)
	if err != nil {
		return err
	}
	if err := atomicWriteCodexFile(files.configPath, configRaw, 0600); err != nil {
		return fmt.Errorf("写入 config.toml: %w", err)
	}
	if err := atomicWriteCodexFile(files.authPath, authRaw, 0600); err != nil {
		if restoreErr := restoreCodexDocument(files.configPath, configDocument, 0600); restoreErr != nil {
			return fmt.Errorf("写入 auth.json: %v；恢复 config.toml: %w", err, restoreErr)
		}
		return fmt.Errorf("写入 auth.json: %w", err)
	}
	return nil
}

func (s *ManagementService) SaveCodexConfiguration(ctx context.Context, input CodexConfigurationInput) (*CodexConfigurationView, error) {
	input, err := validateCodexConfigurationInput(input)
	if err != nil {
		return nil, err
	}
	codexConfigMu.Lock()
	defer codexConfigMu.Unlock()

	files, err := resolveCodexFiles()
	if err != nil {
		return nil, err
	}
	configDocument, err := readCodexTOML(files.configPath)
	if err != nil {
		return nil, err
	}
	authDocument, err := readCodexJSON(files.authPath)
	if err != nil {
		return nil, err
	}
	providers, ok := codexObject(configDocument.value["model_providers"])
	if !ok {
		providers = map[string]any{}
		configDocument.value["model_providers"] = providers
	}
	_, providerExists := providers[input.ProviderName]
	if input.ProviderMode == codexProviderModeExisting && !providerExists {
		return nil, errors.New("选择的现有供应商已不存在，请重新加载")
	}
	if input.ProviderMode == codexProviderModeNew && providerExists {
		return nil, errors.New("供应商标识已存在，请改为修改现有供应商或更换标识")
	}

	apiKey := ""
	createdTokenID := uint64(0)
	var model GatewayModel
	if err := s.store.db.WithContext(ctx).Where("name = ? AND enabled = ?", input.Model, true).First(&model).Error; err != nil {
		return nil, errors.New("选择的模型不存在或未启用")
	}
	if input.TokenMode == codexTokenModeExisting {
		apiKey = codexString(authDocument.value["OPENAI_API_KEY"])
		if apiKey == "" {
			return nil, errors.New("auth.json 中没有可复用的访问令牌，请新增令牌")
		}
		var token ClientToken
		if err := s.store.db.WithContext(ctx).First(&token, input.TokenID).Error; err != nil {
			return nil, err
		}
		if !token.Enabled {
			return nil, errors.New("选择的访问令牌已停用")
		}
		if token.KeyHash != hashSecret(apiKey) {
			return nil, errors.New("现有令牌明文不可恢复，仅可复用 auth.json 当前已配置的匹配令牌")
		}
		if !token.AllowAllModels {
			var permissionCount int64
			if err := s.store.db.WithContext(ctx).Model(&ClientTokenModel{}).
				Where("token_id = ? AND model_id = ?", token.ID, model.ID).Count(&permissionCount).Error; err != nil {
				return nil, err
			}
			if permissionCount == 0 {
				return nil, errors.New("选择的现有访问令牌没有该模型权限，请新增令牌或先调整令牌策略")
			}
		}
	} else {
		issued, err := s.issueToken(ctx, ClientTokenInput{
			Name:           input.NewTokenName,
			Enabled:        true,
			AllowAllModels: false,
			RPM:            60,
			MaxConcurrency: 10,
			ModelIDs:       []uint64{model.ID},
		}, nil)
		if err != nil {
			return nil, err
		}
		apiKey = issued.Secret
		createdTokenID = issued.Token.ID
	}

	provider, _ := codexObject(providers[input.ProviderName])
	if provider == nil {
		provider = map[string]any{}
	}
	if input.ProviderMode == codexProviderModeNew || codexString(provider["name"]) == "" {
		provider["name"] = input.ProviderName
	}
	provider["wire_api"] = "responses"
	provider["requires_openai_auth"] = true
	provider["base_url"] = input.BaseURL
	providers[input.ProviderName] = provider
	configDocument.value["model_provider"] = input.ProviderName
	configDocument.value["model"] = input.Model
	configDocument.value["preferred_auth_method"] = "apikey"
	authDocument.value["OPENAI_API_KEY"] = apiKey

	if err := writeCodexDocuments(files, configDocument, authDocument); err != nil {
		if createdTokenID != 0 {
			if rollbackErr := s.DeleteToken(ctx, createdTokenID); rollbackErr != nil {
				return nil, fmt.Errorf("%v；回滚新访问令牌: %w", err, rollbackErr)
			}
		}
		return nil, err
	}
	return s.CodexConfiguration(ctx)
}
