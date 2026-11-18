package runtimepolicy

import "testing"

func TestModelDefaultReasoningIsReportedNotInferred(t *testing.T) {
	for _, item := range []struct {
		value string
		want  string
	}{
		{"LOW", "LOW"}, {"HIGH", "HIGH"}, {"EXTRA_HIGH", "EXTRA_HIGH"}, {"ULTRA", "ULTRA"},
		{"", ""}, {"DEFAULT", ""}, {"UNKNOWN", ""}, {"MEDIUM", ""},
	} {
		if got := NormalizeDefaultReasoningEffort(item.value, []string{"DEFAULT", "LOW", "HIGH", "EXTRA_HIGH", "ULTRA"}); got != item.want {
			t.Fatalf("default %q = %q, want %q", item.value, got, item.want)
		}
	}
	capabilities := ParseCapabilities([]byte(`{"runtimes":{"codex":{"models":[
		{"id":"reported","default_reasoning_effort":"LOW","reasoning_efforts":["LOW","HIGH"]},
		{"id":"missing","reasoning_efforts":["HIGH"]},
		{"id":"invalid","default_reasoning_effort":"MAX","reasoning_efforts":["LOW"]}
	]}}}`))
	if len(capabilities) != 1 || len(capabilities[0].ModelInfo) != 3 {
		t.Fatalf("models = %+v", capabilities)
	}
	for index, want := range []string{"LOW", "", ""} {
		if got := capabilities[0].ModelInfo[index].DefaultReasoningEffort; got != want {
			t.Fatalf("model %d default = %q, want %q", index, got, want)
		}
	}
}
