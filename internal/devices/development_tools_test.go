package devices

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseDevelopmentTools(t *testing.T) {
	valid := DevelopmentTool{Name: "node", Version: "v22.0.0", Path: "/工具 bin/node", Status: "AVAILABLE", CheckedAt: time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC)}
	for _, tc := range []struct {
		name   string
		change func(*DevelopmentTool)
		want   int
	}{
		{"valid", func(*DevelopmentTool) {}, 1},
		{"missing", func(v *DevelopmentTool) { v.Status = "MISSING"; v.Path = ""; v.Version = "" }, 1},
		{"timeout", func(v *DevelopmentTool) { v.Status = "TIMEOUT"; v.Version = "" }, 1},
		{"error", func(v *DevelopmentTool) { v.Status = "ERROR"; v.Version = "" }, 1},
		{"unknown_name", func(v *DevelopmentTool) { v.Name = "shell" }, 0},
		{"unknown_status", func(v *DevelopmentTool) { v.Status = "OK" }, 0},
		{"no_time", func(v *DevelopmentTool) { v.CheckedAt = time.Time{} }, 0},
		{"no_path", func(v *DevelopmentTool) { v.Path = "" }, 0},
		{"long_version", func(v *DevelopmentTool) { v.Version = strings.Repeat("v", 257) }, 0},
		{"long_path", func(v *DevelopmentTool) { v.Path = strings.Repeat("p", 4097) }, 0},
		{"control", func(v *DevelopmentTool) { v.Version = "v22\x1b" }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid
			tc.change(&v)
			raw, _ := json.Marshal(map[string]any{"development_tools": []any{v, v}})
			got := ParseDevelopmentTools(raw)
			if len(got) != tc.want {
				t.Fatalf("got=%+v", got)
			}
		})
	}
	for _, raw := range []string{`{}`, `null`, `{"development_tools":null}`, `{"development_tools":{}}`, `{"development_tools":[1,{"name":null}]}`} {
		if got := ParseDevelopmentTools([]byte(raw)); got == nil || len(got) != 0 {
			t.Fatalf("%s=%+v", raw, got)
		}
	}
}

func TestDevelopmentToolsJSONCompatibility(t *testing.T) {
	raw, err := json.Marshal(Device{})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(raw, &doc)
	if string(doc["development_tools"]) != "[]" {
		t.Fatalf("legacy tools=%s", doc["development_tools"])
	}
}
