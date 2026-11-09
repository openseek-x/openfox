// Package jiraapi 是 Jira REST v2 的最小客户端：校验连接、读取项目与自定义字段、分页搜索与补拉评论。
// 所有请求经 safehttp 的防护客户端发出；错误只带原因分类与 HTTP 状态，不带凭据与请求头。
package jiraapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"openfox/internal/platform/safehttp"
)

// 部署方式与认证方式。
const (
	DeploymentCloud  = "cloud"
	DeploymentServer = "server"

	AuthAPIToken = "api_token"
	AuthPAT      = "pat"
	AuthPassword = "password"
)

// 连接失败原因（对应契约 ImportConnectionErrorReason）。
const (
	ReasonAuthFailed      = "auth_failed"
	ReasonSiteNotFound    = "site_not_found"
	ReasonProjectNotFound = "project_not_found"
	ReasonJQLInvalid      = "jql_invalid"
	ReasonUnreachable     = "unreachable"
	ReasonDNSFailed       = "dns_failed"
	ReasonTLSFailed       = "tls_failed"
	ReasonBlockedAddress  = "blocked_address"
	ReasonInvalidURL      = "invalid_url"
	ReasonRateLimited     = "rate_limited"
	ReasonTooManyIssues   = "too_many_issues"
)

const (
	// MaxBodyBytes 是单个响应体的读取上限。
	MaxBodyBytes = 10 << 20
	// PageSize 是搜索每页条数。
	PageSize = 100
	// MaxRetryAfter 是 Retry-After 的上限（秒）。
	MaxRetryAfter = 600
	// CountCap 是逐页计数时数到的上限（5001 条即可判定超过 5000）。
	CountCap = 5001

	userAgent = "OpenFox-Importer/1"
)

// Error 是带分类的失败。Message 不含凭据。
type Error struct {
	Reason     string
	Status     int // 来源 HTTP 状态，没有时为 0
	RetryAfter int // 秒，仅 rate_limited
	Message    string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return "jira: " + e.Reason + ": " + e.Message
	}
	return "jira: " + e.Reason
}

// ReasonOf 取出错误的原因分类；不是 *Error 时返回空串。
func ReasonOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

// Auth 是认证信息。Secret 为 API 令牌 / 个人访问令牌 / 密码。
type Auth struct {
	Kind     string
	Email    string
	Username string
	Secret   string
}

// Config 是创建客户端的参数。
type Config struct {
	Deployment string
	SiteURL    string
	Auth       Auth
}

// Client 是 Jira v2 客户端。
type Client struct {
	base       *url.URL
	deployment string
	authHeader string
	http       *http.Client
}

// NormalizeSiteURL 校验并规范化站点地址（https、无查询串、去掉末尾斜杠），与 New 使用同一规则。
func NormalizeSiteURL(raw string) (string, error) {
	u, err := normalizeSite(raw)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func normalizeSite(raw string) (*url.URL, error) {
	u, err := safehttp.CheckURL(raw)
	if err != nil || u.RawQuery != "" || u.ForceQuery {
		return nil, &Error{Reason: ReasonInvalidURL, Message: "站点地址不合法"}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// New 校验站点地址并生成客户端；httpClient 通常来自 safehttp.Client。
func New(cfg Config, httpClient *http.Client) (*Client, error) {
	u, err := normalizeSite(cfg.SiteURL)
	if err != nil {
		return nil, err
	}
	c := &Client{base: u, deployment: cfg.Deployment, http: httpClient}
	switch {
	case cfg.Deployment == DeploymentCloud && cfg.Auth.Kind == AuthAPIToken && cfg.Auth.Email != "" && cfg.Auth.Secret != "":
		c.authHeader = basic(cfg.Auth.Email, cfg.Auth.Secret)
	case cfg.Deployment == DeploymentServer && cfg.Auth.Kind == AuthPAT && cfg.Auth.Secret != "":
		c.authHeader = "Bearer " + cfg.Auth.Secret
	case cfg.Deployment == DeploymentServer && cfg.Auth.Kind == AuthPassword && cfg.Auth.Username != "" && cfg.Auth.Secret != "":
		c.authHeader = basic(cfg.Auth.Username, cfg.Auth.Secret)
	default:
		return nil, &Error{Reason: ReasonAuthFailed, Message: "认证信息不完整"}
	}
	return c, nil
}

func basic(user, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}

// Host 返回「主机[:端口][/路径前缀]」（不含协议），用作 source_label。
func (c *Client) Host() string { return c.base.Host + c.base.Path }

// SiteURL 返回规范化后的站点地址（不含末尾斜杠）。
func (c *Client) SiteURL() string { return c.base.String() }

func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path = c.base.Path + path
	u.RawQuery = query.Encode()
	return u.String()
}

// do 发请求并读取最多 10 MiB 的响应体。HTTP 状态不在这里判断（由各方法按语义映射）。
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (int, http.Header, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, &Error{Reason: ReasonUnreachable, Message: "请求体编码失败"}
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path, query), reader)
	if err != nil {
		return 0, nil, nil, &Error{Reason: ReasonInvalidURL, Message: "站点地址不合法"}
	}
	req.Header.Set("Authorization", c.authHeader)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, classifyTransport(err)
	}
	if len(data) > MaxBodyBytes {
		return resp.StatusCode, resp.Header, nil, &Error{Reason: ReasonUnreachable, Status: resp.StatusCode, Message: "响应体超过 10 MiB"}
	}
	return resp.StatusCode, resp.Header, data, nil
}

// classifyTransport 把网络层错误映射为原因分类。
func classifyTransport(err error) error {
	var dnsErr *net.DNSError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verify *tls.CertificateVerificationError
	var recordHeader tls.RecordHeaderError
	switch {
	case errors.Is(err, safehttp.ErrBlockedAddress):
		return &Error{Reason: ReasonBlockedAddress, Message: "地址指向内网或保留地址"}
	case errors.Is(err, safehttp.ErrInvalidURL):
		return &Error{Reason: ReasonInvalidURL, Message: "地址格式不合法"}
	case errors.Is(err, safehttp.ErrTooManyRedirects):
		return &Error{Reason: ReasonUnreachable, Message: "重定向次数过多"}
	case errors.As(err, &dnsErr) && !dnsErr.IsTimeout:
		return &Error{Reason: ReasonDNSFailed, Message: "域名无法解析"}
	case errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &verify),
		errors.As(err, &recordHeader), strings.Contains(err.Error(), "tls:"):
		return &Error{Reason: ReasonTLSFailed, Message: "HTTPS 证书校验失败"}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return &Error{Reason: ReasonUnreachable, Message: "请求超时"}
	}
	return &Error{Reason: ReasonUnreachable, Message: "无法连接到站点"}
}

// statusError 把通用的非 2xx 状态映射为错误；2xx 返回 nil。
func statusError(status int, header http.Header, data []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Reason: ReasonAuthFailed, Status: status, Message: "凭证无效或没有权限"}
	case status == http.StatusTooManyRequests:
		return &Error{Reason: ReasonRateLimited, Status: status, RetryAfter: parseRetryAfter(header.Get("Retry-After")), Message: "请求被限流"}
	}
	return &Error{Reason: ReasonUnreachable, Status: status, Message: fmt.Sprintf("站点返回 HTTP %d", status)}
}

func parseRetryAfter(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0
	}
	return min(n, MaxRetryAfter)
}

// ---------- 方法 ----------

// User 是 Jira 用户。cloud 用 AccountID 作标识，server 用 Name 或 Key。
type User struct {
	AccountID    string `json:"accountId"`
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// Identity 返回用户在来源平台上的标识。
func (u User) Identity() string {
	switch {
	case u.AccountID != "":
		return u.AccountID
	case u.Name != "":
		return u.Name
	}
	return u.Key
}

// Myself 校验凭证与站点：404 或非 JSON 响应视为站点不存在。
func (c *Client) Myself(ctx context.Context) (User, error) {
	status, header, data, err := c.do(ctx, http.MethodGet, "/rest/api/2/myself", nil, nil)
	if err != nil {
		return User{}, err
	}
	if status == http.StatusNotFound {
		return User{}, &Error{Reason: ReasonSiteNotFound, Status: status, Message: "找不到 Jira 实例"}
	}
	if err := statusError(status, header, data); err != nil {
		return User{}, err
	}
	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return User{}, &Error{Reason: ReasonSiteNotFound, Status: status, Message: "站点返回的不是 Jira 响应"}
	}
	return user, nil
}

// ProjectInfo 是项目的基本信息。
type ProjectInfo struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Project 读取项目；404 视为项目不存在或无权访问。
func (c *Client) Project(ctx context.Context, key string) (ProjectInfo, error) {
	status, header, data, err := c.do(ctx, http.MethodGet, "/rest/api/2/project/"+url.PathEscape(key), nil, nil)
	if err != nil {
		return ProjectInfo{}, err
	}
	if status == http.StatusNotFound {
		return ProjectInfo{}, &Error{Reason: ReasonProjectNotFound, Status: status, Message: "项目不存在或没有权限"}
	}
	if err := statusError(status, header, data); err != nil {
		return ProjectInfo{}, err
	}
	var info ProjectInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ProjectInfo{}, &Error{Reason: ReasonUnreachable, Status: status, Message: "项目响应无法解析"}
	}
	return info, nil
}

// Field 是 /field 返回的一项。
type Field struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Custom bool   `json:"custom"`
	Schema struct {
		Type   string `json:"type"`
		Custom string `json:"custom"`
	} `json:"schema"`
}

// FieldIDs 是识别出的自定义字段 ID（找不到为空串）。
type FieldIDs struct {
	EpicLink string `json:"epic_link,omitempty"`
	Sprint   string `json:"sprint,omitempty"`
	Points   string `json:"points,omitempty"`
	Start    string `json:"start,omitempty"`
}

// Ids 返回非空的字段 ID。
func (f FieldIDs) Ids() []string {
	var ids []string
	for _, id := range []string{f.Sprint, f.Points, f.Start, f.EpicLink} {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// Fields 读取全部字段。
func (c *Client) Fields(ctx context.Context) ([]Field, error) {
	status, header, data, err := c.do(ctx, http.MethodGet, "/rest/api/2/field", nil, nil)
	if err != nil {
		return nil, err
	}
	if err := statusError(status, header, data); err != nil {
		return nil, err
	}
	var fields []Field
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, &Error{Reason: ReasonUnreachable, Status: status, Message: "字段响应无法解析"}
	}
	return fields, nil
}

// DiscoverFields 找 Sprint、故事点与开始日期字段。
func DiscoverFields(fields []Field) FieldIDs {
	var ids FieldIDs
	for _, f := range fields {
		name := strings.ToLower(strings.TrimSpace(f.Name))
		switch {
		case f.Schema.Custom == "com.pyxis.greenhopper.jira:gh-epic-link":
			if ids.EpicLink == "" {
				ids.EpicLink = f.ID
			}
		case f.Schema.Custom == "com.pyxis.greenhopper.jira:gh-sprint":
			if ids.Sprint == "" {
				ids.Sprint = f.ID
			}
		case f.Schema.Type == "number" && (name == "story point estimate" || name == "story points" || name == "故事点"):
			if ids.Points == "" || name == "story point estimate" {
				ids.Points = f.ID
			}
		case name == "start date" || name == "开始日期":
			if ids.Start == "" {
				ids.Start = f.ID
			}
		}
	}
	return ids
}

// BaseFields 是搜索请求固定带的字段。
var BaseFields = []string{"summary", "status", "priority", "issuetype", "assignee", "reporter", "creator", "created", "updated",
	"duedate", "labels", "description", "parent", "comment"}

// Issue 是搜索返回的一条。
type Issue struct {
	ID     string                     `json:"id"`
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

// Cursor 是搜索游标：cloud 用 Token，server 用 StartAt。
type Cursor struct {
	Token   string `json:"token,omitempty"`
	StartAt int    `json:"start_at,omitempty"`
}

// Page 是一页搜索结果。Done 为 true 表示没有下一页。
type Page struct {
	Issues   []Issue
	Next     Cursor
	Done     bool
	Total    int
	HasTotal bool
}

func searchFields(extra []string) string {
	return strings.Join(append(append([]string{}, BaseFields...), extra...), ",")
}

func jqlError(status int, header http.Header, data []byte) error {
	if status == http.StatusBadRequest {
		var body struct {
			ErrorMessages []string `json:"errorMessages"`
			Errors        map[string]string
		}
		_ = json.Unmarshal(data, &body)
		text := strings.Join(body.ErrorMessages, " ")
		for _, v := range body.Errors {
			text += " " + v
		}
		if strings.Contains(strings.ToLower(text), "jql") {
			return &Error{Reason: ReasonJQLInvalid, Status: status, Message: "JQL 无效"}
		}
	}
	return statusError(status, header, data)
}

// Search 取一页。cursor 为零值表示第一页。maxResults 为 0 时用 PageSize。
func (c *Client) Search(ctx context.Context, jql string, cursor Cursor, extraFields []string, maxResults int) (Page, error) {
	if maxResults <= 0 {
		maxResults = PageSize
	}
	q := url.Values{}
	q.Set("jql", jql)
	q.Set("maxResults", strconv.Itoa(maxResults))
	q.Set("fields", searchFields(extraFields))
	path := "/rest/api/2/search"
	if c.deployment == DeploymentCloud {
		path = "/rest/api/2/search/jql"
		if cursor.Token != "" {
			q.Set("nextPageToken", cursor.Token)
		}
	} else {
		q.Set("startAt", strconv.Itoa(cursor.StartAt))
	}
	status, header, data, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return Page{}, err
	}
	if err := jqlError(status, header, data); err != nil {
		return Page{}, err
	}
	var body struct {
		Issues        []Issue `json:"issues"`
		NextPageToken string  `json:"nextPageToken"`
		Total         *int    `json:"total"`
		StartAt       int     `json:"startAt"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return Page{}, &Error{Reason: ReasonUnreachable, Status: status, Message: "搜索响应无法解析"}
	}
	page := Page{Issues: body.Issues}
	if body.Total != nil {
		page.Total, page.HasTotal = *body.Total, true
	}
	if c.deployment == DeploymentCloud {
		page.Next = Cursor{Token: body.NextPageToken}
		page.Done = body.NextPageToken == "" || len(body.Issues) == 0
		return page, nil
	}
	next := cursor.StartAt + len(body.Issues)
	page.Next = Cursor{StartAt: next}
	page.Done = len(body.Issues) == 0 || (page.HasTotal && next >= page.Total)
	return page, nil
}

// Count 统计 JQL 命中的条数。cloud 先试 approximate-count，不可用时逐页数到 CountCap 条；server 直接用 total。
func (c *Client) Count(ctx context.Context, jql string) (int, error) {
	if c.deployment == DeploymentServer {
		page, err := c.Search(ctx, jql, Cursor{}, nil, 1)
		if err != nil {
			return 0, err
		}
		if page.HasTotal {
			return page.Total, nil
		}
		return len(page.Issues), nil
	}
	status, header, data, err := c.do(ctx, http.MethodPost, "/rest/api/2/search/approximate-count", nil, map[string]string{"jql": jql})
	if err != nil {
		return 0, err
	}
	if status >= 200 && status < 300 {
		var body struct {
			Count *int `json:"count"`
		}
		if json.Unmarshal(data, &body) == nil && body.Count != nil {
			return *body.Count, nil
		}
	} else if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests ||
		status == http.StatusBadRequest {
		if err := jqlError(status, header, data); err != nil {
			return 0, err
		}
	}
	// 接口不可用：逐页数到上限。
	total := 0
	var cursor Cursor
	for {
		page, err := c.Search(ctx, jql, cursor, nil, PageSize)
		if err != nil {
			return 0, err
		}
		total += len(page.Issues)
		if page.Done || total >= CountCap {
			return min(total, CountCap), nil
		}
		cursor = page.Next
	}
}

// Comment 是一条评论。
type Comment struct {
	ID      string `json:"id"`
	Author  User   `json:"author"`
	Body    string `json:"body"`
	Created string `json:"created"`
}

// Comments 补拉某个任务的评论，返回本页评论与总数。
func (c *Client) Comments(ctx context.Context, issueID string, startAt int) ([]Comment, int, error) {
	q := url.Values{}
	q.Set("startAt", strconv.Itoa(startAt))
	q.Set("maxResults", strconv.Itoa(PageSize))
	status, header, data, err := c.do(ctx, http.MethodGet, "/rest/api/2/issue/"+url.PathEscape(issueID)+"/comment", q, nil)
	if err != nil {
		return nil, 0, err
	}
	if err := statusError(status, header, data); err != nil {
		return nil, 0, err
	}
	var body struct {
		Comments []Comment `json:"comments"`
		Total    int       `json:"total"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, 0, &Error{Reason: ReasonUnreachable, Status: status, Message: "评论响应无法解析"}
	}
	return body.Comments, body.Total, nil
}

// UpdateText accepts a numeric ID returned by the current project's search.
func (c *Client) UpdateText(ctx context.Context, id string, title, desc *string) error {
	n, e := strconv.ParseUint(id, 10, 64)
	if e != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		return &Error{Reason: ReasonUnreachable, Message: "无效的 Jira 任务编号"}
	}
	fields := map[string]string{}
	if title != nil {
		fields["summary"] = *title
	}
	if desc != nil {
		fields["description"] = *desc
	}
	status, h, data, e := c.do(ctx, http.MethodPut, "/rest/api/2/issue/"+id, nil, map[string]any{"fields": fields})
	if e != nil {
		return e
	}
	return statusError(status, h, data)
}

// UpdateStatus uses an available workflow transition, not an editable status field.
// Multiple transitions to the same destination require a choice in Jira.
func (c *Client) UpdateStatus(ctx context.Context, id, target string) error {
	n, e := strconv.ParseUint(id, 10, 64)
	if e != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		return &Error{Reason: ReasonUnreachable}
	}
	path := "/rest/api/2/issue/" + id + "/transitions"
	status, h, data, e := c.do(ctx, http.MethodGet, path, url.Values{"expand": {"transitions.fields"}}, nil)
	if e != nil {
		return e
	}
	if e = statusError(status, h, data); e != nil {
		return e
	}
	var result struct {
		Transitions []struct {
			ID     string
			To     struct{ Name string }
			Fields map[string]struct {
				Required        bool
				HasDefaultValue bool
			}
		}
	}
	if json.Unmarshal(data, &result) != nil || len(result.Transitions) > 500 {
		return &Error{Reason: ReasonUnreachable}
	}
	selected := ""
	for _, tr := range result.Transitions {
		if strings.EqualFold(tr.To.Name, target) {
			if selected != "" {
				return &Error{Reason: ReasonUnreachable, Message: "目标状态有多个可用流转"}
			}
			for _, field := range tr.Fields {
				if field.Required && !field.HasDefaultValue {
					return &Error{Reason: ReasonUnreachable, Message: "状态流转需要补充字段"}
				}
			}
			selected = tr.ID
		}
	}
	if selected == "" {
		return &Error{Reason: ReasonUnreachable, Message: "目标状态不可流转"}
	}
	status, h, data, e = c.do(ctx, http.MethodPost, path, nil, map[string]any{"transition": map[string]string{"id": selected}})
	if e != nil {
		return e
	}
	return statusError(status, h, data)
}
