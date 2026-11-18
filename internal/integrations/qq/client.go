package qq

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var transport = &http.Transport{DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10, IdleConnTimeout: 60 * time.Second}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
func failure(kind error, reason string, status int, code int64) error {
	return &Error{Kind: kind, Reason: reason, Status: status, PlatformCode: code}
}
func uncertain(mutation bool, reason string, status int) error {
	if mutation {
		return failure(ErrUnknown, reason, status, 0)
	}
	return failure(ErrUnavailable, reason, status, 0)
}

// No automatic retries or redirects. A write transport failure is unknown even
// if a caller believes no response means QQ did not process the request.
func (c *Client) call(ctx context.Context, path, token string, body any, mutation bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil || len(raw) > MaxBodyBytes {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Origin+path, bytes.NewReader(raw))
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		if !secret(token) {
			return nil, ErrInvalid
		}
		req.Header.Set("Authorization", "QQBot "+token)
	}
	t := http.RoundTripper(transport)
	if c != nil && c.Transport != nil {
		t = c.Transport
	}
	h := http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := h.Do(req)
	if err != nil {
		return nil, uncertain(mutation, "request_failed", 0)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil || len(data) > MaxBodyBytes {
		return nil, uncertain(mutation, "incomplete_response", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		delay, err := retryAfter(resp.Header.Values("Retry-After"), c.now())
		if err != nil {
			return nil, err
		}
		return nil, &Error{Kind: ErrRateLimited, Reason: "rate_limited", Status: resp.StatusCode, RetryAfter: delay}
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, failure(ErrAuthentication, "authentication_rejected", resp.StatusCode, 0)
	}
	if resp.StatusCode >= 500 || resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, uncertain(mutation, "upstream_status", resp.StatusCode)
	}
	if !object(data) {
		return nil, uncertain(mutation, "invalid_response", resp.StatusCode)
	}
	var envelope struct {
		Code    *int64 `json:"code"`
		ErrCode *int64 `json:"err_code"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return nil, uncertain(mutation, "invalid_response", resp.StatusCode)
	}
	if envelope.Code != nil && envelope.ErrCode != nil && *envelope.Code != *envelope.ErrCode {
		return nil, uncertain(mutation, "conflicting_error_codes", resp.StatusCode)
	}
	var code int64
	if envelope.Code != nil {
		code = *envelope.Code
	}
	if envelope.ErrCode != nil {
		code = *envelope.ErrCode
	}
	if code != 0 {
		return nil, platformFailure(code, resp.StatusCode, mutation)
	}
	// Other 2xx statuses are not evidence of the documented completed response.
	if resp.StatusCode != 200 {
		return nil, uncertain(mutation, "unexpected_status", resp.StatusCode)
	}
	return data, nil
}

// QQ documents expires_in as a number but demonstrates a decimal string. Both
// are accepted strictly, without coercing fractions, exponent notation or null.
func seconds(raw json.RawMessage) (int64, error) {
	v := string(raw)
	if len(v) > 1 && v[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, ErrInvalid
		}
		v = s
	}
	if v == "" {
		return 0, ErrInvalid
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, ErrInvalid
		}
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 1 || n > int64((1<<63-1)/time.Second) {
		return 0, ErrInvalid
	}
	return n, nil
}
func (c *Client) AccessToken(ctx context.Context, app Credentials) (Token, error) {
	if !app.Valid() {
		return Token{}, ErrInvalid
	}
	data, e := c.call(ctx, "/app/getAppAccessToken", "", map[string]string{"appId": app.AppID, "clientSecret": app.AppSecret}, false)
	if e != nil {
		return Token{}, e
	}
	var v struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if json.Unmarshal(data, &v) != nil || !secret(v.AccessToken) {
		return Token{}, failure(ErrUnavailable, "invalid_token_response", 200, 0)
	}
	n, e := seconds(v.ExpiresIn)
	if e != nil {
		return Token{}, failure(ErrUnavailable, "invalid_token_expiry", 200, 0)
	}
	return Token{AccessToken: v.AccessToken, ExpiresAt: c.now().Add(time.Duration(n) * time.Second)}, nil
}
func (c *Client) SendText(ctx context.Context, token string, in Reply) (SentMessage, error) {
	if !secret(token) || !ID(in.DestinationID) || in.DestinationID == "." || in.DestinationID == ".." || !ID(in.MessageID) || !text(in.Text) || in.Sequence != 1 {
		return SentMessage{}, ErrInvalid
	}
	path := "/v2/users/"
	if in.Scope == Group {
		path = "/v2/groups/"
	} else if in.Scope != C2C {
		return SentMessage{}, ErrInvalid
	}
	data, e := c.call(ctx, path+url.PathEscape(in.DestinationID)+"/messages", token, map[string]any{"msg_type": 0, "content": in.Text, "msg_id": in.MessageID, "msg_seq": in.Sequence}, true)
	if e != nil {
		return SentMessage{}, e
	}
	var v struct {
		ID        string `json:"id"`
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(data, &v) != nil || !ID(v.ID) {
		return SentMessage{}, failure(ErrUnknown, "invalid_send_receipt", 200, 0)
	}
	ts, e := time.Parse(time.RFC3339Nano, v.Timestamp)
	if e != nil {
		return SentMessage{}, failure(ErrUnknown, "invalid_send_timestamp", 200, 0)
	}
	return SentMessage{ID: v.ID, Timestamp: ts}, nil
}
func platformFailure(code int64, status int, mutation bool) error {
	if !mutation {
		switch code {
		case 100007, 100016, 10004:
			return failure(ErrAuthentication, "credentials_rejected", status, code)
		case 100001:
			return &Error{Kind: ErrRateLimited, Reason: "rate_limited", Status: status, PlatformCode: code, RetryAfter: time.Second}
		}
		return failure(ErrUnavailable, "token_request_failed", status, code)
	}
	switch code {
	case 22006, 50059, 304004, 304036, 304061, 304062, 304064, 304080, 304103, 305007, 340067, 340069, 40034005, 40034006:
		return failure(ErrRejected, "message_rejected", status, code)
	default:
		// In particular, repeated msg_id+msg_seq is not a recovered send receipt.
		return failure(ErrUnknown, "delivery_unknown", status, code)
	}
}
func retryAfter(values []string, now time.Time) (time.Duration, error) {
	if len(values) == 0 {
		return time.Second, nil
	} // Local fallback, not QQ advice.
	if len(values) != 1 {
		return 0, failure(ErrInvalid, "invalid_retry_after", 429, 0)
	}
	v := strings.TrimSpace(values[0])
	if n, e := seconds(json.RawMessage(v)); e == nil {
		return time.Duration(n) * time.Second, nil
	}
	// HTTP Retry-After also defines a date. Honor it without shortening it.
	if t, e := http.ParseTime(v); e == nil && t.After(now) {
		return t.Sub(now), nil
	}
	return 0, failure(ErrInvalid, "invalid_retry_after", 429, 0)
}
