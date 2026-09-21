package security

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const RedactedValue = "[REDACTED]"

var sensitiveAssignmentPattern = regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|x-api-key|api_key|apikey|access_token|refresh_token|token|secret|password|encrypted_secret|provider_secret|RELAY_ADMIN_TOKEN|RELAY_API_TOKEN|RELAY_MASTER_KEY)\b\s*[:=]\s*[^,\s;}]+`)

func RedactString(value string) string {
	if value == "" {
		return value
	}
	return sensitiveAssignmentPattern.ReplaceAllStringFunc(value, func(match string) string {
		separator := strings.IndexAny(match, ":=")
		if separator < 0 {
			return RedactedValue
		}
		return match[:separator+1] + RedactedValue
	})
}

func RedactJSONBytes(value []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, false
	}
	redacted := redactJSONValue("", payload)
	out, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return nil, false
	}
	return out, true
}

func redactJSONValue(key string, value any) any {
	if SensitiveKey(key) {
		return RedactedValue
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = redactJSONValue(childKey, childValue)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, childValue := range typed {
			out[i] = redactJSONValue("", childValue)
		}
		return out
	default:
		return value
	}
}

func SensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	switch normalized {
	case "authorization",
		"proxy_authorization",
		"x_api_key",
		"api_key",
		"apikey",
		"access_token",
		"refresh_token",
		"token",
		"secret",
		"password",
		"encrypted_secret",
		"provider_secret",
		"relay_admin_token",
		"relay_master_key":
		return true
	default:
		return strings.HasSuffix(normalized, "_token") ||
			strings.HasSuffix(normalized, "_secret") ||
			strings.HasSuffix(normalized, "_password") ||
			strings.Contains(normalized, "api_key")
	}
}
