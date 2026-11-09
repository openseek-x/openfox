package daemon

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(dir string) DeviceConfig {
	return DeviceConfig{
		Version: DeviceConfigVersion, CenterURL: "https://center.example.com", WSSURL: "wss://center.example.com/api/v1/device/connect",
		DeviceID: "device-1", OrganizationID: "org-1", KeyID: "key-1",
		PrivateKeyFile: filepath.Join(dir, DeviceKeyFileName), TokenFile: filepath.Join(dir, DeviceTokenFileName),
		WorkspaceRoot: filepath.Join(dir, "workspaces"), CenterEpoch: 3,
	}
}

func TestDeviceConfigRoundTripIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(dir, DeviceConfigFileName)
	config := testConfig(dir)
	config.SupportedVersions = []string{"0.1.0"}
	if err := WriteDeviceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, dir} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, want %v", target, info.Mode().Perm(), want)
		}
	}
	loaded, err := LoadDeviceConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != "device-1" || loaded.CenterEpoch != 3 || len(loaded.SupportedVersions) != 1 {
		t.Fatalf("loaded = %#v", loaded)
	}
	// 组/其他可读的配置被拒绝。
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDeviceConfig(path); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("world-readable config error = %v", err)
	}
}

func TestWritePrivateFileRefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "device-token")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivateFile(link, []byte("new")); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("symlink write error = %v", err)
	}
	if contents, _ := os.ReadFile(victim); string(contents) != "keep" {
		t.Fatalf("symlink target was modified: %q", contents)
	}
	if _, err := ReadTokenFile(link); err == nil {
		t.Fatal("token read followed a symlink")
	}
	if _, err := LoadDeviceConfig(link); err == nil {
		t.Fatal("config read followed a symlink")
	}
}

func TestDeviceKeyRoundTripAndKeyID(t *testing.T) {
	dir := t.TempDir()
	privateKey, keyID, err := GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(keyID) != 32 || keyID != DeviceKeyID(privateKey.Public().(ed25519.PublicKey)) {
		t.Fatalf("key id = %q", keyID)
	}
	path := filepath.Join(dir, DeviceKeyFileName)
	if err := WriteDeviceKey(path, privateKey); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v", info.Mode().Perm())
	}
	loaded, err := ReadDeviceKey(path)
	if err != nil || !loaded.Equal(privateKey) {
		t.Fatalf("ReadDeviceKey() = %v", err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDeviceKey(path); err == nil || strings.Contains(err.Error(), "garbage") {
		t.Fatalf("malformed key error = %v", err)
	}
}

func TestCenterWSSURL(t *testing.T) {
	cases := map[string]string{
		"https://openfox.example.com": "wss://openfox.example.com/api/v1/device/connect",
		"http://127.0.0.1:8080/":      "ws://127.0.0.1:8080/api/v1/device/connect",
		"https://example.com/openfox": "wss://example.com/openfox/api/v1/device/connect",
	}
	for input, want := range cases {
		if got, err := CenterWSSURL(input); err != nil || got != want {
			t.Errorf("CenterWSSURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"ftp://example.com", "https://", "https://u:p@example.com"} {
		if _, err := CenterWSSURL(input); err == nil {
			t.Errorf("CenterWSSURL(%q) accepted", input)
		}
	}
}

// fakeIssuer 模拟挑战签名换令牌；block 非空时每次调用等待其关闭。
type fakeIssuer struct {
	mu      sync.Mutex
	calls   int
	fail    bool
	ttl     time.Duration
	now     func() time.Time
	block   chan struct{}
	started chan struct{}
}

func (f *fakeIssuer) Session(ctx context.Context, deviceID, keyID string, privateKey ed25519.PrivateKey) (DeviceSession, error) {
	f.mu.Lock()
	f.calls++
	calls, fail, block, started := f.calls, f.fail, f.block, f.started
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		<-block
	}
	if fail {
		return DeviceSession{}, &CenterError{Status: http.StatusServiceUnavailable, Code: "NOT_READY"}
	}
	return DeviceSession{Token: "token-" + string(rune('0'+calls)), ExpiresAt: f.now().Add(f.ttl)}, nil
}

func (f *fakeIssuer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newTestProvider(t *testing.T, issuer *fakeIssuer, clock *fakeClock) (*RenewingTokenProvider, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, DeviceTokenFileName)
	if err := os.WriteFile(path, []byte("token-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, privateKey, _ := ed25519.GenerateKey(nil)
	issuer.now = clock.Now
	provider, err := NewRenewingTokenProvider(RenewingTokenProviderConfig{
		TokenFile: path, DeviceID: "device-1", KeyID: "key-1", PrivateKey: privateKey, Issuer: issuer,
		Now: clock.Now, Rand: func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	return provider, path
}

func TestRenewingTokenProviderRenewsBeforeExpiry(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	issuer := &fakeIssuer{ttl: 24 * time.Hour}
	provider, path := newTestProvider(t, issuer, clock)
	// 启动时不知道文件令牌的到期时间：先续期一次。
	token, err := provider.Token(context.Background())
	if err != nil || token != "token-1" || issuer.count() != 1 {
		t.Fatalf("first token = %q, %v, calls = %d", token, err, issuer.count())
	}
	if contents, _ := os.ReadFile(path); strings.TrimSpace(string(contents)) != "token-1" {
		t.Fatalf("token file = %q", contents)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v", info.Mode().Perm())
	}
	// 距到期还有超过 1 小时：复用。
	clock.Advance(22 * time.Hour)
	if token, _ := provider.Token(context.Background()); token != "token-1" || issuer.count() != 1 {
		t.Fatalf("token before renew window = %q, calls = %d", token, issuer.count())
	}
	// 进入到期前 1 小时窗口：续期。
	clock.Advance(90 * time.Minute)
	if token, _ := provider.Token(context.Background()); token != "token-2" || issuer.count() != 2 {
		t.Fatalf("token in renew window = %q, calls = %d", token, issuer.count())
	}
}

func TestRenewingTokenProviderRenewsImmediatelyOnUnauthorized(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	issuer := &fakeIssuer{ttl: 24 * time.Hour}
	provider, _ := newTestProvider(t, issuer, clock)
	token, _ := provider.Token(context.Background())
	// 旧令牌的 401 不影响当前令牌。
	provider.Invalidate("stale-token")
	if again, _ := provider.Token(context.Background()); again != token || issuer.count() != 1 {
		t.Fatalf("stale invalidate renewed: %q, calls = %d", again, issuer.count())
	}
	provider.Invalidate(token)
	if renewed, _ := provider.Token(context.Background()); renewed == token || issuer.count() != 2 {
		t.Fatalf("401 did not renew: %q, calls = %d", renewed, issuer.count())
	}
}

func TestRenewingTokenProviderMergesConcurrentRenewals(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	issuer := &fakeIssuer{ttl: 24 * time.Hour, block: make(chan struct{}), started: make(chan struct{}, 8)}
	provider, _ := newTestProvider(t, issuer, clock)
	var wg sync.WaitGroup
	var tokens sync.Map
	var failures atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			token, err := provider.Token(context.Background())
			if err != nil {
				failures.Add(1)
			}
			tokens.Store(i, token)
		}(i)
	}
	<-issuer.started
	time.Sleep(50 * time.Millisecond)
	close(issuer.block)
	wg.Wait()
	if issuer.count() != 1 || failures.Load() != 0 {
		t.Fatalf("calls = %d, failures = %d", issuer.count(), failures.Load())
	}
	tokens.Range(func(_, value any) bool {
		if value.(string) != "token-1" {
			t.Fatalf("token = %q", value)
		}
		return true
	})
}

func TestRenewingTokenProviderBacksOffAndKeepsUsableToken(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	issuer := &fakeIssuer{ttl: 24 * time.Hour, fail: true}
	provider, _ := newTestProvider(t, issuer, clock)
	// 续期失败时继续使用文件里的令牌（到期时间未知视为可用）。
	if token, err := provider.Token(context.Background()); err != nil || token != "token-file" || issuer.count() != 1 {
		t.Fatalf("token = %q, %v, calls = %d", token, err, issuer.count())
	}
	// 退避期（1s ±20%，Rand=0.5 时正好 1s）内不再请求。
	clock.Advance(500 * time.Millisecond)
	_, _ = provider.Token(context.Background())
	if issuer.count() != 1 {
		t.Fatalf("renewed during backoff: calls = %d", issuer.count())
	}
	clock.Advance(600 * time.Millisecond)
	_, _ = provider.Token(context.Background())
	if issuer.count() != 2 {
		t.Fatalf("did not retry after backoff: calls = %d", issuer.count())
	}
	// 第二次失败后退避 2s。
	clock.Advance(1500 * time.Millisecond)
	_, _ = provider.Token(context.Background())
	if issuer.count() != 2 {
		t.Fatalf("second backoff too short: calls = %d", issuer.count())
	}
	// 被 401 拒绝的令牌在续期失败时不可用。
	provider.Invalidate("token-file")
	if _, err := provider.Token(context.Background()); !errors.Is(err, ErrTokenUnavailable) {
		t.Fatalf("rejected token error = %v", err)
	}
}

func TestRetryDelayIsBoundedWithJitter(t *testing.T) {
	cases := []struct {
		failures int
		random   float64
		want     time.Duration
	}{
		{1, 0.5, time.Second}, {2, 0.5, 2 * time.Second}, {4, 0.5, 8 * time.Second},
		{1, 0, 800 * time.Millisecond}, {1, 1, 1200 * time.Millisecond},
		{20, 0.5, 5 * time.Minute}, {20, 1, 6 * time.Minute},
	}
	for _, c := range cases {
		if got := retryDelay(c.failures, c.random); got != c.want {
			t.Errorf("retryDelay(%d, %v) = %s, want %s", c.failures, c.random, got, c.want)
		}
	}
}

func TestRemoteClientDeliversCommandWakeAndHeartbeatHook(t *testing.T) {
	ack := func(hello Message) Message {
		payload, _ := json.Marshal(HeartbeatAckPayload{DeviceID: "device-1", CenterEpoch: 1, Version: 1, ReceivedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		return Message{Envelope: Envelope{ProtocolMajor: ProtocolMajor, MessageID: hello.MessageID, CenterEpoch: 1, Type: "hello.ack", ComputerID: "device-1"}, Payload: payload}
	}
	wake := func(Message) Message {
		return Message{Envelope: Envelope{
			ProtocolMajor: ProtocolMajor, MessageID: "m-3", CenterEpoch: 1, Type: MessageDeviceCommand,
			ComputerID: "device-1", OrganizationID: "org-1",
		}, Payload: json.RawMessage(`{"command_id":"c-1","kind":"runtime_probe"}`)}
	}
	connection := &scriptedConnection{hello: make(chan Message, 1), script: []func(Message) Message{ack, wake}, closed: make(chan struct{})}
	received := make(chan Message, 1)
	var heartbeats atomic.Int32
	client, err := NewRemoteClient(RemoteClientConfig{
		URL: "wss://example.test/v1/device/connect", DeviceID: "device-1", TokenProvider: StaticTokenProvider("token"),
		HeartbeatSource: NewStaticHeartbeatSource(testHeartbeat()), HeartbeatInterval: time.Hour,
		Dialer: scriptedDialer{connection: connection}, Backoff: BackoffConfig{Base: time.Hour, Max: time.Hour},
		OnCommandMessage: func(message Message) { received <- message },
		OnHeartbeat:      func() { heartbeats.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case message := <-received:
		if message.OrganizationID != "org-1" {
			t.Fatalf("received %+v", message.Envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command wake was not delivered")
	}
	if heartbeats.Load() != 1 {
		t.Fatalf("heartbeat hook calls = %d, want 1 (hello.ack)", heartbeats.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
}

type unauthorizedDialer struct{ attempts atomic.Int32 }

func (d *unauthorizedDialer) Dial(context.Context, string, http.Header) (MessageConnection, error) {
	d.attempts.Add(1)
	return nil, ErrUnauthorized
}

func TestRemoteClientReportsUnauthorizedToken(t *testing.T) {
	dialer := &unauthorizedDialer{}
	rejected := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	client, err := NewRemoteClient(RemoteClientConfig{
		URL: "wss://example.test/v1/device/connect", DeviceID: "device-1", TokenProvider: StaticTokenProvider("old-token"),
		HeartbeatSource: NewStaticHeartbeatSource(testHeartbeat()), Dialer: dialer,
		Sleep:          func(context.Context, time.Duration) error { cancel(); return context.Canceled },
		OnUnauthorized: func(token string) { rejected <- token },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-rejected; got != "old-token" {
		t.Fatalf("rejected token = %q", got)
	}
}

func TestInstallServicePlistUsesConfigAndLogs(t *testing.T) {
	home := t.TempDir()
	binary := filepath.Join(home, "openfox")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var commands [][]string
	manager := Manager{
		SocketPath:      filepath.Join(home, "state", "daemon.sock"),
		LaunchAgentPath: filepath.Join(home, "LaunchAgents", LaunchAgentLabel+".plist"),
		LogPath:         filepath.Join(home, "Logs", "OpenFox", "daemon.log"),
		UserID:          501, GOOS: "darwin",
		RunCommand: func(_ context.Context, name string, args ...string) error {
			commands = append(commands, append([]string{name}, args...))
			return nil
		},
	}
	config := filepath.Join(home, "Application Support", "OpenFox", "config.json")
	if err := manager.InstallService(binary, config); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(manager.LaunchAgentPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, want := range []string{
		"<string>daemon</string><string>run</string>", "<string>--config</string><string>" + config + "</string>",
		"<key>StandardOutPath</key><string>" + manager.LogPath, "<key>StandardErrorPath</key><string>" + manager.LogPath,
		"<key>KeepAlive</key><true/>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q:\n%s", want, text)
		}
	}
	if info, _ := os.Stat(filepath.Dir(manager.LogPath)); info == nil || !info.IsDir() {
		t.Fatal("log directory was not created")
	}
	if err := manager.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"launchctl", "bootout", "gui/501/" + LaunchAgentLabel},
		{"launchctl", "bootstrap", "gui/501", manager.LaunchAgentPath},
	}
	if len(commands) != len(want) || strings.Join(commands[0], " ") != strings.Join(want[0], " ") || strings.Join(commands[1], " ") != strings.Join(want[1], " ") {
		t.Fatalf("launchctl commands = %v", commands)
	}
	manager.GOOS = "linux"
	if err := manager.InstallService(binary, config); err == nil {
		t.Fatal("service installation accepted a non-macOS platform")
	}
}
