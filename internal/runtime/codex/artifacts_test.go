package codex

import (
	"encoding/json"
	"strings"
	"testing"

	domainruntime "openfox/internal/runtime"
)

// 产物目录加入 workspace-write 的可写根；发布工具经 dynamicTools 声明；适配器报告支持工具。
func TestSandboxPolicyAddsArtifactDirectory(t *testing.T) {
	for _, mode := range []string{"", "FOLLOW_RUNTIME", "WORKSPACE_WRITE"} {
		session := &appServerSession{cwd: "/work", networkAccess: true, approvalMode: mode, artifactDir: "/state/run-artifacts/run-1"}
		policy, _ := json.Marshal(session.sandboxPolicy())
		if !strings.Contains(string(policy), `"writableRoots":["/work","/state/run-artifacts/run-1"]`) {
			t.Fatalf("%q sandboxPolicy = %s", mode, policy)
		}
	}
	// 只读与完全访问不变；产物目录与工作目录相同时不重复。
	for mode, want := range map[string]string{"READ_ONLY": `{"type":"readOnly"}`, "FULL_ACCESS": `{"type":"dangerFullAccess"}`} {
		session := &appServerSession{cwd: "/work", approvalMode: mode, artifactDir: "/state/a"}
		if policy, _ := json.Marshal(session.sandboxPolicy()); string(policy) != want {
			t.Fatalf("%s sandboxPolicy = %s", mode, policy)
		}
	}
	same := &appServerSession{cwd: "/work", artifactDir: "/work"}
	if policy, _ := json.Marshal(same.sandboxPolicy()); !strings.Contains(string(policy), `"writableRoots":["/work"]`) {
		t.Fatalf("same dir sandboxPolicy = %s", policy)
	}
}

func TestThreadStartDeclaresPublishTool(t *testing.T) {
	session := newTestSession()
	session.tools = []domainruntime.Tool{{Name: "openfox_publish_artifact", Description: "deliver", InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}}
	params, _ := json.Marshal(session.threadStartParams())
	if !strings.Contains(string(params), `"dynamicTools":[{"description":"deliver","inputSchema":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]},"name":"openfox_publish_artifact","type":"function"}]`) {
		t.Fatalf("thread/start = %s", params)
	}
	if !(&Adapter{}).SupportsTools() {
		t.Fatal("codex adapter must support tools")
	}
}

func TestRunRequestArtifactDirectoryValidation(t *testing.T) {
	for _, dir := range []string{"relative/dir", "/a/../b", "/a\nb"} {
		if _, err := (domainruntime.RunRequest{Prompt: "p", ArtifactDirectory: dir}).Normalize(); err == nil {
			t.Errorf("%q accepted", dir)
		}
	}
	if _, err := (domainruntime.RunRequest{Prompt: "p", ArtifactDirectory: "/state/run-artifacts/r"}).Normalize(); err != nil {
		t.Fatal(err)
	}
}
