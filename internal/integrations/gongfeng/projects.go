package gongfeng

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"openfox/internal/integrations/gitapi"
)

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	State     string `json:"state"`
	AvatarURL string `json:"avatar_url"`
}

func (c *Client) Identity(ctx context.Context, token string) (User, error) {
	var user User
	_, data, err := c.do(ctx, http.MethodGet, "/user", token, nil, nil)
	if err != nil {
		return user, err
	}
	if err = decode(data, &user, false); err != nil {
		return User{}, err
	}
	if user.ID <= 0 || user.Username == "" || user.State != "active" {
		return User{}, gitapi.ErrTokenRevoked
	}
	return user, nil
}
func (c *Client) User(ctx context.Context, token string) (gitapi.User, error) {
	user, err := c.Identity(ctx, token)
	return gitapi.User{ID: user.ID, Login: user.Username, AvatarURL: user.AvatarURL}, err
}

type Project struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path_with_namespace"`
	Type          string `json:"type"`
	WebURL        string `json:"web_url"`
	Archived      *bool  `json:"archived"`
	IssuesEnabled *bool  `json:"issues_enabled"`
	Visibility    *int   `json:"visibility_level"`
	Public        *bool  `json:"public"`
}

// Privacy is known only if the explicit fields agree. Level 10 is deliberately
// not treated as private even when the upstream public boolean is false.
func (p Project) Privacy() (private, known bool) {
	if p.Visibility == nil || p.Public == nil {
		return false, false
	}
	switch *p.Visibility {
	case 0:
		return !*p.Public, !*p.Public
	case 10:
		return false, !*p.Public
	case 20:
		return false, *p.Public
	}
	return false, false
}
func (p Project) Selectable() bool {
	return p.ID > 0 && p.Type == "GIT" && p.Path != "" && p.Archived != nil && !*p.Archived && p.IssuesEnabled != nil && *p.IssuesEnabled
}

func (c *Client) Projects(ctx context.Context, token, search string, page int) (Page[Project], error) {
	return readPage[Project](c, ctx, "/projects", token, url.Values{"type": {"GIT"}, "archived": {"false"}, "search": {search}, "order_by": {"id"}, "sort": {"asc"}}, page, MaxPages)
}
func (c *Client) Project(ctx context.Context, token string, id int64) (Project, error) {
	var project Project
	path, err := projectPath(id)
	if err != nil {
		return project, err
	}
	_, data, err := c.do(ctx, http.MethodGet, path, token, nil, nil)
	if err != nil {
		return project, err
	}
	if err = decode(data, &project, false); err != nil {
		return Project{}, err
	}
	if project.ID != id || !validProjectPath(project.Path) {
		return Project{}, ErrInvalid
	}
	return project, nil
}

type Member struct {
	User
	AccessLevel int     `json:"access_level"`
	ExpiresAt   *string `json:"expires_at"`
}

func activeMembership(m Member, now time.Time) bool {
	if m.State != "active" || m.AccessLevel != 40 && m.AccessLevel != 50 {
		return false
	}
	if m.ExpiresAt == nil {
		return true
	}
	for _, format := range []string{time.RFC3339, "2006-01-02T15:04:05-0700", "2006-01-02-0700"} {
		if expiry, err := time.Parse(format, *m.ExpiresAt); err == nil {
			return expiry.After(now)
		}
	}
	return false
}

// Hook access >= Master is OpenFox's conservative selection policy, not a
// claim that the provider documents this as the API's universal minimum role.
func (c *Client) CanManageHooks(ctx context.Context, token string, projectID int64, user User, now time.Time) (bool, error) {
	path, err := projectPath(projectID)
	if err != nil || user.ID <= 0 || user.State != "active" {
		return false, ErrInvalid
	}
	found := false
	seen := map[int64]bool{}
	for page := 1; page != 0; {
		members, err := readPage[Member](c, ctx, path+"/members/all", token, url.Values{"query": {user.Username}}, page, MaxPages)
		if err != nil {
			return false, err
		}
		for _, member := range members.Items {
			if member.ID <= 0 || seen[member.ID] {
				return false, ErrIncomplete
			}
			seen[member.ID] = true
			if member.ID == user.ID {
				found = activeMembership(member, now)
			}
		}
		page = members.Next
	}
	return found, nil
}

// The official projects API also exposes the effective (including inherited)
// member by numeric ID. Workers use this bounded single request after Identity;
// they never cache a paginated username search as an authorization decision.
func (c *Client) CanManageProject(ctx context.Context, token string, projectID int64, user User, now time.Time) (bool, error) {
	path, e := projectPath(projectID)
	if e != nil || user.ID < 1 || user.State != "active" {
		return false, ErrInvalid
	}
	ref, e := numericID(user.ID)
	if e != nil {
		return false, e
	}
	_, data, e := c.do(ctx, http.MethodGet, path+"/members/all/"+ref, token, nil, nil)
	if e != nil {
		return false, e
	}
	var member Member
	if e = decode(data, &member, false); e != nil {
		return false, e
	}
	if member.ID != user.ID {
		return false, ErrInvalid
	}
	return activeMembership(member, now), nil
}

func validProjectPath(path string) bool {
	if path == "" || len(path) > 1024 || strings.ContainsAny(path, "?%#\\\x00") {
		return false
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
