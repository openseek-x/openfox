package wecom

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSessionAuthenticatesReceivesAndSends(t *testing.T) {
	message := make(chan Message, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		var auth frame
		if err := json.Unmarshal(payload, &auth); err != nil || auth.Command != "aibot_subscribe" {
			t.Errorf("invalid subscribe: %s, %v", payload, err)
			return
		}
		var credentials map[string]string
		if err := json.Unmarshal(auth.Body, &credentials); err != nil || credentials["bot_id"] != "bot-id" || credentials["secret"] != "secret" {
			t.Errorf("invalid credentials: %v, %v", credentials, err)
			return
		}
		ack, _ := json.Marshal(frame{Headers: auth.Headers})
		if err := conn.Write(r.Context(), websocket.MessageText, ack); err != nil {
			t.Error(err)
			return
		}
		callback := []byte(`{"cmd":"aibot_msg_callback","headers":{"req_id":"callback-1"},"body":{"msgid":"message-1","text":{"content":"hello"}}}`)
		if err := conn.Write(r.Context(), websocket.MessageText, callback); err != nil {
			t.Error(err)
			return
		}
		_, payload, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		var sent frame
		if err := json.Unmarshal(payload, &sent); err != nil || sent.Command != "aibot_send_msg" {
			t.Errorf("invalid send: %s, %v", payload, err)
			return
		}
		var body struct {
			ChatID   string `json:"chatid"`
			ChatType int    `json:"chat_type"`
			Type     string `json:"msgtype"`
			Markdown struct {
				Content string `json:"content"`
			} `json:"markdown"`
		}
		if err := json.Unmarshal(sent.Body, &body); err != nil || body.ChatID != "staff-id" || body.ChatType != 1 || body.Type != "markdown" || body.Markdown.Content != "notice" {
			t.Errorf("invalid send body: %+v, %v", body, err)
			return
		}
		ack, _ = json.Marshal(frame{Headers: sent.Headers})
		_ = conn.Write(r.Context(), websocket.MessageText, ack)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := make(chan *Session, 1)
	done := make(chan error, 1)
	go func() {
		done <- (&Client{URL: "ws" + strings.TrimPrefix(server.URL, "http")}).Run(ctx, Config{
			BotID: "bot-id", Secret: "secret", OnReady: func(session *Session) { ready <- session },
			Handler: func(_ context.Context, incoming Message) error { message <- incoming; return nil },
		})
	}()
	var session *Session
	select {
	case session = <-ready:
	case <-ctx.Done():
		t.Fatal("session did not authenticate")
	}
	if err := session.Send(ctx, "staff-id", "notice"); err != nil {
		t.Fatal(err)
	}
	select {
	case incoming := <-message:
		if incoming.RequestID != "callback-1" || !strings.Contains(string(incoming.Body), "message-1") {
			t.Fatalf("unexpected callback: %+v", incoming)
		}
	case <-ctx.Done():
		t.Fatal("callback not dispatched")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestUnknownSubscribeRejectionIsRecoverable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			t.Error(err)
			return
		}
		var auth frame
		_ = json.Unmarshal(payload, &auth)
		ack, _ := json.Marshal(frame{Headers: auth.Headers, ErrCode: 40001})
		_ = conn.Write(r.Context(), websocket.MessageText, ack)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := (&Client{URL: "ws" + strings.TrimPrefix(server.URL, "http")}).Run(ctx, Config{BotID: "bot-id", Secret: "wrong"})
	if !errors.Is(err, ErrUnavailable) || IsPermanent(err) {
		t.Fatalf("unknown subscribe code must not claim invalid credentials: %v", err)
	}
}

func TestCallbackPersistenceRetriesAndWarns(t *testing.T) {
	for _, test := range []struct {
		name       string
		alwaysFail bool
	}{
		{name: "recovers"},
		{name: "exhausted", alwaysFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.CloseNow()
				_, payload, err := conn.Read(r.Context())
				if err != nil {
					return
				}
				var auth frame
				_ = json.Unmarshal(payload, &auth)
				ack, _ := json.Marshal(frame{Headers: auth.Headers})
				if err := conn.Write(r.Context(), websocket.MessageText, ack); err != nil {
					return
				}
				callback := []byte(`{"cmd":"aibot_msg_callback","headers":{"req_id":"callback-retry"},"body":{"msgid":"message-1"}}`)
				if err := conn.Write(r.Context(), websocket.MessageText, callback); err != nil {
					return
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var attempts atomic.Int32
			var logs bytes.Buffer
			recovered := make(chan struct{}, 1)
			done := make(chan error, 1)
			go func() {
				done <- (&Client{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Logger: slog.New(slog.NewTextHandler(&logs, nil))}).Run(ctx, Config{
					BotID: "bot", Secret: "secret",
					Handler: func(_ context.Context, message Message) error {
						if message.RequestID != "callback-retry" {
							t.Errorf("unexpected callback: %s", message.RequestID)
						}
						count := attempts.Add(1)
						if test.alwaysFail || count < callbackAttempts {
							return errors.New("database unavailable")
						}
						recovered <- struct{}{}
						return nil
					},
				})
			}()
			if test.alwaysFail {
				select {
				case err := <-done:
					if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "may be lost") {
						t.Fatalf("exhausted callback retries should disconnect: %v", err)
					}
				case <-ctx.Done():
					t.Fatal("callback did not disconnect")
				}
				if !strings.Contains(logs.String(), "wecom_callback_persistence_failed") || !strings.Contains(logs.String(), "callback-retry") {
					t.Fatalf("missing structured persistence warning: %s", logs.String())
				}
			} else {
				select {
				case <-recovered:
				case <-ctx.Done():
					t.Fatal("callback did not recover")
				}
				cancel()
				<-done
			}
			if got := attempts.Load(); got != callbackAttempts {
				t.Fatalf("expected %d bounded attempts, got %d", callbackAttempts, got)
			}
		})
	}
}

func TestSendAckWhileCallbackHandlersBusy(t *testing.T) {
	started := make(chan struct{}, maxHandlers+1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var auth frame
		_ = json.Unmarshal(payload, &auth)
		ack, _ := json.Marshal(frame{Headers: auth.Headers})
		if err := conn.Write(r.Context(), websocket.MessageText, ack); err != nil {
			return
		}
		for index := range maxHandlers + 1 {
			callback := []byte(`{"cmd":"aibot_msg_callback","headers":{"req_id":"callback-` + string(rune('a'+index)) + `"},"body":{"msgid":"message"}}`)
			if err := conn.Write(r.Context(), websocket.MessageText, callback); err != nil {
				return
			}
		}
		_, payload, err = conn.Read(r.Context())
		if err != nil {
			return
		}
		var sent frame
		_ = json.Unmarshal(payload, &sent)
		ack, _ = json.Marshal(frame{Headers: sent.Headers})
		_ = conn.Write(r.Context(), websocket.MessageText, ack)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ready := make(chan *Session, 1)
	done := make(chan error, 1)
	go func() {
		done <- (&Client{URL: "ws" + strings.TrimPrefix(server.URL, "http")}).Run(ctx, Config{
			BotID: "bot", Secret: "secret", OnReady: func(session *Session) { ready <- session },
			Handler: func(ctx context.Context, _ Message) error {
				started <- struct{}{}
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			},
		})
	}()
	var session *Session
	select {
	case session = <-ready:
	case <-ctx.Done():
		t.Fatal("session did not authenticate")
	}
	for range maxHandlers {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("callback handlers did not saturate")
		}
	}
	if err := session.Send(ctx, "member", "reply"); err != nil {
		t.Fatalf("send acknowledgement blocked by busy callbacks: %v", err)
	}
	close(release)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("client did not stop")
	}
}

func TestCallbackQueueFullReturnsRecoverableError(t *testing.T) {
	queue := make(chan Message, 1)
	if err := enqueueCallback(queue, Message{RequestID: "first"}); err != nil {
		t.Fatal(err)
	}
	err := enqueueCallback(queue, Message{RequestID: "second"})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "may be lost") {
		t.Fatalf("full queue should terminate the connection with an observable loss warning: %v", err)
	}
}

func TestTemporaryRemoteAckCodes(t *testing.T) {
	for _, code := range []int{-1, 45009, 45033} {
		if !temporaryAck(code) {
			t.Fatalf("code %d should allow bounded retry", code)
		}
	}
	if temporaryAck(40001) {
		t.Fatal("invalid credentials must not be retried as rate limiting")
	}
}
