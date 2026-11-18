//go:build live

// Package live 存放需要真实第三方账号的联调测试（阶段 12c）。
//
// 默认不参与任何常规测试：需要构建标签 live，并且显式设置环境变量。
// 凭据只从环境变量读取，不得写入仓库、日志或开发记录。
//
//	set -a; . ~/.openfox-secrets/stage12c.env; set +a
//	OPENFOX_LIVE_GITHUB=1 go test -tags live ./tests/live -run GitHub -count=1 -v
package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations/github"
)

// liveRepo 是唯一允许写入的仓库：用户指定的专用测试仓库。写操作绝不指向其他仓库。
const (
	liveOwner = "openseek-x"
	liveRepo  = "github-integration-test"
)

func liveGitHub(t *testing.T) (*github.Client, string) {
	t.Helper()
	if os.Getenv("OPENFOX_LIVE_GITHUB") != "1" {
		t.Skip("set OPENFOX_LIVE_GITHUB=1 to run against the real GitHub API")
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TEST_TOKEN"))
	if token == "" {
		t.Fatal("GITHUB_TEST_TOKEN is empty")
	}
	// 只用到 REST API；OAuth App 凭据留空（授权流程需要浏览器，由用户验收）。
	return github.New("", "", "https://github.com", "https://api.github.com"), token
}

func liveContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestGitHubLiveReadPaths 核对只读路径在真实接口上的行为。
func TestGitHubLiveReadPaths(t *testing.T) {
	client, token := liveGitHub(t)
	ctx := liveContext(t)

	user, err := client.User(ctx, token)
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if user.Login != liveOwner || user.ID == 0 {
		t.Fatalf("unexpected user %q id=%d", user.Login, user.ID)
	}
	t.Logf("user ok: login=%s scopes=%q（细粒度令牌没有 X-OAuth-Scopes，为空属正常）", user.Login, user.Scopes)

	repo, err := client.GetRepo(ctx, token, liveOwner, liveRepo)
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if repo.FullName != liveOwner+"/"+liveRepo || !repo.Private {
		t.Fatalf("unexpected repo %+v", repo)
	}

	repos, _, err := client.ListRepos(ctx, token, true, liveOwner, "integration-test", 1)
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	found := false
	for _, item := range repos {
		found = found || item.FullName == liveOwner+"/"+liveRepo
	}
	if !found {
		t.Fatalf("test repo not found in ListRepos (got %d)", len(repos))
	}

	// 不存在的仓库：应映射为 ErrNotFound，而不是通用失败。
	if _, err := client.GetRepo(ctx, token, liveOwner, "definitely-missing-repo-12c"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("missing repo: want ErrNotFound, got %v", err)
	}
	// 无效令牌：应映射为 ErrTokenRevoked（连接需要重新授权）。
	if _, err := client.User(ctx, "github_pat_invalid_token_for_live_test"); !errors.Is(err, github.ErrTokenRevoked) {
		t.Fatalf("invalid token: want ErrTokenRevoked, got %v", err)
	}
}

// TestGitHubLiveIssueLifecycle 在专用测试仓库里走一遍 Issue 的创建、修改、评论、关闭，
// 对应 OpenFox 的「任务 → Issue 出站同步」与「评论回写」路径。结束时把 Issue 关闭（GitHub 不支持经 REST 删除 Issue）。
func TestGitHubLiveIssueLifecycle(t *testing.T) {
	client, token := liveGitHub(t)
	ctx := liveContext(t)
	stamp := time.Now().UTC().Format("20060102-150405")
	title := "[OpenFox 12c 联调] " + stamp

	created, err := client.CreateIssue(ctx, token, liveOwner, liveRepo, title, "由 OpenFox 阶段 12c 真实接口联调测试创建，可直接关闭。", nil)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if created.Number == 0 || created.State != "open" || created.Title != title || created.User.Login != liveOwner {
		t.Fatalf("unexpected created issue %+v", created)
	}
	number := created.Number
	t.Cleanup(func() {
		// 无论中途是否失败，都尽量把测试 Issue 关掉。
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = client.UpdateIssue(closeCtx, token, liveOwner, liveRepo, number, github.IssueUpdate{
			Title: title, Body: "联调结束，已关闭。", State: "closed", StateReason: "completed",
		})
	})
	t.Logf("created issue #%d", number)

	// 指派给自己并改标题与正文。
	updated, err := client.UpdateIssue(ctx, token, liveOwner, liveRepo, number, github.IssueUpdate{
		Title: title + "（已更新）", Body: "正文已更新。\n\n- 含换行\n- 含 `代码`", State: "open", Assignees: []string{liveOwner},
	})
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if updated.Title != title+"（已更新）" || len(updated.Assignees) != 1 || updated.Assignees[0].Login != liveOwner {
		t.Fatalf("unexpected updated issue %+v", updated)
	}

	commentID, err := client.CreateIssueComment(ctx, token, liveOwner, liveRepo, number, "OpenFox 评论回写联调："+stamp)
	if err != nil {
		t.Fatalf("CreateIssueComment: %v", err)
	}
	if commentID == "" {
		t.Fatal("empty comment id")
	}

	// 入站：按创建人增量列出，应包含刚创建的 Issue。
	since := created.CreatedAt.Add(-time.Minute)
	mine, err := client.ListIssuesByCreator(ctx, token, liveOwner, liveRepo, liveOwner, since)
	if err != nil {
		t.Fatalf("ListIssuesByCreator: %v", err)
	}
	seen := false
	for _, item := range mine {
		seen = seen || item.Number == number
	}
	if !seen {
		t.Fatalf("issue #%d not returned by ListIssuesByCreator (got %d)", number, len(mine))
	}

	// 未关闭列表应包含它；翻页直到找到或没有下一页（最多 10 页，避免无界请求）。
	open := false
	for page := 1; page <= 10 && !open; page++ {
		items, more, err := client.ListIssues(ctx, token, liveOwner, liveRepo, page)
		if err != nil {
			t.Fatalf("ListIssues page %d: %v", page, err)
		}
		for _, item := range items {
			open = open || item.Number == number
		}
		if !more {
			break
		}
	}
	if !open {
		t.Fatalf("issue #%d not in open list", number)
	}

	// 关闭并带原因；再按「不计划」重新关闭，核对 state_reason 的往返。
	closed, err := client.UpdateIssue(ctx, token, liveOwner, liveRepo, number, github.IssueUpdate{
		Title: title + "（已更新）", Body: "联调结束。", State: "closed", StateReason: "not_planned",
	})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.State != "closed" || closed.StateReason != "not_planned" {
		t.Fatalf("unexpected closed issue state=%s reason=%s", closed.State, closed.StateReason)
	}

	// 对不存在的 Issue 评论：应是 ErrNotFound。
	if _, err := client.CreateIssueComment(ctx, token, liveOwner, liveRepo, 99999999, "x"); !errors.Is(err, github.ErrNotFound) {
		t.Fatalf("comment on missing issue: want ErrNotFound, got %v", err)
	}
	fmt.Fprintf(os.Stderr, "live issue lifecycle ok: %s/%s#%d\n", liveOwner, liveRepo, number)
}
