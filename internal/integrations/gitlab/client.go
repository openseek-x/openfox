// Package gitlab 是 GitLab（自部署）与极狐 GitLab 的 OAuth 与 REST API v4 最小客户端。
// 实例地址由组织管理员填写，属于用户输入，所以：生产环境只允许 https、拒绝解析到内网 / 回环 / 链路本地的地址（含 DNS 重绑定与重定向复查，
// 复用 6c 的 safehttp）；开发 / 测试环境（AllowLocal）允许 http 与本机地址，便于对接本机模拟服务。
// 文档出处：https://docs.gitlab.com/api/oauth2/ 、https://docs.gitlab.com/api/rest/ 、https://docs.gitlab.com/api/issues/ 、
// https://docs.gitlab.com/api/merge_requests/ 、https://docs.gitlab.com/api/notes/ 、https://docs.gitlab.com/api/groups/ 。
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openfox/internal/integrations/gitapi"
	"openfox/internal/platform/safehttp"
)

const (
	// Scope 是申请的 OAuth 权限：api 才能创建 / 修改 Issue 与评论。
	Scope = "api"

	requestTimeout = 20 * time.Second
	connectTimeout = 10 * time.Second
	maxBodyBytes   = 2 << 20
	maxGroupPages  = 3
)

// Options 控制出站防护。零值就是生产的严格配置。
type Options struct {
	// AllowLocal 允许 http 与回环 / 私网实例地址（仅开发、测试环境）。
	AllowLocal bool
	// Safe 是严格模式下传给 safehttp 的选项（测试注入根证书 / 解析器）。
	Safe safehttp.Options
}

// ValidateInstanceURL 校验并规范化实例地址，返回不带末尾斜杠的地址。
func ValidateInstanceURL(ctx context.Context, raw string, opts Options) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return "", safehttp.ErrInvalidURL
	}
	var u *url.URL
	if opts.AllowLocal {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil ||
			parsed.Fragment != "" || parsed.RawQuery != "" || parsed.Opaque != "" {
			return "", safehttp.ErrInvalidURL
		}
		u = parsed
	} else {
		parsed, err := safehttp.CheckURL(raw)
		if err != nil || parsed.RawQuery != "" {
			return "", safehttp.ErrInvalidURL
		}
		u = parsed
		if err := checkHost(ctx, strings.ToLower(u.Hostname()), opts.Safe); err != nil {
			return "", err
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func checkHost(ctx context.Context, host string, safe safehttp.Options) error {
	allow := safe.AllowAddr
	if allow == nil {
		allow = safehttp.IPAllowed
	}
	if ip := net.ParseIP(host); ip != nil {
		if !allow(ip) {
			return safehttp.ErrBlockedAddress
		}
		return nil
	}
	lookup := safe.LookupIP
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			ips := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				ips = append(ips, a.IP)
			}
			return ips, err
		}
	}
	lookupCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	ips, err := lookup(lookupCtx, host)
	if err != nil || len(ips) == 0 {
		return safehttp.ErrInvalidURL
	}
	for _, ip := range ips {
		if !allow(ip) {
			return safehttp.ErrBlockedAddress
		}
	}
	return nil
}

// Client 是一个 GitLab 实例 + 一个 OAuth 应用的客户端。
type Client struct {
	PersonalTokenAuth bool
	BaseURL           string
	ClientID          string
	ClientSecret      string
	RedirectURI       string
	HTTP              *http.Client
}

// New 构造客户端。baseURL 应已通过 ValidateInstanceURL。
func New(baseURL, clientID, clientSecret, redirectURI string, opts Options) *Client {
	var httpClient *http.Client
	if opts.AllowLocal {
		httpClient = &http.Client{
			Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: connectTimeout}).DialContext, TLSHandshakeTimeout: connectTimeout,
				MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second},
			// 重定向不能把令牌带到别的主机。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 3 || req.URL.Host != via[0].URL.Host {
					return http.ErrUseLastResponse
				}
				return nil
			},
		}
	} else {
		httpClient = safehttp.Client(opts.Safe)
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), ClientID: clientID, ClientSecret: clientSecret, RedirectURI: redirectURI, HTTP: httpClient}
}

var _ gitapi.Client = (*Client)(nil)

// AuthorizeURL 生成授权页地址（授权码 + PKCE S256）。
func (c *Client) AuthorizeURL(state, codeChallenge, redirectURI string) string {
	query := url.Values{}
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", Scope)
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	return c.BaseURL + "/oauth/authorize?" + query.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	CreatedAt    int64  `json:"created_at"`
	Error        string `json:"error"`
}

func (r tokenResponse) token() gitapi.Token {
	token := gitapi.Token{Access: r.AccessToken, Refresh: r.RefreshToken, Scope: r.Scope}
	if r.ExpiresIn > 0 {
		base := time.Now()
		if r.CreatedAt > 0 {
			base = time.Unix(r.CreatedAt, 0)
		}
		token.ExpiresAt = base.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	return token
}

// postToken 请求 /oauth/token。换取授权码失败统一为 ErrExchangeFailed；刷新时 4xx（invalid_grant 等）说明刷新令牌已失效，
// 返回 ErrTokenRevoked，网络错误与 5xx 返回 ErrRequestFailed（下一轮再试，不把连接标成需要重新授权）。
func (c *Client) postToken(ctx context.Context, form url.Values, refresh bool) (gitapi.Token, error) {
	fail := gitapi.ErrExchangeFailed
	if refresh {
		fail = gitapi.ErrRequestFailed
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return gitapi.Token{}, fail
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return gitapi.Token{}, fail
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return gitapi.Token{}, fail
	}
	if refresh && resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return gitapi.Token{}, gitapi.ErrTokenRevoked
	}
	if resp.StatusCode != http.StatusOK {
		return gitapi.Token{}, fail
	}
	var payload tokenResponse
	if err := json.Unmarshal(body, &payload); err != nil || payload.Error != "" || payload.AccessToken == "" {
		if refresh && payload.Error != "" {
			return gitapi.Token{}, gitapi.ErrTokenRevoked
		}
		return gitapi.Token{}, fail
	}
	return payload.token(), nil
}

// Exchange 用授权码与 PKCE verifier 换取访问令牌（含刷新令牌与过期时间）。
func (c *Client) Exchange(ctx context.Context, code, verifier, redirectURI string) (gitapi.Token, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	return c.postToken(ctx, form, false)
}

// Refresh 用刷新令牌换新令牌；成功后旧的访问令牌与刷新令牌立即失效，调用方必须整体回写。
func (c *Client) Refresh(ctx context.Context, refreshToken string) (gitapi.Token, error) {
	form := url.Values{}
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")
	form.Set("redirect_uri", c.RedirectURI)
	return c.postToken(ctx, form, true)
}

func (c *Client) do(ctx context.Context, method, target, token string, payload any) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, gitapi.ErrRequestFailed
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, nil, gitapi.ErrRequestFailed
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, gitapi.ErrRequestFailed
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, nil, gitapi.ErrRequestFailed
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return resp, nil, gitapi.ErrTokenRevoked
	case resp.StatusCode == http.StatusNotFound:
		return resp, nil, gitapi.ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return resp, nil, rateLimited(resp)
	}
	return resp, data, nil
}

// rateLimited 读取 Retry-After（秒）或 RateLimit-Reset（Unix 秒）。
func rateLimited(resp *http.Response) *gitapi.ErrRateLimited {
	limited := &gitapi.ErrRateLimited{}
	if seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && seconds >= 0 {
		limited.ResetAt = time.Now().Add(time.Duration(seconds) * time.Second)
	} else if reset, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("RateLimit-Reset")), 10, 64); err == nil && reset > 0 {
		limited.ResetAt = time.Unix(reset, 0)
	}
	return limited
}

func (c *Client) api(path string) string { return c.BaseURL + "/api/v4" + path }

// PersonalToken verifies the token's api scope and expiry without writing to
// GitLab. The self endpoint is available since GitLab 15.5.
func (c *Client) PersonalToken(ctx context.Context, token string) (gitapi.Token, error) {
	resp, data, err := c.do(ctx, http.MethodGet, c.api("/personal_access_tokens/self"), token, nil)
	if err != nil {
		return gitapi.Token{}, err
	}
	if resp.StatusCode == http.StatusForbidden {
		return gitapi.Token{}, gitapi.ErrTokenRevoked
	}
	if resp.StatusCode != http.StatusOK {
		return gitapi.Token{}, gitapi.ErrRequestFailed
	}
	var info struct {
		Active    bool     `json:"active"`
		Revoked   bool     `json:"revoked"`
		Scopes    []string `json:"scopes"`
		ExpiresAt string   `json:"expires_at"`
	}
	if json.Unmarshal(data, &info) != nil {
		return gitapi.Token{}, gitapi.ErrRequestFailed
	}
	hasAPI := false
	for _, scope := range info.Scopes {
		if scope == Scope {
			hasAPI = true
		}
	}
	if !info.Active || info.Revoked || !hasAPI {
		return gitapi.Token{}, gitapi.ErrTokenRevoked
	}
	out := gitapi.Token{Access: token, Scope: Scope}
	if info.ExpiresAt != "" {
		out.ExpiresAt, err = time.Parse("2006-01-02", info.ExpiresAt)
		if err != nil {
			return gitapi.Token{}, gitapi.ErrRequestFailed
		}
		if !out.ExpiresAt.After(time.Now()) {
			return gitapi.Token{}, gitapi.ErrTokenRevoked
		}
	}
	return out, nil
}

// projectRef 是项目在 URL 路径里的标识：URL 编码后的 "group/subgroup/project"。
func projectRef(owner, repo string) string { return url.PathEscape(owner + "/" + repo) }

func hasNext(resp *http.Response) bool {
	return strings.TrimSpace(resp.Header.Get("X-Next-Page")) != ""
}

type glUser struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

// User 读取令牌所属用户。
func (c *Client) User(ctx context.Context, token string) (gitapi.User, error) {
	resp, data, err := c.do(ctx, http.MethodGet, c.api("/user"), token, nil)
	if err != nil {
		return gitapi.User{}, err
	}
	var user glUser
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &user) != nil || user.ID == 0 || user.Username == "" {
		return gitapi.User{}, gitapi.ErrRequestFailed
	}
	return gitapi.User{ID: user.ID, Login: user.Username, AvatarURL: user.AvatarURL, Scopes: Scope}, nil
}

// Orgs 读取用户所属的顶级群组（至少 Guest 角色），最多 3 页（每页 100）。
func (c *Client) Orgs(ctx context.Context, token string) ([]gitapi.Org, error) {
	orgs := make([]gitapi.Org, 0)
	for page := 1; page <= maxGroupPages; page++ {
		target := c.api(fmt.Sprintf("/groups?min_access_level=10&top_level_only=true&per_page=100&page=%d", page))
		resp, data, err := c.do(ctx, http.MethodGet, target, token, nil)
		if err != nil {
			return nil, err
		}
		var items []struct {
			ID        int64  `json:"id"`
			Path      string `json:"path"`
			AvatarURL string `json:"avatar_url"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
			return nil, gitapi.ErrRequestFailed
		}
		for _, item := range items {
			if item.Path != "" {
				orgs = append(orgs, gitapi.Org{ID: item.ID, Login: item.Path, AvatarURL: item.AvatarURL})
			}
		}
		if !hasNext(resp) {
			break
		}
	}
	return orgs, nil
}

type glProject struct {
	Permissions struct {
		Project *struct {
			AccessLevel int `json:"access_level"`
		} `json:"project_access"`
		Group *struct {
			AccessLevel int `json:"access_level"`
		} `json:"group_access"`
	} `json:"permissions"`
	Name              string `json:"name"`
	Path              string `json:"path"`
	PathWithNamespace string `json:"path_with_namespace"`
	Visibility        string `json:"visibility"`
	WebURL            string `json:"web_url"`
}

func (p glProject) repo() gitapi.Repo {
	repo := gitapi.Repo{FullName: p.PathWithNamespace, Name: p.Path, Private: p.Visibility != "public", HTMLURL: p.WebURL}
	if repo.Name == "" {
		repo.Name = p.Name
	}
	repo.Owner.Login, _, _ = strings.Cut(p.PathWithNamespace, "/")
	return repo
}

// ListRepos 读取一页项目（每页 50）。群组读 /groups/:id/projects（含子群组），个人账号读 /users/:username/projects。
func (c *Client) ListRepos(ctx context.Context, token string, userAccount bool, login, q string, page int) ([]gitapi.Repo, bool, error) {
	if page < 1 {
		page = 1
	}
	query := url.Values{"per_page": {"50"}, "page": {strconv.Itoa(page)}, "order_by": {"path"}, "sort": {"asc"}, "archived": {"false"}}
	if search := strings.TrimSpace(q); search != "" {
		query.Set("search", search)
	}
	var target string
	if c.PersonalTokenAuth {
		query.Set("membership", "true")
		query.Set("min_access_level", "40")
		target = c.api("/projects?" + query.Encode())
	} else if userAccount {
		target = c.api("/users/" + url.PathEscape(login) + "/projects?" + query.Encode())
	} else {
		query.Set("include_subgroups", "true")
		target = c.api("/groups/" + url.PathEscape(login) + "/projects?" + query.Encode())
	}
	resp, data, err := c.do(ctx, http.MethodGet, target, token, nil)
	if err != nil {
		return nil, false, err
	}
	var items []glProject
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
		return nil, false, gitapi.ErrRequestFailed
	}
	repos := make([]gitapi.Repo, 0, len(items))
	for _, item := range items {
		if item.PathWithNamespace != "" {
			repos = append(repos, item.repo())
		}
	}
	return repos, hasNext(resp), nil
}

// GetRepo 读取单个项目。
func (c *Client) GetRepo(ctx context.Context, token, owner, repo string) (gitapi.Repo, error) {
	resp, data, err := c.do(ctx, http.MethodGet, c.api("/projects/"+projectRef(owner, repo)), token, nil)
	if err != nil {
		return gitapi.Repo{}, err
	}
	var item glProject
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &item) != nil || item.PathWithNamespace == "" {
		return gitapi.Repo{}, gitapi.ErrRequestFailed
	}
	if c.PersonalTokenAuth && (item.Permissions.Project == nil || item.Permissions.Project.AccessLevel < 40) && (item.Permissions.Group == nil || item.Permissions.Group.AccessLevel < 40) {
		return gitapi.Repo{}, gitapi.ErrNotFound
	}
	return item.repo(), nil
}

type glIssue struct {
	ID          int64     `json:"id"`
	IID         int       `json:"iid"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"`
	WebURL      string    `json:"web_url"`
	Author      glUser    `json:"author"`
	Assignees   []glUser  `json:"assignees"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (i glIssue) issue() gitapi.Issue {
	state := "open"
	if i.State == "closed" {
		state = "closed"
	}
	out := gitapi.Issue{ID: i.ID, Number: i.IID, Title: i.Title, Body: i.Description, State: state, HTMLURL: i.WebURL,
		User: gitapi.IssueUser{ID: i.Author.ID, Login: i.Author.Username}, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
	for _, a := range i.Assignees {
		out.Assignees = append(out.Assignees, gitapi.IssueUser{ID: a.ID, Login: a.Username})
	}
	return out
}

// ListIssues 读取一页未关闭的 Issue（每页 100，按创建时间升序）。
func (c *Client) ListIssues(ctx context.Context, token, owner, repo string, page int) ([]gitapi.Issue, bool, error) {
	if page < 1 {
		page = 1
	}
	target := c.api(fmt.Sprintf("/projects/%s/issues?state=opened&issue_type=issue&per_page=100&page=%d&order_by=created_at&sort=asc", projectRef(owner, repo), page))
	resp, data, err := c.do(ctx, http.MethodGet, target, token, nil)
	if err != nil {
		return nil, false, err
	}
	var items []glIssue
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
		return nil, false, gitapi.ErrRequestFailed
	}
	issues := make([]gitapi.Issue, 0, len(items))
	for _, item := range items {
		issues = append(issues, item.issue())
	}
	return issues, hasNext(resp), nil
}

const maxCreatorPages = 5

// ListIssuesByCreator 列出某用户在 since 之后创建 / 更新过的 Issue（含已关闭），用于创建结果未知时的对账。
func (c *Client) ListIssuesByCreator(ctx context.Context, token, owner, repo, creator string, since time.Time) ([]gitapi.Issue, error) {
	query := url.Values{"author_username": {creator}, "state": {"all"}, "per_page": {"100"}}
	if !since.IsZero() {
		query.Set("updated_after", since.UTC().Format(time.RFC3339))
	}
	var result []gitapi.Issue
	for page := 1; page <= maxCreatorPages; page++ {
		query.Set("page", strconv.Itoa(page))
		resp, data, err := c.do(ctx, http.MethodGet, c.api("/projects/"+projectRef(owner, repo)+"/issues?"+query.Encode()), token, nil)
		if err != nil {
			return nil, err
		}
		var items []glIssue
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
			return nil, gitapi.ErrRequestFailed
		}
		for _, item := range items {
			result = append(result, item.issue())
		}
		if !hasNext(resp) {
			break
		}
	}
	return result, nil
}

// userIDs 把用户名换成 GitLab 用户 ID（assignee_ids 只接受 ID）；查不到的用户名跳过。
func (c *Client) userIDs(ctx context.Context, token string, logins []string) ([]int64, error) {
	ids := make([]int64, 0, len(logins))
	for _, login := range logins {
		resp, data, err := c.do(ctx, http.MethodGet, c.api("/users?username="+url.QueryEscape(login)), token, nil)
		if err != nil {
			return nil, err
		}
		var users []glUser
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &users) != nil {
			return nil, gitapi.ErrRequestFailed
		}
		for _, user := range users {
			if strings.EqualFold(user.Username, login) {
				ids = append(ids, user.ID)
				break
			}
		}
	}
	return ids, nil
}

// CreateIssue 创建 Issue。请求已发出但没有明确响应（传输错误、5xx）时返回 ErrUnknownResult。
func (c *Client) CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (gitapi.Issue, error) {
	ids, err := c.userIDs(ctx, token, assignees)
	if err != nil {
		return gitapi.Issue{}, err
	}
	payload := map[string]any{"title": title, "description": body, "assignee_ids": ids}
	resp, data, err := c.do(ctx, http.MethodPost, c.api("/projects/"+projectRef(owner, repo)+"/issues"), token, payload)
	if errors.Is(err, gitapi.ErrRequestFailed) {
		return gitapi.Issue{}, gitapi.ErrUnknownResult
	}
	if err != nil {
		return gitapi.Issue{}, err
	}
	if resp.StatusCode >= 500 {
		return gitapi.Issue{}, gitapi.ErrUnknownResult
	}
	var created glIssue
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(data, &created) != nil || created.IID <= 0 {
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	return created.issue(), nil
}

// UpdateIssue 修改 Issue：标题、正文、处理人，并用 state_event（close / reopen）切换状态。ref 是 Issue 的 iid。
func (c *Client) UpdateIssue(ctx context.Context, token, owner, repo, ref string, update gitapi.IssueUpdate) (gitapi.Issue, error) {
	ids, err := c.userIDs(ctx, token, update.Assignees)
	if err != nil {
		return gitapi.Issue{}, err
	}
	event := "reopen"
	if update.State == "closed" {
		event = "close"
	}
	payload := map[string]any{"title": update.Title, "description": update.Body, "assignee_ids": ids, "state_event": event}
	resp, data, err := c.do(ctx, http.MethodPut, c.api("/projects/"+projectRef(owner, repo)+"/issues/"+url.PathEscape(ref)), token, payload)
	if err != nil {
		return gitapi.Issue{}, err
	}
	var updated glIssue
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &updated) != nil {
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	return updated.issue(), nil
}

func (c *Client) note(ctx context.Context, token, owner, repo, kind, ref, body string) (string, error) {
	target := c.api("/projects/" + projectRef(owner, repo) + "/" + kind + "/" + url.PathEscape(ref) + "/notes")
	resp, data, err := c.do(ctx, http.MethodPost, target, token, map[string]string{"body": body})
	if err != nil {
		return "", err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK || json.Unmarshal(data, &created) != nil || created.ID == 0 {
		return "", gitapi.ErrRequestFailed
	}
	return strconv.FormatInt(created.ID, 10), nil
}

// CommentIssue 在 Issue 下发评论，返回评论 id。
func (c *Client) CommentIssue(ctx context.Context, token, owner, repo, ref, body string) (string, error) {
	return c.note(ctx, token, owner, repo, "issues", ref, body)
}

// CommentPullRequest 在合并请求（MR）下发评论，返回评论 id。
func (c *Client) CommentPullRequest(ctx context.Context, token, owner, repo string, number int, body string) (string, error) {
	return c.note(ctx, token, owner, repo, "merge_requests", strconv.Itoa(number), body)
}
