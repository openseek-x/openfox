package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

// refreshConnection 回应 hello 与 auth.refresh；accept 决定是否接受新令牌。
type refreshConnection struct {
	inbound chan []byte
	accept  bool
	closed  chan struct{}
	writes  chan Message
}

func (c *refreshConnection) Write(_ context.Context, payload []byte) error {
	var message Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return err
	}
	c.writes <- message
	envelope := Envelope{ProtocolMajor: ProtocolMajor, MessageID: message.MessageID, CenterEpoch: 1, ComputerID: "device-1"}
	switch message.Type {
	case "hello":
		envelope.Type = "hello.ack"
		ack, _ := json.Marshal(HeartbeatAckPayload{DeviceID: "device-1", CenterEpoch: 1, Version: 1, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		encoded, _ := json.Marshal(Message{Envelope: envelope, Payload: ack})
		c.inbound <- encoded
	case MessageAuthRefresh:
		envelope.Type = MessageAuthRefresh + ".ack"
		ack, _ := json.Marshal(AuthRefreshAckPayload{OK: c.accept})
		encoded, _ := json.Marshal(Message{Envelope: envelope, Payload: ack})
		c.inbound <- encoded
	}
	return nil
}

func (c *refreshConnection) Read(ctx context.Context) ([]byte, error) {
	select {
	case payload := <-c.inbound:
		return payload, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, errors.New("closed")
	}
}

func (c *refreshConnection) Ping(context.Context) error { return nil }
func (c *refreshConnection) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

type refreshDialer struct{ connection *refreshConnection }

func (d refreshDialer) Dial(context.Context, string, http.Header) (MessageConnection, error) {
	return d.connection, nil
}

// 密钥轮换时当前 WSS 连接换用新会话令牌：同一条连接继续使用（不重连），被拒绝时返回 ErrAuthRefreshRejected。
func TestRemoteClientRefreshesAuthOnTheSameConnection(t *testing.T) {
	for _, accept := range []bool{true, false} {
		connection := &refreshConnection{inbound: make(chan []byte, 4), accept: accept, closed: make(chan struct{}), writes: make(chan Message, 16)}
		client, err := NewRemoteClient(RemoteClientConfig{
			URL: "wss://example.test/v1/device/connect", DeviceID: "device-1",
			TokenProvider:   StaticTokenProvider("old-token"),
			HeartbeatSource: NewStaticHeartbeatSource(testHeartbeat()), HeartbeatInterval: time.Hour,
			Dialer: refreshDialer{connection: connection}, Backoff: BackoffConfig{Base: time.Hour, Max: time.Hour},
		})
		if err != nil {
			t.Fatal(err)
		}
		// 未连接时直接返回（下次连接会取新令牌）。
		if err := client.RefreshAuth(context.Background(), "new-token"); err != nil {
			t.Fatalf("refresh before connect = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- client.Run(ctx) }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			client.tokenMu.Lock()
			connected := client.connectionToken != ""
			client.tokenMu.Unlock()
			if connected {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("client did not connect")
			}
			time.Sleep(10 * time.Millisecond)
		}
		refreshContext, refreshCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = client.RefreshAuth(refreshContext, "new-token")
		refreshCancel()
		if accept && err != nil {
			t.Fatalf("accepted refresh = %v", err)
		}
		if !accept && !errors.Is(err, ErrAuthRefreshRejected) {
			t.Fatalf("rejected refresh = %v", err)
		}
		// 连接仍然存活：没有被关闭。
		select {
		case <-connection.closed:
			t.Fatal("connection closed by auth refresh")
		default:
		}
		if accept {
			if err := client.RefreshAuth(context.Background(), "new-token"); err != nil {
				t.Fatalf("same-token refresh = %v", err)
			}
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatalf("Run() = %v", err)
		}
	}
}
