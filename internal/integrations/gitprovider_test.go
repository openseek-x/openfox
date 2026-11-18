package integrations

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations/gitee"
	"openfox/internal/integrations/gitlab"
)

func TestGitQueryOnlyRewritesWhitelistedProviders(t *testing.T) {
	query := "SELECT 1 WHERE provider = 'github'"
	if got := gitQuery(query, "gitlab"); got != "SELECT 1 WHERE provider = 'gitlab'" {
		t.Fatalf("gitlab = %q", got)
	}
	if got := gitQuery(query, "gitlab_cn"); !strings.Contains(got, "'gitlab_cn'") {
		t.Fatalf("gitlab_cn = %q", got)
	}
	for _, provider := range []string{"github", "", "bitbucket", "x' OR '1'='1", "feishu"} {
		if got := gitQuery(query, provider); got != query {
			t.Fatalf("%q must leave the query unchanged, got %q", provider, got)
		}
	}
	if (Service{}).git() != ProviderGitHub || (Service{}).ForGit("bogus").git() != ProviderGitHub || (Service{}).ForGit("gitlab").git() != ProviderGitLab {
		t.Fatal("ForGit must fall back to github for unknown providers")
	}
}

func TestValidRepositoryNameFor(t *testing.T) {
	good := map[string][]string{
		"github":    {"a/b", "My-Org/repo.name_1"},
		"gitlab":    {"a/b", "a/b/c", "grp/sub/sub2/project"},
		"gitlab_cn": {"a/b/c"},
	}
	for provider, names := range good {
		for _, name := range names {
			if !ValidRepositoryNameFor(provider, name) {
				t.Errorf("%s %q must be valid", provider, name)
			}
		}
	}
	bad := map[string][]string{
		"github":    {"a", "a/b/c", "a/", "/b", "a b/c", ""},
		"gitlab":    {"a", "a//b", "a/../b", "./a", "a/b/", "a/b c", "a/b?x=1", strings.Repeat("a/", 21) + "a", strings.Repeat("a", 256) + "/b"},
		"gitlab_cn": {"a/%2e%2e/b"},
	}
	for provider, names := range bad {
		for _, name := range names {
			if ValidRepositoryNameFor(provider, name) {
				t.Errorf("%s %q must be invalid", provider, name)
			}
		}
	}
}

func TestGitLabWebhookVerifyAndEnvelope(t *testing.T) {
	a := gitlabWebhook{}
	header := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	now := time.Now()
	if !a.Verify([]byte("s3cret"), header("X-Gitlab-Token", "s3cret"), nil, now) {
		t.Fatal("matching token must verify")
	}
	for _, token := range []string{"", "s3cre", "s3crett", "S3CRET", " s3cret"} {
		if a.Verify([]byte("s3cret"), header("X-Gitlab-Token", token), nil, now) {
			t.Fatalf("token %q must not verify", token)
		}
	}
	if a.Verify(nil, header("X-Gitlab-Token", ""), nil, now) || a.Verify([]byte(""), header("X-Gitlab-Token", "x"), nil, now) {
		t.Fatal("empty secret must never verify")
	}
	key, event := a.Envelope(header("Idempotency-Key", "k1", "X-Gitlab-Event", "Push Hook", "X-Gitlab-Event-UUID", "u"), nil)
	if key != "k1" || event != "Push Hook" {
		t.Fatalf("envelope = %q %q", key, event)
	}
	if key, _ = a.Envelope(header("webhook-id", "w1", "X-Gitlab-Event", "x"), nil); key != "w1" {
		t.Fatalf("webhook-id = %q", key)
	}
	if key, _ = a.Envelope(header("X-Gitlab-Event-UUID", "u1", "X-Gitlab-Event", "x"), nil); key != "u1" {
		t.Fatalf("event uuid = %q", key)
	}
	first, _ := a.Envelope(header("X-Gitlab-Event", "x"), []byte("body"))
	again, _ := a.Envelope(header("X-Gitlab-Event", "x"), []byte("body"))
	other, _ := a.Envelope(header("X-Gitlab-Event", "x"), []byte("body2"))
	if first == "" || first != again || first == other || len(first) > 128 {
		t.Fatalf("digest keys = %q %q %q", first, again, other)
	}
}

func TestGitLabWebhookParse(t *testing.T) {
	a := gitlabWebhook{}
	merged := `{"object_kind":"merge_request","user":{"id":7,"username":"lin"},"project":{"path_with_namespace":"grp/sub/app","visibility_level":0},
		"object_attributes":{"id":1,"iid":5,"title":"T","description":"D","state":"merged","action":"merge","source_branch":"b","author_id":7,
		"url":"https://x/mr/5","updated_at":"2026-11-02 10:00:00 UTC"}}`
	event, payload, err := a.Parse("Merge Request Hook", []byte(merged))
	if err != nil || event != "pull_request" || payload.Action != "closed" || payload.PullRequest == nil || !payload.PullRequest.Merged ||
		payload.PullRequest.MergedAt == nil || payload.PullRequest.Number != 5 || payload.PullRequest.User.Login != "lin" ||
		payload.Repository.FullName != "grp/sub/app" || payload.Repository.Owner.Login != "grp" || !payload.Repository.Private {
		t.Fatalf("merged = %s %+v %+v %v", event, payload, payload.PullRequest, err)
	}
	if prState(payload.PullRequest) != "MERGED" {
		t.Fatalf("state = %s", prState(payload.PullRequest))
	}
	draft := `{"object_kind":"merge_request","project":{"path_with_namespace":"a/b","visibility_level":20},"object_attributes":{"iid":1,"state":"opened","action":"open","draft":true,"url":"u"}}`
	_, payload, _ = a.Parse("x", []byte(draft))
	if payload.Action != "opened" || prState(payload.PullRequest) != "DRAFT" || payload.Repository.Private {
		t.Fatalf("draft = %+v", payload)
	}
	update := `{"object_kind":"merge_request","project":{"path_with_namespace":"a/b"},"object_attributes":{"iid":1,"state":"opened","action":"update","oldrev":"abc","url":"u"}}`
	if _, payload, _ = a.Parse("x", []byte(update)); payload.Action != "synchronize" {
		t.Fatalf("oldrev update = %q", payload.Action)
	}
	note := `{"object_kind":"note","project":{"path_with_namespace":"a/b"},"object_attributes":{"id":3,"note":"hi","noteable_type":"MergeRequest"}}`
	if event, _, _ = a.Parse("Note Hook", []byte(note)); event != "ignored" {
		t.Fatalf("merge request note = %q", event)
	}
	if _, _, err = a.Parse("x", []byte("not json")); err == nil {
		t.Fatal("invalid json must fail")
	}
}

func TestGitQueryRewritesGitee(t *testing.T) {
	query := "SELECT 1 WHERE provider = 'github'"
	if got := gitQuery(query, "gitee"); got != "SELECT 1 WHERE provider = 'gitee'" {
		t.Fatalf("gitee = %q", got)
	}
	if !IsGitProvider("gitee") || IsGitLabFamily("gitee") || (Service{}).ForGit("gitee").git() != ProviderGitee {
		t.Fatal("gitee must be a git provider but not a gitlab family member")
	}
	if !strings.Contains(gitProviderIn, "'gitee'") {
		t.Fatalf("gitProviderIn = %s", gitProviderIn)
	}
	if !ValidRepositoryNameFor("gitee", "a/b") || ValidRepositoryNameFor("gitee", "a/b/c") {
		t.Fatal("gitee repositories are owner/name")
	}
}

func TestGiteeProviderConfigured(t *testing.T) {
	factory := WithGitee(NewGitClientFactory(gitlab.Options{}), gitee.New("id", "secret", "http://127.0.0.1:1", "http://127.0.0.1:1/api/v5"))
	if (Service{}).GitProviderConfigured("gitee") || (Service{GiteeConfigured: true}).GitProviderConfigured("gitee") ||
		(Service{GitFactory: factory}).GitProviderConfigured("gitee") || !(Service{GiteeConfigured: true, GitFactory: factory}).GitProviderConfigured("gitee") {
		t.Fatal("gitee needs both the server config flag and a client factory")
	}
	if _, err := (Service{GitFactory: WithGitee(NewGitClientFactory(gitlab.Options{}), nil)}).gitClient("gitee", GitInstance{}); err == nil {
		t.Fatal("a nil gitee client must report not configured")
	}
	if client, err := (Service{GitFactory: factory}).gitClient("gitee", GitInstance{}); err != nil || client == nil {
		t.Fatalf("gitee client = %v %v", client, err)
	}
}
