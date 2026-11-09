package integrations

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations/gitee"
	fakegitee "openfox/tests/support/fakegitee/fake"
)

func giteeHeader(t *testing.T, secret string, at time.Time, urlEncode bool) http.Header {
	t.Helper()
	request := fakegitee.SignedWebhook("http://x.test/hook", secret, "Merge Request Hook", at, urlEncode, nil)
	return request.Header
}

func TestGiteeVerifyPasswordMode(t *testing.T) {
	adapter := giteeWebhook{}
	secret := []byte("s3cret-password")
	now := time.Now()
	good := fakegitee.PasswordWebhook("http://x.test", "s3cret-password", "Push Hook", now, nil).Header
	if !adapter.Verify(secret, good, nil, now) {
		t.Fatal("matching password must pass")
	}
	// 密码模式不检查时间戳（Gitee 的密码模式没有时间戳语义）。
	old := fakegitee.PasswordWebhook("http://x.test", "s3cret-password", "Push Hook", now.Add(-48*time.Hour), nil).Header
	if !adapter.Verify(secret, old, nil, now) {
		t.Fatal("password mode must not depend on the timestamp")
	}
	for _, bad := range []string{"", "s3cret-passwor", "s3cret-password0", "S3CRET-PASSWORD", "other"} {
		header := fakegitee.PasswordWebhook("http://x.test", bad, "Push Hook", now, nil).Header
		if adapter.Verify(secret, header, nil, now) {
			t.Fatalf("password %q must be rejected", bad)
		}
	}
	if adapter.Verify(nil, good, nil, now) || adapter.Verify([]byte{}, http.Header{}, nil, now) {
		t.Fatal("an empty secret must reject everything")
	}
}

func TestGiteeVerifySignatureMode(t *testing.T) {
	adapter := giteeWebhook{}
	secret := "sign-secret"
	now := time.Now()
	for _, encode := range []bool{true, false} {
		if !adapter.Verify([]byte(secret), giteeHeader(t, secret, now, encode), nil, now) {
			t.Fatalf("valid signature (urlEncode=%v) must pass", encode)
		}
		if !adapter.Verify([]byte(secret), giteeHeader(t, secret, now.Add(-59*time.Minute), encode), nil, now) {
			t.Fatalf("signature just inside the window (urlEncode=%v) must pass", encode)
		}
		// 窗口外（过期 / 来自未来）、密钥不对都拒绝。
		if adapter.Verify([]byte(secret), giteeHeader(t, secret, now.Add(-61*time.Minute), encode), nil, now) {
			t.Fatalf("expired signature (urlEncode=%v) must fail", encode)
		}
		if adapter.Verify([]byte(secret), giteeHeader(t, secret, now.Add(61*time.Minute), encode), nil, now) {
			t.Fatalf("future signature (urlEncode=%v) must fail", encode)
		}
		if adapter.Verify([]byte(secret), giteeHeader(t, "other", now, encode), nil, now) {
			t.Fatalf("wrong secret (urlEncode=%v) must fail", encode)
		}
	}
	header := giteeHeader(t, secret, now, true)
	header.Set("X-Gitee-Timestamp", strconv.FormatInt(now.Add(time.Second).UnixMilli(), 10))
	if adapter.Verify([]byte(secret), header, nil, now) {
		t.Fatal("a signature must be bound to its timestamp")
	}
	header = giteeHeader(t, secret, now, true)
	header.Del("X-Gitee-Timestamp")
	if adapter.Verify([]byte(secret), header, nil, now) {
		t.Fatal("a missing timestamp must fail")
	}
	header.Set("X-Gitee-Timestamp", "not-a-number")
	if adapter.Verify([]byte(secret), header, nil, now) {
		t.Fatal("a malformed timestamp must fail")
	}
}

func TestGiteeEnvelopeDedupesOnEventAndBody(t *testing.T) {
	adapter := giteeWebhook{}
	header := http.Header{"X-Gitee-Event": []string{"Push Hook"}}
	a, event := adapter.Envelope(header, []byte(`{"timestamp":"1"}`))
	b, _ := adapter.Envelope(header, []byte(`{"timestamp":"1"}`))
	c, _ := adapter.Envelope(header, []byte(`{"timestamp":"2"}`))
	d, _ := adapter.Envelope(http.Header{"X-Gitee-Event": []string{"Issue Hook"}}, []byte(`{"timestamp":"1"}`))
	if event != "Push Hook" || a != b || a == c || a == d || !strings.HasPrefix(a, "ge-") {
		t.Fatalf("keys = %q %q %q %q", a, b, c, d)
	}
}

func TestGiteeParsePullRequestMapping(t *testing.T) {
	adapter := giteeWebhook{}
	cases := []struct {
		action, desc, state, want string
	}{
		{"open", "", "open", "opened"},
		{"reopen", "", "open", "reopened"},
		{"close", "", "closed", "closed"},
		{"merge", "", "merged", "closed"},
		{"update", "source_branch_changed", "open", "synchronize"},
		{"update", "", "open", "edited"},
		{"tested", "", "open", "tested"},
	}
	for _, c := range cases {
		body := `{"hook_name":"merge_request_hooks","action":"` + c.action + `","action_desc":"` + c.desc + `","pull_request":{"id":9,"number":3,"state":"` + c.state +
			`","title":"T","body":null,"html_url":"https://g/pulls/3","user":{"id":7,"login":"octo"},"head":{"ref":"feat"}},` +
			`"repository":{"path_with_namespace":"org/repo","full_name":"显示名/repo","private":true}}`
		event, payload, err := adapter.Parse("Merge Request Hook", []byte(body))
		if err != nil || event != "pull_request" || payload.Action != c.want || payload.PullRequest == nil || payload.PullRequest.Number != 3 {
			t.Fatalf("%s/%s = %s %+v %v", c.action, c.desc, event, payload, err)
		}
		if payload.Repository.FullName != "org/repo" || payload.Repository.Owner.Login != "org" || !payload.Repository.Private ||
			payload.PullRequest.Head.Ref != "feat" || payload.PullRequest.User.Login != "octo" {
			t.Fatalf("mapping = %+v %+v", payload.Repository, payload.PullRequest)
		}
		merged := c.action == "merge"
		if payload.PullRequest.Merged != merged || (merged && (payload.PullRequest.MergedAt == nil || payload.PullRequest.ClosedAt == nil)) {
			t.Fatalf("merged = %v for %s", payload.PullRequest.Merged, c.action)
		}
	}
}

func TestGiteeParseIssuesAndNotes(t *testing.T) {
	adapter := giteeWebhook{}
	issue := `{"id":295024870,"number":"IG6E9","title":"t","body":null,"state":"rejected","html_url":"https://g/issues/IG6E9","user":{"id":1,"login":"robot"},` +
		`"assignee":{"id":2,"username":"ming"},"created_at":"2018-02-07T23:46:46+08:00","updated_at":"2018-02-07T23:46:46+08:00"}`
	repo := `"repository":{"path_with_namespace":"org/repo","private":false}`
	event, payload, err := adapter.Parse("Issue Hook", []byte(`{"hook_name":"issue_hooks","action":"state_change","issue":`+issue+`,`+repo+`}`))
	if err != nil || event != "issues" || payload.Action != "closed" || payload.Issue == nil {
		t.Fatalf("issue = %s %+v %v", event, payload, err)
	}
	// Number 是数字 id，Ref 是字母数字串；rejected 归并为 closed；assignee 取 username。
	if payload.Issue.Number != 295024870 || payload.Issue.Ref != "IG6E9" || payload.Issue.State != "closed" ||
		len(payload.Issue.Assignees) != 1 || payload.Issue.Assignees[0].Login != "ming" || payload.Repository.Private {
		t.Fatalf("issue mapping = %+v", payload.Issue)
	}
	for action, want := range map[string]string{"open": "opened", "assign": "assigned", "delete": "deleted", "anything": "edited"} {
		_, p, _ := adapter.Parse("Issue Hook", []byte(`{"hook_name":"issue_hooks","action":"`+action+`","issue":`+issue+`,`+repo+`}`))
		if p.Action != want {
			t.Fatalf("action %s = %s, want %s", action, p.Action, want)
		}
	}
	event, payload, err = adapter.Parse("Note Hook", []byte(`{"hook_name":"note_hooks","action":"comment","noteable_type":"Issue","comment":{"id":5,"body":"hi","user":{"id":3,"login":"c"}},"issue":`+issue+`,`+repo+`}`))
	if err != nil || event != "issue_comment" || payload.Comment == nil || payload.Comment.ID != 5 || payload.Comment.Body != "hi" || payload.Comment.User.Login != "c" {
		t.Fatalf("note = %s %+v %v", event, payload, err)
	}
	if event, _, _ = adapter.Parse("Note Hook", []byte(`{"hook_name":"note_hooks","noteable_type":"PullRequest",`+repo+`}`)); event != "ignored" {
		t.Fatalf("pull request note = %s", event)
	}
	if event, _, _ = adapter.Parse("Tag Push Hook", []byte(`{"hook_name":"tag_push_hooks",`+repo+`}`)); event != "ignored" {
		t.Fatalf("tag push = %s", event)
	}
	// 数字 id 超出 issue_number（int32）范围：Number 为 0，公共层按 missing issue 处理。
	big := strings.Replace(issue, "295024870", "3000000000", 1)
	if _, p, _ := adapter.Parse("Issue Hook", []byte(`{"hook_name":"issue_hooks","action":"open","issue":`+big+`,`+repo+`}`)); p.Issue.Number != 0 {
		t.Fatalf("overflowing id must map to 0, got %d", p.Issue.Number)
	}
	if gitee.NumberFromID(2147483647) != 2147483647 || gitee.NumberFromID(2147483648) != 0 || gitee.NumberFromID(0) != 0 {
		t.Fatal("NumberFromID bounds")
	}
	_, payload, err = adapter.Parse("Push Hook", []byte(`{"hook_name":"push_hooks","ref":"refs/heads/main","commits":[{"id":"abc","message":"m","url":"u","author":{"username":"dev"}}],`+repo+`}`))
	if err != nil || payload.Ref != "refs/heads/main" || len(payload.Commits) != 1 || payload.Commits[0].Author.Username != "dev" {
		t.Fatalf("push = %+v %v", payload, err)
	}
}
