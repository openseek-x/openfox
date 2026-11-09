package devices

import (
	"encoding/json"
	"strings"
	"testing"
)

func storageJSON(categories string) json.RawMessage {
	return json.RawMessage(`{"storage":{"total_bytes":1000,"worktree_bytes":600,"worktree_count":3,"codex_home_bytes":100,` +
		`"journal_bytes":50,"measured_at":"2026-11-01T10:00:00Z"` + categories + `}}`)
}

func TestParseStorageCategories(t *testing.T) {
	got := ParseStorage(storageJSON(`,"categories":[{"key":"cache","bytes":100,"count":0},{"key":"task","bytes":600,"count":2},{"key":"other","bytes":300}]`))
	if got == nil || len(got.Categories) != 3 || got.Categories[1] != (StorageCategory{Key: "task", Bytes: 600, Count: 2}) {
		t.Fatalf("storage = %+v", got)
	}
	// 没有 categories：数组为空（不是 nil），JSON 序列化为 []。
	none := ParseStorage(storageJSON(``))
	encoded, _ := json.Marshal(none)
	if none == nil || none.Categories == nil || len(none.Categories) != 0 || !strings.Contains(string(encoded), `"categories":[]`) {
		t.Fatalf("no categories = %+v %s", none, encoded)
	}
}

func TestParseStorageRejectsInvalidCategories(t *testing.T) {
	cases := map[string]string{
		"负数":       `,"categories":[{"key":"task","bytes":-1}]`,
		"缺 bytes":  `,"categories":[{"key":"task"}]`,
		"非法键":      `,"categories":[{"key":"Task/../x","bytes":1}]`,
		"键过长":      `,"categories":[{"key":"` + strings.Repeat("a", 33) + `","bytes":1}]`,
		"重复键":      `,"categories":[{"key":"task","bytes":1},{"key":"task","bytes":1}]`,
		"总和超限":     `,"categories":[{"key":"task","bytes":900},{"key":"other","bytes":101}]`,
		"溢出":       `,"categories":[{"key":"a","bytes":9223372036854775807},{"key":"b","bytes":9223372036854775807}]`,
		"负数 count": `,"categories":[{"key":"task","bytes":1,"count":-1}]`,
		"条数超限":     `,"categories":[{"key":"a","bytes":0},{"key":"b","bytes":0},{"key":"c","bytes":0},{"key":"d","bytes":0},{"key":"e","bytes":0},{"key":"f","bytes":0},{"key":"g","bytes":0},{"key":"h","bytes":0},{"key":"i","bytes":0}]`,
	}
	for name, categories := range cases {
		got := ParseStorage(storageJSON(categories))
		if got == nil || len(got.Categories) != 0 {
			t.Fatalf("%s: storage = %+v", name, got)
		}
		// 总量仍然可用。
		if got.TotalBytes != 1000 {
			t.Fatalf("%s: total = %d", name, got.TotalBytes)
		}
	}
}
