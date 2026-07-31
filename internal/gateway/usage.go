package gateway

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/tiktoken-go/tokenizer"
)

type TokenEstimator struct {
	codec tokenizer.Codec
}

func NewTokenEstimator() *TokenEstimator {
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		panic(err)
	}
	return &TokenEstimator{codec: codec}
}

func (e *TokenEstimator) EstimateJSON(data []byte) int64 {
	if len(data) == 0 {
		return 0
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	text := ""
	if err := decoder.Decode(&value); err == nil {
		var builder strings.Builder
		collectText(value, &builder)
		text = builder.String()
	}
	if strings.TrimSpace(text) == "" {
		text = string(data)
	}
	return e.EstimateText(text)
}

func (e *TokenEstimator) EstimateValue(value any) int64 {
	var builder strings.Builder
	collectText(value, &builder)
	return e.EstimateText(builder.String())
}

func (e *TokenEstimator) EstimateText(text string) int64 {
	if strings.TrimSpace(text) == "" {
		return 0
	}
	ids, _, err := e.codec.Encode(text)
	if err != nil {
		return int64(math.Ceil(float64(len([]rune(text))) / 4))
	}
	return int64(len(ids))
}

func collectText(value any, builder *strings.Builder) {
	switch typed := value.(type) {
	case string:
		builder.WriteString(typed)
		builder.WriteByte('\n')
	case []any:
		for _, item := range typed {
			collectText(item, builder)
		}
	case map[string]any:
		for key, item := range typed {
			if key == "id" || key == "model" || key == "object" || key == "type" {
				continue
			}
			collectText(item, builder)
		}
	}
}

func ParseUsage(data []byte) (Usage, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return Usage{}, false
	}
	usageMap := findUsageMap(value)
	if usageMap == nil {
		return Usage{}, false
	}
	usage := Usage{
		InputTokens:  firstInt(usageMap, "input_tokens", "prompt_tokens"),
		OutputTokens: firstInt(usageMap, "output_tokens", "completion_tokens"),
		Source:       "upstream",
	}
	for _, detailKey := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if details, ok := usageMap[detailKey].(map[string]any); ok {
			usage.CachedTokens = firstInt(details, "cached_tokens", "cache_read_tokens", "cache_read_input_tokens")
			usage.CacheWriteTokens = firstInt(details, "cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens")
			break
		}
	}
	cacheReadInputTokens := firstInt(usageMap, "cache_read_input_tokens")
	cacheWriteInputTokens := firstInt(usageMap, "cache_creation_input_tokens", "cache_write_input_tokens")
	usage.CachedTokens = max(usage.CachedTokens, cacheReadInputTokens)
	usage.CacheWriteTokens = max(usage.CacheWriteTokens, cacheWriteInputTokens)
	if _, hasPromptTokens := usageMap["prompt_tokens"]; !hasPromptTokens {
		usage.InputTokens += max(cacheReadInputTokens, 0) + max(cacheWriteInputTokens, 0)
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CachedTokens == 0 && usage.CacheWriteTokens == 0 {
		return Usage{}, false
	}
	return usage, true
}

func ParseUpstreamCostMicros(data []byte) (int64, bool) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	root, ok := value.(map[string]any)
	if !ok {
		return 0, false
	}
	if usage := findUsageMap(root); usage != nil {
		if micros, ok := firstCostMicros(usage, "cost", "total_cost"); ok {
			return micros, true
		}
	}
	if micros, ok := firstCostMicros(root, "cost"); ok {
		return micros, true
	}
	if response, ok := root["response"].(map[string]any); ok {
		return firstCostMicros(response, "cost")
	}
	return 0, false
}

func firstCostMicros(values map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		var raw string
		switch typed := value.(type) {
		case json.Number:
			raw = typed.String()
		case string:
			raw = strings.TrimSpace(typed)
		default:
			continue
		}
		amount, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 || amount > float64(math.MaxInt64)/1_000_000 {
			continue
		}
		return int64(math.Round(amount * 1_000_000)), true
	}
	return 0, false
}

func findUsageMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		if usage, ok := typed["usage"].(map[string]any); ok {
			return usage
		}
		for _, nested := range typed {
			if found := findUsageMap(nested); found != nil {
				return found
			}
		}
	case []any:
		for _, nested := range typed {
			if found := findUsageMap(nested); found != nil {
				return found
			}
		}
	}
	return nil
}

func firstInt(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case json.Number:
			parsed, _ := value.Int64()
			return parsed
		case float64:
			return int64(value)
		case int64:
			return value
		case int:
			return int64(value)
		}
	}
	return 0
}

func ResponseID(data []byte) string {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return ""
	}
	if id, ok := value["id"].(string); ok {
		return id
	}
	if response, ok := value["response"].(map[string]any); ok {
		if id, ok := response["id"].(string); ok {
			return id
		}
	}
	return ""
}

func CalculateCostMicros(mapping ChannelModel, usage Usage) int64 {
	cachedInput := usage.CachedTokens
	if cachedInput < 0 {
		cachedInput = 0
	}
	if cachedInput > usage.InputTokens {
		cachedInput = max(usage.InputTokens, 0)
	}
	cacheWrite := usage.CacheWriteTokens
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	if cacheWrite > usage.InputTokens-cachedInput {
		cacheWrite = max(usage.InputTokens-cachedInput, 0)
	}
	normalInput := usage.InputTokens - cachedInput - cacheWrite
	cachedInputPrice := mapping.InputPriceMicros
	if mapping.CachedInputPriceMicros != nil {
		cachedInputPrice = *mapping.CachedInputPriceMicros
	}
	cacheWritePrice := mapping.InputPriceMicros
	if mapping.CacheWritePriceMicros != nil {
		cacheWritePrice = *mapping.CacheWritePriceMicros
	}
	return tokenPrice(normalInput, mapping.InputPriceMicros) +
		tokenPrice(cachedInput, cachedInputPrice) +
		tokenPrice(cacheWrite, cacheWritePrice) +
		tokenPrice(usage.OutputTokens, mapping.OutputPriceMicros)
}

func normalInputTokens(usage Usage) int64 {
	cachedInput := min(max(usage.CachedTokens, 0), max(usage.InputTokens, 0))
	cacheWrite := min(max(usage.CacheWriteTokens, 0), max(usage.InputTokens-cachedInput, 0))
	return max(usage.InputTokens-cachedInput-cacheWrite, 0)
}

func tokenPrice(tokens int64, priceMicrosPerMillion int64) int64 {
	if tokens <= 0 || priceMicrosPerMillion <= 0 {
		return 0
	}
	return (tokens*priceMicrosPerMillion + 999999) / 1000000
}
