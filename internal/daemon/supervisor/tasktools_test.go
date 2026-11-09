package supervisor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func testTask() *RunTask {
	return &RunTask{ID: "t1", DisplayID: "OF-7", Title: "Fix login", Description: "desc body", Acceptance: "ac body",
		StatusName: "To do", StatusCategory: "TODO", Origin: "TASK_ASSIGNMENT",
		Comments: []RunTaskComment{{AuthorName: "Ann", AuthorKind: "USER", CreatedAt: "2026-10-31T00:00:00Z", Body: "please hurry"}}}
}

func TestBuildPromptWithoutTaskUnchanged(t *testing.T) {
	if got := buildPrompt(RunContext{Prompt: "only"}); got != "only" {
		t.Fatalf("prompt = %q", got)
	}
	if taskTools(RunContext{}) != nil {
		t.Fatal("tools declared without a task")
	}
}

func TestBuildPromptWithTaskSection(t *testing.T) {
	prompt := buildPrompt(RunContext{Prompt: "now", Task: testTask(), History: []HistoryMessage{{AuthorKind: "USER", Body: "earlier"}}})
	for _, want := range []string{"OF-7", "Fix login", "desc body", "ac body", "please hurry", "openfox_task_start", "waiting_review", "triggered by assigning"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if strings.Index(prompt, "Task context") > strings.Index(prompt, "Conversation so far") || !strings.HasSuffix(prompt, "now") {
		t.Fatalf("section order wrong: %q", prompt)
	}
}

func TestBuildPromptWithTaskKeepsBudget(t *testing.T) {
	big := strings.Repeat("x", runtime.MaxPromptBytes/2)
	task := testTask()
	task.Description = strings.Repeat("d", 100000)
	prompt := buildPrompt(RunContext{Prompt: "now", Task: task, History: []HistoryMessage{{AuthorKind: "USER", Body: big}, {AuthorKind: "USER", Body: "recent"}}})
	if len(prompt) > runtime.MaxPromptBytes || !strings.Contains(prompt, "[USER] recent") || !strings.Contains(prompt, "Working agreement") {
		t.Fatalf("prompt length=%d", len(prompt))
	}
	huge := buildPrompt(RunContext{Prompt: strings.Repeat("y", runtime.MaxPromptBytes), Task: task})
	if len(huge) > runtime.MaxPromptBytes {
		t.Fatalf("oversized request not bounded: %d", len(huge))
	}
}

func TestTaskToolsDeclaration(t *testing.T) {
	tools := taskTools(RunContext{Task: testTask()})
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if !json.Valid(tool.InputSchema) || !strings.Contains(string(tool.InputSchema), `"additionalProperties":false`) {
			t.Errorf("bad schema for %s", tool.Name)
		}
	}
	for _, name := range []string{"openfox_task_view", "openfox_task_comment", "openfox_task_start", "openfox_conversation_state"} {
		if !names[name] {
			t.Errorf("missing %s", name)
		}
	}
	// 声明必须通过 Normalize 校验。
	if _, err := (runtime.RunRequest{Prompt: "p", Tools: tools}).Normalize(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorPassesTaskToolsAndForwardsCalls(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.extend = func(rc *RunContext) { rc.Task = testTask() }
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	request := adapter.requests[0]
	adapter.mu.Unlock()
	if len(request.Tools) != 4 || request.ToolCall == nil || !strings.Contains(request.Prompt, "OF-7") {
		t.Fatalf("tools=%d call=%v", len(request.Tools), request.ToolCall != nil)
	}
	text, ok := request.ToolCall(context.Background(), "openfox_task_comment", "call-1", json.RawMessage(`{"body":"hi"}`))
	if !ok || text != "done" {
		t.Fatalf("text=%q ok=%v", text, ok)
	}
	center.mu.Lock()
	got := center.toolRequests
	center.toolStatus = 409
	center.mu.Unlock()
	if len(got) != 1 || got[0].Tool != "task_comment" || got[0].Body["call_id"] != "call-1" || got[0].Body["generation"] != float64(1) {
		t.Fatalf("requests = %+v", got)
	}
	if args, _ := got[0].Body["arguments"].(map[string]any); args["body"] != "hi" {
		t.Fatalf("arguments = %v", got[0].Body["arguments"])
	}
	text, ok = request.ToolCall(context.Background(), "openfox_task_view", "call-2", nil)
	if ok || !strings.Contains(text, "409") || strings.Contains(text, "device-token") {
		t.Fatalf("text=%q ok=%v", text, ok)
	}
}

func TestSupervisorWithoutTaskDeclaresNoTools(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	request := adapter.requests[0]
	adapter.mu.Unlock()
	if len(request.Tools) != 0 || request.ToolCall != nil {
		t.Fatalf("tools=%d", len(request.Tools))
	}
}

func testSpaceTools() *RunSpaceTools {
	tools := &RunSpaceTools{SpaceID: "space-1", SpaceName: "产品组", SpacePrefix: "PRD", PublicURL: "https://openfox.example"}
	tools.Limits.Create, tools.Limits.Update = 5, 10
	return tools
}

func TestSpaceToolsDeclarationAndPrompt(t *testing.T) {
	runContext := RunContext{Prompt: "go", SpaceTools: testSpaceTools()}
	tools := taskTools(runContext)
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
		if !json.Valid(tool.InputSchema) || !strings.Contains(string(tool.InputSchema), `"additionalProperties":false`) {
			t.Errorf("bad schema for %s", tool.Name)
		}
	}
	for _, name := range []string{"openfox_space_task_create", "openfox_space_task_search", "openfox_space_task_update"} {
		if !names[name] {
			t.Errorf("missing %s", name)
		}
	}
	if names["openfox_task_view"] {
		t.Error("task tools declared without a task")
	}
	if _, err := (runtime.RunRequest{Prompt: "p", Tools: tools}).Normalize(); err != nil {
		t.Fatal(err)
	}
	prompt := buildPrompt(runContext)
	for _, want := range []string{"产品组", "PRD", "5 tasks created", "10 updates", "https://openfox.example"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	// 同时有任务时两组工具并存。
	runContext.Task = testTask()
	if got := len(taskTools(runContext)); got != 7 {
		t.Fatalf("tools = %d, want 7", got)
	}
	if taskTools(RunContext{}) != nil {
		t.Fatal("tools declared without task or space tools")
	}
}

func TestSupervisorForwardsSpaceToolCalls(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.extend = func(rc *RunContext) { rc.SpaceTools = testSpaceTools() }
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	request := adapter.requests[0]
	adapter.mu.Unlock()
	if len(request.Tools) != 3 || request.ToolCall == nil {
		t.Fatalf("tools=%d call=%v", len(request.Tools), request.ToolCall != nil)
	}
	if text, ok := request.ToolCall(context.Background(), "openfox_space_task_create", "c1", json.RawMessage(`{"title":"x"}`)); !ok || text != "done" {
		t.Fatalf("text=%q ok=%v", text, ok)
	}
	center.mu.Lock()
	got := center.toolRequests
	center.mu.Unlock()
	if len(got) != 1 || got[0].Tool != "space_task_create" || got[0].Body["call_id"] != "c1" {
		t.Fatalf("requests = %+v", got)
	}
}
