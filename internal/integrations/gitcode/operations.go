package gitcode

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"openfox/internal/integrations/gitapi"
)

var _ gitapi.Client = (*Client)(nil)

func (*Client) SupportsAssignees() bool                    { return false }
func (*Client) AuthorizeURL(string, string, string) string { return "" }
func (*Client) Exchange(context.Context, string, string, string) (gitapi.Token, error) {
	return gitapi.Token{}, ErrUnsupported
}
func (*Client) Refresh(context.Context, string) (gitapi.Token, error) {
	return gitapi.Token{}, gitapi.ErrRefreshUnsupported
}

// Generic OAuth identity consumers must never interpret an opaque subject as 0.
func (*Client) User(context.Context, string) (gitapi.User, error) {
	return gitapi.User{}, ErrUnsupported
}
func (*Client) Orgs(context.Context, string) ([]gitapi.Org, error) { return nil, ErrUnsupported }
func (c *Client) ListRepos(ctx context.Context, token string, _ bool, login, q string, page int) ([]gitapi.Repo, bool, error) {
	rows, more, e := c.Repositories(ctx, token, q, page)
	if e != nil {
		return nil, false, e
	}
	out := []gitapi.Repo{}
	for _, r := range rows {
		if r.Namespace.Path == login {
			if _, e := r.Visibility(); e == nil {
				out = append(out, r.Common())
			}
		}
	}
	return out, more, nil
}
func (c *Client) GetRepo(ctx context.Context, token, owner, repo string) (gitapi.Repo, error) {
	r, e := c.Repository(ctx, token, owner, repo)
	if e == nil {
		_, e = r.Visibility()
	}
	return r.Common(), e
}
func (c *Client) ListIssues(ctx context.Context, token, owner, repo string, page int) ([]gitapi.Issue, bool, error) {
	rows, more, e := c.Issues(ctx, token, owner, repo, page, IssueWindow{})
	if e != nil {
		return nil, false, e
	}
	out := []gitapi.Issue{}
	for _, r := range rows {
		v, e := r.Common(owner + "/" + repo)
		if e != nil {
			return nil, false, e
		}
		out = append(out, v)
	}
	return out, more, nil
}

// UNKNOWN reconciliation uses the durable original stable subject and full
// bounded repository listing, never the generic current-login creator shortcut.
func (*Client) ListIssuesByCreator(context.Context, string, string, string, string, time.Time) ([]gitapi.Issue, error) {
	return nil, ErrUnsupported
}
func (c *Client) CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (gitapi.Issue, error) {
	if !segment(owner) || !segment(repo) || len(assignees) > 0 || title == "" {
		return gitapi.Issue{}, ErrUnsupported
	}
	var r Issue
	e := c.request(ctx, token, http.MethodPost, "/repos/"+owner+"/issues", nil, map[string]string{"repo": repo, "title": title, "body": body}, &r)
	if e != nil {
		return gitapi.Issue{}, e
	}
	v, e := r.Common(owner + "/" + repo)
	if e != nil {
		return v, gitapi.ErrUnknownResult
	}
	return v, nil
}
func (c *Client) UpdateIssue(ctx context.Context, token, owner, repo, ref string, in gitapi.IssueUpdate) (gitapi.Issue, error) {
	if !segment(owner) || !segment(repo) || !issueRef(ref) || len(in.Assignees) > 0 || in.Title == "" {
		return gitapi.Issue{}, ErrUnsupported
	}
	state := ""
	switch in.State {
	case "open":
		state = "reopen"
	case "closed":
		state = "close"
	default:
		return gitapi.Issue{}, ErrUnsupported
	}
	e := c.request(ctx, token, http.MethodPatch, "/repos/"+owner+"/issues/"+ref, nil, map[string]string{"repo": repo, "title": in.Title, "body": in.Body, "state": state}, nil)
	if e != nil {
		return gitapi.Issue{}, e
	}
	r, e := c.Issue(ctx, token, owner, repo, ref)
	if e != nil {
		return gitapi.Issue{}, e
	}
	return r.Common(owner + "/" + repo)
}
func (c *Client) postComment(ctx context.Context, token, owner, repo, kind, ref, body string) (string, error) {
	p, e := repoPath(owner, repo)
	if e != nil || !issueRef(ref) || body == "" {
		return "", ErrInvalid
	}
	var r struct {
		ID CommentID `json:"id"`
	}
	e = c.request(ctx, token, http.MethodPost, p+"/"+kind+"/"+ref+"/comments", nil, map[string]string{"body": body}, &r)
	if e == nil && r.ID == "" {
		e = gitapi.ErrUnknownResult
	}
	return string(r.ID), e
}
func (c *Client) CommentIssue(ctx context.Context, token, owner, repo, ref, body string) (string, error) {
	return c.postComment(ctx, token, owner, repo, "issues", ref, body)
}
func (c *Client) CommentPullRequest(ctx context.Context, token, owner, repo string, n int, body string) (string, error) {
	return c.postComment(ctx, token, owner, repo, "pulls", strconv.Itoa(n), body)
}

type Hook struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"project_id"`
	URL       string `json:"url"`
	Active    bool   `json:"active"`
	Push      bool   `json:"push_events"`
	Issues    bool   `json:"issues_events"`
	Notes     bool   `json:"note_events"`
	Pulls     bool   `json:"merge_requests_events"`
	Tags      bool   `json:"tag_push_events"`
}

func (h Hook) Matches(project int64, callback string) bool {
	return h.ID > 0 && h.ProjectID == project && h.URL == callback && h.Active && h.Push && h.Issues && h.Notes && h.Pulls && !h.Tags
}
func (c *Client) Hooks(ctx context.Context, token, owner, repo string, page int) ([]Hook, bool, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return nil, false, e
	}
	q, e := pageQuery(page)
	if e != nil {
		return nil, false, e
	}
	var rows responseList[Hook]
	e = c.request(ctx, token, http.MethodGet, p+"/hooks", q, nil, &rows)
	if e != nil {
		return nil, false, e
	}
	if len(rows) > PageSize {
		return nil, false, ErrInvalid
	}
	for _, v := range rows {
		if v.ID <= 0 || v.ProjectID <= 0 || v.URL == "" {
			return nil, false, ErrInvalid
		}
	}
	return rows, len(rows) == PageSize, nil
}
func (c *Client) Hook(ctx context.Context, token, owner, repo string, id int64) (Hook, error) {
	var v Hook
	p, e := repoPath(owner, repo)
	if e != nil || id <= 0 {
		return v, ErrInvalid
	}
	e = c.request(ctx, token, http.MethodGet, p+"/hooks/"+strconv.FormatInt(id, 10), nil, nil, &v)
	if e == nil && (v.ID != id || v.ProjectID <= 0) {
		e = ErrInvalid
	}
	return v, e
}
func (c *Client) PutHook(ctx context.Context, token, owner, repo string, id int64, callback, secret string) (Hook, error) {
	var v Hook
	p, e := repoPath(owner, repo)
	if e != nil || !validToken(secret) || callback == "" {
		return v, ErrInvalid
	}
	method := http.MethodPost
	p += "/hooks"
	if id > 0 {
		method = http.MethodPatch
		p += "/" + strconv.FormatInt(id, 10)
	}
	input := map[string]any{"url": callback, "encryption_type": 0, "password": secret, "push_events": true, "issues_events": true, "note_events": true, "merge_requests_events": true, "tag_push_events": false}
	e = c.request(ctx, token, method, p, nil, input, &v)
	if e == nil && (v.ID <= 0 || v.ProjectID <= 0) {
		if id == 0 {
			e = gitapi.ErrUnknownResult
		} else {
			e = ErrInvalid
		}
	}
	return v, e
}
func (c *Client) DeleteHook(ctx context.Context, token, owner, repo string, id int64) error {
	p, e := repoPath(owner, repo)
	if e != nil || id <= 0 {
		return ErrInvalid
	}
	return c.request(ctx, token, http.MethodDelete, p+"/hooks/"+strconv.FormatInt(id, 10), nil, nil, nil)
}
