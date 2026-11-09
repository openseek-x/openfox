package devices

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestScopesAreExplicitAndClosed(t *testing.T) {
	for _, scope := range []Scope{ScopePersonal, ScopeSpace, ScopeOrganization} {
		if !scope.Valid() {
			t.Fatalf("scope %q was rejected", scope)
		}
	}
	if Scope("ROLE").Valid() {
		t.Fatal("unknown scope was accepted")
	}
}

func TestRuntimeModelsDefaultReasoning(t *testing.T) {
	runtimes := ParseRuntimes([]byte(`{"runtimes":{"codex":{"models":[
		{"id":"reported","default_reasoning_effort":"HIGH","reasoning_efforts":["HIGH"]},
		{"id":"missing","reasoning_efforts":["HIGH"]},
		{"id":"invalid","default_reasoning_effort":"UNKNOWN","reasoning_efforts":["UNKNOWN"]},
		{"id":"unsupported","default_reasoning_effort":"HIGH","reasoning_efforts":["LOW"]}
	]}}}`))
	if len(runtimes) != 1 || len(runtimes[0].Models) != 4 {
		t.Fatalf("runtimes = %+v", runtimes)
	}
	for index, want := range []string{"HIGH", "", "", ""} {
		if got := runtimes[0].Models[index].DefaultReasoningEffort; got != want {
			t.Fatalf("model %d default = %q, want %q", index, got, want)
		}
	}
}

func TestRegistrationInputBounds(t *testing.T) {
	name, installID, slots, err := normalizeRegistrationInput(RegistrationTokenInput{Name: "Mac", InstallID: "install-1"})
	if err != nil || name != "Mac" || installID != "install-1" || slots != 1 {
		t.Fatalf("normalized registration = %q/%q/%d, err=%v", name, installID, slots, err)
	}
	if _, _, _, err := normalizeRegistrationInput(RegistrationTokenInput{Name: "\n"}); err == nil {
		t.Fatal("control character was accepted in device name")
	}
	if _, _, _, err := normalizeRegistrationInput(RegistrationTokenInput{Name: "Mac", MaxSlots: 1025}); err == nil {
		t.Fatal("out-of-range max slots was accepted")
	}
}

func TestHeartbeatRequiresObjectCapabilities(t *testing.T) {
	base := Heartbeat{CenterEpoch: 1, ProtocolMajor: 1, Capabilities: []byte(`{"runtime":"unknown"}`)}
	if err := validateHeartbeat(base); err != nil {
		t.Fatalf("valid heartbeat rejected: %v", err)
	}
	base.Capabilities = []byte(`[]`)
	if err := validateHeartbeat(base); err == nil {
		t.Fatal("array capabilities accepted")
	}
}

func TestParseRuntimesFromCapabilities(t *testing.T) {
	capabilities := []byte(`{"runtimes":{
		"zeta":{"runnable":false,"reason":"version unsupported","login_status":"LOGGED_IN"},
		"codex":{"version":"0.50.0","login_status":"LOGGED_IN","runnable":true,
		         "models":[{"id":"gpt-5","display_name":"GPT-5","is_default":true}]},
		"logged_out":{"version":"1","login_status":"LOGGED_OUT","runnable":false},
		"broken":"not an object"
	},"timezone":"Asia/Shanghai"}`)
	runtimes := ParseRuntimes(capabilities)
	if len(runtimes) != 3 {
		t.Fatalf("runtimes = %+v", runtimes)
	}
	codex, loggedOut, zeta := runtimes[0], runtimes[1], runtimes[2]
	if codex.Provider != "codex" || codex.Status != RuntimeReady || !codex.Runnable || *codex.Version != "0.50.0" ||
		codex.Reason != nil || len(codex.Models) != 1 || !codex.Models[0].IsDefault {
		t.Fatalf("codex = %+v", codex)
	}
	if loggedOut.Status != RuntimeNeedsLogin || loggedOut.Models == nil {
		t.Fatalf("logged_out = %+v", loggedOut)
	}
	if zeta.Status != RuntimeUnavailable || zeta.Version != nil || *zeta.Reason != "version unsupported" {
		t.Fatalf("zeta = %+v", zeta)
	}
	if got := parseTimezone(capabilities); got != "Asia/Shanghai" {
		t.Fatalf("timezone = %q", got)
	}
	for _, raw := range []string{``, `{}`, `{"runtimes":[]}`, `not json`} {
		if got := ParseRuntimes([]byte(raw)); got == nil || len(got) != 0 {
			t.Fatalf("ParseRuntimes(%q) = %+v", raw, got)
		}
	}
}

func TestDeviceJSONUsesSnakeCaseAndNulls(t *testing.T) {
	encoded, err := json.Marshal(DeviceDetail{Device: Device{ID: "d", GrantID: "g", GrantScope: ScopeOrganization}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"owner_user_id", "owner_email", "install_id", "last_seen_at", "grant_user_id",
		"grant_space_id", "grant_space_name", "timezone"} {
		if value, ok := decoded[key]; !ok || value != nil {
			t.Fatalf("%s = %v (present %v), want null", key, value, ok)
		}
	}
	if decoded["id"] != "d" || decoded["grant_id"] != "g" || decoded["grant_scope"] != "ORGANIZATION" {
		t.Fatalf("decoded = %v", decoded)
	}
	if runtimes, ok := decoded["runtimes"].([]any); !ok || len(runtimes) != 0 {
		t.Fatalf("runtimes = %v", decoded["runtimes"])
	}
	if agents, ok := decoded["bound_agents"].([]any); !ok || len(agents) != 0 {
		t.Fatalf("bound_agents = %v", decoded["bound_agents"])
	}
	if capabilities, ok := decoded["capabilities"].(map[string]any); !ok || len(capabilities) != 0 {
		t.Fatalf("capabilities = %v", decoded["capabilities"])
	}
	if _, ok := decoded["ID"]; ok {
		t.Fatal("Go field name leaked into JSON")
	}
}

func TestDeviceCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 29, 1, 2, 3, 456789000, time.UTC)
	cursor := encodeDeviceCursor(at, "0123ABCD-0000-4000-8000-000000000001")
	gotAt, gotID, err := decodeDeviceCursor(cursor)
	if err != nil || !gotAt.Equal(at) || gotID != "0123abcd-0000-4000-8000-000000000001" {
		t.Fatalf("decoded = %v %q %v", gotAt, gotID, err)
	}
	for _, raw := range []string{"%%%", "bm9waXBl", encodeDeviceCursor(at, "not-a-uuid")} {
		if _, _, err := decodeDeviceCursor(raw); err != ErrInvalidCursor {
			t.Fatalf("decode(%q) error = %v", raw, err)
		}
	}
}

func TestParseRuntimesMultipleProviders(t *testing.T) {
	capabilities := []byte(`{"runtimes":{
		"codex":{"version":"0.1","login_status":"LOGGED_IN","runnable":true,"models":[{"id":"gpt","display_name":"GPT","is_default":true}]},
		"claude-code":{"version":"2.1.0","login_status":"UNKNOWN","runnable":true,"models":[{"id":"default","display_name":"Default","is_default":true}]},
		"opencode":{"version":"1.0.0","login_status":"LOGGED_OUT","runnable":false,"reason":"login"},
		"pi":{"version":"0.9","runnable":false,"reason":"broken"}}}`)
	got := ParseRuntimes(capabilities)
	var order []string
	status := map[string]string{}
	for _, item := range got {
		order = append(order, item.Provider)
		status[item.Provider] = item.Status
	}
	if strings.Join(order, ",") != "claude-code,codex,opencode,pi" ||
		status["claude-code"] != RuntimeReady || status["opencode"] != RuntimeNeedsLogin || status["pi"] != RuntimeUnavailable {
		t.Fatalf("runtimes = %v %v", order, status)
	}
}

func TestNormalizeRuntimeTestAcceptsEveryKnownProvider(t *testing.T) {
	for _, provider := range []string{"codex", "claude-code", "opencode", "pi"} {
		raw, err := NormalizeCommandParams(CommandRuntimeTest, []byte(`{"provider":"`+provider+`","model":"m"}`))
		if err != nil || !strings.Contains(string(raw), `"provider":"`+provider+`"`) {
			t.Fatalf("%s: %s %v", provider, raw, err)
		}
	}
	if _, err := NormalizeCommandParams(CommandRuntimeTest, []byte(`{"provider":"gemini"}`)); err == nil {
		t.Fatal("unknown provider must be rejected")
	}
}
