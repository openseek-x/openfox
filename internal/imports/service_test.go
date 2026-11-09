package imports

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPlanAndClassify(t *testing.T) {
	headers := []string{"Summary", "Issue id", "Issue key", "Status", "Assignee", "Assignee Id", "Labels", "Labels", "Comment", "Parent id"}
	cfg := SuggestedConfig(headers)
	p := buildPlan("jira_csv", headers, cfg.Columns)
	if p.title != 0 || p.sourceID != 1 || p.assignee.name != 4 || p.assignee.id != 5 || len(p.labels) != 2 {
		t.Fatalf("plan = %+v", p)
	}
	rows := []rawItem{
		{Ordinal: 0, Cells: []string{"A", "1", "K-1", "Done", "Alice", "acc-a", "x", "y", "", ""}},
		{Ordinal: 1, Cells: []string{"", "2", "K-2", "To Do", "", "", "", "", "", ""}},
		{Ordinal: 2, Cells: []string{"C", "", "K-3", "To Do", "", "", "", "", "", ""}},
		{Ordinal: 3, Cells: []string{"D", "1", "K-4", "To Do", "", "", "", "", "", "1"}},
		{Ordinal: 4, Cells: []string{"E", "5", "K-5", "In Progress", "Bob", "", "", "", "", ""}},
	}
	infos := classify(p, rows, Options{ImportOpen: true, ImportDone: false})
	wantInvalid := []bool{false, true, true, true, false}
	for i, w := range wantInvalid {
		if infos[i].Invalid != w {
			t.Fatalf("row %d invalid = %v (%s)", i, infos[i].Invalid, infos[i].ErrMsg)
		}
	}
	if infos[3].ErrMsg != "源 ID 在文件内重复" {
		t.Fatalf("dup message = %q", infos[3].ErrMsg)
	}
	if !infos[0].Done || !infos[0].Filtered || infos[4].Done || infos[4].Filtered {
		t.Fatalf("filter flags: %+v / %+v", infos[0], infos[4])
	}
	if infos[0].Data.Assignee.ID != "acc-a" || infos[0].Data.Assignee.Name != "Alice" {
		t.Fatalf("assignee = %+v", infos[0].Data.Assignee)
	}
	// 没有 Id 列时用姓名作标识。
	if infos[4].Data.Assignee.ID != "Bob" {
		t.Fatalf("assignee without id = %+v", infos[4].Data.Assignee)
	}
	if len(infos[0].Data.LabelCells) != 2 {
		t.Fatalf("labels = %v", infos[0].Data.LabelCells)
	}
}

func TestProjectWorkflowCategoryDrivesTaskSelection(t *testing.T) {
	headers := []string{"ID", "Summary", "Status", "Status category"}
	config := SuggestedConfig(headers)
	row := []rawItem{{Cells: []string{"w", "Deliver", "Ready for customers", "done"}}}
	for _, source := range []string{"pingcode_api", "shortcut_api", "teambition_api", "jira_csv"} {
		got := classify(buildPlan(source, headers, config.Columns), row, Options{ImportOpen: true})[0]
		want := source != "jira_csv"
		if got.Invalid || got.Done != want || got.Filtered != want {
			t.Fatalf("%s: %+v", source, got)
		}
	}
}

func TestDuplicateFields(t *testing.T) {
	cols := []ColumnMapping{{0, "title", ""}, {1, "title", ""}, {2, "labels", ""}, {3, "labels", ""}, {4, "status", ""}, {5, "status", ""}}
	got := duplicateFields(cols)
	if len(got) != 2 || got[0] != "title" || got[1] != "status" {
		t.Fatalf("duplicates = %v", got)
	}
	p := buildPlan("jira_csv", make([]string, 6), cols)
	if len(p.duplicates) != 2 || len(p.labels) != 2 || p.title != 0 {
		t.Fatalf("plan = %+v", p)
	}
}

// analyze 在没有有效行时不访问数据库，可用 nil 事务检查 issues 规则。
func TestAnalyzeIssues(t *testing.T) {
	ctx := context.Background()
	l := lookups{statusByName: map[string]string{}, typeByName: map[string]string{}}
	cfg := Config{Columns: []ColumnMapping{{0, "status", ""}, {1, "status", ""}}, Options: Options{ImportOpen: true, ImportDone: true}}
	a, err := analyze(ctx, nil, "space", cfg, nil, l, false, SourceSystem)
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, i := range a.Issues {
		codes[i.Code] = true
	}
	for _, want := range []string{IssueTitleMissing, IssueSourceIDMissing, IssueDuplicateColumn, IssueNothingToImport} {
		if !codes[want] {
			t.Fatalf("missing issue %s in %+v", want, a.Issues)
		}
	}
	if a.Counts.Total != 0 || a.Issues == nil || a.Statuses == nil || a.Users == nil || a.Types == nil {
		t.Fatalf("analysis shape = %+v", a)
	}
}

func TestSuggestMember(t *testing.T) {
	l := lookups{members: []memberRef{{ID: "u1", FullName: "Alice Liu", Email: "alice@example.test"}, {ID: "u2", FullName: "", Email: "bob.k@example.test"}}}
	if id, _ := l.suggestMember("acc-1", "alice liu", ""); id == nil || *id != "u1" {
		t.Fatalf("by full name: %v", id)
	}
	if id, _ := l.suggestMember("acc-2", "Bob.K", ""); id == nil || *id != "u2" {
		t.Fatalf("by email local part: %v", id)
	}
	if id, _ := l.suggestMember("alice", "Someone Else", ""); id == nil || *id != "u1" {
		t.Fatalf("by source id: %v", id)
	}
	if id, _ := l.suggestMember("zzz", "Nobody", ""); id != nil {
		t.Fatalf("unexpected match %v", *id)
	}
}

func TestValidateConfigShape(t *testing.T) {
	s := Service{}
	cases := map[string]Config{
		"column out of range": {Columns: []ColumnMapping{{5, "title", ""}}},
		"duplicate column":    {Columns: []ColumnMapping{{0, "title", ""}, {0, "status", ""}}},
		"unknown field":       {Columns: []ColumnMapping{{0, "nope", ""}}},
		"status no target":    {Statuses: []StatusMapping{{Source: "A"}}},
		"status both":         {Statuses: []StatusMapping{{Source: "A", StatusID: "00000000-0000-0000-0000-000000000001", NewName: "x", NewCategory: "TODO"}}},
		"new without cat":     {Statuses: []StatusMapping{{Source: "A", NewName: "x"}}},
		"triage category":     {Statuses: []StatusMapping{{Source: "A", NewName: "x", NewCategory: "TRIAGE"}}},
		"dup status source":   {Statuses: []StatusMapping{{Source: "A", NewName: "x", NewCategory: "TODO"}, {Source: "a", NewName: "y", NewCategory: "TODO"}}},
		"member no user":      {Users: []UserMapping{{SourceID: "a", Mode: "member"}}},
		"invite bad email":    {Users: []UserMapping{{SourceID: "a", Mode: "invite", Email: "nope"}}},
		"dup user":            {Users: []UserMapping{{SourceID: "a", Mode: "skip"}, {SourceID: "a", Mode: "external"}}},
		"bad mode":            {Users: []UserMapping{{SourceID: "a", Mode: "x"}}},
	}
	for name, cfg := range cases {
		cfg := cfg
		if err := s.validateConfig(context.Background(), nil, &cfg, 2); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	ok := Config{Columns: []ColumnMapping{{0, "title", ""}}, Statuses: []StatusMapping{{Source: "A", NewName: "x", NewCategory: "DONE"}},
		Users: []UserMapping{{SourceID: "a", Mode: "invite", Email: " A@B.com "}, {SourceID: "b", Mode: "skip"}}}
	if err := s.validateConfig(context.Background(), nil, &ok, 2); err != nil {
		t.Fatal(err)
	}
	if ok.Users[0].Email != "a@b.com" {
		t.Fatalf("email not normalized: %q", ok.Users[0].Email)
	}
}

func TestRollbackInfo(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	old := now.Add(-8 * 24 * time.Hour)
	cases := []struct {
		name     string
		status   string
		finished *time.Time
		created  int
		eligible bool
		reason   string
	}{
		{"ok", StatusSucceeded, &recent, 3, true, ""},
		{"failed ok", StatusFailed, &recent, 1, true, ""},
		{"cancelled ok", StatusCancelled, &recent, 1, true, ""},
		{"running", StatusRunning, nil, 3, false, "not_finished"},
		{"rolled back", StatusRolledBack, &recent, 0, false, "rolled_back"},
		{"expired", StatusSucceeded, &old, 3, false, "expired"},
		{"nothing created", StatusSucceeded, &recent, 0, false, "nothing_created"},
	}
	for _, c := range cases {
		got := rollbackInfo(c.status, c.finished, c.created, now)
		if got.Eligible != c.eligible || got.Reason != c.reason {
			t.Fatalf("%s: %+v", c.name, got)
		}
	}
	if got := rollbackInfo(StatusSucceeded, &recent, 1, now); got.Deadline == nil || !got.Deadline.Equal(recent.Add(7*24*time.Hour)) {
		t.Fatalf("deadline = %v", got.Deadline)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 31, 1, 2, 3, 456789000, time.UTC)
	id := "00000000-0000-0000-0000-000000000001"
	gotAt, gotID, err := decodeCursor(encodeCursor(at, id))
	if err != nil || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("cursor = %v %v %v", gotAt, gotID, err)
	}
	for _, bad := range []string{"!!!", "YWJj", encodeCursor(at, "not-uuid")} {
		if _, _, err := decodeCursor(bad); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("bad cursor %q err = %v", bad, err)
		}
	}
}

func TestWarnAccumulation(t *testing.T) {
	a := newWarnAcc(nil)
	a.add("x", "first")
	a.add("x", "second")
	a.add("y", "other")
	if len(a.list) != 2 || a.list[0].Count != 2 || a.list[0].Message != "first" {
		t.Fatalf("warnings = %+v", a.list)
	}
	for i := 0; i < 80; i++ {
		a.add("code"+string(rune('A'+i%26))+string(rune('a'+i/26)), "m")
	}
	if len(a.list) > maxWarningKinds {
		t.Fatalf("kinds = %d", len(a.list))
	}
	c := cloneWarn(a)
	c.add("x", "")
	if a.list[0].Count != 2 {
		t.Fatal("clone must not share state")
	}
}

func TestOperationID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := newOperationID()
		if len(id) != 15 || id[:3] != "op_" || seen[id] {
			t.Fatalf("operation id %q", id)
		}
		seen[id] = true
	}
}
