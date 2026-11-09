package integrations

// / 7g-3：Git 服务商参数化。GitHub（6b）、GitLab（自部署）、极狐 GitLab、Gitee 共用同一套公共层
// （OAuth 绑定、Webhook、PR / MR 与 Commit 关联任务、Issue 同步）：
//   - SQL 以 'github' 字面量为模板，运行时只会被替换成白名单内的服务商标识（与 7g-1 的 forProv 同一做法）；
//   - 服务商客户端实现 gitapi.Client；GitHub 的老客户端用 githubAdapter 适配，行为不变；
//   - GitLab 的实例地址与 OAuth 应用凭据属于组织连接（管理员绑定时填写），所以客户端按连接构造（GitClientFactory）。

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"openfox/internal/gitlabproxy"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/integrations/gitee"
	"openfox/internal/integrations/github"
	"openfox/internal/integrations/gitlab"
)

const (
	ProviderGitHub   = "github"
	ProviderGitLab   = "gitlab"
	ProviderGitLabCN = "gitlab_cn"
	ProviderGitee    = "gitee"
)

// ErrInvalidInstance 表示 GitLab 实例地址不合法（非 https、内网 / 回环地址等）。
var ErrInvalidInstance = errors.New("integrations: instance url is invalid")

// gitProviderList 是支持的 Git 服务商白名单。
var gitProviderList = []string{ProviderGitHub, ProviderGitLab, ProviderGitLabCN, ProviderGitee}

// gitProviderIn 是 SQL 里匹配全部 Git 服务商的 IN 列表。
const gitProviderIn = "('github', 'gitlab', 'gitlab_cn', 'gitee')"

// IsGitProvider 判断服务商是否是 Git 服务商。
func IsGitProvider(provider string) bool {
	for _, item := range gitProviderList {
		if item == provider {
			return true
		}
	}
	return false
}

// IsGitLabFamily 判断是否是 GitLab 系（自部署 GitLab、极狐）：实例地址与 OAuth 应用由组织填写。
func IsGitLabFamily(provider string) bool {
	return provider == ProviderGitLab || provider == ProviderGitLabCN
}

// DefaultGitInstanceURL 返回服务商的默认实例地址（极狐 SaaS 为 jihulab.com；自部署 GitLab 没有默认值）。
func DefaultGitInstanceURL(provider string) string {
	if provider == ProviderGitLabCN {
		return "https://jihulab.com"
	}
	return ""
}

// GitInstance 是一个连接所属服务商实例的信息。GitHub / Gitee 为空（用服务端配置）。
type GitInstance struct {
	Proxy         gitlabproxy.Binding
	PersonalToken bool
	BaseURL       string
	ClientID      string
	ClientSecret  string
	// RedirectURI 由 gitClient 填入：刷新令牌时需要与授权时一致的回调地址。
	RedirectURI string
}

// GitClientFactory 按服务商与实例信息构造客户端；由 API 层按服务端配置注入。
type GitClientFactory func(provider string, inst GitInstance) (gitapi.Client, error)

// git 返回当前 Git 服务商标识；零值按 GitHub 处理，保持 6b 的行为不变。
func (s Service) git() string {
	if s.Git == "" {
		return ProviderGitHub
	}
	return s.Git
}

// ForGit 返回绑定到指定 Git 服务商的 Service 副本；不在白名单内时退回 GitHub。
func (s Service) ForGit(provider string) Service {
	out := s
	if IsGitProvider(provider) {
		out.Git = provider
	} else {
		out.Git = ProviderGitHub
	}
	return out
}

// gq 把模板 SQL 里的 'github' 字面量换成当前 Git 服务商（白名单内）。
func (s Service) gq(query string) string { return gitQuery(query, s.git()) }

func gitQuery(query, provider string) string {
	if provider == ProviderGitHub || !IsGitProvider(provider) {
		return query
	}
	return strings.ReplaceAll(query, "'github'", "'"+provider+"'")
}

// GitProviderConfigured 判断服务商在服务端是否可用：GitHub / Gitee 要有服务端配置的 OAuth 应用，GitLab 系只要有工厂（凭据由组织填写）。
func (s Service) GitProviderConfigured(provider string) bool {
	switch provider {
	case ProviderGitHub:
		return s.GitHubConfigured && s.GitHub != nil
	case ProviderGitLab, ProviderGitLabCN:
		return s.GitFactory != nil
	case ProviderGitee:
		return s.GiteeConfigured && s.GitFactory != nil
	}
	return false
}

// gitClient 取服务商客户端。GitHub 用服务端单例，其余走工厂。
func (s Service) gitClient(provider string, inst GitInstance) (gitapi.Client, error) {
	if provider == ProviderGitHub {
		if s.GitHub == nil {
			return nil, ErrNotConfigured
		}
		return githubAdapter{s.GitHub}, nil
	}
	if !IsGitProvider(provider) || s.GitFactory == nil {
		return nil, ErrNotConfigured
	}
	inst.RedirectURI = s.RedirectURIFor(provider)
	return s.GitFactory(provider, inst)
}

// RedirectURIFor 是各服务商 OAuth 应用的回调地址。
func (s Service) RedirectURIFor(provider string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/integrations/" + provider + "/callback"
}

// githubAdapter 把 GitHub 老客户端适配成 gitapi.Client。
type githubAdapter struct{ c *github.Client }

func (a githubAdapter) AuthorizeURL(state, challenge, redirectURI string) string {
	return a.c.AuthorizeURL(state, challenge, redirectURI)
}

func (a githubAdapter) Exchange(ctx context.Context, code, verifier, redirectURI string) (gitapi.Token, error) {
	token, scope, err := a.c.Exchange(ctx, code, verifier, redirectURI)
	return gitapi.Token{Access: token, Scope: scope}, err
}

func (a githubAdapter) Refresh(context.Context, string) (gitapi.Token, error) {
	return gitapi.Token{}, gitapi.ErrRefreshUnsupported
}

func (a githubAdapter) User(ctx context.Context, token string) (gitapi.User, error) {
	return a.c.User(ctx, token)
}

func (a githubAdapter) Orgs(ctx context.Context, token string) ([]gitapi.Org, error) {
	return a.c.Orgs(ctx, token)
}

func (a githubAdapter) ListRepos(ctx context.Context, token string, userAccount bool, login, q string, page int) ([]gitapi.Repo, bool, error) {
	return a.c.ListRepos(ctx, token, userAccount, login, q, page)
}

func (a githubAdapter) GetRepo(ctx context.Context, token, owner, repo string) (gitapi.Repo, error) {
	return a.c.GetRepo(ctx, token, owner, repo)
}

func (a githubAdapter) ListIssues(ctx context.Context, token, owner, repo string, page int) ([]gitapi.Issue, bool, error) {
	return a.c.ListIssues(ctx, token, owner, repo, page)
}

func (a githubAdapter) ListIssuesByCreator(ctx context.Context, token, owner, repo, creator string, since time.Time) ([]gitapi.Issue, error) {
	return a.c.ListIssuesByCreator(ctx, token, owner, repo, creator, since)
}

func (a githubAdapter) CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (gitapi.Issue, error) {
	return a.c.CreateIssue(ctx, token, owner, repo, title, body, assignees)
}

func (a githubAdapter) UpdateIssue(ctx context.Context, token, owner, repo, ref string, update gitapi.IssueUpdate) (gitapi.Issue, error) {
	number, err := strconv.Atoi(ref)
	if err != nil {
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	return a.c.UpdateIssue(ctx, token, owner, repo, number, update)
}

func (a githubAdapter) CommentIssue(ctx context.Context, token, owner, repo, ref, body string) (string, error) {
	number, err := strconv.Atoi(ref)
	if err != nil {
		return "", gitapi.ErrRequestFailed
	}
	return a.c.CreateIssueComment(ctx, token, owner, repo, number, body)
}

// CommentPullRequest：GitHub 的 PR 评论走 issues 评论接口。
func (a githubAdapter) CommentPullRequest(ctx context.Context, token, owner, repo string, number int, body string) (string, error) {
	return a.c.CreateIssueComment(ctx, token, owner, repo, number, body)
}

// personalParent 返回按所属组织连接过滤个人连接的参数：GitLab 系的个人连接挂在组织连接（实例）下，其他服务商不过滤（nil）。
func (s Service) personalParent(connectionID string) any {
	if IsGitLabFamily(s.git()) && connectionID != "" {
		return connectionID
	}
	return nil
}

// personalParentArg 同 personalParent，但按连接 ID 直接取参数（出站 worker 用）。
func (s Service) personalParentArg(connectionID string) any { return s.personalParent(connectionID) }

// parentArg 返回出站项所属组织连接对应的个人连接过滤参数。
func (d sendData) parentArg() any { return Service{Git: d.provider}.personalParent(d.connID) }

// NewGitClientFactory 构造 GitLab 系（按实例构造）的客户端工厂。
func NewGitClientFactory(gl gitlab.Options, brokers ...gitlabproxy.Broker) GitClientFactory {
	return func(provider string, inst GitInstance) (gitapi.Client, error) {
		switch provider {
		case ProviderGitLab, ProviderGitLabCN:
			// PAT connections do not have an OAuth application. Authorization
			// entry points validate application credentials when OAuth is used.
			if inst.BaseURL == "" {
				return nil, ErrNotConfigured
			}
			client := gitlab.New(inst.BaseURL, inst.ClientID, inst.ClientSecret, inst.RedirectURI, gl)
			if inst.Proxy.Device != "" {
				if len(brokers) != 1 || provider != ProviderGitLab {
					return nil, ErrNotConfigured
				}
				proxy, err := brokers[0].Client(inst.Proxy, inst.BaseURL)
				if err != nil {
					return nil, err
				}
				client.HTTP = proxy
			}
			client.PersonalTokenAuth = inst.PersonalToken
			return client, nil
		}
		return nil, ErrNotConfigured
	}
}

// WithGitee 在工厂上增加 Gitee：Gitee 是单一 SaaS，客户端是服务端配置的单例（凭据来自 OPENFOX_GITEE_*），不看连接的实例信息。
// client 为 nil（未配置）时 Gitee 构造返回 ErrNotConfigured。
func WithGitee(next GitClientFactory, client *gitee.Client) GitClientFactory {
	return func(provider string, inst GitInstance) (gitapi.Client, error) {
		if provider == ProviderGitee {
			if client == nil {
				return nil, ErrNotConfigured
			}
			return client, nil
		}
		return next(provider, inst)
	}
}

// NewInstanceValidator 返回 GitLab 实例地址校验函数（SSRF 防护，规则见 gitlab.ValidateInstanceURL）。
func NewInstanceValidator(gl gitlab.Options) func(ctx context.Context, rawURL string) (string, error) {
	return func(ctx context.Context, rawURL string) (string, error) {
		return gitlab.ValidateInstanceURL(ctx, rawURL, gl)
	}
}
