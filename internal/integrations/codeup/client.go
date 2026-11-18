// Package codeup implements the verified central-edition PAT and new-MR API.
// APIURL/HTTP are server-owned test seams, never user supplied instance settings.
package codeup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"openfox/internal/integrations/gitapi"
)

const MaxCommits = 100
const maxResponse = 2 << 20

var (
	ErrForbidden         = errors.New("codeup: capability or organization access denied")
	ErrCommitLimit       = errors.New("codeup: incomplete or excessive commit payload")
	ErrInvalidRepository = errors.New("codeup: invalid repository identity")
	ErrUnsupported       = errors.New("codeup: unsupported token or API version")
	idPattern            = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
	orgPattern           = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
	shaPattern           = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

type Client struct {
	APIURL string
	HTTP   *http.Client
}

func New() *Client {
	return &Client{APIURL: "https://openapi-rdc.aliyuncs.com", HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second}}}
}
func ValidID(v string) bool  { return idPattern.MatchString(v) }
func ValidOrg(v string) bool { return orgPattern.MatchString(v) }
func ValidSHA(v string) bool { return shaPattern.MatchString(v) }
func ValidPath(v string) bool {
	if len(v) == 0 || len(v) > 255 || len(strings.Split(v, "/")) < 2 {
		return false
	}
	for _, p := range strings.Split(v, "/") {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "\\?#") || strings.ContainsFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
			return false
		}
	}
	return true
}

type ID string

func (s *ID) UnmarshalJSON(b []byte) error {
	var v string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
	} else {
		v = string(b)
	}
	if !ValidID(v) {
		return ErrInvalidRepository
	}
	*s = ID(v)
	return nil
}

type Repository struct {
	ID         ID     `json:"id"`
	Path       string `json:"pathWithNamespace"`
	Name       string `json:"name"`
	WebURL     string `json:"webUrl"`
	Visibility string `json:"visibility"`
	Archived   bool   `json:"archived"`
}

func (r Repository) Valid() bool {
	return ValidID(string(r.ID)) && ValidPath(r.Path) && (r.Visibility == "private" || r.Visibility == "internal") && ValidURL(r.WebURL, r.Path, "")
}
func (r Repository) Private() bool { return true } // internal is organization-visible, never internet-public.
func (r Repository) URL() string   { return r.WebURL }
func ValidURL(raw, path, suffix string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Host != "codeup.aliyun.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Path == "/"+path+suffix && u.RawPath == ""
}
func CommitURL(path, sha string) string {
	return (&url.URL{Scheme: "https", Host: "codeup.aliyun.com", Path: "/" + path + "/commits/" + sha}).String()
}
func base(org string) string         { return "/oapi/v1/codeup/organizations/" + url.PathEscape(org) }
func repoBase(org, id string) string { return base(org) + "/repositories/" + url.PathEscape(id) }
func (c *Client) do(ctx context.Context, token, method, path string, input, output any) (http.Header, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, e := json.Marshal(input)
		if e != nil {
			return nil, gitapi.ErrRequestFailed
		}
		body = bytes.NewReader(raw)
	}
	req, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.APIURL, "/")+path, body)
	if e != nil {
		return nil, gitapi.ErrRequestFailed
	}
	req.Header.Set("x-yunxiao-token", token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = New().HTTP
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	write := method == http.MethodPost && !strings.HasSuffix(path, "/comments/list")
	resp, e := copyClient.Do(req)
	if e != nil {
		if write {
			return nil, gitapi.ErrUnknownResult
		}
		return nil, gitapi.ErrRequestFailed
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if e != nil || len(raw) > maxResponse {
		if write {
			return nil, gitapi.ErrUnknownResult
		}
		return nil, gitapi.ErrRequestFailed
	}
	switch resp.StatusCode {
	case 401:
		return nil, gitapi.ErrTokenRevoked
	case 403:
		return nil, ErrForbidden
	case 404:
		return nil, gitapi.ErrNotFound
	case 429:
		at := time.Now().Add(time.Minute)
		if n, e := strconv.Atoi(resp.Header.Get("Retry-After")); e == nil && n >= 0 {
			at = time.Now().Add(time.Duration(min(n, 86400)) * time.Second)
		} else if t, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
			at = t
		}
		return nil, &gitapi.ErrRateLimited{ResetAt: at}
	case 400:
		var x struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &x)
		if x.Code == "UnsupportedCurrentTokenType" || x.Code == "UnsupportedInCurrentEnv" {
			return nil, ErrUnsupported
		}
		return nil, gitapi.ErrRequestFailed
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if write && resp.StatusCode >= 500 {
			return nil, gitapi.ErrUnknownResult
		}
		return nil, gitapi.ErrRequestFailed
	}
	if output != nil && json.Unmarshal(raw, output) != nil {
		if write {
			return nil, gitapi.ErrUnknownResult
		}
		return nil, gitapi.ErrRequestFailed
	}
	return resp.Header, nil
}
func (c *Client) Identity(ctx context.Context, token, org string) (string, string, error) {
	if !ValidOrg(org) {
		return "", "", ErrInvalidRepository
	}
	var u struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Username string `json:"username"`
	}
	if _, e := c.do(ctx, token, "GET", "/oapi/v1/platform/user", nil, &u); e != nil {
		return "", "", e
	}
	if u.ID == "" || len(u.ID) > 255 {
		return "", "", gitapi.ErrRequestFailed
	}
	var repos []Repository
	if _, e := c.do(ctx, token, "GET", base(org)+"/repositories?perPage=1&page=1", nil, &repos); e != nil {
		return "", "", e
	}
	canonical, _ := json.Marshal([]string{"https://openapi-rdc.aliyuncs.com", org, u.ID})
	hash := sha256.Sum256(canonical)
	name := u.Username
	if name == "" {
		name = u.Name
	}
	if name == "" {
		name = u.ID
	}
	if len(name) > 180 {
		name = "Codeup"
	}
	return hex.EncodeToString(hash[:]), name, nil
}
func (c *Client) Repositories(ctx context.Context, token, org, q string, page int) ([]Repository, bool, error) {
	if !ValidOrg(org) || page < 1 || page > 150 || len(q) > 200 {
		return nil, false, ErrInvalidRepository
	}
	values := url.Values{"search": {q}, "page": {strconv.Itoa(page)}, "perPage": {"20"}}
	var rows []Repository
	h, e := c.do(ctx, token, "GET", base(org)+"/repositories?"+values.Encode(), nil, &rows)
	if e != nil {
		return nil, false, e
	}
	if len(rows) > 20 {
		return nil, false, gitapi.ErrRequestFailed
	}
	for _, r := range rows {
		if !r.Valid() {
			return nil, false, ErrInvalidRepository
		}
	}
	next := h.Get("x-next-page")
	if next != "" {
		n, e := strconv.Atoi(next)
		if e != nil || n <= page || n > 150 {
			return rows, false, gitapi.ErrRequestFailed
		}
		return rows, true, nil
	}
	return rows, false, nil
}
func (c *Client) Repository(ctx context.Context, token, org, id string) (Repository, error) {
	var r Repository
	if !ValidOrg(org) || !ValidID(id) {
		return r, ErrInvalidRepository
	}
	_, e := c.do(ctx, token, "GET", repoBase(org, id), nil, &r)
	if e == nil && (!r.Valid() || string(r.ID) != id) {
		e = ErrInvalidRepository
	}
	return r, e
}

type Pull struct {
	Number       int    `json:"localId"`
	RepositoryID ID     `json:"projectId"`
	TargetID     ID     `json:"targetProjectId"`
	Title        string `json:"title"`
	Body         string `json:"description"`
	Head         string `json:"sourceBranch"`
	Status       string `json:"status"`
	URL          string `json:"webUrl"`
	UpdatedAt    string `json:"updateTime"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
}

func (p Pull) State() string {
	if p.Status == "CLOSED" || p.Status == "MERGED" {
		return "closed"
	}
	return "open"
}
func (c *Client) Pull(ctx context.Context, token, org, id string, n int) (Pull, error) {
	var p Pull
	if n <= 0 || !ValidOrg(org) || !ValidID(id) {
		return p, ErrInvalidRepository
	}
	_, e := c.do(ctx, token, "GET", repoBase(org, id)+"/changeRequests/"+strconv.Itoa(n), nil, &p)
	if e != nil {
		return p, e
	}
	switch p.Status {
	case "UNDER_DEV", "UNDER_REVIEW", "TO_BE_MERGED", "CLOSED", "MERGED":
	default:
		return p, gitapi.ErrRequestFailed
	}
	if p.Number != n || string(p.RepositoryID) != id || string(p.TargetID) != id {
		return p, ErrInvalidRepository
	}
	return p, nil
}

type Commit struct {
	SHA     string `json:"id"`
	Message string `json:"message"`
	URL     string `json:"webUrl"`
	Author  string `json:"authorName"`
}

func (c *Client) Commit(ctx context.Context, token, org, id, sha string) (Commit, error) {
	var out Commit
	if !ValidSHA(sha) || !ValidOrg(org) || !ValidID(id) {
		return out, ErrCommitLimit
	}
	_, e := c.do(ctx, token, "GET", repoBase(org, id)+"/commits/"+sha, nil, &out)
	if e == nil && out.SHA != sha {
		e = ErrCommitLimit
	}
	return out, e
}
func (c *Client) Patchset(ctx context.Context, token, org, id string, n int) (string, error) {
	var rows []struct {
		ID      string `json:"patchSetBizId"`
		Version int    `json:"versionNo"`
		Kind    string `json:"relatedMergeItemType"`
	}
	_, e := c.do(ctx, token, "GET", repoBase(org, id)+"/changeRequests/"+strconv.Itoa(n)+"/diffs/patches", nil, &rows)
	if e != nil {
		return "", e
	}
	if len(rows) > 1000 {
		return "", gitapi.ErrRequestFailed
	}
	best := ""
	v := -1
	for _, r := range rows {
		if r.Kind == "MERGE_SOURCE" && r.Version > v && len(r.ID) > 0 && len(r.ID) <= 128 {
			best = r.ID
			v = r.Version
		}
	}
	if best == "" {
		return "", gitapi.ErrRequestFailed
	}
	return best, nil
}
func (c *Client) Comment(ctx context.Context, token, org, id string, n int, patch, body string) (string, error) {
	if patch == "" || len(patch) > 128 || len(body) == 0 || len(body) > 65535 {
		return "", gitapi.ErrRequestFailed
	}
	var out struct {
		ID string `json:"comment_biz_id"`
	}
	_, e := c.do(ctx, token, "POST", repoBase(org, id)+"/changeRequests/"+strconv.Itoa(n)+"/comments", map[string]any{"comment_type": "GLOBAL_COMMENT", "content": body, "draft": false, "resolved": false, "patchset_biz_id": patch}, &out)
	if e == nil && (out.ID == "" || len(out.ID) > 128) {
		e = gitapi.ErrUnknownResult
	}
	return out.ID, e
}
func (c *Client) FindComment(ctx context.Context, token, org, id string, n int, marker string) (string, error) {
	type item struct {
		ID       string            `json:"comment_biz_id"`
		Content  string            `json:"content"`
		Deleted  bool              `json:"is_deleted"`
		Children []json.RawMessage `json:"child_comments_list"`
	}
	var rows []json.RawMessage
	_, e := c.do(ctx, token, "POST", repoBase(org, id)+"/changeRequests/"+strconv.Itoa(n)+"/comments/list", map[string]any{"comment_type": "GLOBAL_COMMENT"}, &rows)
	if e != nil {
		return "", e
	}
	count := 0
	found := ""
	var walk func([]json.RawMessage, int) error
	walk = func(rows []json.RawMessage, depth int) error {
		if depth > 4 {
			return gitapi.ErrRequestFailed
		}
		for _, raw := range rows {
			count++
			if count > 1000 {
				return gitapi.ErrRequestFailed
			}
			var it item
			if json.Unmarshal(raw, &it) != nil {
				return gitapi.ErrRequestFailed
			}
			if !it.Deleted && strings.Contains(it.Content, marker) && len(it.ID) > 0 && len(it.ID) <= 128 {
				found = it.ID
			}
			if len(it.Children) > 0 {
				if e := walk(it.Children, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e = walk(rows, 0); e != nil {
		return "", e
	}
	return found, nil
}
