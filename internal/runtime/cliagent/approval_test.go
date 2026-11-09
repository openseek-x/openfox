package cliagent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

// Both follow-runtime modes retain their permission arguments with explicit MCP isolation.
func TestClaudeFollowRuntimeArgsAreUnchanged(t *testing.T) {
	want := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits",
		"--model", "sonnet", "--strict-mcp-config", "--allowedTools", claudeAllowedTools}
	for _, mode := range []string{"", "FOLLOW_RUNTIME"} {
		got, err := buildClaude(nil, runtime.RunRequest{Prompt: "p", Model: "sonnet", ApprovalMode: mode}, nil, t.TempDir(), nil)
		if err != nil || !reflect.DeepEqual(got.args, want) {
			t.Fatalf("mode %q: args = %v err=%v", mode, got.args, err)
		}
	}
	if claudeAllowedTools != "Bash,Read,Edit,Write,MultiEdit,NotebookEdit,Glob,Grep,WebFetch,WebSearch,TodoWrite,Task" {
		t.Fatal("claudeAllowedTools changed")
	}
}

func TestClaudeApprovalModeMapping(t *testing.T) {
	cases := map[string]struct{ permission, allowed, disallowed string }{
		"READ_ONLY":       {"plan", claudeReadOnlyTools, claudeWriteTools},
		"WORKSPACE_WRITE": {"acceptEdits", claudeAllowedTools, ""},
		"FULL_ACCESS":     {"bypassPermissions", claudeAllowedTools, ""},
	}
	for mode, want := range cases {
		got, err := buildClaude(nil, runtime.RunRequest{Prompt: "p", ApprovalMode: mode}, nil, t.TempDir(), nil)
		if err != nil {
			t.Fatal(err)
		}
		flags := map[string]string{}
		for index := 0; index+1 < len(got.args); index++ {
			if strings.HasPrefix(got.args[index], "--") {
				flags[got.args[index]] = got.args[index+1]
			}
		}
		if flags["--permission-mode"] != want.permission || flags["--allowedTools"] != want.allowed || flags["--disallowedTools"] != want.disallowed {
			t.Fatalf("%s: flags = %v", mode, flags)
		}
	}
	// READ_ONLY 的放行列表里不能出现任何写文件或执行命令的工具。
	for _, tool := range strings.Split(claudeWriteTools, ",") {
		if strings.Contains(","+claudeReadOnlyTools+",", ","+tool+",") {
			t.Fatalf("READ_ONLY allows %s", tool)
		}
	}
}

func TestOpenCodePermissionMapping(t *testing.T) {
	cases := map[string]string{
		"":                `{"edit":"allow","bash":"allow","webfetch":"allow"}`,
		"FOLLOW_RUNTIME":  `{"edit":"allow","bash":"allow","webfetch":"allow"}`,
		"READ_ONLY":       `{"edit":"deny","bash":"deny","webfetch":"allow","external_directory":"deny"}`,
		"WORKSPACE_WRITE": `{"edit":"allow","bash":"allow","webfetch":"allow","external_directory":"deny"}`,
		"FULL_ACCESS":     `{"edit":"allow","bash":"allow","webfetch":"allow","external_directory":"allow"}`,
	}
	for mode, want := range cases {
		got, err := buildOpenCode(nil, runtime.RunRequest{Prompt: "p", ApprovalMode: mode}, nil, "", nil)
		if err != nil || got.env["OPENCODE_PERMISSION"] != want {
			t.Fatalf("%q: permission = %q err=%v", mode, got.env["OPENCODE_PERMISSION"], err)
		}
	}
}

// pi 没有可靠的权限参数：只支持 FOLLOW_RUNTIME，其他档位在任何探测和进程启动之前失败。
func TestPiRejectsNonDefaultApprovalModesBeforeStarting(t *testing.T) {
	adapter := NewPi("/nonexistent/pi")
	if got := strings.Join(adapter.ApprovalModes(), ","); got != "FOLLOW_RUNTIME" {
		t.Fatalf("pi approval modes = %s", got)
	}
	for _, mode := range []string{"READ_ONLY", "WORKSPACE_WRITE", "FULL_ACCESS"} {
		_, err := adapter.Start(context.Background(), runtime.RunRequest{Prompt: "p", ApprovalMode: mode})
		if !errors.Is(err, runtime.ErrUnsupported) {
			t.Fatalf("pi %s: err = %v", mode, err)
		}
	}
	if _, err := adapter.Start(context.Background(), runtime.RunRequest{Prompt: "p", ApprovalMode: "bogus"}); !errors.Is(err, runtime.ErrInvalidRequest) {
		t.Fatalf("unknown mode: err = %v", err)
	}
}

func TestAdaptersAdvertiseSupportedModes(t *testing.T) {
	for name, adapter := range map[string]*Adapter{"claude": NewClaudeCode(""), "opencode": NewOpenCode("")} {
		if got := strings.Join(adapter.ApprovalModes(), ","); got != "FOLLOW_RUNTIME,READ_ONLY,WORKSPACE_WRITE,FULL_ACCESS" {
			t.Fatalf("%s modes = %s", name, got)
		}
	}
}
