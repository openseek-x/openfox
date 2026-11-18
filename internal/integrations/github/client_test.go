package github_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations/github"
	fakegithub "openfox/tests/support/fakegithub/fake"
)

func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorize(t *testing.T, c *github.Client, state, verifier string) (code string) {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get(c.AuthorizeURL(state, challenge(verifier), "http://app.example.test/cb"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	location, _ := url.Parse(resp.Header.Get("Location"))
	if location.Query().Get("state") != state {
		t.Fatalf("state = %q", location.Query().Get("state"))
	}
	return location.Query().Get("code")
}

func TestAuthorizeURL(t *testing.T) {
	c := github.New("cid", "secret", "https://github.example.test/", "https://api.example.test")
	parsed, err := url.Parse(c.AuthorizeURL("st", "ch", "https://app/cb"))
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if parsed.Host != "github.example.test" || parsed.Path != "/login/oauth/authorize" || q.Get("scope") != "repo read:org" ||
		q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "cid" || q.Get("state") != "st" {
		t.Fatalf("authorize url = %s", parsed)
	}
	if strings.Contains(parsed.String(), "secret") {
		t.Fatal("authorize url must not contain the client secret")
	}
}

func TestOAuthFlowAgainstFake(t *testing.T) {
	fake := fakegithub.New(fakegithub.Options{ClientID: "cid", ClientSecret: "sec", Login: "lin", Orgs: []string{"a", "b", "c"}, OrgsPageSize: 2})
	defer fake.Close()
	c := github.New("cid", "sec", fake.URL, fake.URL)
	ctx := context.Background()
	verifier := strings.Repeat("v", 43)
	code := authorize(t, c, "state-1", verifier)

	if _, _, err := c.Exchange(ctx, code, "wrong-verifier-wrong-verifier-wrong-verifier", "http://app.example.test/cb"); !errors.Is(err, github.ErrExchangeFailed) {
		t.Fatalf("wrong verifier err = %v", err)
	}
	code = authorize(t, c, "state-2", verifier)
	token, scope, err := c.Exchange(ctx, code, verifier, "http://app.example.test/cb")
	if err != nil || token == "" || scope == "" {
		t.Fatalf("exchange = %q %q %v", token, scope, err)
	}
	// 授权码只能用一次。
	if _, _, err := c.Exchange(ctx, code, verifier, "http://app.example.test/cb"); !errors.Is(err, github.ErrExchangeFailed) {
		t.Fatalf("reused code err = %v", err)
	}
	user, err := c.User(ctx, token)
	if err != nil || user.Login != "lin" || !strings.Contains(user.Scopes, "repo") {
		t.Fatalf("user = %+v %v", user, err)
	}
	orgs, err := c.Orgs(ctx, token)
	if err != nil || len(orgs) != 3 {
		t.Fatalf("orgs = %+v %v", orgs, err)
	}
	id, err := c.CreateIssueComment(ctx, token, "o", "r", 7, "hello")
	if err != nil || id == "" {
		t.Fatalf("comment = %q %v", id, err)
	}
	if got := fake.Comments(); len(got) != 1 || got[0].Number != 7 || got[0].Body != "hello" {
		t.Fatalf("comments = %+v", got)
	}
	fake.RevokeTokens()
	if _, err := c.User(ctx, token); !errors.Is(err, github.ErrTokenRevoked) {
		t.Fatalf("revoked err = %v", err)
	}
	if _, err := c.CreateIssueComment(ctx, token, "o", "r", 7, "x"); !errors.Is(err, github.ErrTokenRevoked) {
		t.Fatalf("revoked comment err = %v", err)
	}
}

func TestOrgsPageLimit(t *testing.T) {
	logins := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	fake := fakegithub.New(fakegithub.Options{ClientID: "cid", ClientSecret: "sec", Orgs: logins, OrgsPageSize: 2})
	defer fake.Close()
	c := github.New("cid", "sec", fake.URL, fake.URL)
	verifier := strings.Repeat("v", 43)
	token, _, err := c.Exchange(context.Background(), authorize(t, c, "s", verifier), verifier, "http://app.example.test/cb")
	if err != nil {
		t.Fatal(err)
	}
	orgs, err := c.Orgs(context.Background(), token)
	if err != nil || len(orgs) != 6 {
		t.Fatalf("expected 3 pages x 2 orgs, got %d (%v)", len(orgs), err)
	}
}

func TestRepoAndIssueCalls(t *testing.T) {
	fake := fakegithub.New(fakegithub.Options{ClientID: "cid", ClientSecret: "secret", Login: "lin", Orgs: []string{"acme"}})
	defer fake.Close()
	fake.AddRepo("acme/app", true)
	fake.AddRepo("acme/site", false)
	fake.AddRepo("lin/own", false)
	c := github.New("cid", "secret", fake.URL, fake.URL)
	code := authorize(t, c, "s", "verifier-1234567890-verifier-1234567890-verifier")
	token, _, err := c.Exchange(context.Background(), code, "verifier-1234567890-verifier-1234567890-verifier", "http://app.example.test/cb")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos, more, err := c.ListRepos(ctx, token, false, "acme", "SIT", 1)
	if err != nil || more || len(repos) != 1 || repos[0].FullName != "acme/site" || repos[0].Private {
		t.Fatalf("org repos = %+v %v %v", repos, more, err)
	}
	if repos, _, err = c.ListRepos(ctx, token, true, "lin", "", 1); err != nil || len(repos) != 1 || repos[0].FullName != "lin/own" {
		t.Fatalf("user repos = %+v %v", repos, err)
	}
	repo, err := c.GetRepo(ctx, token, "acme", "app")
	if err != nil || !repo.Private || repo.Owner.Login != "acme" || repo.HTMLURL == "" {
		t.Fatalf("repo = %+v %v", repo, err)
	}
	if _, err := c.GetRepo(ctx, token, "acme", "missing"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("missing repo err = %v", err)
	}
	created, err := c.CreateIssue(ctx, token, "acme", "app", "T", "B", []string{"octo"})
	if err != nil || created.Number != 1 || created.State != "open" {
		t.Fatalf("created = %+v %v", created, err)
	}
	fake.SeedIssue("acme/app", "A PR", "", true)
	issues, more, err := c.ListIssues(ctx, token, "acme", "app", 1)
	if err != nil || more || len(issues) != 1 || issues[0].IsPullRequest() {
		t.Fatalf("issues = %+v %v %v", issues, more, err)
	}
	updated, err := c.UpdateIssue(ctx, token, "acme", "app", 1, github.IssueUpdate{Title: "T2", Body: "B", State: "closed", StateReason: "not_planned"})
	if err != nil || updated.Title != "T2" || updated.State != "closed" || updated.StateReason != "not_planned" || len(updated.Assignees) != 0 {
		t.Fatalf("updated = %+v %v", updated, err)
	}
	mine, err := c.ListIssuesByCreator(ctx, token, "acme", "app", "lin", time.Now().Add(-time.Hour))
	if err != nil || len(mine) != 1 || mine[0].Number != 1 {
		t.Fatalf("by creator = %+v %v", mine, err)
	}
	// 限流：403 + X-RateLimit-Remaining=0 映射为 ErrRateLimited。
	reset := time.Now().Add(time.Hour).Truncate(time.Second)
	fake.SetRateLimit(reset)
	_, err = c.GetRepo(ctx, token, "acme", "app")
	var limited *github.ErrRateLimited
	if !errors.As(err, &limited) || !limited.ResetAt.Equal(reset) {
		t.Fatalf("rate limit err = %v", err)
	}
	fake.SetRateLimit(time.Time{})
	// 创建时连接被断开：结果未知。
	fake.DropNextIssueCreate()
	if _, err := c.CreateIssue(ctx, token, "acme", "app", "dropped", "", nil); !errors.Is(err, github.ErrUnknownResult) {
		t.Fatalf("dropped err = %v", err)
	}
	fake.RevokeTokens()
	if _, err := c.GetRepo(ctx, token, "acme", "app"); !errors.Is(err, github.ErrTokenRevoked) {
		t.Fatalf("revoked err = %v", err)
	}
}
