package wp05

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"openfox/internal/api"
	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/identity"
	"openfox/internal/tenancy"
)

const wssDeviceID = "00000000-0000-0000-0000-0000000000d1"

func TestWSSHeartbeatLoopAgainstTLSCenter(t *testing.T) {
	store := &wssDeviceStore{epoch: 1}
	server := newWSSCenter(t, store)
	defer server.Close()

	client, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL:      strings.Replace(server.URL, "https://", "wss://", 1) + "/v1/device/connect",
		DeviceID: wssDeviceID, TokenProvider: daemon.StaticTokenProvider("device-session"),
		HeartbeatSource:   daemon.NewStaticHeartbeatSource(testWSSHeartbeat()),
		HeartbeatInterval: 10 * time.Millisecond,
		Dialer:            daemon.WebSocketDialer{HTTPClient: server.Client()},
		Backoff:           daemon.BackoffConfig{Base: 5 * time.Millisecond, Max: 25 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	waitFor(t, 2*time.Second, func() bool { return store.HeartbeatCount() >= 3 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("remote client error = %v", err)
	}
	if store.HeartbeatCount() < 3 {
		t.Fatalf("heartbeat count = %d", store.HeartbeatCount())
	}
}

func TestWSSClientReconnectsAfterCenterClose(t *testing.T) {
	var connections atomic.Int32
	var heartbeats atomic.Int32
	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-session" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{daemon.ProtocolSubprotocol},
		})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		connections.Add(1)
		connection.SetReadLimit(daemon.MaxMessageBytes)
		for {
			messageType, payload, err := connection.Read(context.Background())
			if err != nil || messageType != websocket.MessageText {
				return
			}
			var message daemon.Message
			if json.Unmarshal(payload, &message) != nil {
				return
			}
			if message.Type == "heartbeat" {
				heartbeats.Add(1)
			}
			if err := writeWSSAck(connection, message); err != nil {
				return
			}
			if connections.Load() < 3 {
				return
			}
		}
	}))
	defer server.Close()

	client, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL:      strings.Replace(server.URL, "https://", "wss://", 1) + "/device/connect",
		DeviceID: wssDeviceID, TokenProvider: daemon.StaticTokenProvider("device-session"),
		HeartbeatSource:   daemon.NewStaticHeartbeatSource(testWSSHeartbeat()),
		HeartbeatInterval: 10 * time.Millisecond,
		Dialer:            daemon.WebSocketDialer{HTTPClient: server.Client()},
		Backoff:           daemon.BackoffConfig{Base: 5 * time.Millisecond, Max: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	waitFor(t, 3*time.Second, func() bool { return connections.Load() >= 3 && heartbeats.Load() >= 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("remote client error = %v", err)
	}
}

func TestWSSClientReconnectsAfterHeartbeatTimeout(t *testing.T) {
	var connections atomic.Int32
	var timeouts atomic.Int32
	server := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{daemon.ProtocolSubprotocol},
		})
		if err != nil {
			return
		}
		defer connection.CloseNow()
		connection.SetReadLimit(daemon.MaxMessageBytes)
		current := connections.Add(1)
		messageType, payload, err := connection.Read(context.Background())
		if err != nil || messageType != websocket.MessageText {
			return
		}
		var hello daemon.Message
		if json.Unmarshal(payload, &hello) != nil || hello.Type != "hello" {
			return
		}
		if err := writeWSSAck(connection, hello); err != nil {
			return
		}
		if current == 1 {
			_, _, _ = connection.Read(context.Background())
			timeouts.Add(1)
			return
		}
		for {
			messageType, payload, err := connection.Read(context.Background())
			if err != nil || messageType != websocket.MessageText {
				return
			}
			var message daemon.Message
			if json.Unmarshal(payload, &message) != nil {
				return
			}
			if err := writeWSSAck(connection, message); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL:      strings.Replace(server.URL, "https://", "wss://", 1) + "/device/connect",
		DeviceID: wssDeviceID, TokenProvider: daemon.StaticTokenProvider("device-session"),
		HeartbeatSource:   daemon.NewStaticHeartbeatSource(testWSSHeartbeat()),
		HeartbeatInterval: 10 * time.Millisecond, HeartbeatTimeout: 25 * time.Millisecond,
		Dialer:  daemon.WebSocketDialer{HTTPClient: server.Client()},
		Backoff: daemon.BackoffConfig{Base: 5 * time.Millisecond, Max: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	waitFor(t, 3*time.Second, func() bool { return connections.Load() >= 2 && timeouts.Load() >= 1 })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("remote client error = %v", err)
	}
}

func TestWSSClientStopsForEpochReconciliation(t *testing.T) {
	store := &wssDeviceStore{epoch: 2, rejectEpoch: true}
	server := newWSSCenter(t, store)
	defer server.Close()
	var reconcile ReconcileCapture
	client, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL:      strings.Replace(server.URL, "https://", "wss://", 1) + "/v1/device/connect",
		DeviceID: wssDeviceID, TokenProvider: daemon.StaticTokenProvider("device-session"),
		HeartbeatSource:   daemon.NewStaticHeartbeatSource(testWSSHeartbeat()),
		HeartbeatInterval: 10 * time.Millisecond,
		Dialer:            daemon.WebSocketDialer{HTTPClient: server.Client()},
		OnReconcile:       reconcile.Set,
		Backoff:           daemon.BackoffConfig{Base: 5 * time.Millisecond, Max: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Run(context.Background())
	if !errors.Is(err, daemon.ErrReconcileRequired) {
		t.Fatalf("Run() error = %v, want reconciliation", err)
	}
	if info := reconcile.Get(); info.CurrentEpoch != 2 {
		t.Fatalf("reconcile info = %+v", info)
	}
}

func newWSSCenter(t *testing.T, store devices.Store) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := deviceauth.Service{Store: wssAuthStore{}, Now: time.Now}
	api.RegisterDeviceStreamRoute(mux, auth, store)
	return newTLSServer(t, mux)
}

func newTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
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
	server.StartTLS()
	return server
}

func writeWSSAck(connection *websocket.Conn, message daemon.Message) error {
	payload, err := json.Marshal(daemon.HeartbeatAckPayload{
		DeviceID: wssDeviceID, CenterEpoch: message.CenterEpoch,
		Version: 2, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	return connection.Write(context.Background(), websocket.MessageText, mustJSON(daemon.Message{
		Envelope: daemon.Envelope{
			ProtocolMajor: daemon.ProtocolMajor, ProtocolMinor: 0,
			MessageID: message.MessageID, CenterEpoch: message.CenterEpoch,
			Type: message.Type + ".ack", ComputerID: wssDeviceID,
		}, Payload: payload,
	}))
}

func mustJSON(value any) []byte {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return payload
}

func testWSSHeartbeat() devices.Heartbeat {
	return devices.Heartbeat{
		CenterEpoch: 1, ProtocolMajor: 1, ProtocolMinor: 0,
		DaemonVersion: "integration", OperatingSystem: "darwin", Architecture: "arm64",
		Capabilities: []byte(`{"runtime":"unknown"}`),
	}
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

type ReconcileCapture struct {
	mu   sync.Mutex
	info daemon.ReconcileInfo
}

func (c *ReconcileCapture) Set(info daemon.ReconcileInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info = info
}

func (c *ReconcileCapture) Get() daemon.ReconcileInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

type wssAuthStore struct{}

func (wssAuthStore) CreateChallenge(context.Context, deviceauth.Challenge) error { return nil }
func (wssAuthStore) ConsumeChallenge(context.Context, deviceauth.VerifyRequest, deviceauth.PendingSession, time.Time, deviceauth.SignatureVerifier) (deviceauth.Session, error) {
	return deviceauth.Session{}, nil
}
func (wssAuthStore) AuthenticateSession(_ context.Context, tokenHash []byte, now time.Time) (deviceauth.Session, error) {
	if !bytes.Equal(tokenHash, identity.HashSessionToken("device-session")) {
		return deviceauth.Session{}, deviceauth.ErrSessionNotFound
	}
	return deviceauth.Session{
		ID: "00000000-0000-0000-0000-0000000000e1", DeviceID: wssDeviceID,
		KeyID: "primary", DeviceGeneration: 1, KeyGeneration: 1,
		ExpiresAt: now.Add(time.Hour),
	}, nil
}
func (wssAuthStore) SelectOrganization(context.Context, string, string) (tenancy.OrganizationSelection, error) {
	return tenancy.OrganizationSelection{}, nil
}
func (wssAuthStore) RegisterDevice(context.Context, deviceauth.Registration) (string, error) {
	return wssDeviceID, nil
}
func (wssAuthStore) RevokeGrant(context.Context, tenancy.OrganizationSelection, string, time.Time) error {
	return nil
}
func (wssAuthStore) RevokeDevice(context.Context, string, time.Time) error { return nil }
func (wssAuthStore) RevokeKey(context.Context, string, string, time.Time) error {
	return nil
}

type wssDeviceStore struct {
	mu          sync.Mutex
	epoch       int64
	count       int
	rejectEpoch bool
}

func (s *wssDeviceStore) IssueRegistrationToken(context.Context, tenancy.OrganizationSelection, devices.RegistrationTokenInput, time.Time) (devices.RegistrationTokenResult, error) {
	return devices.RegistrationTokenResult{}, nil
}
func (s *wssDeviceStore) RegisterWithToken(context.Context, devices.RegistrationExchange) (devices.RegistrationResult, error) {
	return devices.RegistrationResult{}, nil
}
func (s *wssDeviceStore) List(context.Context, tenancy.OrganizationSelection) ([]devices.Device, error) {
	return nil, nil
}
func (s *wssDeviceStore) UpdateGrantScope(context.Context, tenancy.OrganizationSelection, string, devices.GrantScopeInput) (devices.Device, error) {
	return devices.Device{}, nil
}
func (s *wssDeviceStore) RotateKey(context.Context, tenancy.OrganizationSelection, string, devices.KeyRotationInput, time.Time) (devices.KeyRotationResult, error) {
	return devices.KeyRotationResult{}, nil
}
func (s *wssDeviceStore) RecordHeartbeat(_ context.Context, _ deviceauth.Principal, heartbeat devices.Heartbeat, _ time.Time) (devices.HeartbeatResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejectEpoch {
		return devices.HeartbeatResult{}, devices.ErrEpochConflict
	}
	s.count++
	return devices.HeartbeatResult{DeviceID: wssDeviceID, CenterEpoch: heartbeat.CenterEpoch, Version: int64(s.count + 1), ReceivedAt: time.Now().UTC()}, nil
}
func (s *wssDeviceStore) ListVisible(context.Context, tenancy.OrganizationSelection, devices.Viewer, devices.ListOptions) (devices.Page, error) {
	return devices.Page{Devices: []devices.Device{}}, nil
}
func (s *wssDeviceStore) Get(context.Context, tenancy.OrganizationSelection, devices.Viewer, string) (devices.DeviceDetail, error) {
	return devices.DeviceDetail{}, nil
}
func (s *wssDeviceStore) UpdateSettings(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, devices.SettingsInput) (devices.Device, error) {
	return devices.Device{}, nil
}
func (s *wssDeviceStore) RemovalBlockers(context.Context, tenancy.OrganizationSelection, devices.Viewer, string) (devices.RemovalBlockers, error) {
	return devices.RemovalBlockers{}, nil
}
func (s *wssDeviceStore) Remove(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, time.Time) error {
	return nil
}
func (s *wssDeviceStore) RemoveFromOrganization(context.Context, tenancy.OrganizationSelection, devices.Viewer, string) (devices.RemovalResult, error) {
	return devices.RemovalResult{}, nil
}
func (s *wssDeviceStore) UnbindAgents(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, []string) (devices.RemovalResult, error) {
	return devices.RemovalResult{}, nil
}
func (s *wssDeviceStore) ReplaceDevice(context.Context, tenancy.OrganizationSelection, devices.Viewer, string, string) (devices.ReplacementResult, error) {
	return devices.ReplacementResult{}, nil
}
func (s *wssDeviceStore) CenterEpoch(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch, nil
}
func (s *wssDeviceStore) HeartbeatCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}
