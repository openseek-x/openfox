package cliagent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

func claudeSuccess() []string {
	return []string{
		j(map[string]any{"type": "system", "subtype": "init", "session_id": "sess-1"}),
		j(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "thinking", "thinking": "plan it"},
			map[string]any{"type": "text", "text": "Let me look."},
			map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{"command": "ls -la"}},
		}}}),
		j(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu1", "content": "file-a\nfile-b"},
		}}}),
		j(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": "tu2", "name": "Read", "input": map[string]any{"file_path": "/work/a.go"}},
		}}}),
		j(map[string]any{"type": "user", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tu2", "content": []any{map[string]any{"type": "text", "text": "boom"}}, "is_error": true},
		}}}),
		j(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "All done."}}}}),
		j(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": "All done.", "session_id": "sess-1",
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}}),
	}
}

func TestClaudeSuccessStepsAndArgs(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeSuccess()})
	outcome := f.run(runtime.RunRequest{Prompt: "line1\nline2 \"quoted\"", Instructions: "be brief", Model: "sonnet"})
	if outcome.result.Status != runtime.StatusSucceeded || strings.TrimSpace(outcome.result.Output) != "All done." {
		t.Fatalf("result = %+v", outcome.result)
	}
	if outcome.result.SessionID != "sess-1" || outcome.result.Usage == nil || outcome.result.Usage.Input != 10 || outcome.result.Usage.CachedInput != 3 || outcome.result.Usage.CacheWriteInput != 2 {
		t.Fatalf("session/usage = %q %+v", outcome.result.SessionID, outcome.result.Usage)
	}
	exec, ok := outcome.finalStep("tu1")
	if !ok || exec.Kind != runtime.StepKindExec || exec.State != runtime.StepDone || exec.Command != "ls -la" || exec.Output == nil || *exec.Output != "file-a\nfile-b" {
		t.Fatalf("exec step = %+v", exec)
	}
	read, _ := outcome.finalStep("tu2")
	if read.Kind != runtime.StepKindRead || read.State != runtime.StepFailed || read.Path != "a.go" && read.Path != "/work/a.go" {
		t.Fatalf("read step = %+v", read)
	}
	var kinds []string
	for _, step := range outcome.steps() {
		kinds = append(kinds, step.Kind)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, runtime.StepKindThinking) || !strings.Contains(joined, runtime.StepKindAgentText) || !strings.HasSuffix(joined, runtime.StepKindTurnEnd) {
		t.Fatalf("step kinds = %s", joined)
	}
	if !outcome.has(runtime.EventStarted) || !outcome.has(runtime.EventCompleted) {
		t.Fatal("missing started/completed events")
	}
	rec := f.record()
	if rec.Stdin != "line1\nline2 \"quoted\"" {
		t.Fatalf("stdin = %q", rec.Stdin)
	}
	args := strings.Join(rec.Args, " ")
	for _, want := range []string{"-p", "--output-format stream-json", "--verbose", "--model sonnet", "--append-system-prompt be brief", "--permission-mode acceptEdits"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "--resume") || strings.Contains(args, "--mcp-config") {
		t.Fatalf("unexpected args %q", args)
	}
}

func TestClaudeDefaultModelAndResume(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeSuccess()})
	f.run(runtime.RunRequest{Model: "default", ResumeSession: "sess-0"})
	args := strings.Join(f.record().Args, " ")
	if strings.Contains(args, "--model") || !strings.Contains(args, "--resume sess-0") {
		t.Fatalf("args = %q", args)
	}
	if _, err := f.adapter.Start(context.Background(), runtime.RunRequest{Prompt: "x", ResumeSession: "--evil"}); err == nil {
		t.Fatal("resume session starting with - must be rejected")
	}
}

func TestClaudeResultErrorBecomesFailed(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: []string{
		j(map[string]any{"type": "system", "subtype": "init", "session_id": "s"}),
		j(map[string]any{"type": "result", "subtype": "success", "is_error": true, "result": "Not logged in · Please run /login"}),
	}, Exit: 1})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusFailed || !strings.Contains(outcome.result.Error, "Not logged in") {
		t.Fatalf("result = %+v", outcome.result)
	}
	turn, _ := outcome.finalStep("turn_end")
	if turn.FinalState != "failed" {
		t.Fatalf("turn_end = %+v", turn)
	}
}

func TestClaudeNonZeroExitWithoutResult(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stderr: "fatal: config broken", Exit: 2})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusFailed || !strings.Contains(outcome.result.Error, "config broken") {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestClaudeCleanExitWithoutResultIsProtocolError(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: []string{"not json banner"}})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusFailed || outcome.result.ErrorCode != "PROTOCOL_ERROR" {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestClaudeCancelMarksStepsInterrupted(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeSuccess()[:2], SleepMS: 60000})
	execution, err := f.adapter.Start(context.Background(), runtime.RunRequest{Prompt: "x", WorkingDirectory: f.dir})
	if err != nil {
		t.Fatal(err)
	}
	var outcome runOutcome
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range execution.Events() {
			outcome.events = append(outcome.events, event)
		}
	}()
	time.Sleep(600 * time.Millisecond)
	cancelCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := execution.Cancel(cancelCtx); err != nil {
		t.Fatal(err)
	}
	result, _ := execution.Wait(context.Background())
	<-done
	if result.Status != runtime.StatusCancelled {
		t.Fatalf("status = %s", result.Status)
	}
	step, _ := (runOutcome{events: outcome.events}).finalStep("tu1")
	if step.State != runtime.StepInterrupted {
		t.Fatalf("tool step = %+v", step)
	}
	if !(runOutcome{events: outcome.events}).has(runtime.EventCancelled) {
		t.Fatal("no cancelled event")
	}
}

func TestClaudeTimeout(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeSuccess()[:1], SleepMS: 60000})
	started := time.Now()
	outcome := f.run(runtime.RunRequest{Timeout: 700 * time.Millisecond})
	if outcome.result.Status != runtime.StatusTimedOut || time.Since(started) > 15*time.Second {
		t.Fatalf("result = %+v after %s", outcome.result, time.Since(started))
	}
}

func TestClaudeDetectLoginStates(t *testing.T) {
	cases := []struct {
		name  string
		sc    scenario
		login runtime.LoginStatus
		run   bool
	}{
		{"logged in", scenario{AuthJSON: `{"loggedIn":true,"authMethod":"claude.ai"}`}, runtime.LoginLoggedIn, true},
		{"logged out", scenario{AuthJSON: `{"loggedIn":false,"authMethod":"none"}`, AuthExit: 1}, runtime.LoginLoggedOut, false},
		{"unknown output", scenario{AuthJSON: `unrecognized`, AuthExit: 1}, runtime.LoginUnknown, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, NewClaudeCode(""))
			tc.sc.Version = "2.1.100 (Claude Code)"
			f.set(tc.sc)
			report, err := f.adapter.Detect(context.Background())
			if err != nil || report.Login != tc.login || report.Runnable != tc.run || report.Version != "2.1.100" || report.Runtime != NameClaudeCode {
				t.Fatalf("report = %+v err=%v", report, err)
			}
		})
	}
	// 未登录时 Start 拒绝启动。
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{AuthJSON: `{"loggedIn":false}`, AuthExit: 1})
	if _, err := f.adapter.Start(context.Background(), runtime.RunRequest{Prompt: "x"}); err == nil {
		t.Fatal("Start must fail when logged out")
	}
}

func TestDetectMissingExecutable(t *testing.T) {
	adapter := NewClaudeCode("/nonexistent/claude")
	if _, err := adapter.Detect(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat("/nonexistent/claude"); err == nil {
		t.Fatal("sanity")
	}
}

func TestClaudeListModelsIsStaticDefault(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{})
	models, err := f.adapter.ListModels(context.Background())
	if err != nil || len(models) < 2 || models[0].ID != "default" || !models[0].IsDefault {
		t.Fatalf("models = %+v err=%v", models, err)
	}
}

func TestClaudeTaskToolBridge(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeSuccess(), MCPFrom: "claude", MCPTool: "openfox_task_get"})
	var called []string
	outcome := f.run(runtime.RunRequest{
		Tools: []runtime.Tool{{Name: "openfox_task_get", Description: "get a task", InputSchema: []byte(`{"type":"object"}`)}},
		ToolCall: func(_ context.Context, name, callID string, arguments json.RawMessage) (string, bool) {
			called = append(called, name+":"+string(arguments))
			return "task body", true
		},
	})
	if outcome.result.Status != runtime.StatusSucceeded {
		t.Fatalf("result = %+v", outcome.result)
	}
	if len(called) != 1 || called[0] != `openfox_task_get:{"a":1}` {
		t.Fatalf("called = %v", called)
	}
	raw, _ := os.ReadFile(f.dir + "/record.json.mcp")
	text := string(raw)
	for _, want := range []string{"2025-03-26", "openfox_task_get", "task body", `"isError":false`, "unknown tool", "202 ", "wrong-token 404"} {
		if !strings.Contains(text, want) {
			t.Fatalf("mcp transcript lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(strings.Join(f.record().Args, " "), "mcp__openfox") {
		t.Fatalf("allowedTools lack the bridge: %v", f.record().Args)
	}
	// Run 结束后桥与临时配置被清理。
	entries, _ := os.ReadDir(f.dir + "/state")
	if len(entries) != 0 {
		t.Fatalf("state dir not cleaned: %v", entries)
	}
}
