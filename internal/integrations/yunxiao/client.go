// Package yunxiao implements the center-edition, read-only Projex API.
// APIURL and HTTP are server-owned test seams, never user-editable settings.
package yunxiao

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"openfox/internal/integrations/gitapi"
)

const Origin = "https://openapi-rdc.aliyuncs.com"
const MaxItems = 5000
const maxResponse = 8 << 20
const pageSize = 200

var (
	ErrForbidden = errors.New("yunxiao: required organization or resource access denied")
	ErrInvalid   = errors.New("yunxiao: invalid identity or request")
	ErrProtocol  = errors.New("yunxiao: incomplete or invalid response")
	ErrLimit     = errors.New("yunxiao: source exceeds import limits")
	idPattern    = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
)

func ValidID(id string) bool { return idPattern.MatchString(id) }
func ValidToken(token string) bool {
	return token != "" && len(token) <= 4096 && strings.TrimSpace(token) == token && strings.IndexFunc(token, unicode.IsControl) < 0
}

// ConnectionIdentity remains stable when the credential holder changes.
func ConnectionIdentity(org string) string { return identityKey("yunxiao", Origin, org) }
func UserIdentity(org, user string) string { return identityKey(Origin, org, user) }
func identityKey(parts ...string) string {
	raw, _ := json.Marshal(parts)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

type Client struct {
	APIURL string
	HTTP   *http.Client
}

func New() *Client {
	return &Client{APIURL: Origin, HTTP: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second,
		MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second,
	}}}
}

// POST is used only for the documented read-only search endpoints. No retries or
// write API exist in this client; a failed read never produces a partial snapshot.
func (c *Client) request(ctx context.Context, token, method, path string, input, output any) (http.Header, error) {
	if !ValidToken(token) {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return nil, ErrInvalid
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.APIURL, "/")+path, body)
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("x-yunxiao-token", token)
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = New().HTTP
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		return nil, gitapi.ErrRequestFailed
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, gitapi.ErrTokenRevoked
	case http.StatusForbidden:
		return nil, ErrForbidden
	case http.StatusNotFound:
		return nil, gitapi.ErrNotFound
	case http.StatusTooManyRequests:
		now := time.Now()
		at := now.Add(5 * time.Minute)
		if seconds, e := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); e == nil && seconds >= 0 {
			at = now.Add(time.Duration(min(seconds, 86400)) * time.Second)
		} else if value, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil && value.After(now) {
			at = value
			if at.After(now.Add(24 * time.Hour)) {
				at = now.Add(24 * time.Hour)
			}
		}
		return nil, &gitapi.ErrRateLimited{ResetAt: at}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, gitapi.ErrRequestFailed
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return nil, ErrProtocol
	}
	if json.Unmarshal(raw, output) != nil {
		return nil, ErrProtocol
	}
	return resp.Header, nil
}

type User struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	DeletedAt string `json:"deletedAt"`
}

func (c *Client) Identity(ctx context.Context, token, org string) (User, error) {
	var user User
	if !ValidID(org) {
		return user, ErrInvalid
	}
	if _, err := c.request(ctx, token, http.MethodGet, "/oapi/v1/platform/user", nil, &user); err != nil {
		return user, err
	}
	if !ValidID(user.ID) || user.DeletedAt != "" {
		return User{}, ErrForbidden
	}
	var member struct {
		UserID         string `json:"userId"`
		OrganizationID string `json:"organizationId"`
		Status         string `json:"status"`
	}
	path := "/oapi/v1/platform/organizations/" + url.PathEscape(org) + "/members:readByUser?" + url.Values{"userId": {user.ID}}.Encode()
	if _, err := c.request(ctx, token, http.MethodGet, path, nil, &member); err != nil {
		return User{}, err
	}
	if member.UserID != user.ID || member.OrganizationID != org || (member.Status != "ENABLED" && member.Status != "NORMAL_USING") {
		return User{}, ErrForbidden
	}
	if user.Name == "" {
		user.Name = user.Username
	}
	if user.Name == "" {
		user.Name = user.ID
	}
	if len(user.Name) > 200 || len(user.Email) > 320 {
		return User{}, ErrProtocol
	}
	return user, nil
}

func base(org string) string { return "/oapi/v1/projex/organizations/" + url.PathEscape(org) }

type Named struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type Project struct {
	Named
	LogicalStatus string `json:"logicalStatus"`
}
type WorkitemType struct {
	Named
	CategoryID string `json:"categoryId"`
	Enable     bool   `json:"enable"`
}

func (c *Client) Projects(ctx context.Context, token, org string) ([]Project, error) {
	if !ValidID(org) {
		return nil, ErrInvalid
	}
	rows, err := pages[Project](ctx, c, token, base(org)+"/projects:search", map[string]any{}, func(p Project) string { return p.ID })
	if err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(rows))
	for _, p := range rows {
		if p.Name == "" || len(p.Name) > 1000 || (p.LogicalStatus != "normal" && p.LogicalStatus != "archived") {
			return nil, ErrProtocol
		}
		if p.LogicalStatus == "normal" {
			out = append(out, p)
		}
	}
	return out, nil
}

func (c *Client) Types(ctx context.Context, token, org, project, category string) ([]WorkitemType, error) {
	if !ValidID(org) || project != "" && (!ValidID(project) || !ValidID(category)) {
		return nil, ErrInvalid
	}
	path := base(org) + "/workitemTypes"
	if project != "" {
		path = base(org) + "/projects/" + url.PathEscape(project) + "/workitemTypes?" + url.Values{"category": {category}}.Encode()
	}
	var rows []WorkitemType
	if _, err := c.request(ctx, token, http.MethodGet, path, nil, &rows); err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, ErrProtocol
	}
	if len(rows) > MaxItems {
		return nil, ErrLimit
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if !ValidID(row.ID) || !ValidID(row.CategoryID) || row.Name == "" || len(row.Name) > 1000 || seen[row.ID] || project != "" && row.CategoryID != category {
			return nil, ErrProtocol
		}
		seen[row.ID] = true
	}
	return rows, nil
}

func pages[T any](ctx context.Context, c *Client, token, path string, input map[string]any, id func(T) string) ([]T, error) {
	out := make([]T, 0)
	seen := map[string]bool{}
	total := -1
	input["perPage"], input["orderBy"], input["sort"] = pageSize, "gmtCreate", "asc"
	for page := 1; page <= MaxItems/pageSize+1; page++ {
		input["page"] = page
		var rows []T
		h, err := c.request(ctx, token, http.MethodPost, path, input, &rows)
		if err != nil {
			return nil, err
		}
		if rows == nil || len(rows) > pageSize {
			return nil, ErrProtocol
		}
		count, e := strconv.Atoi(h.Get("x-total"))
		if e != nil || count < 0 {
			return nil, ErrProtocol
		}
		if count > MaxItems {
			return nil, ErrLimit
		}
		if total >= 0 && total != count {
			return nil, ErrProtocol
		}
		total = count
		gotPage, e1 := strconv.Atoi(h.Get("x-page"))
		perPage, e2 := strconv.Atoi(h.Get("x-per-page"))
		totalPages, e3 := strconv.Atoi(h.Get("x-total-pages"))
		expectedPages := (total + pageSize - 1) / pageSize
		if e1 != nil || e2 != nil || e3 != nil || gotPage != page || perPage != pageSize || (totalPages != expectedPages && !(total == 0 && totalPages == 1)) {
			return nil, ErrProtocol
		}
		if len(rows) != min(pageSize, total-len(out)) {
			return nil, ErrProtocol
		}
		for _, row := range rows {
			key := id(row)
			if !ValidID(key) || seen[key] {
				return nil, ErrProtocol
			}
			seen[key] = true
			out = append(out, row)
		}
		next := h.Get("x-next-page")
		if len(out) == total {
			if next != "" && next != "0" {
				return nil, ErrProtocol
			}
			return out, nil
		}
		if next != "" && next != strconv.Itoa(page+1) {
			return nil, ErrProtocol
		}
	}
	return nil, ErrLimit
}
