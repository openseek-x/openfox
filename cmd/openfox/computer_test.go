package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
)

const (
	testSecret       = "regtoken-SECRET-0123456789"
	testSessionToken = "session-TOKEN-abcdef"
)

// fakeCenter 模拟 /api 下的注册与挑战签名端点，并校验签名。
type fakeCenter struct {
	mu        sync.Mutex
	server    *httptest.Server
	publicKey ed25519.PublicKey
	keyID     string
	registers int
	rejectReg int
	nonce     []byte
	challenge []byte
}

func newFakeCenter(t *testing.T) *fakeCenter {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP listeners unavailable: %v", err)
	}
	center := &fakeCenter{nonce: []byte("nonce-0123456789"), challenge: []byte("challenge-0123456789")}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("POST /api/v1/device/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token     string `json:"token"`
			KeyID     string `json:"key_id"`
			PublicKey string `json:"public_key"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "INVALID_REQUEST"}})
			return
		}
		center.mu.Lock()
		defer center.mu.Unlock()
		center.registers++
		secret, err := devices.RegistrationSecret(body.Token)
		if center.rejectReg != 0 || err != nil || secret != testSecret {
			status := center.rejectReg
			if status == 0 {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]any{"error": map[string]string{"code": "REGISTRATION_CONFLICT", "message": body.Token}})
			return
		}
		key, err := deviceauth.DecodeBase64URL(body.PublicKey, ed25519.PublicKeySize)
		if err != nil || daemon.DeviceKeyID(key) != body.KeyID {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "INVALID_REQUEST"}})
			return
		}
		center.publicKey, center.keyID = key, body.KeyID
		writeJSON(w, http.StatusCreated, map[string]any{
			"device_id": "00000000-0000-4000-8000-0000000000d1", "organization_id": "00000000-0000-4000-8000-0000000000a1",
			"owner_user_id": "u", "key_generation": 1, "center_epoch": 7,
		})
	})
	mux.HandleFunc("POST /api/v1/device/auth/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusCreated, map[string]any{
			"challenge_id": "00000000-0000-4000-8000-0000000000c1", "device_id": "00000000-0000-4000-8000-0000000000d1",
			"key_id": center.keyID, "nonce": deviceauth.EncodeBase64URL(center.nonce),
			"challenge": deviceauth.EncodeBase64URL(center.challenge), "expires_at": time.Now().Add(time.Minute),
		})
	})
	mux.HandleFunc("POST /api/v1/device/auth/token", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ChallengeID string `json:"challenge_id"`
			DeviceID    string `json:"device_id"`
			KeyID       string `json:"key_id"`
			Signature   string `json:"signature"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		signature, _ := deviceauth.DecodeBase64URL(body.Signature, ed25519.SignatureSize)
		center.mu.Lock()
		key := center.publicKey
		center.mu.Unlock()
		payload := deviceauth.CanonicalPayload(body.ChallengeID, body.DeviceID, body.KeyID, center.nonce, center.challenge)
		if key == nil || !ed25519.Verify(key, payload, signature) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "DEVICE_UNAUTHENTICATED"}})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"device_id": body.DeviceID, "token": testSessionToken, "expires_at": time.Now().Add(24 * time.Hour)})
	})
	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	center.server = server
	return center
}

func (c *fakeCenter) installToken(t *testing.T) string {
	t.Helper()
	token, err := devices.EncodeInstallToken(c.server.URL, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

type fakeLaunchctl struct{ calls []string }

func testDeps(t *testing.T, stdout *bytes.Buffer, goos string, launchctl *fakeLaunchctl) computerDeps {
	t.Helper()
	home := t.TempDir()
	binary := filepath.Join(home, "openfox")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return computerDeps{
		stdin: strings.NewReader(""), stdout: stdout, httpClient: &http.Client{Timeout: 5 * time.Second}, goos: goos,
		executable: func() (string, error) { return binary, nil },
		manager: func() (daemon.Manager, error) {
			return daemon.Manager{
				SocketPath:      filepath.Join(home, "default.sock"),
				LaunchAgentPath: filepath.Join(home, "LaunchAgents", daemon.LaunchAgentLabel+".plist"),
				LogPath:         filepath.Join(home, "Logs", "OpenFox", "daemon.log"), UserID: 501,
				RunCommand: func(_ context.Context, name string, args ...string) error {
					launchctl.calls = append(launchctl.calls, name+" "+strings.Join(args, " "))
					return nil
				},
			}, nil
		},
	}
}

func assertNoSecrets(t *testing.T, texts ...string) {
	t.Helper()
	for _, text := range texts {
		for _, secret := range []string{testSecret, testSessionToken, "ofx_v2_", "ed25519-seed"} {
			if strings.Contains(text, secret) {
				t.Fatalf("output leaked %q: %s", secret, text)
			}
		}
	}
}

func TestComputerRegisterWritesPrivateConfigWithoutService(t *testing.T) {
	center := newFakeCenter(t)
	var stdout bytes.Buffer
	launchctl := &fakeLaunchctl{}
	dir := filepath.Join(t.TempDir(), "cfg")
	err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", dir, "--no-service",
		"--supported-version", "0.1.0"}, testDeps(t, &stdout, "darwin", launchctl))
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, stdout.String())
	if !strings.Contains(stdout.String(), "openfox daemon run --config "+filepath.Join(dir, "config.json")) || len(launchctl.calls) != 0 {
		t.Fatalf("stdout = %s, launchctl = %v", stdout.String(), launchctl.calls)
	}
	for _, name := range []string{"config.json", "device-key", "device-token"} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s = %v, %v", name, info, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "device-key.pending")); !os.IsNotExist(err) {
		t.Fatalf("pending key left behind: %v", err)
	}
	config, err := daemon.LoadDeviceConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	wantWSS := strings.Replace(center.server.URL, "http://", "ws://", 1) + "/api/v1/device/connect"
	if config.CenterURL != center.server.URL || config.WSSURL != wantWSS || config.CenterEpoch != 7 || config.KeyID != center.keyID ||
		config.WorkspaceRoot != filepath.Join(dir, "workspaces") || config.SupportedVersions[0] != "0.1.0" {
		t.Fatalf("config = %#v", config)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	assertNoSecrets(t, string(raw))
	if token, _ := daemon.ReadTokenFile(config.TokenFile); token != testSessionToken {
		t.Fatalf("token file = %q", token)
	}
	key, err := daemon.ReadDeviceKey(config.PrivateKeyFile)
	if err != nil || daemon.DeviceKeyID(key.Public().(ed25519.PublicKey)) != config.KeyID {
		t.Fatalf("device key = %v", err)
	}

	// 已有 config.json 且未加 --force：拒绝（退出码 3），不联系中心。
	before := center.registers
	err = runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", dir, "--no-service"}, testDeps(t, &stdout, "darwin", launchctl))
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitAlreadyRegistered || center.registers != before {
		t.Fatalf("second register = %v", err)
	}
	// --force：替换注册，生成新密钥。
	if err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", dir, "--no-service", "--force"}, testDeps(t, &stdout, "darwin", launchctl)); err != nil {
		t.Fatal(err)
	}
	replaced, _ := daemon.LoadDeviceConfig(filepath.Join(dir, "config.json"))
	if replaced.KeyID == config.KeyID {
		t.Fatal("--force did not generate a new key")
	}
}

func TestComputerRegisterInstallsLaunchAgentOnMacOS(t *testing.T) {
	center := newFakeCenter(t)
	var stdout bytes.Buffer
	launchctl := &fakeLaunchctl{}
	deps := testDeps(t, &stdout, "darwin", launchctl)
	dir := filepath.Join(t.TempDir(), "cfg")
	if err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", dir}, deps); err != nil {
		t.Fatal(err)
	}
	manager, _ := deps.manager()
	plist, err := os.ReadFile(manager.LaunchAgentPath)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json")
	for _, want := range []string{"<string>--config</string><string>" + config + "</string>",
		"<string>--socket</string><string>" + filepath.Join(dir, "daemon.sock") + "</string>", manager.LogPath} {
		if !strings.Contains(string(plist), want) {
			t.Fatalf("plist missing %q:\n%s", want, plist)
		}
	}
	assertNoSecrets(t, string(plist), stdout.String())
	if len(launchctl.calls) != 2 || !strings.HasPrefix(launchctl.calls[1], "launchctl bootstrap gui/501 ") {
		t.Fatalf("launchctl = %v", launchctl.calls)
	}

	// 非 macOS：不安装服务，只打印前台命令。
	stdout.Reset()
	linux := &fakeLaunchctl{}
	if err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", filepath.Join(t.TempDir(), "l")}, testDeps(t, &stdout, "linux", linux)); err != nil {
		t.Fatal(err)
	}
	if len(linux.calls) != 0 || !strings.Contains(stdout.String(), "openfox daemon run --config") {
		t.Fatalf("linux output = %s, calls = %v", stdout.String(), linux.calls)
	}
}

func TestComputerRegisterErrorsDoNotLeakToken(t *testing.T) {
	center := newFakeCenter(t)
	var stdout bytes.Buffer
	cases := []struct {
		name string
		args []string
		code int
		prep func()
	}{
		{"malformed", []string{"--install-token", "ofx_v2_" + testSecret}, exitInvalidToken, nil},
		{"legacy plain token", []string{"--install-token", testSecret}, exitInvalidToken, nil},
		{"unknown flag echoes value", []string{"--install-token=" + center.installToken(t), "--bogus=" + testSecret}, 1, nil},
		{"rejected", []string{"--install-token", center.installToken(t)}, exitTokenRejected, func() { center.rejectReg = http.StatusConflict }},
		{"center down", []string{"--install-token", center.installToken(t)}, exitCenterUnavailable, func() { center.rejectReg = http.StatusServiceUnavailable }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.prep != nil {
				c.prep()
			}
			dir := filepath.Join(t.TempDir(), "cfg")
			err := runComputerWith(append(append([]string{"register"}, c.args...), "--config-dir", dir, "--no-service"), testDeps(t, &stdout, "darwin", &fakeLaunchctl{}))
			if err == nil {
				t.Fatal("register succeeded")
			}
			assertNoSecrets(t, err.Error(), stdout.String())
			var coded *exitError
			code := 1
			if errors.As(err, &coded) {
				code = coded.code
			}
			if code != c.code {
				t.Fatalf("exit code = %d, want %d (%v)", code, c.code, err)
			}
			// 注册失败不留下半成品配置与令牌。
			for _, name := range []string{"config.json", "device-token", "device-key.pending"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("%s left behind: %v", name, err)
				}
			}
		})
	}
	// 中心不可达。
	center.server.Close()
	center.rejectReg = 0
	err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", t.TempDir(), "--no-service"}, testDeps(t, &stdout, "darwin", &fakeLaunchctl{}))
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitCenterUnavailable {
		t.Fatalf("unreachable center = %v", err)
	}
	assertNoSecrets(t, err.Error())
}

func TestComputerRegisterReadsTokenFromStdin(t *testing.T) {
	center := newFakeCenter(t)
	var stdout bytes.Buffer
	deps := testDeps(t, &stdout, "darwin", &fakeLaunchctl{})
	deps.stdin = strings.NewReader(center.installToken(t) + "\n")
	if err := runComputerWith([]string{"register", "--install-token", "-", "--config-dir", filepath.Join(t.TempDir(), "cfg"), "--no-service"}, deps); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonRunConfigRejectsMixedFlags(t *testing.T) {
	err := run([]string{"daemon", "run", "--config", filepath.Join(t.TempDir(), "config.json"), "--device-id", "x"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("mixed flags error = %v", err)
	}
	if err := run([]string{"daemon", "run", "--config", filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestComputerStatusReportsRegistrationWithoutSecrets(t *testing.T) {
	center := newFakeCenter(t)
	launchctl := &fakeLaunchctl{}
	dir := filepath.Join(t.TempDir(), "cfg")
	status := func() computerStatus {
		t.Helper()
		var stdout bytes.Buffer
		if err := runComputerWith([]string{"status", "--json", "--config-dir", dir}, testDeps(t, &stdout, "darwin", launchctl)); err != nil {
			t.Fatal(err)
		}
		assertNoSecrets(t, stdout.String())
		var got computerStatus
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("status output %q: %v", stdout.String(), err)
		}
		return got
	}
	if got := status(); got.Registered || got.Reason != "not_registered" || got.DeviceID != "" {
		t.Fatalf("before register = %#v", got)
	}

	var stdout bytes.Buffer
	if err := runComputerWith([]string{"register", "--install-token", center.installToken(t), "--config-dir", dir, "--no-service"},
		testDeps(t, &stdout, "darwin", launchctl)); err != nil {
		t.Fatal(err)
	}
	config, err := daemon.LoadDeviceConfig(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	got := status()
	if !got.Registered || got.Reason != "" || got.DeviceID != config.DeviceID || got.OrganizationID != config.OrganizationID ||
		got.CenterURL != center.server.URL {
		t.Fatalf("after register = %#v, config = %#v", got, config)
	}

	// 权限过宽的配置文件不读取，按无效配置报告
	if err := os.Chmod(filepath.Join(dir, "config.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := status(); got.Registered || got.Reason != "invalid_config" {
		t.Fatalf("unsafe config = %#v", got)
	}
}
