package wecom

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const DefaultURL = "wss://openws.work.weixin.qq.com"

const (
	dialTimeout       = 15 * time.Second
	authTimeout       = 10 * time.Second
	ackTimeout        = 5 * time.Second
	writeTimeout      = 10 * time.Second
	pingInterval      = 30 * time.Second
	maxFrameBytes     = 1 << 20
	maxHandlers       = 16
	maxQueuedMessages = 64
	maxMarkdownRunes  = 4000
	callbackAttempts  = 3
)

var (
	ErrInvalidCredentials = errors.New("wecom: bot credentials rejected")
	ErrUnavailable        = errors.New("wecom: connection unavailable")
	ErrDeliveryUnknown    = errors.New("wecom: message delivery is unknown")
	ErrSendRejected       = errors.New("wecom: message rejected")
	ErrRemoteTemporary    = errors.New("wecom: remote request temporarily rejected")
)

func IsPermanent(err error) bool { return errors.Is(err, ErrInvalidCredentials) }

func temporaryAck(code int) bool { return code == -1 || code == 45009 || code == 45033 }

type Message struct {
	RequestID string
	Body      json.RawMessage
}

type Handler func(context.Context, Message) error

type Config struct {
	BotID   string
	Secret  string
	OnReady func(*Session)
	Handler Handler
}

type Client struct {
	URL    string
	Logger *slog.Logger
}

func (c *Client) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *Client) Validate(ctx context.Context, botID, secret string) error {
	ready := false
	validateCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	err := c.Run(validateCtx, Config{BotID: botID, Secret: secret, OnReady: func(*Session) {
		ready = true
		cancel()
	}})
	if ready {
		return nil
	}
	return err
}

type frame struct {
	Command string `json:"cmd,omitempty"`
	Headers struct {
		RequestID string `json:"req_id"`
	} `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode int             `json:"errcode,omitempty"`
}

type Session struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan frame
	done    chan struct{}
}

func (c *Client) Run(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.BotID) == "" || strings.TrimSpace(cfg.Secret) == "" {
		return ErrInvalidCredentials
	}
	endpoint := c.URL
	if endpoint == "" {
		endpoint = DefaultURL
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	conn, response, err := websocket.Dial(dialCtx, endpoint, &websocket.DialOptions{HTTPHeader: http.Header{"User-Agent": {"OpenFox"}}})
	cancelDial()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("%w: dial failed", ErrUnavailable)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxFrameBytes)
	session := &Session{conn: conn, pending: make(map[string]chan frame), done: make(chan struct{})}
	defer close(session.done)
	requestID, err := newRequestID("aibot_subscribe")
	if err != nil {
		return err
	}
	body, _ := json.Marshal(struct {
		BotID  string `json:"bot_id"`
		Secret string `json:"secret"`
	}{cfg.BotID, cfg.Secret})
	if err := session.write(ctx, frame{Command: "aibot_subscribe", Headers: struct {
		RequestID string `json:"req_id"`
	}{requestID}, Body: body}); err != nil {
		return fmt.Errorf("%w: subscribe write failed", ErrUnavailable)
	}
	authCtx, cancelAuth := context.WithTimeout(ctx, authTimeout)
	_, payload, err := conn.Read(authCtx)
	cancelAuth()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: subscribe acknowledgement missing", ErrUnavailable)
	}
	var ack frame
	if json.Unmarshal(payload, &ack) != nil || ack.Headers.RequestID != requestID {
		return fmt.Errorf("%w: malformed subscribe acknowledgement", ErrUnavailable)
	}
	if ack.ErrCode != 0 {
		return fmt.Errorf("%w: subscribe code %d", ErrUnavailable, ack.ErrCode)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if cfg.OnReady != nil {
		cfg.OnReady(session)
	}
	go session.pingLoop(runCtx, cancelRun)
	return session.readLoop(runCtx, cfg.Handler, c.logger(), cancelRun)
}

func newRequestID(command string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return command + "_" + hex.EncodeToString(nonce[:]), nil
}

func (s *Session) write(ctx context.Context, value frame) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return s.conn.Write(writeCtx, websocket.MessageText, payload)
}

func (s *Session) request(ctx context.Context, command string, body json.RawMessage) error {
	requestID, err := newRequestID(command)
	if err != nil {
		return err
	}
	response := make(chan frame, 1)
	s.mu.Lock()
	s.pending[requestID] = response
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()
	if err := s.write(ctx, frame{Command: command, Headers: struct {
		RequestID string `json:"req_id"`
	}{requestID}, Body: body}); err != nil {
		return fmt.Errorf("%w: write failed", ErrDeliveryUnknown)
	}
	ackCtx, cancel := context.WithTimeout(ctx, ackTimeout)
	defer cancel()
	select {
	case ack := <-response:
		if ack.ErrCode != 0 {
			if temporaryAck(ack.ErrCode) {
				return fmt.Errorf("%w: code %d", ErrRemoteTemporary, ack.ErrCode)
			}
			return fmt.Errorf("%w: code %d", ErrSendRejected, ack.ErrCode)
		}
		return nil
	case <-s.done:
		return ErrDeliveryUnknown
	case <-ackCtx.Done():
		return ErrDeliveryUnknown
	}
}

func (s *Session) Send(ctx context.Context, chatID, content string) error {
	if strings.TrimSpace(chatID) == "" || strings.TrimSpace(content) == "" || len([]rune(content)) > maxMarkdownRunes {
		return ErrSendRejected
	}
	body, _ := json.Marshal(struct {
		ChatID   string `json:"chatid"`
		ChatType int    `json:"chat_type"`
		Type     string `json:"msgtype"`
		Markdown struct {
			Content string `json:"content"`
		} `json:"markdown"`
	}{ChatID: chatID, ChatType: 1, Type: "markdown", Markdown: struct {
		Content string `json:"content"`
	}{Content: content}})
	return s.request(ctx, "aibot_send_msg", body)
}

func (s *Session) pingLoop(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.request(ctx, "ping", nil); err != nil {
				cancel()
				return
			}
		}
	}
}

func (s *Session) readLoop(ctx context.Context, handler Handler, logger *slog.Logger, cancel context.CancelFunc) error {
	queue := make(chan Message, maxQueuedMessages)
	handlerFailed := make(chan struct{}, 1)
	var active sync.WaitGroup
	if handler != nil {
		for range maxHandlers {
			active.Add(1)
			go func() {
				defer active.Done()
				for message := range queue {
					for attempt := range callbackAttempts {
						if ctx.Err() != nil {
							break
						}
						handlerCtx, stop := context.WithTimeout(ctx, 10*time.Second)
						err := handler(handlerCtx, message)
						stop()
						if err == nil {
							break
						}
						if attempt == callbackAttempts-1 {
							logger.Error("wecom_callback_persistence_failed", "request_id", message.RequestID, "attempts", callbackAttempts)
							select {
							case handlerFailed <- struct{}{}:
							default:
							}
							cancel()
							break
						}
						timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
						select {
						case <-ctx.Done():
							timer.Stop()
						case <-timer.C:
						}
					}
				}
			}()
		}
	}
	defer func() {
		close(queue)
		active.Wait()
	}()
	for {
		_, payload, err := s.conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				select {
				case <-handlerFailed:
					return fmt.Errorf("%w: callback persistence failed, inbound message may be lost", ErrUnavailable)
				default:
				}
				return ctx.Err()
			}
			return fmt.Errorf("%w: read failed", ErrUnavailable)
		}
		var incoming frame
		if json.Unmarshal(payload, &incoming) != nil || incoming.Headers.RequestID == "" {
			continue
		}
		s.mu.Lock()
		pending := s.pending[incoming.Headers.RequestID]
		s.mu.Unlock()
		if pending != nil {
			select {
			case pending <- incoming:
			default:
			}
			continue
		}
		if incoming.Command != "aibot_msg_callback" || handler == nil || len(incoming.Body) == 0 {
			continue
		}
		if err := enqueueCallback(queue, Message{RequestID: incoming.Headers.RequestID, Body: incoming.Body}); err != nil {
			logger.Error("wecom_callback_queue_full", "request_id", incoming.Headers.RequestID, "queued", len(queue))
			cancel()
			return err
		}
	}
}

func enqueueCallback(queue chan<- Message, message Message) error {
	select {
	case queue <- message:
		return nil
	default:
		return fmt.Errorf("%w: callback queue full, inbound message may be lost", ErrUnavailable)
	}
}
