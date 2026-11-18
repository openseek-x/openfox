package runtimepolicy

import (
	"strings"
	"testing"
)

func TestGeminiModelConfiguration(t *testing.T) {
	env := map[string]string{"GEMINI_API_KEY": "CONTROLLED-R02-KEY", "GEMINI_MODEL": "gemini-fixture-1"}
	for _, tc := range []struct{ selected, want string }{{"explicit-model", "explicit-model"}, {DefaultModel, "gemini-fixture-1"}, {"", "gemini-fixture-1"}} {
		if got, err := ResolveGeminiModel(tc.selected, env); err != nil || got != tc.want {
			t.Fatalf("model precedence: got=%q error=%v", got, err)
		}
	}
	for _, model := range []string{"auto", "default", "../model", "models/name", "https://other.test", "model?key=secret", "-model", "模型", strings.Repeat("a", 129)} {
		if ValidGeminiModel(model) {
			t.Fatalf("accepted non-model identifier %q", model)
		}
	}
	if !ValidGeminiModel(strings.Repeat("a", 128)) || !ValidGeminiModel("gemini-model_1.0") {
		t.Fatal("valid model rejected")
	}
	for _, key := range []string{"", " secret", "secret\n", strings.Repeat("s", 8193)} {
		if _, err := ResolveGeminiModel("fixture-model", map[string]string{"GEMINI_API_KEY": key}); err == nil || strings.Contains(err.Error(), "CONTROLLED-R02-KEY") {
			t.Fatal("invalid key accepted or secret leaked")
		}
	}
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GEMINI_CLI_HOME", "GEMINI_API_KEY_AUTH_MECHANISM", "NODE_OPTIONS", "NODE_EXTRA_CA_CERTS", "HTTP_PROXY", "https_proxy", "ALL_PROXY", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR", "HOME", "OPENFOX_GEMINI_PATH"} {
		if _, err := ResolveGeminiModel("fixture-model", map[string]string{"GEMINI_API_KEY": "CONTROLLED-R02-KEY", name: "CONTROLLED-REJECTED"}); err == nil || strings.Contains(err.Error(), "CONTROLLED-") {
			t.Fatalf("unsupported startup setting %s accepted or leaked", name)
		}
	}
	if _, err := ResolveGeminiModel("fixture-model", map[string]string{"GEMINI_API_KEY": "CONTROLLED-R02-KEY", "APP_NAME": "ordinary"}); err != nil {
		t.Fatal("unrelated stored environment changed the Gemini contract")
	}
}

func TestGeminiModesRemainReadOnly(t *testing.T) {
	for _, approval := range []string{"", FollowRuntime, ReadOnly} {
		for _, collaboration := range []string{"", CollaborationFollowRuntime, CollaborationDefault, CollaborationPlan} {
			if err := ValidateGeminiModes(approval, collaboration, ReasoningDefault, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, approval := range []string{WorkspaceWrite, FullAccess, "unknown"} {
		if ValidateGeminiModes(approval, CollaborationPlan, ReasoningDefault, nil) == nil {
			t.Fatalf("accepted writable mode %s", approval)
		}
	}
	if ValidateGeminiModes(ReadOnly, "unknown", ReasoningDefault, nil) == nil || ValidateGeminiModes(ReadOnly, CollaborationPlan, ReasoningHigh, nil) == nil || ValidateGeminiModes(ReadOnly, CollaborationPlan, ReasoningDefault, []string{"--unsafe"}) == nil {
		t.Fatal("unverified execution setting accepted")
	}
}
