package cliagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func piAssistant(stop, text string, extra ...map[string]any) map[string]any {
	content := []any{}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, block := range extra {
		content = append(content, block)
	}
	return map[string]any{"role": "assistant", "stopReason": stop, "content": content,
		"usage": map[string]any{"input": 5, "output": 2, "cacheRead": 1, "cacheWrite": 1}}
}

func piSuccess() []string {
	return []string{
		j(map[string]any{"type": "session", "version": 3, "id": "pi-sess", "cwd": "/w"}),
		j(map[string]any{"type": "agent_start"}),
		j(map[string]any{"type": "message_update", "assistantMessageEvent": map[string]any{"type": "text_delta", "delta": "x"}}),
		j(map[string]any{"type": "message_end", "message": piAssistant("toolUse", "Looking.", map[string]any{"type": "thinking", "thinking": "hmm"}, map[string]any{"type": "toolCall", "id": "t1", "name": "bash"})}),
		j(map[string]any{"type": "tool_execution_start", "toolCallId": "t1", "toolName": "bash", "args": map[string]any{"command": "echo hi"}}),
		j(map[string]any{"type": "tool_execution_end", "toolCallId": "t1", "toolName": "bash", "isError": false,
			"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "hi"}}}}),
		j(map[string]any{"type": "message_end", "message": piAssistant("stop", "Done.")}),
		j(map[string]any{"type": "agent_end", "willRetry": false}),
		j(map[string]any{"type": "agent_settled", "aborted": false}),
	}
}

func TestPiSuccess(t *testing.T) {
	f := newFixture(t, NewPi(""))
	f.set(scenario{Stdout: piSuccess()})
	outcome := f.run(runtime.RunRequest{Prompt: "multi\nline", Model: "anthropic/sonnet", Instructions: "inst"})
	if outcome.result.Status != runtime.StatusSucceeded || strings.TrimSpace(outcome.result.Output) != "Done." {
		t.Fatalf("result = %+v", outcome.result)
	}
	if outcome.result.SessionID != "pi-sess" || outcome.result.Usage == nil || outcome.result.Usage.Input != 10 {
		t.Fatalf("session/usage = %q %+v", outcome.result.SessionID, outcome.result.Usage)
	}
	step, _ := outcome.finalStep("t1")
	if step.Kind != runtime.StepKindExec || step.State != runtime.StepDone || step.Command != "echo hi" || step.Output == nil || *step.Output != "hi" {
		t.Fatalf("step = %+v", step)
	}
	rec := f.record()
	if rec.Stdin != "inst\n\nmulti\nline" || !strings.Contains(strings.Join(rec.Args, " "), "--mode json --model anthropic/sonnet") {
		t.Fatalf("record = %+v", rec)
	}
}

func TestPiErrorStopReason(t *testing.T) {
	f := newFixture(t, NewPi(""))
	msg := piAssistant("error", "")
	msg["errorMessage"] = "401 invalid key"
	f.set(scenario{Stdout: []string{
		j(map[string]any{"type": "session", "id": "s"}),
		j(map[string]any{"type": "message_end", "message": msg}),
		j(map[string]any{"type": "agent_settled", "aborted": false}),
	}})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusFailed || !strings.Contains(outcome.result.Error, "401 invalid key") {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestPiRetrySuccessClearsError(t *testing.T) {
	f := newFixture(t, NewPi(""))
	bad := piAssistant("error", "")
	bad["errorMessage"] = "529 overloaded"
	f.set(scenario{Stdout: []string{
		j(map[string]any{"type": "message_end", "message": bad}),
		j(map[string]any{"type": "auto_retry_start", "attempt": 1}),
		j(map[string]any{"type": "message_end", "message": piAssistant("stop", "ok now")}),
		j(map[string]any{"type": "agent_settled", "aborted": false}),
	}})
	outcome := f.run(runtime.RunRequest{})
	if outcome.result.Status != runtime.StatusSucceeded || strings.TrimSpace(outcome.result.Output) != "ok now" || !outcome.has(runtime.EventWarning) {
		t.Fatalf("result = %+v", outcome.result)
	}
}

func TestPiAbortedAndExitWithoutSettled(t *testing.T) {
	f := newFixture(t, NewPi(""))
	f.set(scenario{Stdout: []string{j(map[string]any{"type": "agent_settled", "aborted": true})}})
	if outcome := f.run(runtime.RunRequest{}); outcome.result.Status != runtime.StatusFailed {
		t.Fatalf("aborted result = %+v", outcome.result)
	}
	f.set(scenario{Stdout: []string{j(map[string]any{"type": "message_end", "message": piAssistant("stop", "late reply")})}})
	if outcome := f.run(runtime.RunRequest{}); outcome.result.Status != runtime.StatusSucceeded || strings.TrimSpace(outcome.result.Output) != "late reply" {
		t.Fatalf("finalize result = %+v", outcome.result)
	}
}

func TestPiResumeDetectModelsAndNoTools(t *testing.T) {
	f := newFixture(t, NewPi(""))
	f.set(scenario{Stdout: piSuccess(), Version: "0.9.1", ModelsOut: "provider  model  context\nanthropic claude-x 200k\nopenai gpt-y 128k\n"})
	called := false
	f.run(runtime.RunRequest{ResumeSession: "pi-0",
		Tools:    []runtime.Tool{{Name: "openfox_task_get", InputSchema: []byte(`{}`)}},
		ToolCall: func(context.Context, string, string, json.RawMessage) (string, bool) { called = true; return "", true }})
	if !strings.Contains(strings.Join(f.record().Args, " "), "--session pi-0") || called {
		t.Fatalf("args = %v called=%v", f.record().Args, called)
	}
	if !strings.Contains(strings.Join(f.record().Args, " "), "--no-mcp") {
		t.Fatal("pi must disable unbound MCP servers")
	}
	report, err := f.adapter.Detect(context.Background())
	if err != nil || report.Version != "0.9.1" || report.Login != runtime.LoginUnknown || !report.Runnable {
		t.Fatalf("report = %+v err=%v", report, err)
	}
	models, _ := f.adapter.ListModels(context.Background())
	if len(models) != 3 || models[1].ID != "anthropic/claude-x" {
		t.Fatalf("models = %+v", models)
	}
}

func TestPiNoModelsDoesNotExposeLoginGuidanceAsModel(t *testing.T) {
	f := newFixture(t, NewPi(""))
	f.set(scenario{Version: "1.5.0", ModelsOut: "No models available. Use /login to log into a provider via OAuth or API key. See:\n  /tmp/pi/docs/providers.md\n"})
	models, err := f.adapter.ListModels(context.Background())
	if err == nil || len(models) != 1 || models[0].ID != "default" {
		t.Fatalf("models = %+v, error = %v; want default and unavailable-model error", models, err)
	}
}

func TestPiModelHeaderAfterPreamble(t *testing.T) {
	f := newFixture(t, NewPi(""))
	f.set(scenario{Version: "1.5.0", ModelsOut: "\nWarning: loading provider catalog\nprovider model context max-out thinking images\nopenai gpt-test 128K 16K yes yes\n"})
	models, err := f.adapter.ListModels(context.Background())
	if err != nil || len(models) != 2 || models[1].ID != "openai/gpt-test" {
		t.Fatalf("models = %+v, error = %v", models, err)
	}
}

func TestPiBoundSkills(t *testing.T) {
	skills := []runtime.Skill{{Name: "public-pdf", Files: []runtime.SkillFile{{Path: "SKILL.md", Content: []byte("---\nname: public-pdf\ndescription: test\n---\nRead refs/check.txt")}, {Path: "refs/check.txt", Content: []byte("PUBLIC_SKILL_REFERENCE")}}}}
	root := t.TempDir()
	inv, err := buildPi(NewPi(""), runtime.RunRequest{Skills: skills}, nil, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "skills", "public-pdf", "SKILL.md")
	if !strings.Contains(strings.Join(inv.args, " "), "--skill "+path) {
		t.Fatalf("args=%v", inv.args)
	}
	b, err := os.ReadFile(filepath.Join(root, "skills", "public-pdf", "refs/check.txt"))
	if err != nil || string(b) != "PUBLIC_SKILL_REFERENCE" {
		t.Fatalf("reference=%q err=%v", b, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v %v", info, err)
	}
	f := newFixture(t, NewPi(""))
	f.set(scenario{Stdout: piSuccess()})
	result := f.run(runtime.RunRequest{Skills: skills, Prompt: "read bound skill"})
	if result.result.Status != runtime.StatusSucceeded {
		t.Fatal(result.result)
	}
	args := f.record().Args
	for i, arg := range args {
		if arg == "--skill" {
			if _, err := os.Stat(args[i+1]); !os.IsNotExist(err) {
				t.Fatalf("skill survived completion: %v", err)
			}
		}
	}
}
