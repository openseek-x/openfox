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

func ocEvent(kind string, part map[string]any) string {
	return j(map[string]any{"type": kind, "timestamp": 1, "sessionID": "ses_1", "part": part})
}

func openCodeSuccess() []string {
	return []string{
		ocEvent("step_start", map[string]any{"type": "step-start"}),
		ocEvent("reasoning", map[string]any{"type": "reasoning", "text": "thinking hard"}),
		ocEvent("text", map[string]any{"type": "text", "text": "Checking files."}),
		ocEvent("tool_use", map[string]any{"type": "tool", "tool": "bash", "callID": "c1", "state": map[string]any{
			"status": "completed", "input": map[string]any{"command": "ls"}, "output": "a\nb", "metadata": map[string]any{"exit": 0}}}),
		ocEvent("tool_use", map[string]any{"type": "tool", "tool": "bash", "callID": "c2", "state": map[string]any{
			"status": "completed", "input": map[string]any{"command": "false"}, "output": "", "metadata": map[string]any{"exit": 1}}}),
		ocEvent("step_finish", map[string]any{"type": "step-finish", "reason": "tool-calls", "tokens": map[string]any{"input": 7, "output": 3, "reasoning": 1, "cache": map[string]any{"read": 4, "write": 2}}}),
		ocEvent("step_start", map[string]any{"type": "step-start"}),
		ocEvent("text", map[string]any{"type": "text", "text": "Finished."}),
		ocEvent("step_finish", map[string]any{"type": "step-finish", "reason": "stop", "tokens": map[string]any{"input": 1, "output": 1}}),
	}
}

func TestOpenCodeSuccess(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stdout: openCodeSuccess()})
	outcome := f.run(runtime.RunRequest{Prompt: "-rf dangerous prompt", Model: "anthropic/claude-sonnet", Instructions: "rules"})
	if outcome.result.Status != runtime.StatusSucceeded || strings.TrimSpace(outcome.result.Output) != "Finished." {
		t.Fatalf("result = %+v", outcome.result)
	}
	if outcome.result.SessionID != "ses_1" || outcome.result.Usage == nil || outcome.result.Usage.Input != 8 || outcome.result.Usage.CachedInput != 4 || outcome.result.Usage.ReasoningOutput != 1 {
		t.Fatalf("session/usage = %q %+v", outcome.result.SessionID, outcome.result.Usage)
	}
	ok, _ := outcome.finalStep("c1")
	bad, _ := outcome.finalStep("c2")
	if ok.State != runtime.StepDone || ok.Command != "ls" || bad.State != runtime.StepFailed || bad.ExitCode == nil || *bad.ExitCode != 1 {
		t.Fatalf("tool steps = %+v / %+v", ok, bad)
	}
	var sawCommentary bool
	for _, step := range outcome.steps() {
		if step.Kind == runtime.StepKindAgentText && step.Text == "Checking files." {
			sawCommentary = true
		}
	}
	if !sawCommentary {
		t.Fatal("text before tool use must be an agent_text step")
	}
	rec := f.record()
	args := strings.Join(rec.Args, "\x00")
	if !strings.Contains(args, "run\x00--format\x00json") || !strings.Contains(args, "--model\x00anthropic/claude-sonnet") ||
		!strings.HasSuffix(args, "--\x00rules\n\n-rf dangerous prompt") {
		t.Fatalf("args = %q", rec.Args)
	}
	if !strings.Contains(rec.Permission, `"bash":"allow"`) {
		t.Fatalf("permission env = %q", rec.Permission)
	}
}

func TestOpenCodeResumeAndPromptLimit(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stdout: openCodeSuccess()})
	f.run(runtime.RunRequest{ResumeSession: "ses_0"})
	if !strings.Contains(strings.Join(f.record().Args, " "), "--session ses_0") {
		t.Fatalf("args = %v", f.record().Args)
	}
	if _, err := f.adapter.Start(context.Background(), runtime.RunRequest{Prompt: strings.Repeat("x", maxPromptViaArgs+1)}); err == nil {
		t.Fatal("oversized prompt must be rejected")
	}
}

func TestOpenCodeMissingFinalStepFinishStillCompletes(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stdout: openCodeSuccess()[:8]})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusSucceeded || strings.TrimSpace(outcome.result.Output) != "Finished." {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestOpenCodeErrorEvent(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stdout: []string{
		ocEvent("step_start", map[string]any{"type": "step-start"}),
		j(map[string]any{"type": "error", "sessionID": "ses_1", "error": map[string]any{"name": "APIError", "data": map[string]any{"message": "model not found"}}}),
	}})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusFailed || !strings.Contains(outcome.result.Error, "model not found") {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestOpenCodeErrorExit(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stderr: "Error: crashed", Exit: 1})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusFailed || !strings.Contains(outcome.result.Error, "crashed") {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestOpenCodeRetryTimeoutIncludesRedactedProviderError(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stderr: `timestamp=2026-11-05T03:56:17Z level=ERROR error.error="AI_APICallError: Free usage limit reached; token=qa-secret-value"`, SleepMS: 5000})
	outcome := f.run(runtime.RunRequest{Timeout: time.Second,
		Environment: map[string]string{"TEST_TOKEN": "qa-secret-value"}, EnvironmentAllowlist: []string{"TEST_TOKEN"}})
	if outcome.result.Status != runtime.StatusTimedOut || !outcome.result.ProcessExited ||
		!strings.Contains(outcome.result.Error, "Free usage limit reached") ||
		strings.Contains(outcome.result.Error, "qa-secret-value") {
		t.Fatalf("result = %+v", outcome.result)
	}
	if strings.Contains(outcome.result.Error, "timestamp=") {
		t.Fatalf("raw log metadata leaked into message: %s", outcome.result.Error)
	}
	if !strings.Contains(strings.Join(f.record().Args, " "), "--print-logs --log-level ERROR") {
		t.Fatalf("missing provider diagnostics: %v", f.record().Args)
	}
}

func TestOpenCodeDetectAndModels(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Version: "1.18.5", ModelsOut: "anthropic/claude-sonnet\nopenai/gpt-x\nnoise line\n"})
	report, err := f.adapter.Detect(context.Background())
	// 登录状态判断不了：unknown，但可运行。
	if err != nil || report.Login != runtime.LoginUnknown || !report.Runnable || report.Version != "1.18.5" {
		t.Fatalf("report = %+v err=%v", report, err)
	}
	models, err := f.adapter.ListModels(context.Background())
	if err != nil || len(models) != 3 || models[0].ID != "default" || models[1].ID != "anthropic/claude-sonnet" {
		t.Fatalf("models = %+v err=%v", models, err)
	}
}

func TestOpenCodeTaskToolBridge(t *testing.T) {
	f := newFixture(t, NewOpenCode(""))
	f.set(scenario{Stdout: openCodeSuccess(), MCPFrom: "opencode", MCPTool: "openfox_task_get"})
	outcome := f.run(runtime.RunRequest{
		Tools: []runtime.Tool{{Name: "openfox_task_get", Description: "d", InputSchema: []byte(`{"type":"object"}`)}},
		ToolCall: func(context.Context, string, string, json.RawMessage) (string, bool) {
			return "oc body", false
		},
	})
	if outcome.result.Status != runtime.StatusSucceeded {
		t.Fatalf("result = %+v", outcome.result)
	}
	raw, _ := os.ReadFile(f.dir + "/record.json.mcp")
	if !strings.Contains(string(raw), "oc body") || !strings.Contains(string(raw), `"isError":true`) {
		t.Fatalf("mcp transcript:\n%s", raw)
	}
}
