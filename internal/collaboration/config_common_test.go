package collaboration

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func ruleCode(err error) string {
	var rule *RuleError
	if errors.As(err, &rule) {
		return rule.Code
	}
	return ""
}

func TestOptionalStringAndRaw(t *testing.T) {
	var input struct {
		Group   OptionalString `json:"group_id"`
		Default OptionalRaw    `json:"default_value"`
	}
	if err := json.Unmarshal([]byte(`{}`), &input); err != nil || input.Group.Set || input.Default.Set {
		t.Fatalf("missing fields must be unset: %+v %v", input, err)
	}
	if err := json.Unmarshal([]byte(`{"group_id":null,"default_value":null}`), &input); err != nil {
		t.Fatal(err)
	}
	if !input.Group.Set || input.Group.Value != nil || !input.Default.Set || input.Default.Value != nil {
		t.Fatalf("explicit null must be set with nil value: %+v", input)
	}
	if err := json.Unmarshal([]byte(`{"group_id":"abc","default_value":{"a":1}}`), &input); err != nil {
		t.Fatal(err)
	}
	if input.Group.Value == nil || *input.Group.Value != "abc" || string(input.Default.Value) != `{"a":1}` {
		t.Fatalf("values not captured: %+v", input)
	}
	if err := json.Unmarshal([]byte(`{"group_id":12}`), &input); err == nil {
		t.Fatal("non-string group_id must fail")
	}
}

func TestNormalizeConfigName(t *testing.T) {
	if got, err := normalizeConfigName("  标签  ", 50); err != nil || got != "标签" {
		t.Fatalf("trimmed name = %q %v", got, err)
	}
	for _, bad := range []string{"", "   ", strings.Repeat("字", 51), "a\nb"} {
		if _, err := normalizeConfigName(bad, 50); ruleCode(err) != "INVALID_REQUEST" {
			t.Fatalf("normalizeConfigName(%q) err = %v", bad, err)
		}
	}
	if _, err := normalizeConfigName(strings.Repeat("字", 50), 50); err != nil {
		t.Fatalf("50 runes must be accepted: %v", err)
	}
}

func TestUniqueUUIDs(t *testing.T) {
	a, b := "00000000-0000-0000-0000-0000000000AA", "00000000-0000-0000-0000-0000000000bb"
	got, err := uniqueUUIDs([]string{a, b, strings.ToLower(a)}, 3)
	if err != nil || len(got) != 2 || got[0] != strings.ToLower(a) || got[1] != b {
		t.Fatalf("uniqueUUIDs = %v %v", got, err)
	}
	if _, err := uniqueUUIDs([]string{a, b}, 1); err == nil {
		t.Fatal("too many ids must fail")
	}
	if _, err := uniqueUUIDs([]string{"nope"}, 5); err == nil {
		t.Fatal("invalid uuid must fail")
	}
}

func TestClassifyRelationTypeID(t *testing.T) {
	if builtin, err := classifyRelationTypeID("builtin:RELATES"); err != nil || !builtin {
		t.Fatalf("builtin id = %v %v", builtin, err)
	}
	if builtin, err := classifyRelationTypeID("00000000-0000-0000-0000-000000000001"); err != nil || builtin {
		t.Fatalf("custom id = %v %v", builtin, err)
	}
	for _, bad := range []string{"builtin:NOPE", "builtin:blocks", "x"} {
		var rule *RuleError
		if _, err := classifyRelationTypeID(bad); !errors.As(err, &rule) || rule.Status != http.StatusNotFound {
			t.Fatalf("classifyRelationTypeID(%q) err = %v", bad, err)
		}
	}
	items := builtinRelationTypes()
	if len(items) != 4 || items[0].ID != "builtin:BLOCKS" || *items[3].Key != "RELATES" || !items[3].Symmetric || items[0].Symmetric {
		t.Fatalf("builtin relation types = %+v", items)
	}
	// 每个内置项持有自己的 key 指针。
	if items[0].Key == items[1].Key {
		t.Fatal("builtin keys share a pointer")
	}
}

func TestCategoryOrder(t *testing.T) {
	rows := []statusRow{
		{ID: "c", Category: "TODO", Position: 1},
		{ID: "a", Category: "TODO", Position: 1},
		{ID: "b", Category: "TODO", Position: 0},
		{ID: "x", Category: "DONE", Position: 0},
	}
	list := categoryOrder(rows, "TODO", "c")
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("categoryOrder = %+v", list)
	}
	if active := activeRows([]statusRow{{ID: "a"}, {ID: "b", Archived: true}}); len(active) != 1 || active[0].ID != "a" {
		t.Fatalf("activeRows = %+v", active)
	}
}

func TestNormalizeFieldValue(t *testing.T) {
	option := "00000000-0000-0000-0000-00000000000a"
	other := "00000000-0000-0000-0000-00000000000b"
	member := "00000000-0000-0000-0000-000000000001"
	options := map[string]bool{option: true, other: true}
	cases := []struct {
		kind, scope, raw, want string
	}{
		{"TEXT", "", `"hello"`, `"hello"`},
		{"RICH_TEXT", "", `"` + strings.Repeat("x", 300) + `"`, `"` + strings.Repeat("x", 300) + `"`},
		{"NUMBER", "", ` -1.50 `, `-1.50`},
		{"NUMBER", "", `1e3`, `1e3`},
		{"BOOLEAN", "", `false`, `false`},
		{"DATE", "", `"2026-02-28"`, `"2026-02-28"`},
		{"URL", "", `"https://example.test/a?b=1"`, `"https://example.test/a?b=1"`},
		{"SELECT", "", `"` + strings.ToUpper(option) + `"`, `"` + option + `"`},
		{"MULTI_SELECT", "", `["` + other + `","` + option + `"]`, `["` + other + `","` + option + `"]`},
		{"MEMBER", "ALL", `{"kind":"agent","id":"` + member + `"}`, `{"kind":"AGENT","id":"` + member + `"}`},
		{"TEXT", "", `null`, ``},
		{"MULTI_SELECT", "", `[]`, ``},
	}
	for _, item := range cases {
		got, _, err := NormalizeFieldValue(item.kind, item.scope, json.RawMessage(item.raw), options)
		if err != nil || string(got) != item.want {
			t.Fatalf("%s %s = %s %v, want %s", item.kind, item.raw, got, err, item.want)
		}
	}
	invalid := []struct{ kind, scope, raw string }{
		{"TEXT", "", `12`},
		{"TEXT", "", `"` + strings.Repeat("x", 257) + `"`},
		{"RICH_TEXT", "", `"` + strings.Repeat("x", 65537) + `"`},
		{"NUMBER", "", `"1"`},
		{"NUMBER", "", `1e999`},
		{"NUMBER", "", `NaN`},
		{"BOOLEAN", "", `"true"`},
		{"DATE", "", `"2026-02-30"`},
		{"DATE", "", `"2026-2-3"`},
		{"URL", "", `"ftp://example.test"`},
		{"URL", "", `"https://"`},
		{"URL", "", `"https://example.test/` + strings.Repeat("a", 2048) + `"`},
		{"SELECT", "", `"00000000-0000-0000-0000-0000000000ff"`},
		{"MULTI_SELECT", "", `["` + option + `","` + option + `"]`},
		{"MULTI_SELECT", "", `"` + option + `"`},
		{"MEMBER", "HUMANS", `{"kind":"AGENT","id":"` + member + `"}`},
		{"MEMBER", "AGENTS", `{"kind":"HUMAN","id":"` + member + `"}`},
		{"MEMBER", "ALL", `{"kind":"CREW","id":"` + member + `"}`},
		{"MEMBER", "ALL", `{"kind":"HUMAN","id":"` + member + `","extra":1}`},
		{"TEXT", "", `"a" "b"`},
		{"COLOR", "", `"red"`},
	}
	for _, item := range invalid {
		if _, _, err := NormalizeFieldValue(item.kind, item.scope, json.RawMessage(item.raw), options); ruleCode(err) != "INVALID_REQUEST" {
			t.Fatalf("%s %.40s must be rejected, err = %v", item.kind, item.raw, err)
		}
	}
	_, ref, err := NormalizeFieldValue("MEMBER", "ALL", json.RawMessage(`{"kind":"human","id":"`+member+`"}`), nil)
	if err != nil || ref == nil || ref.Kind != "HUMAN" || ref.ID != member {
		t.Fatalf("member ref = %+v %v", ref, err)
	}
}

func TestResolveCreateDefault(t *testing.T) {
	ids := []string{"00000000-0000-0000-0000-00000000000a", "00000000-0000-0000-0000-00000000000b"}
	if got, _, err := resolveCreateDefault("SELECT", "", json.RawMessage(`{"option_index":1}`), ids); err != nil || string(got) != `"`+ids[1]+`"` {
		t.Fatalf("select default = %s %v", got, err)
	}
	if got, _, err := resolveCreateDefault("MULTI_SELECT", "", json.RawMessage(`{"option_indexes":[1,0]}`), ids); err != nil ||
		string(got) != `["`+ids[1]+`","`+ids[0]+`"]` {
		t.Fatalf("multi select default = %s %v", got, err)
	}
	if got, _, err := resolveCreateDefault("NUMBER", "", json.RawMessage(`7`), nil); err != nil || string(got) != `7` {
		t.Fatalf("number default = %s %v", got, err)
	}
	if got, _, err := resolveCreateDefault("SELECT", "", nil, ids); err != nil || got != nil {
		t.Fatalf("empty default = %s %v", got, err)
	}
	for _, bad := range []struct{ kind, raw string }{
		{"SELECT", `{"option_index":2}`},
		{"SELECT", `{"option_index":-1}`},
		{"SELECT", `{}`},
		{"SELECT", `"` + ids[0] + `"`},
		{"MULTI_SELECT", `{"option_indexes":[0,0]}`},
		{"MULTI_SELECT", `{"option_indexes":[3]}`},
	} {
		if _, _, err := resolveCreateDefault(bad.kind, "", json.RawMessage(bad.raw), ids); ruleCode(err) != "INVALID_REQUEST" {
			t.Fatalf("%s %s must be rejected, err = %v", bad.kind, bad.raw, err)
		}
	}
}
