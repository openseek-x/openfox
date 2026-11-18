package cliagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// Explicit opt-in. The only model input is a controlled screenshot fixture;
// normal tests neither contact providers nor use personal credentials.
func TestRealBrowserImageTools(t *testing.T) {
	if os.Getenv("OPENFOX_BROWSER_REAL_IMAGE") != "1" {
		t.Skip("explicit model integration test")
	}
	data, err := os.ReadFile(os.Getenv("OPENFOX_BROWSER_IMAGE"))
	if err != nil {
		t.Fatal("fixture image unavailable")
	}
	for _, name := range []string{NameClaudeCode, NameOpenCode} {
		t.Run(name, func(t *testing.T) {
			adapter := New(name, "")
			adapter.StateRoot = t.TempDir()
			var calls atomic.Int32
			start := time.Now()
			execution, err := adapter.Start(context.Background(), runtime.RunRequest{
				WorkingDirectory: t.TempDir(), ApprovalMode: "READ_ONLY", Timeout: 90 * time.Second,
				RequireTools: true,
				Prompt:       "Call inspect_fixture exactly once. The tool returns a screenshot. Read the verification code printed inside the image and identify the left and right shapes and their colors. Reply with one JSON object using keys code, left_color, left_shape, right_color, right_shape, using English color/shape names. Use only this tool; do not run shell commands or open files. If the image is inaccessible say so, never guess.",
				Tools:        []runtime.Tool{{Name: "inspect_fixture", Description: "Read the test browser screenshot.", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), MCP: true, ReadOnly: true}},
				ToolCall: func(_ context.Context, tool, _ string, _ json.RawMessage) runtime.ToolResult {
					if tool != "inspect_fixture" || calls.Add(1) != 1 {
						return runtime.TextResult("unavailable", false)
					}
					return runtime.ToolResult{OK: true, Text: "Browser screenshot follows.", Image: &runtime.ToolImage{MIMEType: "image/png", Data: data}}
				},
			})
			if err != nil {
				t.Fatalf("adapter start: %v", err)
			}
			for event := range execution.Events() {
				if strings.Contains(string(event.Payload), `"mimeType":"image/png"`) || strings.Contains(string(event.Payload), `"type":"image"`) {
					t.Error("image content leaked into execution events")
				}
			}
			result, err := execution.Wait(context.Background())
			if err != nil || result.Status != runtime.StatusSucceeded {
				t.Fatalf("model run failed: status=%s error=%v code=%s tool_calls=%d detail=%s", result.Status, err, result.ErrorCode, calls.Load(), runtime.OutputPreview(result.Error, 1000))
			}
			for _, want := range []string{strings.ToLower(os.Getenv("OPENFOX_BROWSER_EXPECT_CODE")), "red", "circle", "blue", "square"} {
				if want == "" || !strings.Contains(strings.ToLower(result.Output), want) {
					t.Fatalf("model did not identify the screenshot: %s", runtime.OutputPreview(result.Output, 1000))
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("tool calls: %d", calls.Load())
			}
			if directory := os.Getenv("OPENFOX_BROWSER_EVIDENCE_DIR"); directory != "" {
				record, _ := json.MarshalIndent(map[string]any{"status": "passed", "runtime": name, "transport": "production Go adapter + MCP image", "tool_calls": calls.Load(), "duration_ms": time.Since(start).Milliseconds(), "response": result.Output}, "", "  ")
				if err := os.MkdirAll(directory, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, name+"-image-result.json"), append(record, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
