package integrations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/secrets"
)

// 凭据与 OAuth 暂存值的信封加密格式：keyID（16 个十六进制字符）‖ nonce ‖ 密文。
// integration_oauth_states 没有 key id 列，所以 key id 随密文一起存放。

const keyIDLen = 16

func connectionAAD(organizationID, connectionID string) string {
	return "integration:" + organizationID + ":" + connectionID
}

func oauthAAD(organizationID, sessionID string) string {
	return "oauth:" + organizationID + ":" + sessionID
}

// registrationAAD 是扫码会话里 device_code 的附加认证数据。
func registrationAAD(organizationID, registrationID string) string {
	return "imreg:" + organizationID + ":" + registrationID
}

func (s Service) seal(plain []byte, aad string) (blob []byte, keyID string, err error) {
	if s.Box == nil {
		return nil, "", ErrSecretsUnavailable
	}
	sealed, err := s.Box.Seal(plain, aad)
	if err != nil {
		return nil, "", err
	}
	blob = make([]byte, 0, keyIDLen+len(sealed.Nonce)+len(sealed.Ciphertext))
	blob = append(blob, sealed.KeyID...)
	blob = append(blob, sealed.Nonce...)
	blob = append(blob, sealed.Ciphertext...)
	return blob, sealed.KeyID, nil
}

func (s Service) open(blob []byte, aad string) ([]byte, error) {
	if s.Box == nil {
		return nil, ErrSecretsUnavailable
	}
	if len(blob) < keyIDLen+secrets.NonceSize+1 {
		return nil, secrets.ErrDecrypt
	}
	return s.Box.Open(secrets.Sealed{KeyID: string(blob[:keyIDLen]), Nonce: blob[keyIDLen : keyIDLen+secrets.NonceSize],
		Ciphertext: blob[keyIDLen+secrets.NonceSize:]}, aad)
}

// connectionCredentials 是 integration_connections.credentials_sealed 解密后的内容。
// webhook_* 只有组织连接才有；webhook_token 明文保存是为了在配置页重新显示 Payload URL（库里只存其哈希）。
type connectionCredentials struct {
	AccessToken   string `json:"access_token"`
	Scope         string `json:"scope"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
	WebhookToken  string `json:"webhook_token,omitempty"`
	// AppSecret 是飞书应用的 App Secret，仅组织级飞书应用连接使用。
	AppSecret string `json:"app_secret,omitempty"`
	// RefreshToken 是 GitLab / 极狐 OAuth 刷新令牌；刷新会使旧令牌失效，所以每次刷新都要整体回写。
	RefreshToken string `json:"refresh_token,omitempty"`
	// GitAppSecret 是 GitLab 系组织连接的 OAuth 应用 Client Secret（管理员在实例上创建应用后填入）。
	GitAppSecret string `json:"git_app_secret,omitempty"`
}

func (s Service) sealCredentials(organizationID, connectionID string, creds connectionCredentials) ([]byte, string, error) {
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, "", err
	}
	return s.seal(plain, connectionAAD(organizationID, connectionID))
}

func (s Service) openCredentials(organizationID, connectionID string, blob []byte) (connectionCredentials, error) {
	var creds connectionCredentials
	plain, err := s.open(blob, connectionAAD(organizationID, connectionID))
	if err != nil {
		return creds, err
	}
	if err := json.Unmarshal(plain, &creds); err != nil {
		return creds, secrets.ErrDecrypt
	}
	return creds, nil
}

// randomBase64URL 返回 n 字节随机数的 base64url（无填充）编码。
func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// TokenHash 是 state 与 Webhook 地址令牌的存储形态（SHA-256）。
func TokenHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// tokenHint 只保留令牌末四位。
func tokenHint(token string) string {
	if len(token) <= 4 {
		return ""
	}
	return token[len(token)-4:]
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// AuditEntry 是调用方需要在同一事务里写入的审计事件（元数据不含任何凭据）。
type AuditEntry struct {
	Action     string
	ResourceID string
	Metadata   map[string]string
}

func formatID(id int64) string { return strconv.FormatInt(id, 10) }
