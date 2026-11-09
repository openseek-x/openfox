package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"openfox/internal/deviceauth"
)

const (
	// 设备认证与注册请求的总超时。
	deviceAuthTimeout = 15 * time.Second
	// 中心错误响应体上限。
	maxDeviceAuthResponse = 64 * 1024
)

var ErrUnauthorized = errors.New("daemon: center rejected the device session")

// CenterError 是中心返回的非 2xx 响应；只保留状态码与错误码，不包含请求内容。
type CenterError struct {
	Status int
	Code   string
}

func (e *CenterError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("center returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("center returned HTTP %d %s", e.Status, e.Code)
}

// Permanent 表示重试不会改变结果（4xx，除 408/429）。
func (e *CenterError) Permanent() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusRequestTimeout && e.Status != http.StatusTooManyRequests
}

// DeviceSession 是一次挑战签名换来的会话令牌。Token 不得写日志。
type DeviceSession struct {
	Token     string
	ExpiresAt time.Time
}

// RegistrationResponse 是 POST /v1/device/register 的响应。
type RegistrationResponse struct {
	DeviceID       string `json:"device_id"`
	OrganizationID string `json:"organization_id"`
	OwnerUserID    string `json:"owner_user_id"`
	KeyGeneration  int64  `json:"key_generation"`
	CenterEpoch    int64  `json:"center_epoch"`
}

// AuthClient 调用中心的设备注册与挑战签名端点。BaseURL 是 API 根（含 /api 前缀）。
type AuthClient struct {
	BaseURL string
	HTTP    *http.Client
}

// Register 用注册令牌登记公钥。token 是 v2 安装令牌或旧的纯令牌，原样提交给中心。
// 电脑名在签发令牌时确定，注册接口不接受改名（中心严格解码请求体）。
func (c AuthClient) Register(ctx context.Context, token, keyID string, publicKey ed25519.PublicKey) (RegistrationResponse, error) {
	body := map[string]any{
		"token": token, "key_id": keyID, "public_key": deviceauth.EncodeBase64URL(publicKey),
	}
	var response RegistrationResponse
	if err := c.post(ctx, "/v1/device/register", body, &response); err != nil {
		return RegistrationResponse{}, err
	}
	if response.DeviceID == "" || response.OrganizationID == "" || response.CenterEpoch <= 0 {
		return RegistrationResponse{}, errors.New("daemon: center registration response is incomplete")
	}
	return response, nil
}

// Session 申请挑战、签名并换取设备会话令牌。
func (c AuthClient) Session(ctx context.Context, deviceID, keyID string, privateKey ed25519.PrivateKey) (DeviceSession, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return DeviceSession{}, ErrConfigInvalid
	}
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		DeviceID    string `json:"device_id"`
		KeyID       string `json:"key_id"`
		Nonce       string `json:"nonce"`
		Challenge   string `json:"challenge"`
	}
	if err := c.post(ctx, "/v1/device/auth/challenge", map[string]string{"device_id": deviceID, "key_id": keyID}, &challenge); err != nil {
		return DeviceSession{}, err
	}
	nonce, err := deviceauth.DecodeBase64URL(challenge.Nonce, 0)
	if err != nil {
		return DeviceSession{}, errors.New("daemon: center challenge is malformed")
	}
	challengeBytes, err := deviceauth.DecodeBase64URL(challenge.Challenge, 0)
	if err != nil || challenge.ChallengeID == "" {
		return DeviceSession{}, errors.New("daemon: center challenge is malformed")
	}
	// 签名绑定本机请求的 device_id / key_id，而不是响应里回显的值。
	payload := deviceauth.CanonicalPayload(challenge.ChallengeID, deviceID, keyID, nonce, challengeBytes)
	signature := ed25519.Sign(privateKey, payload)
	var session struct {
		DeviceID  string    `json:"device_id"`
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.post(ctx, "/v1/device/auth/token", map[string]string{
		"challenge_id": challenge.ChallengeID, "device_id": deviceID, "key_id": keyID,
		"signature": deviceauth.EncodeBase64URL(signature),
	}, &session); err != nil {
		return DeviceSession{}, err
	}
	token := strings.TrimSpace(session.Token)
	if token == "" || len(token) > 128 || session.ExpiresAt.IsZero() {
		return DeviceSession{}, errors.New("daemon: center session response is incomplete")
	}
	return DeviceSession{Token: token, ExpiresAt: session.ExpiresAt}, nil
}

func (c AuthClient) post(ctx context.Context, path string, body, out any) error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return ErrRemoteConfiguration
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	requestContext, cancel := context.WithTimeout(ctx, deviceAuthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		// net/http 的错误包含 URL，但不包含请求体；令牌只在请求体中。
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxDeviceAuthResponse+1))
	if err != nil {
		return err
	}
	if len(payload) > maxDeviceAuthResponse {
		return errors.New("daemon: center response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(payload, &envelope)
		return &CenterError{Status: response.StatusCode, Code: sanitizeCode(envelope.Error.Code)}
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return errors.New("daemon: center response is not valid JSON")
	}
	return nil
}

// sanitizeCode 只保留形如 ERROR_CODE 的错误码，避免把任意响应内容带进错误信息。
func sanitizeCode(code string) string {
	if len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
			return ""
		}
	}
	return code
}
