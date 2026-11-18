package bitbucket

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"openfox/internal/integrations/gitapi"
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ValidSHA(value string) bool {
	return shaPattern.MatchString(value) && value != strings.Repeat("0", 40)
}
func validSlug(value string) bool {
	return len(value) > 0 && len(value) <= 255 && value != "." && value != ".." && slugPattern.MatchString(value)
}

// RepositoryURL constructs navigation from verified path segments, never from
// upstream links.html (which is not an authority for outbound credentials).
func RepositoryURL(fullName string) string {
	parts := strings.Split(fullName, "/")
	if len(fullName) > 255 || len(parts) != 2 || !validSlug(parts[0]) || !validSlug(parts[1]) {
		return ""
	}
	return Origin + "/" + parts[0] + "/" + parts[1]
}
func repoURL(workspaceID, repositoryID string) (string, error) {
	w, err := uuidPath(workspaceID)
	if err != nil {
		return "", err
	}
	r, err := uuidPath(repositoryID)
	if err != nil {
		return "", err
	}
	return APIURL + "/repositories/" + w + "/" + r, nil
}

func (c *Client) Workspace(ctx context.Context, token, id string) (Workspace, error) {
	var out Workspace
	canonical, err := UUID(id)
	if err != nil {
		return out, err
	}
	path, _ := uuidPath(canonical)
	if err = c.request(ctx, token, "GET", APIURL+"/workspaces/"+path, nil, &out, 200); err != nil {
		return Workspace{}, err
	}
	out.UUID, err = UUID(out.UUID)
	if err != nil || out.UUID != canonical || !validSlug(out.Slug) || len(out.Name) > 1024 {
		return Workspace{}, ErrInvalid
	}
	return out, nil
}

type Repository struct {
	UUID      string    `json:"uuid"`
	FullName  string    `json:"full_name"`
	Name      string    `json:"name"`
	SCM       string    `json:"scm"`
	Private   *bool     `json:"is_private"`
	Workspace Workspace `json:"workspace"`
}

func normalizeRepository(out *Repository, workspace Workspace) error {
	id, err := UUID(out.UUID)
	if err != nil || out.Private == nil || out.SCM != "git" || len(out.Name) > 1024 || RepositoryURL(out.FullName) == "" || strings.Split(out.FullName, "/")[0] != workspace.Slug {
		return ErrInvalid
	}
	// The REST schema omits workspace whereas the documented event entity
	// includes it. Bind full_name to a fresh UUID-addressed workspace lookup;
	// any explicit workspace UUID must independently agree as well.
	if out.Workspace.UUID != "" {
		w, e := UUID(out.Workspace.UUID)
		if e != nil || w != workspace.UUID {
			return ErrInvalid
		}
	}
	out.UUID, out.Workspace = id, workspace
	return nil
}

func (c *Client) Repository(ctx context.Context, token, workspaceID, repositoryID string) (Repository, error) {
	var out Repository
	path, err := repoURL(workspaceID, repositoryID)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout*3)
	defer cancel()
	if err = c.request(ctx, token, "GET", path, nil, &out, 200); err != nil {
		return Repository{}, err
	}
	w, err := c.Workspace(ctx, token, workspaceID)
	if err != nil {
		return Repository{}, err
	}
	expected, _ := UUID(repositoryID)
	if err = normalizeRepository(&out, w); err != nil || out.UUID != expected {
		return Repository{}, ErrInvalid
	}
	return out, nil
}

func (c *Client) Repositories(ctx context.Context, token, workspaceID, next string) (Page[Repository], error) {
	w, err := c.Workspace(ctx, token, workspaceID)
	if err != nil {
		return Page[Repository]{}, err
	}
	path, _ := uuidPath(w.UUID)
	out, err := readPage[Repository](ctx, c, token, APIURL+"/repositories/"+path, next)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for i := range out.Values {
		if err = normalizeRepository(&out.Values[i], w); err != nil || seen[out.Values[i].UUID] {
			return Page[Repository]{}, ErrInvalid
		}
		seen[out.Values[i].UUID] = true
	}
	return out, nil
}

type RawText struct {
	Raw *string `json:"raw"`
}
type PullEndpoint struct {
	Repository struct {
		UUID string `json:"uuid"`
	} `json:"repository"`
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
}
type PullRequest struct {
	ID          int64        `json:"id"`
	Title       string       `json:"title"`
	Description *string      `json:"description"`
	Summary     RawText      `json:"summary"`
	State       string       `json:"state"`
	Draft       bool         `json:"draft"`
	Author      User         `json:"author"`
	Source      PullEndpoint `json:"source"`
	Destination PullEndpoint `json:"destination"`
	UpdatedAt   time.Time    `json:"updated_on"`
}

func (p PullRequest) Body() string {
	if p.Description != nil {
		return *p.Description
	}
	if p.Summary.Raw != nil {
		return *p.Summary.Raw
	}
	return ""
}
func (p PullRequest) LinkState() (string, error) {
	switch p.State {
	case "OPEN":
		if p.Draft {
			return "DRAFT", nil
		}
		return "OPEN", nil
	case "MERGED":
		return "MERGED", nil
	case "DECLINED":
		return "CLOSED", nil
	default:
		return "", ErrInvalid
	}
}
func normalizeOptionalUser(user *User) error {
	if len(user.DisplayName) > 1024 || len(user.Nickname) > 1024 {
		return ErrInvalid
	}
	if user.UUID != "" {
		id, err := UUID(user.UUID)
		if err != nil {
			return err
		}
		user.UUID = id
	}
	return nil
}
func pullURL(workspaceID, repositoryID string, id int64) (string, error) {
	base, err := repoURL(workspaceID, repositoryID)
	if err != nil || id < 1 {
		return "", ErrInvalid
	}
	return base + "/pullrequests/" + strconv.FormatInt(id, 10), nil
}
func (c *Client) PullRequest(ctx context.Context, token, workspaceID, repositoryID string, id int64) (PullRequest, error) {
	var out PullRequest
	path, err := pullURL(workspaceID, repositoryID, id)
	if err != nil {
		return out, err
	}
	if err = c.request(ctx, token, "GET", path, nil, &out, 200); err != nil {
		return PullRequest{}, err
	}
	dest, err := UUID(out.Destination.Repository.UUID)
	expected, _ := UUID(repositoryID)
	if err != nil || dest != expected || out.ID != id || len(out.Title) == 0 || len(out.Title) > 4096 || len(out.Body()) > 256<<10 || (out.Description == nil && out.Summary.Raw == nil) || out.UpdatedAt.IsZero() || len(out.Source.Branch.Name) > 1024 || len(out.State) > 32 || out.State == "" || normalizeOptionalUser(&out.Author) != nil {
		return PullRequest{}, ErrInvalid
	}
	out.Destination.Repository.UUID = dest
	// Unknown states remain explicit data; callers cannot silently close a PR.
	return out, nil
}

type Comment struct {
	ID          int64     `json:"id"`
	Content     RawText   `json:"content"`
	User        User      `json:"user"`
	Deleted     bool      `json:"deleted"`
	CreatedAt   time.Time `json:"created_on"`
	UpdatedAt   time.Time `json:"updated_on"`
	PullRequest struct {
		ID int64 `json:"id"`
	} `json:"pullrequest"`
}

func normalizeComment(out *Comment, prID int64) error {
	if out.ID < 1 || out.PullRequest.ID != prID || out.CreatedAt.IsZero() || out.UpdatedAt.IsZero() || normalizeOptionalUser(&out.User) != nil || !out.Deleted && out.Content.Raw == nil || out.Content.Raw != nil && len(*out.Content.Raw) > 256<<10 {
		return ErrInvalid
	}
	return nil
}
func (c *Client) Comment(ctx context.Context, token, workspaceID, repositoryID string, prID, commentID int64) (Comment, error) {
	var out Comment
	base, err := pullURL(workspaceID, repositoryID, prID)
	if err != nil || commentID < 1 {
		return out, ErrInvalid
	}
	if err = c.request(ctx, token, "GET", base+"/comments/"+strconv.FormatInt(commentID, 10), nil, &out, 200); err != nil {
		return Comment{}, err
	}
	if out.ID != commentID || normalizeComment(&out, prID) != nil {
		return Comment{}, ErrInvalid
	}
	return out, nil
}
func (c *Client) Comments(ctx context.Context, token, workspaceID, repositoryID string, prID int64, next string) (Page[Comment], error) {
	base, err := pullURL(workspaceID, repositoryID, prID)
	if err != nil {
		return Page[Comment]{}, err
	}
	out, err := readPage[Comment](ctx, c, token, base+"/comments", next)
	if err != nil {
		return out, err
	}
	seen := map[int64]bool{}
	for i := range out.Values {
		if normalizeComment(&out.Values[i], prID) != nil || seen[out.Values[i].ID] {
			return Page[Comment]{}, ErrInvalid
		}
		seen[out.Values[i].ID] = true
	}
	return out, nil
}
func (c *Client) CreateComment(ctx context.Context, token, workspaceID, repositoryID string, prID int64, body string) (Comment, error) {
	var out Comment
	base, err := pullURL(workspaceID, repositoryID, prID)
	if err != nil || strings.TrimSpace(body) == "" || len(body) > 32<<10 {
		return out, ErrInvalid
	}
	input := struct {
		Content RawText `json:"content"`
	}{Content: RawText{Raw: &body}}
	if err = c.request(ctx, token, "POST", base+"/comments", input, &out, 201); err != nil {
		return Comment{}, err
	}
	if normalizeComment(&out, prID) != nil || out.Deleted || out.User.UUID == "" {
		return Comment{}, gitapi.ErrUnknownResult
	}
	return out, nil
}

type Commit struct {
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	Date    time.Time `json:"date"`
	Author  struct {
		Raw  string `json:"raw"`
		User *User  `json:"user"`
	} `json:"author"`
	Repository struct {
		UUID string `json:"uuid"`
	} `json:"repository"`
}

func normalizeCommit(out *Commit, repositoryID string) error {
	if !ValidSHA(out.Hash) || len(out.Message) > 256<<10 || out.Date.IsZero() || len(out.Author.Raw) > 4096 {
		return ErrInvalid
	}
	if out.Repository.UUID != "" {
		id, err := UUID(out.Repository.UUID)
		if err != nil || id != repositoryID {
			return ErrInvalid
		}
		out.Repository.UUID = id
	}
	if out.Author.User != nil && normalizeOptionalUser(out.Author.User) != nil {
		return ErrInvalid
	}
	return nil
}
func (c *Client) Commit(ctx context.Context, token, workspaceID, repositoryID, sha string) (Commit, error) {
	var out Commit
	base, err := repoURL(workspaceID, repositoryID)
	if err != nil || !ValidSHA(sha) {
		return out, ErrInvalid
	}
	if err = c.request(ctx, token, "GET", base+"/commit/"+sha, nil, &out, 200); err != nil {
		return Commit{}, err
	}
	id, _ := UUID(repositoryID)
	if normalizeCommit(&out, id) != nil || out.Hash != sha {
		return Commit{}, ErrInvalid
	}
	return out, nil
}

// CommitDifference returns no partial result: any failed page or budget excess
// rejects the whole difference. The event worker additionally shares the same
// 100-commit/page/time/byte budget across every branch in a push delivery.
func (c *Client) CommitDifference(ctx context.Context, token, workspaceID, repositoryID, oldSHA, newSHA string) ([]Commit, error) {
	base, err := repoURL(workspaceID, repositoryID)
	if err != nil || !ValidSHA(oldSHA) || !ValidSHA(newSHA) {
		return nil, ErrInvalid
	}
	ctx, cancel := WithTraversalBudget(ctx)
	defer cancel()
	collection := base + "/commits?" + url.Values{"include": {newSHA}, "exclude": {oldSHA}}.Encode()
	id, _ := UUID(repositoryID)
	seen := map[string]bool{}
	cursors := map[string]bool{}
	next := ""
	out := []Commit{}
	for page := 0; page < 20; page++ {
		rows, e := readPage[Commit](ctx, c, token, collection, next)
		if e != nil {
			return nil, e
		}
		for _, item := range rows.Values {
			if normalizeCommit(&item, id) != nil {
				return nil, ErrInvalid
			}
			if !seen[item.Hash] {
				seen[item.Hash] = true
				out = append(out, item)
				if len(out) > MaxCommits {
					return nil, ErrCommitLimit
				}
			}
		}
		if rows.Next == "" {
			return out, nil
		}
		if cursors[rows.Next] {
			return nil, ErrIncomplete
		}
		cursors[rows.Next] = true
		next = rows.Next
	}
	return nil, ErrIncomplete
}
