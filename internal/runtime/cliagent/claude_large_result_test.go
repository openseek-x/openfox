package cliagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func TestClaudeBridgeAcceptsRepeatedMaxImageWithoutArchiving(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeImageResult(base64.StdEncoding.EncodedLen(runtime.MaxToolImageBytes))})
	var archived strings.Builder
	out := f.run(runtime.RunRequest{
		Tools: []runtime.Tool{{Name: "observe", MCP: true, ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolCall: func(context.Context, string, string, json.RawMessage) runtime.ToolResult {
			return runtime.TextResult("unused", true)
		},
		RecordOutput: func(_, _, text string) { archived.WriteString(text) },
	})
	if out.result.Status != runtime.StatusSucceeded || strings.TrimSpace(out.result.Output) != "Image checked" {
		t.Fatalf("MCP frame limit: %+v", out.result)
	}
	for _, event := range out.events {
		if strings.Contains(string(event.Payload), strings.Repeat("A", 128)) {
			t.Fatal("image base64 leaked into published events")
		}
	}
	if strings.Contains(archived.String(), strings.Repeat("A", 128)) {
		t.Fatal("image base64 leaked into output archive")
	}
}

// Matches the observed Read image result: base64 appears both in message
// content and tool_use_result. No real file, credential or model is used.
func claudeImageResult(size int) []string {
	data := strings.Repeat("A", size)
	return []string{
		`{"type":"system","subtype":"init","session_id":"image-session"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"image-read","name":"Read","input":{"file_path":"/work/screen.png"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"image-read","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}]}]},"tool_use_result":{"type":"image","file":{"base64":"` + data + `","type":"image/png"}}}`,
		`{"type":"result","subtype":"success","result":"Image checked","session_id":"image-session"}`,
	}
}

func TestClaudeLargeImageResultWithinOutputBudget(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeImageResult(296476)})
	out := f.run(runtime.RunRequest{})
	if out.result.Status != runtime.StatusSucceeded || strings.TrimSpace(out.result.Output) != "Image checked" {
		t.Fatalf("large image result: %+v", out.result)
	}
	step, ok := out.finalStep("image-read")
	if !ok || step.Kind != runtime.StepKindRead || step.State != runtime.StepDone {
		t.Fatalf("read did not complete: %+v", step)
	}
	for _, event := range out.events {
		if strings.Contains(string(event.Payload), strings.Repeat("A", 128)) {
			t.Fatal("image base64 leaked into published events")
		}
	}
}

func TestClaudeLargeImageLimitsRemainEnforced(t *testing.T) {
	for _, tc := range []struct {
		name   string
		req    runtime.RunRequest
		frames []string
	}{
		{"explicit-line", runtime.RunRequest{MaxLineBytes: 1024}, claudeImageResult(296476)},
		{"oversized-frame", runtime.RunRequest{}, claudeImageResult(runtime.DefaultMaxOutput / 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, NewClaudeCode(""))
			f.set(scenario{Stdout: tc.frames})
			out := f.run(tc.req)
			if out.result.Status != runtime.StatusFailed || out.result.ErrorCode != "OUTPUT_LIMIT" || !out.result.ProcessExited {
				t.Fatalf("limit not enforced: %+v", out.result)
			}
		})
	}
}
