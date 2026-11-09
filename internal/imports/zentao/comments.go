package zentao

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// REST v1 detail actions contain the stable action ID. Actor is a display name,
// not an account ID, so callers must not use it to assign a local identity.
type Comment struct {
	ID    json.Number `json:"id"`
	Actor string      `json:"actor"`
	Date  string      `json:"date"`
	Body  string      `json:"comment"`
}

func (c *Client) Comments(ctx context.Context, external string) ([]Comment, error) {
	rest, ok := strings.CutPrefix(external, c.prefix)
	kind, id, split := strings.Cut(rest, "/")
	n, e := strconv.ParseUint(id, 10, 63)
	if !ok || !split || e != nil || n == 0 || strconv.FormatUint(n, 10) != id || (kind != "stories" && kind != "tasks" && kind != "bugs") {
		return nil, ErrInput
	}
	var v struct {
		ID      json.Number
		Actions []Comment
	}
	if e = c.request(ctx, "/"+kind+"/"+id, nil, &v); e != nil {
		return nil, e
	}
	if v.ID.String() != id || len(v.Actions) > 2000 {
		return nil, ErrUpstream
	}
	out := []Comment{}
	seen := map[string]bool{}
	for _, a := range v.Actions {
		if strings.TrimSpace(a.Body) == "" {
			continue
		}
		n, e := a.ID.Int64()
		if e != nil || n <= 0 || seen[a.ID.String()] || len(a.Body) > 64000 {
			return nil, ErrUpstream
		}
		seen[a.ID.String()] = true
		out = append(out, a)
	}
	return out, nil
}
