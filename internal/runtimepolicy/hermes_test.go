package runtimepolicy

import (
	"strings"
	"testing"
)

func TestHermesDeclaredModelConfiguration(t *testing.T) {
	env := map[string]string{"HERMES_API_KEY": "secret-canary", "HERMES_BASE_URL": "https://model.example/api/v1", "HERMES_MODEL": "explicit-model", "HERMES_CONTEXT_LENGTH": "65536"}
	got, err := ResolveHermesModel(DefaultModel, env)
	if err != nil || got.ContextLength != 65536 || got.Model != "explicit-model" || got.BaseURL != env["HERMES_BASE_URL"] {
		t.Fatalf("configuration: %#v %v", got, err)
	}
	for _, value := range []string{"", "32768", "64000.0", "64000x", "-64000", "2147483648"} {
		env["HERMES_CONTEXT_LENGTH"] = value
		if _, err := ResolveHermesModel("m", env); err == nil || strings.Contains(err.Error(), env["HERMES_API_KEY"]) {
			t.Errorf("invalid declaration accepted or leaked: %q", value)
		}
	}
	env["HERMES_CONTEXT_LENGTH"] = "64000"
	env["HERMES_ENABLE_PROJECT_PLUGINS"] = "1"
	if _, err := ResolveHermesModel("m", env); err == nil {
		t.Fatal("native control variable accepted")
	}
}

func TestHermesEndpointBoundaries(t *testing.T) {
	for _, raw := range []string{"http://model.example/v1", "https://127.0.0.1/v1", "https://localhost/v1", "https://model.example:444/v1", "https://u:p@model.example/v1", "https://model.example/v1?x=y", "https://model.example/v1?", "https://model.example/v1#x", "https://model.example/%2e%2e/v1", "https://model.example/a/../v1", "https://model.example//v1", "https://model.example/v1/chat/completions", "https://model.example\\attacker.invalid/v1", "https://bad-.example/v1"} {
		if _, err := HermesEndpoint(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	if got, err := HermesEndpoint("https://Model.Example:443/api/v1/"); err != nil || got != "https://model.example/api/v1" {
		t.Fatalf("canonical endpoint: %q %v", got, err)
	}
}

func TestHermesModesDoNotElevate(t *testing.T) {
	for _, mode := range []string{WorkspaceWrite, FullAccess} {
		if ValidateHermesModes(mode, CollaborationPlan, ReasoningDefault, nil) == nil {
			t.Fatal("PLAN silently admitted elevated permissions")
		}
	}
	if ValidateHermesModes(ReadOnly, CollaborationPlan, ReasoningDefault, nil) != nil || ValidateHermesModes(ReadOnly, CollaborationPlan, ReasoningHigh, nil) == nil || ValidateHermesModes(ReadOnly, CollaborationPlan, ReasoningDefault, []string{"--unsafe"}) == nil {
		t.Fatal("mode contract")
	}
}
