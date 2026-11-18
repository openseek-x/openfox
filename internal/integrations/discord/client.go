package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var defaultTransport = &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 7 * time.Second, MaxResponseHeaderBytes: 64 << 10, IdleConnTimeout: 60 * time.Second, MaxIdleConns: 20, MaxIdleConnsPerHost: 4}

const userAgent = "DiscordBot (https://github.com/openseek-x/openfox, 1) OpenFox"

func (c *Client) httpClient(timeout time.Duration) *http.Client {
	transport := http.RoundTripper(defaultTransport)
	if c != nil && c.transport != nil {
		transport = c.transport
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type RateLimit struct {
	Route, Major, Bucket, Scope string
	Remaining                   int64
	ResetAfter                  time.Duration
	Global                      bool
}
type rateObserverKey struct{}

// WithRateObserver exposes sanitized response bucket facts to the durable
// caller. The callback must be bounded and must not log the request/token.
func WithRateObserver(ctx context.Context, observe func(RateLimit)) context.Context {
	return context.WithValue(ctx, rateObserverKey{}, observe)
}
func safeHeader(s string) string {
	if len(s) > 128 {
		return ""
	}
	for _, r := range s {
		if r < 33 || r > 126 {
			return ""
		}
	}
	return s
}
func decimalDuration(s string) (time.Duration, bool) {
	if s == "" || len(s) > 40 || strings.ContainsAny(s, "eE+-/ ") {
		return 0, false
	}
	for _, r := range s {
		if r != '.' && (r < '0' || r > '9') {
			return 0, false
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() <= 0 {
		return 0, false
	}
	r.Mul(r, new(big.Rat).SetInt64(int64(time.Second)))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() || q.Sign() <= 0 {
		return 0, false
	}
	return time.Duration(q.Int64()), true
}
func (c *Client) do(ctx context.Context, token, method, path, route, major string, body any) ([]byte, error) {
	if !tokenValid(token) {
		return nil, ErrInvalid
	}
	if ctx.Err() != nil {
		return nil, failure(ErrUnavailable, "cancelled_before_request")
	}
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil || len(data) > 16<<10 {
			return nil, ErrInvalid
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, APIOrigin+"/api/v10"+path, bytes.NewReader(data))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	write := method != "GET"
	resp, err := c.httpClient(10 * time.Second).Do(req)
	if err != nil {
		if write {
			return nil, failure(ErrUnknown, "delivery_unknown")
		}
		return nil, failure(ErrUnavailable, "transport_unavailable")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil || len(raw) > MaxResponseBytes {
		if write {
			return nil, failure(ErrUnknown, "delivery_unknown")
		}
		return nil, failure(ErrResponse, "response_capacity")
	}
	meta := RateLimit{Route: route, Major: major, Bucket: safeHeader(resp.Header.Get("X-RateLimit-Bucket")), Scope: safeHeader(resp.Header.Get("X-RateLimit-Scope")), Remaining: -1, Global: resp.Header.Get("X-RateLimit-Global") == "true"}
	if n, e := strconv.ParseInt(resp.Header.Get("X-RateLimit-Remaining"), 10, 64); e == nil && n >= 0 {
		meta.Remaining = n
	}
	meta.ResetAfter, _ = decimalDuration(resp.Header.Get("X-RateLimit-Reset-After"))
	if observe, ok := ctx.Value(rateObserverKey{}).(func(RateLimit)); ok && observe != nil {
		observe(meta)
	}
	if resp.StatusCode == http.StatusOK {
		return raw, nil
	}
	if resp.StatusCode == 429 {
		var v struct {
			Retry  json.RawMessage `json:"retry_after"`
			Global bool            `json:"global"`
		}
		valid := decodeObject(raw, &v) == nil
		delay, ok := decimalDuration(string(v.Retry))
		ok = ok && valid
		if !ok {
			delay, ok = decimalDuration(resp.Header.Get("Retry-After"))
		}
		code := "rate_limited"
		if !ok {
			code = "retry_after_invalid"
		}
		return nil, &Error{Kind: ErrRateLimited, Status: 429, Code: code, RetryAfter: delay, Global: meta.Global || v.Global, Bucket: meta.Bucket, Scope: meta.Scope}
	}
	if write && (resp.StatusCode < 400 || resp.StatusCode >= 500) {
		return nil, &Error{Kind: ErrUnknown, Status: resp.StatusCode, Code: "delivery_unknown"}
	}
	kind, code := ErrResponse, "request_rejected"
	switch resp.StatusCode {
	case 401:
		kind, code = ErrAuth, "credentials_invalid"
	case 403:
		kind, code = ErrForbidden, "permission_denied"
	case 404:
		kind, code = ErrNotFound, "not_found"
	default:
		if resp.StatusCode >= 500 {
			kind, code = ErrUnavailable, "server_unavailable"
		}
	}
	return nil, &Error{Kind: kind, Status: resp.StatusCode, Code: code}
}
func decodeObject(raw []byte, target any) error {
	if validateJSON(raw) != nil || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || json.Unmarshal(raw, target) != nil {
		return ErrResponse
	}
	return nil
}
func decodeArray[T any](raw []byte, limit int) ([]T, error) {
	if validateJSON(raw) != nil || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '[' {
		return nil, ErrResponse
	}
	var rows []T
	if json.Unmarshal(raw, &rows) != nil || len(rows) > limit {
		return nil, ErrResponse
	}
	return rows, nil
}

// Reject ambiguous duplicate keys, invalid UTF-8, trailing values and excessive
// nesting before Go's decoder can silently replace one field with another.
func validateJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return ErrResponse
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return ErrResponse
		}
		t, e := d.Token()
		if e != nil {
			return ErrResponse
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				s, ok := k.(string)
				if e != nil || !ok || seen[s] {
					return ErrResponse
				}
				seen[s] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			close, e := d.Token()
			if e != nil || close != json.Delim('}') {
				return ErrResponse
			}
		case '[':
			for d.More() {
				if e = value(depth + 1); e != nil {
					return e
				}
			}
			close, e := d.Token()
			if e != nil || close != json.Delim(']') {
				return ErrResponse
			}
		default:
			return ErrResponse
		}
		return nil
	}
	if value(0) != nil {
		return ErrResponse
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrResponse
	}
	return nil
}
func query(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}
func compareID(a, b ID) int {
	x, _ := strconv.ParseUint(string(a), 10, 64)
	y, _ := strconv.ParseUint(string(b), 10, 64)
	if x < y {
		return -1
	}
	if x > y {
		return 1
	}
	return 0
}
