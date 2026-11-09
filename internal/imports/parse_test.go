package imports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseJiraCSVBasics(t *testing.T) {
	data := "\xEF\xBB\xBFSummary,Labels,Labels\nA,x,y\n\n  , ,\nB,only\nC,1,2,3,4\n"
	f, err := ParseJiraCSV(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.Headers[0] != "Summary" {
		t.Fatalf("BOM not stripped: %q", f.Headers[0])
	}
	if len(f.Rows) != 3 || f.SkippedRows != 2 {
		t.Fatalf("rows = %d skipped = %d", len(f.Rows), f.SkippedRows)
	}
	if got := f.Rows[1].Cells; len(got) != 3 || got[2] != "" {
		t.Fatalf("short row not padded: %v", got)
	}
	if len(f.Rows[2].Cells) != 3 || len(f.RowErrors) != 1 || f.RowErrors[0].Row != 6 {
		t.Fatalf("long row: cells %v errors %v", f.Rows[2].Cells, f.RowErrors)
	}
	if f.Rows[0].Line != 2 {
		t.Fatalf("line = %d", f.Rows[0].Line)
	}
}

func TestParseJiraCSVMultilineAndQuotes(t *testing.T) {
	data := "Summary,Description\n\"A\",\"line1\nline2, with \"\"quote\"\"\"\nB,bad \"quote here\n"
	f, err := ParseJiraCSV(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Rows) != 2 || !strings.Contains(f.Rows[0].Cells[1], "line2") {
		t.Fatalf("rows = %+v", f.Rows)
	}
	if f.Rows[1].Line != 4 {
		t.Fatalf("second row line = %d, want 4", f.Rows[1].Line)
	}
}

func TestParseJiraCSVRejects(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"non-utf8":     "Summary\n\xff\xfe\n",
		"too many col": strings.Repeat("a,", MaxColumns) + "b\n",
		"big cell":     "Summary\n" + strings.Repeat("x", MaxCellBytes+1) + "\n",
		"blank header": " , \nx,y\n",
	}
	var rows strings.Builder
	rows.WriteString("Summary\n")
	for i := 0; i < MaxDataRows+1; i++ {
		fmt.Fprintf(&rows, "r%d\n", i)
	}
	cases["too many rows"] = rows.String()
	for name, data := range cases {
		_, err := ParseJiraCSV(strings.NewReader(data))
		if !errors.Is(err, ErrInvalidFile) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if InvalidFileReason(err) == "" {
			t.Fatalf("%s: empty reason", name)
		}
	}
	ok := "Summary\n" + strings.Repeat("x\n", MaxDataRows)
	if _, err := ParseJiraCSV(strings.NewReader(ok)); err != nil {
		t.Fatalf("5000 rows should pass: %v", err)
	}
}

func TestSuggestFields(t *testing.T) {
	headers := []string{"Summary", "Issue key", "Issue id", "Issue Type", "Status", "Priority", "Assignee", "Assignee Id", "Reporter",
		"Parent", "Parent id", "Labels", "Labels", "Comment", "Comment", "Created", "Updated", "Due date",
		"Custom field (Start date)", "Custom field (Story point estimate)", "Sprint", "Sprint", "Inward issue link (Blocks)", "摘要", "  STATUS "}
	got := SuggestFields(headers)
	want := []string{"title", "source_key", "source_id", "type", "status", "priority", "assignee", "ignore", "reporter",
		"ignore", "parent", "labels", "labels", "comment", "comment", "created_at", "updated_at", "due_date",
		"start_date", "story_points", "sprint", "ignore", "relations", "ignore", "ignore"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("col %d (%q) = %s, want %s", i, headers[i], got[i], want[i])
		}
	}
	cfg := SuggestedConfig(headers)
	if len(cfg.Columns) != len(headers) || !cfg.Options.ImportOpen || !cfg.Options.ImportDone || cfg.Options.Overwrite || cfg.Options.MigratedLabel {
		t.Fatalf("config = %+v", cfg.Options)
	}
	if cfg.Statuses == nil || cfg.Users == nil {
		t.Fatal("statuses/users must be non-nil")
	}
}

func TestParseDate(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	cases := []struct {
		in   string
		loc  *time.Location
		want string
	}{
		{"15/Mar/24 2:30 PM", sh, "2024-03-15 14:30 +0800"},
		{"15/Mar/24 14:30", sh, "2024-03-15 14:30 +0800"},
		{"15/Mar/24", sh, "2024-03-15 00:00 +0800"},
		{"5/Mar/24 9:05 AM", sh, "2024-03-05 09:05 +0800"},
		{"2024-03-15 14:30", ny, "2024-03-15 14:30 -0400"},
		{"2024-03-15T14:30:00.000+0100", sh, "2024-03-15 14:30 +0100"},
		{"2024-03-15T14:30:00Z", sh, "2024-03-15 14:30 +0000"},
		{"2024-03-15", sh, "2024-03-15 00:00 +0800"},
		{"2024/03/15 14:30", sh, "2024-03-15 14:30 +0800"},
		{"2024/03/15", sh, "2024-03-15 00:00 +0800"},
	}
	for _, c := range cases {
		got, ok := ParseDate(c.in, c.loc)
		if !ok || got.Format("2006-01-02 15:04 -0700") != c.want {
			t.Fatalf("ParseDate(%q) = %v %v, want %s", c.in, got, ok, c.want)
		}
	}
	for _, bad := range []string{"", "not a date", "32/Jan/24"} {
		if _, ok := ParseDate(bad, sh); ok {
			t.Fatalf("ParseDate(%q) should fail", bad)
		}
	}
	// 默认时区为 Asia/Shanghai。
	if got, ok := ParseDate("2024-03-15 08:00", nil); !ok || got.UTC().Hour() != 0 {
		t.Fatalf("default tz: %v", got)
	}
}

func TestParseCommentCell(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	c := ParseCommentCell("20/Mar/24 10:00 AM;acc-1;Hello; with semicolon", sh)
	if !c.HasAt || c.Author != "acc-1" || c.Body != "Hello; with semicolon" || c.At.Hour() != 10 {
		t.Fatalf("comment = %+v", c)
	}
	c = ParseCommentCell("just text; with semicolons; here", sh)
	if c.HasAt || c.Author != "" || c.Body != "just text; with semicolons; here" {
		t.Fatalf("plain comment = %+v", c)
	}
	c = ParseCommentCell("only one;part", sh)
	if c.HasAt || c.Body != "only one;part" {
		t.Fatalf("two-part = %+v", c)
	}
}

func TestMapPriority(t *testing.T) {
	cases := []struct {
		in     string
		levels int
		want   string
		ok     bool
	}{
		{"Highest", 5, "URGENT", true}, {"BLOCKER", 5, "URGENT", true}, {"紧急", 5, "URGENT", true},
		{"High", 5, "HIGH", true}, {"major", 4, "HIGH", true}, {"Medium", 5, "NORMAL", true}, {"普通", 5, "NORMAL", true},
		{"Low", 5, "LOW", true}, {"Minor", 5, "LOW", true}, {"Lowest", 5, "LOWEST", true}, {"Lowest", 4, "LOW", true},
		{"Trivial", 5, "LOWEST", true}, {"", 5, "NONE", true}, {"  ", 5, "NONE", true}, {"Weird", 5, "NONE", false},
	}
	for _, c := range cases {
		got, ok := MapPriority(c.in, c.levels)
		if got != c.want || ok != c.ok {
			t.Fatalf("MapPriority(%q, %d) = %s %v, want %s %v", c.in, c.levels, got, ok, c.want, c.ok)
		}
	}
}

func TestInferStatusCategory(t *testing.T) {
	for _, s := range []string{"Done", "closed", "Resolved", "Completed", "已完成", "关闭", "已解决"} {
		if InferStatusCategory(s) != "DONE" || !StatusIsDone(s) {
			t.Fatalf("%q should be DONE", s)
		}
	}
	for _, s := range []string{"Won't Do", "Cancelled", "canceled", "已取消"} {
		if InferStatusCategory(s) != "CANCELLED" || !StatusIsDone(s) {
			t.Fatalf("%q should be CANCELLED", s)
		}
	}
	for _, s := range []string{"To Do", "In Progress", "", "Backlog"} {
		if InferStatusCategory(s) != "" || StatusIsDone(s) {
			t.Fatalf("%q should be open", s)
		}
	}
}

func TestParseStoryPointsAndLabels(t *testing.T) {
	if v, ok := ParseStoryPoints("3.456"); !ok || v != 3.46 {
		t.Fatalf("points = %v %v", v, ok)
	}
	for _, bad := range []string{"", "abc", "-1", "NaN", "Inf"} {
		if _, ok := ParseStoryPoints(bad); ok {
			t.Fatalf("%q should be invalid", bad)
		}
	}
	labels, tooLong := SplitLabels([]string{"Backend urgent", "backend", " Front-end "}, 9)
	if strings.Join(labels, ",") != "Backend,urgent,Front-end" || tooLong != 0 {
		t.Fatalf("labels = %v tooLong=%d", labels, tooLong)
	}
	_, tooLong = SplitLabels([]string{"this-is-too-long ok"}, 8)
	if tooLong != 1 {
		t.Fatalf("tooLong = %d", tooLong)
	}
	if s, cut := TruncateRunes("你好世界", 2); s != "你好" || !cut {
		t.Fatalf("truncate = %q %v", s, cut)
	}
	if s, cut := TruncateRunes("ab", 2); s != "ab" || cut {
		t.Fatalf("no-op truncate = %q %v", s, cut)
	}
}
