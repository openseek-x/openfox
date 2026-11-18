package asana

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

type Ref struct {
	GID string `json:"gid"`
}

type Named struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type User struct {
	GID        string  `json:"gid"`
	Name       string  `json:"name"`
	Email      string  `json:"email"`
	Workspaces []Named `json:"workspaces,omitempty"`
}

type Project struct {
	GID       string `json:"gid"`
	Name      string `json:"name"`
	Workspace Ref    `json:"workspace"`
	Archived  bool   `json:"archived"`
}

type Task struct {
	GID             string     `json:"gid"`
	Name            string     `json:"name"`
	Notes           string     `json:"notes"`
	Completed       bool       `json:"completed"`
	CompletedAt     *time.Time `json:"completed_at"`
	CreatedAt       string     `json:"created_at,omitempty"`
	ModifiedAt      string     `json:"modified_at,omitempty"`
	Assignee        *Named     `json:"assignee"`
	ResourceSubtype string     `json:"resource_subtype,omitempty"`
	ApprovalStatus  string     `json:"approval_status,omitempty"`
	Parent          *Ref       `json:"parent"`
	Workspace       Ref        `json:"workspace"`
	Projects        []Ref      `json:"projects"`
	NumSubtasks     int        `json:"num_subtasks"`
	PermalinkURL    string     `json:"permalink_url,omitempty"`
	DueOn           string     `json:"due_on,omitempty"`
	DueAt           string     `json:"due_at,omitempty"`
	StartOn         string     `json:"start_on,omitempty"`
}

type ProjectPage struct {
	Data       []Project `json:"data"`
	NextOffset string    `json:"next_offset,omitempty"`
}

type TaskPage struct {
	Data       []Task `json:"data"`
	NextOffset string `json:"next_offset,omitempty"`
}

const taskFields = "gid,name,notes,completed,completed_at,created_at,modified_at,assignee.gid,assignee.name,resource_subtype,approval_status,parent.gid,workspace.gid,projects.gid,num_subtasks,permalink_url,due_on,due_at,start_on"

func protocolError() error { return &Error{Code: CodeProtocol, HTTPStatus: http.StatusOK} }

func (c *Client) get(ctx context.Context, token, path string, query url.Values) ([]byte, error) {
	return c.request(ctx, http.MethodGet, APIURL+path+"?"+query.Encode(), token, nil)
}

func objectData(raw []byte) (json.RawMessage, error) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 || envelope.Data[0] != '{' {
		return nil, protocolError()
	}
	return envelope.Data, nil
}

func (c *Client) UserMe(ctx context.Context, token string) (User, error) {
	body, err := c.get(ctx, token, "/users/me", url.Values{"opt_fields": {"gid,name,email,workspaces"}})
	if err != nil {
		return User{}, err
	}
	return parseUser(body, true)
}

func (c *Client) WorkspaceUser(ctx context.Context, token, workspace, subject string) (User, error) {
	if !ValidID(workspace) || !ValidID(subject) {
		return User{}, &Error{Code: CodeInvalidRequest}
	}
	body, err := c.get(ctx, token, "/workspaces/"+url.PathEscape(workspace)+"/users/"+url.PathEscape(subject), url.Values{"opt_fields": {"gid,name,email"}})
	if err != nil {
		return User{}, err
	}
	user, err := parseUser(body, false)
	if err == nil && user.GID != subject {
		return User{}, protocolError()
	}
	return user, err
}

func parseUser(body []byte, workspaces bool) (User, error) {
	data, err := objectData(body)
	if err != nil {
		return User{}, err
	}
	var wire struct {
		GID        string          `json:"gid"`
		Name       *string         `json:"name"`
		Email      string          `json:"email"`
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(data, &wire) != nil || !ValidID(wire.GID) || wire.Name == nil || len(*wire.Name) > MaxCellBytes || len(wire.Email) > 320 {
		return User{}, protocolError()
	}
	user := User{GID: wire.GID, Name: *wire.Name, Email: wire.Email}
	if workspaces {
		if len(wire.Workspaces) == 0 || wire.Workspaces[0] != '[' || json.Unmarshal(wire.Workspaces, &user.Workspaces) != nil || len(user.Workspaces) > 5000 {
			return User{}, protocolError()
		}
		for _, workspace := range user.Workspaces {
			if !ValidID(workspace.GID) || len(workspace.Name) > MaxCellBytes {
				return User{}, protocolError()
			}
		}
	}
	return user, nil
}

func (c *Client) Project(ctx context.Context, token, project string) (Project, error) {
	if !ValidID(project) {
		return Project{}, &Error{Code: CodeInvalidRequest}
	}
	body, err := c.get(ctx, token, "/projects/"+url.PathEscape(project), url.Values{"opt_fields": {"gid,name,workspace,archived"}})
	if err != nil {
		return Project{}, err
	}
	data, err := objectData(body)
	if err != nil {
		return Project{}, err
	}
	p, err := parseProject(data)
	if err == nil && p.GID != project {
		return Project{}, protocolError()
	}
	return p, err
}

func parseProject(data json.RawMessage) (Project, error) {
	var wire struct {
		GID       string  `json:"gid"`
		Name      *string `json:"name"`
		Workspace Ref     `json:"workspace"`
		Archived  *bool   `json:"archived"`
	}
	if json.Unmarshal(data, &wire) != nil || !ValidID(wire.GID) || wire.Name == nil || len(*wire.Name) > MaxCellBytes ||
		!ValidID(wire.Workspace.GID) || wire.Archived == nil {
		return Project{}, protocolError()
	}
	return Project{GID: wire.GID, Name: *wire.Name, Workspace: wire.Workspace, Archived: *wire.Archived}, nil
}

func pageQuery(offset, fields string) (url.Values, error) {
	if offset != "" && !validOpaque(offset, maxOffsetBytes) {
		return nil, &Error{Code: CodeInvalidRequest}
	}
	query := url.Values{"limit": {"100"}, "opt_fields": {fields}}
	if offset != "" {
		query.Set("offset", offset)
	}
	return query, nil
}

func parsePage(body []byte, offset string) ([]json.RawMessage, string, error) {
	var wire struct {
		Data     json.RawMessage `json:"data"`
		NextPage json.RawMessage `json:"next_page"`
	}
	if json.Unmarshal(body, &wire) != nil || len(wire.Data) == 0 || wire.Data[0] != '[' || len(wire.NextPage) == 0 {
		return nil, "", protocolError()
	}
	var items []json.RawMessage
	if json.Unmarshal(wire.Data, &items) != nil || len(items) > pageSize {
		return nil, "", protocolError()
	}
	nextOffset := ""
	if !bytes.Equal(wire.NextPage, []byte("null")) {
		var next struct {
			Offset string `json:"offset"`
		}
		if json.Unmarshal(wire.NextPage, &next) != nil || !validOpaque(next.Offset, maxOffsetBytes) || next.Offset == offset {
			return nil, "", protocolError()
		}
		nextOffset = next.Offset
	}
	return items, nextOffset, nil
}

func (c *Client) ProjectsPage(ctx context.Context, token, workspace, offset string) (ProjectPage, error) {
	if !ValidID(workspace) {
		return ProjectPage{}, &Error{Code: CodeInvalidRequest}
	}
	query, err := pageQuery(offset, "gid,name,workspace,archived")
	if err != nil {
		return ProjectPage{}, err
	}
	query.Set("archived", "false")
	body, err := c.get(ctx, token, "/workspaces/"+url.PathEscape(workspace)+"/projects", query)
	if err != nil {
		return ProjectPage{}, err
	}
	items, next, err := parsePage(body, offset)
	if err != nil {
		return ProjectPage{}, err
	}
	page := ProjectPage{Data: make([]Project, 0, len(items)), NextOffset: next}
	for _, item := range items {
		p, err := parseProject(item)
		if err != nil || p.Workspace.GID != workspace || p.Archived {
			return ProjectPage{}, protocolError()
		}
		page.Data = append(page.Data, p)
	}
	return page, nil
}

func (c *Client) TasksPage(ctx context.Context, token, project, offset string) (TaskPage, error) {
	if !ValidID(project) {
		return TaskPage{}, &Error{Code: CodeInvalidRequest}
	}
	return c.taskPage(ctx, token, "/projects/"+url.PathEscape(project)+"/tasks", offset, "")
}

func (c *Client) SubtasksPage(ctx context.Context, token, parent, offset string) (TaskPage, error) {
	if !ValidID(parent) {
		return TaskPage{}, &Error{Code: CodeInvalidRequest}
	}
	return c.taskPage(ctx, token, "/tasks/"+url.PathEscape(parent)+"/subtasks", offset, parent)
}

func (c *Client) taskPage(ctx context.Context, token, path, offset, parent string) (TaskPage, error) {
	query, err := pageQuery(offset, taskFields)
	if err != nil {
		return TaskPage{}, err
	}
	body, err := c.get(ctx, token, path, query)
	if err != nil {
		return TaskPage{}, err
	}
	items, next, err := parsePage(body, offset)
	if err != nil {
		return TaskPage{}, err
	}
	page := TaskPage{Data: make([]Task, 0, len(items)), NextOffset: next}
	for _, item := range items {
		task, err := parseTask(item)
		if err != nil {
			return TaskPage{}, err
		}
		if parent != "" && (task.Parent == nil || task.Parent.GID != parent) {
			return TaskPage{}, protocolError()
		}
		page.Data = append(page.Data, task)
	}
	return page, nil
}

func parseTask(data json.RawMessage) (Task, error) {
	var task Task
	var presence struct {
		Name        *string         `json:"name"`
		Notes       *string         `json:"notes"`
		Completed   *bool           `json:"completed"`
		NumSubtasks *int            `json:"num_subtasks"`
		Parent      json.RawMessage `json:"parent"`
		Assignee    json.RawMessage `json:"assignee"`
		Projects    json.RawMessage `json:"projects"`
	}
	if json.Unmarshal(data, &task) != nil || json.Unmarshal(data, &presence) != nil || !ValidID(task.GID) || !ValidID(task.Workspace.GID) ||
		presence.Name == nil || presence.Notes == nil || presence.Completed == nil || presence.NumSubtasks == nil || task.NumSubtasks < 0 ||
		len(presence.Parent) == 0 || len(presence.Assignee) == 0 || len(presence.Projects) == 0 || presence.Projects[0] != '[' {
		return Task{}, protocolError()
	}
	if len(task.Name) > MaxCellBytes || len(task.Notes) > MaxCellBytes || len(task.ResourceSubtype) > MaxCellBytes || len(task.ApprovalStatus) > MaxCellBytes {
		return Task{}, &Error{Code: CodeLimit, HTTPStatus: http.StatusOK}
	}
	if task.Parent != nil && (!ValidID(task.Parent.GID) || task.Parent.GID == task.GID) ||
		task.Assignee != nil && (!ValidID(task.Assignee.GID) || len(task.Assignee.Name) > MaxCellBytes) || len(task.Projects) > 5000 {
		return Task{}, protocolError()
	}
	for _, project := range task.Projects {
		if !ValidID(project.GID) {
			return Task{}, protocolError()
		}
	}
	if !task.Completed && task.CompletedAt != nil {
		return Task{}, protocolError()
	}
	for _, timestamp := range []string{task.CreatedAt, task.ModifiedAt, task.DueAt} {
		if timestamp != "" {
			if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
				return Task{}, protocolError()
			}
		}
	}
	for _, date := range []string{task.DueOn, task.StartOn} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return Task{}, protocolError()
			}
		}
	}
	if task.PermalinkURL != "" {
		u, err := url.Parse(task.PermalinkURL)
		if err != nil || len(task.PermalinkURL) > 4096 || u.Scheme != "https" || u.Host != "app.asana.com" || u.User != nil {
			return Task{}, protocolError()
		}
	}
	return task, nil
}
