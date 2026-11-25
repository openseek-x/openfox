package daemon

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openfox/internal/deviceauth"
)

// 设备侧两步式密钥轮换（阶段 12e，D-01）。
//
// 顺序（任一步失败都停下，中心上的旧密钥一直有效，下次 Rotate 从断点继续）：
//
//	prepare  生成新私钥写入配置目录（0600，独立文件名），并写旁路状态文件 key-rotation.json；
//	register 用旧密钥的会话登记新公钥（新私钥与旧私钥各签一次：证明持有新钥，且调用方确实持有旧私钥而非只有会话令牌）；
//	session  用新私钥换取新会话；
//	token    写入新会话令牌；
//	config   原子改写 config.json 的 key_id / private_key_file（格式不变，旧版本 daemon 也能读）；
//	switch   进程内切换令牌提供者；refresh 让当前 WSS 连接换用新会话（尽力而为，失败只会短暂重连）；
//	confirm  以新会话请求中心吊销旧密钥及其会话；
//	cleanup  删除旧私钥文件与状态文件。
//
// 在 config 之前失败：本地仍用旧密钥，中心两把都认（未确认的新钥下次登记时被替换）。
// 在 config 之后、confirm 之前失败：本地已用新密钥，中心两把都认，下次 Rotate 只补 confirm。
// 旧私钥文件一直保留到 confirm 成功之后才删除。若在 confirm 之前新密钥被中心吊销（例如所有者在网页上用旧接口
// 换了钥，或旧私钥的持有者重新登记了别的新钥），续做时发现中心明确拒绝新密钥而旧密钥仍可换到会话，
// 就把本机配置退回旧密钥（见 rollback）。
// 因此不存在「本地与中心都不认」的中间态。私钥、令牌与签名都不写日志。

const (
	KeyRotationStateFileName = "key-rotation.json"
	keyRotationStateVersion  = 1
)

// 轮换步骤名（FailPoint 与日志使用）。
const (
	RotationStepPrepare  = "prepare"
	RotationStepRegister = "register"
	RotationStepSession  = "session"
	RotationStepToken    = "token"
	RotationStepConfig   = "config"
	RotationStepRefresh  = "refresh"
	RotationStepConfirm  = "confirm"
)

// ErrKeyRotationRolledBack 表示新密钥在确认之前已被中心吊销，本机已退回仍然有效的旧密钥；可以重新发起轮换。
var ErrKeyRotationRolledBack = errors.New("daemon: the new key was rejected by the center; rolled back to the previous key")

// ErrKeyRotationUnsupported 表示中心不支持设备侧轮换（旧版中心返回 404 / 405 / 501）。
var ErrKeyRotationUnsupported = errors.New("daemon: center does not support device key rotation")

type keyRotationState struct {
	Version         int       `json:"version"`
	DeviceID        string    `json:"device_id"`
	PreviousKeyID   string    `json:"previous_key_id"`
	PreviousKeyFile string    `json:"previous_key_file"`
	NextKeyID       string    `json:"next_key_id"`
	NextKeyFile     string    `json:"next_key_file"`
	StartedAt       time.Time `json:"started_at"`
}

// KeyRotationClient 是轮换用到的中心端点（AuthClient 实现）。
type KeyRotationClient interface {
	Session(ctx context.Context, deviceID, keyID string, privateKey ed25519.PrivateKey) (DeviceSession, error)
	// RegisterNextKey 登记新公钥：signature 由新私钥签（证明持有新钥），currentSignature 由当前私钥签
	// （证明调用方持有当前私钥，而不只是拿到了会话令牌）。
	RegisterNextKey(ctx context.Context, token, keyID string, publicKey ed25519.PublicKey, signature, currentSignature []byte) error
	ConfirmKeyRotation(ctx context.Context, token, previousKeyID string) error
}

// KeyRotator 执行轮换。daemon 内配置 CurrentToken / Switch / Refresh；CLI 离线轮换时三者为空。
type KeyRotator struct {
	ConfigPath string
	Client     KeyRotationClient
	// CurrentToken 返回当前密钥的会话令牌；为空时用当前私钥现签一个会话。
	CurrentToken func(ctx context.Context) (string, error)
	// Switch 在新配置落盘之后切换进程内的令牌提供者。
	Switch func(keyID string, privateKey ed25519.PrivateKey, session DeviceSession)
	// Refresh 让当前 WSS 连接换用新令牌。
	Refresh func(ctx context.Context, token string) error
	Logger  *slog.Logger
	// FailPoint 仅供测试：在进入某一步之前返回错误，模拟中途失败。
	FailPoint func(step string) error

	mu sync.Mutex
}

// KeyRotationResult 是一次轮换的结果；KeyID 是公钥摘要，不是密钥材料。
type KeyRotationResult struct {
	PreviousKeyID string `json:"previous_key_id"`
	KeyID         string `json:"key_id"`
	// Resumed 为 true 表示接着上次中断的轮换完成。
	Resumed bool `json:"resumed"`
}

// PendingKeyRotation 报告配置目录里是否有未完成的轮换（daemon 启动时据此续做）。
func PendingKeyRotation(configPath string) bool {
	_, err := os.Lstat(filepath.Join(filepath.Dir(configPath), KeyRotationStateFileName))
	return err == nil
}

func (r *KeyRotator) Rotate(ctx context.Context) (KeyRotationResult, error) {
	if r == nil || r.Client == nil || strings.TrimSpace(r.ConfigPath) == "" {
		return KeyRotationResult{}, ErrRemoteConfiguration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	lock, err := LockDeviceIdentity(filepath.Dir(r.ConfigPath))
	if err != nil {
		return KeyRotationResult{}, err
	}
	defer lock.Close()
	logger := r.Logger
	if logger == nil {
		logger = slog.Default()
	}
	config, err := LoadDeviceConfig(r.ConfigPath)
	if err != nil {
		return KeyRotationResult{}, err
	}
	dir := filepath.Dir(r.ConfigPath)
	statePath := filepath.Join(dir, KeyRotationStateFileName)
	state, stateErr := readKeyRotationState(statePath)
	if stateErr == nil && state.DeviceID != config.DeviceID {
		stateErr = ErrConfigInvalid
	}

	// 已切换到新密钥、只差确认：直接补确认。
	if stateErr == nil && state.NextKeyID == config.KeyID && state.PreviousKeyID != config.KeyID {
		privateKey, err := ReadDeviceKey(config.PrivateKeyFile)
		if err != nil {
			return KeyRotationResult{}, err
		}
		token, err := r.token(ctx, config.DeviceID, config.KeyID, privateKey)
		if err != nil {
			if r.rollback(ctx, logger, config, privateKey, state, dir, statePath) {
				return KeyRotationResult{}, ErrKeyRotationRolledBack
			}
			return KeyRotationResult{}, fmt.Errorf("daemon: key rotation session: %w", err)
		}
		r.refresh(ctx, logger, token)
		if err := r.confirm(ctx, token, state); err != nil {
			if r.rollback(ctx, logger, config, privateKey, state, dir, statePath) {
				return KeyRotationResult{}, ErrKeyRotationRolledBack
			}
			return KeyRotationResult{}, err
		}
		r.cleanup(logger, dir, statePath, state, config.PrivateKeyFile)
		logger.Info("daemon_key_rotation_completed", "key_id", config.KeyID, "resumed", true)
		return KeyRotationResult{PreviousKeyID: state.PreviousKeyID, KeyID: config.KeyID, Resumed: true}, nil
	}
	// 状态与当前配置对不上（例如用户手工改过配置）：丢弃旧状态，重新开始。
	if stateErr == nil && state.PreviousKeyID != config.KeyID {
		removeOwnedKeyFile(dir, state.NextKeyFile, config.PrivateKeyFile)
		stateErr = os.ErrNotExist
	}

	currentKey, err := ReadDeviceKey(config.PrivateKeyFile)
	if err != nil {
		return KeyRotationResult{}, err
	}
	resumed := stateErr == nil
	var nextKey ed25519.PrivateKey
	if resumed {
		nextKey, err = ReadDeviceKey(state.NextKeyFile)
		if err != nil || DeviceKeyID(nextKey.Public().(ed25519.PublicKey)) != state.NextKeyID {
			resumed = false
		}
	}
	if !resumed {
		if err := r.fail(RotationStepPrepare); err != nil {
			return KeyRotationResult{}, err
		}
		var nextKeyID string
		nextKey, nextKeyID, err = GenerateDeviceKey()
		if err != nil {
			return KeyRotationResult{}, err
		}
		state = keyRotationState{
			Version: keyRotationStateVersion, DeviceID: config.DeviceID,
			PreviousKeyID: config.KeyID, PreviousKeyFile: config.PrivateKeyFile,
			NextKeyID: nextKeyID, NextKeyFile: filepath.Join(dir, DeviceKeyFileName+"-"+nextKeyID[:16]),
			StartedAt: time.Now().UTC(),
		}
		if err := WriteDeviceKey(state.NextKeyFile, nextKey); err != nil {
			return KeyRotationResult{}, err
		}
		if err := writeKeyRotationState(statePath, state); err != nil {
			return KeyRotationResult{}, err
		}
	}
	nextPublic := nextKey.Public().(ed25519.PublicKey)

	if err := r.fail(RotationStepRegister); err != nil {
		return KeyRotationResult{}, err
	}
	currentToken, err := r.token(ctx, config.DeviceID, config.KeyID, currentKey)
	if err != nil {
		return KeyRotationResult{}, fmt.Errorf("daemon: key rotation session: %w", err)
	}
	payload := deviceauth.KeyRotationPayload(config.DeviceID, config.KeyID, state.NextKeyID, nextPublic)
	if err := r.Client.RegisterNextKey(ctx, currentToken, state.NextKeyID, nextPublic, ed25519.Sign(nextKey, payload), ed25519.Sign(currentKey, payload)); err != nil {
		err = unsupportedOr(err, "register")
		if errors.Is(err, ErrKeyRotationUnsupported) {
			// 旧版中心：新钥从未登记，清掉本地的待用新钥与状态，避免每次启动都续做。
			removeOwnedKeyFile(dir, state.NextKeyFile, config.PrivateKeyFile)
			_ = os.Remove(statePath)
		}
		return KeyRotationResult{}, err
	}

	if err := r.fail(RotationStepSession); err != nil {
		return KeyRotationResult{}, err
	}
	session, err := r.Client.Session(ctx, config.DeviceID, state.NextKeyID, nextKey)
	if err != nil {
		return KeyRotationResult{}, fmt.Errorf("daemon: key rotation new session: %w", err)
	}

	if err := r.fail(RotationStepToken); err != nil {
		return KeyRotationResult{}, err
	}
	if err := WritePrivateFile(config.TokenFile, []byte(session.Token+"\n")); err != nil {
		return KeyRotationResult{}, err
	}

	if err := r.fail(RotationStepConfig); err != nil {
		return KeyRotationResult{}, err
	}
	updated := config
	updated.KeyID, updated.PrivateKeyFile = state.NextKeyID, state.NextKeyFile
	if err := WriteDeviceConfig(r.ConfigPath, updated); err != nil {
		return KeyRotationResult{}, err
	}
	if r.Switch != nil {
		r.Switch(state.NextKeyID, nextKey, session)
	}
	r.refresh(ctx, logger, session.Token)

	if err := r.confirm(ctx, session.Token, state); err != nil {
		return KeyRotationResult{}, err
	}
	r.cleanup(logger, dir, statePath, state, updated.PrivateKeyFile)
	logger.Info("daemon_key_rotation_completed", "key_id", state.NextKeyID, "resumed", resumed)
	return KeyRotationResult{PreviousKeyID: state.PreviousKeyID, KeyID: state.NextKeyID, Resumed: resumed}, nil
}

// rollback 处理「本机已切到新密钥、但中心已不认新密钥」：只有在中心明确拒绝新密钥（4xx，不是网络或限流问题）、
// 且保留的旧私钥仍能换到会话时，才把令牌文件与 config.json 退回旧密钥并清掉轮换状态。返回是否已退回。
// 两把密钥都被拒绝时不动本机文件（电脑需要重新注册），由调用方返回原错误。
func (r *KeyRotator) rollback(ctx context.Context, logger *slog.Logger, config DeviceConfig, currentKey ed25519.PrivateKey,
	state keyRotationState, dir, statePath string) bool {
	_, probeErr := r.Client.Session(ctx, config.DeviceID, config.KeyID, currentKey)
	var centerErr *CenterError
	if !errors.As(probeErr, &centerErr) || !centerErr.Permanent() {
		return false
	}
	previousKey, err := ReadDeviceKey(state.PreviousKeyFile)
	if err != nil || DeviceKeyID(previousKey.Public().(ed25519.PublicKey)) != state.PreviousKeyID {
		return false
	}
	session, err := r.Client.Session(ctx, config.DeviceID, state.PreviousKeyID, previousKey)
	if err != nil {
		return false
	}
	if err := WritePrivateFile(config.TokenFile, []byte(session.Token+"\n")); err != nil {
		return false
	}
	restored := config
	restored.KeyID, restored.PrivateKeyFile = state.PreviousKeyID, state.PreviousKeyFile
	if err := WriteDeviceConfig(r.ConfigPath, restored); err != nil {
		return false
	}
	if r.Switch != nil {
		r.Switch(state.PreviousKeyID, previousKey, session)
	}
	r.refresh(ctx, logger, session.Token)
	removeOwnedKeyFile(dir, state.NextKeyFile, restored.PrivateKeyFile)
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("daemon_key_rotation_cleanup_failed", "error", err.Error())
	}
	logger.Warn("daemon_key_rotation_rolled_back", "key_id", state.PreviousKeyID, "rejected_key_id", state.NextKeyID)
	return true
}

func (r *KeyRotator) fail(step string) error {
	if r.FailPoint == nil {
		return nil
	}
	return r.FailPoint(step)
}

func (r *KeyRotator) token(ctx context.Context, deviceID, keyID string, privateKey ed25519.PrivateKey) (string, error) {
	if r.CurrentToken != nil {
		return r.CurrentToken(ctx)
	}
	session, err := r.Client.Session(ctx, deviceID, keyID, privateKey)
	if err != nil {
		return "", err
	}
	return session.Token, nil
}

// refresh 尽力让当前 WSS 连接换用新令牌，最多 3 次；失败只记录（确认后旧会话失效，连接会自动重连）。
func (r *KeyRotator) refresh(ctx context.Context, logger *slog.Logger, token string) {
	if r.Refresh == nil {
		return
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = r.fail(RotationStepRefresh); err == nil {
			refreshContext, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = r.Refresh(refreshContext, token)
			cancel()
		}
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			break
		}
	}
	logger.Warn("daemon_key_rotation_refresh_failed", "error", err.Error())
}

func (r *KeyRotator) confirm(ctx context.Context, token string, state keyRotationState) error {
	if err := r.fail(RotationStepConfirm); err != nil {
		return err
	}
	if err := r.Client.ConfirmKeyRotation(ctx, token, state.PreviousKeyID); err != nil {
		return unsupportedOr(err, "confirm")
	}
	return nil
}

func (r *KeyRotator) cleanup(logger *slog.Logger, dir, statePath string, state keyRotationState, currentKeyFile string) {
	removeOwnedKeyFile(dir, state.PreviousKeyFile, currentKeyFile)
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("daemon_key_rotation_cleanup_failed", "error", err.Error())
	}
}

// removeOwnedKeyFile 只删除配置目录内、且不是当前私钥的密钥文件。
func removeOwnedKeyFile(dir, path, current string) {
	if path == "" || path == current || filepath.Dir(path) != dir ||
		!strings.HasPrefix(filepath.Base(path), DeviceKeyFileName) {
		return
	}
	_ = os.Remove(path)
}

func unsupportedOr(err error, step string) error {
	var centerErr *CenterError
	if errors.As(err, &centerErr) && (centerErr.Status == http.StatusNotFound || centerErr.Status == http.StatusMethodNotAllowed ||
		centerErr.Status == http.StatusNotImplemented) && centerErr.Code != "NOT_FOUND" {
		return ErrKeyRotationUnsupported
	}
	return fmt.Errorf("daemon: key rotation %s: %w", step, err)
}

func readKeyRotationState(path string) (keyRotationState, error) {
	contents, err := readPrivateFile(path, 16*1024)
	if err != nil {
		return keyRotationState{}, err
	}
	var state keyRotationState
	if err := json.Unmarshal(contents, &state); err != nil || state.Version != keyRotationStateVersion ||
		state.PreviousKeyID == "" || state.NextKeyID == "" || !filepath.IsAbs(state.NextKeyFile) ||
		filepath.Clean(state.NextKeyFile) != state.NextKeyFile {
		return keyRotationState{}, ErrConfigInvalid
	}
	return state, nil
}

func writeKeyRotationState(path string, state keyRotationState) error {
	encoded, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return WritePrivateFile(path, append(encoded, '\n'))
}

// RegisterNextKey 用当前会话登记新公钥（POST /v1/device/keys/rotation）。
func (c AuthClient) RegisterNextKey(ctx context.Context, token, keyID string, publicKey ed25519.PublicKey, signature, currentSignature []byte) error {
	var response struct {
		KeyID string `json:"key_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/device/keys/rotation", token, map[string]string{
		"key_id": keyID, "public_key": deviceauth.EncodeBase64URL(publicKey),
		"signature": deviceauth.EncodeBase64URL(signature), "current_signature": deviceauth.EncodeBase64URL(currentSignature),
	}, &response); err != nil {
		return err
	}
	if response.KeyID != keyID {
		return errors.New("daemon: center key rotation response is incomplete")
	}
	return nil
}

// ConfirmKeyRotation 以新会话请求吊销旧密钥（POST /v1/device/keys/rotation/confirm）。
func (c AuthClient) ConfirmKeyRotation(ctx context.Context, token, previousKeyID string) error {
	var response struct {
		PreviousKeyID string `json:"previous_key_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/device/keys/rotation/confirm", token, map[string]string{
		"previous_key_id": previousKeyID,
	}, &response); err != nil {
		return err
	}
	if response.PreviousKeyID != previousKeyID {
		return errors.New("daemon: center key rotation response is incomplete")
	}
	return nil
}
