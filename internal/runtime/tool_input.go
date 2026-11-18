package runtime

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const toolInputLimit = 8 * 1024
const toolInputParseLimit = 64 * 1024

// ToolInputPreview returns display-only JSON, never executable arguments. Missing
// input stays absent. Malformed/oversized input is omitted and marked truncated.
// Sensitive fields and configured secret values are removed before JSON escaping.
func ToolInputPreview(input any, redact func(string) string) (string, bool) {
	if input == nil {
		return "", false
	}
	var raw []byte
	if message, ok := input.(json.RawMessage); ok {
		if len(message) == 0 {
			return "", false
		}
		raw = message
	} else {
		var err error
		raw, err = json.Marshal(input)
		if err != nil {
			return "", true
		}
		// A typed nil map is also missing input.
		if bytes.Equal(raw, []byte("null")) {
			return "", false
		}
	}
	if len(raw) > toolInputParseLimit || !json.Valid(raw) {
		return "", true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", true
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	var clean func(any, int) (any, bool)
	clean = func(value any, depth int) (any, bool) {
		if depth > 32 {
			return nil, false
		}
		switch v := value.(type) {
		case map[string]any:
			result := make(map[string]any, len(v))
			for key, item := range v {
				if sensitiveToolInputKey(key) {
					result[redact(key)] = "[REDACTED]"
					continue
				}
				filtered, ok := clean(item, depth+1)
				if !ok {
					return nil, false
				}
				result[redact(key)] = filtered
			}
			return result, true
		case []any:
			for index, item := range v {
				filtered, ok := clean(item, depth+1)
				if !ok {
					return nil, false
				}
				v[index] = filtered
			}
			return v, true
		case string:
			return redact(v), true
		default:
			return value, true
		}
	}
	cleaned, ok := clean(value, 0)
	if !ok {
		return "", true
	}
	formatted, err := json.MarshalIndent(cleaned, "", "  ")
	if err != nil {
		return "", true
	}
	if len(formatted) <= toolInputLimit {
		return string(formatted), false
	}
	end := toolInputLimit
	for end > 0 && !utf8.RuneStart(formatted[end]) {
		end--
	}
	return string(formatted[:end]), true
}

func sensitiveToolInputKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key))
	for _, part := range []string{"password", "passwd", "secret", "token", "apikey", "authorization", "credential", "privatekey", "cookie"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}
