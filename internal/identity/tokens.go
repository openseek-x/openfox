package identity

// 个人访问令牌。令牌与本人权限相同；明文只在创建时返回一次，库里只存 SHA-256 哈希。

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrTokenNotFound = errors.New("identity: personal access token not found")
	ErrTokenLimit    = errors.New("identity: personal access token limit reached")
	ErrInvalidToken  = errors.New("identity: personal access token request is invalid")
)

const (
	// PersonalAccessTokenPrefix 是明文令牌的固定前缀，认证时用它区分令牌与会话 cookie 值。
	PersonalAccessTokenPrefix = "ofx_pat_"
	// maxPersonalAccessTokens 是每人未吊销令牌的上限。
	maxPersonalAccessTokens = 50
	maxTokenNameLength      = 80
	// tokenSecretLength 是 32 字节随机数 base62 定长编码后的字符数（62^43 > 2^256）。
	tokenSecretLength = 43
	// tokenUsageInterval 是 last_used_at 的节流间隔：距上次记录不足一分钟时不再写库。
	tokenUsageInterval = time.Minute
)

var tokenSecretPattern = regexp.MustCompile(`^ofx_pat_[A-Za-z0-9]{43}$`)

// personalAccessTokenExpiryDays 是允许的有效天数；nil 表示永不过期。
var personalAccessTokenExpiryDays = []int{30, 90, 365}

// PersonalAccessToken 是令牌的展示信息，不含明文与哈希。
type PersonalAccessToken struct {
	ID         string
	Name       string
	Prefix     string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
}

// TokenStore 是可选的令牌能力，单独定义以免只关心登录的实现被迫实现它。
type TokenStore interface {
	// CreatePersonalAccessToken 在按用户加锁的事务里统计未吊销数量，达到 limit 时返回 ErrTokenLimit，否则插入并返回带 ID 的记录。
	CreatePersonalAccessToken(ctx context.Context, userID string, token PersonalAccessToken, tokenHash []byte, limit int) (PersonalAccessToken, error)
	ListPersonalAccessTokens(ctx context.Context, userID string, now time.Time) ([]PersonalAccessToken, error)
	// RevokePersonalAccessToken 只吊销本人且未吊销的令牌；0 行时返回 ErrTokenNotFound。
	RevokePersonalAccessToken(ctx context.Context, userID, tokenID string, now time.Time) error
	// AuthenticatePersonalAccessToken 只接受未吊销、未过期且账号凭据有效的令牌；last_used_at 按 lastUsedBefore 节流更新。
	AuthenticatePersonalAccessToken(ctx context.Context, tokenHash []byte, now, lastUsedBefore time.Time) (tokenID, userID string, err error)
}

// NewPersonalAccessToken 生成明文令牌、展示前缀与哈希。前缀取随机部分的前四个字符。
func NewPersonalAccessToken() (secret, prefix string, tokenHash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", nil, fmt.Errorf("generate personal access token: %w", err)
	}
	random := new(big.Int).SetBytes(raw).Text(62)
	random = strings.Repeat("0", tokenSecretLength-len(random)) + random
	secret = PersonalAccessTokenPrefix + random
	return secret, PersonalAccessTokenPrefix + random[:4], HashPersonalAccessToken(secret), nil
}

// HashPersonalAccessToken 返回明文令牌的 SHA-256，库里只保存它。
func HashPersonalAccessToken(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}

// IsPersonalAccessToken 判断 Bearer 值是否为个人访问令牌格式（只看前缀，格式校验在认证时完成）。
func IsPersonalAccessToken(value string) bool {
	return strings.HasPrefix(value, PersonalAccessTokenPrefix)
}

// CreatePersonalAccessToken 创建令牌。名称去除首尾空白后 1–80 个字符；expiresInDays 只能是 30 / 90 / 365 或 nil。
func (s Service) CreatePersonalAccessToken(ctx context.Context, userID, name string, expiresInDays *int) (PersonalAccessToken, string, error) {
	store, ok := s.Store.(TokenStore)
	if !ok || s.Now == nil {
		return PersonalAccessToken{}, "", ErrAccountUnsupported
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxTokenNameLength {
		return PersonalAccessToken{}, "", ErrInvalidToken
	}
	now := s.Now().UTC()
	var expiresAt *time.Time
	if expiresInDays != nil {
		if !slices.Contains(personalAccessTokenExpiryDays, *expiresInDays) {
			return PersonalAccessToken{}, "", ErrInvalidToken
		}
		value := now.Add(time.Duration(*expiresInDays) * 24 * time.Hour)
		expiresAt = &value
	}
	secret, prefix, tokenHash, err := NewPersonalAccessToken()
	if err != nil {
		return PersonalAccessToken{}, "", err
	}
	created, err := store.CreatePersonalAccessToken(ctx, userID, PersonalAccessToken{
		Name: name, Prefix: prefix, CreatedAt: now, ExpiresAt: expiresAt,
	}, tokenHash, maxPersonalAccessTokens)
	if err != nil {
		return PersonalAccessToken{}, "", err
	}
	return created, secret, nil
}

// ListPersonalAccessTokens 返回本人未吊销的令牌（含已过期），按创建时间倒序。
func (s Service) ListPersonalAccessTokens(ctx context.Context, userID string) ([]PersonalAccessToken, error) {
	store, ok := s.Store.(TokenStore)
	if !ok || s.Now == nil {
		return nil, ErrAccountUnsupported
	}
	return store.ListPersonalAccessTokens(ctx, userID, s.Now().UTC())
}

// RevokePersonalAccessToken 吊销本人的令牌；别人的或已吊销的返回 ErrTokenNotFound。
func (s Service) RevokePersonalAccessToken(ctx context.Context, userID, tokenID string) error {
	store, ok := s.Store.(TokenStore)
	if !ok || s.Now == nil {
		return ErrAccountUnsupported
	}
	return store.RevokePersonalAccessToken(ctx, userID, tokenID, s.Now().UTC())
}

// AuthenticatePersonalAccessToken 校验明文令牌并返回带 TokenID 的 Principal。格式错误、不存在、过期、吊销都返回 ErrSessionNotFound。
func (s Service) AuthenticatePersonalAccessToken(ctx context.Context, secret string) (Principal, error) {
	store, ok := s.Store.(TokenStore)
	if !ok || s.Now == nil {
		return Principal{}, ErrSessionNotFound
	}
	if !tokenSecretPattern.MatchString(secret) {
		return Principal{}, ErrSessionNotFound
	}
	now := s.Now().UTC()
	tokenID, userID, err := store.AuthenticatePersonalAccessToken(ctx, HashPersonalAccessToken(secret), now, now.Add(-tokenUsageInterval))
	if errors.Is(err, ErrTokenNotFound) {
		return Principal{}, ErrSessionNotFound
	}
	if err != nil {
		return Principal{}, err
	}
	return Principal{UserID: userID, TokenID: tokenID}, nil
}
