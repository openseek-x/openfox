package dingtalk

// Stream 模式长连接（官方协议说明：https://open.dingtalk.com/document/direction/stream-mode-protocol-access-description ）：
//  1. POST {api}/v1.0/gateway/connections/open 注册连接凭证，得到 endpoint 与 ticket（ticket 有效期 90 秒、只能建一条连接）；
//  2. GET {endpoint}?ticket=... 建立 WebSocket；之后服务端推送 JSON 文本帧：
//     {specVersion, type: SYSTEM|EVENT|CALLBACK, headers{messageId, topic, eventType, eventId, ...}, data: "<json 字符串>"}；
//  3. 客户端按帧回 ACK：{code: 200, headers{contentType, messageId}, message: "OK", data: "<json 字符串>"}。
//     SYSTEM 帧里 ping 要原样带回 data.opaque；disconnect 表示服务端即将断开（10 秒后），客户端应重连。
//     机器人消息是回调（CALLBACK，topic /v1.0/im/bot/messages/get），"fire-and-forget"，不会重复推送。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	handshakeTimeout   = 15 * time.Second
	writeTimeout       = 10 * time.Second
	handlerTimeout     = 10 * time.Second
	defaultIdleTimeout = 5 * time.Minute
	maxHandlerWorkers  = 16
	maxFrameBytes      = 1 << 20

	// TopicBotMessage 是机器人消息回调的 topic。
	TopicBotMessage = "/v1.0/im/bot/messages/get"

	// userAgent 标记客户端类型，格式 name-sdk-lang/version。
	userAgent = "openfox-dingtalk-stream-go/1.0"
)

var (
	// ErrInvalidEndpoint 表示钉钉返回的长连接地址不符合安全校验。
	ErrInvalidEndpoint = errors.New("dingtalk: invalid stream endpoint")
	// ErrDisconnected 表示服务端要求断开（disconnect 帧）或连接被关闭，可立即重连。
	ErrDisconnected = errors.New("dingtalk: stream disconnected")
)

// RetryError 表示可以退避后重连的错误。
type RetryError struct{ Err error }

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// IsPermanent 判断错误是否不可自动恢复（凭证无效、端点不合法）。
func IsPermanent(err error) bool {
	return errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrInvalidEndpoint)
}

// Message 是一条业务推送（EVENT 或 CALLBACK）。
type Message struct {
	Type      string // EVENT / CALLBACK
	Topic     string
	MessageID string
	EventID   string
	EventType string
	Data      []byte // data 字段里的 JSON 字符串
}

// Handler 处理一条业务推送；返回 error 表示本次消费失败（事件会回 LATER 让服务端稍后重推）。
type Handler func(ctx context.Context, msg Message) error

// Config 是一次长连接会话的参数。
type Config struct {
	AppKey      string
	Secret      string
	OnConnected func()
	Handler     Handler
	// IdleTimeout 内没有收到任何帧（含 ping）视为连接已死；默认 5 分钟。
	IdleTimeout time.Duration
}

type frame struct {
	SpecVersion string            `json:"specVersion,omitempty"`
	Type        string            `json:"type,omitempty"`
	Headers     map[string]string `json:"headers"`
	Data        string            `json:"data"`
}

type ack struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

// Endpoint 是注册连接凭证的结果。
type Endpoint struct {
	URL    string
	Ticket string
}

// OpenConnection 注册连接凭证。400 / 401 / 403 视为凭证无效（不可重试）；其余错误可退避重试。
func (c *Client) OpenConnection(ctx context.Context, appKey, secret string) (Endpoint, error) {
	body := map[string]any{
		"clientId": appKey, "clientSecret": secret, "ua": userAgent,
		"subscriptions": []map[string]string{
			{"type": "EVENT", "topic": "*"},
			{"type": "CALLBACK", "topic": TopicBotMessage},
		},
	}
	status, data, err := c.call(ctx, http.MethodPost, c.APIURL+"/v1.0/gateway/connections/open", nil, body)
	if err != nil {
		return Endpoint{}, &RetryError{Err: err}
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
		e := decodeV1Error(data)
		return Endpoint{}, &APIError{Code: e.Code, Msg: e.Message, base: ErrInvalidCredentials}
	}
	var payload struct {
		Endpoint string `json:"endpoint"`
		Ticket   string `json:"ticket"`
	}
	if status != http.StatusOK || json.Unmarshal(data, &payload) != nil || payload.Endpoint == "" || payload.Ticket == "" {
		return Endpoint{}, &RetryError{Err: fmt.Errorf("%w: unexpected open response (status %d)", ErrUnavailable, status)}
	}
	parsed, err := c.validateEndpoint(payload.Endpoint)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{URL: parsed.String(), Ticket: payload.Ticket}, nil
}

// validateEndpoint 防 SSRF：协议必须是 wss（基础地址是 http 时允许 ws，即本机模拟）；主机必须等于基础地址主机，
// 或与基础地址主机共享同一个上级域名（api.dingtalk.com → *.dingtalk.com）；IP 字面量只允许与基础地址完全相同。
func (c *Client) validateEndpoint(raw string) (*url.URL, error) {
	invalid := fmt.Errorf("%w", ErrInvalidEndpoint)
	base, err := url.Parse(c.APIURL)
	if err != nil || base.Hostname() == "" {
		return nil, invalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, invalid
	}
	switch {
	case u.Scheme == "wss":
	case u.Scheme == "ws" && base.Scheme == "http":
	default:
		return nil, invalid
	}
	host, baseHost := strings.ToLower(u.Hostname()), strings.ToLower(base.Hostname())
	if host == baseHost {
		// 基础地址是 IP 字面量（本机模拟）时端口也必须相同；域名则不限端口。
		if net.ParseIP(host) != nil && portOf(u) != portOf(base) {
			return nil, invalid
		}
		return u, nil
	}
	if net.ParseIP(host) != nil || net.ParseIP(baseHost) != nil {
		return nil, invalid
	}
	// 共享上级域名：去掉第一段后至少还剩两段。
	parts := strings.SplitN(baseHost, ".", 2)
	if len(parts) == 2 && strings.Count(parts[1], ".") >= 1 && strings.HasSuffix(host, "."+parts[1]) {
		return u, nil
	}
	return nil, invalid
}

func portOf(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" || u.Scheme == "wss" {
		return "443"
	}
	return "80"
}

// Run 跑一次长连接直到断开。ctx 取消返回 ctx.Err()；其余返回 *RetryError（可重连）或不可恢复错误（IsPermanent）。
func (c *Client) Run(ctx context.Context, cfg Config) error {
	endpoint, err := c.OpenConnection(ctx, cfg.AppKey, cfg.Secret)
	if err != nil {
		return err
	}
	dialCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	target := endpoint.URL + "?ticket=" + url.QueryEscape(endpoint.Ticket)
	conn, _, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPClient: &http.Client{Transport: c.HTTP.Transport}})
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &RetryError{Err: fmt.Errorf("%w: handshake failed", ErrUnavailable)}
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxFrameBytes)
	if cfg.OnConnected != nil {
		cfg.OnConnected()
	}
	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	workers := make(chan struct{}, maxHandlerWorkers)
	handlerCtx, stopHandlers := context.WithCancel(ctx)
	defer stopHandlers()
	write := func(a ack) {
		payload, _ := json.Marshal(a)
		writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
		defer cancel()
		_ = conn.Write(writeCtx, websocket.MessageText, payload)
	}
	for {
		readCtx, cancel := context.WithTimeout(ctx, idle)
		_, payload, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &RetryError{Err: fmt.Errorf("%w: read failed", ErrUnavailable)}
		}
		var f frame
		if json.Unmarshal(payload, &f) != nil {
			continue
		}
		messageID := f.Headers["messageId"]
		okHeaders := map[string]string{"contentType": "application/json", "messageId": messageID}
		switch f.Type {
		case "SYSTEM":
			switch f.Headers["topic"] {
			case "ping":
				var ping struct {
					Opaque string `json:"opaque"`
				}
				_ = json.Unmarshal([]byte(f.Data), &ping)
				data, _ := json.Marshal(map[string]string{"opaque": ping.Opaque})
				write(ack{Code: 200, Headers: okHeaders, Message: "OK", Data: string(data)})
			case "disconnect":
				// 服务端会在 10 秒后断开；主动关闭并让调用方立即重连。
				return &RetryError{Err: ErrDisconnected}
			}
		case "EVENT", "CALLBACK":
			if cfg.Handler == nil {
				write(ack{Code: 200, Headers: okHeaders, Message: "OK", Data: ackData(f.Type, true)})
				continue
			}
			msg := Message{Type: f.Type, Topic: f.Headers["topic"], MessageID: messageID, EventID: f.Headers["eventId"],
				EventType: f.Headers["eventType"], Data: []byte(f.Data)}
			workers <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-workers }()
				hctx, cancel := context.WithTimeout(handlerCtx, handlerTimeout)
				err := cfg.Handler(hctx, msg)
				cancel()
				write(ack{Code: 200, Headers: okHeaders, Message: "OK", Data: ackData(f.Type, err == nil)})
			}()
		}
	}
}

// ackData 生成 ACK 的 data：事件回 SUCCESS / LATER（LATER 让服务端稍后重推），回调回空 response。
func ackData(frameType string, ok bool) string {
	if frameType == "EVENT" {
		status := "SUCCESS"
		if !ok {
			status = "LATER"
		}
		data, _ := json.Marshal(map[string]string{"status": status, "message": strings.ToLower(status)})
		return string(data)
	}
	return `{"response":{}}`
}
