package slack

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var defaultTransport = &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10, IdleConnTimeout: 60 * time.Second}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func failure(kind error, code string) error { return &Error{Kind: kind, Code: code} }
func incomplete(mutation bool) error {
	if mutation {
		return failure(ErrUnknown, "result_unknown")
	}
	return failure(ErrIncomplete, "incomplete_response")
}
func secret(v string) bool {
	return len(v) > 0 && len(v) <= 8192 && strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0 && !strings.ContainsAny(v, "\x00\r\n")
}
func ID(v string) bool {
	return len(v) > 0 && len(v) <= 255 && utf8.ValidString(v) && strings.TrimSpace(v) == v && strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
func timestamp(v string) bool {
	p := strings.Split(v, ".")
	if len(p) != 2 || len(p[0]) < 1 || len(p[0]) > 16 || len(p[1]) < 1 || len(p[1]) > 9 {
		return false
	}
	for _, x := range p {
		for _, b := range x {
			if b < '0' || b > '9' {
				return false
			}
		}
	}
	return true
}
func bounded(v string, n int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= n && !strings.ContainsRune(v, 0)
}
func cursor(v string) bool { return len(v) <= 8192 && !strings.ContainsAny(v, "\x00\r\n") }
func redirect(v string) bool {
	u, e := url.Parse(v)
	return e == nil && len(v) <= 2048 && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && u.RawQuery == ""
}
func scopes(v string) ([]string, error) {
	if len(v) > 8192 {
		return nil, ErrIncomplete
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(s) > 100 || strings.IndexFunc(s, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '_' || r == '.' || r == '-')
		}) >= 0 {
			return nil, ErrIncomplete
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// raw accepts only package-owned fixed paths. No redirect or automatic retry.
func (c *Client) raw(ctx context.Context, method, path, token string, app *AppCredentials, form url.Values, body any, mutation bool) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if token != "" && !secret(token) {
		return nil, ErrInvalid
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\\") {
		return nil, ErrInvalid
	}
	var payload []byte
	contentType := ""
	if body != nil {
		var e error
		payload, e = json.Marshal(body)
		if e != nil {
			return nil, ErrInvalid
		}
		contentType = "application/json; charset=utf-8"
	} else if form != nil && method != "GET" {
		payload = []byte(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	if len(payload) > MaxResponseBytes {
		return nil, ErrInvalid
	}
	endpoint := Origin + path
	if method == "GET" && len(form) > 0 {
		endpoint += "?" + form.Encode()
	}
	callctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(callctx, method, endpoint, bytes.NewReader(payload))
	if e != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if app != nil {
		if !ID(app.ClientID) || !secret(app.ClientSecret) {
			return nil, ErrInvalid
		}
		req.SetBasicAuth(app.ClientID, app.ClientSecret)
	}
	transport := http.RoundTripper(defaultTransport)
	if c != nil && c.Transport != nil {
		transport = c.Transport
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		if mutation {
			return nil, failure(ErrUnknown, "result_unknown")
		}
		return nil, failure(errors.Join(ErrUnavailable, callctx.Err()), "request_failed")
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if e != nil || len(data) > MaxResponseBytes {
		return nil, incomplete(mutation)
	}
	if resp.StatusCode == 429 {
		seconds := int64(1) // Missing header: local fallback, not provider advice.
		if value := strings.TrimSpace(resp.Header.Get("Retry-After")); value != "" {
			for _, digit := range value {
				if digit < '0' || digit > '9' {
					return nil, failure(ErrInvalid, "retry_after_invalid")
				}
			}
			var parseErr error
			seconds, parseErr = strconv.ParseInt(value, 10, 64)
			if parseErr != nil || seconds < 1 || seconds > int64((1<<63-1)/time.Second) {
				return nil, failure(ErrInvalid, "retry_after_invalid")
			}
		}
		// Never shorten a provider delay. The worker may stop on its own
		// budget; it must not reschedule earlier than this exact duration.
		return nil, &Error{Kind: ErrRateLimited, Code: "rate_limited", RetryAfter: time.Duration(seconds) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 401 {
			return nil, failure(ErrAuth, "invalid_auth")
		}
		if resp.StatusCode == 403 {
			return nil, failure(ErrForbidden, "access_denied")
		}
		if resp.StatusCode == 400 && jsonObject(data) {
			var rejected struct {
				OK   *bool  `json:"ok"`
				Code string `json:"error"`
			}
			if json.Unmarshal(data, &rejected) == nil && rejected.OK != nil && !*rejected.OK {
				return nil, slackError(rejected.Code, mutation)
			}
		}
		if mutation {
			return nil, failure(ErrUnknown, "result_unknown")
		}
		if resp.StatusCode == 401 {
			return nil, failure(ErrAuth, "invalid_auth")
		}
		if resp.StatusCode == 403 {
			return nil, failure(ErrForbidden, "access_denied")
		}
		return nil, failure(ErrUnavailable, "request_failed")
	}
	if !jsonObject(data) {
		return nil, incomplete(mutation)
	}
	return data, nil
}
func (c *Client) api(ctx context.Context, method, path, token string, app *AppCredentials, form url.Values, body any, mutation bool, out any) error {
	if app == nil && path != "tooling.tokens.rotate" && !secret(token) {
		return ErrInvalid
	}
	data, e := c.raw(ctx, method, "/api/"+path, token, app, form, body, mutation)
	if e != nil {
		return e
	}
	var envelope struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.OK == nil {
		return incomplete(mutation)
	}
	if !*envelope.OK {
		return slackError(envelope.Error, mutation)
	}
	if envelope.Error != "" || json.Unmarshal(data, out) != nil {
		return incomplete(mutation)
	}
	return nil
}
func slackError(code string, mutation bool) error {
	switch code {
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive", "invalid_refresh_token", "invalid_client_id", "bad_client_secret", "invalid_client", "invalid_code", "bad_verification_code", "code_already_used":
		return failure(ErrAuth, code)
	case "missing_scope", "not_allowed_token_type", "access_denied", "restricted_action", "not_in_channel", "is_archived", "ekm_access_denied", "app_not_owned_by_manager_app", "team_access_not_granted", "enterprise_is_restricted":
		return failure(ErrForbidden, code)
	case "channel_not_found", "thread_not_found", "user_not_found", "app_not_found", "message_not_found":
		return failure(ErrNotFound, code)
	case "ratelimited", "rate_limited":
		return &Error{Kind: ErrRateLimited, Code: "rate_limited", RetryAfter: time.Second}
	case "invalid_arguments", "invalid_arg_name", "invalid_array_arg", "invalid_app_id", "invalid_manifest", "invalid_app", "invalid_name", "invalid_redirect_uri", "bad_redirect_uri", "no_text", "msg_too_long", "cannot_unfurl_url", "invalid_metadata_format", "invalid_metadata_event_type", "too_many_attachments":
		return failure(ErrInvalid, code)
	default:
		if mutation {
			return failure(ErrUnknown, "result_unknown")
		}
		return failure(ErrUnavailable, "request_failed")
	}
}

// Duplicate JSON keys and excessive nesting are ambiguous for authentication
// claims and receipts. Reject them instead of relying on last-key-wins decoding.
func jsonObject(data []byte) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	first, e := d.Token()
	if e != nil || first != json.Delim('{') {
		return false
	}
	if !jsonContainer(d, '{', 0) {
		return false
	}
	_, e = d.Token()
	return e == io.EOF
}
func jsonContainer(d *json.Decoder, kind byte, depth int) bool {
	if depth > 32 {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			key, e := d.Token()
			s, ok := key.(string)
			if e != nil || !ok || seen[s] {
				return false
			}
			seen[s] = true
		}
		v, e := d.Token()
		if e != nil {
			return false
		}
		if x, ok := v.(json.Delim); ok {
			if x != '{' && x != '[' || !jsonContainer(d, byte(x), depth+1) {
				return false
			}
		}
	}
	end, e := d.Token()
	if e != nil {
		return false
	}
	return kind == '{' && end == json.Delim('}') || kind == '[' && end == json.Delim(']')
}
func requiredArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}
