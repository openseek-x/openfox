package discord

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

func validGatewayURL(raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "wss" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawQuery != "" || u.Path != "" && u.Path != "/" || u.RawPath != "" {
		return false
	}
	host := u.Hostname()
	if host == "gateway.discord.gg" {
		return true
	}
	if !strings.HasPrefix(host, "gateway-") || !strings.HasSuffix(host, ".discord.gg") {
		return false
	}
	label := strings.TrimSuffix(host, ".discord.gg")
	if len(label) > 63 {
		return false
	}
	for _, r := range label {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

type gatewayFrame struct {
	Op       *int            `json:"op"`
	Data     json.RawMessage `json:"d"`
	Sequence *int64          `json:"s"`
	Type     *string         `json:"t"`
}

func readGateway(ctx context.Context, conn *websocket.Conn) (gatewayFrame, error) {
	kind, raw, e := conn.Read(ctx)
	if e != nil {
		return gatewayFrame{}, e
	}
	var f gatewayFrame
	if kind != websocket.MessageText || decodeObject(raw, &f) != nil || f.Op == nil {
		return f, ErrResponse
	}
	return f, nil
}
func gatewayError(err error, result *GatewayResult) error {
	code := int(websocket.CloseStatus(err))
	out := &Error{Kind: ErrGateway, Code: "gateway_disconnected", CloseCode: code}
	switch code {
	case 4004:
		out.Kind, out.Code = ErrAuth, "credentials_invalid"
	case 4010, 4011, 4012, 4013, 4014:
		out.Kind, out.Code = ErrUnsupported, "gateway_capability_unsupported"
	case 4007, 4009, 1000, 1001:
		result.IdentifyRequired = true
		out.Code = "gateway_session_lost"
	case 4008:
		out.Code = "gateway_rate_limited"
	}
	return out
}

// RunGateway owns exactly one physical connection; durable state, lease checks,
// Identify budgets across instances and reconnect policy belong to its caller.
// No automatic reconnect or HTTP command replay occurs here.
func (c *Client) RunGateway(ctx context.Context, cfg GatewayConfig) (GatewayResult, error) {
	result := GatewayResult{ReceivedSequence: -1, DurableSequence: -1}
	if !tokenValid(cfg.Token) || !cfg.BotID.Valid() || !cfg.ApplicationID.Valid() || cfg.OnDispatch == nil || !validGatewayURL(cfg.Gateway.URL) || cfg.Gateway.Shards < 1 || cfg.Intents != IntentGuilds|IntentGuildMessages|IntentDirectMessages && cfg.Intents != IntentGuilds|IntentGuildMessages|IntentDirectMessages|IntentMessageContent {
		return result, ErrInvalid
	}
	if cfg.Gateway.Shards != 1 {
		return result, failure(ErrUnsupported, "unsupported_sharding")
	}
	rawURL := cfg.Gateway.URL
	if cfg.Resume != nil {
		if cfg.Resume.SessionID == "" || len(cfg.Resume.SessionID) > 256 || !validGatewayURL(cfg.Resume.URL) || cfg.Resume.Sequence < 0 {
			return result, ErrInvalid
		}
		result.SessionID, result.ResumeURL = cfg.Resume.SessionID, cfg.Resume.URL
		result.ReceivedSequence, result.DurableSequence = cfg.Resume.Sequence, cfg.Resume.Sequence
		rawURL = cfg.Resume.URL
	} else if cfg.Gateway.Starts.Remaining <= 0 {
		delay := time.Duration(0)
		if cfg.Gateway.Starts.ResetAfter > 0 && cfg.Gateway.Starts.ResetAfter <= math.MaxInt64/int64(time.Millisecond) {
			delay = time.Duration(cfg.Gateway.Starts.ResetAfter) * time.Millisecond
		}
		return result, &Error{Kind: ErrRateLimited, Code: "identify_budget_exhausted", RetryAfter: delay}
	}
	u, _ := url.Parse(rawURL)
	u.RawQuery = "v=10&encoding=json"
	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	conn, _, e := websocket.Dial(sessionCtx, u.String(), &websocket.DialOptions{HTTPClient: c.httpClient(10 * time.Second), HTTPHeader: http.Header{"User-Agent": {userAgent}}, CompressionMode: websocket.CompressionDisabled})
	if e != nil {
		return result, failure(ErrUnavailable, "gateway_connect_failed")
	}
	defer conn.CloseNow()
	conn.SetReadLimit(MaxGatewayBytes)
	helloCtx, helloCancel := context.WithTimeout(sessionCtx, 10*time.Second)
	hello, e := readGateway(helloCtx, conn)
	helloCancel()
	if e != nil {
		return result, gatewayError(e, &result)
	}
	if *hello.Op == 7 {
		return result, failure(ErrGateway, "gateway_reconnect")
	}
	if *hello.Op != 10 {
		return result, failure(ErrResponse, "gateway_hello_required")
	}
	var h struct {
		Interval int64 `json:"heartbeat_interval"`
	}
	if decodeObject(hello.Data, &h) != nil || h.Interval <= 0 || h.Interval > int64((5*time.Minute)/time.Millisecond) {
		return result, failure(ErrResponse, "gateway_heartbeat_invalid")
	}
	interval := time.Duration(h.Interval) * time.Millisecond
	var writeMu sync.Mutex
	sent := make([]time.Time, 0, 120)
	write := func(op int, data any) error {
		raw, e := json.Marshal(struct {
			Op   int `json:"op"`
			Data any `json:"d"`
		}{op, data})
		if e != nil || len(raw) > MaxGatewayWriteBytes {
			return failure(ErrInvalid, "gateway_write_capacity")
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		now := time.Now()
		for len(sent) > 0 && now.Sub(sent[0]) >= time.Minute {
			sent = sent[1:]
		}
		if len(sent) >= 120 {
			return failure(ErrRateLimited, "gateway_write_budget")
		}
		sent = append(sent, now)
		writeCtx, stop := context.WithTimeout(sessionCtx, 5*time.Second)
		defer stop()
		if conn.Write(writeCtx, websocket.MessageText, raw) != nil {
			return failure(ErrGateway, "gateway_write_failed")
		}
		return nil
	}
	if cfg.Resume != nil {
		e = write(6, map[string]any{"token": cfg.Token, "session_id": cfg.Resume.SessionID, "seq": cfg.Resume.Sequence})
	} else {
		e = write(2, map[string]any{"token": cfg.Token, "intents": cfg.Intents, "properties": map[string]string{"os": runtime.GOOS, "browser": "OpenFox", "device": "OpenFox"}, "shard": [2]int{0, 1}})
	}
	if e != nil {
		return result, e
	}
	var sequence atomic.Int64
	sequence.Store(result.ReceivedSequence)
	var pending atomic.Bool
	heartbeat := func() error {
		var seq any
		if n := sequence.Load(); n >= 0 {
			seq = n
		}
		pending.Store(true)
		return write(1, seq)
	}
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		var randomness [8]byte
		if _, e := rand.Read(randomness[:]); e != nil {
			heartbeatErrors <- failure(ErrUnavailable, "random_unavailable")
			cancel()
			return
		}
		jitter := time.Duration(binary.LittleEndian.Uint64(randomness[:]) % uint64(interval))
		timer := time.NewTimer(jitter)
		defer timer.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-timer.C:
				if pending.Load() {
					heartbeatErrors <- failure(ErrGateway, "gateway_heartbeat_unacknowledged")
					cancel()
					return
				}
				if e := heartbeat(); e != nil {
					heartbeatErrors <- e
					cancel()
					return
				}
				timer.Reset(interval)
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	for {
		frame, err := readGateway(sessionCtx, conn)
		if err != nil {
			select {
			case e := <-heartbeatErrors:
				return result, e
			default:
			}
			if ctx.Err() != nil {
				return result, failure(ErrUnavailable, "gateway_cancelled")
			}
			if errors.Is(err, ErrResponse) {
				return result, err
			}
			return result, gatewayError(err, &result)
		}
		switch *frame.Op {
		case 0:
			if frame.Sequence == nil || *frame.Sequence < 0 || *frame.Sequence < result.ReceivedSequence || frame.Type == nil || *frame.Type == "" || len(*frame.Type) > 100 || len(frame.Data) == 0 {
				return result, failure(ErrResponse, "gateway_dispatch_invalid")
			}
			seq, typ := *frame.Sequence, *frame.Type
			result.ReceivedSequence = seq
			sequence.Store(seq)
			var ready Ready
			if typ == "READY" {
				if cfg.Resume != nil || result.Ready || decodeObject(frame.Data, &ready) != nil || ready.Version != 10 || ready.User.ID != cfg.BotID || !ready.User.Bot || ready.Application.ID != cfg.ApplicationID || ready.SessionID == "" || len(ready.SessionID) > 256 || !validGatewayURL(ready.ResumeURL) || len(ready.Shard) != 0 && (len(ready.Shard) != 2 || ready.Shard[0] != 0 || ready.Shard[1] != 1) {
					return result, failure(ErrResponse, "gateway_ready_invalid")
				}
			}
			if typ == "RESUMED" && cfg.Resume == nil {
				return result, failure(ErrResponse, "gateway_resume_invalid")
			}
			if typ != "READY" && typ != "RESUMED" && !result.Ready && cfg.Resume == nil {
				return result, failure(ErrResponse, "gateway_ready_required")
			}
			// One event at a time; the caller commits both lifecycle/queue and seq.
			commitCtx, stop := context.WithTimeout(sessionCtx, 10*time.Second)
			err = cfg.OnDispatch(commitCtx, Dispatch{seq, typ, frame.Data})
			stop()
			if err != nil {
				return result, failure(ErrUnavailable, "gateway_persistence_failed")
			}
			result.DurableSequence = seq
			if typ == "READY" {
				result.Ready = true
				result.SessionID, result.ResumeURL = ready.SessionID, ready.ResumeURL
			}
			if typ == "RESUMED" {
				result.Resumed = true
				result.Ready = true
			}
		case 1:
			if e := heartbeat(); e != nil {
				return result, e
			}
		case 7:
			return result, failure(ErrGateway, "gateway_reconnect")
		case 9:
			var resumable bool
			if json.Unmarshal(frame.Data, &resumable) != nil || string(frame.Data) == "null" {
				return result, ErrResponse
			}
			result.IdentifyRequired = !resumable
			return result, failure(ErrGateway, "gateway_invalid_session")
		case 11:
			pending.Store(false)
		default:
			return result, failure(ErrResponse, "gateway_opcode_unsupported")
		}
	}
}
