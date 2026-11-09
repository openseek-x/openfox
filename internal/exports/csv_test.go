package exports

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"openfox/internal/spacebundle"
)

func TestSanitizeCell(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"hello", "hello"}, {"=1+1", "'=1+1"}, {"+SUM(A1)", "'+SUM(A1)"}, {"-2", "'-2"}, {"@cmd", "'@cmd"},
		{"\tx", "'\tx"}, {"\rx", "'\rx"}, {"a=b", "a=b"}, {"2026-01-01", "2026-01-01"},
	} {
		if got := SanitizeCell(tc.in); got != tc.want {
			t.Errorf("SanitizeCell(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// buildBundle 写一个最小的快照包。
func buildBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := spacebundle.NewWriter(&buf)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.Write(spacebundle.KindHeader, spacebundle.Header{Version: spacebundle.Version, Space: spacebundle.Space{Name: "Alpha", Prefix: "ALP"}}))
	must(w.Write(spacebundle.KindStatus, spacebundle.Status{ID: "st1", Name: "进行中", Category: "IN_PROGRESS"}))
	must(w.Write(spacebundle.KindType, spacebundle.Type{ID: "ty1", Name: "缺陷"}))
	must(w.Write(spacebundle.KindLabel, spacebundle.Label{ID: "l1", Name: "bug"}))
	must(w.Write(spacebundle.KindLabel, spacebundle.Label{ID: "l2", Name: "ui"}))
	must(w.Write(spacebundle.KindField, spacebundle.Field{ID: "f1", Name: "Severity", Kind: "SELECT",
		Options: []spacebundle.FieldOption{{ID: "o1", Label: "High"}}}))
	must(w.Write(spacebundle.KindField, spacebundle.Field{ID: "f2", Name: "Notes", Kind: "TEXT"}))
	must(w.Write(spacebundle.KindProject, spacebundle.Project{ID: "p1", Name: "Roadmap"}))
	est := 2.5
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	must(w.Write(spacebundle.KindTask, spacebundle.Task{ID: "a", Sequence: 1, Title: "=HYPERLINK(\"http://x\")", Description: "line1\nline2, \"q\"",
		Priority: "HIGH", TypeID: "ty1", StatusID: "st1", ProjectID: "p1", DueDate: "2026-02-01", Estimate: &est, CreatedAt: now, UpdatedAt: now,
		LabelIDs: []string{"l1", "l2"}, IterationName: "Sprint 1", CreatedByKey: "u:1",
		Assignees: []spacebundle.Assignee{{Kind: "HUMAN", PersonKey: "u:1"}, {Kind: "AGENT", Name: "Bot"}}, External: "x:9",
		Fields: map[string]json.RawMessage{"f1": json.RawMessage(`"o1"`), "f2": json.RawMessage(`"+cmd"`)}}))
	must(w.Write(spacebundle.KindTask, spacebundle.Task{ID: "b", Sequence: 2, Title: "child", Priority: "LOW", TypeID: "ty1", StatusID: "st1",
		ParentID: "a", CreatedAt: now, UpdatedAt: now}))
	must(w.Write(spacebundle.KindPerson, spacebundle.Person{Key: "u:1", Kind: "user", Name: "Lin", Email: "lin@example.test"}))
	must(w.Write(spacebundle.KindPerson, spacebundle.Person{Key: "x:9", Kind: "external", Name: "Ext Person"}))
	must(w.Close())
	return buf.Bytes()
}

func TestWriteCSV(t *testing.T) {
	data := buildBundle(t)
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	var out bytes.Buffer
	n, err := WriteCSV(&out, open)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("tasks = %d", n)
	}
	raw := out.Bytes()
	if !bytes.HasPrefix(raw, utf8BOM) {
		t.Fatal("missing UTF-8 BOM")
	}
	rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, utf8BOM))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	header := strings.Join(rows[0], "|")
	// 自定义字段列在固定列之后，按名称排序（Notes 在 Severity 前）。
	if !strings.HasSuffix(header, "描述|Notes|Severity") || !strings.HasPrefix(header, "编号|标题|类型|状态|状态类别|优先级|处理人|标签|专项|迭代|父任务编号|截止日期|估时|创建人|创建时间|更新时间|") {
		t.Fatalf("header = %s", header)
	}
	col := map[string]int{}
	for i, name := range rows[0] {
		col[name] = i
	}
	first, second := rows[1], rows[2]
	check := func(name, want string, row []string) {
		t.Helper()
		if got := row[col[name]]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	check("编号", "ALP-1", first)
	check("标题", `'=HYPERLINK("http://x")`, first) // 公式注入防护
	check("类型", "缺陷", first)
	check("状态", "进行中", first)
	check("状态类别", "进行中", first)
	check("处理人", "lin@example.test;Bot;Ext Person", first)
	check("标签", "bug;ui", first)
	check("专项", "Roadmap", first)
	check("迭代", "Sprint 1", first)
	check("截止日期", "2026-02-01", first)
	check("估时", "2.5", first)
	check("创建人", "lin@example.test", first)
	check("创建时间", "2026-01-02T03:04:05Z", first)
	check("描述", "line1\nline2, \"q\"", first)
	check("Severity", "High", first) // 选项换成标签
	check("Notes", "'+cmd", first)
	check("编号", "ALP-2", second)
	check("父任务编号", "ALP-1", second)
}

func TestWriteCSVTruncatedBundle(t *testing.T) {
	data := buildBundle(t)
	cut := data[:len(data)/2]
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(cut)), nil }
	if _, err := WriteCSV(io.Discard, open); err == nil {
		t.Fatal("expected an error for a truncated bundle")
	}
}

func TestPriorityLabel(t *testing.T) {
	for in, want := range map[string]string{"NONE": "无", "URGENT": "紧急", "NORMAL": "中", "custom": "custom"} {
		if got := priorityLabel(in); got != want {
			t.Fatalf("priorityLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
