package config

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/Jemonee/simple-openai-gateway/internal/projectmeta"

	"github.com/pelletier/go-toml/v2"
)

type configFieldPresence struct {
	WebHost              bool
	WebPort              bool
	NodeSharedToken      bool
	RoutingQualityWeight bool
	RoutingBalanceWeight bool
}

type startupGuidePlan struct {
	FirstRun           bool
	PromptWebHost      bool
	PromptWebPort      bool
	PromptSharedToken  bool
	DefaultSharedToken string
}

var tokenValuePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var hostNamePattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

func (plan startupGuidePlan) HasQuestions() bool {
	return plan.PromptWebHost || plan.PromptWebPort || plan.PromptSharedToken
}

func detectConfigFieldPresence(content []byte) (configFieldPresence, error) {
	var raw map[string]any
	if err := toml.Unmarshal(content, &raw); err != nil {
		return configFieldPresence{}, err
	}
	return configFieldPresence{
		WebHost:              hasTomlKey(raw, "web_config", "host"),
		WebPort:              hasTomlKey(raw, "web_config", "port"),
		NodeSharedToken:      hasTomlKey(raw, "node_config", "shared_token"),
		RoutingQualityWeight: hasTomlKey(raw, "gateway_config", "routing_quality_weight_percent"),
		RoutingBalanceWeight: hasTomlKey(raw, "gateway_config", "routing_balance_weight_percent"),
	}, nil
}

func buildStartupGuidePlan(firstRun bool, presence configFieldPresence, cfg *ApplicationConfig) (startupGuidePlan, error) {
	plan := startupGuidePlan{
		FirstRun:          firstRun,
		PromptWebHost:     firstRun || !presence.WebHost || strings.TrimSpace(cfg.WebConfig.Host) == "",
		PromptWebPort:     firstRun || !presence.WebPort || strings.TrimSpace(cfg.WebConfig.Port) == "",
		PromptSharedToken: firstRun || !presence.NodeSharedToken || strings.TrimSpace(cfg.NodeConfig.SharedToken) == "",
	}
	if plan.PromptSharedToken {
		defaultToken := strings.TrimSpace(cfg.NodeConfig.SharedToken)
		if defaultToken == "" {
			generatedToken, err := generateSharedToken()
			if err != nil {
				return startupGuidePlan{}, err
			}
			defaultToken = generatedToken
		}
		plan.DefaultSharedToken = defaultToken
	}
	return plan, nil
}

func applyRuntimeFallbacks(cfg *ApplicationConfig, plan startupGuidePlan) (bool, error) {
	changed := false
	if strings.TrimSpace(cfg.WebConfig.Host) == "" {
		cfg.WebConfig.Host = DefaultWebHost
		changed = true
	}
	if strings.TrimSpace(cfg.WebConfig.Port) == "" {
		cfg.WebConfig.Port = DefaultWebPort
		changed = true
	}
	if strings.TrimSpace(cfg.NodeConfig.SharedToken) == "" {
		defaultToken := plan.DefaultSharedToken
		if defaultToken == "" {
			generatedToken, err := generateSharedToken()
			if err != nil {
				return false, err
			}
			defaultToken = generatedToken
		}
		cfg.NodeConfig.SharedToken = defaultToken
		changed = true
	}
	return changed, nil
}

func hasTomlKey(raw map[string]any, tableName string, key string) bool {
	sectionValue, ok := raw[tableName]
	if !ok {
		return false
	}
	sectionMap, ok := sectionValue.(map[string]any)
	if !ok {
		return false
	}
	_, ok = sectionMap[key]
	return ok
}

func canPromptForConfig() bool {
	return isTerminal(os.Stdin) && isTerminal(os.Stdout)
}

func isTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func runStartupConfigGuide(configFilePath string, cfg *ApplicationConfig, plan startupGuidePlan) error {
	guide := startupConfigGuide{
		reader: bufio.NewReader(os.Stdin),
		writer: os.Stdout,
	}
	return guide.run(configFilePath, cfg, plan)
}

type startupConfigGuide struct {
	reader *bufio.Reader
	writer io.Writer
}

func (g startupConfigGuide) run(configFilePath string, cfg *ApplicationConfig, plan startupGuidePlan) error {
	if plan.FirstRun {
		fmt.Fprintf(g.writer, "\n%s 首次启动，正在引导填写配置。\n", projectmeta.DisplayName)
	} else {
		fmt.Fprintf(g.writer, "\n检测到配置文件存在缺失项，正在补全启动配置。\n")
	}
	fmt.Fprintf(g.writer, "配置文件: %s\n", configFilePath)
	fmt.Fprintln(g.writer, "直接回车可接受方括号中的默认值。")

	if plan.PromptWebHost {
		value, err := g.promptString("web_config.host", "监听主机地址，例如 0.0.0.0、localhost 或局域网 IP", defaultString(cfg.WebConfig.Host, DefaultWebHost), false, validateHost)
		if err != nil {
			return err
		}
		cfg.WebConfig.Host = value
	}
	if plan.PromptWebPort {
		value, err := g.promptString("web_config.port", "监听端口，范围 1-65535", defaultString(cfg.WebConfig.Port, DefaultWebPort), false, validatePort)
		if err != nil {
			return err
		}
		cfg.WebConfig.Port = value
	}
	if plan.PromptSharedToken {
		value, err := g.promptString("node_config.shared_token", "节点间互联使用的共享令牌，添加远程节点时要填写对端这个值", plan.DefaultSharedToken, false, validateSharedToken)
		if err != nil {
			return err
		}
		cfg.NodeConfig.SharedToken = value
	}
	fmt.Fprintln(g.writer, "配置填写完成，应用继续启动。")
	return nil
}

func (g startupConfigGuide) promptString(label string, description string, defaultValue string, allowEmpty bool, validator func(string) error) (string, error) {
	for {
		if strings.TrimSpace(description) != "" {
			fmt.Fprintf(g.writer, "%s：%s\n", label, description)
		}
		if defaultValue != "" {
			fmt.Fprintf(g.writer, "%s [%s]: ", label, defaultValue)
		} else {
			fmt.Fprintf(g.writer, "%s: ", label)
		}
		input, err := g.readLine()
		if err != nil {
			return "", err
		}
		resolvedValue := strings.TrimSpace(input)
		if resolvedValue == "" {
			if defaultValue != "" {
				resolvedValue = defaultValue
			}
			if allowEmpty {
				return "", nil
			}
		}
		if resolvedValue == "" {
			fmt.Fprintln(g.writer, "输入不能为空，请重新填写。")
			continue
		}
		if validator != nil {
			if err := validator(resolvedValue); err != nil {
				fmt.Fprintf(g.writer, "%s\n", err.Error())
				continue
			}
		}
		return resolvedValue, nil
	}
}

func (g startupConfigGuide) promptBool(label string, description string, defaultValue bool) (bool, error) {
	defaultText := "n"
	if defaultValue {
		defaultText = "y"
	}
	for {
		if strings.TrimSpace(description) != "" {
			fmt.Fprintf(g.writer, "%s：%s\n", label, description)
		}
		fmt.Fprintf(g.writer, "%s [y/n，默认 %s]: ", label, defaultText)
		input, err := g.readLine()
		if err != nil {
			return false, err
		}
		trimmed := strings.TrimSpace(strings.ToLower(input))
		if trimmed == "" {
			return defaultValue, nil
		}
		switch trimmed {
		case "y", "yes", "true", "1":
			return true, nil
		case "n", "no", "false", "0":
			return false, nil
		default:
			fmt.Fprintln(g.writer, "请输入 y/n、true/false 或 1/0。")
		}
	}
}

func (g startupConfigGuide) readLine() (string, error) {
	line, err := g.reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return strings.TrimRight(line, "\r\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func defaultString(value string, fallback string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		return trimmed
	}
	return fallback
}

func validateHost(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("host 不能为空")
	}
	if strings.Contains(trimmed, "://") {
		return fmt.Errorf("host 只需要填写主机地址，不要带协议头")
	}
	if strings.ContainsAny(trimmed, "/?# ") {
		return fmt.Errorf("host 不能包含路径、查询参数或空格")
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		_ = host
		return fmt.Errorf("host 只需要填写主机地址，不要带端口")
	}
	if err := validateHostOnly(trimmed); err != nil {
		return err
	}
	return nil
}

func validatePort(value string) error {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fmt.Errorf("port 必须是 1-65535 的整数")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("port 必须是 1-65535 的整数")
	}
	return nil
}

func validateSharedToken(value string) error {
	return validatePrefixedToken(value, projectmeta.TokenPrefix, "shared_token")
}

func validatePrefixedToken(value string, prefix string, fieldName string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s 不能为空", fieldName)
	}
	expectedPrefix := prefix + "_"
	if !strings.HasPrefix(trimmed, expectedPrefix) {
		return fmt.Errorf("%s 必须以 %s 开头", fieldName, expectedPrefix)
	}
	suffix := strings.TrimPrefix(trimmed, expectedPrefix)
	if len(suffix) < 16 {
		return fmt.Errorf("%s 长度过短，请重新输入完整令牌", fieldName)
	}
	if !tokenValuePattern.MatchString(suffix) {
		return fmt.Errorf("%s 只能包含字母、数字、下划线和短横线", fieldName)
	}
	return nil
}

func validateHostOrEndpoint(value string, allowPort bool) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	if host, port, err := net.SplitHostPort(trimmed); err == nil {
		if !allowPort {
			return fmt.Errorf("该字段不允许包含端口")
		}
		if err := validateHostOnly(host); err != nil {
			return err
		}
		if err := validatePort(port); err != nil {
			return err
		}
		return nil
	}
	if strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]") {
		return fmt.Errorf("IPv6 地址如果包含端口，请使用 [地址]:端口 格式")
	}
	if strings.Count(trimmed, ":") == 1 && !allowPort {
		return fmt.Errorf("该字段不允许包含端口")
	}
	if strings.Count(trimmed, ":") == 1 && allowPort {
		return fmt.Errorf("地址格式不正确，请使用 host、host:port、IP 或 http(s)://host:port")
	}
	return validateHostOnly(trimmed)
}

func validateHostOnly(value string) error {
	trimmed := strings.Trim(strings.TrimSpace(value), "[]")
	if trimmed == "" {
		return fmt.Errorf("主机地址不能为空")
	}
	if net.ParseIP(trimmed) != nil {
		return nil
	}
	if strings.EqualFold(trimmed, "localhost") {
		return nil
	}
	if !hostNamePattern.MatchString(trimmed) {
		return fmt.Errorf("主机地址格式不正确")
	}
	parts := strings.Split(trimmed, ".")
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("主机地址格式不正确")
		}
		if strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return fmt.Errorf("主机地址格式不正确")
		}
	}
	return nil
}
