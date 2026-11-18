// Package teams implements the approved commercial, classic Bot Connector
// protocol. It does not accept Emulator, Agent365, sovereign-cloud or arbitrary
// endpoints. The service layer owns tenant authorization and durable attempts.
package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"openfox/internal/platform/safehttp"
)

const (
	MaxActivityBytes = 256 << 10
	MaxTokenBytes    = 16 << 10
	maxResponse      = 256 << 10
	openidURL        = "https://login.botframework.com/v1/.well-known/openidconfiguration"
	keysURL          = "https://login.botframework.com/v1/.well-known/keys"
	connectorIssuer  = "https://api.botframework.com"
	ConnectorScope   = "https://api.botframework.com/.default"
	GraphScope       = "https://graph.microsoft.com/.default"
)

var (
	ErrInvalid      = errors.New("teams: invalid request")
	ErrUnauthorized = errors.New("teams: unauthorized")
	ErrUnsupported  = errors.New("teams: unsupported activity")
	ErrUnavailable  = errors.New("teams: upstream unavailable")
	ErrIncomplete   = errors.New("teams: incomplete response")
	ErrChannelScope = errors.New("teams: channel scope unavailable")
)

// Error contains only fixed diagnostics, never a URL, token or response body.
// Unknown means a non-idempotent request may have reached the platform.
type Error struct {
	Code       string
	Status     int
	RetryAfter time.Duration
	// A missing, malformed or unrepresentable Retry-After must not become a
	// shorter fallback wait. The worker leaves that 429 terminal for diagnosis.
	RetryAfterValid bool
	Unknown         bool
}

func (e *Error) Error() string { return "teams: " + e.Code }

type Client struct {
	http  *http.Client
	now   func() time.Time
	slots chan struct{}
	mu    sync.Mutex
	keys  keyCache
}

func NewClient() *Client { return NewClientWithHTTP(nil) }

// NewClientWithHTTP permits trusted application tests to map the fixed origins
// to an owned TLS peer. There is no configuration/env switch for URLs or TLS.
// Even an injected client cannot enable redirects, cookies or longer requests.
func NewClientWithHTTP(h *http.Client) *Client {
	if h == nil {
		h = safehttp.Client(safehttp.Options{})
	}
	copy := *h
	copy.Timeout = 10 * time.Second
	copy.Jar = nil
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &copy, now: time.Now, slots: make(chan struct{}, 16)}
}

func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

func fixedURL(raw string) (*url.URL, error) {
	u, err := safehttp.CheckURL(raw)
	if err != nil || u.Port() != "" && u.Port() != "443" || u.Hostname() != strings.ToLower(u.Hostname()) {
		return nil, ErrInvalid
	}
	switch u.Hostname() {
	case "login.botframework.com", "login.microsoftonline.com", "graph.microsoft.com", "smba.trafficmanager.net":
		return u, nil
	default:
		return nil, ErrInvalid
	}
}

// ServiceURL normalizes only host/scheme and one terminal slash. The opaque
// path is retained. It is never a user-provided outbound endpoint.
func ServiceURL(raw string) (string, error) {
	if raw != strings.TrimSpace(raw) || len(raw) > 2048 {
		return "", ErrInvalid
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalid
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if _, err = fixedURL(u.String()); err != nil || u.Hostname() != "smba.trafficmanager.net" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(u.Path, "\\") {
		return "", ErrInvalid
	}
	if strings.Contains(u.Path, "//") {
		return "", ErrInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return "", ErrInvalid
		}
	}
	if u.Port() == "443" {
		u.Host = u.Hostname()
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (c *Client) request(ctx context.Context, method, raw, token, contentType string, body []byte, limit int64, write bool) ([]byte, error) {
	if _, err := fixedURL(raw); err != nil {
		return nil, err
	}
	if len(token) > MaxTokenBytes || strings.ContainsAny(token, "\r\n") {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return nil, &Error{Code: "request_cancelled"}
	}
	var mayWrite atomic.Bool
	// WroteHeaders fires before writing a request body; a lost body/response is
	// already ambiguous. WroteRequest also covers a failed partial write.
	trace := &httptrace.ClientTrace{WroteHeaders: func() { mayWrite.Store(true) }, WroteRequest: func(httptrace.WroteRequestInfo) { mayWrite.Store(true) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, raw, bytes.NewReader(body))
	if err != nil {
		return nil, ErrInvalid
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if req.URL.Hostname() == "graph.microsoft.com" {
		req.Header.Set("Prefer", "include-unknown-enum-members")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		return nil, &Error{Code: "transport_failed", Unknown: write && mayWrite.Load()}
	}
	defer res.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(res.Body, limit+1))
	status := res.StatusCode
	if status == http.StatusTooManyRequests {
		delay, valid := retryAfter(res.Header.Get("Retry-After"), c.now())
		return nil, &Error{Code: "rate_limited", Status: status, RetryAfter: delay, RetryAfterValid: valid}
	}
	if status < 200 || status >= 300 {
		code := "upstream_failed"
		switch status {
		case 400:
			code = "request_rejected"
		case 401:
			code = "credentials_rejected"
		case 403:
			code = "permission_denied"
		case 404:
			code = "not_found"
		}
		// Only these explicit rejections prove no successful send. Unexpected
		// status codes (including a redirect) are not a send-idempotency contract.
		knownRejected := status == 400 || status == 401 || status == 403 || status == 404
		return nil, &Error{Code: code, Status: status, Unknown: write && !knownRejected}
	}
	if readErr != nil || int64(len(data)) > limit {
		return nil, &Error{Code: "incomplete_response", Status: status, Unknown: write}
	}
	return data, nil
}

func retryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	const maxDuration = time.Duration(1<<63 - 1)
	digits := value != ""
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil || n > uint64(maxDuration/time.Second) {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		if !t.After(now) {
			return 0, true
		}
		// time.Time.Sub saturates, which would retry before an unrepresentable
		// requested date. Test the bound before performing that subtraction.
		if t.After(now.Add(maxDuration)) {
			return 0, false
		}
		return t.Sub(now), true
	}
	return 0, false
}

// Strict JSON rejects duplicate keys and malformed UTF-8 without rejecting
// future, irrelevant fields in Microsoft's extensible Activity/JWT objects.
func decodeJSON(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := uniqueJSON(d, 0); err != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return ErrInvalid
	}
	return nil
}

func uniqueJSON(d *json.Decoder, depth int) error {
	if depth > 32 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	open, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch open {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
			if e = uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	case '[':
		for d.More() {
			if e := uniqueJSON(d, depth+1); e != nil {
				return e
			}
		}
	default:
		return ErrInvalid
	}
	_, err = d.Token()
	return err
}

func opaque(v string, max int) bool {
	if v == "" || len(v) > max || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func GUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
		} else if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
