// Package cnb implements only the verified CNB repository, PR, commit and comment API.
// Production always uses api.cnb.cool. Injected URL/HTTP fields are server-owned test seams.
package cnb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"openfox/internal/integrations/gitapi"
)

const MaxCommits = 100
const maxResponse = 2 << 20

var ErrForbidden = errors.New("cnb: capability or resource access denied")
var ErrCommitLimit = errors.New("cnb: commit comparison is incomplete or exceeds 100")
var ErrInvalidRepository = errors.New("cnb: repository identity is invalid")

type Client struct {
	APIURL string
	HTTP   *http.Client
}

func New() *Client {
	return &Client{APIURL: "https://api.cnb.cool", HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second,
	}}}
}

type Repository struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	Visibility string `json:"visibility_level"`
	Access     string `json:"access"`
}

func ValidPath(path string) bool {
	if len(path) > 255 || len(strings.Split(path, "/")) < 2 {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\") {
			return false
		}
		for _, r := range part {
			if unicode.IsControl(r) || unicode.IsSpace(r) {
				return false
			}
		}
	}
	return true
}

func repoPath(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

func (r Repository) Valid() bool {
	return r.ID != "" && len(r.ID) <= 255 && ValidPath(r.Path) && (r.Visibility == "Private" || r.Visibility == "Public")
}
func (r Repository) Private() bool { return r.Visibility == "Private" }
func (r Repository) URL() string   { return "https://cnb.cool/" + repoPath(r.Path) }
func PullURL(path string, number int) string {
	return "https://cnb.cool/" + repoPath(path) + "/-/pulls/" + strconv.Itoa(number)
}
func CommitURL(path, sha string) string {
	return "https://cnb.cool/" + repoPath(path) + "/-/commit/" + sha
}

type Pull struct {
	Number   string     `json:"number"`
	Title    string     `json:"title"`
	Body     string     `json:"body"`
	State    string     `json:"state"`
	Merged   bool       `json:"is_merged"`
	Draft    bool       `json:"is_wip"`
	ClosedAt *time.Time `json:"closed_at"`
	Author   struct {
		Username string `json:"username"`
	} `json:"author"`
	Base struct {
		Repo Repository `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
	} `json:"head"`
}
type Commit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
	} `json:"commit"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
}
type Comment struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

func (c *Client) Identity(ctx context.Context, token string) (string, string, error) {
	var user struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := c.do(ctx, token, "GET", "/user", nil, &user); err != nil {
		return "", "", err
	}
	if user.ID == "" || len(user.ID) > 255 || len(user.Username) > 200 {
		return "", "", gitapi.ErrRequestFailed
	}
	if user.Username == "" {
		user.Username = user.ID
	}
	return user.ID, user.Username, nil
}

func (c *Client) do(ctx context.Context, token, method, path string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return gitapi.ErrRequestFailed
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.APIURL, "/")+path, body)
	if err != nil {
		return gitapi.ErrRequestFailed
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.cnb.api+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
	httpClient := *c.HTTP
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := httpClient.Do(req)
	if err != nil {
		if method == http.MethodPost {
			return gitapi.ErrUnknownResult
		}
		return gitapi.ErrRequestFailed
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 401:
		return gitapi.ErrTokenRevoked
	case 403:
		return ErrForbidden
	case 404:
		return gitapi.ErrNotFound
	case 429:
		at := time.Time{}
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds >= 0 && seconds <= 86400 {
			at = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			at = date
		}
		return &gitapi.ErrRateLimited{ResetAt: at}
	}
	want := http.StatusOK
	if method == http.MethodPost {
		want = http.StatusCreated
	}
	if resp.StatusCode != want {
		if method == http.MethodPost && resp.StatusCode >= 500 {
			return gitapi.ErrUnknownResult
		}
		return gitapi.ErrRequestFailed
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse || json.Unmarshal(raw, output) != nil {
		if method == http.MethodPost {
			return gitapi.ErrUnknownResult
		}
		return gitapi.ErrRequestFailed
	}
	return nil
}

func (c *Client) Repositories(ctx context.Context, token, search string, page int) ([]Repository, bool, error) {
	if page < 1 || page > 20 || len(search) > 100 {
		return nil, false, gitapi.ErrRequestFailed
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "page_size": {"50"}, "role": {"Reporter"}, "search": {search}}
	var rows []Repository
	if err := c.do(ctx, token, "GET", "/user/repos?"+q.Encode(), nil, &rows); err != nil {
		return nil, false, err
	}
	if len(rows) > 50 {
		return nil, false, gitapi.ErrRequestFailed
	}
	more := len(rows) == 50 && page < 20
	out := make([]Repository, 0, len(rows))
	for _, row := range rows {
		if row.Valid() {
			out = append(out, row)
		}
	}
	return out, more, nil
}
func (c *Client) Repository(ctx context.Context, token, path string) (Repository, error) {
	var row Repository
	if !ValidPath(path) {
		return row, ErrInvalidRepository
	}
	if err := c.do(ctx, token, "GET", "/"+repoPath(path), nil, &row); err != nil {
		return row, err
	}
	if !row.Valid() || row.Path != path {
		return Repository{}, ErrInvalidRepository
	}
	return row, nil
}
func (c *Client) Pull(ctx context.Context, token, path string, number int) (Pull, error) {
	var row Pull
	if !ValidPath(path) || number <= 0 || int64(number) > 2147483647 {
		return row, gitapi.ErrRequestFailed
	}
	if err := c.do(ctx, token, "GET", "/"+repoPath(path)+"/-/pulls/"+strconv.Itoa(number), nil, &row); err != nil {
		return row, err
	}
	if row.Number != strconv.Itoa(number) || row.Base.Repo.Path != path || row.Base.Repo.ID == "" || (row.State != "open" && row.State != "closed" && row.State != "merged") {
		return Pull{}, gitapi.ErrRequestFailed
	}
	return row, nil
}
func ValidSHA(sha string) bool {
	if len(sha) != 40 && len(sha) != 64 {
		return false
	}
	for _, r := range sha {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func (c *Client) Commits(ctx context.Context, token, path, before, head string) ([]Commit, error) {
	if !ValidPath(path) || !ValidSHA(head) || !ValidSHA(before) {
		return nil, gitapi.ErrRequestFailed
	}
	if strings.Trim(before, "0") == "" {
		var item Commit
		if err := c.do(ctx, token, "GET", "/"+repoPath(path)+"/-/git/commits/"+head, nil, &item); err != nil {
			return nil, err
		}
		if item.SHA != head {
			return nil, gitapi.ErrRequestFailed
		}
		return []Commit{item}, nil
	}
	var result struct {
		Commits []Commit `json:"commits"`
		Total   int      `json:"total_commits"`
		Base    Commit   `json:"base_commit"`
		Head    Commit   `json:"head_commit"`
	}
	if err := c.do(ctx, token, "GET", "/"+repoPath(path)+"/-/git/compare/"+before+"..."+head, nil, &result); err != nil {
		return nil, err
	}
	if result.Total > MaxCommits || result.Total != len(result.Commits) {
		return nil, ErrCommitLimit
	}
	if result.Base.SHA != before || result.Head.SHA != head {
		return nil, gitapi.ErrRequestFailed
	}
	for _, item := range result.Commits {
		if !ValidSHA(item.SHA) {
			return nil, gitapi.ErrRequestFailed
		}
	}
	return result.Commits, nil
}
func (c *Client) Comment(ctx context.Context, token, path string, number int, body string) (string, error) {
	if !ValidPath(path) || number <= 0 || len(body) > 65536 {
		return "", gitapi.ErrRequestFailed
	}
	var row Comment
	err := c.do(ctx, token, "POST", "/"+repoPath(path)+"/-/pulls/"+strconv.Itoa(number)+"/comments", map[string]any{"body": body, "work_mode": false}, &row)
	if err != nil {
		return "", err
	}
	if row.ID == "" || len(row.ID) > 64 {
		return "", gitapi.ErrUnknownResult
	}
	return row.ID, nil
}

// FindComment reconciles an unknown result without ever retrying the POST.
func (c *Client) FindComment(ctx context.Context, token, path string, number int, marker string) (string, error) {
	if !ValidPath(path) || number <= 0 {
		return "", gitapi.ErrRequestFailed
	}
	for page := 1; page <= 5; page++ {
		var rows []Comment
		err := c.do(ctx, token, "GET", "/"+repoPath(path)+"/-/pulls/"+strconv.Itoa(number)+"/comments?page_size=50&page="+strconv.Itoa(page), nil, &rows)
		if err != nil {
			return "", err
		}
		if len(rows) > 50 {
			return "", gitapi.ErrRequestFailed
		}
		for _, row := range rows {
			if strings.Contains(row.Body, marker) && row.ID != "" && len(row.ID) <= 64 {
				return row.ID, nil
			}
		}
		if len(rows) < 50 {
			return "", nil
		}
	}
	return "", nil
}
