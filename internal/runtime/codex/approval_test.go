package codex

import (
	"encoding/json"
	"strings"
	"testing"
)

// FOLLOW_RUNTIME（以及空串）的 thread/start 与 turn/start 沙箱参数必须保持原有默认行为。
func TestFollowRuntimeSandboxParamsAreUnchanged(t *testing.T) {
	for _, mode := range []string{"", "FOLLOW_RUNTIME", "WORKSPACE_WRITE"} {
		session := &appServerSession{cwd: "/work", networkAccess: true, approvalMode: mode}
		thread, _ := json.Marshal(session.threadStartParams())
		if string(thread) != `{"approvalPolicy":"never","cwd":"/work","ephemeral":true,"sandbox":"workspace-write"}` {
			t.Fatalf("%q thread/start = %s", mode, thread)
		}
		policy, _ := json.Marshal(session.sandboxPolicy())
		want := `{"excludeSlashTmp":false,"excludeTmpdirEnvVar":false,"networkAccess":true,"type":"workspaceWrite","writableRoots":["/work"]}`
		if string(policy) != want {
			t.Fatalf("%q sandboxPolicy = %s", mode, policy)
		}
	}
}

func TestSandboxPerApprovalMode(t *testing.T) {
	cases := map[string]struct{ sandbox, policy string }{
		"READ_ONLY":   {"read-only", `{"type":"readOnly"}`},
		"FULL_ACCESS": {"danger-full-access", `{"type":"dangerFullAccess"}`},
	}
	for mode, want := range cases {
		session := &appServerSession{cwd: "/work", networkAccess: true, approvalMode: mode}
		params := session.threadStartParams()
		if params["sandbox"] != want.sandbox || params["approvalPolicy"] != "never" {
			t.Fatalf("%s thread/start = %v", mode, params)
		}
		policy, _ := json.Marshal(session.sandboxPolicy())
		if string(policy) != want.policy {
			t.Fatalf("%s sandboxPolicy = %s", mode, policy)
		}
	}
	// 未知档位按最严格处理（失败即安全），不会落到更宽的 workspace-write。
	if got := sandboxMode("SOMETHING_NEW"); got != "read-only" {
		t.Fatalf("unknown mode sandbox = %s", got)
	}
}

func TestIsolatedConfigNeverWiderThanRequest(t *testing.T) {
	if isolatedConfigFor("") != isolatedConfig || isolatedConfigFor("FOLLOW_RUNTIME") != isolatedConfig || isolatedConfigFor("WORKSPACE_WRITE") != isolatedConfig {
		t.Fatal("FOLLOW_RUNTIME config must be unchanged")
	}
	if !strings.Contains(isolatedConfigFor("READ_ONLY"), `sandbox_mode = "read-only"`) || strings.Contains(isolatedConfigFor("READ_ONLY"), `"workspace-write"`) {
		t.Fatalf("READ_ONLY config = %s", isolatedConfigFor("READ_ONLY"))
	}
	if !strings.Contains(isolatedConfigFor("FULL_ACCESS"), `sandbox_mode = "danger-full-access"`) {
		t.Fatalf("FULL_ACCESS config = %s", isolatedConfigFor("FULL_ACCESS"))
	}
	if !strings.Contains(isolatedConfigFor("READ_ONLY"), `approval_policy = "never"`) {
		t.Fatal("approval policy must stay never")
	}
}
