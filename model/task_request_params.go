package model

import (
	"encoding/json"
	"strings"
)

const maxTaskRequestParamsBytes = 16 * 1024

// BuildTaskRequestParams keeps a bounded, credential-free JSON request snapshot for task logs.
func BuildTaskRequestParams(body []byte, contentType string) json.RawMessage {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "application/json") {
		return nil
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return nil
	}
	value = sanitizeTaskRequestValue(value)
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maxTaskRequestParamsBytes {
		return nil
	}
	return json.RawMessage(encoded)
}

func sanitizeTaskRequestValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, child := range v {
			if isSensitiveTaskRequestKey(strings.ToLower(strings.TrimSpace(key))) {
				continue
			}
			result[key] = sanitizeTaskRequestValue(child)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, child := range v {
			result[i] = sanitizeTaskRequestValue(child)
		}
		return result
	case string:
		if len(v) > 4096 {
			return v[:4096] + "…"
		}
		return v
	default:
		return value
	}
}

func isSensitiveTaskRequestKey(key string) bool {
	for _, token := range []string{"key", "token", "password", "secret", "authorization", "cookie", "credential", "apikey", "api_key", "header"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}
