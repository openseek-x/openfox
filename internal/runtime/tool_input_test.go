package runtime

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolInputPreviewRedactsBeforeEncoding(t *testing.T) {
	secret := "secret\"with\\escapes\n"
	input := map[string]any{"query": "find " + secret, "Authorization": "Bearer unknown",
		"nested": []any{map[string]any{"api_key": "new-key", "passWord": "pass", "count": 12}}, "secret-key": "value"}
	preview, truncated := ToolInputPreview(input, func(value string) string { return strings.ReplaceAll(value, secret, "[REDACTED]") })
	if truncated || !json.Valid([]byte(preview)) || !strings.Contains(preview, "\n  ") {
		t.Fatalf("preview is not formatted JSON: %q truncated=%v", preview, truncated)
	}
	for _, forbidden := range []string{"Bearer", "new-key", `"pass"`, "with", `"value"`} {
		if strings.Contains(preview, forbidden) {
			t.Fatalf("preview leaked sensitive field %q", forbidden)
		}
	}
	if !strings.Contains(preview, `"count": 12`) || strings.Count(preview, "[REDACTED]") != 5 {
		t.Fatalf("redaction changed ordinary values: %s", preview)
	}
	if input["Authorization"] != "Bearer unknown" {
		t.Fatal("preview mutated original input")
	}
}

func TestToolInputPreviewBoundsAndMissing(t *testing.T) {
	for _, input := range []any{nil, json.RawMessage(nil), map[string]any(nil)} {
		if text, cut := ToolInputPreview(input, nil); text != "" || cut {
			t.Fatalf("missing input: %q %v", text, cut)
		}
	}
	for _, input := range []any{json.RawMessage(`{"bad":`), json.RawMessage(strings.Repeat(" ", toolInputParseLimit+1)),
		json.RawMessage(strings.Repeat("[", 34) + "1" + strings.Repeat("]", 34))} {
		if text, cut := ToolInputPreview(input, nil); text != "" || !cut {
			t.Fatalf("invalid/over-budget input: %q %v", text, cut)
		}
	}
	text, cut := ToolInputPreview(map[string]any{"query": strings.Repeat("界", 5000)}, nil)
	if !cut || len(text) > toolInputLimit || !utf8.ValidString(text) {
		t.Fatalf("invalid UTF-8/length: %d %v", len(text), cut)
	}
	text, cut = ToolInputPreview(json.RawMessage(`{"id":9007199254740993}`), nil)
	if cut || !strings.Contains(text, "9007199254740993") {
		t.Fatalf("integer lost precision: %q", text)
	}
	text, cut = ToolInputPreview(json.RawMessage(`{}`), nil)
	if cut || text != "{}" {
		t.Fatalf("empty object: %q %v", text, cut)
	}
}
