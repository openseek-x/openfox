package integrations

import (
	"encoding/json"
	"strings"
	"testing"
)

func buglyBody(issue, title string) []byte {
	b, _ := json.Marshal(map[string]any{"ProductID": "Product-01", "IssueKey": issue, "IssueType": "crash", "ProjectName": "External name", "FieldValues": map[string]any{"title": title, "description": "<b>literal</b>", "priority": "urgent", "workspace_id": "untrusted", "assignee_id": "untrusted", "agent_id": "untrusted"}})
	return b
}
func TestBuglyPayloadContract(t *testing.T) {
	raw := buglyBody("AbC-001", "Actual title")
	p, e := ParseBuglyPayload(raw)
	if e != nil || p.IssueKey != "AbC-001" || p.ProductID != "Product-01" || p.Description != "```text\n<b>literal</b>\n```" {
		t.Fatal(p, e)
	}
	for name, b := range map[string][]byte{
		"duplicate":        []byte(`{"ProductID":"a","ProductID":"b"}`),
		"nested_duplicate": []byte(`{"ProductID":"p","IssueKey":"i","IssueType":"crash","FieldValues":{"title":"x","title":"y"}}`),
		"number_id":        []byte(strings.Replace(string(raw), `"ProductID":"Product-01"`, `"ProductID":12`, 1)),
		"array":            []byte("[]"), "trailing": append(append([]byte{}, raw...), []byte(" {}")...), "oversized": []byte(strings.Repeat(" ", BuglyBodyLimit+1)),
		"control": buglyBody("x", "hello\x00"), "title_long": buglyBody("x", strings.Repeat("字", 501)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, e := ParseBuglyPayload(b); e == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}
func TestBuglyLiteralSecretRemoval(t *testing.T) {
	b, _ := json.Marshal(map[string]any{"ProductID": "p", "IssueKey": "i", "IssueType": "crash", "FieldValues": map[string]any{"title": "t", "description": "<img src='https://u:CANARY_PASSWORD@example.test/a?ok=1&amp;access_token=CANARY_TOKEN#CANARY_FRAGMENT'>\n```\n<script>CANARY_SCRIPT</script>", "reporter": "https://example.test/?api_key=CANARY_API", "current_owner": "external"}})
	p, e := ParseBuglyPayload(b)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"CANARY_PASSWORD", "CANARY_TOKEN", "CANARY_FRAGMENT", "CANARY_API"} {
		if strings.Contains(p.Description, secret) {
			t.Fatal("secret retained", secret)
		}
	}
	if !strings.HasPrefix(p.Description, "````text\n") || !strings.HasSuffix(p.Description, "\n````") || !strings.Contains(p.Description, "<script>CANARY_SCRIPT</script>") || !strings.Contains(p.Description, "ok=1") {
		t.Fatal("literal source contract changed", p.Description)
	}
}
