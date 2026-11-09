package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

type ExternalProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type ProjectSource struct {
	Provider, ExternalID string
	Version              int64
	auth                 ProviderTokenInput
	transport            http.RoundTripper
}

func (s Service) ProjectSource(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, provider, id string) (ProjectSource, error) {
	var out ProjectSource
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return out, err
	}
	if !CanManage(actor.Role) {
		return out, ErrForbidden
	}
	if !membership.ValidateUUID(id) || (provider != "tapd" && provider != "pingcode" && provider != "shortcut" && provider != "teambition") {
		return out, ErrInvalidRequest
	}
	var sealed []byte
	err := tx.QueryRow(ctx, `SELECT provider,external_id,credentials_sealed,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider=$2 AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, id, provider).Scan(&out.Provider, &out.ExternalID, &sealed, &out.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	plain, err := s.open(sealed, connectionAAD(org, id))
	if err != nil {
		return out, err
	}
	if json.Unmarshal(plain, &out.auth) != nil || !out.auth.validate(provider) {
		return out, ErrNeedsReauth
	}
	return out, nil
}

var providerRoots = map[string]string{"teambition": "https://open.teambition.com/api", "shortcut": "https://api.app.shortcut.com/api/v3", "tapd": "https://api.tapd.cn", "pingcode": "https://open.pingcode.com/v1", "cnb": "https://api.cnb.cool"}

func (s ProjectSource) get(ctx context.Context, path string, q url.Values, dst any) error {
	root := providerRoots[s.Provider]
	if root == "" {
		return ErrInvalidRequest
	}
	endpoint := root + path
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ErrInvalidRequest
	}
	s.authorize(req)
	req.Header.Set("Accept", "application/json")
	client := http.Client{Transport: s.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrNeedsReauth
	}
	if resp.StatusCode != 200 {
		return ErrUpstreamUnavailable
	}
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return ErrUpstreamUnavailable
	}
	if s.Provider == "tapd" {
		var wrapped struct {
			Status int
			Data   json.RawMessage
		}
		if json.Unmarshal(body, &wrapped) != nil {
			return ErrUpstreamUnavailable
		}
		if wrapped.Status != 1 {
			return ErrNeedsReauth
		}
		body = wrapped.Data
	}
	if json.Unmarshal(body, dst) != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}

func (s ProjectSource) authorize(req *http.Request) {
	if s.Provider == "teambition" {
		req.Header.Set("Authorization", "Bearer "+teambitionToken(s.auth, time.Now()))
		req.Header.Set("X-Tenant-Id", s.auth.CompanyID)
		req.Header.Set("X-Tenant-Type", "organization")
	} else if s.Provider == "shortcut" {
		req.Header.Set("Shortcut-Token", s.auth.Secret)
	} else if s.auth.Method == "basic" {
		req.SetBasicAuth(s.auth.Account, s.auth.Secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+s.auth.Secret)
	}
}

func (s ProjectSource) pingCompany(ctx context.Context) (providerIdentity, error) {
	var team providerIdentity
	if err := s.get(ctx, "/directory/team", nil, &team); err != nil {
		return team, err
	}
	if team.ID == "" || team.ID != s.auth.CompanyID {
		return team, ErrNeedsReauth
	}
	return team, nil
}

func (s ProjectSource) Projects(ctx context.Context) ([]ExternalProject, error) {
	projects := []ExternalProject{}
	switch s.Provider {
	case "teambition":
		err := s.teambitionPages(ctx, "/v3/project/query", func(p teambitionItem) error {
			if p.OrganizationID != s.auth.CompanyID {
				return ErrUpstreamUnavailable
			}
			if !p.IsArchived {
				projects = append(projects, ExternalProject{p.ID, p.Name})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	case "shortcut":
		var items []struct {
			ID       int64
			Name     string
			Archived bool
		}
		if err := s.get(ctx, "/projects", nil, &items); err != nil {
			return nil, err
		}
		for _, p := range items {
			if !p.Archived {
				projects = append(projects, ExternalProject{strconv.FormatInt(p.ID, 10), p.Name})
			}
		}
	case "pingcode":
		if _, err := s.pingCompany(ctx); err != nil {
			return nil, err
		}
		err := s.pingPages(ctx, "/project/projects", nil, func(raw json.RawMessage) error {
			var p ExternalProject
			if err := json.Unmarshal(raw, &p); err != nil || !companyPattern.MatchString(p.ID) {
				return ErrUpstreamUnavailable
			}
			projects = append(projects, p)
			return nil
		})
		if err != nil {
			return nil, err
		}
	case "tapd":
		path := "/workspaces/projects"
		q := url.Values{"company_id": {s.auth.CompanyID}, "category": {"project"}}
		if s.auth.Method != "basic" {
			var user providerIdentity
			if err := s.get(ctx, "/users/info", nil, &user); err != nil {
				return nil, err
			}
			if user.Nick == "" {
				return nil, ErrNeedsReauth
			}
			path = "/workspaces/user_participant_projects"
			q = url.Values{"nick": {user.Nick}}
		}
		var items []struct{ Workspace ExternalProject }
		if err := s.get(ctx, path, q, &items); err != nil {
			return nil, err
		}
		for _, p := range items {
			projects = append(projects, p.Workspace)
		}
	default:
		return nil, ErrInvalidRequest
	}
	if len(projects) > 5000 {
		return nil, ErrInvalidRequest
	}
	return projects, nil
}
func (s ProjectSource) pingPages(ctx context.Context, path string, query url.Values, visit func(json.RawMessage) error) error {
	if query == nil {
		query = url.Values{}
	}
	for index, seen := 0, 0; index <= 50; index++ {
		query.Set("page_size", "100")
		query.Set("page_index", strconv.Itoa(index))
		var page struct {
			Values []json.RawMessage
			Total  int `json:"total"`
		}
		if err := s.get(ctx, path, query, &page); err != nil {
			return err
		}
		if page.Values == nil || len(page.Values) > 100 || page.Total > 5000 {
			return ErrUpstreamUnavailable
		}
		seen += len(page.Values)
		if seen > 5000 {
			return ErrInvalidRequest
		}
		for _, r := range page.Values {
			if err := visit(r); err != nil {
				return err
			}
		}
		if len(page.Values) == 0 || seen >= page.Total && page.Total > 0 || len(page.Values) < 100 {
			return nil
		}
	}
	return ErrInvalidRequest
}

// The first seven columns are shared with existing import drafts. Extra columns
// carry stable user identities and upstream workflow state into the same worker.
type projectWorkItem struct {
	id, title, description, status, assignee, kind, parent string
	assigneeID, category, priority, key                    string
}

func (s ProjectSource) ProjectRows(ctx context.Context, project string) ([][]string, error) {
	if !companyPattern.MatchString(project) {
		return nil, ErrInvalidRequest
	}
	rows := [][]string{{"ID", "Summary", "Description", "Status", "Assignee", "Issue type", "Parent id", "Assignee Id", "Status category", "Priority", "Issue key"}}
	seen := map[string]bool{}
	bytes := 0
	add := func(item projectWorkItem) error {
		if len(rows) > 5000 || item.id == "" || seen[item.id] {
			return ErrInvalidRequest
		}
		seen[item.id] = true
		row := []string{item.id, item.title, item.description, item.status, item.assignee, item.kind, item.parent, item.assigneeID, item.category, item.priority, item.key}
		for _, v := range row {
			bytes += len(v)
			if len(v) > 65536 || bytes > 10<<20 {
				return ErrInvalidRequest
			}
		}
		rows = append(rows, row)
		return nil
	}
	switch s.Provider {
	case "teambition":
		base := "/v3/project/" + project
		states := map[string]string{}
		if err := s.teambitionPages(ctx, base+"/taskflowstatus/search", func(v teambitionItem) error { states[v.ID] = v.Name; return nil }); err != nil {
			return nil, err
		}
		if err := s.teambitionPages(ctx, base+"/task/query", func(v teambitionItem) error {
			if v.ProjectID != project {
				return ErrUpstreamUnavailable
			}
			state := states[v.TfsID]
			if state == "" {
				state = "Open"
				if v.IsDone {
					state = "Done"
				}
			}
			category := "new"
			if v.IsDone {
				category = "done"
			}
			return add(projectWorkItem{id: v.ID, title: v.Content, description: v.Note, status: state, assignee: v.ExecutorID, kind: "Task", parent: v.ParentTaskID, category: category})
		}); err != nil {
			return nil, err
		}
	case "shortcut":
		if !tapdCompanyPattern.MatchString(project) {
			return nil, ErrInvalidRequest
		}
		type shortcutState struct {
			ID         int64
			Name, Type string
		}
		var flows []struct {
			States []shortcutState
		}
		if err := s.get(ctx, "/workflows", nil, &flows); err != nil {
			return nil, err
		}
		states := map[int64]shortcutState{}
		for _, f := range flows {
			for _, v := range f.States {
				states[v.ID] = v
			}
		}
		var stories []struct {
			ID                int64
			Name, Description string
			Project           int64    `json:"project_id"`
			State             int64    `json:"workflow_state_id"`
			Owners            []string `json:"owner_ids"`
			Kind              string   `json:"story_type"`
			Parent            int64    `json:"parent_story_id"`
		}
		if err := s.get(ctx, "/projects/"+project+"/stories", nil, &stories); err != nil {
			return nil, err
		}
		for _, v := range stories {
			if strconv.FormatInt(v.Project, 10) != project || v.ID <= 0 {
				return nil, ErrUpstreamUnavailable
			}
			parent, owner := "", ""
			if v.Parent != 0 {
				parent = fmt.Sprintf("%s/%d", s.ExternalID, v.Parent)
			}
			if len(v.Owners) > 0 {
				owner = v.Owners[0]
			}
			state := states[v.State]
			category := ""
			switch state.Type {
			case "done":
				category = "done"
			case "started", "unstarted":
				category = "new"
			}
			if err := add(projectWorkItem{id: fmt.Sprintf("%s/%d", s.ExternalID, v.ID), title: v.Name, description: v.Description, status: state.Name, assignee: owner, kind: v.Kind, parent: parent, category: category}); err != nil {
				return nil, err
			}
		}
	case "pingcode":
		err := s.pingPages(ctx, "/project/work_items", url.Values{"project_id": {project}}, func(raw json.RawMessage) error {
			var w struct {
				ID, Title, Description, Type, Identifier string
				Parent                                   string `json:"parent_id"`
				Project, Priority                        ExternalProject
				Assignee                                 struct {
					ID, Name    string
					DisplayName string `json:"display_name"`
				}
				State struct{ Name, Type string }
			}
			if json.Unmarshal(raw, &w) != nil || w.Project.ID != project {
				return ErrUpstreamUnavailable
			}
			name := w.Assignee.DisplayName
			if name == "" {
				name = w.Assignee.Name
			}
			category := ""
			switch w.State.Type {
			case "completed", "closed":
				category = "done"
			case "pending", "in_progress":
				category = "new"
			}
			return add(projectWorkItem{id: w.ID, title: w.Title, description: w.Description, status: w.State.Name, assignee: name, assigneeID: w.Assignee.ID, kind: w.Type, parent: w.Parent, priority: w.Priority.Name, key: w.Identifier, category: category})
		})
		if err != nil {
			return nil, err
		}
	case "tapd":
		if !tapdCompanyPattern.MatchString(project) {
			return nil, ErrInvalidRequest
		}
		for _, kind := range []string{"Story", "Bug", "Task"} {
			path := map[string]string{"Story": "/stories", "Bug": "/bugs", "Task": "/tasks"}[kind]
			for page := 1; page <= 51; page++ {
				var items []map[string]struct {
					ID, Name, Title, Description, Status, Owner, CurrentOwner string
					Assignee                                                  string `json:"current_owner"`
					Parent                                                    string `json:"parent_id"`
					Workspace                                                 string `json:"workspace_id"`
				}
				if err := s.get(ctx, path, url.Values{"workspace_id": {project}, "limit": {"100"}, "page": {strconv.Itoa(page)}}, &items); err != nil {
					return nil, err
				}
				for _, item := range items {
					v, ok := item[kind]
					if !ok || v.Workspace != project {
						return nil, ErrUpstreamUnavailable
					}
					title := v.Name
					if title == "" {
						title = v.Title
					}
					owner := v.Owner
					if owner == "" {
						owner = v.Assignee
					}
					parent := v.Parent
					if parent == "0" {
						parent = ""
					}
					if err := add(projectWorkItem{id: v.ID, title: title, description: v.Description, status: v.Status, assignee: strings.Trim(owner, ";"), kind: kind, parent: parent}); err != nil {
						return nil, err
					}
				}
				if len(items) < 100 {
					break
				}
			}
		}
	default:
		return nil, ErrInvalidRequest
	}
	return rows, nil
}

// UpdateTAPDText updates only fields changed by the local member. The endpoint
// requires an existing ID and workspace; an empty ID would create an object.
func (s ProjectSource) UpdateTAPDText(ctx context.Context, project, id, kind string, title, description *string) error {
	return s.updateTAPDFields(ctx, project, id, kind, title, description, nil)
}
func (s ProjectSource) updateTAPDFields(ctx context.Context, project, id, kind string, title, description, status *string) error {
	paths := map[string]string{"Story": "/stories", "Task": "/tasks", "Bug": "/bugs"}
	path := paths[kind]
	if s.Provider != "tapd" || !tapdCompanyPattern.MatchString(project) || !tapdCompanyPattern.MatchString(id) || path == "" {
		return ErrInvalidRequest
	}
	form := url.Values{"workspace_id": {project}, "id": {id}}
	if status != nil {
		form.Set("status", *status)
	}
	if title != nil {
		key := "name"
		if kind == "Bug" {
			key = "title"
		}
		form.Set(key, *title)
	}
	if description != nil {
		form.Set("description", *description)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, providerRoots["tapd"]+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if s.auth.Method == "basic" {
		req.SetBasicAuth(s.auth.Account, s.auth.Secret)
	} else {
		req.Header.Set("Authorization", "Bearer "+s.auth.Secret)
	}
	client := http.Client{Transport: s.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	var out struct {
		Status int
		Data   map[string]struct {
			ID        string
			Status    string
			Workspace string `json:"workspace_id"`
		}
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil || out.Status != 1 || out.Data[kind].ID != id || out.Data[kind].Workspace != project || (status != nil && out.Data[kind].Status != *status) {
		return ErrUpstreamUnavailable
	}
	return nil
}

// UpdateProjectText writes only fields that changed locally. ProjectRows checks
// object membership before reconciliation, and we validate the write response.
func (s ProjectSource) UpdateProjectText(ctx context.Context, project, id, kind string, title, description *string) error {
	if s.Provider == "tapd" {
		return s.UpdateTAPDText(ctx, project, id, kind, title, description)
	}
	fields := map[string]string{}
	if description != nil {
		fields["description"] = *description
	}
	switch s.Provider {
	case "shortcut":
		workspace, story, ok := strings.Cut(id, "/")
		if !ok || workspace != s.ExternalID || !tapdCompanyPattern.MatchString(story) || !tapdCompanyPattern.MatchString(project) {
			return ErrInvalidRequest
		}
		if title != nil {
			fields["name"] = *title
		}
		var result struct {
			ID      json.Number
			Project json.Number `json:"project_id"`
		}
		if err := s.writeProjectJSON(ctx, http.MethodPut, "/stories/"+story, fields, &result); err != nil {
			return err
		}
		if result.ID.String() != story || result.Project.String() != project {
			return ErrUpstreamUnavailable
		}
	case "pingcode":
		if !companyPattern.MatchString(id) || !companyPattern.MatchString(project) {
			return ErrInvalidRequest
		}
		if title != nil {
			fields["title"] = *title
		}
		var result struct {
			ID      string
			Project ExternalProject
		}
		if err := s.writeProjectJSON(ctx, http.MethodPatch, "/project/work_items/"+id, fields, &result); err != nil {
			return err
		}
		if result.ID != id || result.Project.ID != project {
			return ErrUpstreamUnavailable
		}
	case "teambition":
		if !companyPattern.MatchString(id) || !companyPattern.MatchString(project) {
			return ErrInvalidRequest
		}
		for _, change := range []struct {
			name  string
			value *string
		}{{"content", title}, {"note", description}} {
			if change.value == nil {
				continue
			}
			data := map[string]string{change.name: *change.value}
			if change.name == "note" {
				data["renderMode"] = "markdown"
			}
			var result struct {
				Result       map[string]string
				ErrorMessage string
			}
			if err := s.writeProjectJSON(ctx, http.MethodPut, "/v3/task/"+id+"/"+change.name, data, &result); err != nil {
				return err
			}
			value, exists := result.Result[change.name]
			if !exists || value != *change.value || result.ErrorMessage != "" {
				return ErrUpstreamUnavailable
			}
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func (s ProjectSource) writeProjectJSON(ctx context.Context, method, path string, fields any, result any) error {
	body, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, providerRoots[s.Provider]+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	s.authorize(req)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Transport: s.transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return ErrUpstreamUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrNeedsReauth
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result) != nil {
		return ErrUpstreamUnavailable
	}
	return nil
}
