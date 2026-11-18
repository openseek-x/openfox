package stage12e

// D-01 设备密钥两步式轮换：成功、每个中途失败点之后本机密钥仍被中心接受、与心跳 / 领取 / WSS 并发不丢连接、
// key_rotate 远程命令只对声明能力位的 daemon 开放。

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
)

func TestKeyRotationSucceedsAndRevokesOldKey(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	path := env.writeDaemonConfig(device, env.org)
	rotator := &daemon.KeyRotator{ConfigPath: path, Client: env.auth()}
	result, err := rotator.Rotate(env.ctx)
	if err != nil {
		t.Fatal(err)
	}
	config, private := localKey(t, path)
	if result.PreviousKeyID != device.KeyID || result.KeyID != config.KeyID || config.KeyID == device.KeyID {
		t.Fatalf("result = %+v config key = %s", result, config.KeyID)
	}
	if !env.keyAccepted(device.ID, config.KeyID, private) {
		t.Fatal("new key rejected")
	}
	if env.keyAccepted(device.ID, device.KeyID, device.private) {
		t.Fatal("old key still accepted after confirm")
	}
	// 旧密钥的会话随之失效；新令牌（已写入本机令牌文件）可用。
	if response := env.heartbeat(device, device.session, nil); response.Status != http.StatusUnauthorized {
		t.Fatalf("old session heartbeat = %d %s", response.Status, response.Body)
	}
	token, err := daemon.ReadTokenFile(config.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if response := env.heartbeat(device, token, nil); response.Status != http.StatusOK {
		t.Fatalf("new session heartbeat = %d %s", response.Status, response.Body)
	}
	if daemon.PendingKeyRotation(path) {
		t.Fatal("rotation state left behind")
	}
	// 审计：记在电脑所有者名下，元数据不含密钥材料。
	var metadata string
	env.scalar(&metadata, `SELECT safe_metadata::text FROM openfox.audit_events
		WHERE organization_id = $1::uuid AND action = 'device.key_rotated' AND resource_id = $2 AND actor_id = $3::uuid`,
		env.org, device.ID, qcOwnerID)
	if strings.Contains(metadata, "public") || strings.Contains(metadata, "token") {
		t.Fatalf("audit metadata leaks key material: %s", metadata)
	}
	// 再轮换一次仍然成功（代次递增）。
	if _, err := rotator.Rotate(env.ctx); err != nil {
		t.Fatal(err)
	}
	if count := env.count(`SELECT count(*) FROM openfox.device_keys WHERE device_id = $1::uuid AND status = 'ACTIVE'`, device.ID); count != 1 {
		t.Fatalf("active keys = %d", count)
	}
}

func TestKeyRotationFailurePointsKeepAWorkingKey(t *testing.T) {
	for _, step := range []string{daemon.RotationStepPrepare, daemon.RotationStepRegister, daemon.RotationStepSession,
		daemon.RotationStepToken, daemon.RotationStepConfig, daemon.RotationStepConfirm} {
		t.Run(step, func(t *testing.T) {
			env := newQcEnv(t)
			device := env.registerDevice(env.owner(), env.org)
			path := env.writeDaemonConfig(device, env.org)
			injected := errors.New("injected")
			rotator := &daemon.KeyRotator{ConfigPath: path, Client: env.auth(), FailPoint: func(s string) error {
				if s == step {
					return injected
				}
				return nil
			}}
			if _, err := rotator.Rotate(env.ctx); !errors.Is(err, injected) {
				t.Fatalf("Rotate = %v", err)
			}
			// 中途失败：本机配置当前的密钥中心一定接受；旧密钥在确认前一直有效。
			config, private := localKey(t, path)
			if !env.keyAccepted(device.ID, config.KeyID, private) {
				t.Fatalf("center rejects the local key after failing at %s", step)
			}
			if !env.keyAccepted(device.ID, device.KeyID, device.private) {
				t.Fatalf("old key revoked although %s failed", step)
			}
			if response := env.heartbeat(device, device.session, nil); response.Status != http.StatusOK {
				t.Fatalf("old session heartbeat after failure = %d", response.Status)
			}
			rotator.FailPoint = nil
			if _, err := rotator.Rotate(env.ctx); err != nil {
				t.Fatalf("resume: %v", err)
			}
			config, private = localKey(t, path)
			if !env.keyAccepted(device.ID, config.KeyID, private) || env.keyAccepted(device.ID, device.KeyID, device.private) {
				t.Fatal("resumed rotation did not finish")
			}
			if count := env.count(`SELECT count(*) FROM openfox.device_keys WHERE device_id = $1::uuid AND status = 'ACTIVE'`, device.ID); count != 1 {
				t.Fatalf("active keys after resume = %d", count)
			}
		})
	}
}

func TestKeyRotationRejectsForgedProofAndStaleConfirm(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	nextPrivate, nextID, err := daemon.GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	otherPrivate, _, err := daemon.GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	nextPublic := nextPrivate.Public().(ed25519.PublicKey)
	// 新私钥签名才能登记：用别的私钥签名 → 400 INVALID_SIGNATURE。
	payload := deviceauth.KeyRotationPayload(device.ID, device.KeyID, nextID, nextPublic)
	forged := ed25519.Sign(otherPrivate, payload)
	err = env.auth().RegisterNextKey(env.ctx, device.session, nextID, nextPublic, forged, ed25519.Sign(device.private, payload))
	var centerErr *daemon.CenterError
	if !errors.As(err, &centerErr) || centerErr.Status != http.StatusBadRequest || centerErr.Code != "INVALID_SIGNATURE" {
		t.Fatalf("forged proof = %v", err)
	}
	// 只有会话令牌、没有当前私钥（令牌泄露的情形）：新钥自签有效，但当前密钥的持有证明不对 → 400，新钥不被登记。
	err = env.auth().RegisterNextKey(env.ctx, device.session, nextID, nextPublic, ed25519.Sign(nextPrivate, payload), ed25519.Sign(nextPrivate, payload))
	if !errors.As(err, &centerErr) || centerErr.Status != http.StatusBadRequest || centerErr.Code != "INVALID_SIGNATURE" {
		t.Fatalf("missing current-key proof = %v", err)
	}
	if env.keyAccepted(device.ID, nextID, nextPrivate) {
		t.Fatal("attacker key registered with a session token alone")
	}
	if count := env.count(`SELECT count(*) FROM openfox.device_keys WHERE device_id = $1::uuid`, device.ID); count != 1 {
		t.Fatalf("device keys after rejected registrations = %d", count)
	}
	// 用当前会话确认「吊销当前密钥」→ 409，旧密钥不受影响。
	err = env.auth().ConfirmKeyRotation(env.ctx, device.session, device.KeyID)
	if !errors.As(err, &centerErr) || centerErr.Status != http.StatusConflict {
		t.Fatalf("self confirm = %v", err)
	}
	if !env.keyAccepted(device.ID, device.KeyID, device.private) {
		t.Fatal("current key revoked by an invalid confirm")
	}
}

// 轮换与心跳、领取（列 offer）并发：调用方用令牌提供者（401 时失效重取一次），全程不出现失败。
func TestKeyRotationConcurrentWithHeartbeatAndClaims(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	path := env.writeDaemonConfig(device, env.org)
	config, _ := localKey(t, path)
	provider, err := daemon.NewRenewingTokenProvider(daemon.RenewingTokenProviderConfig{
		TokenFile: config.TokenFile, DeviceID: device.ID, KeyID: device.KeyID, PrivateKey: device.private, Issuer: env.auth(),
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(do func(token string) qcResponse) error {
		for attempt := 0; attempt < 2; attempt++ {
			token, err := provider.Token(env.ctx)
			if err != nil {
				return err
			}
			response := do(token)
			if response.Status == http.StatusOK {
				return nil
			}
			if response.Status != http.StatusUnauthorized {
				return errors.New(string(response.Body))
			}
			provider.Invalidate(token)
		}
		return errors.New("still unauthorized after renewal")
	}
	stop := make(chan struct{})
	var failures atomic.Int32
	var calls atomic.Int32
	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var err error
				if worker%2 == 0 {
					err = call(func(token string) qcResponse { return env.heartbeat(device, token, nil) })
				} else {
					err = call(func(token string) qcResponse {
						return env.anonymous(http.MethodGet, "/v1/device/organizations/"+env.org+"/offers", token, nil)
					})
				}
				calls.Add(1)
				if err != nil {
					failures.Add(1)
					t.Errorf("worker %d: %v", worker, err)
					return
				}
			}
		}(worker)
	}
	time.Sleep(100 * time.Millisecond)
	rotator := &daemon.KeyRotator{ConfigPath: path, Client: env.auth(), CurrentToken: provider.Token, Switch: provider.SwitchKey}
	for round := 0; round < 3; round++ {
		if _, err := rotator.Rotate(env.ctx); err != nil {
			t.Fatalf("rotate round %d: %v", round, err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	close(stop)
	group.Wait()
	if failures.Load() != 0 || calls.Load() == 0 {
		t.Fatalf("failures = %d calls = %d", failures.Load(), calls.Load())
	}
}

// 轮换时 WSS 连接换用新会话：auth.refresh 被中心接受，连接不中断（旧会话吊销后中心不再接受旧令牌）。
func TestKeyRotationRefreshesLiveConnection(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	path := env.writeDaemonConfig(device, env.org)
	config, _ := localKey(t, path)
	provider, err := daemon.NewRenewingTokenProvider(daemon.RenewingTokenProviderConfig{
		TokenFile: config.TokenFile, DeviceID: device.ID, KeyID: device.KeyID, PrivateKey: device.private, Issuer: env.auth(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var connects atomic.Int32
	states := make(chan daemon.RemoteState, 64)
	heartbeat := devices.Heartbeat{CenterEpoch: device.CenterEpoch, ProtocolMajor: daemon.ProtocolMajor,
		DaemonVersion: "test", OperatingSystem: "darwin", Architecture: "arm64", Capabilities: []byte(`{}`)}
	remote, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL: config.WSSURL, DeviceID: device.ID, TokenProvider: provider.Token,
		HeartbeatSource: daemon.NewStaticHeartbeatSource(heartbeat), HeartbeatInterval: time.Hour,
		Backoff: daemon.BackoffConfig{Base: 50 * time.Millisecond, Max: 200 * time.Millisecond},
		OnState: func(state daemon.RemoteState) {
			if state == daemon.RemoteConnected {
				connects.Add(1)
			}
			select {
			case states <- state:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(env.ctx)
	done := make(chan error, 1)
	go func() { done <- remote.Run(ctx) }()
	deadline := time.After(10 * time.Second)
	for connects.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("remote client did not connect")
		case <-states:
		}
	}
	var refreshErr error
	rotator := &daemon.KeyRotator{ConfigPath: path, Client: env.auth(), CurrentToken: provider.Token, Switch: provider.SwitchKey,
		Refresh: func(ctx context.Context, token string) error {
			refreshErr = remote.RefreshAuth(ctx, token)
			return refreshErr
		}}
	if _, err := rotator.Rotate(env.ctx); err != nil {
		t.Fatal(err)
	}
	if refreshErr != nil {
		t.Fatalf("auth refresh over WSS = %v", refreshErr)
	}
	time.Sleep(300 * time.Millisecond)
	if got := connects.Load(); got != 1 {
		t.Fatalf("connections = %d, want 1 (rotation must not drop the connection)", got)
	}
	cancel()
	<-done
}

func TestKeyRotateCommandRequiresCapabilityAndOwner(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	env.mustHeartbeat(device, map[string]any{})
	// 旧 daemon（未声明 key_rotation）：拒绝下发。
	legacy := env.owner().do(http.MethodPost, "devices/"+device.ID+"/commands", map[string]any{"kind": "key_rotate", "params": map[string]any{}})
	if legacy.Status < 400 {
		t.Fatalf("legacy daemon accepted key_rotate: %d %s", legacy.Status, legacy.Body)
	}
	env.mustHeartbeat(device, map[string]any{"key_rotation": true})
	created := env.owner().do(http.MethodPost, "devices/"+device.ID+"/commands", map[string]any{"kind": "key_rotate", "params": map[string]any{}})
	if created.Status != http.StatusAccepted {
		t.Fatalf("owner key_rotate = %d %s", created.Status, created.Body)
	}
	// 管理员不是电脑所有者：不能下发。
	admin := env.admin().do(http.MethodPost, "devices/"+device.ID+"/commands", map[string]any{"kind": "key_rotate", "params": map[string]any{}})
	if admin.Status < 400 {
		t.Fatalf("admin key_rotate = %d %s", admin.Status, admin.Body)
	}
}

// 确认只允许「新钥吊销自己的前驱」：旧钥的会话（只有令牌、没有私钥的调用方也一样）不能吊销接替它的新钥，
// 无论新钥是刚登记未落盘，还是已经换到新会话但尚未确认；方向不对时两把钥的状态都不变。
func TestKeyRotationConfirmOnlyRevokesPredecessor(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	nextPrivate, nextID, err := daemon.GenerateDeviceKey()
	if err != nil {
		t.Fatal(err)
	}
	nextPublic := nextPrivate.Public().(ed25519.PublicKey)
	payload := deviceauth.KeyRotationPayload(device.ID, device.KeyID, nextID, nextPublic)
	if err := env.auth().RegisterNextKey(env.ctx, device.session, nextID, nextPublic,
		ed25519.Sign(nextPrivate, payload), ed25519.Sign(device.private, payload)); err != nil {
		t.Fatalf("register next key: %v", err)
	}
	keyStates := func() string {
		var states string
		env.scalar(&states, `SELECT string_agg(key_id || '=' || status || '/' || generation, ',' ORDER BY key_id)
			FROM openfox.device_keys WHERE device_id = $1::uuid`, device.ID)
		return states
	}
	bothValid := func(stage string, before string) {
		t.Helper()
		if after := keyStates(); after != before {
			t.Fatalf("%s: key rows changed: %s -> %s", stage, before, after)
		}
		if !env.keyAccepted(device.ID, device.KeyID, device.private) || !env.keyAccepted(device.ID, nextID, nextPrivate) {
			t.Fatalf("%s: a key stopped working", stage)
		}
		if response := env.heartbeat(device, device.session, nil); response.Status != http.StatusOK {
			t.Fatalf("%s: old session heartbeat = %d", stage, response.Status)
		}
	}
	rejected := func(stage, token, previous string) {
		t.Helper()
		before := keyStates()
		err := env.auth().ConfirmKeyRotation(env.ctx, token, previous)
		var centerErr *daemon.CenterError
		if !errors.As(err, &centerErr) || centerErr.Status != http.StatusConflict || centerErr.Code != "KEY_ROTATION_INVALID" {
			t.Fatalf("%s: confirm = %v, want 409 KEY_ROTATION_INVALID", stage, err)
		}
		bothValid(stage, before)
	}
	// 窗口一：新钥已登记、daemon 还没换会话也没落盘。旧会话想吊销待用新钥 → 拒绝。
	rejected("registered, old session revokes the pending key", device.session, nextID)
	// 窗口二：daemon 已换到新会话并把新钥写入本地配置，但还没确认。旧会话想吊销新钥 → 拒绝。
	nextSession, err := env.auth().Session(env.ctx, device.ID, nextID, nextPrivate)
	if err != nil {
		t.Fatal(err)
	}
	rejected("switched, old session revokes the new key", device.session, nextID)
	if response := env.heartbeat(device, nextSession.Token, nil); response.Status != http.StatusOK {
		t.Fatalf("new session heartbeat = %d", response.Status)
	}
	// 新会话也只能吊销自己的前驱，不能指定别的密钥编号。
	rejected("new session names a key that is not its predecessor", nextSession.Token, "0000000000000000000000000000dead")
	// 正常方向：新钥吊销前驱，成功；重复确认幂等，新钥不受影响。
	if err := env.auth().ConfirmKeyRotation(env.ctx, nextSession.Token, device.KeyID); err != nil {
		t.Fatalf("confirm in the right direction: %v", err)
	}
	if env.keyAccepted(device.ID, device.KeyID, device.private) || !env.keyAccepted(device.ID, nextID, nextPrivate) {
		t.Fatal("after confirm the old key must be revoked and the new key valid")
	}
	if response := env.heartbeat(device, device.session, nil); response.Status != http.StatusUnauthorized {
		t.Fatalf("old session after confirm = %d", response.Status)
	}
	after := keyStates()
	if err := env.auth().ConfirmKeyRotation(env.ctx, nextSession.Token, device.KeyID); err != nil {
		t.Fatalf("repeated confirm: %v", err)
	}
	if again := keyStates(); again != after || !env.keyAccepted(device.ID, nextID, nextPrivate) {
		t.Fatalf("repeated confirm changed state: %s -> %s", after, again)
	}
	if response := env.heartbeat(device, nextSession.Token, nil); response.Status != http.StatusOK {
		t.Fatalf("new session after repeated confirm = %d", response.Status)
	}
}
