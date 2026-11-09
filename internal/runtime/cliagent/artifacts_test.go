package cliagent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

// 发布工具经 MCP 桥声明；Claude Code 用 --add-dir 放行产物目录（只读档位与 PLAN 不加）；pi 不支持工具。
func TestSupportsToolsPerProtocol(t *testing.T) {
	for _, tc := range []struct {
		adapter *Adapter
		want    bool
	}{{NewClaudeCode(""), true}, {NewOpenCode(""), true}, {NewPi(""), false}} {
		if got := tc.adapter.SupportsTools(); got != tc.want {
			t.Errorf("%s SupportsTools = %v", tc.adapter.Name(), got)
		}
	}
}

func TestClaudeAddsArtifactDirectory(t *testing.T) {
	dir := "/state/run-artifacts/run-1"
	cases := map[[2]string]bool{
		{"", ""}:                       true,
		{"WORKSPACE_WRITE", ""}:        true,
		{"FULL_ACCESS", ""}:            true,
		{"READ_ONLY", ""}:              false,
		{"WORKSPACE_WRITE", "PLAN"}:    false,
		{"WORKSPACE_WRITE", "DEFAULT"}: true,
	}
	for modes, want := range cases {
		got, err := buildClaude(nil, runtime.RunRequest{Prompt: "p", ApprovalMode: modes[0], CollaborationMode: modes[1], ArtifactDirectory: dir}, nil, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Join(got.args, " ")
		if has := strings.Contains(args, "--add-dir "+dir); has != want {
			t.Errorf("%v: args = %v", modes, got.args)
		}
	}
	// 没有产物目录时参数与之前一致（不出现 --add-dir）。
	got, _ := buildClaude(nil, runtime.RunRequest{Prompt: "p"}, nil, t.TempDir(), nil)
	if strings.Contains(strings.Join(got.args, " "), "--add-dir") {
		t.Fatalf("args = %v", got.args)
	}
}

func TestClaudePublishToolThroughBridge(t *testing.T) {
	f := newFixture(t, NewClaudeCode(""))
	f.set(scenario{Stdout: claudeSuccess(), MCPFrom: "claude", MCPTool: "openfox_publish_artifact"})
	var called []string
	outcome := f.run(runtime.RunRequest{
		Tools: []runtime.Tool{{Name: "openfox_publish_artifact", Description: "deliver a file",
			InputSchema: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}},
		ToolCall: func(_ context.Context, name, _ string, arguments json.RawMessage) (string, bool) {
			called = append(called, name)
			return "Artifact not published: refused: the path is outside the managed run roots.", false
		},
		ArtifactDirectory: f.dir,
	})
	if outcome.result.Status != runtime.StatusSucceeded || len(called) != 1 || called[0] != "openfox_publish_artifact" {
		t.Fatalf("result=%+v called=%v", outcome.result, called)
	}
	raw, _ := os.ReadFile(f.dir + "/record.json.mcp")
	for _, want := range []string{"openfox_publish_artifact", "outside the managed run roots", `"isError":true`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("mcp transcript lacks %q:\n%s", want, raw)
		}
	}
	if args := strings.Join(f.record().Args, " "); !strings.Contains(args, "--add-dir "+f.dir) || !strings.Contains(args, "mcp__openfox") {
		t.Fatalf("args = %s", args)
	}
}
