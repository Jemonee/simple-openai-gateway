package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Jemonee/simple-openai-gateway/internal/projectmeta"
	"github.com/Jemonee/simple-openai-gateway/pkg/until"

	"github.com/creasty/defaults"
	"github.com/pelletier/go-toml/v2"
)

var log = until.Log
var configPath = filepath.Join("./", "config", "config.toml")

const (
	DefaultWebHost                        = "0.0.0.0"
	DefaultWebPort                        = "8888"
	DefaultRoutingPriceWeightPercent      = 40
	DefaultRoutingEfficiencyWeightPercent = 35
	DefaultRoutingQualityWeightPercent    = 15
	DefaultRoutingBalanceWeightPercent    = 10
	MinimumRoutingQualityWeightPercent    = 5
	PayloadLogDetailDefault               = "default"
	PayloadLogDetailSummary               = "summary"
	PayloadLogDetailNone                  = "none"
)

var DefaultCommonModelNames = []string{
	"gpt-image-2",
	"gpt-5.6-terra",
	"gpt-5.6-sol",
	"gpt-5.6-luna",
	"gpt-5.5",
	"gpt-5.4-mini",
	"codex-auto-review",
}

type ApplicationConfigManager struct {
	mu     sync.RWMutex
	config *ApplicationConfig
}

func (acm *ApplicationConfigManager) GetConfig() *ApplicationConfig {
	acm.mu.RLock()
	defer acm.mu.RUnlock()
	if acm.config == nil {
		return nil
	}
	configCopy := *acm.config
	configCopy.GatewayConfig.CommonModelNames = append([]string(nil), acm.config.GatewayConfig.CommonModelNames...)
	return &configCopy
}

func (acm *ApplicationConfigManager) Load() {
	firstRun := false
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		createDefaultConfig(configPath)
		firstRun = true
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		panic("读取配置文件失败")
	}
	presence, err := detectConfigFieldPresence(content)
	if err != nil {
		panic(fmt.Sprintf("解析配置项存在性失败: %v", err))
	}

	// 解析配置文件
	var cfg ApplicationConfig
	if err := defaults.Set(&cfg); err != nil {
		panic(err)
	}
	if err := toml.Unmarshal(content, &cfg); err != nil {
		panic("解析配置文件失败")
	}
	payloadLogDetail, err := normalizePayloadLogDetail(cfg.GatewayConfig.PayloadLogDetail)
	if err != nil {
		panic(err)
	}
	cfg.GatewayConfig.PayloadLogDetail = payloadLogDetail
	cfg.GatewayConfig.CommonModelNames = normalizeCommonModelNames(cfg.GatewayConfig.CommonModelNames)
	routingWeightsMigrated := completeRoutingDecisionWeights(&cfg.GatewayConfig, presence)
	if err := normalizeRoutingDecisionWeights(&cfg.GatewayConfig); err != nil {
		panic(err)
	}

	guidePlan, err := buildStartupGuidePlan(firstRun, presence, &cfg)
	if err != nil {
		panic(fmt.Sprintf("构建启动配置引导失败: %v", err))
	}
	if guidePlan.HasQuestions() {
		if canPromptForConfig() {
			if guideErr := runStartupConfigGuide(configPath, &cfg, guidePlan); guideErr != nil {
				panic(fmt.Sprintf("启动配置引导失败: %v", guideErr))
			}
		} else {
			log.Warn("检测到缺失启动配置，但当前环境不是交互式终端，已按默认值或自动生成值继续启动")
		}
	}
	needRewrite, err := applyRuntimeFallbacks(&cfg, guidePlan)
	if err != nil {
		panic(fmt.Sprintf("补全启动配置失败: %v", err))
	}
	needRewrite = needRewrite || routingWeightsMigrated
	if firstRun || guidePlan.HasQuestions() || needRewrite {
		if writeErr := writeConfigFile(configPath, &cfg); writeErr != nil {
			panic(fmt.Sprintf("写入配置文件失败: %v", writeErr))
		}
	}

	acm.mu.Lock()
	acm.config = &cfg
	acm.mu.Unlock()
}

func NewApplicationConfigManager() *ApplicationConfigManager {
	configManager := &ApplicationConfigManager{}
	configManager.Load()
	return configManager
}

type ApplicationConfig struct {
	WebConfig     WebConfig     `toml:"web_config" json:"webConfig"`
	NodeConfig    NodeConfig    `toml:"node_config" json:"nodeConfig"`
	GatewayConfig GatewayConfig `toml:"gateway_config" json:"gatewayConfig"`
}

type WebConfig struct {
	Host string `toml:"host" json:"host" default:"0.0.0.0"`
	Port string `toml:"port" json:"port" default:"8888"`
}

type NodeConfig struct {
	SharedToken string `toml:"shared_token" json:"sharedToken"`
}

type GatewayConfig struct {
	MaxAttempts                    int      `toml:"max_attempts" json:"maxAttempts" default:"3"`
	RequestBodyLimitMB             int      `toml:"request_body_limit_mb" json:"requestBodyLimitMB" default:"32"`
	ResponseHeaderTimeoutSeconds   int      `toml:"response_header_timeout_seconds" json:"responseHeaderTimeoutSeconds" default:"120"`
	StreamIdleTimeoutSeconds       int      `toml:"stream_idle_timeout_seconds" json:"streamIdleTimeoutSeconds" default:"300"`
	RoutingPriceWeightPercent      int      `toml:"routing_price_weight_percent" json:"routingPriceWeightPercent" default:"40"`
	RoutingEfficiencyWeightPercent int      `toml:"routing_efficiency_weight_percent" json:"routingEfficiencyWeightPercent" default:"35"`
	RoutingQualityWeightPercent    int      `toml:"routing_quality_weight_percent" json:"routingQualityWeightPercent" default:"15"`
	RoutingBalanceWeightPercent    int      `toml:"routing_balance_weight_percent" json:"routingBalanceWeightPercent" default:"10"`
	SessionTTLHours                int      `toml:"session_ttl_hours" json:"sessionTTLHours" default:"12"`
	SecureCookie                   bool     `toml:"secure_cookie" json:"secureCookie" default:"false"`
	PayloadLogDetail               string   `toml:"payload_log_detail" json:"payloadLogDetail" default:"default"`
	CommonModelNames               []string `toml:"common_model_names" json:"commonModelNames"`
}

func (acm *ApplicationConfigManager) Save(cfg *ApplicationConfig) error {
	if cfg == nil {
		return errors.New("配置不能为空")
	}
	configCopy := *cfg
	if configCopy.GatewayConfig.RoutingQualityWeightPercent == 0 && configCopy.GatewayConfig.RoutingBalanceWeightPercent == 0 && configCopy.GatewayConfig.RoutingPriceWeightPercent+configCopy.GatewayConfig.RoutingEfficiencyWeightPercent <= 100-MinimumRoutingQualityWeightPercent {
		completeRoutingDecisionWeights(&configCopy.GatewayConfig, configFieldPresence{})
	}
	payloadLogDetail, err := normalizePayloadLogDetail(configCopy.GatewayConfig.PayloadLogDetail)
	if err != nil {
		return err
	}
	configCopy.GatewayConfig.PayloadLogDetail = payloadLogDetail
	configCopy.GatewayConfig.CommonModelNames = normalizeCommonModelNames(configCopy.GatewayConfig.CommonModelNames)
	if err := normalizeRoutingDecisionWeights(&configCopy.GatewayConfig); err != nil {
		return err
	}
	if err := writeConfigFile(configPath, &configCopy); err != nil {
		return err
	}
	acm.mu.Lock()
	acm.config = &configCopy
	acm.mu.Unlock()
	return nil
}

func normalizeRoutingDecisionWeights(gatewayConfig *GatewayConfig) error {
	if gatewayConfig.RoutingPriceWeightPercent == 0 && gatewayConfig.RoutingEfficiencyWeightPercent == 0 && gatewayConfig.RoutingQualityWeightPercent == 0 && gatewayConfig.RoutingBalanceWeightPercent == 0 {
		gatewayConfig.RoutingPriceWeightPercent = DefaultRoutingPriceWeightPercent
		gatewayConfig.RoutingEfficiencyWeightPercent = DefaultRoutingEfficiencyWeightPercent
		gatewayConfig.RoutingQualityWeightPercent = DefaultRoutingQualityWeightPercent
		gatewayConfig.RoutingBalanceWeightPercent = DefaultRoutingBalanceWeightPercent
	} else if gatewayConfig.RoutingQualityWeightPercent == 0 && gatewayConfig.RoutingBalanceWeightPercent == 0 {
		remaining := 100 - gatewayConfig.RoutingPriceWeightPercent - gatewayConfig.RoutingEfficiencyWeightPercent
		if remaining >= MinimumRoutingQualityWeightPercent {
			gatewayConfig.RoutingBalanceWeightPercent = min(DefaultRoutingBalanceWeightPercent, remaining-MinimumRoutingQualityWeightPercent)
			gatewayConfig.RoutingQualityWeightPercent = remaining - gatewayConfig.RoutingBalanceWeightPercent
		}
	}
	price := gatewayConfig.RoutingPriceWeightPercent
	efficiency := gatewayConfig.RoutingEfficiencyWeightPercent
	quality := gatewayConfig.RoutingQualityWeightPercent
	balance := gatewayConfig.RoutingBalanceWeightPercent
	if price < 0 || price > 100 || efficiency < 0 || efficiency > 100 || quality < MinimumRoutingQualityWeightPercent || quality > 100 || balance < 0 || balance > 100 || price+efficiency+quality+balance != 100 {
		return fmt.Errorf("路由价格、效率、质量与均衡占比之和必须为 100%%，且质量占比不能低于 %d%%", MinimumRoutingQualityWeightPercent)
	}
	return nil
}

func completeRoutingDecisionWeights(gatewayConfig *GatewayConfig, presence configFieldPresence) bool {
	if presence.RoutingQualityWeight && presence.RoutingBalanceWeight {
		return false
	}
	price := min(max(gatewayConfig.RoutingPriceWeightPercent, 0), 100-MinimumRoutingQualityWeightPercent)
	efficiency := min(max(gatewayConfig.RoutingEfficiencyWeightPercent, 0), 100-price-MinimumRoutingQualityWeightPercent)
	if price == 0 && efficiency == 0 {
		price = DefaultRoutingPriceWeightPercent
		efficiency = DefaultRoutingEfficiencyWeightPercent
	}
	remaining := 100 - price - efficiency
	quality := gatewayConfig.RoutingQualityWeightPercent
	balance := gatewayConfig.RoutingBalanceWeightPercent
	switch {
	case !presence.RoutingQualityWeight && !presence.RoutingBalanceWeight:
		balance = min(DefaultRoutingBalanceWeightPercent, max(remaining-MinimumRoutingQualityWeightPercent, 0))
		quality = remaining - balance
	case !presence.RoutingQualityWeight:
		balance = min(max(balance, 0), max(remaining-MinimumRoutingQualityWeightPercent, 0))
		quality = remaining - balance
	case !presence.RoutingBalanceWeight:
		quality = min(max(quality, MinimumRoutingQualityWeightPercent), remaining)
		balance = remaining - quality
	}
	gatewayConfig.RoutingPriceWeightPercent = price
	gatewayConfig.RoutingEfficiencyWeightPercent = efficiency
	gatewayConfig.RoutingQualityWeightPercent = quality
	gatewayConfig.RoutingBalanceWeightPercent = balance
	return true
}

func normalizeCommonModelNames(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 200 {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return append([]string(nil), DefaultCommonModelNames...)
	}
	return normalized
}

func EffectiveRoutingDecisionWeights(cfg *ApplicationConfig) (price float64, efficiency float64, quality float64, balance float64) {
	pricePercent := DefaultRoutingPriceWeightPercent
	efficiencyPercent := DefaultRoutingEfficiencyWeightPercent
	qualityPercent := DefaultRoutingQualityWeightPercent
	balancePercent := DefaultRoutingBalanceWeightPercent
	if cfg != nil {
		candidate := cfg.GatewayConfig
		if normalizeRoutingDecisionWeights(&candidate) == nil {
			pricePercent = candidate.RoutingPriceWeightPercent
			efficiencyPercent = candidate.RoutingEfficiencyWeightPercent
			qualityPercent = candidate.RoutingQualityWeightPercent
			balancePercent = candidate.RoutingBalanceWeightPercent
		}
	}
	return float64(pricePercent) / 100, float64(efficiencyPercent) / 100, float64(qualityPercent) / 100, float64(balancePercent) / 100
}

func normalizePayloadLogDetail(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return PayloadLogDetailDefault, nil
	}
	switch value {
	case PayloadLogDetailDefault, PayloadLogDetailSummary, PayloadLogDetailNone:
		return value, nil
	default:
		return "", fmt.Errorf("调用日志记录细节无效: %s", value)
	}
}

func EffectivePayloadLogDetail(cfg *ApplicationConfig) string {
	if cfg == nil {
		return PayloadLogDetailDefault
	}
	value, err := normalizePayloadLogDetail(cfg.GatewayConfig.PayloadLogDetail)
	if err != nil {
		return PayloadLogDetailDefault
	}
	return value
}

func generateSharedToken() (string, error) {
	return generatePrefixedToken(projectmeta.TokenPrefix)
}

func generatePrefixedToken(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + "_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func encodeConfig(cfg *ApplicationConfig) ([]byte, error) {
	var buf bytes.Buffer
	encoder := toml.NewEncoder(&buf)
	encoder.SetIndentTables(true)
	encoder.SetTablesInline(false)
	if err := encoder.Encode(cfg); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeConfigFile(filePath string, cfg *ApplicationConfig) error {
	content, err := encodeConfig(cfg)
	if err != nil {
		return err
	}
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filePath, content, 0644)
}

func createDefaultConfig(filePath string) *ApplicationConfig {
	defaultConfig := &ApplicationConfig{}
	err := defaults.Set(defaultConfig)
	if err != nil {
		panic(err)
	}
	if err := writeConfigFile(filePath, defaultConfig); err != nil {
		panic(err)
	}
	return defaultConfig
}
