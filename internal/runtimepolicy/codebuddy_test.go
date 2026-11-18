package runtimepolicy

import "testing"

func TestCodeBuddyConfiguration(t *testing.T) {
	base := map[string]string{"CODEBUDDY_API_KEY": "fixture-secret", "CODEBUDDY_BASE_URL": "https://model.example/v1", "CODEBUDDY_MODEL": "configured-model"}
	for _, tc := range []struct {
		name, model, key, value, want string
		invalid                       bool
	}{
		{"explicit", "explicit-model", "", "", "explicit-model", false},
		{"default", DefaultModel, "", "", "configured-model", false},
		{"missing-key", DefaultModel, "CODEBUDDY_API_KEY", "", "", true},
		{"missing-base", DefaultModel, "CODEBUDDY_BASE_URL", "", "", true},
		{"missing-default", DefaultModel, "CODEBUDDY_MODEL", "", "", true},
		{"default-is-not-a-model", DefaultModel, "CODEBUDDY_MODEL", DefaultModel, "", true},
		{"auto", "auto", "", "", "", true},
		{"model-control", "model\nsecret", "", "", "", true},
		{"loopback", DefaultModel, "CODEBUDDY_BASE_URL", "http://127.0.0.1:1234/v1", "configured-model", false},
		{"ipv6-loopback", DefaultModel, "CODEBUDDY_BASE_URL", "http://[::1]:1234/v1", "configured-model", false},
		{"public-http", DefaultModel, "CODEBUDDY_BASE_URL", "http://model.example/v1", "", true},
		{"userinfo", DefaultModel, "CODEBUDDY_BASE_URL", "https://key@model.example/v1", "", true},
		{"query", DefaultModel, "CODEBUDDY_BASE_URL", "https://model.example/v1?key=secret", "", true},
		{"empty-query", DefaultModel, "CODEBUDDY_BASE_URL", "https://model.example/v1?", "", true},
		{"empty-fragment", DefaultModel, "CODEBUDDY_BASE_URL", "https://model.example/v1#", "", true},
		{"full-api-path", DefaultModel, "CODEBUDDY_BASE_URL", "https://model.example/v1/chat/completions/", "", true},
		{"auth-token", DefaultModel, "CODEBUDDY_AUTH_TOKEN", "fixture-token", "", true},
		{"guard-override", DefaultModel, "CODEBUDDY_DISABLE_CUSTOM_MODELS_FILE", "0", "", true},
		{"tls-bypass", DefaultModel, "NODE_TLS_REJECT_UNAUTHORIZED", "0", "", true},
		{"tls-explicit-verify", DefaultModel, "NODE_TLS_REJECT_UNAUTHORIZED", "1", "configured-model", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			for k, v := range base {
				env[k] = v
			}
			if tc.key != "" {
				env[tc.key] = tc.value
			}
			got, err := ResolveCodeBuddyModel(tc.model, env)
			if (err != nil) != tc.invalid || (!tc.invalid && got != tc.want) {
				t.Fatalf("model=%q err=%v", got, err)
			}
		})
	}
}

func TestCodeBuddyModes(t *testing.T) {
	for _, approval := range []string{"", FollowRuntime, ReadOnly} {
		for _, collab := range []string{"", CollaborationFollowRuntime, CollaborationDefault, CollaborationPlan} {
			if err := ValidateCodeBuddyModes(approval, collab, ReasoningDefault, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, approval := range []string{WorkspaceWrite, FullAccess} {
		if ValidateCodeBuddyModes(approval, CollaborationPlan, ReasoningDefault, nil) == nil {
			t.Fatal("unsupported permission accepted with PLAN")
		}
	}
	if ValidateCodeBuddyModes(ReadOnly, CollaborationPlan, ReasoningHigh, nil) == nil {
		t.Fatal("reasoning accepted")
	}
	if ValidateCodeBuddyModes(ReadOnly, CollaborationPlan, ReasoningDefault, []string{"--plugin-dir", "x"}) == nil {
		t.Fatal("plugin arguments accepted")
	}
}
