package gongfeng

import (
	"context"
	"errors"
	"strconv"
	"time"

	"openfox/internal/integrations/gitapi"
)

var ErrUnsupported = errors.New("gongfeng: operation requires dedicated repository workflow")

// ScopedClient is constructed from a server-owned repository binding. Display
// names can be checked but never select the numeric upstream routing scope.
type ScopedClient struct {
	client  *Client
	project Project
}

func (c *Client) Scope(projectID int64, displayPath string) (*ScopedClient, error) {
	if projectID < 1 || !validProjectPath(displayPath) {
		return nil, ErrInvalid
	}
	return &ScopedClient{client: c, project: Project{ID: projectID, Path: displayPath}}, nil
}
func (s *ScopedClient) matches(owner, repo string) bool {
	name := owner + "/" + repo
	return name == s.project.Path || name == "git.tencent.com/"+strconv.FormatInt(s.project.ID, 10)
}
func (*ScopedClient) SupportsAssignees() bool                    { return false }
func (*ScopedClient) AuthorizeURL(string, string, string) string { return "" }
func (*ScopedClient) Exchange(context.Context, string, string, string) (gitapi.Token, error) {
	return gitapi.Token{}, ErrUnsupported
}
func (*ScopedClient) Refresh(context.Context, string) (gitapi.Token, error) {
	return gitapi.Token{}, gitapi.ErrRefreshUnsupported
}
func (s *ScopedClient) User(ctx context.Context, token string) (gitapi.User, error) {
	return s.client.User(ctx, token)
}
func (*ScopedClient) Orgs(context.Context, string) ([]gitapi.Org, error) { return nil, ErrUnsupported }
func (*ScopedClient) ListRepos(context.Context, string, bool, string, string, int) ([]gitapi.Repo, bool, error) {
	return nil, false, ErrUnsupported
}
func (s *ScopedClient) GetRepo(ctx context.Context, token, owner, repo string) (gitapi.Repo, error) {
	if !s.matches(owner, repo) {
		return gitapi.Repo{}, ErrInvalid
	}
	p, err := s.client.Project(ctx, token, s.project.ID)
	if err != nil {
		return gitapi.Repo{}, err
	}
	private, known := p.Privacy()
	if !known || p.Archived == nil || *p.Archived || p.IssuesEnabled == nil || !*p.IssuesEnabled {
		return gitapi.Repo{}, ErrInvalid
	}
	return gitapi.Repo{FullName: p.Path, Name: p.Name, Private: private, HTMLURL: Origin + "/" + p.Path}, nil
}

// History and UNKNOWN reconciliation use the provider's persisted, bounded
// opened/closed cursors. The generic all-issue API cannot express that contract.
func (*ScopedClient) ListIssues(context.Context, string, string, string, int) ([]gitapi.Issue, bool, error) {
	return nil, false, ErrUnsupported
}
func (*ScopedClient) ListIssuesByCreator(context.Context, string, string, string, string, time.Time) ([]gitapi.Issue, error) {
	return nil, ErrUnsupported
}
func (s *ScopedClient) CreateIssue(ctx context.Context, token, owner, repo, title, body string, assignees []string) (gitapi.Issue, error) {
	if !s.matches(owner, repo) || len(assignees) > 0 {
		return gitapi.Issue{}, ErrInvalid
	}
	i, err := s.client.Create(ctx, token, s.project.ID, title, body)
	if err != nil {
		return gitapi.Issue{}, err
	}
	return i.PublicIssue(s.project)
}
func (s *ScopedClient) UpdateIssue(ctx context.Context, token, owner, repo, ref string, in gitapi.IssueUpdate) (gitapi.Issue, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id < 1 || !s.matches(owner, repo) || len(in.Assignees) > 0 {
		return gitapi.Issue{}, ErrInvalid
	}
	i, err := s.client.Update(ctx, token, s.project.ID, id, in)
	if err != nil {
		return gitapi.Issue{}, err
	}
	return i.PublicIssue(s.project)
}
func (s *ScopedClient) CommentIssue(ctx context.Context, token, owner, repo, ref, body string) (string, error) {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id < 1 || !s.matches(owner, repo) {
		return "", ErrInvalid
	}
	n, err := s.client.Comment(ctx, token, s.project.ID, id, false, body)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n.ID, 10), nil
}

// The generic PR interface carries a display iid; Gongfeng notes require the
// global MR id. Only the provider's durable work may post using that global id.
func (*ScopedClient) CommentPullRequest(context.Context, string, string, string, int, string) (string, error) {
	return "", ErrUnsupported
}

var _ gitapi.Client = (*ScopedClient)(nil)
