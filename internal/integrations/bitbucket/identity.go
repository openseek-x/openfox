package bitbucket

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// UUID keeps the native identity without numeric conversion. Both documented
// brace-wrapped UUIDs and their canonical form normalize to the same key.
func UUID(raw string) (string, error) {
	value := raw
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		value = value[1 : len(value)-1]
	}
	value = strings.ToLower(value)
	if !uuidPattern.MatchString(value) || value == "00000000-0000-0000-0000-000000000000" {
		return "", ErrInvalid
	}
	return value, nil
}
func uuidPath(raw string) (string, error) {
	value, err := UUID(raw)
	if err != nil {
		return "", err
	}
	return url.PathEscape("{" + value + "}"), nil
}

type User struct {
	UUID        string `json:"uuid"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname"`
}

func (c *Client) Identity(ctx context.Context, token string) (User, error) {
	var out User
	if err := c.request(ctx, token, "GET", APIURL+"/user", nil, &out, 200); err != nil {
		return User{}, err
	}
	id, err := UUID(out.UUID)
	if err != nil || len(out.DisplayName) > 1024 || len(out.Nickname) > 1024 {
		return User{}, ErrInvalid
	}
	out.UUID = id
	return out, nil
}

type Workspace struct {
	UUID string `json:"uuid"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}
type WorkspaceAccess struct {
	Administrator *bool     `json:"administrator"`
	Workspace     Workspace `json:"workspace"`
}
type Page[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

// safeNext follows the opaque cursor only within the original collection and
// origin. No upstream link can cause bearer credentials to leave that endpoint.
func safeNext(next, collection string) (string, error) {
	if next == "" {
		return "", nil
	}
	if len(next) > 8192 {
		return "", ErrIncomplete
	}
	u, err := url.Parse(next)
	base, e := url.Parse(collection)
	if err != nil || e != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" || u.Path != base.Path || u.EscapedPath() != base.EscapedPath() {
		return "", ErrIncomplete
	}
	return u.String(), nil
}
func readPage[T any](ctx context.Context, c *Client, token, collection, next string) (Page[T], error) {
	var out Page[T]
	if budget, ok := ctx.Value(traversalKey{}).(*traversalBudget); ok && budget.pages.Add(1) > 20 {
		return out, ErrIncomplete
	}
	raw := collection
	if next != "" {
		var err error
		raw, err = safeNext(next, collection)
		if err != nil {
			return out, err
		}
	} else {
		u, _ := url.Parse(collection)
		q := u.Query()
		q.Set("pagelen", "100")
		u.RawQuery = q.Encode()
		raw = u.String()
	}
	var payload struct {
		Values json.RawMessage `json:"values"`
		Next   string          `json:"next"`
	}
	if err := c.request(ctx, token, "GET", raw, nil, &payload, 200); err != nil {
		return out, err
	}
	if len(payload.Values) == 0 || string(payload.Values) == "null" || json.Unmarshal(payload.Values, &out.Values) != nil || len(out.Values) > PageSize {
		return Page[T]{}, ErrIncomplete
	}
	var err error
	out.Next, err = safeNext(payload.Next, collection)
	if err != nil || out.Next != "" && out.Next == raw {
		return Page[T]{}, ErrIncomplete
	}
	return out, nil
}
func (c *Client) Workspaces(ctx context.Context, token, next string) (Page[WorkspaceAccess], error) {
	out, err := readPage[WorkspaceAccess](ctx, c, token, APIURL+"/user/workspaces", next)
	if err != nil {
		return out, err
	}
	for i := range out.Values {
		item := &out.Values[i]
		id, e := UUID(item.Workspace.UUID)
		if e != nil || item.Administrator == nil || !validOpaque(item.Workspace.Slug, 255) || len(item.Workspace.Name) > 1024 {
			return Page[WorkspaceAccess]{}, ErrInvalid
		}
		item.Workspace.UUID = id
	}
	return out, nil
}

// WorkspaceAdmin rechecks the explicit administrator field, without guessing a
// role from display names or the deprecated collaborator permission.
func (c *Client) WorkspaceAdmin(ctx context.Context, token, workspaceID string) (bool, error) {
	target, err := UUID(workspaceID)
	if err != nil {
		return false, err
	}
	ctx, cancel := WithTraversalBudget(ctx)
	defer cancel()
	next := ""
	seen := map[string]bool{}
	count := 0
	for page := 0; page < MaxPages; page++ {
		rows, e := c.Workspaces(ctx, token, next)
		if e != nil {
			return false, e
		}
		count += len(rows.Values)
		if count > 100 {
			return false, ErrIncomplete
		}
		for _, row := range rows.Values {
			if row.Workspace.UUID == target {
				return *row.Administrator, nil
			}
		}
		if rows.Next == "" {
			return false, nil
		}
		if count == 100 {
			return false, ErrIncomplete
		}
		if seen[rows.Next] {
			return false, ErrIncomplete
		}
		seen[rows.Next] = true
		next = rows.Next
	}
	return false, ErrIncomplete
}
