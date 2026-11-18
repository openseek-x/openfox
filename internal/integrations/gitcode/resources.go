package gitcode

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openfox/internal/integrations/gitapi"
)

// NumberID accepts only the documented decimal string/integer union (Issue
// number and comment ID). No float, exponent, sign, whitespace or lossy cast.
type NumberID string

// CommentID is opaque on POST (documented PR sample is a 40-character hash)
// and numeric on some list responses. It is stored/compared, never routed as iid.
type CommentID string

func (v *CommentID) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) != nil || len(s) == 0 || len(s) > 64 {
			return ErrInvalid
		}
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return ErrInvalid
			}
		}
		*v = CommentID(s)
		return nil
	}
	var n NumberID
	if e := json.Unmarshal(b, &n); e != nil {
		return e
	}
	*v = CommentID(n)
	return nil
}

func (v *NumberID) UnmarshalJSON(b []byte) error {
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if json.Unmarshal(b, &s) != nil {
			return ErrInvalid
		}
	}
	if len(s) == 0 || len(s) > 32 || s[0] == '0' {
		return ErrInvalid
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return ErrInvalid
		}
	}
	*v = NumberID(s)
	return nil
}
func (v NumberID) Number() (int, error) {
	n, e := strconv.ParseInt(string(v), 10, 32)
	if e != nil || n <= 0 || n > math.MaxInt32 {
		return 0, ErrInvalid
	}
	return int(n), nil
}

type Principal struct {
	ID        string `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

func (p Principal) Valid() bool { return ValidKey(p.ID) && segment(p.Login) }

type Organization struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}
type Membership struct {
	Active       bool         `json:"active"`
	Role         string       `json:"role"`
	User         Principal    `json:"user"`
	Organization Organization `json:"organization"`
}

func (c *Client) Principal(ctx context.Context, token string) (Principal, error) {
	var v Principal
	e := c.request(ctx, token, http.MethodGet, "/user", nil, nil, &v)
	if e == nil && !v.Valid() {
		e = ErrInvalid
	}
	return v, e
}
func (c *Client) Organizations(ctx context.Context, token string) ([]Organization, error) {
	out := []Organization{}
	seen := map[int64]bool{}
	for p := 1; p <= MaxPages; p++ {
		q, _ := pageQuery(p)
		q.Set("admin", "true")
		var list responseList[Organization]
		if e := c.request(ctx, token, http.MethodGet, "/users/orgs", q, nil, &list); e != nil {
			return nil, e
		}
		if len(list) > PageSize {
			return nil, ErrInvalid
		}
		for _, v := range list {
			if v.ID <= 0 || !segment(v.Login) || seen[v.ID] {
				return nil, ErrInvalid
			}
			seen[v.ID] = true
			out = append(out, v)
		}
		if len(list) < PageSize {
			return out, nil
		}
	}
	return nil, ErrIncomplete
}
func (c *Client) Membership(ctx context.Context, token, org string) (Membership, error) {
	var v Membership
	if !segment(org) {
		return v, ErrInvalid
	}
	e := c.request(ctx, token, http.MethodGet, "/user/memberships/orgs/"+org, nil, nil, &v)
	if e == nil && (!v.User.Valid() || v.Organization.ID <= 0 || !segment(v.Organization.Login)) {
		e = ErrInvalid
	}
	return v, e
}

type Repository struct {
	ID        int64  `json:"id"`
	FullName  string `json:"full_name"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	URL       string `json:"web_url"`
	Private   *int   `json:"private"`
	Public    *int   `json:"public"`
	Internal  *int   `json:"internal"`
	Namespace struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	} `json:"namespace"`
}

func (r Repository) Visibility() (string, error) {
	if r.Private == nil || r.Public == nil || r.Internal == nil || *r.Internal != 0 {
		return "", ErrUnsupported
	}
	if *r.Private == 1 && *r.Public == 0 {
		return "private", nil
	}
	if *r.Private == 0 && *r.Public == 1 {
		return "public", nil
	}
	return "", ErrUnsupported
}
func (r Repository) Valid() bool {
	o, n, e := SplitRepository(r.FullName)
	return e == nil && r.ID > 0 && r.Namespace.ID > 0 && r.Namespace.Path == o && r.Path == n && WebURL(r.URL, r.FullName, "")
}
func (r Repository) Common() gitapi.Repo {
	v := gitapi.Repo{FullName: r.FullName, Name: r.Name, Private: r.Private != nil && *r.Private == 1, HTMLURL: r.URL}
	v.Owner.Login = r.Namespace.Path
	return v
}
func (c *Client) Repository(ctx context.Context, token, owner, repo string) (Repository, error) {
	var v Repository
	p, e := repoPath(owner, repo)
	if e != nil {
		return v, e
	}
	e = c.request(ctx, token, http.MethodGet, p, nil, nil, &v)
	if e == nil && (!v.Valid() || v.FullName != owner+"/"+repo) {
		e = ErrInvalid
	}
	return v, e
}
func (c *Client) Repositories(ctx context.Context, token, q string, page int) ([]Repository, bool, error) {
	args, e := pageQuery(page)
	if e != nil {
		return nil, false, e
	}
	if len(q) > 200 {
		return nil, false, ErrInvalid
	}
	args.Set("q", q)
	args.Set("sort", "full_name")
	args.Set("direction", "asc")
	var out responseList[Repository]
	e = c.request(ctx, token, http.MethodGet, "/user/repos", args, nil, &out)
	if e != nil {
		return nil, false, e
	}
	if len(out) > PageSize {
		return nil, false, ErrInvalid
	}
	for _, r := range out {
		if !r.Valid() {
			return nil, false, ErrInvalid
		}
	}
	return out, len(out) == PageSize, nil
}

type Issue struct {
	ID         int64           `json:"id"`
	Number     NumberID        `json:"number"`
	Title      string          `json:"title"`
	Body       string          `json:"body"`
	State      string          `json:"state"`
	URL        string          `json:"html_url"`
	User       Principal       `json:"user"`
	Assignee   json.RawMessage `json:"assignee"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (v Issue) Common(path string) (gitapi.Issue, error) {
	n, e := v.Number.Number()
	if e != nil || v.ID <= 0 || !v.User.Valid() || v.Repository.ID <= 0 || v.Repository.FullName != path || !WebURL(v.URL, path, "/issues/"+string(v.Number)) || v.CreatedAt.IsZero() || v.UpdatedAt.IsZero() {
		return gitapi.Issue{}, ErrInvalid
	}
	state := ""
	switch v.State {
	case "open", "opened":
		state = "open"
	case "closed":
		state = "closed"
	default:
		return gitapi.Issue{}, ErrUnsupported
	}
	out := gitapi.Issue{ID: v.ID, Number: n, Ref: string(v.Number), Title: v.Title, Body: v.Body, State: state, HTMLURL: v.URL, User: gitapi.IssueUser{ExternalKey: v.User.ID, Login: v.User.Login}, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, AssigneesMissing: len(v.Assignee) == 0, AssigneesReadOnly: true}
	if len(v.Assignee) > 0 && string(v.Assignee) != "null" {
		var a Principal
		if json.Unmarshal(v.Assignee, &a) != nil || !a.Valid() {
			return gitapi.Issue{}, ErrInvalid
		}
		out.Assignees = []gitapi.IssueUser{{ExternalKey: a.ID, Login: a.Login}}
	}
	return out, nil
}
func issueRef(ref string) bool {
	v := NumberID(ref)
	n, e := v.Number()
	return e == nil && strconv.Itoa(n) == ref
}
func (c *Client) Issue(ctx context.Context, token, owner, repo, ref string) (Issue, error) {
	var v Issue
	p, e := repoPath(owner, repo)
	if e != nil || !issueRef(ref) {
		return v, ErrInvalid
	}
	e = c.request(ctx, token, http.MethodGet, p+"/issues/"+ref, nil, nil, &v)
	if e == nil {
		_, e = v.Common(owner + "/" + repo)
		if string(v.Number) != ref {
			e = ErrInvalid
		}
	}
	return v, e
}

type IssueWindow struct{ Since, Before, UpdatedBefore time.Time }

func (c *Client) Issues(ctx context.Context, token, owner, repo string, page int, window IssueWindow) ([]Issue, bool, error) {
	p, e := repoPath(owner, repo)
	if e != nil {
		return nil, false, e
	}
	q, e := pageQuery(page)
	if e != nil {
		return nil, false, e
	}
	q.Set("state", "all")
	q.Set("sort", "created")
	q.Set("direction", "asc")
	if !window.Since.IsZero() {
		q.Set("updated_after", window.Since.UTC().Format(time.RFC3339))
	}
	if !window.Before.IsZero() {
		q.Set("created_before", window.Before.UTC().Format(time.RFC3339))
	}
	if !window.UpdatedBefore.IsZero() {
		q.Set("updated_before", window.UpdatedBefore.UTC().Format(time.RFC3339))
	}
	var list responseList[Issue]
	e = c.request(ctx, token, http.MethodGet, p+"/issues", q, nil, &list)
	if e != nil {
		return nil, false, e
	}
	if len(list) > PageSize {
		return nil, false, ErrInvalid
	}
	for _, v := range list {
		if _, e = v.Common(owner + "/" + repo); e != nil {
			return nil, false, e
		}
	}
	return list, len(list) == PageSize, nil
}

type Comment struct {
	ID        CommentID `json:"id"`
	Body      string    `json:"body"`
	User      Principal `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (c *Client) Comments(ctx context.Context, token, owner, repo, kind, ref string, page int) ([]Comment, bool, error) {
	p, e := repoPath(owner, repo)
	if e != nil || !issueRef(ref) || (kind != "issues" && kind != "pulls") {
		return nil, false, ErrInvalid
	}
	q, e := pageQuery(page)
	if e != nil {
		return nil, false, e
	}
	if kind == "issues" {
		q.Set("order", "asc")
	} else {
		q.Set("direction", "asc")
		q.Set("comment_type", "normal")
	}
	var list responseList[Comment]
	e = c.request(ctx, token, http.MethodGet, p+"/"+kind+"/"+ref+"/comments", q, nil, &list)
	if e != nil {
		return nil, false, e
	}
	if len(list) > PageSize {
		return nil, false, ErrInvalid
	}
	for _, v := range list {
		if v.ID == "" || !v.User.Valid() || v.CreatedAt.IsZero() {
			return nil, false, ErrInvalid
		}
	}
	return list, len(list) == PageSize, nil
}

type Pull struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	URL       string    `json:"html_url"`
	User      Principal `json:"user"`
	UpdatedAt time.Time `json:"updated_at"`
	Base      struct {
		Ref  string `json:"ref"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
}

func (c *Client) Pull(ctx context.Context, token, owner, repo string, n int) (Pull, error) {
	var v Pull
	p, e := repoPath(owner, repo)
	if e != nil || n <= 0 {
		return v, ErrInvalid
	}
	e = c.request(ctx, token, http.MethodGet, p+"/pulls/"+strconv.Itoa(n), nil, nil, &v)
	if e == nil && (v.ID <= 0 || v.Number != n || !v.User.Valid() || v.Base.Repo.FullName != owner+"/"+repo || v.UpdatedAt.IsZero() || !WebURL(v.URL, owner+"/"+repo, "/pull/"+strconv.Itoa(n))) {
		e = ErrInvalid
	}
	if e == nil && v.State != "open" && v.State != "closed" && v.State != "merged" {
		e = ErrUnsupported
	}
	return v, e
}

type Commit struct {
	SHA    string `json:"sha"`
	URL    string `json:"html_url"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

func ValidSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, v := range s {
		if !(v >= '0' && v <= '9' || v >= 'a' && v <= 'f') {
			return false
		}
	}
	return strings.Trim(s, "0") != ""
}
func (c *Client) Commit(ctx context.Context, token, owner, repo, sha string) (Commit, error) {
	var v Commit
	p, e := repoPath(owner, repo)
	if e != nil || !ValidSHA(sha) {
		return v, ErrInvalid
	}
	e = c.request(ctx, token, http.MethodGet, p+"/commits/"+sha, url.Values{"show_diff": {"false"}}, nil, &v)
	if e == nil && (v.SHA != sha || !WebURL(v.URL, owner+"/"+repo, "/commit/"+sha)) {
		e = ErrInvalid
	}
	return v, e
}

// IssueComment verifies the REST comment's target Issue; a webhook's integer
// author ID is never substituted for the opaque REST principal.
func (c *Client) IssueComment(ctx context.Context, token, owner, repo, id, ref string) (Comment, error) {
	var out struct {
		Comment
		Target struct {
			Issue struct {
				ID     int64    `json:"id"`
				Number NumberID `json:"number"`
			} `json:"issue"`
		} `json:"target"`
	}
	p, e := repoPath(owner, repo)
	if e != nil || !issueRef(id) || !issueRef(ref) {
		return Comment{}, ErrInvalid
	}
	e = c.request(ctx, token, http.MethodGet, p+"/issues/comments/"+id, nil, nil, &out)
	if e == nil && (string(out.ID) != id || !out.User.Valid() || out.CreatedAt.IsZero() || out.Target.Issue.ID <= 0 || string(out.Target.Issue.Number) != ref) {
		e = ErrInvalid
	}
	return out.Comment, e
}
