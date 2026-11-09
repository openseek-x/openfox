// Package gitee 是 Gitee OAuth 与 OpenAPI v5 的最小客户端。
// 地址来自运维配置（OPENFOX_GITEE_BASE_URL / OPENFOX_GITEE_API_URL）而不是用户输入，所以与 GitHub 客户端一样不做 SSRF 拦截，只设置超时与响应体上限。
//
// 协议要点（官方出处：https://gitee.com/api/v5/oauth_doc 、https://gitee.com/api/v5/swagger 的 OpenAPI 描述）：
//   - 授权码模式：GET {base}/oauth/authorize?client_id&redirect_uri&response_type=code&scope&state；
//     POST {base}/oauth/token（grant_type=authorization_code，client_secret 放在 Body）。
//     文档与 OpenAPI 描述里没有 PKCE（code_challenge / code_verifier），所以不发送 challenge；防 CSRF 靠 state。
//   - access_token 有效期一天，refresh_token 换新：POST {base}/oauth/token（grant_type=refresh_token）。
//   - API 的令牌传递方式：OpenAPI 描述里只有 access_token 参数（GET 走查询串，POST / PATCH 走表单），没有 Authorization 头的说明，
//     所以按文档传参；令牌随请求出站，但客户端的所有错误都是固定的哨兵错误，URL 与请求体不会进入错误信息或日志。
//   - 获取令牌时必须带 User-Agent，否则服务端返回 403（官方文档注明）。
//   - Issue 编号是字母数字串（如 IG6E9）：放进 gitapi.Issue.Ref；Number 存数字 id（见 NumberFromID）。
package gitee

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openfox/internal/integrations/gitapi"
)

const (
	// Scope 是申请的 OAuth 权限：读取用户、仓库，读写 Issue 与评论、Pull Request 评论、组织。
	Scope = "user_info projects pull_requests issues notes groups"

	connectTimeout = 10 * time.Second
	totalTimeout   = 20 * time.Second
	maxBodyBytes   = 2 << 20
	maxOrgPages    = 3
	reposPerPage   = 50
	issuesPerPage  = 100
	maxCreatorPage = 5
)

// Client 持有第三方应用凭据与服务地址。零值不可用，用 New 构造。
type Client struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	APIURL       string
	HTTP         *http.Client
}

var _ gitapi.Client = (*Client)(nil)

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

// NumberFromID 把 Gitee 的数字 id 换成 task_issue_links.issue_number（integer，必须在 int32 范围内且大于 0）。
// 超出范围返回 0，调用方当作「不可同步」处理。
func NumberFromID(id int64) int {
	if id <= 0 || id > math.MaxInt32 {
		return 0
	}
	return int(id)
}

// AuthorizeURL 生成授权页地址。Gitee 不支持 PKCE，所以忽略 codeChallenge。
func (c *Client) AuthorizeURL(state, _ string, redirectURI string) string {
	query := url.Values{}
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", Scope)
	query.Set("state", state)
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

// postToken 请求 /oauth/token。换取授权码失败统一为 ErrExchangeFailed；刷新时 4xx 说明刷新令牌已失效，返回 ErrTokenRevoked，
// 网络错误与 5xx 返回 ErrRequestFailed（下一轮再试，不把连接标成需要重新授权）。
func (c *Client) postToken(ctx context.Context, form url.Values, refresh bool) (gitapi.Token, error) {
	fail := gitapi.ErrExchangeFailed
	if refresh {
		fail = gitapi.ErrRequestFailed
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return gitapi.Token{}, fail
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
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

// Exchange 用授权码换取访问令牌（含刷新令牌与过期时间）。verifier 不使用：Gitee 没有 PKCE。
func (c *Client) Exchange(ctx context.Context, code, _ string, redirectURI string) (gitapi.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	form.Set("redirect_uri", redirectURI)
	return c.postToken(ctx, form, false)
}

// Refresh 用刷新令牌换新令牌。响应里没有新的刷新令牌时，调用方保留旧的。
func (c *Client) Refresh(ctx context.Context, refreshToken string) (gitapi.Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", c.ClientID)
	form.Set("client_secret", c.ClientSecret)
	return c.postToken(ctx, form, true)
}

// do 发出一次 API 请求。GET / DELETE 的令牌走查询串，POST / PATCH 的令牌走表单（OpenAPI 描述里 access_token 的位置）。
// 401 → ErrTokenRevoked；404 → ErrNotFound；429 → ErrRateLimited（Retry-After 秒）。
func (c *Client) do(ctx context.Context, method, path, token string, query, form url.Values) (*http.Response, []byte, error) {
	if query == nil {
		query = url.Values{}
	}
	var body io.Reader
	if method == http.MethodGet || method == http.MethodDelete {
		query.Set("access_token", token)
	} else {
		if form == nil {
			form = url.Values{}
		}
		form.Set("access_token", token)
		body = strings.NewReader(form.Encode())
	}
	target := c.APIURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, nil, gitapi.ErrRequestFailed
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return resp, nil, gitapi.ErrTokenRevoked
	case http.StatusNotFound:
		return resp, nil, gitapi.ErrNotFound
	case http.StatusTooManyRequests:
		limited := &gitapi.ErrRateLimited{}
		if seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && seconds >= 0 {
			limited.ResetAt = time.Now().Add(time.Duration(seconds) * time.Second)
		}
		return resp, nil, limited
	}
	return resp, data, nil
}

// seg 转义路径段（owner / repo / 编号都是用户可影响的值）。
func seg(value string) string { return url.PathEscape(value) }

func hasNext(resp *http.Response, count, perPage int) bool {
	if total, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("total_page"))); err == nil && total > 0 {
		page, _ := strconv.Atoi(strings.TrimSpace(resp.Header.Get("current_page")))
		if page == 0 {
			return count >= perPage
		}
		return page < total
	}
	return count >= perPage
}

type userPayload struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// User 读取令牌所属用户。Gitee 不回报实际授权范围，Scopes 用申请的范围。
func (c *Client) User(ctx context.Context, token string) (gitapi.User, error) {
	resp, data, err := c.do(ctx, http.MethodGet, "/user", token, nil, nil)
	if err != nil {
		return gitapi.User{}, err
	}
	var user userPayload
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &user) != nil || user.ID == 0 || user.Login == "" {
		return gitapi.User{}, gitapi.ErrRequestFailed
	}
	return gitapi.User{ID: user.ID, Login: user.Login, AvatarURL: user.AvatarURL, Scopes: Scope}, nil
}

// Orgs 读取用户所属的组织，最多 3 页（每页 100）。
func (c *Client) Orgs(ctx context.Context, token string) ([]gitapi.Org, error) {
	orgs := make([]gitapi.Org, 0)
	for page := 1; page <= maxOrgPages; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"100"}}
		resp, data, err := c.do(ctx, http.MethodGet, "/user/orgs", token, query, nil)
		if err != nil {
			return nil, err
		}
		var items []userPayload
		if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
			return nil, gitapi.ErrRequestFailed
		}
		for _, item := range items {
			if item.Login != "" {
				orgs = append(orgs, gitapi.Org{ID: item.ID, Login: item.Login, AvatarURL: item.AvatarURL})
			}
		}
		if !hasNext(resp, len(items), 100) {
			break
		}
	}
	return orgs, nil
}

type repoPayload struct {
	FullName  string `json:"full_name"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	HTMLURL   string `json:"html_url"`
	Private   bool   `json:"private"`
	Namespace struct {
		Path string `json:"path"`
	} `json:"namespace"`
}

// repo 转成公共仓库类型。FullName 取「空间 path / 仓库 path」（full_name 在部分响应里是中文显示名，不能直接用）。
func (r repoPayload) repo() gitapi.Repo {
	full := r.FullName
	if r.Namespace.Path != "" && r.Path != "" {
		full = r.Namespace.Path + "/" + r.Path
	}
	out := gitapi.Repo{FullName: full, Name: r.Path, Private: r.Private, HTMLURL: r.HTMLURL}
	if out.Name == "" {
		out.Name = r.Name
	}
	out.Owner.Login, _, _ = strings.Cut(full, "/")
	return out
}

// ListRepos 读取一页仓库（每页 50）。组织读 /orgs/{org}/repos；授权用户本人读 /user/repos?affiliation=owner。
// /orgs/{org}/repos 没有搜索参数，q 在本页结果里按名称过滤。
func (c *Client) ListRepos(ctx context.Context, token string, userAccount bool, login, q string, page int) ([]gitapi.Repo, bool, error) {
	if page < 1 {
		page = 1
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(reposPerPage)}}
	search := strings.TrimSpace(q)
	path := "/orgs/" + seg(login) + "/repos"
	if userAccount {
		path = "/user/repos"
		query.Set("affiliation", "owner")
		if search != "" {
			query.Set("q", search)
		}
	} else {
		query.Set("type", "all")
	}
	resp, data, err := c.do(ctx, http.MethodGet, path, token, query, nil)
	if err != nil {
		return nil, false, err
	}
	var items []repoPayload
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &items) != nil {
		return nil, false, gitapi.ErrRequestFailed
	}
	repos := make([]gitapi.Repo, 0, len(items))
	for _, item := range items {
		repo := item.repo()
		if repo.FullName == "" || !strings.Contains(repo.FullName, "/") {
			continue
		}
		if !userAccount && search != "" && !strings.Contains(strings.ToLower(repo.FullName), strings.ToLower(search)) {
			continue
		}
		repos = append(repos, repo)
	}
	return repos, hasNext(resp, len(items), reposPerPage), nil
}

// GetRepo 读取单个仓库。
func (c *Client) GetRepo(ctx context.Context, token, owner, repo string) (gitapi.Repo, error) {
	resp, data, err := c.do(ctx, http.MethodGet, "/repos/"+seg(owner)+"/"+seg(repo), token, nil, nil)
	if err != nil {
		return gitapi.Repo{}, err
	}
	var item repoPayload
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &item) != nil {
		return gitapi.Repo{}, gitapi.ErrRequestFailed
	}
	out := item.repo()
	if out.FullName == "" {
		return gitapi.Repo{}, gitapi.ErrRequestFailed
	}
	return out, nil
}

type issuePayload struct {
	ID        int64        `json:"id"`
	Number    string       `json:"number"`
	Title     string       `json:"title"`
	Body      string       `json:"body"`
	State     string       `json:"state"`
	HTMLURL   string       `json:"html_url"`
	User      userPayload  `json:"user"`
	Assignee  *userPayload `json:"assignee"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

// IssueState 把 Gitee 的 Issue 状态（open / progressing / closed / rejected）归并为 open / closed。
func IssueState(state string) string {
	switch state {
	case "closed", "rejected":
		return "closed"
	}
	return "open"
}

// issue 转成公共 Issue；数字 id 超出 int32 时 ok 为 false（无法放进 issue_number）。
func (i issuePayload) issue() (gitapi.Issue, bool) {
	number := NumberFromID(i.ID)
	if number == 0 || i.Number == "" {
		return gitapi.Issue{}, false
	}
	out := gitapi.Issue{ID: i.ID, Number: number, Ref: i.Number, Title: i.Title, Body: i.Body, State: IssueState(i.State), HTMLURL: i.HTMLURL,
		User: gitapi.IssueUser{ID: i.User.ID, Login: i.User.Login}, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
	if i.Assignee != nil && i.Assignee.Login != "" {
		out.Assignees = []gitapi.IssueUser{{ID: i.Assignee.ID, Login: i.Assignee.Login}}
	}
	return out, true
}

func decodeIssues(data []byte) ([]gitapi.Issue, error) {
	var items []issuePayload
	if json.Unmarshal(data, &items) != nil {
		return nil, gitapi.ErrRequestFailed
	}
	issues := make([]gitapi.Issue, 0, len(items))
	for _, item := range items {
		if issue, ok := item.issue(); ok {
			issues = append(issues, issue)
		}
	}
	return issues, nil
}

func issuesPath(owner, repo string) string {
	return "/repos/" + seg(owner) + "/" + seg(repo) + "/issues"
}

// ListIssues 读取一页开启状态的 Issue（state=open，每页 100，按创建时间升序）。「进行中」状态的 Issue 不在其中（已知局限）。
func (c *Client) ListIssues(ctx context.Context, token, owner, repo string, page int) ([]gitapi.Issue, bool, error) {
	if page < 1 {
		page = 1
	}
	query := url.Values{"state": {"open"}, "sort": {"created"}, "direction": {"asc"}, "page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(issuesPerPage)}}
	resp, data, err := c.do(ctx, http.MethodGet, issuesPath(owner, repo), token, query, nil)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, gitapi.ErrRequestFailed
	}
	var raw []json.RawMessage
	_ = json.Unmarshal(data, &raw)
	issues, err := decodeIssues(data)
	if err != nil {
		return nil, false, err
	}
	return issues, hasNext(resp, len(raw), issuesPerPage), nil
}

// ListIssuesByCreator 列出某用户在 since 之后更新过的 Issue（含已关闭），用于创建结果未知时的对账。
func (c *Client) ListIssuesByCreator(ctx context.Context, token, owner, repo, creator string, since time.Time) ([]gitapi.Issue, error) {
	var result []gitapi.Issue
	for page := 1; page <= maxCreatorPage; page++ {
		query := url.Values{"state": {"all"}, "creator": {creator}, "sort": {"created"}, "direction": {"desc"},
			"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(issuesPerPage)}}
		if !since.IsZero() {
			query.Set("since", since.UTC().Format(time.RFC3339))
		}
		resp, data, err := c.do(ctx, http.MethodGet, issuesPath(owner, repo), token, query, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, gitapi.ErrRequestFailed
		}
		var raw []json.RawMessage
		_ = json.Unmarshal(data, &raw)
		issues, err := decodeIssues(data)
		if err != nil {
			return nil, err
		}
		result = append(result, issues...)
		if !hasNext(resp, len(raw), issuesPerPage) {
			break
		}
	}
	return result, nil
}

// CreateIssue 创建 Issue（POST /repos/{owner}/issues，仓库名放在表单的 repo 字段）。Gitee 只有一个负责人，取第一个处理人。
// 请求已发出但没有明确响应（传输错误、5xx）时返回 ErrUnknownResult。
func (c *Client) CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (gitapi.Issue, error) {
	form := url.Values{"repo": {repo}, "title": {title}, "body": {body}}
	if len(assignees) > 0 {
		form.Set("assignee", assignees[0])
	}
	resp, data, err := c.do(ctx, http.MethodPost, "/repos/"+seg(owner)+"/issues", token, nil, form)
	if errors.Is(err, gitapi.ErrRequestFailed) {
		return gitapi.Issue{}, gitapi.ErrUnknownResult
	}
	if err != nil {
		return gitapi.Issue{}, err
	}
	if resp.StatusCode >= 500 {
		return gitapi.Issue{}, gitapi.ErrUnknownResult
	}
	var created issuePayload
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK || json.Unmarshal(data, &created) != nil {
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	issue, ok := created.issue()
	if !ok {
		// 已创建，但编号无法放进 issue_number：当作结果未知，让对账流程按创建人 + 任务标记查找（对账同样会跳过它，最终失败而不是重复创建）。
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	return issue, nil
}

// UpdateIssue 修改 Issue（PATCH /repos/{owner}/issues/{number}）。ref 是字母数字串编号。
// state 只发 open / closed；assignee 总是发送，空串表示清空负责人（OpenAPI 描述未明确写空串语义，属于推断）。
func (c *Client) UpdateIssue(ctx context.Context, token, owner, repo, ref string, update gitapi.IssueUpdate) (gitapi.Issue, error) {
	state := "open"
	if update.State == "closed" {
		state = "closed"
	}
	assignee := ""
	if len(update.Assignees) > 0 {
		assignee = update.Assignees[0]
	}
	form := url.Values{"repo": {repo}, "title": {update.Title}, "body": {update.Body}, "state": {state}, "assignee": {assignee}}
	resp, data, err := c.do(ctx, http.MethodPatch, "/repos/"+seg(owner)+"/issues/"+seg(ref), token, nil, form)
	if err != nil {
		return gitapi.Issue{}, err
	}
	var updated issuePayload
	if resp.StatusCode != http.StatusOK || json.Unmarshal(data, &updated) != nil {
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	issue, ok := updated.issue()
	if !ok {
		return gitapi.Issue{}, gitapi.ErrRequestFailed
	}
	return issue, nil
}

func (c *Client) note(ctx context.Context, token, path string, query url.Values, body string) (string, error) {
	resp, data, err := c.do(ctx, http.MethodPost, path, token, query, url.Values{"body": {body}})
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
	return c.note(ctx, token, issuesPath(owner, repo)+"/"+seg(ref)+"/comments", nil, body)
}

// CommentPullRequest 在 Pull Request 下发评论，返回评论 id。该接口的 access_token 在 OpenAPI 描述里是查询参数，所以同时放进查询串。
func (c *Client) CommentPullRequest(ctx context.Context, token, owner, repo string, number int, body string) (string, error) {
	return c.note(ctx, token, "/repos/"+seg(owner)+"/"+seg(repo)+"/pulls/"+strconv.Itoa(number)+"/comments",
		url.Values{"access_token": {token}}, body)
}
