package azuredevops

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type IdentityRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Descriptor  string `json:"descriptor"`
}
type PullRequest struct {
	RemoteURL     string      `json:"remoteUrl"`
	ID            int64       `json:"pullRequestId"`
	Repository    Repository  `json:"repository"`
	CreatedBy     IdentityRef `json:"createdBy"`
	Title         string      `json:"title"`
	Description   string      `json:"description"`
	Status        string      `json:"status"`
	IsDraft       bool        `json:"isDraft"`
	SourceRefName string      `json:"sourceRefName"`
	TargetRefName string      `json:"targetRefName"`
}
type RefUpdate struct {
	Name        string `json:"name"`
	OldObjectID string `json:"oldObjectId"`
	NewObjectID string `json:"newObjectId"`
}
type Push struct {
	ID         int64       `json:"pushId"`
	Repository Repository  `json:"repository"`
	PushedBy   IdentityRef `json:"pushedBy"`
	RefUpdates []RefUpdate `json:"refUpdates"`
	Date       time.Time   `json:"date"`
}

// CommitAuthor is untrusted commit metadata, never a verified account identity.
type CommitAuthor struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}
type Commit struct {
	RemoteURL        string       `json:"remoteUrl"`
	ID               string       `json:"commitId"`
	Comment          string       `json:"comment"`
	CommentTruncated bool         `json:"commentTruncated"`
	Author           CommitAuthor `json:"author"`
	Committer        CommitAuthor `json:"committer"`
}

func validCommit(v *Commit) bool {
	if !ValidSHA(v.ID) || !boundedText(v.RemoteURL) || !boundedText(v.Comment) || !boundedText(v.Author.Name) || !boundedText(v.Author.Email) || !boundedText(v.Committer.Name) || !boundedText(v.Committer.Email) {
		return false
	}
	v.ID = strings.ToLower(v.ID)
	return true
}

// Commit reads only commit metadata. Changed files and returned URLs are never fetched.
func (c *Client) Commit(ctx context.Context, token, org, project, repo, sha string) (Commit, error) {
	if !validRepo(org, project, repo) || !ValidSHA(sha) {
		return Commit{}, invalid()
	}
	var out Commit
	q := url.Values{"changeCount": {"0"}}
	_, err := c.request(ctx, "GET", apiURL("dev.azure.com", repoPath(org, project, repo)+"/commits/"+strings.ToLower(sha), q), token, nil, nil, false, &out)
	if err != nil {
		return Commit{}, err
	}
	if !validCommit(&out) || out.ID != strings.ToLower(sha) || out.CommentTruncated {
		return Commit{}, protocol()
	}
	return out, nil
}

type ThreadComment struct {
	ID              int64       `json:"id"`
	ParentCommentID int64       `json:"parentCommentId"`
	Author          IdentityRef `json:"author"`
	Content         string      `json:"content"`
	Deleted         bool        `json:"isDeleted"`
	CommentType     string      `json:"commentType"`
}
type Thread struct {
	ID       int64           `json:"id"`
	Comments []ThreadComment `json:"comments"`
	Status   string          `json:"status"`
	Deleted  bool            `json:"isDeleted"`
}

func repoPath(org, project, repo string) string {
	return projectPath(org, project) + "/_apis/git/repositories/" + uuid(repo)
}
func validRepo(org, project, repo string) bool {
	return ValidOrganization(org) && ValidUUID(project) && ValidUUID(repo)
}
func validIdentityRef(i *IdentityRef) bool {
	if !ValidUUID(i.ID) || !boundedText(i.DisplayName) || !boundedText(i.Descriptor) {
		return false
	}
	i.ID = uuid(i.ID)
	return true
}
func boundedText(s string) bool {
	return len(s) <= MaxCellBytes && utf8.ValidString(s) && !strings.ContainsRune(s, '\x00')
}
func matchesRepo(r *Repository, project, repo string) bool {
	if !ValidUUID(r.ID) || !ValidUUID(r.Project.ID) || uuid(r.ID) != uuid(repo) || uuid(r.Project.ID) != uuid(project) || !boundedText(r.Name) {
		return false
	}
	r.ID = uuid(r.ID)
	r.Project.ID = uuid(r.Project.ID)
	return true
}
func (c *Client) PullRequest(ctx context.Context, token, org, project, repo string, id int64) (PullRequest, error) {
	if !validRepo(org, project, repo) || !ValidObjectID(id) {
		return PullRequest{}, invalid()
	}
	var out PullRequest
	_, err := c.request(ctx, "GET", apiURL("dev.azure.com", repoPath(org, project, repo)+"/pullRequests/"+strconv.FormatInt(id, 10), nil), token, nil, nil, false, &out)
	if err != nil {
		return PullRequest{}, err
	}
	if out.ID != id || !boundedText(out.RemoteURL) || !matchesRepo(&out.Repository, project, repo) || !validIdentityRef(&out.CreatedBy) || !ValidText(out.Title, MaxCellBytes) || !boundedText(out.Description) || !ValidText(out.SourceRefName, MaxCellBytes) || !ValidText(out.TargetRefName, MaxCellBytes) {
		return PullRequest{}, protocol()
	}
	switch out.Status {
	case "active", "abandoned", "completed":
	default:
		return PullRequest{}, protocol()
	}
	return out, nil
}
func (c *Client) Push(ctx context.Context, token, org, project, repo string, id int64) (Push, error) {
	if !validRepo(org, project, repo) || !ValidObjectID(id) {
		return Push{}, invalid()
	}
	var out Push
	q := url.Values{"includeRefUpdates": {"true"}, "includeCommits": {"0"}}
	_, err := c.request(ctx, "GET", apiURL("dev.azure.com", repoPath(org, project, repo)+"/pushes/"+strconv.FormatInt(id, 10), q), token, nil, nil, false, &out)
	if err != nil {
		return Push{}, err
	}
	if out.ID != id || !matchesRepo(&out.Repository, project, repo) || !validIdentityRef(&out.PushedBy) || out.RefUpdates == nil || len(out.RefUpdates) > MaxItems || out.Date.IsZero() {
		return Push{}, protocol()
	}
	seen := map[string]bool{}
	for i := range out.RefUpdates {
		r := &out.RefUpdates[i]
		if !ValidText(r.Name, MaxCellBytes) || !strings.HasPrefix(r.Name, "refs/") || !ValidSHA(r.OldObjectID) || !ValidSHA(r.NewObjectID) || seen[r.Name] {
			return Push{}, protocol()
		}
		seen[r.Name] = true
		r.OldObjectID = strings.ToLower(r.OldObjectID)
		r.NewObjectID = strings.ToLower(r.NewObjectID)
	}
	return out, nil
}

// PushCommits returns a complete push or an error. The 101st item is solely an
// over-limit probe; callers never receive a truncated successful prefix.
func (c *Client) PushCommits(ctx context.Context, token, org, project, repo string, id int64) ([]Commit, error) {
	if !validRepo(org, project, repo) || !ValidObjectID(id) {
		return nil, invalid()
	}
	commits := []Commit{}
	seen := map[string]bool{}
	// Track successful-response pacing even for the optional full-message reads.
	// Runtime supplies the wait gate; a bare client returns instead of violating it.
	client := *c
	var nextDelay time.Duration
	observe := client.Observe
	client.Observe = func(m ResponseMeta) {
		nextDelay = m.RetryAfter
		if observe != nil {
			observe(m)
		}
	}
	for len(commits) <= MaxCommits {
		top := min(50, MaxCommits+1-len(commits))
		q := url.Values{"pushId": {strconv.FormatInt(id, 10)}, "top": {strconv.Itoa(top)}, "skip": {strconv.Itoa(len(commits))}}
		var page struct {
			Count *int     `json:"count"`
			Value []Commit `json:"value"`
		}
		h, err := client.request(ctx, "GET", apiURL("dev.azure.com", repoPath(org, project, repo)+"/commits", q), token, nil, nil, false, &page)
		if err != nil {
			return nil, err
		}
		if page.Value == nil || len(page.Value) > top || page.Count != nil && *page.Count != len(page.Value) || h.Get("x-ms-continuationtoken") != "" {
			return nil, protocol()
		}
		for i := range page.Value {
			v := &page.Value[i]
			if !validCommit(v) || seen[v.ID] {
				return nil, protocol()
			}
			v.ID = strings.ToLower(v.ID)
			seen[v.ID] = true
		}
		commits = append(commits, page.Value...)
		if len(commits) > MaxCommits {
			return nil, &Error{Code: "source_limit"}
		}
		if len(page.Value) < top {
			for i := range commits {
				if !commits[i].CommentTruncated {
					continue
				}
				if nextDelay > 0 && c.BeforeRequest == nil {
					return nil, &Error{Code: "rate_limited", HTTPStatus: 200, RetryAfter: nextDelay}
				}
				full, err := client.Commit(ctx, token, org, project, repo, commits[i].ID)
				if err != nil {
					return nil, err
				}
				commits[i] = full
			}
			return commits, nil
		}
		if delay := retryAfter(h.Get("Retry-After"), time.Now()); delay > 0 && c.BeforeRequest == nil {
			// A successful limited page must not be retried immediately or followed
			// by another page. Discard this prefix; the durable caller delays its
			// next read and may safely restart the whole bounded push enumeration.
			return nil, &Error{Code: "rate_limited", HTTPStatus: 200, RetryAfter: delay}
		}
	}
	return nil, &Error{Code: "source_limit"}
}
func validateThread(t *Thread) bool {
	if !ValidObjectID(t.ID) || t.Comments == nil || len(t.Comments) > MaxItems || !boundedText(t.Status) {
		return false
	}
	seen := map[int64]bool{}
	for i := range t.Comments {
		v := &t.Comments[i]
		if !ValidObjectID(v.ID) || v.ParentCommentID < 0 || v.ParentCommentID > 2147483647 || seen[v.ID] || !boundedText(v.Content) || !boundedText(v.CommentType) {
			return false
		}
		if !v.Deleted && !validIdentityRef(&v.Author) {
			return false
		}
		seen[v.ID] = true
	}
	return true
}
func (c *Client) Threads(ctx context.Context, token, org, project, repo string, id int64) ([]Thread, error) {
	if !validRepo(org, project, repo) || !ValidObjectID(id) {
		return nil, invalid()
	}
	var out struct {
		Count *int     `json:"count"`
		Value []Thread `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", repoPath(org, project, repo)+"/pullRequests/"+strconv.FormatInt(id, 10)+"/threads", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > MaxItems || out.Count != nil && *out.Count != len(out.Value) || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[int64]bool{}
	comments := 0
	for i := range out.Value {
		v := &out.Value[i]
		if !validateThread(v) || seen[v.ID] {
			return nil, protocol()
		}
		comments += len(v.Comments)
		if comments > MaxItems {
			return nil, &Error{Code: "source_limit"}
		}
		seen[v.ID] = true
	}
	return out.Value, nil
}
func (c *Client) CreateThread(ctx context.Context, token, org, project, repo string, id int64, content string) (Thread, error) {
	if !validRepo(org, project, repo) || !ValidObjectID(id) || strings.TrimSpace(content) == "" || !boundedText(content) {
		return Thread{}, invalid()
	}
	input := map[string]any{"status": 1, "comments": []any{map[string]any{"parentCommentId": 0, "content": content, "commentType": 1}}}
	var out Thread
	_, err := c.request(ctx, "POST", apiURL("dev.azure.com", repoPath(org, project, repo)+"/pullRequests/"+strconv.FormatInt(id, 10)+"/threads", nil), token, input, nil, true, &out)
	if err != nil {
		return Thread{}, err
	}
	if !validateThread(&out) || len(out.Comments) != 1 || out.Comments[0].Content != content || out.Comments[0].Deleted || out.Deleted {
		return Thread{}, &Error{Code: "invalid_response", Unknown: true}
	}
	return out, nil
}
