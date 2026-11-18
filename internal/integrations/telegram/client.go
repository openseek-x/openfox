package telegram

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var defaultTransport = &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 64 << 10, IdleConnTimeout: 60 * time.Second}

// Token is deliberately not stored in Client. Restrict URL path characters
// before construction; do not infer the authenticated bot identity from it.
func validToken(token string) bool {
	if len(token) > 512 {
		return false
	}
	parts := strings.Split(token, ":")
	if len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 16 || len(parts[1]) < 1 || len(parts[1]) > 256 {
		return false
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, c := range parts[1] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (c *Client) call(ctx context.Context, token, method string, input any, write bool) (json.RawMessage, error) {
	if !validToken(token) {
		return nil, ErrInvalid
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	switch method {
	case "getMe", "getWebhookInfo", "setWebhook", "deleteWebhook", "getChat", "getChatMember", "sendMessage":
	default:
		return nil, ErrInvalid
	}
	raw, e := json.Marshal(input)
	if e != nil || len(raw) > MaxBodyBytes {
		return nil, ErrInvalid
	}
	callctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(callctx, http.MethodPost, Origin+"/bot"+token+"/"+method, bytes.NewReader(raw))
	if e != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	transport := http.RoundTripper(defaultTransport)
	if c != nil && c.Transport != nil {
		transport = c.Transport
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil { // Never wrap url.Error: its URL includes the Bot Token.
		if write {
			return nil, fixed(ErrUnknown, "result_unknown")
		}
		return nil, fixed(ErrUnavailable, "request_failed")
	}
	defer response.Body.Close()
	data, e := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if e != nil || len(data) > MaxBodyBytes {
		return nil, incomplete(write)
	}
	status := response.StatusCode
	// A response description may reflect the URL or token. Never expose it.
	var envelope struct {
		OK         *bool           `json:"ok"`
		Result     json.RawMessage `json:"result"`
		ErrorCode  int             `json:"error_code"`
		Parameters struct {
			RetryAfter      json.RawMessage `json:"retry_after"`
			MigrateToChatID ID              `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	parsed := validJSON(data) && json.Unmarshal(data, &envelope) == nil && envelope.OK != nil
	if status == 401 || status == 403 {
		if status == 401 {
			return nil, &Error{Kind: ErrAuth, Code: "invalid_auth", HTTPStatus: status}
		}
		return nil, &Error{Kind: ErrForbidden, Code: "forbidden", HTTPStatus: status}
	}
	if status >= 500 {
		if write {
			return nil, &Error{Kind: ErrUnknown, Code: "result_unknown", HTTPStatus: status}
		}
		return nil, &Error{Kind: ErrUnavailable, Code: "request_failed", HTTPStatus: status}
	}
	if status != 200 && (status < 400 || status >= 500) {
		return nil, incomplete(write)
	}
	if status == 429 || (parsed && !*envelope.OK && envelope.ErrorCode == 429) {
		value := strings.TrimSpace(string(envelope.Parameters.RetryAfter))
		if value == "" {
			value = strings.TrimSpace(response.Header.Get("Retry-After"))
		}
		seconds := int64(1) // Missing advice: local fallback only.
		if value != "" {
			for _, digit := range value {
				if digit < '0' || digit > '9' {
					return nil, &Error{Kind: ErrRejected, Code: "retry_after_invalid", HTTPStatus: status}
				}
			}
			var parseErr error
			seconds, parseErr = strconv.ParseInt(value, 10, 64)
			if parseErr != nil || seconds < 1 || seconds > int64((1<<63-1)/time.Second) {
				return nil, &Error{Kind: ErrRejected, Code: "retry_after_invalid", HTTPStatus: status}
			}
		}
		// Preserve the provider's full delay; caller budget may stop work,
		// but must not turn a longer delay into an earlier retry.
		return nil, &Error{Kind: ErrRateLimited, Code: "rate_limited", HTTPStatus: status, RetryAfter: time.Duration(seconds) * time.Second}
	}

	if !parsed {
		return nil, incomplete(write)
	}
	if !*envelope.OK {
		switch envelope.ErrorCode {
		case 401:
			return nil, &Error{Kind: ErrAuth, Code: "invalid_auth", HTTPStatus: status}
		case 403:
			return nil, &Error{Kind: ErrForbidden, Code: "forbidden", HTTPStatus: status}
		}
		if envelope.ErrorCode >= 400 && envelope.ErrorCode < 500 {
			return nil, &Error{Kind: ErrRejected, Code: "request_rejected", HTTPStatus: status, MigrateToChatID: envelope.Parameters.MigrateToChatID}
		}
		if write {
			return nil, &Error{Kind: ErrUnknown, Code: "result_unknown", HTTPStatus: status}
		}
		return nil, &Error{Kind: ErrUnavailable, Code: "request_failed", HTTPStatus: status}
	}
	if status != 200 || envelope.ErrorCode != 0 || len(envelope.Result) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Result), []byte("null")) {
		return nil, incomplete(write)
	}
	return envelope.Result, nil
}
