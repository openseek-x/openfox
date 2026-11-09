package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openfox/internal/devices"
)

func TestBackoffIsBoundedAndJittered(t *testing.T) {
	backoff, err := NewBackoff(BackoffConfig{
		Base: time.Second, Max: 4 * time.Second, Jitter: 0.5,
		Rand: func() float64 { return 1 },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{1500 * time.Millisecond, 3 * time.Second, 4 * time.Second, 4 * time.Second}
	for index, expected := range want {
		if got := backoff.Next(); got != expected {
			t.Fatalf("delay %d = %s, want %s", index, got, expected)
		}
	}
	backoff.Reset()
	if got := backoff.Next(); got != 1500*time.Millisecond {
		t.Fatalf("reset delay = %s", got)
	}
}

func TestReadTokenFileRequiresPrivateRegularFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "token")
	if err := os.WriteFile(path, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := ReadTokenFile(path)
	if err != nil || token != "secret-token" {
		t.Fatalf("ReadTokenFile() = %q, %v", token, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(path); !errors.Is(err, ErrRemoteConfiguration) {
		t.Fatalf("world-readable token error = %v", err)
	}
}

func TestRemoteClientStopsOnContextCancellation(t *testing.T) {
	source := NewStaticHeartbeatSource(testHeartbeat())
	client, err := NewRemoteClient(RemoteClientConfig{
		URL: "wss://example.test/device/connect", DeviceID: "device-1",
		TokenProvider:   func(context.Context) (string, error) { return "token", nil },
		HeartbeatSource: source, Dialer: failingDialer{},
		Backoff: BackoffConfig{Base: time.Hour, Max: time.Hour},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

type failingDialer struct{}

func (failingDialer) Dial(context.Context, string, http.Header) (MessageConnection, error) {
	return nil, errors.New("dial failed")
}

func testHeartbeat() devices.Heartbeat {
	return devices.Heartbeat{
		CenterEpoch: 1, ProtocolMajor: ProtocolMajor, ProtocolMinor: 0,
		DaemonVersion: "test", OperatingSystem: "test", Architecture: "test",
		Capabilities: []byte(`{"runtime":"test"}`),
	}
}

// scriptedConnection 在收到 hello 后回 ack，然后依次推送脚本消息，最后阻塞到关闭。
type scriptedConnection struct {
	hello  chan Message
	script []func(hello Message) Message
	closed chan struct{}
}

func (c *scriptedConnection) Write(_ context.Context, payload []byte) error {
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return err
	}
	if message.Type == "hello" {
		c.hello <- message
	}
	return nil
}

func (c *scriptedConnection) Read(ctx context.Context) ([]byte, error) {
	hello := <-c.hello
	c.hello <- hello
	if len(c.script) == 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.closed:
			return nil, errors.New("closed")
		}
	}
	next := c.script[0]
	c.script = c.script[1:]
	return json.Marshal(next(hello))
}

func (c *scriptedConnection) Ping(context.Context) error { return nil }
func (c *scriptedConnection) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

type scriptedDialer struct{ connection *scriptedConnection }

func (d scriptedDialer) Dial(context.Context, string, http.Header) (MessageConnection, error) {
	return d.connection, nil
}

func TestRemoteClientDeliversRunMessages(t *testing.T) {
	ack := func(hello Message) Message {
		payload, _ := json.Marshal(HeartbeatAckPayload{DeviceID: "device-1", CenterEpoch: 1, Version: 1, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		return Message{Envelope: Envelope{ProtocolMajor: ProtocolMajor, MessageID: hello.MessageID, CenterEpoch: 1, Type: "hello.ack", ComputerID: "device-1"}, Payload: payload}
	}
	wake := func(Message) Message {
		return Message{Envelope: Envelope{
			ProtocolMajor: ProtocolMajor, MessageID: "m-2", CenterEpoch: 1, Type: MessageRunDispatch,
			ComputerID: "device-1", OrganizationID: "org-1", RunID: "run-1", DispatchGeneration: 1,
		}, Payload: json.RawMessage(`{"offer_id":"offer-1"}`)}
	}
	connection := &scriptedConnection{hello: make(chan Message, 1), script: []func(Message) Message{ack, wake}, closed: make(chan struct{})}
	received := make(chan Message, 1)
	client, err := NewRemoteClient(RemoteClientConfig{
		URL: "wss://example.test/v1/device/connect", DeviceID: "device-1",
		TokenProvider:   StaticTokenProvider("token"),
		HeartbeatSource: NewStaticHeartbeatSource(testHeartbeat()), HeartbeatInterval: time.Hour,
		Dialer: scriptedDialer{connection: connection}, Backoff: BackoffConfig{Base: time.Hour, Max: time.Hour},
		OnRunMessage: func(message Message) { received <- message },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case message := <-received:
		if message.Type != MessageRunDispatch || message.RunID != "run-1" || message.OrganizationID != "org-1" {
			t.Fatalf("received %+v", message.Envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run message was not delivered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}
