package runtimepolicy

import "testing"

func TestMachineModelPrecedence(t *testing.T) {
	agent := Config{Runtime: "codex", Model: "default", ModelSource: "MACHINE", MachineModels: map[string]string{"codex": "machine-codex", "pi": "machine-pi"}}
	cases := []struct {
		name          string
		source, model string
		overrides     Overrides
		want, origin  string
	}{
		{"machine", "MACHINE", "default", Overrides{}, "machine-codex", "computer"},
		{"explicit default skips machine", "EXPLICIT", "default", Overrides{}, "default", "runtime"},
		{"old missing source skips machine", "", "default", Overrides{}, "default", "runtime"},
		{"explicit model", "EXPLICIT", "agent-model", Overrides{}, "agent-model", "agent"},
		{"conversation concrete", "MACHINE", "default", Overrides{Model: ptr("conversation-model")}, "conversation-model", "conversation"},
		{"conversation explicit default", "MACHINE", "default", Overrides{Model: ptr("default")}, "default", "conversation"},
		{"runtime switches to own machine default", "MACHINE", "default", Overrides{Runtime: ptr("pi")}, "machine-pi", "computer"},
		{"runtime with no defaults", "MACHINE", "default", Overrides{Runtime: ptr("claude-code")}, "default", "runtime"},
		{"explicit agent runtime changed", "EXPLICIT", "agent-model", Overrides{Runtime: ptr("pi")}, "default", "runtime"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := agent
			config.ModelSource = tc.source
			config.Model = tc.model
			got := Effective(config, tc.overrides)
			if got.Model != tc.want || got.ModelOrigin != tc.origin {
				t.Fatalf("got %q/%q want %q/%q", got.Model, got.ModelOrigin, tc.want, tc.origin)
			}
		})
	}
	agent.MachineModels = nil
	if Effective(agent, Overrides{}).Model != DefaultModel {
		t.Fatal("missing defaults must use Runtime default")
	}
}
