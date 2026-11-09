package devices

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCommandParams(t *testing.T) {
	valid := map[string]struct {
		kind CommandKind
		raw  string
		want string
	}{
		"probe empty":      {CommandRuntimeProbe, ``, `{}`},
		"probe null":       {CommandRuntimeProbe, `null`, `{}`},
		"cleanup object":   {CommandStorageCleanup, ` {} `, `{}`},
		"test default":     {CommandRuntimeTest, `{}`, `{"provider":"codex"}`},
		"test with model":  {CommandRuntimeTest, `{"provider":"codex","model":" gpt-5 "}`, `{"provider":"codex","model":"gpt-5"}`},
		"test empty model": {CommandRuntimeTest, `{"model":""}`, `{"provider":"codex"}`},
	}
	for name, item := range valid {
		got, err := NormalizeCommandParams(item.kind, json.RawMessage(item.raw))
		if err != nil || string(got) != item.want {
			t.Fatalf("%s: %s, %v", name, got, err)
		}
	}
	invalid := map[string]struct {
		kind CommandKind
		raw  string
	}{
		"unknown kind":     {"shell", `{}`},
		"probe with key":   {CommandRuntimeProbe, `{"x":1}`},
		"array":            {CommandRuntimeProbe, `[]`},
		"other provider":   {CommandRuntimeTest, `{"provider":"claude"}`},
		"model not string": {CommandRuntimeTest, `{"model":1}`},
		"unknown test key": {CommandRuntimeTest, `{"sandbox":"off"}`},
		"model too long":   {CommandRuntimeTest, `{"model":"` + strings.Repeat("m", 129) + `"}`},
		"model control":    {CommandRuntimeTest, `{"model":"a\nb"}`},
		"too large":        {CommandRuntimeTest, `{"model":"` + strings.Repeat(" ", 3000) + `"}`},
	}
	for name, item := range invalid {
		if _, err := NormalizeCommandParams(item.kind, json.RawMessage(item.raw)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

func TestValidateCommandResult(t *testing.T) {
	ok := []CommandResultInput{
		{Status: CommandSucceeded, Result: json.RawMessage(`{"ok":true}`), ErrorCode: "IGNORED"},
		{Status: CommandFailed, ErrorCode: "RUNTIME_UNAVAILABLE"},
		{Status: CommandFailed, Result: json.RawMessage(`null`), ErrorCode: "X"},
	}
	for _, input := range ok {
		normalized, err := validateCommandResult(input)
		if err != nil {
			t.Fatalf("%+v: %v", input, err)
		}
		if input.Status == CommandSucceeded && normalized.ErrorCode != "" {
			t.Fatal("succeeded result keeps error code")
		}
	}
	bad := []CommandResultInput{
		{Status: CommandSucceeded},
		{Status: CommandSucceeded, Result: json.RawMessage(`[1]`)},
		{Status: CommandSucceeded, Result: json.RawMessage(`{"a":"` + strings.Repeat("x", maxCommandResultBytes) + `"}`)},
		{Status: CommandFailed},
		{Status: CommandFailed, ErrorCode: "lower"},
		{Status: CommandExpired, ErrorCode: "X"},
		{Status: CommandDelivered, Result: json.RawMessage(`{}`)},
	}
	for _, input := range bad {
		if _, err := validateCommandResult(input); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%+v: err = %v", input.Status, err)
		}
	}
}

func TestCommandMarshalNulls(t *testing.T) {
	encoded, err := json.Marshal(Command{ID: "c", DeviceID: "d", Kind: CommandRuntimeProbe, Status: CommandPending,
		CreatedAt: time.Unix(0, 0).UTC(), ExpiresAt: time.Unix(120, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"params": "{}", "result": "null", "delivered_at": "null", "completed_at": "null", "error_code": `""`} {
		if string(document[key]) != want {
			t.Fatalf("%s = %s, want %s", key, document[key], want)
		}
	}
}

func TestParseStorageAndRuntimesCheckedAt(t *testing.T) {
	storage := ParseStorage(json.RawMessage(`{"storage":{"total_bytes":10,"worktree_bytes":4,"worktree_count":1,
		"codex_home_bytes":3,"journal_bytes":3,"measured_at":"2026-10-29T09:00:00+08:00"}}`))
	if storage == nil || storage.TotalBytes != 10 || storage.Partial || storage.MeasuredAt.Location() != time.UTC ||
		!storage.MeasuredAt.Equal(time.Date(2026, 10, 29, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("storage = %+v", storage)
	}
	for _, raw := range []string{
		``, `{}`, `{"storage":null}`, `{"storage":"x"}`,
		`{"storage":{"total_bytes":-1,"worktree_bytes":0,"worktree_count":0,"codex_home_bytes":0,"journal_bytes":0,"measured_at":"2026-10-29T09:00:00Z"}}`,
		`{"storage":{"total_bytes":1,"worktree_bytes":0,"worktree_count":0,"codex_home_bytes":0,"journal_bytes":0}}`,
		`{"storage":{"worktree_bytes":0,"worktree_count":0,"codex_home_bytes":0,"journal_bytes":0,"measured_at":"2026-10-29T09:00:00Z"}}`,
	} {
		if parsed := ParseStorage(json.RawMessage(raw)); parsed != nil {
			t.Fatalf("ParseStorage(%s) = %+v", raw, parsed)
		}
	}
	checked := parseRuntimesCheckedAt(json.RawMessage(`{"runtimes":{"a":{"checked_at":"2026-10-29T08:00:00Z"},
		"b":{"checked_at":"2026-10-29T09:00:00Z"},"c":{"runnable":true},"d":"bad"}}`))
	if checked == nil || !checked.Equal(time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("checked = %v", checked)
	}
	if parseRuntimesCheckedAt(json.RawMessage(`{"runtimes":{"a":{}}}`)) != nil {
		t.Fatal("missing checked_at should be nil")
	}
}

// skill_scan 的结果上限是 64 KB，其他种类仍是 16 KB（迁移 000042 的 device_commands_result_shape 按 kind 区分）。
func TestCommandResultLimitByKind(t *testing.T) {
	if CommandSkillScan.maxResultBytes() != 64*1024 {
		t.Fatalf("skill_scan limit = %d", CommandSkillScan.maxResultBytes())
	}
	for _, kind := range []CommandKind{CommandRuntimeProbe, CommandRuntimeTest, CommandStorageCleanup, CommandSkillImport} {
		if kind.maxResultBytes() != 16*1024 {
			t.Fatalf("%s limit = %d", kind, kind.maxResultBytes())
		}
	}
	large := CommandResultInput{Status: CommandSucceeded, Result: json.RawMessage(`{"a":"` + strings.Repeat("x", 63*1000) + `"}`)}
	if _, err := validateCommandResultLimit(large, maxSkillScanResultBytes); err != nil {
		t.Fatalf("63 KB should pass the skill_scan limit: %v", err)
	}
	if _, err := validateCommandResult(large); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("63 KB must fail the default limit: %v", err)
	}
}

func TestNormalizeSkillImportParams(t *testing.T) {
	space := "6f1f0c7e-3c1b-4c44-9d0b-1a2b3c4d5e6f"
	got, err := NormalizeCommandParams(CommandSkillImport, json.RawMessage(`{"names":["a","b.c_d-1"],"space_id":"`+space+`"}`))
	if err != nil || string(got) != `{"space_id":"`+space+`","names":["a","b.c_d-1"]}` {
		t.Fatalf("got %s, %v", got, err)
	}
	for label, raw := range map[string]string{
		"empty":          `{}`,
		"no names":       `{"space_id":"` + space + `"}`,
		"unknown key":    `{"space_id":"` + space + `","names":["a"],"x":1}`,
		"empty names":    `{"space_id":"` + space + `","names":[]}`,
		"bad space":      `{"space_id":"nope","names":["a"]}`,
		"traversal":      `{"space_id":"` + space + `","names":["../etc"]}`,
		"slash":          `{"space_id":"` + space + `","names":["a/b"]}`,
		"duplicate":      `{"space_id":"` + space + `","names":["a","a"]}`,
		"21 names":       `{"space_id":"` + space + `","names":["` + strings.Join(manyNames(21), `","`) + `"]}`,
		"name too long":  `{"space_id":"` + space + `","names":["` + strings.Repeat("a", 65) + `"]}`,
		"names not list": `{"space_id":"` + space + `","names":"a"}`,
	} {
		if _, err := NormalizeCommandParams(CommandSkillImport, json.RawMessage(raw)); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", label, err)
		}
	}
	if _, err := NormalizeCommandParams(CommandSkillImport, json.RawMessage(`{"space_id":"`+space+`","names":["`+strings.Join(manyNames(20), `","`)+`"]}`)); err != nil {
		t.Errorf("20 names should pass: %v", err)
	}
	if _, err := NormalizeCommandParams(CommandSkillScan, json.RawMessage(`{"x":1}`)); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("skill_scan takes no params: %v", err)
	}
	if !SameSkillImportParams(json.RawMessage(`{"space_id":"s","names":["a","b"]}`), json.RawMessage(`{"names":["a","b"],"space_id":"s"}`)) ||
		SameSkillImportParams(json.RawMessage(`{"space_id":"s","names":["a","b"]}`), json.RawMessage(`{"space_id":"s","names":["b","a"]}`)) {
		t.Error("SameSkillImportParams compares space and ordered names")
	}
}

func manyNames(n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = "n" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	return names
}
