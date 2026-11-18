package zohobugtracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ID accepts only the two documented representations, preserving every digit.
type ID string

func (id *ID) UnmarshalJSON(raw []byte) error {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &s) != nil {
			return protocol()
		}
	} else {
		s = string(raw)
	}
	if !ValidID(s) {
		return protocol()
	}
	*id = ID(s)
	return nil
}

type Portal struct {
	ID   ID     `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}
type PortalPage struct {
	LoginID   ID       `json:"login_id"`
	Portals   []Portal `json:"portals"`
	NextIndex int      `json:"next_index"`
}
type User struct {
	ID     ID     `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Active bool   `json:"active"`
	Role   string `json:"role"`
}
type UserPage struct {
	Users     []User `json:"users"`
	NextIndex int    `json:"next_index"`
}
type Project struct {
	ID     ID     `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}
type ProjectPage struct {
	Projects  []Project `json:"projects"`
	NextIndex int       `json:"next_index"`
}
type NamedField struct {
	ID   ID     `json:"id"`
	Type string `json:"type"`
}
type Bug struct {
	ID            ID          `json:"id"`
	Title         string      `json:"title"`
	Status        NamedField  `json:"status"`
	Severity      *NamedField `json:"severity,omitempty"`
	ReporterID    ID          `json:"reporter_id,omitempty"`
	ReporterName  string      `json:"reported_person,omitempty"`
	Closed        bool        `json:"closed"`
	CreatedMillis *int64      `json:"created_time_long,omitempty"`
}
type BugPage struct {
	Bugs      []Bug `json:"bugs"`
	NextIndex int   `json:"next_index"`
}

func protocol() error { return &Error{Code: CodeProtocol} }
func validText(s string, required bool) bool {
	return utf8.ValidString(s) && len(s) <= MaxCellBytes && (!required || strings.TrimSpace(s) != "") && !strings.ContainsRune(s, 0)
}
func pagination(index int) (url.Values, error) {
	if index < 1 || index > 1000000 {
		return nil, &Error{Code: CodeInvalidRequest}
	}
	return url.Values{"index": {strconv.Itoa(index)}, "range": {strconv.Itoa(PageSize)}}, nil
}
func nextIndex(index, count int) (int, error) {
	if count > PageSize {
		return 0, &Error{Code: CodeLimit}
	}
	if count == PageSize {
		return index + PageSize, nil
	}
	return 0, nil
}
func (c *Client) get(ctx context.Context, token, path string, q url.Values, out any) error {
	raw, err := c.request(ctx, http.MethodGet, APIURL+path+"?"+q.Encode(), token, nil)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, out) != nil {
		return protocol()
	}
	return nil
}
func (c *Client) PortalsPage(ctx context.Context, token string, index int) (PortalPage, error) {
	var out PortalPage
	q, err := pagination(index)
	if err != nil {
		return out, err
	}
	if err = c.get(ctx, token, "/portals/", q, &out); err != nil {
		return out, err
	}
	if !ValidID(string(out.LoginID)) || out.Portals == nil {
		return out, protocol()
	}
	seen := map[ID]bool{}
	for _, p := range out.Portals {
		if !ValidID(string(p.ID)) || seen[p.ID] || !validText(p.Name, true) || !validText(p.Role, true) {
			return PortalPage{}, protocol()
		}
		seen[p.ID] = true
	}
	out.NextIndex, err = nextIndex(index, len(out.Portals))
	return out, err
}
func (c *Client) UsersPage(ctx context.Context, token, portal string, index int) (UserPage, error) {
	var out UserPage
	q, err := pagination(index)
	if err != nil || !ValidID(portal) {
		return out, &Error{Code: CodeInvalidRequest}
	}
	q.Set("user_type", "active")
	if err = c.get(ctx, token, "/portal/"+portal+"/users/", q, &out); err != nil {
		return out, err
	}
	if out.Users == nil {
		return out, protocol()
	}
	seen := map[ID]bool{}
	for _, u := range out.Users {
		if !ValidID(string(u.ID)) || seen[u.ID] || !validText(u.Name, true) || !validText(u.Email, true) || !validText(u.Role, true) {
			return UserPage{}, protocol()
		}
		seen[u.ID] = true
	}
	out.NextIndex, err = nextIndex(index, len(out.Users))
	return out, err
}
func (c *Client) ProjectsPage(ctx context.Context, token, portal string, index int) (ProjectPage, error) {
	var out ProjectPage
	q, err := pagination(index)
	if err != nil || !ValidID(portal) {
		return out, &Error{Code: CodeInvalidRequest}
	}
	q.Set("status", "active")
	if err = c.get(ctx, token, "/portal/"+portal+"/projects/", q, &out); err != nil {
		return out, err
	}
	if err = checkProjects(out.Projects); err != nil {
		return ProjectPage{}, err
	}
	out.NextIndex, err = nextIndex(index, len(out.Projects))
	return out, err
}
func (c *Client) Project(ctx context.Context, token, portal, project string) (Project, error) {
	if !ValidID(portal) || !ValidID(project) {
		return Project{}, &Error{Code: CodeInvalidRequest}
	}
	var out ProjectPage
	if err := c.get(ctx, token, "/portal/"+portal+"/projects/"+project+"/", nil, &out); err != nil {
		return Project{}, err
	}
	if err := checkProjects(out.Projects); err != nil {
		return Project{}, err
	}
	if len(out.Projects) != 1 || string(out.Projects[0].ID) != project {
		return Project{}, protocol()
	}
	return out.Projects[0], nil
}
func checkProjects(projects []Project) error {
	if projects == nil {
		return protocol()
	}
	seen := map[ID]bool{}
	for _, p := range projects {
		if !ValidID(string(p.ID)) || seen[p.ID] || !validText(p.Name, true) || (p.Status != "active" && p.Status != "archived" && p.Status != "template") {
			return protocol()
		}
		seen[p.ID] = true
	}
	return nil
}
func (c *Client) BugsPage(ctx context.Context, token, portal, project, partition string, index int) (BugPage, error) {
	var out BugPage
	q, err := pagination(index)
	if err != nil || !ValidID(portal) || !ValidID(project) || (partition != "open" && partition != "closed") {
		return out, &Error{Code: CodeInvalidRequest}
	}
	q.Set("statustype", partition)
	q.Set("sort_column", "created_time")
	q.Set("sort_order", "ascending")
	// A pointer detects omitted closed, avoiding silently treating malformed input as open.
	var wire struct {
		Bugs []struct {
			Bug
			Closed *bool `json:"closed"`
		} `json:"bugs"`
	}
	if err = c.get(ctx, token, "/portal/"+portal+"/projects/"+project+"/bugs/", q, &wire); err != nil {
		return out, err
	}
	if wire.Bugs == nil {
		return out, protocol()
	}
	out.Bugs = []Bug{}
	seen := map[ID]bool{}
	for _, w := range wire.Bugs {
		b := w.Bug
		if !ValidID(string(b.ID)) || seen[b.ID] || !validText(b.Title, true) || !ValidID(string(b.Status.ID)) || !validText(b.Status.Type, true) || w.Closed == nil || *w.Closed != (partition == "closed") || !validText(b.ReporterName, false) {
			return BugPage{}, protocol()
		}
		if b.Severity != nil && (!ValidID(string(b.Severity.ID)) || !validText(b.Severity.Type, true)) {
			return BugPage{}, protocol()
		}
		if b.CreatedMillis != nil && (*b.CreatedMillis < 0 || *b.CreatedMillis > 253402300799999) {
			return BugPage{}, protocol()
		}
		b.Closed = *w.Closed
		seen[b.ID] = true
		out.Bugs = append(out.Bugs, b)
	}
	out.NextIndex, err = nextIndex(index, len(out.Bugs))
	return out, err
}
