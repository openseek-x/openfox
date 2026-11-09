// Package gitapi 定义 Git 服务商（GitHub、GitLab、Gitee）客户端的公共类型、错误与接口。
// 公共层（internal/integrations）只依赖这里的接口；各服务商在自己的包里实现。
package gitapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

var (
	// ErrTokenRevoked 表示服务商返回 401：令牌已撤销或过期，连接需要重新授权。
	ErrTokenRevoked = errors.New("git: token revoked")
	// ErrExchangeFailed 表示用授权码换取令牌失败。
	ErrExchangeFailed = errors.New("git: exchange failed")
	// ErrRequestFailed 表示其他请求失败（网络错误、非预期状态码、响应异常）。
	ErrRequestFailed = errors.New("git: request failed")
	// ErrNotFound 表示服务商返回 404（仓库、Issue 不存在，或令牌无权访问）。
	ErrNotFound = errors.New("git: not found")
	// ErrUnknownResult 表示创建类请求已发出但没有拿到明确响应（超时、连接中断、5xx），服务商上可能已经创建成功。
	ErrUnknownResult = errors.New("git: result unknown")
	// ErrRefreshUnsupported 表示该服务商的令牌不会过期（GitHub OAuth App），没有刷新流程。
	ErrRefreshUnsupported = errors.New("git: token refresh unsupported")
)

// ErrRateLimited 表示触发限流。ResetAt 为空表示响应没有给出恢复时间。
type ErrRateLimited struct{ ResetAt time.Time }

func (e *ErrRateLimited) Error() string { return "git: rate limited" }

// User 是授权用户。Scopes 来自服务商实际授权到的权限（没有则为空）。
type User struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	Scopes    string `json:"-"`
}

// Org 是授权用户所属的组织（GitLab 为顶级群组）。
type Org struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// Repo 是仓库的最小字段集；FullName 为 owner/name（GitLab 可含多级群组）。
type Repo struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Private  bool   `json:"private"`
	HTMLURL  string `json:"html_url"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// IssueUser 是 Issue 里的用户。
type IssueUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Issue 是 Issue 的最小字段集。State 统一为 open / closed。
type Issue struct {
	ID          int64       `json:"id"`
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	State       string      `json:"state"`
	StateReason string      `json:"state_reason"`
	HTMLURL     string      `json:"html_url"`
	User        IssueUser   `json:"user"`
	Assignees   []IssueUser `json:"assignees"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	// PullRequest 非空表示这个条目其实是 PR。
	PullRequest json.RawMessage `json:"pull_request"`
	// Ref 是 API 里引用该 Issue 的标识；为空时就是 Number（GitHub、GitLab）。Gitee 的 Issue 编号是字母数字串，放在这里。
	Ref string `json:"-"`
}

// IsPullRequest 判断条目是否为 PR。
func (i Issue) IsPullRequest() bool {
	return len(i.PullRequest) > 0 && string(i.PullRequest) != "null"
}

// APIRef 返回调用 API 时引用该 Issue 的标识。
func (i Issue) APIRef() string {
	if i.Ref != "" {
		return i.Ref
	}
	return strconv.Itoa(i.Number)
}

// IssueUpdate 是修改 Issue 的内容。Assignees 总是发送（空数组表示清空处理人）。
type IssueUpdate struct {
	Title       string
	Body        string
	State       string // open / closed
	StateReason string // completed / not_planned / reopened；空表示不发送（仅 GitHub 使用）
	Assignees   []string
}

// Token 是一次令牌换取 / 刷新的结果。ExpiresAt 为零值表示不会过期。
type Token struct {
	Access    string
	Refresh   string
	Scope     string
	ExpiresAt time.Time
}

// Client 是公共层使用的服务商客户端。令牌由调用方在事务内读出后传入，所有请求都在事务外发出。
type Client interface {
	AuthorizeURL(state, codeChallenge, redirectURI string) string
	Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, error)
	Refresh(ctx context.Context, refreshToken string) (Token, error)
	User(ctx context.Context, token string) (User, error)
	Orgs(ctx context.Context, token string) ([]Org, error)
	ListRepos(ctx context.Context, token string, userAccount bool, login, q string, page int) ([]Repo, bool, error)
	GetRepo(ctx context.Context, token, owner, repo string) (Repo, error)
	ListIssues(ctx context.Context, token, owner, repo string, page int) ([]Issue, bool, error)
	ListIssuesByCreator(ctx context.Context, token, owner, repo, creator string, since time.Time) ([]Issue, error)
	CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (Issue, error)
	UpdateIssue(ctx context.Context, token, owner, repo, ref string, update IssueUpdate) (Issue, error)
	// CommentIssue / CommentPullRequest 在 Issue / PR（MR）下发评论，返回评论 id。
	CommentIssue(ctx context.Context, token, owner, repo, ref, body string) (string, error)
	CommentPullRequest(ctx context.Context, token, owner, repo string, number int, body string) (string, error)
}
