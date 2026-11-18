// Package gongfeng implements the approved Tencent partner edition REST v3
// contract. It is deliberately independent of GitLab's v4/OAuth implementation.
package gongfeng

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
	"time"

	"openfox/internal/integrations/gitapi"
	"openfox/internal/platform/safehttp"
)

const (
	Origin         = "https://git.tencent.com"
	API            = Origin + "/api/v3"
	PageSize       = 100
	MaxPages       = 50
	requestTimeout = 15 * time.Second
	connectTimeout = 5 * time.Second
	maxResponse    = 8 << 20
)

var (
	ErrForbidden    = errors.New("gongfeng: target permission denied")
	ErrIncomplete   = errors.New("gongfeng: pagination incomplete or inconsistent")
	ErrInvalid      = errors.New("gongfeng: invalid request or response")
	ErrConfidential = errors.New("gongfeng: confidential issue excluded")
)

// HTTP is injectable by the server's controlled integration tests, never by an
// API caller. The origin is fixed even with an injected transport. Redirects
// are always disabled for the individual request's client copy.
type Client struct {
	HTTP  *http.Client
	slots chan struct{}
}

func New() *Client {
	h := safehttp.Client(safehttp.Options{})
	transport := h.Transport.(*http.Transport)
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, connectTimeout)
		defer cancel()
		return dial(ctx, network, address)
	}
	transport.TLSHandshakeTimeout = connectTimeout
	transport.ResponseHeaderTimeout = requestTimeout
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{HTTP: h, slots: make(chan struct{}, 2)}
}

func (*Client) SupportsAssignees() bool { return false }

// The fixed PAT-only provider has no OAuth or refresh endpoint.
func (*Client) AuthorizeURL(string, string, string) string { return "" }
func (*Client) Exchange(context.Context, string, string, string) (gitapi.Token, error) {
	return gitapi.Token{}, gitapi.ErrExchangeFailed
}
func (*Client) Refresh(context.Context, string) (gitapi.Token, error) {
	return gitapi.Token{}, gitapi.ErrRefreshUnsupported
}

func validToken(token string) bool {
	if len(token) == 0 || len(token) > 8192 {
		return false
	}
	for _, c := range token {
		if c <= 32 || c >= 127 {
			return false
		}
	}
	return true
}

func retryAfter(value string, now time.Time) time.Time {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		if seconds > 3600 {
			seconds = 3600
		}
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if parsed, err := http.ParseTime(value); err == nil {
		if parsed.Before(now) {
			return now
		}
		if parsed.After(now.Add(time.Hour)) {
			return now.Add(time.Hour)
		}
		return parsed
	}
	return time.Time{}
}

// No retry occurs here. A mutating request with an ambiguous outcome must be
// reconciled by its durable work item before another mutation can be sent.
func (c *Client) do(ctx context.Context, method, path, token string, query url.Values, payload any) (http.Header, []byte, error) {
	if c == nil || c.HTTP == nil || !validToken(token) || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\\x00") {
		return nil, nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if c.slots != nil {
		select {
		case c.slots <- struct{}{}:
			defer func() { <-c.slots }()
		case <-ctx.Done():
			return nil, nil, gitapi.ErrRequestFailed
		}
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, ErrInvalid
		}
		body = bytes.NewReader(b)
	}
	target := API + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, nil, ErrInvalid
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OpenFox")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h := *c.HTTP
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	h.Jar = nil
	if !takeRequest(ctx) {
		return nil, nil, ErrBudgetExhausted
	}
	response, err := h.Do(req)
	mutating := method != http.MethodGet
	if err != nil {
		if mutating {
			return nil, nil, gitapi.ErrUnknownResult
		}
		return nil, nil, gitapi.ErrRequestFailed
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusLocked:
		return response.Header, nil, gitapi.ErrTokenRevoked
	case http.StatusForbidden:
		return response.Header, nil, ErrForbidden
	case http.StatusNotFound:
		return response.Header, nil, gitapi.ErrNotFound
	case http.StatusTooManyRequests:
		return response.Header, nil, &gitapi.ErrRateLimited{ResetAt: retryAfter(response.Header.Get("Retry-After"), time.Now())}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if mutating && (response.StatusCode >= 500 || response.StatusCode < 400) {
			return response.Header, nil, gitapi.ErrUnknownResult
		}
		return response.Header, nil, gitapi.ErrRequestFailed
	}
	wantStatus := http.StatusOK
	if method == http.MethodPost && !strings.HasSuffix(path, "/activate") {
		wantStatus = http.StatusCreated
	}
	if response.StatusCode != wantStatus {
		if mutating {
			return response.Header, nil, gitapi.ErrUnknownResult
		}
		return response.Header, nil, gitapi.ErrRequestFailed
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		if mutating {
			return response.Header, nil, gitapi.ErrUnknownResult
		}
		return response.Header, nil, gitapi.ErrRequestFailed
	}
	return response.Header, data, nil
}

func decode(data []byte, target any, mutation bool) error {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, target) != nil {
		if mutation {
			return gitapi.ErrUnknownResult
		}
		return ErrInvalid
	}
	return nil
}

func numericID(id int64) (string, error) {
	if id <= 0 {
		return "", ErrInvalid
	}
	return strconv.FormatInt(id, 10), nil
}
func projectPath(id int64) (string, error) {
	value, err := numericID(id)
	return "/projects/" + value, err
}

type Page[T any] struct {
	Items []T
	Next  int
}

func readPage[T any](c *Client, ctx context.Context, path, token string, query url.Values, page, maxPages int) (Page[T], error) {
	var result Page[T]
	if page < 1 || page > maxPages {
		return result, ErrIncomplete
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(PageSize))
	header, data, err := c.do(ctx, http.MethodGet, path, token, query, nil)
	if err != nil {
		return result, err
	}
	if err = decode(data, &result.Items, false); err != nil {
		return result, err
	}
	if len(result.Items) > PageSize {
		return Page[T]{}, ErrIncomplete
	}
	for name, want := range map[string]int{"X-Page": page, "X-Per-Page": PageSize} {
		if values, exists := header[http.CanonicalHeaderKey(name)]; exists {
			if len(values) != 1 {
				return Page[T]{}, ErrIncomplete
			}
			value, e := strconv.Atoi(values[0])
			if e != nil || value != want {
				return Page[T]{}, ErrIncomplete
			}
		}
	}
	nextKnown := false
	if values, exists := header["X-Next-Page"]; exists {
		nextKnown = true
		if len(values) != 1 {
			return Page[T]{}, ErrIncomplete
		}
		if values[0] != "" {
			next, e := strconv.Atoi(values[0])
			if e != nil || next != page+1 || len(result.Items) == 0 {
				return Page[T]{}, ErrIncomplete
			}
			result.Next = next
		}
	} else if len(result.Items) == PageSize {
		result.Next = page + 1
	}
	// Pagination metadata is optional, but contradictory supplied metadata is
	// never evidence that an UNKNOWN reconciliation has exhausted candidates.
	if values, exists := header["X-Total-Pages"]; exists {
		if len(values) != 1 {
			return Page[T]{}, ErrIncomplete
		}
		total, err := strconv.Atoi(values[0])
		if err != nil || total < 0 || total == 0 && (page != 1 || len(result.Items) != 0) || total > 0 && page > total {
			return Page[T]{}, ErrIncomplete
		}
		wantNext := 0
		if page < total {
			wantNext = page + 1
		}
		if nextKnown && result.Next != wantNext {
			return Page[T]{}, ErrIncomplete
		}
		result.Next, nextKnown = wantNext, true
	}
	if values, exists := header["X-Total"]; exists {
		if len(values) != 1 {
			return Page[T]{}, ErrIncomplete
		}
		total, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || total < 0 {
			return Page[T]{}, ErrIncomplete
		}
		remaining := total - int64(page-1)*PageSize
		expected := max(int64(0), min(int64(PageSize), remaining))
		wantNext := 0
		if remaining > PageSize {
			wantNext = page + 1
		}
		if int64(len(result.Items)) != expected || nextKnown && result.Next != wantNext {
			return Page[T]{}, ErrIncomplete
		}
		result.Next, nextKnown = wantNext, true
	}
	if result.Next > maxPages {
		return Page[T]{}, ErrIncomplete
	}
	return result, nil
}
