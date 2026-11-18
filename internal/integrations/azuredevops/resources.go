package azuredevops

import (
	"context"
	"net/url"
	"strings"
)

type Profile struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Email       string `json:"emailAddress"`
}
type Account struct {
	ID   string `json:"accountId"`
	Name string `json:"accountName"`
}
type Identity struct {
	ID          string `json:"id"`
	Descriptor  string `json:"descriptor"`
	DisplayName string `json:"providerDisplayName"`
	IsActive    bool   `json:"isActive"`
	IsContainer bool   `json:"isContainer"`
}
type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Visibility string `json:"visibility"`
	State      string `json:"state"`
}
type Repository struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Project  Project `json:"project"`
	Disabled bool    `json:"isDisabled"`
}
type ProjectPage struct {
	Projects []Project
	Next     string
}

// DisplayName is only presentation, never an identity key. Character truncation
// matches integration_connections' 200-character constraint, including Unicode.
func DisplayName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Azure DevOps"
	}
	r := []rune(name)
	if len(r) > 200 {
		name = string(r[:200])
	}
	return name
}
func (c *Client) Profile(ctx context.Context, token string) (Profile, error) {
	var out Profile
	_, e := c.request(ctx, "GET", apiURL("app.vssps.visualstudio.com", "/_apis/profile/profiles/me", nil), token, nil, nil, false, &out)
	if e != nil {
		return Profile{}, e
	}
	if !ValidUUID(out.ID) || len(out.DisplayName) > MaxCellBytes || len(out.Email) > MaxCellBytes {
		return Profile{}, protocol()
	}
	out.ID = uuid(out.ID)
	out.DisplayName = DisplayName(out.DisplayName, out.ID)
	return out, nil
}

// Accounts must be called only with a subject obtained by Profile using this
// same temporary user token; public handlers must not accept a caller memberID.
func (c *Client) Accounts(ctx context.Context, token, selfID string) ([]Account, error) {
	if !ValidUUID(selfID) {
		return nil, invalid()
	}
	var out struct {
		Value []Account `json:"value"`
	}
	_, e := c.request(ctx, "GET", apiURL("app.vssps.visualstudio.com", "/_apis/accounts", url.Values{"memberId": {uuid(selfID)}}), token, nil, nil, false, &out)
	if e != nil {
		return nil, e
	}
	if out.Value == nil || len(out.Value) > 1000 {
		return nil, protocol()
	}
	seen := map[string]bool{}
	for i := range out.Value {
		v := &out.Value[i]
		if !ValidUUID(v.ID) || !ValidOrganization(v.Name) || seen[uuid(v.ID)] {
			return nil, protocol()
		}
		v.ID = uuid(v.ID)
		seen[v.ID] = true
	}
	return out.Value, nil
}
func (c *Client) Identity(ctx context.Context, token, org, selfID string) (Identity, error) {
	if !ValidOrganization(org) || !ValidUUID(selfID) {
		return Identity{}, invalid()
	}
	var out struct {
		Value []Identity `json:"value"`
	}
	_, e := c.request(ctx, "GET", apiURL("vssps.dev.azure.com", orgPath(org)+"/_apis/identities", url.Values{"identityIds": {uuid(selfID)}}), token, nil, nil, false, &out)
	if e != nil {
		return Identity{}, e
	}
	// A self profile ID is not assumed universally interchangeable with an IMS
	// ID: only an exact, unique, active user returned by this organization proves it.
	if len(out.Value) != 1 {
		return Identity{}, protocol()
	}
	v := out.Value[0]
	if !ValidUUID(v.ID) || uuid(v.ID) != uuid(selfID) || !ValidText(v.Descriptor, 2048) || !v.IsActive || v.IsContainer || len(v.DisplayName) > MaxCellBytes {
		return Identity{}, protocol()
	}
	v.ID = uuid(v.ID)
	v.DisplayName = DisplayName(v.DisplayName, v.ID)
	return v, nil
}
func (c *Client) Projects(ctx context.Context, token, org, cursor string) (ProjectPage, error) {
	if !ValidOrganization(org) || (cursor != "" && !ValidText(cursor, 4096)) {
		return ProjectPage{}, invalid()
	}
	q := url.Values{"$top": {"100"}}
	if cursor != "" {
		q.Set("continuationToken", cursor)
	}
	var out struct {
		Value []Project `json:"value"`
	}
	h, e := c.request(ctx, "GET", apiURL("dev.azure.com", orgPath(org)+"/_apis/projects", q), token, nil, nil, false, &out)
	if e != nil {
		return ProjectPage{}, e
	}
	next := h.Get("x-ms-continuationtoken")
	if out.Value == nil || len(out.Value) > 100 || (next != "" && (!ValidText(next, 4096) || next == cursor)) {
		return ProjectPage{}, protocol()
	}
	seen := map[string]bool{}
	for i := range out.Value {
		p := &out.Value[i]
		if !ValidUUID(p.ID) || !ValidText(p.Name, MaxCellBytes) || seen[uuid(p.ID)] {
			return ProjectPage{}, protocol()
		}
		p.ID = uuid(p.ID)
		seen[p.ID] = true
	}
	return ProjectPage{Projects: out.Value, Next: next}, nil
}
func (c *Client) Project(ctx context.Context, token, org, id string) (Project, error) {
	if !ValidOrganization(org) || !ValidUUID(id) {
		return Project{}, invalid()
	}
	var out Project
	_, e := c.request(ctx, "GET", apiURL("dev.azure.com", orgPath(org)+"/_apis/projects/"+uuid(id), nil), token, nil, nil, false, &out)
	if e != nil {
		return Project{}, e
	}
	if !ValidUUID(out.ID) || uuid(out.ID) != uuid(id) || !ValidText(out.Name, MaxCellBytes) || (out.Visibility != "private" && out.Visibility != "public") {
		return Project{}, protocol()
	}
	out.ID = uuid(out.ID)
	return out, nil
}
func (c *Client) Repositories(ctx context.Context, token, org, project string) ([]Repository, error) {
	if !ValidOrganization(org) || !ValidUUID(project) {
		return nil, invalid()
	}
	var out struct {
		Value []Repository `json:"value"`
	}
	h, e := c.request(ctx, "GET", apiURL("dev.azure.com", projectPath(org, project)+"/_apis/git/repositories", nil), token, nil, nil, false, &out)
	if e != nil {
		return nil, e
	}
	// The official endpoint has no pagination contract. Never invent $skip or
	// silently accept a continuation header/partial repository selection.
	if out.Value == nil || len(out.Value) > MaxItems || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[string]bool{}
	for i := range out.Value {
		r := &out.Value[i]
		if !ValidUUID(r.ID) || !ValidText(r.Name, MaxCellBytes) || uuid(r.Project.ID) != uuid(project) || seen[uuid(r.ID)] {
			return nil, protocol()
		}
		r.ID = uuid(r.ID)
		r.Project.ID = uuid(r.Project.ID)
		seen[r.ID] = true
	}
	return out.Value, nil
}
func (c *Client) Repository(ctx context.Context, token, org, project, id string) (Repository, error) {
	if !ValidOrganization(org) || !ValidUUID(project) || !ValidUUID(id) {
		return Repository{}, invalid()
	}
	var out Repository
	_, e := c.request(ctx, "GET", apiURL("dev.azure.com", projectPath(org, project)+"/_apis/git/repositories/"+uuid(id), nil), token, nil, nil, false, &out)
	if e != nil {
		return Repository{}, e
	}
	if !ValidUUID(out.ID) || uuid(out.ID) != uuid(id) || uuid(out.Project.ID) != uuid(project) || !ValidText(out.Name, MaxCellBytes) || out.Disabled {
		return Repository{}, protocol()
	}
	out.ID = uuid(out.ID)
	out.Project.ID = uuid(out.Project.ID)
	return out, nil
}
