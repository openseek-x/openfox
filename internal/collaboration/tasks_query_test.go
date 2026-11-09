package collaboration

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseTaskFilter(t *testing.T) {
	user := "00000000-0000-0000-0000-000000000001"
	values := url.Values{
		"status_id":        {user},
		"priority":         {"high", "NONE"},
		"assignee":         {"user:" + strings.ToUpper(user), "none"},
		"assignee_id":      {user},
		"category":         {"canceled"},
		"q":                {"  report  "},
		"due_from":         {"2026-11-01"},
		"include_subtasks": {"false"},
		"completed":        {"hide"},
		"sort":             {"Priority"},
		"order":            {"DESC"},
	}
	filter, err := ParseTaskFilter(values)
	if err != nil {
		t.Fatal(err)
	}
	if len(filter.Priorities) != 2 || filter.Priorities[0] != "HIGH" || filter.Categories[0] != "CANCELLED" {
		t.Fatalf("filter = %+v", filter)
	}
	// 旧参数 assignee_id 追加为 user:<id>；取值统一小写。
	if len(filter.Assignees) != 3 || filter.Assignees[0] != "user:"+user || filter.Assignees[2] != "user:"+user {
		t.Fatalf("assignees = %v", filter.Assignees)
	}
	if filter.Query != "report" || filter.DueFrom != "2026-11-01" || !filter.ExcludeSubtasks || !filter.HideCompleted ||
		filter.Sort != "priority" || filter.Order != "desc" {
		t.Fatalf("filter = %+v", filter)
	}
	invalid := []url.Values{
		{"status_id": {"nope"}},
		{"priority": {"MEDIUM"}},
		{"assignee": {"team:" + user}},
		{"assignee": {"user:"}},
		{"category": {"OPEN"}},
		{"q": {strings.Repeat("x", 101)}},
		{"start_to": {"2026-02-30"}},
		{"parent_task_id": {"x"}},
		{"include_subtasks": {"yes"}},
		{"completed": {"none"}},
		{"label_id": make([]string, maxFilterValues+1)},
	}
	for _, values := range invalid {
		if _, err := ParseTaskFilter(values); !errors.Is(err, errInvalidFilter) {
			t.Fatalf("ParseTaskFilter(%v) error = %v", values, err)
		}
	}
}

func TestResolveTaskSortAndCursor(t *testing.T) {
	for _, tc := range []struct{ sort, order string }{{"random", ""}, {"manual", "desc"}, {"created", "sideways"}} {
		if _, err := resolveTaskSort(tc.sort, tc.order, "manual", "asc"); err == nil {
			t.Fatalf("resolveTaskSort(%q, %q) accepted", tc.sort, tc.order)
		}
	}
	// 默认排序带默认方向；显式排序不带方向时为 asc。
	if sorting, err := resolveTaskSort("", "", "updated", "desc"); err != nil || sorting.name != "updated" || sorting.order != "desc" {
		t.Fatalf("default sort = %+v %v", sorting, err)
	}
	if sorting, err := resolveTaskSort("due", "", "updated", "desc"); err != nil || sorting.order != "asc" {
		t.Fatalf("explicit sort = %+v %v", sorting, err)
	}
	task := Task{ID: "00000000-0000-0000-0000-000000000009", Rank: "h000000000007", Priority: "HIGH",
		CreatedAt: time.Date(2026, 10, 29, 1, 2, 3, 456789000, time.UTC)}
	created, _ := resolveTaskSort("created", "desc", "manual", "asc")
	cursor := created.encodeCursor(task)
	decoded, err := created.decodeCursor(cursor)
	if err != nil || decoded.Key != "2026-10-29T01:02:03.456789Z" || decoded.ID != task.ID {
		t.Fatalf("decoded = %+v %v", decoded, err)
	}
	// 游标与排序（名称或方向）不匹配都拒绝。
	createdAsc, _ := resolveTaskSort("created", "asc", "manual", "asc")
	manual, _ := resolveTaskSort("manual", "", "manual", "asc")
	for _, sorting := range []taskSort{createdAsc, manual} {
		if _, err := sorting.decodeCursor(cursor); !errors.Is(err, errInvalidCursor) {
			t.Fatalf("mismatched cursor accepted by %+v", sorting)
		}
	}
	if _, err := manual.decodeCursor("!!"); !errors.Is(err, errInvalidCursor) {
		t.Fatal("garbage cursor accepted")
	}
	// due 排序：空值在 asc 时排最后（'~'），desc 时排最后（''）。
	dueAsc, _ := resolveTaskSort("due", "asc", "manual", "asc")
	dueDesc, _ := resolveTaskSort("due", "desc", "manual", "asc")
	if dueAsc.sortKey(task) != "~" || dueDesc.sortKey(task) != "" || manual.sortKey(task) != task.Rank {
		t.Fatalf("sort keys = %q %q %q", dueAsc.sortKey(task), dueDesc.sortKey(task), manual.sortKey(task))
	}
	priority, _ := resolveTaskSort("priority", "desc", "manual", "asc")
	if priority.sortKey(task) != "4" || priorityOrdinal("NONE") != 0 || priorityOrdinal("LOWEST") != 1 ||
		priorityOrdinal("URGENT") != 5 {
		t.Fatalf("priority key = %q", priority.sortKey(task))
	}
}

func TestAssigneeFilterSQLIsNullSafe(t *testing.T) {
	q := &taskQuery{}
	sql := assigneeMatchSQL(q, []string{"user:00000000-0000-0000-0000-000000000001", "none"})
	// 「不是」直接对整个表达式取反，必须保证表达式不会是 NULL。
	if !strings.HasPrefix(sql, "COALESCE((") || !strings.HasSuffix(sql, "), false)") || len(q.args) != 1 {
		t.Fatalf("assignee sql = %s (%d args)", sql, len(q.args))
	}
}

func TestNormalizeAssigneesAndLegacyReplacement(t *testing.T) {
	id := "00000000-0000-0000-0000-00000000000A"
	refs, err := normalizeAssignees([]TaskAssigneeRef{{Kind: "human", ID: id}, {Kind: "HUMAN", ID: strings.ToLower(id)}, {Kind: "crew", ID: id}})
	if err != nil || len(refs) != 2 || refs[0].ID != strings.ToLower(id) || refs[1].Kind != "CREW" {
		t.Fatalf("normalize = %+v %v", refs, err)
	}
	if _, err := normalizeAssignees([]TaskAssigneeRef{{Kind: "TEAM", ID: id}}); !errors.Is(err, errAssigneeNotAllowed) {
		t.Fatalf("invalid kind error = %v", err)
	}
	many := make([]TaskAssigneeRef, maxTaskAssignees+1)
	if _, err := normalizeAssignees(many); !errors.Is(err, errTooManyAssignees) {
		t.Fatalf("too many error = %v", err)
	}
	a, b, agent := "a", "b", "g"
	current := []TaskAssigneeRef{{Kind: "HUMAN", ID: a}, {Kind: "AGENT", ID: agent}, {Kind: "HUMAN", ID: b}}
	// 替换第一位人员，保留顺序与其他处理人。
	replaced, changed := replaceFirstOfKind(current, "HUMAN", "c", false)
	if !changed || replaced[0].ID != "c" || replaced[2].ID != b {
		t.Fatalf("replace = %+v", replaced)
	}
	// 与现状相同不算变化；空串表示移除第一位。
	if _, changed := replaceFirstOfKind(current, "HUMAN", a, false); changed {
		t.Fatal("same value reported as changed")
	}
	removed, _ := replaceFirstOfKind(current, "AGENT", "", false)
	if len(removed) != 2 || firstOfKind(removed, "AGENT") != "" {
		t.Fatalf("remove = %+v", removed)
	}
	appended, _ := replaceFirstOfKind([]TaskAssigneeRef{{Kind: "HUMAN", ID: a}}, "AGENT", agent, false)
	if len(appended) != 2 || appended[1].ID != agent {
		t.Fatalf("append = %+v", appended)
	}
}

func TestTaskDatesAndLinks(t *testing.T) {
	if _, err := parseDate("2026-13-01"); !errors.Is(err, errInvalidDate) {
		t.Fatalf("invalid date error = %v", err)
	}
	if err := checkDateRange("2026-11-02", "2026-11-01"); !errors.Is(err, errInvalidDateRange) {
		t.Fatalf("range error = %v", err)
	}
	if err := checkDateRange("", "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"ftp://example.test", "https://", "javascript:alert(1)", "https://example.test/" + strings.Repeat("x", 2048)} {
		if _, err := normalizeLinkURL(raw); !errors.Is(err, errInvalidLink) {
			t.Fatalf("normalizeLinkURL(%q) error = %v", raw, err)
		}
	}
	if link, err := normalizeLinkURL("  https://example.test/a?b=1 "); err != nil || link != "https://example.test/a?b=1" {
		t.Fatalf("link = %q %v", link, err)
	}
	if _, err := normalizeEmoji(strings.Repeat("a", 33)); !errors.Is(err, errInvalidReaction) {
		t.Fatal("long emoji accepted")
	}
}
