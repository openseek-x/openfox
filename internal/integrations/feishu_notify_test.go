package integrations

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEventForKind(t *testing.T) {
	cases := map[string]string{
		"TASK_ASSIGNED": "assign", "TRIAGE_NEW": "assign", "TASK_COMMENT": "comment", "REPLY": "comment", "MENTION": "mention",
		"RUN_FAILED": "agent", "HANDOFF": "agent", "HANDOFF_REVIEW": "agent", "AUTOMATION_RESULT": "automation", "SOMETHING_NEW": "agent",
	}
	for kind, want := range cases {
		if got := eventForKind(kind); got != want {
			t.Errorf("eventForKind(%s) = %s, want %s", kind, got, want)
		}
	}
}

func TestEscapeLarkMD(t *testing.T) {
	got := escapeLarkMD(`<at id=all></at> & **bold** [x](http://e) #h`)
	for _, raw := range []string{"<", ">", "**", "[x]"} {
		if strings.Contains(got, raw) {
			t.Fatalf("escaped text still contains %q: %s", raw, got)
		}
	}
	if strings.Contains(strings.ReplaceAll(got, `\(`, ""), "(") || !strings.Contains(got, "&lt;at id=all&gt;") || !strings.Contains(got, "&amp;") {
		t.Fatalf("escaped = %s", got)
	}
}

func TestBuildNotificationCard(t *testing.T) {
	card := buildNotificationCard("MENTION", "", "hi <b>", "https://x.test/o/1/inbox")
	var parsed struct {
		Header   struct{ Template string } `json:"header"`
		Elements []map[string]any          `json:"elements"`
	}
	if err := json.Unmarshal([]byte(card), &parsed); err != nil || parsed.Header.Template != "blue" || len(parsed.Elements) != 2 {
		t.Fatalf("card = %s (%v)", card, err)
	}
	if !strings.Contains(card, "有人提到了你") || !strings.Contains(card, "https://x.test/o/1/inbox") || strings.Contains(card, "<b>") {
		t.Fatalf("card = %s", card)
	}
}

func TestNotifyBackoff(t *testing.T) {
	prev := time.Duration(0)
	for attempts := 1; attempts <= 4; attempts++ {
		wait := notifyBackoff(attempts)
		base := notifyBackoffBase << (attempts - 1)
		if wait < time.Duration(float64(base)*0.79) || wait > time.Duration(float64(base)*1.21) {
			t.Fatalf("attempt %d: wait %v outside ±20%% of %v", attempts, wait, base)
		}
		prev = wait
	}
	if notifyBackoff(20) > time.Duration(float64(notifyBackoffMax)*1.21) || prev == 0 {
		t.Fatal("backoff must be capped")
	}
}

func TestParsePreviewURL(t *testing.T) {
	const base = "https://fox.example.com"
	org := "0b8a3f1e-1111-2222-3333-444455556666"
	cases := []struct {
		url     string
		ok      bool
		display string
	}{
		{base + "/o/" + org + "/spaces/abc/tasks?task=FOX-12", true, "FOX-12"},
		{base + "/o/" + strings.ToUpper(org) + "/spaces/abc/tasks?task=fox-3", true, "fox-3"},
		{"http://fox.example.com/o/" + org + "/spaces/abc/tasks?task=FOX-12", false, ""},
		{"https://evil.example.com/o/" + org + "/spaces/abc/tasks?task=FOX-12", false, ""},
		{"https://fox.example.com@evil.example.com/o/" + org + "/spaces/abc/tasks?task=FOX-12", false, ""},
		{base + "/o/" + org + "/spaces/abc/tasks", false, ""},
		{base + "/o/" + org + "/inbox?task=FOX-1", false, ""},
		{base + "/o/" + org + "/spaces/abc/tasks/extra?task=FOX-1", false, ""},
		{"not a url", false, ""},
	}
	for _, c := range cases {
		gotOrg, display, ok := ParsePreviewURL(base+"/", c.url)
		if ok != c.ok || (ok && (display != c.display || gotOrg != strings.ToLower(org))) {
			t.Errorf("ParsePreviewURL(%q) = %q, %q, %v", c.url, gotOrg, display, ok)
		}
	}
}

func TestBuildNotificationCardTaskHeading(t *testing.T) {
	card := buildNotificationCard("TASK_ASSIGNED", "OPEN-1 · 修复 *登录*", "A task was assigned to you.", "https://x.test/t")
	if !strings.Contains(card, `**OPEN-1 · 修复 \\*登录\\***`) || !strings.Contains(card, "你被指派了这个任务。") || strings.Contains(card, "A task was") {
		t.Fatalf("card = %s", card)
	}
}
