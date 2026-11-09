// Package zentao imports and synchronizes project-scoped ZenTao REST v1 objects.
package zentao

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openfox/internal/platform/safehttp"
)

var ErrInput = errors.New("invalid ZenTao source")
var ErrAuth = errors.New("ZenTao authorization rejected")
var ErrUpstream = errors.New("invalid ZenTao response")

type Credentials struct {
	Site          string `json:"site"`
	Account       string `json:"account"`
	Password      string `json:"password"`
	BasicUser     string `json:"basic_user,omitempty"`
	BasicPassword string `json:"basic_password,omitempty"`
}
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Client struct {
	base, token, prefix string
	credentials         Credentials
	http                *http.Client
}

func Connect(ctx context.Context, in Credentials) (*Client, error) {
	return connect(ctx, in, safehttp.Options{})
}

func connect(ctx context.Context, in Credentials, opts safehttp.Options) (*Client, error) {
	u, err := safehttp.CheckURL(in.Site)
	if err != nil || u.RawQuery != "" || u.ForceQuery || in.Account == "" || in.Password == "" || len(in.Account) > 200 || len(in.Password) > 2048 || len(in.BasicPassword) > 2048 || len(in.BasicUser) > 200 || strings.ContainsAny(in.BasicUser, ":\r\n") || (in.BasicUser == "") != (in.BasicPassword == "") {
		return nil, ErrInput
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	id := sha256.Sum256([]byte(u.String()))
	c := &Client{base: u.String() + "/api.php/v1", prefix: hex.EncodeToString(id[:16]) + "/", credentials: in, http: safehttp.Client(opts)}
	c.http.Timeout = 10 * time.Second
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	body, _ := json.Marshal(map[string]string{"account": in.Account, "password": in.Password})
	var result struct{ Token string }
	if err = c.request(ctx, "/tokens", body, &result); err != nil {
		return nil, err
	}
	if result.Token == "" || strings.ContainsAny(result.Token, "\r\n") {
		return nil, ErrAuth
	}
	c.token = result.Token
	c.credentials.Account = ""
	c.credentials.Password = ""
	return c, nil
}

// Session holds the issued API token and any independently required gateway auth.
type Session struct{ Site, Token, BasicUser, BasicPassword string }

func (c *Client) Session() Session {
	return Session{strings.TrimSuffix(c.base, "/api.php/v1"), c.token, c.credentials.BasicUser, c.credentials.BasicPassword}
}
func (s Session) Client(opts safehttp.Options) (*Client, error) {
	u, err := safehttp.CheckURL(s.Site)
	if err != nil || u.RawQuery != "" || s.Token == "" {
		return nil, ErrInput
	}
	sum := sha256.Sum256([]byte(s.Site))
	httpClient := safehttp.Client(opts)
	httpClient.Timeout = 10 * time.Second
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: s.Site + "/api.php/v1", token: s.Token, prefix: hex.EncodeToString(sum[:16]) + "/", http: httpClient, credentials: Credentials{BasicUser: s.BasicUser, BasicPassword: s.BasicPassword}}, nil
}
func (c *Client) request(ctx context.Context, path string, body []byte, out any) error {
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	return c.requestMethod(ctx, method, path, body, out)
}
func (c *Client) requestMethod(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return ErrInput
	}
	if c.credentials.BasicUser != "" {
		req.SetBasicAuth(c.credentials.BasicUser, c.credentials.BasicPassword)
	}
	if c.token != "" {
		req.Header.Set("Token", c.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrAuth
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ErrUpstream
	}
	const limit = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(data) > limit || json.Unmarshal(data, out) != nil {
		return ErrUpstream
	}
	return nil
}

type entry struct {
	ID, Parent                             json.Number
	Name, Title, Status, Spec, Steps, Desc string
	AssignedTo                             json.RawMessage
}

func (c *Client) pages(ctx context.Context, path, kind, status string, visit func(entry) error) error {
	seen := map[string]bool{}
	for page := 1; page <= 250; page++ {
		q := url.Values{"limit": {"100"}, "page": {strconv.Itoa(page)}}
		if status != "" {
			q.Set("status", status)
		}
		var data map[string]json.RawMessage
		if err := c.request(ctx, path+"?"+q.Encode(), nil, &data); err != nil {
			return err
		}
		var rows []entry
		var total int
		if json.Unmarshal(data[kind], &rows) != nil || rows == nil || json.Unmarshal(data["total"], &total) != nil || total > 5000 {
			return ErrUpstream
		}
		for _, v := range rows {
			n, e := v.ID.Int64()
			if e != nil || n < 1 || seen[v.ID.String()] || len(seen) >= 5000 {
				return ErrUpstream
			}
			seen[v.ID.String()] = true
			if err := visit(v); err != nil {
				return err
			}
		}
		if len(seen) >= total {
			return nil
		}
		if len(rows) == 0 {
			return ErrUpstream
		}
	}
	return ErrUpstream
}
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	result := []Project{}
	for _, kind := range []string{"products", "projects"} {
		if err := c.pages(ctx, "/"+kind, kind, "", func(v entry) error { result = append(result, Project{kind + "/" + v.ID.String(), v.Name}); return nil }); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (c *Client) Rows(ctx context.Context, project string) ([][]string, error) {
	kind, id, ok := strings.Cut(project, "/")
	n, err := strconv.ParseUint(id, 10, 63)
	if !ok || n == 0 || err != nil || strconv.FormatUint(n, 10) != id || (kind != "products" && kind != "projects") {
		return nil, ErrInput
	}
	rows := [][]string{{"ID", "Summary", "Description", "Status", "Assignee", "Issue type", "Parent id"}}
	size := 0
	fetch := func(path, category, status string) error {
		return c.pages(ctx, path, category, status, func(v entry) error {
			var owner string
			if json.Unmarshal(v.AssignedTo, &owner) != nil {
				var user struct{ Account string }
				_ = json.Unmarshal(v.AssignedTo, &user)
				owner = user.Account
			}
			title := v.Title
			if title == "" {
				title = v.Name
			}
			desc := v.Spec
			if desc == "" {
				desc = v.Steps
			}
			if desc == "" {
				desc = v.Desc
			}
			parent := ""
			if pv, e := v.Parent.Int64(); e == nil && pv > 0 {
				parent = c.prefix + category + "/" + v.Parent.String()
			}
			row := []string{c.prefix + category + "/" + v.ID.String(), title, desc, v.Status, owner, category, parent}
			for _, value := range row {
				size += len(value)
				if len(value) > 64<<10 || size > 10<<20 {
					return ErrInput
				}
			}
			if len(rows) > 5000 {
				return ErrInput
			}
			rows = append(rows, row)
			return nil
		})
	}
	if err := fetch("/"+project+"/stories", "stories", "allstory"); err != nil {
		return nil, err
	}
	if kind == "products" {
		if err := fetch("/"+project+"/bugs", "bugs", "all"); err != nil {
			return nil, err
		}
	} else {
		err := c.pages(ctx, "/"+project+"/executions", "executions", "", func(v entry) error { return fetch("/executions/"+v.ID.String()+"/tasks", "tasks", "all") })
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// UpdateText only receives IDs discovered by Rows for the linked project.
func (c *Client) UpdateText(ctx context.Context, project, external string, title, desc *string) error {
	rest, ok := strings.CutPrefix(external, c.prefix)
	kind, id, split := strings.Cut(rest, "/")
	n, e := strconv.ParseUint(id, 10, 63)
	if !ok || !split || e != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		return ErrInput
	}
	path := "/" + kind + "/" + id
	method := http.MethodPut
	fields := map[string]any{}
	titleKey, descKey := "title", "spec"
	switch kind {
	case "stories":
		path += "/change"
		method = http.MethodPost
	case "tasks":
		titleKey = "name"
		descKey = "desc"
	case "bugs":
		var b struct {
			ID, Product, Pri, Severity json.Number
			Title, Type                string
		}
		if e := c.request(ctx, path, nil, &b); e != nil {
			return e
		}
		if b.ID.String() != id || project != "products/"+b.Product.String() || b.Type == "" || b.Pri == "" || b.Severity == "" {
			return ErrUpstream
		}
		fields["title"], fields["pri"], fields["severity"], fields["type"] = b.Title, b.Pri, b.Severity, b.Type
		descKey = "steps"
	default:
		return ErrInput
	}
	if title != nil {
		fields[titleKey] = *title
	}
	if desc != nil {
		fields[descKey] = *desc
	}
	body, e := json.Marshal(fields)
	if e != nil {
		return ErrUpstream
	}
	var result entry
	if e = c.requestMethod(ctx, method, path, body, &result); e != nil {
		return e
	}
	if result.ID.String() != id {
		return ErrUpstream
	}
	actualTitle, actualDesc := result.Title, result.Spec
	switch kind {
	case "tasks":
		actualTitle, actualDesc = result.Name, result.Desc
	case "bugs":
		actualDesc = result.Steps
	}
	if (title != nil && actualTitle != *title) || (desc != nil && actualDesc != *desc) {
		return ErrUpstream
	}
	return nil
}

func (c *Client) UpdateStatus(ctx context.Context, external, target string) error {
	rest, ok := strings.CutPrefix(external, c.prefix)
	kind, id, split := strings.Cut(rest, "/")
	n, e := strconv.ParseUint(id, 10, 63)
	if !ok || !split || e != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		return ErrInput
	}
	path := "/" + kind + "/" + id
	var current struct {
		ID                       json.Number
		Status                   string
		Left, Consumed           *float64
		ClosedReason, Resolution string
	}
	if e = c.request(ctx, path, nil, &current); e != nil {
		return e
	}
	if current.ID.String() != id {
		return ErrUpstream
	}
	if current.Status == target {
		return nil
	}
	fields := map[string]any{}
	action := ""
	switch kind {
	case "stories":
		switch target {
		case "active":
			action = "activate"
		case "closed":
			action = "close"
			if current.ClosedReason == "" {
				return ErrInput
			}
			fields["closedReason"] = current.ClosedReason
		}
	case "bugs":
		switch target {
		case "active":
			action = "activate"
		case "closed":
			action = "close"
		case "resolved":
			action = "resolve"
			if current.Resolution == "" || current.Resolution == "duplicate" {
				return ErrInput
			}
			fields["resolution"] = current.Resolution
		}
	case "tasks":
		switch target {
		case "doing":
			action = "start"
			if current.Left == nil {
				return ErrInput
			}
			fields["left"] = *current.Left
		case "done":
			action = "finish"
			if current.Consumed == nil {
				return ErrInput
			}
			fields["consumed"] = *current.Consumed
		case "pause":
			action = "pause"
		case "cancel":
			action = "cancel"
		case "closed":
			action = "close"
		case "wait":
			action = "activate"
			if current.Left == nil {
				return ErrInput
			}
			fields["left"] = *current.Left
		}
	}
	if action == "" {
		return ErrInput
	}
	body, e := json.Marshal(fields)
	if e != nil {
		return e
	}
	var result entry
	if e = c.requestMethod(ctx, http.MethodPost, path+"/"+action, body, &result); e != nil {
		return e
	}
	if result.ID.String() != id || result.Status != target {
		return ErrUpstream
	}
	return nil
}
