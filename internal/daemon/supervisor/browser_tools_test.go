package supervisor

import (
	"context"
	"encoding/json"
	"testing"

	localbrowser "openfox/internal/daemon/browser"
	"openfox/internal/runtime"
)

func TestBrowserToolsComposeWithExistingToolsAndReadOnlyMetadata(t *testing.T) {
	runContext := RunContext{Runtime: "codex", ActorID: "owner", ConversationID: "conversation", FileComments: true, SocialTools: true, CrewDelegate: true, SpaceTools: &RunSpaceTools{}, Task: &RunTask{}}
	base := append(taskTools(runContext), publishTool())
	previous := func(context.Context, string, string, json.RawMessage) runtime.ToolResult {
		return runtime.TextResult("existing", true)
	}
	run := localbrowser.Run{ActorID: "owner", ConversationID: "conversation", Generation: 1}
	broker := localbrowser.New(nil, "device")
	tools, call, prompt := withBrowserTools(broker, run, runContext, base, previous)
	if len(tools) != 19 || len(tools) > runtime.MaxTools || prompt == "" {
		t.Fatalf("tools=%d limit=%d", len(tools), runtime.MaxTools)
	}
	if got := call(context.Background(), publishToolName, "call", json.RawMessage(`{}`)); got.Text != "existing" {
		t.Fatal(got)
	}
	if got := call(context.Background(), "openfox_browser_tabs", "call", json.RawMessage(`{"action":"list"}`)); got.OK || got.Text != "UNAVAILABLE" {
		t.Fatal(got)
	}
	for _, mode := range []string{"READ_ONLY", "PLAN"} {
		runContext.ApprovalMode, runContext.CollaborationMode = mode, mode
		tools, _, _ = withBrowserTools(broker, run, runContext, nil, nil)
		if len(tools) != 3 {
			t.Fatal(len(tools))
		}
		for _, tool := range tools {
			if !tool.MCP || !tool.ReadOnly || tool.Name == "openfox_browser_act" {
				t.Fatalf("unsafe readonly tool: %s", tool.Name)
			}
			if !json.Valid(tool.InputSchema) {
				t.Fatalf("invalid schema: %s", tool.Name)
			}
		}
	}
	runContext.AutomationOrigin = true
	if tools, _, _ := withBrowserTools(broker, run, runContext, nil, nil); len(tools) != 0 {
		t.Fatal("automation received browser tools")
	}
	runContext.AutomationOrigin = false
	runContext.Runtime = "pi"
	if tools, _, _ := withBrowserTools(broker, run, runContext, nil, nil); len(tools) != 0 {
		t.Fatal("unsupported runtime received browser tools")
	}
}
