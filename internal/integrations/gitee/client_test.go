package gitee_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitee"
	fakegitee "openfox/tests/support/fakegitee/fake"
)

func setup(t *testing.T) (*gitee.Client, *fakegitee.Server) {
	t.Helper()
	fake := fakegitee.New(fakegitee.Options{ClientID: "cid", ClientSecret: "csecret", Login: "lin", UserID: 11, Orgs: []string{"org1"}})
	t.Cleanup(fake.Close)
	fake.AddRepo("org1/app", true)
	fake.AddRepo("org1/site", false)
	fake.AddRepo("lin/mine", false)
	return gitee.New("cid", "csecret", fake.URL, fake.URL+"/api/v5"), fake
}

// token 走完授权码流程，返回令牌。
func token(t *testing.T, client *gitee.Client, fake *fakegitee.Server) gitapi.Token {
	t.Helper()
	authorize := client.AuthorizeURL("state1", "ignored-challenge", "http://app.test/cb")
	parsed, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if !strings.HasPrefix(authorize, fake.URL+"/oauth/authorize?") || q.Get("response_type") != "code" || q.Get("client_id") != "cid" ||
		q.Get("state") != "state1" || q.Get("redirect_uri") != "http://app.test/cb" || q.Get("scope") != gitee.Scope ||
		q.Has("code_challenge") || q.Has("code_challenge_method") || strings.Contains(authorize, "csecret") {
		t.Fatalf("authorize url = %s", authorize)
	}
	code := obtainCode(t, authorize)
	tok, err := client.Exchange(context.Background(), code, "unused-verifier", "http://app.test/cb")
	if err != nil || tok.Access == "" || tok.Refresh == "" || time.Until(tok.ExpiresAt) < 23*time.Hour {
		t.Fatalf("exchange = %+v %v", tok, err)
	}
	return tok
}

func TestOAuthFlow(t *testing.T) {
	client, fake := setup(t)
	ctx := context.Background()
	tok := token(t, client, fake)
	if len(fake.Challenges()) != 0 || fake.MissingUserAgent() != 0 {
		t.Fatalf("challenges = %v, missing user agent = %d", fake.Challenges(), fake.MissingUserAgent())
	}
	// 授权码只能用一次；错误的 Secret 换不到令牌。
	if _, err := client.Exchange(ctx, "gcode-1", "", "http://app.test/cb"); !errors.Is(err, gitapi.ErrExchangeFailed) {
		t.Fatalf("reused code = %v", err)
	}
	bad := gitee.New("cid", "wrong", fake.URL, fake.URL+"/api/v5")
	if _, err := bad.Exchange(ctx, "x", "", "http://app.test/cb"); !errors.Is(err, gitapi.ErrExchangeFailed) {
		t.Fatalf("wrong secret = %v", err)
	}
	// 刷新：换出新令牌，旧令牌立即失效。
	next, err := client.Refresh(ctx, tok.Refresh)
	if err != nil || next.Access == tok.Access || next.Refresh == "" {
		t.Fatalf("refresh = %+v %v", next, err)
	}
	if _, err := client.User(ctx, tok.Access); !errors.Is(err, gitapi.ErrTokenRevoked) {
		t.Fatalf("old token = %v", err)
	}
	if _, err := client.User(ctx, next.Access); err != nil {
		t.Fatal(err)
	}
	// 旧刷新令牌已失效 → ErrTokenRevoked；服务故障 → ErrRequestFailed（不当作撤销）。
	if _, err := client.Refresh(ctx, tok.Refresh); !errors.Is(err, gitapi.ErrTokenRevoked) {
		t.Fatalf("stale refresh token = %v", err)
	}
	fake.SetRefreshStatus(500)
	if _, err := client.Refresh(ctx, next.Refresh); !errors.Is(err, gitapi.ErrRequestFailed) || errors.Is(err, gitapi.ErrTokenRevoked) {
		t.Fatalf("server failure = %v", err)
	}
}

func TestReadAPIs(t *testing.T) {
	client, fake := setup(t)
	ctx := context.Background()
	tok := token(t, client, fake).Access
	user, err := client.User(ctx, tok)
	if err != nil || user.ID != 11 || user.Login != "lin" || user.Scopes != gitee.Scope {
		t.Fatalf("user = %+v %v", user, err)
	}
	orgs, err := client.Orgs(ctx, tok)
	if err != nil || len(orgs) != 1 || orgs[0].Login != "org1" {
		t.Fatalf("orgs = %+v %v", orgs, err)
	}
	// 仓库全名是「空间 path / 仓库 path」，不是中文显示名。
	repos, more, err := client.ListRepos(ctx, tok, false, "org1", "", 1)
	if err != nil || more || len(repos) != 2 || repos[0].FullName != "org1/app" || !repos[0].Private || repos[1].FullName != "org1/site" || repos[1].Owner.Login != "org1" {
		t.Fatalf("org repos = %+v %v %v", repos, more, err)
	}
	if repos, _, err = client.ListRepos(ctx, tok, false, "org1", "SITE", 1); err != nil || len(repos) != 1 || repos[0].FullName != "org1/site" {
		t.Fatalf("search = %+v %v", repos, err)
	}
	if repos, _, err = client.ListRepos(ctx, tok, true, "lin", "mine", 1); err != nil || len(repos) != 1 || repos[0].FullName != "lin/mine" {
		t.Fatalf("user repos = %+v %v", repos, err)
	}
	repo, err := client.GetRepo(ctx, tok, "org1", "app")
	if err != nil || repo.FullName != "org1/app" || !repo.Private || repo.Name != "app" {
		t.Fatalf("repo = %+v %v", repo, err)
	}
	if _, err = client.GetRepo(ctx, tok, "org1", "missing"); !errors.Is(err, gitapi.ErrNotFound) {
		t.Fatalf("missing repo = %v", err)
	}
	if _, err = client.User(ctx, "bogus"); !errors.Is(err, gitapi.ErrTokenRevoked) {
		t.Fatalf("bogus token = %v", err)
	}
}

func TestIssuesUseAlphanumericNumbers(t *testing.T) {
	client, fake := setup(t)
	ctx := context.Background()
	tok := token(t, client, fake).Access
	fake.AddUser(22, "ming")
	created, err := client.CreateIssue(ctx, tok, "org1", "app", "Title", "Body", []string{"ming", "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	// Number 是数字 id（放得进 int32），Ref 才是 API 里的字母数字串编号。
	if created.Number <= 0 || int64(created.Number) != created.ID || !strings.HasPrefix(created.Ref, "IT") || created.APIRef() != created.Ref ||
		created.State != "open" || len(created.Assignees) != 1 || created.Assignees[0].Login != "ming" || created.User.Login != "lin" {
		t.Fatalf("created = %+v", created)
	}
	if fake.QueryTokenWrites() != 0 {
		t.Fatal("the token of a write request must not be in the URL")
	}
	updated, err := client.UpdateIssue(ctx, tok, "org1", "app", created.Ref, gitapi.IssueUpdate{Title: "New", Body: "B2", State: "closed"})
	if err != nil || updated.Title != "New" || updated.State != "closed" || len(updated.Assignees) != 0 || updated.Ref != created.Ref {
		t.Fatalf("updated = %+v %v", updated, err)
	}
	if _, err = client.UpdateIssue(ctx, tok, "org1", "app", "IZZZZ", gitapi.IssueUpdate{Title: "x", State: "open"}); !errors.Is(err, gitapi.ErrNotFound) {
		t.Fatalf("missing issue = %v", err)
	}
	// 列表只含开启的；按创建人对账含已关闭。
	open, _, err := client.ListIssues(ctx, tok, "org1", "app", 1)
	if err != nil || len(open) != 0 {
		t.Fatalf("open = %+v %v", open, err)
	}
	mine, err := client.ListIssuesByCreator(ctx, tok, "org1", "app", "lin", time.Time{})
	if err != nil || len(mine) != 1 || mine[0].Ref != created.Ref {
		t.Fatalf("by creator = %+v %v", mine, err)
	}
	if mine, err = client.ListIssuesByCreator(ctx, tok, "org1", "app", "nobody", time.Time{}); err != nil || len(mine) != 0 {
		t.Fatalf("other creator = %+v %v", mine, err)
	}
	// 评论：Issue 用字母数字串编号，PR 用数字编号。
	if id, err := client.CommentIssue(ctx, tok, "org1", "app", created.Ref, "hello"); err != nil || id == "" {
		t.Fatalf("comment = %q %v", id, err)
	}
	if id, err := client.CommentPullRequest(ctx, tok, "org1", "app", 7, "pr hello"); err != nil || id == "" {
		t.Fatalf("pr comment = %q %v", id, err)
	}
	notes := fake.Notes()
	if len(notes) != 2 || notes[0].Kind != "issues" || notes[0].Number != created.Ref || notes[1].Kind != "pulls" || notes[1].Number != "7" || notes[0].Token != tok {
		t.Fatalf("notes = %+v", notes)
	}
}

func TestIssueEdgeCases(t *testing.T) {
	client, fake := setup(t)
	ctx := context.Background()
	tok := token(t, client, fake).Access
	// 创建结果未知（连接中断 / 5xx）。
	fake.DropNextCreate()
	if _, err := client.CreateIssue(ctx, tok, "org1", "app", "t", "b", nil); !errors.Is(err, gitapi.ErrUnknownResult) {
		t.Fatalf("dropped = %v", err)
	}
	fake.SetWriteStatus(502)
	if _, err := client.CreateIssue(ctx, tok, "org1", "app", "t", "b", nil); !errors.Is(err, gitapi.ErrUnknownResult) {
		t.Fatalf("5xx = %v", err)
	}
	fake.SetWriteStatus(0)
	// 限流：429 + Retry-After。
	fake.SetRateLimit(time.Hour)
	_, _, err := client.ListIssues(ctx, tok, "org1", "app", 1)
	var limited *gitapi.ErrRateLimited
	if !errors.As(err, &limited) || time.Until(limited.ResetAt) < 59*time.Minute {
		t.Fatalf("rate limited = %v", err)
	}
	fake.SetRateLimit(0)
	// 令牌失效 → ErrTokenRevoked；仓库不存在 → ErrNotFound。
	fake.RevokeTokens()
	if _, _, err = client.ListIssues(ctx, tok, "org1", "app", 1); !errors.Is(err, gitapi.ErrTokenRevoked) {
		t.Fatalf("revoked = %v", err)
	}
	// 错误信息不含令牌、Secret 或 URL。
	if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), "csecret") {
		t.Fatalf("error leaks: %v", err)
	}
	if gitee.IssueState("progressing") != "open" || gitee.IssueState("rejected") != "closed" || gitee.IssueState("closed") != "closed" || gitee.IssueState("open") != "open" {
		t.Fatal("IssueState mapping")
	}
}
