package feishu_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/integrations/feishu"
	fakefeishu "openfox/tests/support/fakefeishu/fake"
)

const (
	appID  = "cli_testapp0001"
	secret = "secret-0123456789abcdef"
)

func newFake(t *testing.T) (*fakefeishu.Server, *feishu.Client) {
	t.Helper()
	fake := fakefeishu.New()
	t.Cleanup(fake.Close)
	fake.AddApp(appID, secret, "测试机器人")
	return fake, feishu.New(fake.URL, fake.URL)
}

type session struct {
	done      chan error
	connected chan struct{}
	received  chan string
	cancel    context.CancelFunc
}

func start(t *testing.T, client *feishu.Client, handler feishu.Handler) *session {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{done: make(chan error, 1), connected: make(chan struct{}, 4), received: make(chan string, 16), cancel: cancel}
	if handler == nil {
		handler = func(_ context.Context, payload []byte) ([]byte, error) {
			s.received <- string(payload)
			return []byte(`{"ok":true}`), nil
		}
	}
	go func() {
		s.done <- client.Run(ctx, feishu.Config{AppID: appID, Secret: secret, Handler: handler,
			OnConnected: func() { s.connected <- struct{}{} }})
	}()
	t.Cleanup(cancel)
	select {
	case <-s.connected:
	case err := <-s.done:
		t.Fatalf("session ended before connecting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("session did not connect")
	}
	return s
}

func (s *session) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-s.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end")
		return nil
	}
}

func TestEventIsAcknowledgedWithHandlerData(t *testing.T) {
	fake, client := newFake(t)
	s := start(t, client, nil)
	data, err := fake.PushEvent(appID, "im.message.receive_v1", `{"message":{"text":"hi"}}`)
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("PushEvent = %q, %v", data, err)
	}
	if got := <-s.received; !strings.Contains(got, `"event_type":"im.message.receive_v1"`) {
		t.Fatalf("handler payload = %s", got)
	}
}

func TestHandlerErrorReturnsCode500(t *testing.T) {
	fake, client := newFake(t)
	start(t, client, func(context.Context, []byte) ([]byte, error) { return nil, errors.New("boom") })
	if _, err := fake.PushEvent(appID, "x", ""); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want ack code 500", err)
	}
}

func TestSplitFramesAreCombined(t *testing.T) {
	fake, client := newFake(t)
	s := start(t, client, nil)
	payload := []byte(`{"schema":"2.0","header":{"event_id":"e1","event_type":"t","app_id":"` + appID + `"},"event":{"pad":"` + strings.Repeat("x", 600) + `"}}`)
	if _, err := fake.PushRaw(appID, payload, 4); err != nil {
		t.Fatal(err)
	}
	if got := <-s.received; got != string(payload) {
		t.Fatalf("combined payload differs (%d bytes)", len(got))
	}
}

func TestPongUpdatesPingInterval(t *testing.T) {
	fake, client := newFake(t)
	fake.SetPingInterval(1)
	start(t, client, nil)
	deadline := time.Now().Add(4 * time.Second)
	for fake.PingCount(appID) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("pings = %d, want at least 3 with a 1s interval", fake.PingCount(appID))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestDroppedConnectionIsRetryable(t *testing.T) {
	fake, client := newFake(t)
	s := start(t, client, nil)
	fake.DropConnections(appID)
	err := s.wait(t)
	var retry *feishu.RetryError
	if !errors.As(err, &retry) || feishu.IsPermanent(err) {
		t.Fatalf("err = %v, want retryable", err)
	}
	if retry.Interval != time.Second {
		t.Fatalf("reconnect interval = %v, want the server-provided 1s", retry.Interval)
	}
}

func TestContextCancelStopsSession(t *testing.T) {
	_, client := newFake(t)
	s := start(t, client, nil)
	s.cancel()
	if err := s.wait(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestHandshakeFailures(t *testing.T) {
	fake, client := newFake(t)
	run := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return client.Run(ctx, feishu.Config{AppID: appID, Secret: secret})
	}
	fake.SetHandshakeStatus(514, 1000040350)
	if err := run(); !errors.Is(err, feishu.ErrConnectionLimit) || !feishu.IsPermanent(err) {
		t.Fatalf("limit err = %v", err)
	}
	fake.SetHandshakeStatus(514, 1000040341)
	if err := run(); !errors.Is(err, feishu.ErrInvalidCredentials) {
		t.Fatalf("auth err = %v", err)
	}
	fake.SetHandshakeStatus(403, 0)
	if err := run(); !errors.Is(err, feishu.ErrInvalidCredentials) {
		t.Fatalf("forbidden err = %v", err)
	}
	fake.SetHandshakeStatus(1, 0)
	if err := run(); feishu.IsPermanent(err) || err == nil {
		t.Fatalf("busy err = %v, want retryable", err)
	}
}

func TestEndpointCodes(t *testing.T) {
	fake, client := newFake(t)
	fake.SetEndpointCode(1000040343)
	_, err := client.FetchEndpoint(context.Background(), appID, secret)
	var retry *feishu.RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("internal error code: %v, want retryable", err)
	}
	fake.SetEndpointCode(1000040345)
	if _, err := client.FetchEndpoint(context.Background(), appID, secret); !errors.Is(err, feishu.ErrInvalidCredentials) {
		t.Fatalf("auth code: %v", err)
	}
	fake.SetEndpointCode(0)
	if _, err := client.FetchEndpoint(context.Background(), appID, "wrong-secret-0000000"); !errors.Is(err, feishu.ErrInvalidCredentials) {
		t.Fatalf("wrong secret: %v", err)
	}
	fake.SetServerError(true)
	if _, err := client.FetchEndpoint(context.Background(), appID, secret); !errors.As(err, &retry) {
		t.Fatalf("http 500: %v, want retryable", err)
	}
}

func TestEndpointURLValidation(t *testing.T) {
	cases := []struct {
		base, endpoint string
		ok             bool
	}{
		{"https://open.feishu.cn", "wss://msg-frontier.feishu.cn/ws?device_id=1", true},
		{"https://open.feishu.cn", "wss://open.feishu.cn/ws", true},
		{"https://open.feishu.cn", "wss://feishu.cn/ws", false},
		{"https://open.feishu.cn", "wss://evil.example.com/ws", false},
		{"https://open.feishu.cn", "wss://open.feishu.cn.evil.com/ws", false},
		{"https://open.feishu.cn", "wss://evilfeishu.cn/ws", false},
		{"https://open.feishu.cn", "ws://msg-frontier.feishu.cn/ws", false},
		{"https://open.feishu.cn", "wss://1.2.3.4/ws", false},
		{"https://open.feishu.cn", "wss://user:pw@msg.feishu.cn/ws", false},
		{"https://open.feishu.cn", "wss://msg.feishu.cn:8443/ws", false},
		{"http://127.0.0.1:18097", "ws://127.0.0.1:18097/ws", true},
		{"http://127.0.0.1:18097", "ws://127.0.0.1:9/ws", false},
		{"http://127.0.0.1:18097", "ws://10.0.0.1:18097/ws", false},
		{"https://127.0.0.1:18097", "ws://127.0.0.1:18097/ws", false},
	}
	for _, c := range cases {
		client := feishu.New(c.base, c.base)
		if got := client.ValidateEndpoint(c.endpoint) == nil; got != c.ok {
			t.Errorf("base %s endpoint %s: accepted = %v, want %v", c.base, c.endpoint, got, c.ok)
		}
	}
}

func TestConcurrentEventsAreHandledInParallel(t *testing.T) {
	fake, client := newFake(t)
	var running, peak atomic.Int32
	start(t, client, func(context.Context, []byte) ([]byte, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		running.Add(-1)
		return nil, nil
	})
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { _, err := fake.PushEvent(appID, "x", ""); errs <- err }()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrency = %d, want handlers to run concurrently", peak.Load())
	}
}
