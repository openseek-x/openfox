// Package cliclient 是协作 CLI 访问中心 API 的 HTTP 客户端：Bearer 个人访问令牌、
// 有界超时、错误解析，以及仅限带幂等键写请求的一次网络错误重试。
package cliclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ConnectTimeout 是建立连接（含 TLS 握手）的上限；RequestTimeout 是单次请求的整体上限。
	ConnectTimeout = 10 * time.Second
	RequestTimeout = 30 * time.Second
	// maxResponseBytes 限制响应体大小，防止异常响应耗尽内存。
	maxResponseBytes = 16 << 20
)

// Request 描述一次 API 调用。Path 以 "/v1/" 开头，动态片段由调用方用 url.PathEscape 转义。
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   any
	// IdempotencyKey 非空时作为 Idempotency-Key 头发送。
	IdempotencyKey string
	// Idempotent 为 true 表示请求自带幂等保证（Idempotency-Key 头或 client_*_id 字段），
	// 只有这类请求在网络错误时才会重试一次。
	Idempotent bool
}

// Response 是成功（2xx）响应。
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// APIError 是中心返回的非 2xx 响应。
type APIError struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *APIError) Error() string {
	text := fmt.Sprintf("center returned HTTP %d", e.Status)
	if e.Code != "" {
		text += " " + e.Code
	}
	if e.Message != "" {
		text += ": " + e.Message
	}
	return text
}

// NetworkError 是连接、超时、读取失败等传输层错误（没有拿到完整响应）。
type NetworkError struct{ Err error }

func (e *NetworkError) Error() string { return "center unreachable: " + e.Err.Error() }
func (e *NetworkError) Unwrap() error { return e.Err }

// Client 访问一个中心。
type Client struct {
	BaseURL   string // 规范化后的服务器地址，不含 /api
	Token     string
	UserAgent string
	HTTP      *http.Client
	// Sleep 与 Jitter 可由测试替换。
	Sleep  func(time.Duration)
	Jitter func() time.Duration
}

// NormalizeServer 校验并规范化服务器地址：只接受 http/https，不含用户信息、查询串与片段。
func NormalizeServer(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid server URL: expected http(s)://host[:port]")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", errors.New("invalid server URL: scheme must be https or http")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

// InsecureRemote 报告地址是否为非本机的明文 http（令牌会在网络上明文传输）。
func InsecureRemote(server string) bool {
	parsed, err := url.Parse(server)
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// New 创建客户端。version 写入 User-Agent。
func New(server, token, version string) (*Client, error) {
	base, err := NormalizeServer(server)
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: base, Token: token, UserAgent: "openfox-cli/" + version, HTTP: DefaultHTTPClient()}, nil
}

// DefaultHTTPClient 返回带连接超时与整体超时的客户端；不跟随重定向，避免令牌被转发。
func DefaultHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = ConnectTimeout
	return &http.Client{
		Timeout:       RequestTimeout,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Do 发送请求。2xx 返回 Response；其他状态返回 *APIError；传输层失败返回 *NetworkError。
// 仅当 req.Idempotent 为 true 且第一次因网络错误失败时，带抖动重试一次（沿用同一个幂等键）。
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	var payload []byte
	if req.Body != nil {
		encoded, err := json.Marshal(req.Body)
		if err != nil {
			return nil, err
		}
		payload = encoded
	}
	response, err := c.once(ctx, req, payload)
	var network *NetworkError
	if err != nil && req.Idempotent && errors.As(err, &network) && ctx.Err() == nil {
		c.sleep(c.jitter())
		response, err = c.once(ctx, req, payload)
	}
	return response, err
}

func (c *Client) jitter() time.Duration {
	if c.Jitter != nil {
		return c.Jitter()
	}
	return 200*time.Millisecond + time.Duration(rand.Int64N(int64(400*time.Millisecond)))
}

func (c *Client) sleep(d time.Duration) {
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (c *Client) once(ctx context.Context, req Request, payload []byte) (*Response, error) {
	target := c.BaseURL + "/api" + req.Path
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, req.Method, target, body)
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+c.Token)
	httpRequest.Header.Set("User-Agent", c.UserAgent)
	httpRequest.Header.Set("Accept", "application/json")
	if payload != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if req.IdempotencyKey != "" {
		httpRequest.Header.Set("Idempotency-Key", req.IdempotencyKey)
	}
	httpResponse, err := c.HTTP.Do(httpRequest)
	if err != nil {
		return nil, &NetworkError{Err: c.redactError(err)}
	}
	defer httpResponse.Body.Close()
	data, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &NetworkError{Err: c.redactError(err)}
	}
	if len(data) > maxResponseBytes {
		return nil, errors.New("response too large")
	}
	if httpResponse.StatusCode >= 200 && httpResponse.StatusCode < 300 {
		return &Response{Status: httpResponse.StatusCode, Header: httpResponse.Header, Body: data}, nil
	}
	apiError := &APIError{Status: httpResponse.StatusCode}
	var parsed struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &parsed) == nil {
		apiError.Code, apiError.Message, apiError.RequestID = parsed.Error.Code, parsed.Error.Message, parsed.Error.RequestID
	}
	if apiError.Message == "" {
		apiError.Message = http.StatusText(httpResponse.StatusCode)
	}
	apiError.Message = c.redact(apiError.Message)
	return nil, apiError
}

// redact 从任何要展示的文本里抹掉令牌，防止被服务端或底层库回显。
func (c *Client) redact(text string) string {
	if c.Token == "" {
		return text
	}
	return strings.ReplaceAll(text, c.Token, "[REDACTED]")
}

func (c *Client) redactError(err error) error {
	return errors.New(c.redact(err.Error()))
}
