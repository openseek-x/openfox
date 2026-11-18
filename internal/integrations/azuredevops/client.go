// Package azuredevops implements the fixed-origin Azure DevOps Services API.
// It performs no automatic retries. Durable grants, scheduling and unknown-write
// reconciliation belong to callers, outside database transactions.
package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	APIVersion       = "7.1"
	ResourceScope    = "https://app.vssps.visualstudio.com/.default"
	MaxResponseBytes = 10 << 20
	MaxCellBytes     = 64 << 10
	MaxCommits       = 100
	MaxItems         = 5000
	RequestTimeout   = 20 * time.Second
)

// Error never includes an upstream body, URL, OAuth code or credential.
// Unknown means a mutating request may have succeeded and must be reconciled.
type Error struct {
	Code       string
	HTTPStatus int
	RetryAfter time.Duration
	Unknown    bool
}

func (e *Error) Error() string { return "azure_devops: " + e.Code }
func (e *Error) Unwrap() error {
	if e.Code == "canceled" {
		return context.Canceled
	}
	if e.Code == "timeout" {
		return context.DeadlineExceeded
	}
	return nil
}
func invalid() error  { return &Error{Code: "invalid_request"} }
func protocol() error { return &Error{Code: "invalid_response"} }

type App struct{ ClientID, ClientSecret string }

// Observe is optional and receives only status/rate information, including a
// successful HTTP 200 Retry-After. Callers delay their NEXT request, not this one.
// Configuration, Transport and Observe must remain immutable during use.
type Client struct {
	App           App
	Transport     http.RoundTripper
	Observe       func(ResponseMeta)
	BeforeRequest func(context.Context) error
}
type ResponseMeta struct {
	HTTPStatus int
	RetryAfter time.Duration
}

func ValidText(v string, limit int) bool {
	return v != "" && len(v) <= limit && utf8.ValidString(v) && strings.TrimSpace(v) == v && !strings.ContainsFunc(v, unicode.IsControl)
}
func ValidToken(v string) bool { return ValidText(v, 32768) }
func ValidUUID(v string) bool {
	if len(v) != 36 || v == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	for i, b := range []byte(v) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return false
			}
			continue
		}
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
			return false
		}
	}
	return true
}
func ValidOrganization(v string) bool {
	if len(v) == 0 || len(v) > 100 || v == "." || v == ".." {
		return false
	}
	for _, b := range []byte(v) {
		if !((b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.') {
			return false
		}
	}
	return true
}
func ValidObjectID(v int64) bool { return v > 0 && v <= 2147483647 }
func ValidSHA(v string) bool {
	if len(v) != 40 {
		return false
	}
	for _, b := range []byte(v) {
		if !((b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') || (b >= '0' && b <= '9')) {
			return false
		}
	}
	return true
}
func uuid(v string) string { return strings.ToLower(v) }
func apiURL(host, path string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("api-version", APIVersion)
	return (&url.URL{Scheme: "https", Host: host, Path: path, RawQuery: q.Encode()}).String()
}
func orgPath(org string) string              { return "/" + org }
func projectPath(org, project string) string { return orgPath(org) + "/" + uuid(project) }
func retryAfter(v string, now time.Time) time.Duration {
	if n, e := strconv.ParseInt(v, 10, 32); e == nil && n >= 0 {
		return time.Duration(min(n, 86400)) * time.Second
	}
	if at, e := http.ParseTime(v); e == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return 0
}

// request is private: all endpoints are constructed by typed methods, never by
// an API caller or a remote resource/pagination URL. Redirects are not followed.
func (c *Client) request(ctx context.Context, method, endpoint, token string, input any, form url.Values, mutating bool, out any) (http.Header, error) {
	if c == nil || ctx == nil {
		return nil, invalid()
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return nil, invalid()
	}
	switch u.Host {
	case "dev.azure.com", "vssps.dev.azure.com", "app.vssps.visualstudio.com", "login.microsoftonline.com":
	default:
		return nil, invalid()
	}
	if (u.Host == "login.microsoftonline.com") != (form != nil) {
		return nil, invalid()
	}
	if form == nil && !ValidToken(token) {
		return nil, invalid()
	}
	var raw []byte
	contentType := "application/json"
	if form != nil {
		raw = []byte(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	} else if input != nil {
		raw, e = json.Marshal(input)
		if e != nil {
			return nil, invalid()
		}
	}
	if c.BeforeRequest != nil {
		if e := c.BeforeRequest(ctx); e != nil {
			return nil, e
		}
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
	if e != nil {
		return nil, invalid()
	}
	req.Header.Set("Accept", "application/json")
	if raw != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Transport: c.Transport, Timeout: RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		code := "unavailable"
		if errors.Is(e, context.DeadlineExceeded) {
			code = "timeout"
		}
		if errors.Is(e, context.Canceled) {
			code = "canceled"
		}
		return nil, &Error{Code: code, Unknown: mutating}
	}
	defer resp.Body.Close()
	meta := ResponseMeta{HTTPStatus: resp.StatusCode, RetryAfter: retryAfter(resp.Header.Get("Retry-After"), time.Now())}
	if c.Observe != nil {
		c.Observe(meta)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if e != nil || len(body) > MaxResponseBytes {
		return resp.Header, &Error{Code: "response_limit_or_read", HTTPStatus: resp.StatusCode, RetryAfter: meta.RetryAfter, Unknown: mutating}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := "upstream_error"
		switch resp.StatusCode {
		case 400:
			code = "upstream_bad_request"
		case 401:
			code = "unauthorized"
		case 403:
			code = "forbidden"
		case 404:
			code = "not_found"
		case 409, 412:
			code = "conflict"
		case 429:
			code = "rate_limited"
		}
		if form != nil {
			var x struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &x)
			switch x.Error {
			case "invalid_grant", "invalid_client", "interaction_required", "consent_required":
				code = x.Error
			}
		}
		return resp.Header, &Error{Code: code, HTTPStatus: resp.StatusCode, RetryAfter: meta.RetryAfter, Unknown: mutating && resp.StatusCode >= 500}
	}
	if out != nil && (!utf8.Valid(body) || json.Unmarshal(body, out) != nil) {
		return resp.Header, &Error{Code: "invalid_response", HTTPStatus: resp.StatusCode, Unknown: mutating}
	}
	return resp.Header, nil
}
