package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func gatewayConfig() GatewayConfig {
	return GatewayConfig{Token: testToken, BotID: "11", ApplicationID: "12", Gateway: GatewayBot{URL: "wss://gateway.discord.gg/", Shards: 1, Starts: SessionStartLimit{Total: 1000, Remaining: 10, ResetAfter: 1000, MaxConcurrency: 1}}, Intents: IntentGuilds | IntentGuildMessages | IntentDirectMessages, OnDispatch: func(context.Context, Dispatch) error { return nil }}
}
func readyJSON() map[string]any {
	return map[string]any{"v": 10, "user": map[string]any{"id": "11", "bot": true, "username": "Bot"}, "application": map[string]any{"id": "12"}, "guilds": []any{}, "session_id": "synthetic_session", "resume_gateway_url": "wss://gateway-us-east1.discord.gg/"}
}
func wsClient(t *testing.T, server func(context.Context, *websocket.Conn)) *Client {
	t.Helper()
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "10" || r.URL.Query().Get("encoding") != "json" || len(r.URL.Query()) != 2 || r.Header.Get("Authorization") != "" {
			t.Error("unsafe gateway URL/headers")
		}
		conn, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		server(ctx, conn)
	})
	return c
}
func wsWrite(ctx context.Context, c *websocket.Conn, op int, data any, seq *int64, typ string) error {
	v := map[string]any{"op": op, "d": data}
	if seq != nil {
		v["s"] = *seq
		v["t"] = typ
	}
	raw, _ := json.Marshal(v)
	return c.Write(ctx, websocket.MessageText, raw)
}
func wsReady(ctx context.Context, c *websocket.Conn) error {
	n := int64(1)
	return wsWrite(ctx, c, 0, readyJSON(), &n, "READY")
}
func readClient(t *testing.T, ctx context.Context, c *websocket.Conn, expected int) map[string]any {
	t.Helper()
	for {
		_, raw, e := c.Read(ctx)
		if e != nil {
			t.Error("client frame missing")
			return nil
		}
		var frame struct {
			Op   int             `json:"op"`
			Data json.RawMessage `json:"d"`
		}
		if json.Unmarshal(raw, &frame) != nil {
			t.Error("bad client frame")
			return nil
		}
		if frame.Op == 1 {
			_ = wsWrite(ctx, c, 11, nil, nil, "")
			continue
		}
		if frame.Op != expected {
			t.Errorf("client opcode %d want %d", frame.Op, expected)
			return nil
		}
		var data map[string]any
		if json.Unmarshal(frame.Data, &data) != nil {
			t.Error("client data malformed")
		}
		return data
	}
}
func TestGatewayIdentifyDispatchAndResume(t *testing.T) {
	var seen []Dispatch
	c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
		_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
		d := readClient(t, ctx, conn, 2)
		if d["token"] != testToken || d["intents"] != float64(IntentGuilds|IntentGuildMessages|IntentDirectMessages) {
			t.Error("wrong identify")
		}
		shard, ok := d["shard"].([]any)
		if !ok || len(shard) != 2 || shard[0] != float64(0) || shard[1] != float64(1) {
			t.Error("wrong shard")
		}
		_ = wsReady(ctx, conn)
		n := int64(2)
		_ = wsWrite(ctx, conn, 0, map[string]any{"id": "77"}, &n, "MESSAGE_CREATE")
		_ = wsWrite(ctx, conn, 7, nil, nil, "")
	})
	cfg := gatewayConfig()
	cfg.OnDispatch = func(ctx context.Context, d Dispatch) error { seen = append(seen, d); return nil }
	result, e := c.RunGateway(context.Background(), cfg)
	if !errors.Is(e, ErrGateway) || !result.Ready || result.DurableSequence != 2 || result.ReceivedSequence != 2 || len(seen) != 2 || result.SessionID != "synthetic_session" {
		t.Fatal(result, e)
	}
	c = wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
		_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
		d := readClient(t, ctx, conn, 6)
		if d["seq"] != float64(2) || d["session_id"] != "synthetic_session" || d["token"] != testToken {
			t.Error("resume did not use durable seq")
		}
		n := int64(3)
		_ = wsWrite(ctx, conn, 0, map[string]any{"id": "78"}, &n, "MESSAGE_CREATE")
		n = 4
		_ = wsWrite(ctx, conn, 0, map[string]any{}, &n, "RESUMED")
		_ = wsWrite(ctx, conn, 7, nil, nil, "")
	})
	cfg.Resume = &Resume{SessionID: result.SessionID, URL: result.ResumeURL, Sequence: result.DurableSequence}
	result, e = c.RunGateway(context.Background(), cfg)
	if !errors.Is(e, ErrGateway) || !result.Resumed || result.DurableSequence != 4 {
		t.Fatal(result, e)
	}
}
func TestGatewayPersistenceFailureDoesNotAdvanceDurableSequence(t *testing.T) {
	c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
		_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
		readClient(t, ctx, conn, 2)
		_ = wsReady(ctx, conn)
		n := int64(2)
		_ = wsWrite(ctx, conn, 0, map[string]any{}, &n, "MESSAGE_CREATE")
		_, _, _ = conn.Read(ctx)
	})
	cfg := gatewayConfig()
	cfg.OnDispatch = func(ctx context.Context, d Dispatch) error {
		if d.Type == "MESSAGE_CREATE" {
			return errors.New("synthetic database failure " + testToken)
		}
		return nil
	}
	result, e := c.RunGateway(context.Background(), cfg)
	if !errors.Is(e, ErrUnavailable) || result.DurableSequence != 1 || result.ReceivedSequence != 2 || strings.Contains(e.Error(), testToken) {
		t.Fatal(result, e)
	}
}
func TestGatewayHeartbeatAckAndServerRequest(t *testing.T) {
	for _, ack := range []bool{true, false} {
		name := "missing ack"
		if ack {
			name = "ack and request"
		}
		t.Run(name, func(t *testing.T) {
			var heartbeats atomic.Int64
			c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
				_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 50}, nil, "")
				readClient(t, ctx, conn, 2)
				_ = wsReady(ctx, conn)
				for {
					_, raw, e := conn.Read(ctx)
					if e != nil {
						return
					}
					var f struct {
						Op   int    `json:"op"`
						Data *int64 `json:"d"`
					}
					if json.Unmarshal(raw, &f) != nil || f.Op != 1 {
						t.Error("heartbeat missing")
						return
					}
					n := heartbeats.Add(1)
					// The initial jitter can expire before READY has been read.
					if f.Data != nil && *f.Data != 1 || n > 1 && f.Data == nil {
						t.Error("heartbeat not received seq")
					}
					if ack {
						_ = wsWrite(ctx, conn, 11, nil, nil, "")
						if n == 1 {
							_ = wsWrite(ctx, conn, 1, nil, nil, "")
						}
						if n == 2 {
							_ = wsWrite(ctx, conn, 7, nil, nil, "")
							return
						}
					}
				}
			})
			start := time.Now()
			_, e := c.RunGateway(context.Background(), gatewayConfig())
			if !errors.Is(e, ErrGateway) || time.Since(start) > 2*time.Second {
				t.Fatal(e)
			}
			if ack && heartbeats.Load() != 2 || !ack && heartbeats.Load() != 1 {
				t.Fatal(heartbeats.Load())
			}
		})
	}
}
func TestGatewayCloseCodesAndInvalidSession(t *testing.T) {
	for _, code := range []int{4004, 4007, 4009, 4011, 4014} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
				_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
				readClient(t, ctx, conn, 2)
				_ = wsReady(ctx, conn)
				_ = conn.Close(websocket.StatusCode(code), "synthetic secret "+testToken)
			})
			r, e := c.RunGateway(context.Background(), gatewayConfig())
			var close *Error
			if !errors.As(e, &close) || close.CloseCode != code || strings.Contains(e.Error(), testToken) {
				t.Fatal(r, e)
			}
			if (code == 4007 || code == 4009) != r.IdentifyRequired {
				t.Fatal(r)
			}
			if code == 4004 && !errors.Is(e, ErrAuth) {
				t.Fatal(e)
			}
			if (code == 4011 || code == 4014) && !errors.Is(e, ErrUnsupported) {
				t.Fatal(e)
			}
		})
	}
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid new", true: "invalid resumable"}[resume], func(t *testing.T) {
			c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
				_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
				readClient(t, ctx, conn, 2)
				_ = wsReady(ctx, conn)
				_ = wsWrite(ctx, conn, 9, resume, nil, "")
			})
			r, e := c.RunGateway(context.Background(), gatewayConfig())
			if !errors.Is(e, ErrGateway) || r.IdentifyRequired == resume {
				t.Fatal(r, e)
			}
		})
	}
}
func TestGatewayReadyIdentityCapacityAndBudgets(t *testing.T) {
	for _, mode := range []string{"wrong bot", "wrong app", "unsafe resume", "duplicate json", "big frame", "oversized identify"} {
		t.Run(mode, func(t *testing.T) {
			var dispatched atomic.Int64
			c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
				if mode == "duplicate json" {
					_ = conn.Write(ctx, websocket.MessageText, []byte(`{"op":10,"op":7,"d":null}`))
					return
				}
				if mode == "big frame" {
					_ = conn.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", MaxGatewayBytes+1)))
					return
				}
				_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
				if mode == "oversized identify" {
					_, _, _ = conn.Read(ctx)
					return
				}
				readClient(t, ctx, conn, 2)
				data := readyJSON()
				switch mode {
				case "wrong bot":
					data["user"] = map[string]any{"id": "13", "bot": true}
				case "wrong app":
					data["application"] = map[string]any{"id": "13"}
				case "unsafe resume":
					data["resume_gateway_url"] = "wss://untrusted.example/"
				}
				n := int64(1)
				_ = wsWrite(ctx, conn, 0, data, &n, "READY")
			})
			cfg := gatewayConfig()
			cfg.OnDispatch = func(context.Context, Dispatch) error { dispatched.Add(1); return nil }
			if mode == "oversized identify" {
				cfg.Token = strings.Repeat("a", 4096)
			}
			_, e := c.RunGateway(context.Background(), cfg)
			if e == nil || dispatched.Load() != 0 || strings.Contains(e.Error(), testToken) {
				t.Fatal(e)
			}
		})
	}
	for _, mode := range []string{"shards", "zero budget"} {
		t.Run(mode, func(t *testing.T) {
			cfg := gatewayConfig()
			if mode == "shards" {
				cfg.Gateway.Shards = 2
			} else {
				cfg.Gateway.Starts.Remaining = 0
			}
			c := NewClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("must not dial")
				return nil, errors.New("unexpected dial")
			}))
			_, e := c.RunGateway(context.Background(), cfg)
			if mode == "shards" && !errors.Is(e, ErrUnsupported) || mode == "zero budget" && !errors.Is(e, ErrRateLimited) {
				t.Fatal(e)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGatewayCancellationSequenceAndWriteBudget(t *testing.T) {
	t.Run("cancellation during persistence", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
			_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
			readClient(t, ctx, conn, 2)
			_ = wsReady(ctx, conn)
			_, _, _ = conn.Read(ctx)
		})
		cfg := gatewayConfig()
		cfg.OnDispatch = func(ctx context.Context, d Dispatch) error { cancel(); <-ctx.Done(); return ctx.Err() }
		got, e := c.RunGateway(ctx, cfg)
		if !errors.Is(e, ErrUnavailable) || got.DurableSequence != -1 || got.ReceivedSequence != 1 {
			t.Fatal(got, e)
		}
	})
	t.Run("decreasing sequence", func(t *testing.T) {
		c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
			_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 500}, nil, "")
			readClient(t, ctx, conn, 2)
			_ = wsReady(ctx, conn)
			n := int64(0)
			_ = wsWrite(ctx, conn, 0, map[string]any{}, &n, "MESSAGE_CREATE")
		})
		got, e := c.RunGateway(context.Background(), gatewayConfig())
		if !errors.Is(e, ErrResponse) || got.DurableSequence != 1 {
			t.Fatal(got, e)
		}
	})
	t.Run("120 command budget", func(t *testing.T) {
		var writes atomic.Int64
		c := wsClient(t, func(ctx context.Context, conn *websocket.Conn) {
			_ = wsWrite(ctx, conn, 10, map[string]int{"heartbeat_interval": 60000}, nil, "")
			readClient(t, ctx, conn, 2)
			writes.Add(1)
			_ = wsReady(ctx, conn)
			for n := 0; n < 120; n++ {
				_ = wsWrite(ctx, conn, 1, nil, nil, "")
				_, raw, e := conn.Read(ctx)
				if e != nil {
					return
				}
				var v struct {
					Op int `json:"op"`
				}
				if json.Unmarshal(raw, &v) != nil || v.Op != 1 {
					t.Error("wrong command")
					return
				}
				writes.Add(1)
				_ = wsWrite(ctx, conn, 11, nil, nil, "")
			}
		})
		_, e := c.RunGateway(context.Background(), gatewayConfig())
		if !errors.Is(e, ErrRateLimited) || writes.Load() != 120 {
			t.Fatal(e, writes.Load())
		}
	})
}
func TestGatewayCertificateAndRedirect(t *testing.T) {
	t.Run("untrusted certificate", func(t *testing.T) {
		_, s := tlsClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted handshake accepted") })
		u, _ := url.Parse(s.URL)
		c := NewClient(fixtureTransport{u, http.DefaultTransport})
		_, e := c.RunGateway(context.Background(), gatewayConfig())
		if !errors.Is(e, ErrUnavailable) || strings.Contains(e.Error(), testToken) {
			t.Fatal(e)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var count atomic.Int64
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			http.Redirect(w, r, "https://untrusted.example/"+testToken, 302)
		})
		_, e := c.RunGateway(context.Background(), gatewayConfig())
		if !errors.Is(e, ErrUnavailable) || count.Load() != 1 || strings.Contains(e.Error(), testToken) {
			t.Fatal(e)
		}
	})
}
