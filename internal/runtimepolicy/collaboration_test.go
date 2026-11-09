package runtimepolicy

import (
	"strings"
	"testing"
)

// 协作模式取值、规范化与心跳解析。
func TestCollaborationModeValues(t *testing.T) {
	for _, mode := range []string{"FOLLOW_RUNTIME", "DEFAULT", "PLAN"} {
		if !ValidCollaborationMode(mode) {
			t.Fatalf("%s should be valid", mode)
		}
	}
	for _, mode := range []string{"", "FOLLOW_AGENT", "plan", "READ_ONLY", "AUTO"} {
		if ValidCollaborationMode(mode) {
			t.Fatalf("%q should be invalid", mode)
		}
	}
	for input, want := range map[string]string{"": "FOLLOW_RUNTIME", "FOLLOW_AGENT": "FOLLOW_RUNTIME", "PLAN": "PLAN", "X": "X"} {
		if got := NormalizeCollaborationMode(input); got != want {
			t.Fatalf("normalize %q = %q", input, got)
		}
	}
	if got := strings.Join(NormalizeCollaborationModes(nil), ","); got != "FOLLOW_RUNTIME" {
		t.Fatalf("nil = %s", got)
	}
	if got := strings.Join(NormalizeCollaborationModes([]string{"PLAN", "X", "DEFAULT", "PLAN"}), ","); got != "FOLLOW_RUNTIME,DEFAULT,PLAN" {
		t.Fatalf("got %s", got)
	}
}

func TestParseCapabilitiesCollaborationModes(t *testing.T) {
	raw := []byte(`{"runtimes":{
		"claude-code":{"runnable":true,"approval_modes":["FOLLOW_RUNTIME"],"collaboration_modes":["FOLLOW_RUNTIME","DEFAULT","PLAN","BOGUS"]},
		"codex":{"runnable":true,"approval_modes":["FOLLOW_RUNTIME","READ_ONLY"]}}}`)
	parsed := ParseCapabilities(raw)
	claude, _ := Find(parsed, "claude-code")
	if strings.Join(claude.CollaborationModes, ",") != "FOLLOW_RUNTIME,DEFAULT,PLAN" ||
		!claude.SupportsCollaborationMode("PLAN") || !claude.SupportsCollaborationMode("DEFAULT") {
		t.Fatalf("claude = %+v", claude)
	}
	// 旧版后台服务没有 collaboration_modes：只支持 FOLLOW_RUNTIME（以及等价的空串与 FOLLOW_AGENT）。
	codex, _ := Find(parsed, "codex")
	if codex.SupportsCollaborationMode("PLAN") || codex.SupportsCollaborationMode("DEFAULT") ||
		!codex.SupportsCollaborationMode("FOLLOW_RUNTIME") || !codex.SupportsCollaborationMode("") ||
		!codex.SupportsCollaborationMode("FOLLOW_AGENT") {
		t.Fatalf("codex = %+v", codex)
	}
	// 未知值永远不支持（派发时失败，而不是当作 FOLLOW_RUNTIME 执行）。
	if claude.SupportsCollaborationMode("BOGUS") {
		t.Fatal("unknown mode must not be supported")
	}
}
