package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/dispatch"
)

func TestDeviceStreamAuthenticatesAndAcknowledgesHeartbeat(t *testing.T) {
	store := &managementStore{heartbeat: devices.HeartbeatResult{
		DeviceID: testDeviceID, CenterEpoch: 1, Version: 4,
		ReceivedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
	}}
	mux := http.NewServeMux()
	RegisterDeviceStreamRoute(mux, deviceauth.Service{Store: managementDeviceAuthStore{}, Now: time.Now}, store)
	server := newTestHTTPServer(t, mux)
	defer server.Close()

	url := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/device/connect"
	connection, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer device-session"}},
		Subprotocols: []string{daemon.ProtocolSubprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()

	heartbeat, err := daemon.NewHeartbeatMessage("hello", testDeviceID, devices.Heartbeat{
		CenterEpoch: 1, ProtocolMajor: 1, ProtocolMinor: 0,
		DaemonVersion: "test", OperatingSystem: "darwin", Architecture: "arm64",
		Capabilities: []byte(`{"runtime":"unknown"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(heartbeat)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(context.Background(), websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	_, response, err := connection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ack daemon.Message
	if err := json.Unmarshal(response, &ack); err != nil {
		t.Fatal(err)
	}
	if ack.Type != "hello.ack" || ack.MessageID != heartbeat.MessageID {
		t.Fatalf("ack = %+v", ack)
	}
}

func newTestHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) || strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
			t.Skipf("sandbox does not permit TCP listeners: %v", err)
		}
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server
}

func TestDeviceStreamForwardsDispatchWakesAfterHeartbeat(t *testing.T) {
	store := &managementStore{heartbeat: devices.HeartbeatResult{
		DeviceID: testDeviceID, CenterEpoch: 7, Version: 1,
		ReceivedAt: time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC),
	}}
	hub := dispatch.NewHub()
	mux := http.NewServeMux()
	RegisterDeviceStreamRoute(mux, deviceauth.Service{Store: managementDeviceAuthStore{}, Now: time.Now}, store, hub)
	server := newTestHTTPServer(t, mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := strings.Replace(server.URL, "http://", "ws://", 1) + "/v1/device/connect"
	connection, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:   http.Header{"Authorization": []string{"Bearer device-session"}},
		Subprotocols: []string{daemon.ProtocolSubprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()

	// 心跳前不订阅：未完成首个心跳的连接收不到唤醒。
	if delivered := hub.PublishCancel(testDeviceID, "org", "run", 1); delivered != 0 {
		t.Fatalf("delivered before heartbeat = %d", delivered)
	}
	heartbeat, err := daemon.NewHeartbeatMessage("hello", testDeviceID, devices.Heartbeat{
		CenterEpoch: 7, ProtocolMajor: 1, ProtocolMinor: 0,
		DaemonVersion: "test", OperatingSystem: "darwin", Architecture: "arm64",
		Capabilities: []byte(`{"runtime":"unknown"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(heartbeat)
	if err := connection.Write(ctx, websocket.MessageText, payload); err != nil {
		t.Fatal(err)
	}
	if _, _, err := connection.Read(ctx); err != nil {
		t.Fatal(err)
	}
	// 订阅在写完 ack 之后发生，轮询直到投递成功。
	offer := dispatch.Offer{
		OrganizationID: "11111111-1111-4111-8111-111111111111", OfferID: "22222222-2222-4222-8222-222222222222",
		RunID: "33333333-3333-4333-8333-333333333333", Generation: 2, DeviceID: testDeviceID,
		ExpiresAt: time.Date(2026, 10, 26, 12, 0, 15, 0, time.UTC),
	}
	for hub.PublishOffer(offer) == 0 {
		if ctx.Err() != nil {
			t.Fatal("stream never subscribed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, response, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message daemon.Message
	if err := json.Unmarshal(response, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != dispatch.MessageRunDispatch || message.OrganizationID != offer.OrganizationID ||
		message.RunID != offer.RunID || message.DispatchGeneration != 2 || message.ComputerID != testDeviceID ||
		message.CenterEpoch != 7 {
		t.Fatalf("dispatch message = %+v", message.Envelope)
	}
	var body struct {
		OfferID   string `json:"offer_id"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(message.Payload, &body); err != nil || body.OfferID != offer.OfferID ||
		body.ExpiresAt != "2026-10-26T12:00:15Z" {
		t.Fatalf("dispatch payload = %s (%v)", message.Payload, err)
	}

	// 远程命令唤醒 device.command，载荷只有 command_id 与 kind，不带 run。
	commandID := "44444444-4444-4444-8444-444444444444"
	if delivered := hub.PublishCommand(testDeviceID, offer.OrganizationID, commandID, "runtime_probe"); delivered != 1 {
		t.Fatalf("command delivered = %d", delivered)
	}
	_, response, err = connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	message = daemon.Message{}
	if err := json.Unmarshal(response, &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != dispatch.MessageDeviceCommand || message.OrganizationID != offer.OrganizationID ||
		message.RunID != "" || message.DispatchGeneration != 0 || message.ComputerID != testDeviceID {
		t.Fatalf("command message = %+v", message.Envelope)
	}
	var command map[string]string
	if err := json.Unmarshal(message.Payload, &command); err != nil || len(command) != 2 ||
		command["command_id"] != commandID || command["kind"] != "runtime_probe" {
		t.Fatalf("command payload = %s (%v)", message.Payload, err)
	}
}
