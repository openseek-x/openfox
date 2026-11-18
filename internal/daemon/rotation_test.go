package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"openfox/internal/deviceauth"
)

// fakeRotationCenter 模拟中心的密钥语义：ACTIVE 密钥集合、会话绑定密钥、确认后吊销旧密钥。
type fakeRotationCenter struct {
	mu       sync.Mutex
	deviceID string
	active   map[string]ed25519.PublicKey
	sessions map[string]string // token -> key id
	// predecessor 记录新钥登记时接替的密钥（与服务端一致：确认只能吊销自己的前驱）。
	predecessor map[string]string
	counter     int
	unsupported bool
}

func newFakeRotationCenter(deviceID, keyID string, public ed25519.PublicKey) *fakeRotationCenter {
	return &fakeRotationCenter{deviceID: deviceID, active: map[string]ed25519.PublicKey{keyID: public}, sessions: map[string]string{}}
}

func (c *fakeRotationCenter) Session(_ context.Context, deviceID, keyID string, privateKey ed25519.PrivateKey) (DeviceSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	public, ok := c.active[keyID]
	if !ok || deviceID != c.deviceID || !bytes.Equal(public, privateKey.Public().(ed25519.PublicKey)) {
		return DeviceSession{}, &CenterError{Status: http.StatusUnauthorized, Code: "UNAUTHENTICATED"}
	}
	c.counter++
	token := "token-" + keyID + "-" + string(rune('a'+c.counter))
	c.sessions[token] = keyID
	return DeviceSession{Token: token}, nil
}

func (c *fakeRotationCenter) keyFor(token string) (string, error) {
	keyID, ok := c.sessions[token]
	if !ok {
		return "", &CenterError{Status: http.StatusUnauthorized, Code: "UNAUTHENTICATED"}
	}
	if _, active := c.active[keyID]; !active {
		return "", &CenterError{Status: http.StatusUnauthorized, Code: "UNAUTHENTICATED"}
	}
	return keyID, nil
}

func (c *fakeRotationCenter) RegisterNextKey(_ context.Context, token, keyID string, publicKey ed25519.PublicKey, signature, currentSignature []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.unsupported {
		return &CenterError{Status: http.StatusNotFound}
	}
	current, err := c.keyFor(token)
	if err != nil {
		return err
	}
	payload := deviceauth.KeyRotationPayload(c.deviceID, current, keyID, publicKey)
	if !ed25519.Verify(publicKey, payload, signature) || !ed25519.Verify(c.active[current], payload, currentSignature) {
		return &CenterError{Status: http.StatusBadRequest, Code: "INVALID_SIGNATURE"}
	}
	if existing, ok := c.active[keyID]; ok {
		if bytes.Equal(existing, publicKey) {
			return nil
		}
		return &CenterError{Status: http.StatusConflict, Code: "KEY_CONFLICT"}
	}
	// 未确认的其他新钥被替换（与服务端一致）。
	for id := range c.active {
		if id != current {
			delete(c.active, id)
		}
	}
	c.active[keyID] = publicKey
	if c.predecessor == nil {
		c.predecessor = map[string]string{}
	}
	c.predecessor[keyID] = current
	return nil
}

func (c *fakeRotationCenter) ConfirmKeyRotation(_ context.Context, token, previousKeyID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, err := c.keyFor(token)
	if err != nil {
		return err
	}
	if current == previousKeyID || c.predecessor[current] != previousKeyID {
		return &CenterError{Status: http.StatusConflict, Code: "KEY_ROTATION_INVALID"}
	}
	delete(c.active, previousKeyID)
	return nil
}

func (c *fakeRotationCenter) accepts(keyID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.active[keyID]
	return ok
}

func (c *fakeRotationCenter) activeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.active)
}

func setupRotation(t *testing.T) (string, *fakeRotationCenter) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "config")
	config := testConfig(dir)
	privateKey, keyID, err := GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	config.KeyID = keyID
	if err := WriteDeviceKey(config.PrivateKeyFile, privateKey); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, DeviceConfigFileName)
	if err := WriteDeviceConfig(path, config); err != nil {
		t.Fatal(err)
	}
	return path, newFakeRotationCenter(config.DeviceID, keyID, privateKey.Public().(ed25519.PublicKey))
}

// localKeyWorks 用本地 config.json 当前的密钥向中心换会话：轮换中途失败后必须仍然可用。
func localKeyWorks(t *testing.T, path string, center *fakeRotationCenter) {
	t.Helper()
	config, err := LoadDeviceConfig(path)
	if err != nil {
		t.Fatalf("config unreadable after failure: %v", err)
	}
	privateKey, err := ReadDeviceKey(config.PrivateKeyFile)
	if err != nil {
		t.Fatalf("private key unreadable after failure: %v", err)
	}
	if _, err := center.Session(context.Background(), config.DeviceID, config.KeyID, privateKey); err != nil {
		t.Fatalf("center rejects the local key %s: %v", config.KeyID, err)
	}
}

func TestKeyRotationSucceedsAndCleansUp(t *testing.T) {
	path, center := setupRotation(t)
	before, _ := LoadDeviceConfig(path)
	var switched, refreshed string
	rotator := &KeyRotator{ConfigPath: path, Client: center,
		Switch:  func(keyID string, _ ed25519.PrivateKey, _ DeviceSession) { switched = keyID },
		Refresh: func(_ context.Context, token string) error { refreshed = token; return nil },
	}
	result, err := rotator.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, _ := LoadDeviceConfig(path)
	if result.PreviousKeyID != before.KeyID || result.KeyID != after.KeyID || after.KeyID == before.KeyID || result.Resumed {
		t.Fatalf("result = %#v before=%s after=%s", result, before.KeyID, after.KeyID)
	}
	if switched != after.KeyID || refreshed == "" {
		t.Fatalf("switch=%q refresh=%q", switched, refreshed)
	}
	if center.accepts(before.KeyID) || !center.accepts(after.KeyID) || center.activeCount() != 1 {
		t.Fatalf("center keys after rotation are wrong")
	}
	if _, err := os.Stat(before.PrivateKeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old private key file not removed: %v", err)
	}
	if PendingKeyRotation(path) {
		t.Fatal("rotation state file left behind")
	}
	// 新令牌已落盘。
	token, err := os.ReadFile(after.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		t.Fatalf("token file = %q, %v", token, err)
	}
	localKeyWorks(t, path, center)
}

func TestKeyRotationEveryFailurePointKeepsAWorkingKey(t *testing.T) {
	for _, step := range []string{RotationStepPrepare, RotationStepRegister, RotationStepSession, RotationStepToken, RotationStepConfig, RotationStepConfirm} {
		t.Run(step, func(t *testing.T) {
			path, center := setupRotation(t)
			before, _ := LoadDeviceConfig(path)
			injected := errors.New("injected failure")
			rotator := &KeyRotator{ConfigPath: path, Client: center, FailPoint: func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}}
			if _, err := rotator.Rotate(context.Background()); !errors.Is(err, injected) {
				t.Fatalf("Rotate error = %v", err)
			}
			// 失败之后：本地配置的密钥中心仍然认。config 之前失败时就是旧密钥。
			localKeyWorks(t, path, center)
			if !center.accepts(before.KeyID) {
				t.Fatal("old key revoked although confirm did not happen")
			}
			// 修复后再次轮换能完成（confirm 失败的情形只补确认）。
			rotator.FailPoint = nil
			result, err := rotator.Rotate(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if step == RotationStepConfirm && !result.Resumed {
				t.Fatalf("confirm-only resume not detected: %#v", result)
			}
			if center.accepts(before.KeyID) || center.activeCount() != 1 || PendingKeyRotation(path) {
				t.Fatal("resumed rotation did not finish cleanly")
			}
			localKeyWorks(t, path, center)
		})
	}
}

func TestKeyRotationRefreshFailureIsNotFatal(t *testing.T) {
	path, center := setupRotation(t)
	calls := 0
	rotator := &KeyRotator{ConfigPath: path, Client: center, Refresh: func(context.Context, string) error {
		calls++
		return errors.New("connection busy")
	}}
	if _, err := rotator.Rotate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("refresh attempts = %d, want 3", calls)
	}
	localKeyWorks(t, path, center)
}

func TestKeyRotationUnsupportedCenterKeepsOldKey(t *testing.T) {
	path, center := setupRotation(t)
	center.unsupported = true
	before, _ := os.ReadFile(path)
	rotator := &KeyRotator{ConfigPath: path, Client: center}
	if _, err := rotator.Rotate(context.Background()); !errors.Is(err, ErrKeyRotationUnsupported) {
		t.Fatalf("error = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("config.json changed although the center does not support rotation")
	}
	// 旧版中心：不留下待续做的状态与新私钥文件（否则每次启动都会重试）。
	if PendingKeyRotation(path) {
		t.Fatal("rotation state left behind for an unsupported center")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, entry := range entries {
		if entry.Name() != DeviceKeyFileName && len(entry.Name()) > len(DeviceKeyFileName) && entry.Name()[:len(DeviceKeyFileName)] == DeviceKeyFileName {
			t.Fatalf("pending key file left behind: %s", entry.Name())
		}
	}
	localKeyWorks(t, path, center)
}

type recordingIssuer struct {
	mu   sync.Mutex
	keys []string
}

func (i *recordingIssuer) Session(_ context.Context, _, keyID string, _ ed25519.PrivateKey) (DeviceSession, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.keys = append(i.keys, keyID)
	return DeviceSession{Token: "renewed-" + keyID}, nil
}

// 轮换后令牌提供者立即用新会话，之后续期改用新密钥（不会再用已吊销的旧密钥续期）。
func TestRenewingTokenProviderSwitchKey(t *testing.T) {
	dir := t.TempDir()
	oldKey, oldID, _ := GenerateDeviceKey()
	newKey, newID, _ := GenerateDeviceKey()
	issuer := &recordingIssuer{}
	provider, err := NewRenewingTokenProvider(RenewingTokenProviderConfig{
		TokenFile: filepath.Join(dir, DeviceTokenFileName), DeviceID: "device-1", KeyID: oldID, PrivateKey: oldKey, Issuer: issuer,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.SwitchKey(newID, newKey, DeviceSession{Token: "rotated-token", ExpiresAt: time.Now().Add(12 * time.Hour)})
	if token, err := provider.Token(context.Background()); err != nil || token != "rotated-token" {
		t.Fatalf("token after switch = %q, %v", token, err)
	}
	provider.Invalidate("rotated-token")
	token, err := provider.Token(context.Background())
	if err != nil || token != "renewed-"+newID {
		t.Fatalf("renewed token = %q, %v", token, err)
	}
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	for _, keyID := range issuer.keys {
		if keyID == oldID {
			t.Fatal("renewed with the revoked key")
		}
	}
}

// openfox computer rotate 经控制 socket 交给运行中的 daemon；旧版 daemon（没有设置轮换函数）返回 ErrControlUnsupported。
func TestControlSocketKeyRotation(t *testing.T) {
	socketPath := filepath.Join("/tmp", "openfox-rotate-test-"+strconv.Itoa(os.Getpid())+".sock")
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	server := NewControlServer(socketPath)
	server.SetRunning(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	ready := false
	for index := 0; index < 400 && !ready; index++ {
		if _, err := os.Stat(socketPath); err == nil {
			ready = true
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if !ready {
		select {
		case err := <-done:
			t.Skipf("Unix socket unavailable: %v", err)
		default:
			t.Fatal("daemon socket was not created")
		}
	}
	if _, err := RequestKeyRotation(ctx, socketPath); !errors.Is(err, ErrControlUnsupported) {
		t.Fatalf("legacy daemon error = %v", err)
	}
	// Serve 启动之后再设置（与 daemon 入口的顺序一致）。
	server.SetRotateKey(func(context.Context) (KeyRotationResult, error) {
		return KeyRotationResult{PreviousKeyID: "old", KeyID: "new"}, nil
	})
	result, err := RequestKeyRotation(ctx, socketPath)
	if err != nil || result.KeyID != "new" || result.PreviousKeyID != "old" {
		t.Fatalf("rotation over socket = %#v, %v", result, err)
	}
	server.SetRotateKey(func(context.Context) (KeyRotationResult, error) {
		return KeyRotationResult{}, errors.New("daemon: key rotation register: center returned HTTP 503")
	})
	if _, err := RequestKeyRotation(ctx, socketPath); err == nil || errors.Is(err, ErrControlUnsupported) {
		t.Fatalf("failed rotation error = %v", err)
	}
	// 状态查询不受影响。
	if status, err := QuerySocket(ctx, socketPath); err != nil || status.State != StateRunning {
		t.Fatalf("status = %+v, %v", status, err)
	}
}

// 已切到新密钥、尚未确认时新密钥被中心吊销（旧密钥仍有效）：续做时退回旧密钥，本机始终有一把中心认可的密钥；
// 之后可以重新轮换。两把都被拒绝时不改本机文件。
func TestKeyRotationRollsBackWhenNewKeyIsRevokedBeforeConfirm(t *testing.T) {
	path, center := setupRotation(t)
	before, _ := LoadDeviceConfig(path)
	injected := errors.New("injected failure")
	var switched []string
	rotator := &KeyRotator{ConfigPath: path, Client: center,
		Switch: func(keyID string, _ ed25519.PrivateKey, _ DeviceSession) { switched = append(switched, keyID) },
		FailPoint: func(step string) error {
			if step == RotationStepConfirm {
				return injected
			}
			return nil
		}}
	if _, err := rotator.Rotate(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("Rotate error = %v", err)
	}
	pending, _ := LoadDeviceConfig(path)
	if pending.KeyID == before.KeyID {
		t.Fatal("config was not switched to the new key before confirm")
	}
	// 旧私钥文件保留到确认成功。
	if _, err := ReadDeviceKey(before.PrivateKeyFile); err != nil {
		t.Fatalf("previous private key must be kept until confirm: %v", err)
	}
	// 中心吊销新密钥（旧密钥仍 ACTIVE）。
	center.mu.Lock()
	delete(center.active, pending.KeyID)
	center.mu.Unlock()
	rotator.FailPoint = nil
	if _, err := rotator.Rotate(context.Background()); !errors.Is(err, ErrKeyRotationRolledBack) {
		t.Fatalf("resume with a revoked new key = %v, want rollback", err)
	}
	restored, _ := LoadDeviceConfig(path)
	if restored.KeyID != before.KeyID || restored.PrivateKeyFile != before.PrivateKeyFile {
		t.Fatalf("config after rollback = %s, want %s", restored.KeyID, before.KeyID)
	}
	if PendingKeyRotation(path) {
		t.Fatal("rotation state left behind after rollback")
	}
	if _, err := os.Stat(pending.PrivateKeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected key file not removed: %v", err)
	}
	if len(switched) != 2 || switched[1] != before.KeyID {
		t.Fatalf("token provider switches = %v", switched)
	}
	localKeyWorks(t, path, center)
	// 退回之后可以重新轮换。
	if _, err := rotator.Rotate(context.Background()); err != nil {
		t.Fatalf("rotate after rollback: %v", err)
	}
	localKeyWorks(t, path, center)

	// 只是网络问题（中心没有明确拒绝新密钥）时不退回。
	path2, center2 := setupRotation(t)
	second := &KeyRotator{ConfigPath: path2, Client: center2, FailPoint: func(step string) error {
		if step == RotationStepConfirm {
			return injected
		}
		return nil
	}}
	if _, err := second.Rotate(context.Background()); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	switchedConfig, _ := LoadDeviceConfig(path2)
	if _, err := second.Rotate(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("second attempt = %v", err)
	}
	if still, _ := LoadDeviceConfig(path2); still.KeyID != switchedConfig.KeyID || !PendingKeyRotation(path2) {
		t.Fatal("a transient confirm failure must not roll back")
	}
	localKeyWorks(t, path2, center2)
}
