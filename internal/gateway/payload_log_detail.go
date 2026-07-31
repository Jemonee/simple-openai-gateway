package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Jemonee/simple-openai-gateway/internal/config"
)

const (
	maxSummaryPayloadBytes = 64 << 10
	maxSummaryStringRunes  = 160
	maxSummaryArrayItems   = 8
	maxSummaryObjectFields = 48
	maxSummaryDepth        = 6
)

type payloadRetentionMetadata struct {
	Version       int    `json:"version"`
	Detail        string `json:"detail"`
	OriginalBytes int    `json:"originalBytes"`
}

func retainLoggedPayload(detail string, data []byte, alreadyTruncated bool) (string, bool) {
	switch detail {
	case config.PayloadLogDetailNone:
		return "", false
	case config.PayloadLogDetailSummary:
		summary, reduced := summarizeLoggedPayload(data)
		return storedPayloadWithLimit(summary, alreadyTruncated || reduced, maxSummaryPayloadBytes)
	default:
		return storedPayload(data, alreadyTruncated)
	}
}

func summarizeLoggedPayload(data []byte) ([]byte, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, false
	}
	if value, ok := decodeSummaryJSON(trimmed); ok {
		summary, reduced := summarizePayloadValue(value, 0)
		return encodePayloadSummary(summary, len(data)), reduced
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) || bytes.HasPrefix(trimmed, []byte("event:")) {
		return summarizeSSEPayload(trimmed, len(data)), true
	}
	return summarizeTextPayload(trimmed, len(data)), len(trimmed) > maxSummaryStringRunes
}

func decodeSummaryJSON(data []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return value, true
}

func summarizePayloadValue(value any, depth int) (any, bool) {
	if depth >= maxSummaryDepth {
		switch typed := value.(type) {
		case map[string]any:
			return map[string]any{"_type": "object", "fieldCount": len(typed)}, len(typed) > 0
		case []any:
			return map[string]any{"_type": "array", "itemCount": len(typed)}, len(typed) > 0
		case string:
			return summarizePayloadString(typed)
		default:
			return value, false
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		reduced := false
		if len(keys) > maxSummaryObjectFields {
			keys = keys[:maxSummaryObjectFields]
			reduced = true
		}
		result := make(map[string]any, len(keys)+1)
		for _, key := range keys {
			summarized, itemReduced := summarizePayloadValue(typed[key], depth+1)
			result[key] = summarized
			reduced = reduced || itemReduced
		}
		if len(typed) > len(keys) {
			result["_gatewayLogOmittedFields"] = len(typed) - len(keys)
		}
		return result, reduced
	case []any:
		indexes := make([]int, 0, min(len(typed), maxSummaryArrayItems))
		if len(typed) <= maxSummaryArrayItems {
			for index := range typed {
				indexes = append(indexes, index)
			}
		} else {
			for index := 0; index < maxSummaryArrayItems/2; index++ {
				indexes = append(indexes, index)
			}
			for index := len(typed) - maxSummaryArrayItems/2; index < len(typed); index++ {
				indexes = append(indexes, index)
			}
		}
		result := make([]any, 0, len(indexes)+1)
		reduced := len(typed) > len(indexes)
		for position, index := range indexes {
			if reduced && position == maxSummaryArrayItems/2 {
				result = append(result, map[string]any{"_gatewayLogOmittedItems": len(typed) - len(indexes)})
			}
			summarized, itemReduced := summarizePayloadValue(typed[index], depth+1)
			result = append(result, summarized)
			reduced = reduced || itemReduced
		}
		return result, reduced
	case string:
		return summarizePayloadString(typed)
	default:
		return value, false
	}
}

func summarizePayloadString(value string) (any, bool) {
	runes := []rune(value)
	if len(runes) <= maxSummaryStringRunes {
		return value, false
	}
	omitted := len(runes) - maxSummaryStringRunes
	return string(runes[:maxSummaryStringRunes]) + "... [omitted " + strconv.Itoa(omitted) + " chars]", true
}

func encodePayloadSummary(summary any, originalBytes int) []byte {
	metadata := payloadRetentionMetadata{Version: 1, Detail: config.PayloadLogDetailSummary, OriginalBytes: originalBytes}
	var envelope any
	if object, ok := summary.(map[string]any); ok {
		object["_gatewayLogRetention"] = metadata
		envelope = object
	} else {
		envelope = map[string]any{"_gatewayLogRetention": metadata, "payload": summary}
	}
	encoded, err := json.Marshal(envelope)
	if err == nil && len(encoded) <= maxSummaryPayloadBytes {
		return encoded
	}
	fallback, _ := json.Marshal(map[string]any{
		"_gatewayLogRetention": metadata,
		"summary":              "payload structure exceeded the summary retention limit",
	})
	return fallback
}

func summarizeSSEPayload(data []byte, originalBytes int) []byte {
	first := make([]any, 0, maxSummaryArrayItems/2)
	last := make([]any, 0, maxSummaryArrayItems/2)
	eventCount := 0
	eventName := ""
	for _, rawLine := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(rawLine)
		if bytes.HasPrefix(line, []byte("event:")) {
			eventName = strings.TrimSpace(string(bytes.TrimPrefix(line, []byte("event:"))))
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		rawData := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		entry := map[string]any{"event": eventName}
		if bytes.Equal(rawData, []byte("[DONE]")) {
			entry["data"] = "[DONE]"
		} else if value, ok := decodeSummaryJSON(rawData); ok {
			entry["data"], _ = summarizePayloadValue(value, 0)
		} else {
			entry["data"] = summarizeUTF8Text(rawData, maxSummaryStringRunes)
		}
		eventCount++
		if len(first) < maxSummaryArrayItems/2 {
			first = append(first, entry)
		} else {
			if len(last) == maxSummaryArrayItems/2 {
				copy(last, last[1:])
				last = last[:len(last)-1]
			}
			last = append(last, entry)
		}
		eventName = ""
	}
	events := append(first, last...)
	return encodePayloadSummary(map[string]any{
		"format":        "sse",
		"eventCount":    eventCount,
		"omittedEvents": max(eventCount-len(events), 0),
		"events":        events,
	}, originalBytes)
}

func summarizeTextPayload(data []byte, originalBytes int) []byte {
	return encodePayloadSummary(map[string]any{
		"format":  "text",
		"preview": summarizeUTF8Text(data, maxSummaryStringRunes),
	}, originalBytes)
}

func summarizeUTF8Text(data []byte, maxRunes int) string {
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	value := string(data)
	summary, _ := summarizePayloadStringWithLimit(value, maxRunes)
	return summary
}

func summarizePayloadStringWithLimit(value string, maxRunes int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	omitted := len(runes) - maxRunes
	return string(runes[:maxRunes]) + "... [omitted " + strconv.Itoa(omitted) + " chars]", true
}
