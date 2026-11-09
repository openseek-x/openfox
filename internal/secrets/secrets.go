// Package secrets 用 AES-256-GCM 加密需要落库的敏感配置值（如 Agent 环境变量）。
//
// 约定：
//   - 主密钥是 32 字节随机数，由 OPENFOX_SECRETS_KEY_FILE 指向的文件提供（原始 32 字节，或 64 位十六进制）；
//     文件权限必须不对组和其他用户开放（0600 或更严），与数据库连接串文件同样的要求。
//   - KeyID 是主密钥 SHA-256 的前 16 位十六进制，随密文落库，用于识别轮换前后的密钥。
//   - 每次加密使用新的 12 字节随机 nonce；附加数据（AAD）绑定「用途 + 组织 + 对象 + 名称」，
//     密文被挪到别的行或别的组织时解密失败。
//   - 未配置主密钥时 Box 为 nil：调用方必须返回 ErrUnavailable，不允许退回明文存储。
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	KeySize   = 32
	NonceSize = 12
	// MaxPlaintext 是单个值的明文上限（字节）。
	MaxPlaintext = 8192
)

var (
	ErrUnavailable = errors.New("secrets: encryption key is not configured")
	ErrTooLarge    = errors.New("secrets: value is too large")
	ErrDecrypt     = errors.New("secrets: value cannot be decrypted")
)

// Box 持有主密钥与 AEAD。零值不可用，用 New 或 LoadFile 构造。
type Box struct {
	keyID string
	aead  cipher.AEAD
}

// Sealed 是落库的密文形态。
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

// New 用 32 字节主密钥构造 Box。
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(key)
	return &Box{keyID: hex.EncodeToString(digest[:])[:16], aead: aead}, nil
}

// LoadFile 读取主密钥文件。path 为空返回 (nil, nil)，表示未配置。
func LoadFile(path string) (*Box, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: key file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secrets: key file must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("secrets: key file must not be readable by group or others")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: key file: %w", err)
	}
	key := raw
	if trimmed := strings.TrimSpace(string(raw)); len(trimmed) == KeySize*2 {
		if decoded, decodeErr := hex.DecodeString(trimmed); decodeErr == nil {
			key = decoded
		}
	}
	return New(key)
}

// KeyID 返回当前主密钥标识。
func (b *Box) KeyID() string { return b.keyID }

// Seal 加密 plaintext；aad 必须与 Open 时完全一致。
func (b *Box) Seal(plaintext []byte, aad string) (Sealed, error) {
	if b == nil {
		return Sealed{}, ErrUnavailable
	}
	if len(plaintext) > MaxPlaintext {
		return Sealed{}, ErrTooLarge
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Sealed{}, err
	}
	return Sealed{Ciphertext: b.aead.Seal(nil, nonce, plaintext, []byte(aad)), Nonce: nonce, KeyID: b.keyID}, nil
}

// Open 解密；密钥标识不匹配、AAD 不一致或密文被篡改都返回 ErrDecrypt。
func (b *Box) Open(sealed Sealed, aad string) ([]byte, error) {
	if b == nil {
		return nil, ErrUnavailable
	}
	if sealed.KeyID != b.keyID || len(sealed.Nonce) != NonceSize {
		return nil, ErrDecrypt
	}
	plaintext, err := b.aead.Open(nil, sealed.Nonce, sealed.Ciphertext, []byte(aad))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plaintext, nil
}

// EnvAAD 是 Agent 环境变量的附加数据。
func EnvAAD(organizationID, agentID, name string) string {
	return "agent-env-v1\x00" + organizationID + "\x00" + agentID + "\x00" + name
}
