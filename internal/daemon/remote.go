package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"openfox/internal/devices"
)

const (
	DefaultHeartbeatInterval     = 20 * time.Second
	DefaultDialTimeout           = 10 * time.Second
	DefaultHandshakeTimeout      = 10 * time.Second
	DefaultWriteTimeout          = 5 * time.Second
	DefaultHeartbeatTimeout      = 30 * time.Second
	DefaultConnectionStableAfter = time.Minute
	DefaultBackoffBase           = time.Second
	DefaultBackoffMax            = time.Minute
)

var (
	ErrRemoteConfiguration = errors.New("daemon: remote configuration is invalid")
	ErrRemoteMessage       = errors.New("daemon: invalid remote message")
)

type RemoteState string

const (
	RemoteConnecting   RemoteState = "CONNECTING"
	RemoteConnected    RemoteState = "CONNECTED"
	RemoteReconnecting RemoteState = "RECONNECTING"
	RemoteDisconnected RemoteState = "DISCONNECTED"
	RemoteReconcile    RemoteState = "RECONCILE_REQUIRED"
)

type ReconcileInfo struct {
	CurrentEpoch int64
	Reason       string
}

type ReconcileError struct {
	Info ReconcileInfo
}

func (e ReconcileError) Error() string {
	if e.Info.Reason == "" {
		return ErrReconcileRequired.Error()
	}
	return fmt.Sprintf("%s: %s", ErrReconcileRequired, e.Info.Reason)
}

func (e ReconcileError) Unwrap() error { return ErrReconcileRequired }

type MessageConnection interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Ping(context.Context) error
	Close() error
}

type Dialer interface {
	Dial(context.Context, string, http.Header) (MessageConnection, error)
}

type WebSocketDialer struct {
	HTTPClient *http.Client
}

func (d WebSocketDialer) Dial(ctx context.Context, rawURL string, headers http.Header) (MessageConnection, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" {
		return nil, ErrRemoteConfiguration
	}
	requestHeaders := headers.Clone()
	if requestHeaders == nil {
		requestHeaders = make(http.Header)
	}
	connection, response, err := websocket.Dial(ctx, rawURL, &websocket.DialOptions{
		HTTPClient:      d.HTTPClient,
		HTTPHeader:      requestHeaders,
		Subprotocols:    []string{ProtocolSubprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		if response != nil && response.StatusCode == http.StatusUnauthorized {
			// 会话令牌失效：交给 TokenProvider 续期，而不是按普通网络错误重试旧令牌。
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	connection.SetReadLimit(MaxMessageBytes)
	return &webSocketConnection{connection: connection}, nil
}

type webSocketConnection struct {
	connection *websocket.Conn
}

func (c *webSocketConnection) Read(ctx context.Context) ([]byte, error) {
	typ, payload, err := c.connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, ErrRemoteMessage
	}
	return payload, nil
}

func (c *webSocketConnection) Write(ctx context.Context, payload []byte) error {
	return c.connection.Write(ctx, websocket.MessageText, payload)
}

func (c *webSocketConnection) Ping(ctx context.Context) error {
	return c.connection.Ping(ctx)
}

func (c *webSocketConnection) Close() error {
	return c.connection.CloseNow()
}

type HeartbeatSource interface {
	Current(context.Context) (devices.Heartbeat, error)
}

type StaticHeartbeatSource struct {
	mu        sync.RWMutex
	heartbeat devices.Heartbeat
}

func NewStaticHeartbeatSource(heartbeat devices.Heartbeat) *StaticHeartbeatSource {
	return &StaticHeartbeatSource{heartbeat: copyHeartbeat(heartbeat)}
}

func (s *StaticHeartbeatSource) Current(context.Context) (devices.Heartbeat, error) {
	if s == nil {
		return devices.Heartbeat{}, ErrRemoteConfiguration
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyHeartbeat(s.heartbeat), nil
}

func (s *StaticHeartbeatSource) SetCenterEpoch(epoch int64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeat.CenterEpoch = epoch
}

// SetCapabilities 替换后续心跳上报的能力 JSON（例如 Runtime 版本与可用模型列表）。
func (s *StaticHeartbeatSource) SetCapabilities(capabilities []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeat.Capabilities = append([]byte(nil), capabilities...)
}

type BackoffConfig struct {
	Base   time.Duration
	Max    time.Duration
	Jitter float64
	Rand   func() float64
}

type Backoff struct {
	config  BackoffConfig
	attempt int
}

func NewBackoff(config BackoffConfig) (Backoff, error) {
	if config.Base <= 0 {
		config.Base = DefaultBackoffBase
	}
	if config.Max <= 0 {
		config.Max = DefaultBackoffMax
	}
	if config.Max < config.Base || config.Jitter < 0 || config.Jitter > 1 {
		return Backoff{}, ErrRemoteConfiguration
	}
	if config.Rand == nil {
		config.Rand = rand.Float64
	}
	return Backoff{config: config}, nil
}

func (b *Backoff) Reset() {
	if b != nil {
		b.attempt = 0
	}
}

func (b *Backoff) Next() time.Duration {
	if b == nil {
		return DefaultBackoffBase
	}
	delay := b.config.Base
	for i := 0; i < b.attempt && delay < b.config.Max; i++ {
		if delay > b.config.Max/2 {
			delay = b.config.Max
			break
		}
		delay *= 2
	}
	if delay > b.config.Max {
		delay = b.config.Max
	}
	b.attempt++
	if b.config.Jitter == 0 {
		return delay
	}
	randomValue := b.config.Rand()
	if randomValue < 0 {
		randomValue = 0
	}
	if randomValue > 1 {
		randomValue = 1
	}
	delayed := delay + time.Duration(float64(delay)*b.config.Jitter*randomValue)
	if delayed > b.config.Max {
		return b.config.Max
	}
	return delayed
}

type RemoteClientConfig struct {
	URL               string
	DeviceID          string
	TokenProvider     func(context.Context) (string, error)
	HeartbeatSource   HeartbeatSource
	HeartbeatInterval time.Duration
	DialTimeout       time.Duration
	HandshakeTimeout  time.Duration
	WriteTimeout      time.Duration
	HeartbeatTimeout  time.Duration
	StableAfter       time.Duration
	Backoff           BackoffConfig
	Dialer            Dialer
	Sleep             func(context.Context, time.Duration) error
	OnState           func(RemoteState)
	OnReconcile       func(ReconcileInfo)
	// OnRunMessage 接收中心推送的 run.dispatch / run.cancel 唤醒。回调必须非阻塞；
	// 唤醒只是提示，事实以 HTTPS 端点为准。
	OnRunMessage func(Message)
	// OnCommandMessage 接收 device.command 唤醒（远程命令）。回调必须非阻塞。
	OnCommandMessage func(Message)
	// OnHeartbeat 在 hello.ack 与每次 heartbeat.ack 之后调用（例如顺带拉取远程命令）。回调必须非阻塞。
	OnHeartbeat func()
	// OnUnauthorized 在中心以 401 拒绝 WSS 握手时调用，参数是被拒绝的令牌（用于触发续期，不得记录）。
	OnUnauthorized func(token string)
}

type RemoteClient struct {
	config  RemoteClientConfig
	backoff Backoff
	// heartbeatNow 请求立即发送一次心跳（例如 Runtime 重新检测后刷新 capabilities）。
	heartbeatNow chan struct{}
}

// RequestHeartbeat 请求在当前连接上尽快发送一次心跳；非阻塞，多次请求合并。
func (c *RemoteClient) RequestHeartbeat() {
	if c == nil {
		return
	}
	select {
	case c.heartbeatNow <- struct{}{}:
	default:
	}
}

func NewRemoteClient(config RemoteClientConfig) (*RemoteClient, error) {
	config.URL = strings.TrimSpace(config.URL)
	config.DeviceID = strings.TrimSpace(config.DeviceID)
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = DefaultDialTimeout
	}
	if config.HandshakeTimeout <= 0 {
		config.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = DefaultWriteTimeout
	}
	if config.HeartbeatTimeout <= 0 {
		config.HeartbeatTimeout = DefaultHeartbeatTimeout
	}
	if config.StableAfter <= 0 {
		config.StableAfter = DefaultConnectionStableAfter
	}
	if config.Dialer == nil {
		config.Dialer = WebSocketDialer{}
	}
	if config.Sleep == nil {
		config.Sleep = sleepContext
	}
	if config.Backoff.Base <= 0 {
		config.Backoff.Base = DefaultBackoffBase
	}
	if config.Backoff.Max <= 0 {
		config.Backoff.Max = DefaultBackoffMax
	}
	backoff, err := NewBackoff(config.Backoff)
	if err != nil || config.URL == "" || config.DeviceID == "" || config.TokenProvider == nil || config.HeartbeatSource == nil ||
		config.DialTimeout <= 0 || config.HandshakeTimeout <= 0 || config.WriteTimeout <= 0 || config.HeartbeatTimeout <= 0 || config.StableAfter <= 0 {
		return nil, ErrRemoteConfiguration
	}
	return &RemoteClient{config: config, backoff: backoff, heartbeatNow: make(chan struct{}, 1)}, nil
}

func (c *RemoteClient) Run(ctx context.Context) error {
	if c == nil {
		return ErrRemoteConfiguration
	}
	for {
		if ctx.Err() != nil {
			c.notify(RemoteDisconnected)
			return nil
		}
		c.notify(RemoteConnecting)
		token, err := c.config.TokenProvider(ctx)
		if err == nil {
			token = strings.TrimSpace(token)
			if token == "" {
				err = ErrRemoteConfiguration
			}
		}
		var connection MessageConnection
		if err == nil {
			dialContext, cancel := context.WithTimeout(ctx, c.config.DialTimeout)
			connection, err = c.config.Dialer.Dial(dialContext, c.config.URL, authorizationHeader(token))
			cancel()
			if errors.Is(err, ErrUnauthorized) && c.config.OnUnauthorized != nil {
				c.config.OnUnauthorized(token)
			}
		}
		if err == nil {
			c.notify(RemoteConnected)
			healthy, sessionErr := c.runConnection(ctx, connection)
			_ = connection.Close()
			if errors.Is(sessionErr, ErrReconcileRequired) {
				c.notify(RemoteReconcile)
				return sessionErr
			}
			if ctx.Err() != nil {
				c.notify(RemoteDisconnected)
				return nil
			}
			if healthy {
				c.backoff.Reset()
			}
		}
		if ctx.Err() != nil {
			c.notify(RemoteDisconnected)
			return nil
		}
		c.notify(RemoteReconnecting)
		if err := c.config.Sleep(ctx, c.backoff.Next()); err != nil {
			c.notify(RemoteDisconnected)
			return nil
		}
	}
}

func (c *RemoteClient) runConnection(ctx context.Context, connection MessageConnection) (bool, error) {
	heartbeat, err := c.config.HeartbeatSource.Current(ctx)
	if err != nil {
		return false, err
	}
	hello, err := NewHeartbeatMessage("hello", c.config.DeviceID, heartbeat)
	if err != nil {
		return false, err
	}
	if err := c.write(ctx, connection, hello); err != nil {
		return false, err
	}
	handshakeContext, cancel := context.WithTimeout(ctx, c.config.HandshakeTimeout)
	message, err := c.read(handshakeContext, connection)
	cancel()
	if err != nil {
		return false, err
	}
	if err := c.validateResponse(message, c.config.DeviceID, hello.MessageID, "hello.ack", heartbeat.CenterEpoch); err != nil {
		return false, err
	}
	c.heartbeatAcked()

	readContext, stopReading := context.WithCancel(ctx)
	defer stopReading()
	readResults := make(chan readResult, 1)
	startRead := func() {
		go func() {
			message, err := c.read(readContext, connection)
			readResults <- readResult{message: message, err: err}
		}()
	}
	startRead()
	ticker := time.NewTicker(c.config.HeartbeatInterval)
	defer ticker.Stop()
	var pendingMessageID string
	var heartbeatTimeout <-chan time.Time
	var heartbeatTimer *time.Timer
	stable := false
	connectedAt := time.Now()
	for {
		select {
		case <-ctx.Done():
			return stable, nil
		case result := <-readResults:
			if result.err != nil {
				return stable, result.err
			}
			if result.message.Type == "reconcile_required" {
				return stable, c.reconcileError(result.message)
			}
			if result.message.Type == MessageRunDispatch || result.message.Type == MessageRunCancel {
				if err := c.handleRunMessage(result.message, heartbeat.CenterEpoch); err != nil {
					return stable, err
				}
				startRead()
				continue
			}
			if result.message.Type == MessageDeviceCommand || result.message.Type == "gitlab.proxy" {
				if err := c.handleCommandMessage(result.message, heartbeat.CenterEpoch); err != nil {
					return stable, err
				}
				startRead()
				continue
			}
			if err := c.validateResponse(result.message, c.config.DeviceID, pendingMessageID, "heartbeat.ack", heartbeat.CenterEpoch); err != nil {
				return stable, err
			}
			pendingMessageID = ""
			if heartbeatTimer != nil {
				if !heartbeatTimer.Stop() {
					select {
					case <-heartbeatTimer.C:
					default:
					}
				}
				heartbeatTimer = nil
				heartbeatTimeout = nil
			}
			if time.Since(connectedAt) >= c.config.StableAfter {
				stable = true
			}
			c.heartbeatAcked()
			startRead()
		case <-c.heartbeatNow:
			if pendingMessageID != "" {
				// 已有心跳在途：其 ack 之后的下一次周期心跳会带上最新 capabilities。
				continue
			}
			heartbeat, err = c.config.HeartbeatSource.Current(ctx)
			if err != nil {
				return stable, err
			}
			message, err := NewHeartbeatMessage("heartbeat", c.config.DeviceID, heartbeat)
			if err != nil {
				return stable, err
			}
			if err := c.write(ctx, connection, message); err != nil {
				return stable, err
			}
			pendingMessageID = message.MessageID
			heartbeatTimer = time.NewTimer(c.config.HeartbeatTimeout)
			heartbeatTimeout = heartbeatTimer.C
		case <-ticker.C:
			if pendingMessageID != "" {
				continue
			}
			heartbeat, err = c.config.HeartbeatSource.Current(ctx)
			if err != nil {
				return stable, err
			}
			message, err := NewHeartbeatMessage("heartbeat", c.config.DeviceID, heartbeat)
			if err != nil {
				return stable, err
			}
			if err := c.write(ctx, connection, message); err != nil {
				return stable, err
			}
			pendingMessageID = message.MessageID
			heartbeatTimer = time.NewTimer(c.config.HeartbeatTimeout)
			heartbeatTimeout = heartbeatTimer.C
		case <-heartbeatTimeout:
			return stable, context.DeadlineExceeded
		}
	}
}

type readResult struct {
	message Message
	err     error
}

func (c *RemoteClient) read(ctx context.Context, connection MessageConnection) (Message, error) {
	payload, err := connection.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	if len(payload) == 0 || len(payload) > MaxMessageBytes {
		return Message{}, ErrMessageTooLarge
	}
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return Message{}, ErrRemoteMessage
	}
	return message, nil
}

func (c *RemoteClient) write(ctx context.Context, connection MessageConnection, message Message) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(encoded) > MaxMessageBytes {
		return ErrMessageTooLarge
	}
	writeContext, cancel := context.WithTimeout(ctx, c.config.WriteTimeout)
	defer cancel()
	return connection.Write(writeContext, encoded)
}

func (c *RemoteClient) validateResponse(message Message, computerID, messageID, expectedType string, expectedEpoch int64) error {
	if message.Type == "reconcile_required" {
		return c.reconcileError(message)
	}
	if err := message.Envelope.Validate(computerID, expectedEpoch); err != nil {
		return err
	}
	if message.Type != expectedType || message.MessageID != messageID {
		return ErrRemoteMessage
	}
	var ack HeartbeatAckPayload
	if len(message.Payload) == 0 || json.Unmarshal(message.Payload, &ack) != nil || ack.CenterEpoch != expectedEpoch ||
		ack.DeviceID != computerID || ack.Version <= 0 {
		return ErrRemoteMessage
	}
	if _, err := time.Parse(time.RFC3339Nano, ack.ReceivedAt); err != nil {
		return ErrRemoteMessage
	}
	return nil
}

// handleRunMessage 校验唤醒的设备身份与纪元后交给回调；缺少 Run 身份的唤醒视为协议错误。
func (c *RemoteClient) handleRunMessage(message Message, expectedEpoch int64) error {
	if err := message.Envelope.Validate(c.config.DeviceID, expectedEpoch); err != nil {
		return err
	}
	if message.OrganizationID == "" || message.RunID == "" || message.DispatchGeneration <= 0 {
		return ErrRemoteMessage
	}
	if c.config.OnRunMessage != nil {
		c.config.OnRunMessage(message)
	}
	return nil
}

// handleCommandMessage 校验 device.command 唤醒的设备身份与纪元后交给回调。
// payload 只是提示（command_id、kind）；命令内容以设备 HTTPS 端点为准。
func (c *RemoteClient) handleCommandMessage(message Message, expectedEpoch int64) error {
	if err := message.Envelope.Validate(c.config.DeviceID, expectedEpoch); err != nil {
		return err
	}
	if c.config.OnCommandMessage != nil {
		c.config.OnCommandMessage(message)
	}
	return nil
}

func (c *RemoteClient) heartbeatAcked() {
	if c.config.OnHeartbeat != nil {
		c.config.OnHeartbeat()
	}
}

func (c *RemoteClient) reconcileError(message Message) error {
	if err := message.Envelope.Validate(c.config.DeviceID, 0); err != nil {
		return err
	}
	var payload ProtocolErrorPayload
	if len(message.Payload) > 0 {
		_ = json.Unmarshal(message.Payload, &payload)
	}
	info := ReconcileInfo{CurrentEpoch: payload.CenterEpoch, Reason: payload.Message}
	if c.config.OnReconcile != nil {
		c.config.OnReconcile(info)
	}
	return ReconcileError{Info: info}
}

func (c *RemoteClient) notify(state RemoteState) {
	if c.config.OnState != nil {
		c.config.OnState(state)
	}
}

func authorizationHeader(token string) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+token)
	return headers
}

func copyHeartbeat(value devices.Heartbeat) devices.Heartbeat {
	value.Capabilities = append([]byte(nil), value.Capabilities...)
	return value
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ReadTokenFile(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ErrRemoteConfiguration
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", ErrRemoteConfiguration
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(contents))
	if token == "" || len(token) > 128 {
		return "", ErrRemoteConfiguration
	}
	return token, nil
}

func FileTokenProvider(path string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return ReadTokenFile(path) }
}

func StaticTokenProvider(token string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		if strings.TrimSpace(token) == "" {
			return "", ErrRemoteConfiguration
		}
		return token, nil
	}
}
