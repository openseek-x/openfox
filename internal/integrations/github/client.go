// Package github 是 GitHub OAuth 与 REST API 的最小客户端。
// 地址来自运维配置而不是用户输入，所以不做 SSRF 拦截，只设置超时与响应体上限。
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"openfox/internal/integrations/gitapi"
)

const (
	// Scope 是申请的 OAuth 权限（用户已确认：repo 与 read:org）。
	Scope = "repo read:org"

	connectTimeout = 10 * time.Second
	totalTimeout   = 20 * time.Second
	maxBodyBytes   = 2 << 20
	maxOrgPages    = 3
)

// 错误与数据类型在 gitapi 里定义，GitHub / GitLab / Gitee 共用；这里保留原名字作为别名，调用方不变。
var (
	// ErrTokenRevoked 表示 GitHub 返回 401：令牌已撤销或过期，连接需要重新授权。
	ErrTokenRevoked = gitapi.ErrTokenRevoked
	// ErrExchangeFailed 表示用授权码换取令牌失败。
	ErrExchangeFailed = gitapi.ErrExchangeFailed
	// ErrRequestFailed 表示其他请求失败（网络错误、非预期状态码、响应异常）。
	ErrRequestFailed = gitapi.ErrRequestFailed
	// ErrNotFound 表示 GitHub 返回 404（仓库、Issue 不存在，或令牌无权访问）。
	ErrNotFound = gitapi.ErrNotFound
	// ErrUnknownResult 表示创建类请求已发出但没有拿到明确响应（超时、连接中断、5xx），GitHub 上可能已经创建成功。
	ErrUnknownResult = gitapi.ErrUnknownResult
)

// ErrRateLimited 表示触发限流（403 / 429 且带限流头）。ResetAt 为空表示响应没有给出恢复时间。
type ErrRateLimited = gitapi.ErrRateLimited

// Client 持有 OAuth App 凭据与服务地址。零值不可用，用 New 构造。
type Client struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	APIURL       string
	HTTP         *http.Client
}

// New 构造带超时的客户端：连接 10 秒，整体 20 秒。
func New(clientID, clientSecret, baseURL, apiURL string) *Client {
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
		TLSHandshakeTimeout: connectTimeout,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	return &Client{ClientID: clientID, ClientSecret: clientSecret,
		BaseURL: strings.TrimRight(baseURL, "/"), APIURL: strings.TrimRight(apiURL, "/"),
		HTTP: &http.Client{Timeout: totalTimeout, Transport: transport}}
}

// User 是 GET /user 的结果；Scopes 来自响应头 X-OAuth-Scopes（实际授权到的权限）。
type User = gitapi.User

// Org 是 GET /user/orgs 里的一个组织。
type Org = gitapi.Org

// AuthorizeURL 生成授权页地址（PKCE S256）。
func (c *Client) AuthorizeURL(state, codeChallenge, redirectURI string) string {
	query := url.Values{}
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("scope", Scope)
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	return c.BaseURL + "/login/oauth/authorize?" + query.Encode()
}

// Exchange 用授权码与 PKCE verifier 换取访问令牌。
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (token, scope string, err error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", ErrExchangeFailed
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", "", ErrExchangeFailed
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", "", ErrExchangeFailed
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Error != "" || payload.AccessToken == "" {
		return "", "", ErrExchangeFailed
	}
	return payload.AccessToken, payload.Scope, nil
}

func (c *Client) do(ctx context.Context, method, target, token string, body io.Reader) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, nil, ErrRequestFailed
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "OpenFox")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, ErrRequestFailed
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, nil, ErrRequestFailed
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp, nil, ErrTokenRevoked
	case resp.StatusCode == http.StatusNotFound:
		return resp, nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden:
		if limited := rateLimitError(resp); limited != nil {
			return resp, nil, limited
		}
	}
	return resp, data, nil
}

// User 读取令牌所属用户。
func (c *Client) User(ctx context.Context, token string) (User, error) {
	resp, data, err := c.do(ctx, http.MethodGet, c.APIURL+"/user", token, nil)
	if err != nil {
		return User{}, err
	}
	var user User
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &user) != nil || user.ID == 0 || user.Login == "" {
		return User{}, ErrRequestFailed
	}
	user.Scopes = resp.Header.Get("X-OAuth-Scopes")
	return user, nil
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// Orgs 读取用户所属组织，按 Link 头翻页，最多 3 页（每页 100）。
func (c *Client) Orgs(ctx context.Context, token string) ([]Org, error) {
	target := c.APIURL + "/user/orgs?per_page=100"
	orgs := make([]Org, 0)
	for page := 0; page < maxOrgPages && target != ""; page++ {
		resp, data, err := c.do(ctx, http.MethodGet, target, token, nil)
		if err != nil {
			return nil, err
		}
		var items []Org
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
			return nil, ErrRequestFailed
		}
		orgs = append(orgs, items...)
		target = ""
		if match := nextLink.FindStringSubmatch(resp.Header.Get("Link")); match != nil && c.sameOrigin(match[1]) {
			target = match[1]
		}
	}
	return orgs, nil
}

// sameOrigin 保证翻页地址仍指向配置的 API 服务，避免把令牌发到别处。
func (c *Client) sameOrigin(target string) bool {
	next, err := url.Parse(target)
	base, err2 := url.Parse(c.APIURL)
	return err == nil && err2 == nil && next.Scheme == base.Scheme && next.Host == base.Host
}

// CreateIssueComment 在 issue / PR 下发评论（PR 评论走 issues 评论接口），返回评论 id。
func (c *Client) CreateIssueComment(ctx context.Context, token, owner, repo string, number int, body string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"body": body})
	target := fmt.Sprintf("%s/repos/%s/%s/issues/%d/comments", c.APIURL, url.PathEscape(owner), url.PathEscape(repo), number)
	resp, data, err := c.do(ctx, http.MethodPost, target, token, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK || json.Unmarshal(data, &created) != nil || created.ID == 0 {
		return "", ErrRequestFailed
	}
	return strconv.FormatInt(created.ID, 10), nil
}

// rateLimitError 识别限流响应：X-RateLimit-Remaining 为 0 或带 Retry-After。
func rateLimitError(resp *http.Response) *ErrRateLimited {
	retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if resp.Header.Get("X-RateLimit-Remaining") != "0" && retryAfter == "" && resp.StatusCode != http.StatusTooManyRequests {
		return nil
	}
	limited := &ErrRateLimited{}
	if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds >= 0 {
		limited.ResetAt = time.Now().Add(time.Duration(seconds) * time.Second)
	} else if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64); err == nil && reset > 0 {
		limited.ResetAt = time.Unix(reset, 0)
	}
	return limited
}

// Repo 是仓库的最小字段集。
type Repo = gitapi.Repo

// IssueUser 是 Issue 里的用户。
type IssueUser = gitapi.IssueUser

// Issue 是 Issue 的最小字段集。
type Issue = gitapi.Issue

func hasNext(resp *http.Response) bool { return nextLink.MatchString(resp.Header.Get("Link")) }

// ListRepos 读取一页仓库（每页 50）。account 为组织时读 /orgs/{login}/repos，个人账号读 /user/repos?affiliation=owner。
// q 在本页结果里按名称过滤（不区分大小写）；hasMore 根据 Link 头判断。
func (c *Client) ListRepos(ctx context.Context, token string, userAccount bool, login, q string, page int) ([]Repo, bool, error) {
	if page < 1 {
		page = 1
	}
	var target string
	if userAccount {
		target = fmt.Sprintf("%s/user/repos?affiliation=owner&per_page=50&page=%d", c.APIURL, page)
	} else {
		target = fmt.Sprintf("%s/orgs/%s/repos?per_page=50&page=%d&sort=full_name", c.APIURL, url.PathEscape(login), page)
	}
	resp, data, err := c.do(ctx, http.MethodGet, target, token, nil)
	if err != nil {
		return nil, false, err
	}
	var items []Repo
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
		return nil, false, ErrRequestFailed
	}
	needle := strings.ToLower(strings.TrimSpace(q))
	repos := make([]Repo, 0, len(items))
	for _, item := range items {
		if userAccount && !strings.EqualFold(item.Owner.Login, login) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(item.FullName), needle) {
			continue
		}
		repos = append(repos, item)
	}
	return repos, hasNext(resp), nil
}

// GetRepo 读取单个仓库。
func (c *Client) GetRepo(ctx context.Context, token, owner, repo string) (Repo, error) {
	resp, data, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s", c.APIURL, url.PathEscape(owner), url.PathEscape(repo)), token, nil)
	if err != nil {
		return Repo{}, err
	}
	var item Repo
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &item) != nil || item.FullName == "" {
		return Repo{}, ErrRequestFailed
	}
	return item, nil
}

// ListIssues 读取一页未关闭的 Issue（每页 100，按创建时间升序），跳过 PR。hasMore 根据 Link 头判断。
func (c *Client) ListIssues(ctx context.Context, token, owner, repo string, page int) ([]Issue, bool, error) {
	if page < 1 {
		page = 1
	}
	target := fmt.Sprintf("%s/repos/%s/%s/issues?state=open&per_page=100&page=%d&sort=created&direction=asc",
		c.APIURL, url.PathEscape(owner), url.PathEscape(repo), page)
	resp, data, err := c.do(ctx, http.MethodGet, target, token, nil)
	if err != nil {
		return nil, false, err
	}
	var items []Issue
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
		return nil, false, ErrRequestFailed
	}
	issues := make([]Issue, 0, len(items))
	for _, item := range items {
		if !item.IsPullRequest() {
			issues = append(issues, item)
		}
	}
	return issues, hasNext(resp), nil
}

// maxCreatorPages 是对账时最多翻的页数（每页 100）。
const maxCreatorPages = 5

// ListIssuesByCreator 列出某用户在 since 之后创建 / 更新过的 Issue（含已关闭，跳过 PR），用于创建结果未知时的对账。
func (c *Client) ListIssuesByCreator(ctx context.Context, token, owner, repo, creator string, since time.Time) ([]Issue, error) {
	query := url.Values{"creator": {creator}, "state": {"all"}, "per_page": {"100"}}
	if !since.IsZero() {
		query.Set("since", since.UTC().Format(time.RFC3339))
	}
	var result []Issue
	for page := 1; page <= maxCreatorPages; page++ {
		query.Set("page", strconv.Itoa(page))
		resp, data, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/%s/issues?%s", c.APIURL, url.PathEscape(owner), url.PathEscape(repo), query.Encode()), token, nil)
		if err != nil {
			return nil, err
		}
		var items []Issue
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
			return nil, ErrRequestFailed
		}
		for _, item := range items {
			if !item.IsPullRequest() {
				result = append(result, item)
			}
		}
		if !hasNext(resp) {
			break
		}
	}
	return result, nil
}

// CreateIssue 创建 Issue。请求已发出但没有明确响应（传输错误、5xx）时返回 ErrUnknownResult。
func (c *Client) CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (Issue, error) {
	if assignees == nil {
		assignees = []string{}
	}
	payload, _ := json.Marshal(map[string]any{"title": title, "body": body, "assignees": assignees})
	resp, data, err := c.do(ctx, http.MethodPost, fmt.Sprintf("%s/repos/%s/%s/issues", c.APIURL, url.PathEscape(owner), url.PathEscape(repo)), token, strings.NewReader(string(payload)))
	if errors.Is(err, ErrRequestFailed) {
		return Issue{}, ErrUnknownResult
	}
	if err != nil {
		return Issue{}, err
	}
	if resp.StatusCode >= 500 {
		return Issue{}, ErrUnknownResult
	}
	var created Issue
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &created) != nil || created.Number <= 0 {
		return Issue{}, ErrRequestFailed
	}
	return created, nil
}

// IssueUpdate 是 PATCH Issue 的内容。Assignees 总是发送（空数组表示清空处理人）。
type IssueUpdate = gitapi.IssueUpdate

// UpdateIssue 修改 Issue。
func (c *Client) UpdateIssue(ctx context.Context, token, owner, repo string, number int, update IssueUpdate) (Issue, error) {
	assignees := update.Assignees
	if assignees == nil {
		assignees = []string{}
	}
	fields := map[string]any{"title": update.Title, "body": update.Body, "state": update.State, "assignees": assignees}
	if update.StateReason != "" {
		fields["state_reason"] = update.StateReason
	}
	payload, _ := json.Marshal(fields)
	resp, data, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("%s/repos/%s/%s/issues/%d", c.APIURL, url.PathEscape(owner), url.PathEscape(repo), number), token, strings.NewReader(string(payload)))
	if err != nil {
		return Issue{}, err
	}
	var updated Issue
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &updated) != nil {
		return Issue{}, ErrRequestFailed
	}
	return updated, nil
}
