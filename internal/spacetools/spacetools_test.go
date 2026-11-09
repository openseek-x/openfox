package spacetools

import (
	"encoding/json"
	"errors"
	"testing"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
)

func TestPriorityMapping(t *testing.T) {
	for in, want := range map[string]string{"": "NONE", "none": "NONE", "Urgent": "URGENT", "high": "HIGH", "medium": "NORMAL", "low": "LOW"} {
		if got, err := spaceToolPriority(in); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	if _, err := spaceToolPriority("critical"); err == nil {
		t.Error("unknown priority accepted")
	}
}

func TestDatesAndCategories(t *testing.T) {
	if spaceToolDate("2026-11-02") != nil || spaceToolDate("2026-13-01") == nil || spaceToolDate("tomorrow") == nil {
		t.Error("date validation")
	}
	for in, want := range map[string]string{"backlog": "BACKLOG", "unstarted": "TODO", "started": "IN_PROGRESS", "completed": "DONE", "canceled": "CANCELLED"} {
		if got, err := spaceToolCategory(in); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	if _, err := spaceToolCategory("done"); err == nil {
		t.Error("unknown category accepted")
	}
}

func TestArgumentParsing(t *testing.T) {
	args := map[string]json.RawMessage{"title": json.RawMessage(`"  标题 "`), "n": json.RawMessage(`3`), "flag": json.RawMessage(`true`), "nil": json.RawMessage(`null`)}
	if v, ok, err := argString(args, "title"); err != nil || !ok || v != "标题" {
		t.Errorf("title = %q %v %v", v, ok, err)
	}
	if _, _, err := argString(args, "n"); err == nil {
		t.Error("number accepted as string")
	}
	if _, ok, err := argString(args, "nil"); err != nil || ok {
		t.Errorf("null = %v %v", ok, err)
	}
	if v, err := argBool(args, "flag"); err != nil || !v {
		t.Errorf("flag = %v %v", v, err)
	}
	if _, err := argBool(args, "title"); err == nil {
		t.Error("string accepted as bool")
	}
}

func TestErrorText(t *testing.T) {
	if text, ok := spaceToolErrorText(newUserError("参数不对")); !ok || text != "参数不对" {
		t.Errorf("user error = %q %v", text, ok)
	}
	if _, ok := spaceToolErrorText(conversations.ErrForbidden); !ok {
		t.Error("forbidden should be a business error")
	}
	if text, ok := spaceToolErrorText(collaboration.ErrSpaceRetired); !ok || text == "" {
		t.Errorf("retired = %q %v", text, ok)
	}
	if _, ok := spaceToolErrorText(errors.New("connection reset")); ok {
		t.Error("system errors must not be swallowed")
	}
}

func TestRequestIDStable(t *testing.T) {
	plan := dispatch.SpaceToolPlan{RunID: "run-1"}
	a, b := spaceToolRequestID(plan, "call-1"), spaceToolRequestID(plan, "call-1")
	if a != b || a == spaceToolRequestID(plan, "call-2") {
		t.Errorf("request ids: %s %s", a, b)
	}
}

func TestTaskLine(t *testing.T) {
	task := collaboration.Task{DisplayID: "OF-7", StatusName: "Todo", Title: "修登录", DueDate: "2026-11-05",
		Assignees: []collaboration.TaskAssignee{{Kind: "HUMAN", Name: "Ann"}, {Kind: "INVITATION", Name: "pending@example.test"}}}
	if got := spaceToolTaskLine(task); got != "OF-7 [Todo] 修登录（处理人：Ann，截止：2026-11-05）" {
		t.Errorf("line = %q", got)
	}
	task.Assignees, task.DueDate = nil, ""
	if got := spaceToolTaskLine(task); got != "OF-7 [Todo] 修登录（处理人：无，截止：无）" {
		t.Errorf("line = %q", got)
	}
}
