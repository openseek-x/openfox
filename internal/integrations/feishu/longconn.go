package feishu

// 一次长连接会话：取得端点 → 握手 → ping / 读帧 / 合包 / 回 ACK，直到断开。重连与退避由调用方（长连接管理器）负责。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	handshakeTimeout    = 15 * time.Second
	writeTimeout        = 10 * time.Second
	handlerTimeout      = 2500 * time.Millisecond
	defaultPingInterval = 120 * time.Second
	maxHandlerWorkers   = 16
	maxCombineParts     = 16
	combineWindow       = 5 * time.Second
	readSlack           = 10 * time.Second
)

var (
	// ErrConnectionLimit 表示应用的长连接数已达飞书上限（最多 50 个），不能自动恢复。
	ErrConnectionLimit = errors.New("feishu: connection limit exceeded")
	// ErrInvalidEndpoint 表示飞书返回的长连接地址不符合安全校验。
	ErrInvalidEndpoint = errors.New("feishu: invalid long connection endpoint")
)

// RetryError 表示可以退避后重连的错误；Interval / Nonce 来自飞书下发的 ClientConfig（秒），为 0 表示没有下发。
type RetryError struct {
	Err      error
	Interval time.Duration
	Nonce    time.Duration
}

func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }

// IsPermanent 判断错误是否不可自动恢复（凭证无效、连接数超限、端点不合法）。
func IsPermanent(err error) bool {
	return errors.Is(err, ErrInvalidCredentials) || errors.Is(err, ErrConnectionLimit) || errors.Is(err, ErrInvalidEndpoint)
}

// ClientConfig 是飞书下发的客户端参数（单位：秒）。
type ClientConfig struct {
	ReconnectCount    int `json:"ReconnectCount"`
	ReconnectInterval int `json:"ReconnectInterval"`
	ReconnectNonce    int `json:"ReconnectNonce"`
	PingInterval      int `json:"PingInterval"`
}

// Endpoint 是 FetchEndpoint 的结果。
type Endpoint struct {
	URL      string
	DeviceID string
	Service  int32
	Config   ClientConfig
}

// FetchEndpoint 取得长连接地址与客户端参数。
// code 为 1 / 1000040343 或 HTTP 非 200 可重试；其余非 0 视为凭证无效（不可重试）。
func (c *Client) FetchEndpoint(ctx context.Context, appID, secret string) (Endpoint, error) {
	status, _, data, err := c.call(ctx, http.MethodPost, c.BaseURL+"/callback/ws/endpoint", "",
		map[string]string{"AppID": appID, "AppSecret": secret}, map[string]string{"locale": "zh"})
	if err != nil {
		return Endpoint{}, &RetryError{Err: err}
	}
	if status != http.StatusOK {
		return Endpoint{}, &RetryError{Err: fmt.Errorf("%w: endpoint http status %d", ErrUnavailable, status)}
	}
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL          string       `json:"URL"`
			ClientConfig ClientConfig `json:"ClientConfig"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return Endpoint{}, &RetryError{Err: ErrUnavailable}
	}
	switch payload.Code {
	case 0:
	case codeSystemBusy, codeInternalError:
		return Endpoint{}, &RetryError{Err: &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrUnavailable}}
	default:
		return Endpoint{}, &APIError{Code: payload.Code, Msg: payload.Msg, base: ErrInvalidCredentials}
	}
	parsed, err := c.validateEndpoint(payload.Data.URL)
	if err != nil {
		return Endpoint{}, err
	}
	endpoint := Endpoint{URL: parsed.String(), DeviceID: parsed.Query().Get("device_id"), Config: payload.Data.ClientConfig}
	if id, err := strconv.ParseInt(parsed.Query().Get("service_id"), 10, 32); err == nil {
		endpoint.Service = int32(id)
	}
	return endpoint, nil
}

// validateEndpoint 防 SSRF：协议必须是 wss（基础地址是 http 时允许 ws，即本机模拟）；主机必须等于基础地址主机，
// 或是基础地址主机去掉第一段后的域名的子域；IP 字面量只允许与基础地址完全相同。
func (c *Client) validateEndpoint(raw string) (*url.URL, error) {
	invalid := fmt.Errorf("%w", ErrInvalidEndpoint)
	base, err := url.Parse(c.BaseURL)
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
	port, basePort := u.Port(), base.Port()
	if net.ParseIP(host) != nil || net.ParseIP(baseHost) != nil || host == baseHost {
		if host == baseHost && (port == basePort || (port == "" && basePort == "")) {
			return u, nil
		}
		return nil, invalid
	}
	dot := strings.Index(baseHost, ".")
	if dot < 0 {
		return nil, invalid
	}
	parent := baseHost[dot+1:]
	if !strings.Contains(parent, ".") || !strings.HasSuffix(host, "."+parent) {
		return nil, invalid
	}
	if port != "" && port != "443" {
		return nil, invalid
	}
	return u, nil
}

// Handler 处理一个事件 / 卡片回调的 payload；返回的字节放进 ACK 的 data。
type Handler func(ctx context.Context, payload []byte) ([]byte, error)

// Config 是一次长连接会话的参数。
type Config struct {
	AppID       string
	Secret      string
	OnConnected func()
	Handler     Handler
}

// Run 跑一次长连接直到断开。ctx 取消返回 ctx.Err()；其余返回 *RetryError（可重连）或不可恢复错误（IsPermanent）。
func (c *Client) Run(ctx context.Context, cfg Config) error {
	endpoint, err := c.FetchEndpoint(ctx, cfg.AppID, cfg.Secret)
	if err != nil {
		return err
	}
	ccfg := endpoint.Config
	retry := func(err error) error {
		return &RetryError{Err: err, Interval: time.Duration(ccfg.ReconnectInterval) * time.Second,
			Nonce: time.Duration(ccfg.ReconnectNonce) * time.Second}
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, handshakeTimeout)
	conn, resp, err := websocket.Dial(dialCtx, endpoint.URL, &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": {"OpenFox"}}})
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if hs := classifyHandshake(resp); hs != nil {
			return hs
		}
		return retry(fmt.Errorf("%w: handshake failed", ErrUnavailable))
	}
	conn.SetReadLimit(maxFrameBytes + 4096)
	s := &session{conn: conn, service: endpoint.Service, handler: cfg.Handler, parts: map[string]*partial{}}
	s.ping.Store(int64(pingDuration(ccfg.PingInterval)))
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.CloseNow()
	if cfg.OnConnected != nil {
		cfg.OnConnected()
	}
	go s.pingLoop(sessCtx, cancel)
	err = s.readLoop(sessCtx, &ccfg)
	cancel()
	s.wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return retry(err)
}

func pingDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultPingInterval
	}
	return time.Duration(seconds) * time.Second
}

// classifyHandshake 读握手失败响应头：514 + 1000040350 = 连接数超限；514 其他 / 403 = 凭证问题；其余可重试。
func classifyHandshake(resp *http.Response) error {
	if resp == nil {
		return nil
	}
	status, _ := strconv.Atoi(resp.Header.Get(HeaderHandshakeStatus))
	authErr, _ := strconv.Atoi(resp.Header.Get(HeaderHandshakeAuthErrCode))
	msg := truncate(resp.Header.Get(HeaderHandshakeMsg), 200)
	switch {
	case status == StatusAuthFailed && authErr == CodeExceedConnLimit:
		return &APIError{Code: authErr, Msg: msg, base: ErrConnectionLimit}
	case status == StatusAuthFailed || status == StatusForbidden:
		return &APIError{Code: authErr, Msg: msg, base: ErrInvalidCredentials}
	case status != 0:
		return &RetryError{Err: &APIError{Code: status, Msg: msg, base: ErrUnavailable}}
	}
	return nil
}

type partial struct {
	parts   [][]byte
	got     int
	size    int
	created time.Time
}

type session struct {
	conn    *websocket.Conn
	service int32
	handler Handler
	ping    atomic.Int64
	wmu     sync.Mutex
	wg      sync.WaitGroup
	parts   map[string]*partial
	sem     chan struct{}
}

func (s *session) write(ctx context.Context, frame *Frame) error {
	b, err := frame.Marshal()
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return s.conn.Write(wctx, websocket.MessageBinary, b)
}

func (s *session) pingLoop(ctx context.Context, cancel context.CancelFunc) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		frame := &Frame{Method: FrameTypeControl, Service: s.service, Headers: []Header{{Key: HeaderType, Value: TypePing}}}
		if err := s.write(ctx, frame); err != nil {
			cancel()
			return
		}
		timer.Reset(time.Duration(s.ping.Load()))
	}
}

func (s *session) readLoop(ctx context.Context, ccfg *ClientConfig) error {
	s.sem = make(chan struct{}, maxHandlerWorkers)
	for {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Duration(s.ping.Load())+readSlack)
		_, data, err := s.conn.Read(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: read failed", ErrUnavailable)
		}
		var frame Frame
		if err := frame.Unmarshal(data); err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		switch frame.Method {
		case FrameTypeControl:
			if frame.Header(HeaderType) == TypePong && len(frame.Payload) > 0 {
				var updated ClientConfig
				if json.Unmarshal(frame.Payload, &updated) == nil {
					if updated.PingInterval > 0 {
						s.ping.Store(int64(pingDuration(updated.PingInterval)))
						ccfg.PingInterval = updated.PingInterval
					}
					ccfg.ReconnectCount, ccfg.ReconnectInterval, ccfg.ReconnectNonce = updated.ReconnectCount, updated.ReconnectInterval, updated.ReconnectNonce
				}
			}
		case FrameTypeData:
			kind := frame.Header(HeaderType)
			if kind != TypeEvent && kind != TypeCard {
				continue
			}
			payload, ok := s.combine(&frame)
			if !ok {
				continue
			}
			select {
			case s.sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			s.wg.Add(1)
			go func(frame Frame, payload []byte) {
				defer s.wg.Done()
				defer func() { <-s.sem }()
				s.dispatch(ctx, frame, payload)
			}(frame, payload)
		}
	}
}

// combine 按 sum / seq / message_id 合包：最多 16 段、总长 1 MiB、5 秒内凑齐，否则丢弃。返回完整 payload。
func (s *session) combine(frame *Frame) ([]byte, bool) {
	sum, _ := strconv.Atoi(frame.Header(HeaderSum))
	seq, _ := strconv.Atoi(frame.Header(HeaderSeq))
	if sum <= 1 {
		return frame.Payload, true
	}
	now := time.Now()
	for id, p := range s.parts {
		if now.Sub(p.created) > combineWindow {
			delete(s.parts, id)
		}
	}
	id := frame.Header(HeaderMessageID)
	if id == "" || sum > maxCombineParts || seq < 0 || seq >= sum {
		return nil, false
	}
	p := s.parts[id]
	if p == nil {
		if len(s.parts) >= maxHandlerWorkers*4 {
			return nil, false
		}
		p = &partial{parts: make([][]byte, sum), created: now}
		s.parts[id] = p
	}
	if len(p.parts) != sum || p.parts[seq] != nil {
		delete(s.parts, id)
		return nil, false
	}
	p.size += len(frame.Payload)
	if p.size > maxFrameBytes {
		delete(s.parts, id)
		return nil, false
	}
	p.parts[seq] = append([]byte{}, frame.Payload...)
	if len(frame.Payload) == 0 {
		p.parts[seq] = []byte{}
	}
	p.got++
	if p.got < sum {
		return nil, false
	}
	delete(s.parts, id)
	joined := make([]byte, 0, p.size)
	for _, part := range p.parts {
		joined = append(joined, part...)
	}
	return joined, true
}

type ackBody struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Data    []byte            `json:"data,omitempty"`
}

// dispatch 在 2.5 秒内执行 handler，并用原帧的副本回 ACK（headers 追加 biz_rt，payload 为 JSON）。
func (s *session) dispatch(ctx context.Context, frame Frame, payload []byte) {
	started := time.Now()
	hctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()
	ack := ackBody{Code: 200, Headers: map[string]string{}}
	if s.handler == nil {
		ack.Code = 500
	} else if data, err := s.handler(hctx, payload); err != nil {
		ack.Code = 500
	} else {
		ack.Data = data
	}
	body, _ := json.Marshal(ack)
	reply := frame
	reply.Headers = append([]Header(nil), frame.Headers...)
	reply.SetHeader(HeaderBizRT, strconv.FormatInt(time.Since(started).Milliseconds(), 10))
	reply.Payload = body
	_ = s.write(ctx, &reply)
}

// ValidateEndpoint 暴露端点地址校验（测试用）。
func (c *Client) ValidateEndpoint(raw string) error {
	_, err := c.validateEndpoint(raw)
	return err
}
