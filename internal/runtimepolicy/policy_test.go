package runtimepolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

func ptr(value string) *string { return &value }

func TestEffective(t *testing.T) {
	agent := Config{Runtime: "codex", Model: "gpt-5.1", ApprovalMode: FollowRuntime, ReasoningEffort: ReasoningDefault}
	cases := []struct {
		name      string
		overrides Overrides
		want      Config
	}{
		{"没有覆盖沿用 Agent 配置", Overrides{}, agent},
		{"只覆盖 Runtime：模型落到 default", Overrides{Runtime: ptr("claude-code")}, Config{"claude-code", DefaultModel, FollowRuntime, ReasoningDefault}},
		{"覆盖成与 Agent 相同的 Runtime：模型不变", Overrides{Runtime: ptr("codex")}, agent},
		{"Runtime 与模型一起覆盖", Overrides{Runtime: ptr("claude-code"), Model: ptr("sonnet")}, Config{"claude-code", "sonnet", FollowRuntime, ReasoningDefault}},
		{"只覆盖模型", Overrides{Model: ptr("gpt-5.2")}, Config{"codex", "gpt-5.2", FollowRuntime, ReasoningDefault}},
		{"只覆盖权限", Overrides{ApprovalMode: ptr(ReadOnly)}, Config{"codex", "gpt-5.1", ReadOnly, ReasoningDefault}},
		{"只覆盖思考深度", Overrides{ReasoningEffort: ptr(ReasoningHigh)}, Config{"codex", "gpt-5.1", FollowRuntime, ReasoningHigh}},
	}
	for _, item := range cases {
		if got := Effective(agent, item.overrides); got != item.want {
			t.Errorf("%s: got %+v want %+v", item.name, got, item.want)
		}
	}
	if got := Effective(Config{Runtime: "codex", Model: "m"}, Overrides{}); got.ApprovalMode != FollowRuntime {
		t.Fatalf("empty approval mode = %q", got.ApprovalMode)
	}
}

func TestValidators(t *testing.T) {
	for _, mode := range ApprovalModes {
		if !ValidApprovalMode(mode) {
			t.Fatalf("%s should be valid", mode)
		}
	}
	for _, mode := range []string{"", "read_only", "YOLO", "FOLLOW_RUNTIME "} {
		if ValidApprovalMode(mode) {
			t.Fatalf("%q should be invalid", mode)
		}
	}
	if !ValidProvider("claude-code") || ValidProvider("gemini") || ValidProvider("") {
		t.Fatal("provider validation")
	}
	if !ValidModel("default") || ValidModel("") || ValidModel(strings.Repeat("a", 129)) || ValidModel("a\nb") || ValidModel(" a") {
		t.Fatal("model validation")
	}
}

func TestParseCapabilitiesAndSupport(t *testing.T) {
	raw := []byte(`{"runtimes":{
		"codex":{"runnable":true,"models":[{"id":"gpt-5.1"}],"approval_modes":["FOLLOW_RUNTIME","READ_ONLY","BOGUS"]},
		"pi":{"runnable":true},
		"broken":"x"}}`)
	parsed := ParseCapabilities(raw)
	codex, ok := Find(parsed, "codex")
	if !ok || !codex.Runnable || !codex.SupportsApprovalMode(ReadOnly) || codex.SupportsApprovalMode(FullAccess) || !codex.SupportsApprovalMode(FollowRuntime) {
		t.Fatalf("codex = %+v", codex)
	}
	if !codex.HasModel("default") || !codex.HasModel("gpt-5.1") || codex.HasModel("other") {
		t.Fatal("model list check")
	}
	pi, _ := Find(parsed, "pi")
	// 旧版后台服务没有 approval_modes：只支持 FOLLOW_RUNTIME；没有模型列表时只校验格式。
	if pi.SupportsApprovalMode(ReadOnly) || !pi.SupportsApprovalMode(FollowRuntime) || !pi.SupportsApprovalMode("") || !pi.HasModel("anything") {
		t.Fatalf("pi = %+v", pi)
	}
	if _, found := Find(parsed, "broken"); found {
		t.Fatal("malformed entry should be skipped")
	}
	if len(ParseCapabilities([]byte(`not json`))) != 0 || len(ParseCapabilities(nil)) != 0 {
		t.Fatal("garbage input should parse to nothing")
	}
	withError := ParseCapabilities([]byte(`{"runtimes":{"codex":{"models":[{"id":"a"}],"models_error":"boom"}}}`))
	if c, _ := Find(withError, "codex"); !c.HasModel("zzz") {
		t.Fatal("models_error should only check the format")
	}
}

func TestNormalizeApprovalModes(t *testing.T) {
	if got := strings.Join(NormalizeApprovalModes(nil), ","); got != "FOLLOW_RUNTIME" {
		t.Fatalf("nil = %s", got)
	}
	if got := strings.Join(NormalizeApprovalModes([]string{"FULL_ACCESS", "READ_ONLY", "READ_ONLY", "X"}), ","); got != "FOLLOW_RUNTIME,READ_ONLY,FULL_ACCESS" {
		t.Fatalf("got %s", got)
	}
}

func TestParseCapabilitiesModelInfo(t *testing.T) {
	raw := json.RawMessage(`{"runtimes":{"codex":{"runnable":true,"models":[{"id":"gpt-5.1","display_name":"GPT","is_default":true},{"id":"o3"}]}}}`)
	found, ok := Find(ParseCapabilities(raw), "codex")
	if !ok || len(found.ModelInfo) != 2 || len(found.Models) != 2 {
		t.Fatalf("capability = %+v", found)
	}
	if found.ModelInfo[0].DisplayName != "GPT" || !found.ModelInfo[0].IsDefault {
		t.Fatalf("model 0 = %+v", found.ModelInfo[0])
	}
	// 显示名缺省时回退为 ID。
	if found.ModelInfo[1].DisplayName != "o3" || found.ModelInfo[1].IsDefault {
		t.Fatalf("model 1 = %+v", found.ModelInfo[1])
	}
}
