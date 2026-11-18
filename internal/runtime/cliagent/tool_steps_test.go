package cliagent

import (
	"encoding/json"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func decodeToolStep(t *testing.T, event runtime.Event) runtime.Step {
	t.Helper()
	var body struct {
		Step runtime.Step `json:"step"`
	}
	if err := json.Unmarshal(event.Payload, &body); err != nil {
		t.Fatal(err)
	}
	return body.Step
}

func TestGenericToolStepLifecycle(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return strings.ReplaceAll(s, "known-secret", "[REDACTED]") })
	input := map[string]any{"query": "known-secret", "nested": map[string]any{"password": "another-secret"}}
	started := s.startStep("tool1", s.toolStep("mcp__projects__list", input))
	step := decodeToolStep(t, started[0])
	if step.Kind != runtime.StepKindToolCall || step.ToolName != "mcp__projects__list" || step.Command != "" || strings.Contains(step.ToolInput, "secret") {
		t.Fatalf("start = %+v", step)
	}
	expectedInput := step.ToolInput
	finished := s.finishStep("tool1", runtime.Step{Kind: runtime.StepKindToolCall}, runtime.StepFailed, "known-secret error", nil)
	step = decodeToolStep(t, finished[0])
	if step.ToolInput != expectedInput || step.ToolName != "mcp__projects__list" || step.State != runtime.StepFailed || step.Output == nil || *step.Output != "[REDACTED] error" {
		t.Fatalf("finish = %+v", step)
	}
	s.startStep("tool2", s.toolStep("unknown", input))
	events := s.turnEnd("cancelled")
	step = decodeToolStep(t, events[0])
	if step.Kind != runtime.StepKindToolCall || step.State != runtime.StepInterrupted || step.ToolName != "unknown" || step.ToolInput != expectedInput {
		t.Fatalf("interrupted = %+v", step)
	}
}

func TestGenericToolFallbackAndSpecializations(t *testing.T) {
	s := newSession(protocol{}, "/w", func(s string) string { return s })
	events, _ := parsePi(s, []byte(j(map[string]any{"type": "tool_execution_end", "toolCallId": "no-start", "toolName": "project_lookup", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}})))
	step := decodeToolStep(t, events[0])
	if step.Kind != runtime.StepKindToolCall || step.ToolName != "project_lookup" || step.ToolInput != "" || step.Output == nil || *step.Output != "done" {
		t.Fatalf("no start = %+v", step)
	}
	for _, pair := range []struct{ name, kind string }{{"bash", runtime.StepKindExec}, {"read", runtime.StepKindRead}, {"grep", runtime.StepKindSearch}, {"write", runtime.StepKindWriteFile}} {
		step := s.toolStep(pair.name, map[string]any{"command": "echo ok", "path": "/w/a", "pattern": "x"})
		if step.Kind != pair.kind || step.ToolName != "" {
			t.Fatalf("specialized = %+v", step)
		}
	}
}

func TestToolArgumentsPreserveNumbersAcrossProtocols(t *testing.T) {
	const input = `{"id":9007199254740993,"nested":{"ratio":0.1234567890123456789},"ids":[9007199254740995]}`
	cases := []struct {
		name  string
		parse func(*session, []byte) ([]runtime.Event, bool)
		frame string
	}{
		{"claude", parseClaude, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c1","name":"crm_lookup","input":` + input + `}]}}`},
		{"codebuddy", parseCodeBuddy, `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c1","name":"crm_lookup","input":` + input + `}]}}`},
		{"pi", parsePi, `{"type":"tool_execution_start","toolCallId":"c1","toolName":"crm_lookup","args":` + input + `}`},
		{"opencode", parseOpenCode, `{"type":"tool_use","part":{"type":"tool","tool":"crm_lookup","callID":"c1","state":{"status":"completed","input":` + input + `,"output":"found"}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(protocol{}, "/w", func(s string) string { return s })
			events, _ := tc.parse(s, []byte(tc.frame))
			var preview string
			for _, event := range events {
				if event.Type != runtime.EventStep {
					continue
				}
				step := decodeToolStep(t, event)
				if step.Kind == runtime.StepKindToolCall {
					preview = step.ToolInput
				}
			}
			for _, number := range []string{"9007199254740993", "0.1234567890123456789", "9007199254740995"} {
				if !strings.Contains(preview, number) {
					t.Fatalf("tool parameter precision lost: wanted %s, preview %s", number, preview)
				}
			}
		})
	}
}
