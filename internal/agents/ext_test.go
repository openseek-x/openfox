package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/dispatch"
)

func TestOnlineWindowMatchesDispatch(t *testing.T) {
	if time.Duration(OnlineWindowSeconds)*time.Second != dispatch.DefaultDeviceOnlineAfter {
		t.Fatalf("online window %ds != dispatch %s", OnlineWindowSeconds, dispatch.DefaultDeviceOnlineAfter)
	}
	if !strings.Contains(agentOnlineSQL, fmt.Sprintf("secs => %d", OnlineWindowSeconds)) {
		t.Fatal("agentOnlineSQL does not use OnlineWindowSeconds")
	}
	// 对话列表的 agent.online 用自己的常量（避免循环依赖），必须与目录一致。
	if conversations.OnlineWindowSeconds != OnlineWindowSeconds {
		t.Fatalf("conversations online window %d != agents %d", conversations.OnlineWindowSeconds, OnlineWindowSeconds)
	}
}

func TestNormalizeRunSettingsDefaultsAndBounds(t *testing.T) {
	settings, err := normalizeRunSettings(RunSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if settings.ChatConcurrency != "DEFAULT" || settings.WorkdirIsolation != "WORKTREE" || settings.ApprovalMode != "FOLLOW_RUNTIME" ||
		settings.CollaborationMode != "FOLLOW_RUNTIME" || settings.GitAuthorNameMode != "USER_AND_AGENT" ||
		settings.GitAuthorEmailMode != "USER" || settings.CustomArgs == nil {
		t.Fatalf("defaults = %+v", settings)
	}
	value := func(v int) *int { return &v }
	ok := []RunSettings{
		{ChatConcurrency: "manual", ChatConcurrencyValue: value(64)},
		{ChatConcurrency: "SHARE_TASK", WorkdirIsolation: "direct"},
		{GitAuthorNameMode: "MANUAL", GitAuthorName: "Bot", GitAuthorEmailMode: "MANUAL", GitAuthorEmail: "bot@example.test"},
		{CustomArgs: []string{"--flag", "value with space"}},
	}
	for _, input := range ok {
		if _, err := normalizeRunSettings(input); err != nil {
			t.Fatalf("%+v rejected: %v", input, err)
		}
	}
	tooMany := make([]string, maxCustomArgs+1)
	bad := []RunSettings{
		{ChatConcurrency: "MANUAL"},
		{ChatConcurrency: "MANUAL", ChatConcurrencyValue: value(65)},
		{ChatConcurrency: "DEFAULT", ChatConcurrencyValue: value(2)},
		{ChatConcurrency: "SOMETIMES"},
		{WorkdirIsolation: "COPY"},
		{ApprovalMode: "bad mode"},
		{CustomArgs: tooMany},
		{CustomArgs: []string{"line\nbreak"}},
		{CustomArgs: []string{strings.Repeat("x", maxCustomArgBytes+1)}},
		{GitAuthorNameMode: "MANUAL"},
		{GitAuthorNameMode: "ROBOT"},
		{GitAuthorEmailMode: "MANUAL", GitAuthorEmail: "not-an-email"},
	}
	for _, input := range bad {
		if _, err := normalizeRunSettings(input); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%+v error = %v", input, err)
		}
	}
}

// 协作模式只接受三个取值与历史值 FOLLOW_AGENT（规范化为 FOLLOW_RUNTIME），其他值拒绝；权限模式不受影响。
func TestNormalizeRunSettingsCollaborationMode(t *testing.T) {
	cases := map[string]string{
		"": "FOLLOW_RUNTIME", "FOLLOW_RUNTIME": "FOLLOW_RUNTIME", "FOLLOW_AGENT": "FOLLOW_RUNTIME",
		"follow_agent": "FOLLOW_RUNTIME", "DEFAULT": "DEFAULT", "PLAN": "PLAN", " plan ": "PLAN",
	}
	for input, want := range cases {
		got, err := normalizeRunSettings(RunSettings{CollaborationMode: input, ApprovalMode: "READ_ONLY"})
		if err != nil || got.CollaborationMode != want || got.ApprovalMode != "READ_ONLY" {
			t.Fatalf("%q -> %+v, %v", input, got, err)
		}
	}
	for _, input := range []string{"FOLLOW_AGENTS", "AUTO", "PLAN_MODE", "plan mode", "X"} {
		if _, err := normalizeRunSettings(RunSettings{CollaborationMode: input}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%q error = %v", input, err)
		}
	}
}

func TestEnvDigest(t *testing.T) {
	if EnvDigest(nil, nil) != "" {
		t.Fatal("empty digest should be empty string")
	}
	values := map[string][]byte{"A": []byte("1"), "B": []byte("2")}
	digest := EnvDigest([]string{"A", "B"}, values)
	sum := func(v string) string { d := sha256.Sum256([]byte(v)); return hex.EncodeToString(d[:]) }
	want := sum("A=" + sum("1") + "\nB=" + sum("2") + "\n")
	if digest != want || len(digest) != 64 {
		t.Fatalf("digest = %s, want %s", digest, want)
	}
	values["B"] = []byte("3")
	if EnvDigest([]string{"A", "B"}, values) == digest {
		t.Fatal("digest must change when a value changes")
	}
}

func TestCopyName(t *testing.T) {
	if got := CopyName("Reviewer", 1); got != "Reviewer 副本" {
		t.Fatalf("first = %q", got)
	}
	if got := CopyName("Reviewer", 3); got != "Reviewer 副本 3" {
		t.Fatalf("third = %q", got)
	}
	long := CopyName(strings.Repeat("名", 300), 12)
	if len([]rune(long)) > 255 || !strings.HasSuffix(long, "副本 12") {
		t.Fatalf("long name = %d runes", len([]rune(long)))
	}
}

func TestRunCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 29, 1, 2, 3, 456789000, time.UTC)
	id := "00000000-0000-0000-0000-000000000001"
	gotAt, gotID, ok := DecodeRunCursor(EncodeRunCursor(at, id))
	if !ok || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = %v %s %v", gotAt, gotID, ok)
	}
	for _, bad := range []string{"", "!!", EncodeRunCursor(at, "nope")} {
		if _, _, ok := DecodeRunCursor(bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestValidRepoURL(t *testing.T) {
	for _, url := range []string{"https://github.com/acme/web.git", "git@github.com:acme/web.git", "ssh://git@host.example/acme/web"} {
		if !ValidRepoURL(url) {
			t.Fatalf("%s rejected", url)
		}
	}
	for _, url := range []string{"", "http://github.com/acme/web", "file:///etc/passwd", "https://x y", "github.com/acme/web"} {
		if ValidRepoURL(url) {
			t.Fatalf("%s accepted", url)
		}
	}
}

func TestNormalizeAvatar(t *testing.T) {
	if value, err := normalizeAvatar(collaboration.OptionalRaw{Set: true}); err != nil || value != nil {
		t.Fatalf("null avatar = %s, %v", value, err)
	}
	value, err := normalizeAvatar(collaboration.OptionalRaw{Set: true, Value: []byte(`{ "kind": "emoji", "value": "🦊" }`)})
	if err != nil || string(value) != `{"kind":"emoji","value":"🦊"}` {
		t.Fatalf("avatar = %s, %v", value, err)
	}
	for _, raw := range []string{`"text"`, `[1]`, `{"x":"` + strings.Repeat("a", 1100) + `"}`} {
		if _, err := normalizeAvatar(collaboration.OptionalRaw{Set: true, Value: []byte(raw)}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("%.20s error = %v", raw, err)
		}
	}
}

func TestGrantAllows(t *testing.T) {
	space, user := "s1", "u1"
	if grantAllows("ORGANIZATION", nil, nil, "x", "y") != nil {
		t.Fatal("organization grant should allow everything")
	}
	if grantAllows("SPACE", &space, nil, "s1", "y") != nil || grantAllows("SPACE", &space, nil, "s2", "y") == nil {
		t.Fatal("space grant check is wrong")
	}
	if grantAllows("PERSONAL", nil, &user, "x", "u1") != nil || grantAllows("PERSONAL", nil, &user, "x", "u2") == nil {
		t.Fatal("personal grant check is wrong")
	}
}
