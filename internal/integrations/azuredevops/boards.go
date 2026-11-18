package azuredevops

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type WorkItemType struct {
	Name          string `json:"name"`
	ReferenceName string `json:"referenceName"`
	Disabled      bool   `json:"isDisabled"`
}
type WorkItemState struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}
type WorkItemField struct {
	Name          string `json:"name"`
	ReferenceName string `json:"referenceName"`
	Type          string `json:"type"`
	ReadOnly      bool   `json:"readOnly"`
	IsIdentity    bool   `json:"isIdentity"`
}
type WorkItemRelation struct {
	Rel        string                     `json:"rel"`
	URL        string                     `json:"url"`
	Attributes map[string]json.RawMessage `json:"attributes,omitempty"`
}

// Fields may contain HTML. Callers must convert according to field metadata;
// these bytes are not sanitized and must never be rendered directly.
type WorkItem struct {
	ID        int64                      `json:"id"`
	Rev       int64                      `json:"rev"`
	Fields    map[string]json.RawMessage `json:"fields"`
	Relations []WorkItemRelation         `json:"relations"`
}
type WorkItemQuery struct {
	AsOf           time.Time
	AfterID        int64
	ChangedFrom    *time.Time
	ChangedThrough *time.Time
}
type WorkItemPage struct {
	IDs  []int64
	AsOf time.Time
}
type wiqlResult struct {
	QueryType string    `json:"queryType"`
	AsOf      time.Time `json:"asOf"`
	WorkItems []struct {
		ID int64 `json:"id"`
	} `json:"workItems"`
}

func validProject(org, project string) bool { return ValidOrganization(org) && ValidUUID(project) }
func validField(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_') {
			return false
		}
	}
	return true
}
func (c *Client) WorkItemTypes(ctx context.Context, token, org, project string) ([]WorkItemType, error) {
	if !validProject(org, project) {
		return nil, invalid()
	}
	var out struct {
		Value []WorkItemType `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", projectPath(org, project)+"/_apis/wit/workitemtypes", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > MaxItems || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, v := range out.Value {
		if !ValidText(v.Name, MaxCellBytes) || !validField(v.ReferenceName) || seen[v.ReferenceName] || names[v.Name] {
			return nil, protocol()
		}
		seen[v.ReferenceName] = true
		names[v.Name] = true
	}
	return out.Value, nil
}
func (c *Client) WorkItemStates(ctx context.Context, token, org, project, typeName string) ([]WorkItemState, error) {
	// URL.Path escapes spaces once. Slashes are rejected rather than pre-escaped
	// and then accidentally escaped twice, or treated as another path segment.
	if !validProject(org, project) || !ValidText(typeName, 256) || strings.ContainsAny(typeName, "/\\") || typeName == "." || typeName == ".." {
		return nil, invalid()
	}
	var out struct {
		Value []WorkItemState `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", projectPath(org, project)+"/_apis/wit/workitemtypes/"+typeName+"/states", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > MaxItems || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[string]bool{}
	for _, v := range out.Value {
		if !ValidText(v.Name, MaxCellBytes) || !ValidText(v.Category, 256) || v.ID != "" && !ValidUUID(v.ID) || seen[v.Name] {
			return nil, protocol()
		}
		seen[v.Name] = true
	}
	return out.Value, nil
}
func (c *Client) WorkItemFields(ctx context.Context, token, org, project string) ([]WorkItemField, error) {
	if !validProject(org, project) {
		return nil, invalid()
	}
	var out struct {
		Value []WorkItemField `json:"value"`
	}
	h, err := c.request(ctx, "GET", apiURL("dev.azure.com", projectPath(org, project)+"/_apis/wit/fields", nil), token, nil, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) > MaxItems || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	seen := map[string]bool{}
	for _, v := range out.Value {
		if !ValidText(v.Name, MaxCellBytes) || !validField(v.ReferenceName) || !ValidText(v.Type, 256) || seen[v.ReferenceName] {
			return nil, protocol()
		}
		seen[v.ReferenceName] = true
	}
	return out.Value, nil
}
func (c *Client) queryWorkItems(ctx context.Context, token, org, project, query string, top int) (wiqlResult, error) {
	var out wiqlResult
	_, err := c.request(ctx, "POST", apiURL("dev.azure.com", projectPath(org, project)+"/_apis/wit/wiql", url.Values{"$top": {strconv.Itoa(top)}, "timePrecision": {"true"}}), token, map[string]string{"query": query}, nil, false, &out)
	if err != nil {
		return out, err
	}
	if out.QueryType != "flat" || out.AsOf.IsZero() || out.WorkItems == nil || len(out.WorkItems) > top {
		return wiqlResult{}, protocol()
	}
	return out, nil
}

// WorkItemCutover obtains the upstream clock without enumerating history.
func (c *Client) WorkItemCutover(ctx context.Context, token, org, project string) (time.Time, error) {
	if !validProject(org, project) {
		return time.Time{}, invalid()
	}
	out, err := c.queryWorkItems(ctx, token, org, project, "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project ORDER BY [System.Id] ASC", 1)
	if err != nil {
		return time.Time{}, err
	}
	for _, v := range out.WorkItems {
		if !ValidObjectID(v.ID) {
			return time.Time{}, protocol()
		}
	}
	return out.AsOf.UTC(), nil
}

// QueryWorkItems accepts only typed server cursors and time bounds, never WIQL.
// ChangedFrom is inclusive so equal-timestamp changes can be deduplicated by rev.
func (c *Client) QueryWorkItems(ctx context.Context, token, org, project string, in WorkItemQuery) (WorkItemPage, error) {
	if !validProject(org, project) || in.AsOf.IsZero() || in.AfterID < 0 || in.AfterID > 2147483647 || (in.ChangedFrom == nil) != (in.ChangedThrough == nil) {
		return WorkItemPage{}, invalid()
	}
	q := "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project AND [System.Id] > " + strconv.FormatInt(in.AfterID, 10)
	if in.ChangedFrom != nil {
		if in.ChangedFrom.IsZero() || in.ChangedThrough.IsZero() || in.ChangedFrom.After(*in.ChangedThrough) || in.ChangedThrough.After(in.AsOf) {
			return WorkItemPage{}, invalid()
		}
		q += " AND [System.ChangedDate] >= '" + in.ChangedFrom.UTC().Format(time.RFC3339Nano) + "' AND [System.ChangedDate] <= '" + in.ChangedThrough.UTC().Format(time.RFC3339Nano) + "'"
	}
	q += " ORDER BY [System.Id] ASC ASOF '" + in.AsOf.UTC().Format(time.RFC3339Nano) + "'"
	out, err := c.queryWorkItems(ctx, token, org, project, q, 200)
	if err != nil {
		return WorkItemPage{}, err
	}
	// The response must identify the requested snapshot; a changed asOf cannot be
	// silently mixed with subsequent batches from the caller's fixed timestamp.
	if !out.AsOf.Equal(in.AsOf) {
		return WorkItemPage{}, protocol()
	}
	page := WorkItemPage{AsOf: out.AsOf.UTC(), IDs: []int64{}}
	last := in.AfterID
	for _, v := range out.WorkItems {
		if !ValidObjectID(v.ID) || v.ID <= last {
			return WorkItemPage{}, protocol()
		}
		page.IDs = append(page.IDs, v.ID)
		last = v.ID
	}
	return page, nil
}

var requiredWorkItemFields = []string{"System.Id", "System.TeamProject", "System.Title", "System.WorkItemType", "System.State", "System.CreatedDate", "System.ChangedDate"}

func workItemFields(fields []string) ([]string, error) {
	if len(fields) > 200 {
		return nil, invalid()
	}
	out := []string{}
	seen := map[string]bool{}
	for _, f := range append(append([]string{}, requiredWorkItemFields...), fields...) {
		if !validField(f) {
			return nil, invalid()
		}
		if !seen[f] {
			out = append(out, f)
			seen[f] = true
		}
	}
	if len(out) > 200 {
		return nil, invalid()
	}
	return out, nil
}
func checkFieldValue(raw json.RawMessage) bool {
	var v any
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return false
	}
	var walk func(any, int) bool
	walk = func(v any, depth int) bool {
		if depth > 8 {
			return false
		}
		switch x := v.(type) {
		case string:
			return boundedText(x)
		case []any:
			if len(x) > MaxItems {
				return false
			}
			for _, a := range x {
				if !walk(a, depth+1) {
					return false
				}
			}
		case map[string]any:
			if len(x) > 200 {
				return false
			}
			for k, a := range x {
				if !boundedText(k) || !walk(a, depth+1) {
					return false
				}
			}
		}
		return true
	}
	return walk(v, 0)
}
func validateWorkItem(v *WorkItem, project Project, asOf time.Time) bool {
	if !ValidObjectID(v.ID) || !ValidObjectID(v.Rev) || v.Fields == nil || len(v.Fields) > 200 || len(v.Relations) > MaxItems {
		return false
	}
	for k, raw := range v.Fields {
		if !validField(k) || !checkFieldValue(raw) {
			return false
		}
	}
	var id int64
	var team, title, kind, state string
	var created, changed time.Time
	if json.Unmarshal(v.Fields["System.Id"], &id) != nil || id != v.ID || json.Unmarshal(v.Fields["System.TeamProject"], &team) != nil || team != project.Name || json.Unmarshal(v.Fields["System.Title"], &title) != nil || strings.TrimSpace(title) == "" || json.Unmarshal(v.Fields["System.WorkItemType"], &kind) != nil || kind == "" || json.Unmarshal(v.Fields["System.State"], &state) != nil || state == "" || json.Unmarshal(v.Fields["System.CreatedDate"], &created) != nil || created.IsZero() || json.Unmarshal(v.Fields["System.ChangedDate"], &changed) != nil || changed.IsZero() || changed.Before(created) || !asOf.IsZero() && changed.After(asOf) {
		return false
	}
	if raw, ok := v.Fields["System.AssignedTo"]; ok && string(raw) != "null" {
		var identity IdentityRef
		if json.Unmarshal(raw, &identity) != nil || !validIdentityRef(&identity) {
			return false
		}
	}
	for _, r := range v.Relations {
		if !ValidText(r.Rel, 256) || !ValidText(r.URL, MaxCellBytes) || len(r.Attributes) > 200 {
			return false
		}
		for _, raw := range r.Attributes {
			if !checkFieldValue(raw) {
				return false
			}
		}
	}
	return true
}
func (c *Client) WorkItems(ctx context.Context, token, org string, project Project, ids []int64, asOf time.Time, fields []string) ([]WorkItem, error) {
	if !validProject(org, project.ID) || !ValidText(project.Name, MaxCellBytes) || len(ids) == 0 || len(ids) > 200 || asOf.IsZero() {
		return nil, invalid()
	}
	selected, err := workItemFields(fields)
	if err != nil {
		return nil, err
	}
	wanted := map[int64]bool{}
	for _, id := range ids {
		if !ValidObjectID(id) || wanted[id] {
			return nil, invalid()
		}
		wanted[id] = true
	}
	input := map[string]any{"ids": ids, "fields": selected, "asOf": asOf.UTC().Format(time.RFC3339Nano), "errorPolicy": "Fail", "$expand": "Relations"}
	var out struct {
		Value []WorkItem `json:"value"`
	}
	h, err := c.request(ctx, "POST", apiURL("dev.azure.com", projectPath(org, project.ID)+"/_apis/wit/workitemsbatch", nil), token, input, nil, false, &out)
	if err != nil {
		return nil, err
	}
	if out.Value == nil || len(out.Value) != len(ids) || h.Get("x-ms-continuationtoken") != "" {
		return nil, protocol()
	}
	for i := range out.Value {
		v := &out.Value[i]
		if !wanted[v.ID] || !validateWorkItem(v, project, asOf) {
			return nil, protocol()
		}
		delete(wanted, v.ID)
	}
	return out.Value, nil
}
func (c *Client) WorkItem(ctx context.Context, token, org string, project Project, id int64, asOf time.Time, fields []string) (WorkItem, error) {
	if !validProject(org, project.ID) || !ValidText(project.Name, MaxCellBytes) || !ValidObjectID(id) {
		return WorkItem{}, invalid()
	}
	selected, err := workItemFields(fields)
	if err != nil {
		return WorkItem{}, err
	}
	q := url.Values{"fields": {strings.Join(selected, ",")}, "$expand": {"Relations"}}
	if !asOf.IsZero() {
		q.Set("asOf", asOf.UTC().Format(time.RFC3339Nano))
	}
	var out WorkItem
	_, err = c.request(ctx, "GET", apiURL("dev.azure.com", projectPath(org, project.ID)+"/_apis/wit/workitems/"+strconv.FormatInt(id, 10), q), token, nil, nil, false, &out)
	if err != nil {
		return WorkItem{}, err
	}
	if out.ID != id || !validateWorkItem(&out, project, asOf) {
		return WorkItem{}, protocol()
	}
	return out, nil
}
