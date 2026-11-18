// Package zohobugtracker implements the fixed-origin OAuth and read-only US BugTracker REST API used
// by project imports. Scheduling, retries and grant ownership belong to callers.
package zohobugtracker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	AccountsURL       = "https://accounts.zoho.com"
	APIURL            = "https://bugtracker.zoho.com/restapi"
	OrganizationScope = "ZohoBugTracker.portals.READ,ZohoBugTracker.projects.READ,ZohoBugTracker.users.READ,ZohoBugTracker.bugs.READ"
	IdentityScope     = "ZohoBugTracker.portals.READ"
	MaxTokenBytes     = 4096
	MaxCellBytes      = 64 << 10
	maxResponseBytes  = 8 << 20
	requestTimeout    = 10 * time.Second
	PageSize          = 100
)

const (
	CodeInvalidRequest = "invalid_request"
	CodeNotConfigured  = "not_configured"
	CodeUnauthorized   = "unauthorized"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeRateLimited    = "rate_limited"
	CodeInvalidGrant   = "invalid_grant"
	CodeBadRequest     = "upstream_bad_request"
	CodeUnavailable    = "upstream_unavailable"
	CodeProtocol       = "invalid_response"
	CodeLimit          = "limit_exceeded"
	CodeTimeout        = "timeout"
	CodeCanceled       = "canceled"
)

// Error deliberately carries no upstream response, URL, token or OAuth code.
// On 429, RetryAfterValid=false means automatic scheduling must stop. Missing
// Retry-After uses the published 30-minute REST lock window; no delay is truncated.
type Error struct {
	Code            string
	HTTPStatus      int
	RetryAfter      time.Duration
	RetryAfterValid bool
}

func (e *Error) Error() string { return "zoho_bugtracker: " + e.Code }

func (e *Error) Unwrap() error {
	if e.Code == CodeTimeout {
		return context.DeadlineExceeded
	}
	if e.Code == CodeCanceled {
		return context.Canceled
	}
	return nil
}

type App struct {
	ClientID     string
	ClientSecret string
}

// Client is safe for concurrent use when its configuration is immutable.
// Transport is a server-owned test seam. Request URLs always use the fixed AccountsURL/APIURL origins.
type Client struct {
	App       App
	Transport http.RoundTripper
}

type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresIn    int64     `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ValidID preserves the documented decimal identifiers without float conversion.
func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] == '0' {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func ValidToken(token string) bool { return validOpaque(token, MaxTokenBytes) }

func validOpaque(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (c *Client) configured() bool {
	return c != nil && validOpaque(c.App.ClientID, 255) && validOpaque(c.App.ClientSecret, MaxTokenBytes)
}

func validRedirect(raw string) bool {
	if !validOpaque(raw, 2048) {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

func validVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	return true
}

func (c *Client) AuthorizeURL(state, challenge, redirect string, identity bool) (string, error) {
	if !c.configured() {
		return "", &Error{Code: CodeNotConfigured}
	}
	digest, err := base64.RawURLEncoding.Strict().DecodeString(challenge)
	if err != nil || len(digest) != 32 || !validOpaque(state, 512) || !validRedirect(redirect) {
		return "", &Error{Code: CodeInvalidRequest}
	}
	scope := OrganizationScope
	if identity {
		scope = IdentityScope
	}
	query := url.Values{"client_id": {c.App.ClientID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"state": {state}, "code_challenge_method": {"S256"}, "code_challenge": {challenge}, "scope": {scope}}
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	if identity {
		query.Set("access_type", "online")
	}
	return AccountsURL + "/oauth/v2/auth?" + query.Encode(), nil
}

func (c *Client) Exchange(ctx context.Context, code, verifier, redirect string) (Token, error) {
	if !validOpaque(code, MaxTokenBytes) || !validVerifier(verifier) || !validRedirect(redirect) {
		return Token{}, &Error{Code: CodeInvalidRequest}
	}
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}})
}

func (c *Client) Refresh(ctx context.Context, refresh string) (Token, error) {
	if !ValidToken(refresh) {
		return Token{}, &Error{Code: CodeInvalidRequest}
	}
	return c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}})
}

func (c *Client) token(ctx context.Context, form url.Values) (Token, error) {
	if !c.configured() {
		return Token{}, &Error{Code: CodeNotConfigured}
	}
	form.Set("client_id", c.App.ClientID)
	form.Set("client_secret", c.App.ClientSecret)
	started := time.Now()
	body, err := c.request(ctx, http.MethodPost, AccountsURL+"/oauth/v2/token", "", form)
	if err != nil {
		return Token{}, err
	}
	var wire struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
		ExpiresIn    *int64 `json:"expires_in"`
		Error        string `json:"error"`
	}
	if json.Unmarshal(body, &wire) == nil && wire.Error != "" {
		code := CodeProtocol
		switch wire.Error {
		case "invalid_grant", "invalid_code":
			code = CodeInvalidGrant
		case "invalid_client":
			code = CodeNotConfigured
		}
		return Token{}, &Error{Code: code, HTTPStatus: http.StatusOK}
	}
	if json.Unmarshal(body, &wire) != nil || !ValidToken(wire.AccessToken) ||
		!strings.EqualFold(wire.TokenType, "bearer") || wire.ExpiresIn == nil || *wire.ExpiresIn <= 0 ||
		*wire.ExpiresIn > 86400 || len(wire.Scope) > MaxTokenBytes ||
		(wire.RefreshToken != "" && !ValidToken(wire.RefreshToken)) {
		return Token{}, &Error{Code: CodeProtocol, HTTPStatus: http.StatusOK}
	}
	return Token{AccessToken: wire.AccessToken, RefreshToken: wire.RefreshToken, TokenType: wire.TokenType,
		Scope: wire.Scope, ExpiresIn: *wire.ExpiresIn, ExpiresAt: started.Add(time.Duration(*wire.ExpiresIn) * time.Second)}, nil
}

func (c *Client) request(ctx context.Context, method, endpoint, token string, form url.Values) ([]byte, error) {
	if c == nil || ctx == nil {
		return nil, &Error{Code: CodeInvalidRequest}
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return nil, &Error{Code: CodeInvalidRequest}
	}
	allowed := method == http.MethodPost && u.Host == "accounts.zoho.com" && u.Path == "/oauth/v2/token" && u.RawQuery == "" && form != nil && token == ""
	allowed = allowed || method == http.MethodGet && u.Host == "bugtracker.zoho.com" && strings.HasPrefix(u.Path, "/restapi/") && form == nil && ValidToken(token)
	if !allowed {
		return nil, &Error{Code: CodeInvalidRequest}
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, &Error{Code: CodeInvalidRequest}
	}
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Zoho-oauthtoken "+token)
	}
	client := http.Client{Transport: c.Transport, Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, transportError(err)
	}
	if len(data) > maxResponseBytes {
		return nil, &Error{Code: CodeLimit, HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		code := CodeUnavailable
		switch resp.StatusCode {
		case http.StatusBadRequest:
			code = CodeBadRequest
		case http.StatusUnauthorized:
			code = CodeUnauthorized
		case http.StatusForbidden:
			code = CodeForbidden
		case http.StatusNotFound:
			code = CodeNotFound
		case http.StatusTooManyRequests:
			code = CodeRateLimited
		}
		// Only a documented, finite OAuth code is interpreted. Upstream error
		// descriptions and resource error messages are never retained.
		if method == http.MethodPost && u.Path == "/oauth/v2/token" && resp.StatusCode == http.StatusBadRequest {
			var failure struct {
				Error string `json:"error"`
			}
			if json.Unmarshal(data, &failure) == nil && failure.Error == "invalid_grant" {
				code = CodeInvalidGrant
			}
		}
		delay, valid := retryAfter(resp.Header.Get("Retry-After"), time.Now())
		return nil, &Error{Code: code, HTTPStatus: resp.StatusCode, RetryAfter: delay, RetryAfterValid: valid}
	}
	if !utf8.Valid(data) {
		return nil, &Error{Code: CodeProtocol, HTTPStatus: resp.StatusCode}
	}
	return data, nil
}

func transportError(err error) error {
	code := CodeUnavailable
	var network net.Error
	if errors.Is(err, context.Canceled) {
		code = CodeCanceled
	} else if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		code = CodeTimeout
	}
	return &Error{Code: code}
}

func retryAfter(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 30 * time.Minute, true
	}
	if seconds, err := strconv.ParseUint(raw, 10, 64); err == nil {
		if seconds > uint64((time.Duration(1<<63-1))/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		if !deadline.After(now) {
			return 0, true
		}
		max := time.Duration(1<<63 - 1)
		if deadline.After(now.Add(max)) {
			return 0, false
		}
		return deadline.Sub(now), true
	}
	return 0, false
}

// ScopeMatches validates a returned optional scope without broadening requests.
func ScopeMatches(scope string, identity bool) bool {
	if scope == "" {
		return true
	}
	have := map[string]bool{}
	for _, s := range strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' }) {
		have[s] = true
	}
	want := OrganizationScope
	if identity {
		want = IdentityScope
	}
	for _, s := range strings.Split(want, ",") {
		if !have[s] {
			return false
		}
	}
	return true
}
