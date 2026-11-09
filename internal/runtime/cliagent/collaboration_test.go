package cliagent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"openfox/internal/runtime"
)

func claudeFlags(t *testing.T, approval, collaboration string) (args []string, flags map[string]string) {
	t.Helper()
	got, err := buildClaude(nil, runtime.RunRequest{Prompt: "p", ApprovalMode: approval, CollaborationMode: collaboration}, nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	flags = map[string]string{}
	for index := 0; index+1 < len(got.args); index++ {
		if strings.HasPrefix(got.args[index], "--") {
			flags[got.args[index]] = got.args[index+1]
		}
	}
	return got.args, flags
}

// Claude Code 三种协作模式 × 四档权限。
//   - PLAN：任何权限档位下都是 --permission-mode plan + READ_ONLY 的工具范围（FULL_ACCESS 也不例外）；
//   - DEFAULT 与 FOLLOW_RUNTIME：参数与只设权限档位时逐字一致，READ_ONLY 仍是 plan（DEFAULT 不放宽只读）。
func TestClaudeCollaborationModeArgs(t *testing.T) {
	for _, approval := range []string{"", "FOLLOW_RUNTIME", "READ_ONLY", "WORKSPACE_WRITE", "FULL_ACCESS"} {
		base, _ := claudeFlags(t, approval, "")
		for _, collaboration := range []string{"FOLLOW_RUNTIME", "FOLLOW_AGENT", "DEFAULT"} {
			if args, _ := claudeFlags(t, approval, collaboration); !reflect.DeepEqual(args, base) {
				t.Fatalf("%s/%s args = %v, want %v", approval, collaboration, args, base)
			}
		}
		_, plan := claudeFlags(t, approval, "PLAN")
		if plan["--permission-mode"] != "plan" || plan["--allowedTools"] != claudeReadOnlyTools || plan["--disallowedTools"] != claudeWriteTools {
			t.Fatalf("%s/PLAN flags = %v", approval, plan)
		}
	}
	if _, flags := claudeFlags(t, "READ_ONLY", "DEFAULT"); flags["--permission-mode"] != "plan" || flags["--disallowedTools"] != claudeWriteTools {
		t.Fatalf("DEFAULT must not widen READ_ONLY: %v", flags)
	}
	if _, flags := claudeFlags(t, "FULL_ACCESS", "DEFAULT"); flags["--permission-mode"] != "bypassPermissions" {
		t.Fatalf("FULL_ACCESS/DEFAULT = %v", flags)
	}
}

func TestClaudeAdvertisesCollaborationModes(t *testing.T) {
	if got := strings.Join(NewClaudeCode("").CollaborationModes(), ","); got != "FOLLOW_RUNTIME,DEFAULT,PLAN" {
		t.Fatalf("claude collaboration modes = %s", got)
	}
}

// OpenCode 与 pi 在仓库代码和已有记录里都没有可确认的计划模式调用方式：只支持 FOLLOW_RUNTIME，
// DEFAULT / PLAN 在任何探测和进程启动之前失败；FOLLOW_RUNTIME 的参数与之前逐字一致。
func TestOpenCodeAndPiRejectCollaborationModes(t *testing.T) {
	for name, adapter := range map[string]*Adapter{"opencode": NewOpenCode("/nonexistent/opencode"), "pi": NewPi("/nonexistent/pi")} {
		if got := strings.Join(adapter.CollaborationModes(), ","); got != "FOLLOW_RUNTIME" {
			t.Fatalf("%s collaboration modes = %s", name, got)
		}
		for _, mode := range []string{"DEFAULT", "PLAN"} {
			_, err := adapter.Start(context.Background(), runtime.RunRequest{Prompt: "p", CollaborationMode: mode})
			if !errors.Is(err, runtime.ErrUnsupported) {
				t.Fatalf("%s %s: err = %v", name, mode, err)
			}
		}
	}
	base, _ := buildOpenCode(nil, runtime.RunRequest{Prompt: "p"}, nil, "", nil)
	follow, _ := buildOpenCode(nil, runtime.RunRequest{Prompt: "p", CollaborationMode: "FOLLOW_RUNTIME"}, nil, "", nil)
	if !reflect.DeepEqual(base, follow) {
		t.Fatalf("opencode FOLLOW_RUNTIME changed: %+v vs %+v", follow, base)
	}
}
