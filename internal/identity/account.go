package identity

import (
	"context"
	"errors"
	"strings"
	"time"
)

var (
	ErrUserNotFound       = errors.New("identity: user not found")
	ErrAccountUnsupported = errors.New("identity: account operations are not supported by the store")
)

// User 是当前登录账号的资料。
type User struct {
	ID       string
	Email    string
	FullName string
	Bio      string
	// AvatarUpdatedAt 为空表示没有头像；它同时是头像地址的版本参数。
	AvatarUpdatedAt *time.Time
	// AccessTier 为 standard / beta / internal；只有 beta / internal 能使用内测功能。
	AccessTier string
}

// AccountStore 是可选的账号能力：读取用户资料、按令牌吊销会话。
// 单独定义而不并入 Store，避免只关心登录的实现和测试替身被迫实现它。
type AccountStore interface {
	FindUser(ctx context.Context, userID string) (User, error)
	RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error
}

// CurrentUser 返回会话所属用户的资料。
func (s Service) CurrentUser(ctx context.Context, userID string) (User, error) {
	store, ok := s.Store.(AccountStore)
	if !ok {
		return User{}, ErrAccountUnsupported
	}
	if strings.TrimSpace(userID) == "" {
		return User{}, ErrUserNotFound
	}
	return store.FindUser(ctx, userID)
}

// Logout 吊销令牌对应的会话。令牌为空、会话不存在或已吊销都视为成功（幂等）。
func (s Service) Logout(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	store, ok := s.Store.(AccountStore)
	if !ok || s.Now == nil {
		return ErrAccountUnsupported
	}
	return store.RevokeSession(ctx, HashSessionToken(token), s.Now().UTC())
}
