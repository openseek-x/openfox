package cliagent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"openfox/internal/runtime"
)

func TestClaudeProtocolTrafficExceedingEightMiBCompletes(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	frames := claudeImageResult(300000)
	for i := 0; i < 20; i++ {
		frames = append(frames[:len(frames)-1], frames[2], frames[len(frames)-1])
	}
	var bytes int
	for _, frame := range frames {
		bytes += len(frame)
	}
	if bytes <= runtime.DefaultMaxOutput {
		t.Fatal("fixture does not exceed former limit")
	}
	f.set(scenario{Stdout: frames})
	for _, retainedBudget := range []int{0, 1024} {
		var captured []string
		out := f.run(runtime.RunRequest{MaxOutputBytes: retainedBudget, RecordOutput: func(_, _, text string) { captured = append(captured, text) }})
		if out.result.Status != runtime.StatusSucceeded || strings.TrimSpace(out.result.Output) != "Image checked" || !out.result.ProcessExited {
			t.Fatalf("large protocol run: %+v", out.result)
		}
		for _, text := range captured {
			if strings.Contains(text, strings.Repeat("A", 128)) {
				t.Fatal("image data entered output archive")
			}
		}
	}
}

func TestClaudeLargeTextArchivedBeforePreviewClipping(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	text := "HEAD\n" + strings.Repeat("中文🙂\n", 12000) + "\nTAIL"
	frames := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"large-tool","name":"Bash","input":{"command":"fixture"}}]}}`,
		j(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "large-tool", "content": text}}}}),
		j(map[string]any{"type": "result", "subtype": "success", "result": strings.Repeat("回复🙂", 50000)}),
	}
	f.set(scenario{Stdout: frames})
	var tool, reply string
	out := f.run(runtime.RunRequest{MaxOutputBytes: 1024, RecordOutput: func(kind, id, value string) {
		if id == "large-tool" {
			tool = value
		}
		if kind == "reply" {
			reply = value
		}
	}})
	if out.result.Status != runtime.StatusSucceeded || len(out.result.Output) > 1024 || !utf8.ValidString(out.result.Output) || !strings.Contains(out.result.Output, runtime.OutputTruncationNotice) {
		t.Fatalf("bounded reply status=%s bytes=%d", out.result.Status, len(out.result.Output))
	}
	step, ok := out.finalStep("large-tool")
	if !ok || !step.Truncated || step.Output == nil || len(*step.Output) > stepOutputLimit || !strings.HasSuffix(*step.Output, "TAIL") {
		t.Fatal("tool preview missing or invalid")
	}
	if tool != text || len(reply) <= runtime.DefaultOutputPreviewBytes {
		t.Fatal("archive did not receive complete decoded text")
	}
}

func TestClaudeEventLimitStillStopsExecution(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeImageResult(100)})
	out := f.run(runtime.RunRequest{MaxEvents: 1})
	if out.result.Status != runtime.StatusFailed || out.result.ErrorCode != "EVENT_LIMIT" || !out.result.ProcessExited {
		t.Fatalf("event limit not enforced: %+v", out.result)
	}
}
