package agents

import "testing"

// Agent 默认权限模式限定为四档；小写会被规范化，形状合法但不在四档内的值（旧正则放行）现在被拒绝。
func TestNormalizeRunSettingsApprovalModes(t *testing.T) {
	for input, want := range map[string]string{
		"": "FOLLOW_RUNTIME", "follow_runtime": "FOLLOW_RUNTIME", "READ_ONLY": "READ_ONLY",
		"workspace_write": "WORKSPACE_WRITE", "FULL_ACCESS": "FULL_ACCESS",
	} {
		got, err := normalizeRunSettings(RunSettings{ApprovalMode: input})
		if err != nil || got.ApprovalMode != want {
			t.Fatalf("%q -> %q, %v", input, got.ApprovalMode, err)
		}
	}
	for _, input := range []string{"ON_REQUEST", "UNTRUSTED", "bad mode", "FULL"} {
		if _, err := normalizeRunSettings(RunSettings{ApprovalMode: input}); err != ErrInvalidConfig {
			t.Fatalf("%q accepted: %v", input, err)
		}
	}
}
