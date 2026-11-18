// Package bitbucket implements the fixed-origin Bitbucket Cloud API. Tokens,
// refresh leases, retry policy and tenant authorization belong to its callers.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"openfox/internal/integrations/gitapi"
)

const (
	Origin           = "https://bitbucket.org"
	APIURL           = "https://api.bitbucket.org/2.0"
	tokenURL         = Origin + "/site/oauth2/access_token"
	maxResponseBytes = 2 << 20
	maxTokenBytes    = 8192
	requestTimeout   = 10 * time.Second
	PageSize         = 100
	MaxPages         = 50
	MaxCommits       = 100
)

var (
	ErrInvalid       = errors.New("bitbucket: invalid input or response")
	ErrNotConfigured = errors.New("bitbucket: OAuth application not configured")
	ErrInvalidGrant  = errors.New("bitbucket: invalid OAuth grant")
	ErrForbidden     = errors.New("bitbucket: access denied")
	ErrScope         = errors.New("bitbucket: unexpected OAuth scopes")
	ErrIncomplete    = errors.New("bitbucket: incomplete pagination")
	ErrCommitLimit   = errors.New("bitbucket: commit limit exceeded")
)

type App struct{ ClientID, ClientSecret string }

// Transport is a server-owned test seam; no configurable upstream origin is exposed.
type Client struct {
	App       App
	Transport http.RoundTripper
}

type traversalKey struct{}
type traversalBudget struct {
	bytes atomic.Int64
	pages atomic.Int64
}

// WithTraversalBudget shares a 30-second, 20-page, 10-MiB read budget across
// all branches in one push, or all pages in a comment reconciliation. Nested
// helpers preserve the caller's budget rather than multiplying it.
func WithTraversalBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if ctx.Value(traversalKey{}) == nil {
		ctx = context.WithValue(ctx, traversalKey{}, &traversalBudget{})
	}
	return ctx, cancel
}

func New(app App) *Client { return &Client{App: app} }

var defaultTransport = &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second,
	MaxIdleConns: 10, MaxIdleConnsPerHost: 5, IdleConnTimeout: 30 * time.Second,
}

func validOpaque(s string, limit int) bool {
	if len(s) < 1 || len(s) > limit {
		return false
	}
	for _, ch := range s {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}
func ValidToken(s string) bool { return validOpaque(s, maxTokenBytes) }
func (c *Client) configured() bool {
	return c != nil && validOpaque(c.App.ClientID, 255) && validOpaque(c.App.ClientSecret, maxTokenBytes)
}
func (c *Client) AuthorizeURL(state string) (string, error) {
	if !c.configured() {
		return "", ErrNotConfigured
	}
	if !validOpaque(state, 256) || len(state) < 32 {
		return "", ErrInvalid
	}
	query := url.Values{"client_id": {c.App.ClientID}, "response_type": {"code"}, "state": {state}}
	// Consumer scopes and the exact callback are registered server-side. The
	// grant cannot narrow scopes; do not add an unverified PKCE contract.
	return Origin + "/site/oauth2/authorize?" + query.Encode(), nil
}

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Scopes       string    `json:"scopes"`
	ExpiresIn    int64     `json:"expires_in"`
	ExpiresAt    time.Time `json:"-"`
}

func ValidateScopes(scopes string, identity bool) error {
	seen := map[string]bool{}
	for _, scope := range strings.Fields(scopes) {
		if scope != "account" && (identity || scope != "pullrequest" && scope != "repository") {
			return ErrScope
		}
		seen[scope] = true
	}
	if !seen["account"] || !identity && !seen["pullrequest"] {
		return ErrScope
	}
	return nil
}
func (c *Client) Exchange(ctx context.Context, code string) (Token, error) {
	if !validOpaque(code, maxTokenBytes) {
		return Token{}, ErrInvalid
	}
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}})
}
func (c *Client) Refresh(ctx context.Context, refresh string) (Token, error) {
	if !ValidToken(refresh) {
		return Token{}, ErrInvalid
	}
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}
func (c *Client) token(ctx context.Context, form url.Values) (Token, error) {
	var out Token
	if !c.configured() {
		return out, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return out, ErrInvalid
	}
	req.SetBasicAuth(c.App.ClientID, c.App.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err = c.send(req, 200, &out); err != nil {
		return Token{}, err
	}
	// Never substitute a guessed lifetime or keep the previous rotating token.
	if !ValidToken(out.AccessToken) || !ValidToken(out.RefreshToken) || !strings.EqualFold(out.TokenType, "bearer") || out.ExpiresIn < 1 || out.ExpiresIn > int64((1<<63-1)/time.Second) || len(out.Scopes) > 1024 || len(strings.Fields(out.Scopes)) == 0 {
		return Token{}, gitapi.ErrUnknownResult
	}
	out.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return out, nil
}
func (c *Client) request(ctx context.Context, token, method, rawURL string, input, output any, expected int) error {
	if !ValidToken(token) {
		return ErrInvalid
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.Host != "api.bitbucket.org" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "/2.0/") {
		return ErrInvalid
	}
	var body io.Reader
	if input != nil {
		data, e := json.Marshal(input)
		if e != nil || len(data) > 64<<10 {
			return ErrInvalid
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return ErrInvalid
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.send(req, expected, output)
}
func (c *Client) send(req *http.Request, expected int, output any) error {
	ctx, cancel := context.WithTimeout(req.Context(), requestTimeout)
	defer cancel()
	req = req.Clone(ctx)
	req.Header.Set("Accept", "application/json")
	transport := http.RoundTripper(defaultTransport)
	if c != nil && c.Transport != nil {
		transport = c.Transport
	}
	client := http.Client{Transport: transport, Timeout: requestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	write := req.Method != http.MethodGet
	resp, err := client.Do(req)
	if err != nil {
		if write {
			return gitapi.ErrUnknownResult
		}
		return gitapi.ErrRequestFailed
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		if write {
			return gitapi.ErrUnknownResult
		}
		return ErrInvalid
	}
	if !write {
		if budget, ok := ctx.Value(traversalKey{}).(*traversalBudget); ok && budget.bytes.Add(int64(len(data))) > 10<<20 {
			return ErrIncomplete
		}
	}
	switch resp.StatusCode {
	case 400:
		if req.URL.String() == tokenURL {
			var body struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &body) == nil && body.Error == "invalid_grant" {
				return ErrInvalidGrant
			}
		}
		return ErrInvalid
	case 401:
		return gitapi.ErrTokenRevoked
	case 403:
		return ErrForbidden
	case 404:
		return gitapi.ErrNotFound
	case 429:
		now := time.Now()
		at := now.Add(time.Minute)
		if seconds, e := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); e == nil && seconds >= 0 {
			at = now.Add(time.Duration(max(1, min(seconds, 3600))) * time.Second)
		} else if parsed, e := http.ParseTime(resp.Header.Get("Retry-After")); e == nil {
			at = now.Add(max(time.Second, min(time.Hour, parsed.Sub(now))))
		}
		return &gitapi.ErrRateLimited{ResetAt: at}
	}
	if resp.StatusCode != expected {
		if write && (resp.StatusCode >= 500 || resp.StatusCode >= 200 && resp.StatusCode < 300) {
			return gitapi.ErrUnknownResult
		}
		return gitapi.ErrRequestFailed
	}
	if output != nil && (bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, output) != nil) {
		if write {
			return gitapi.ErrUnknownResult
		}
		return ErrInvalid
	}
	return nil
}
