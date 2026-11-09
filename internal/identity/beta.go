package identity

// 内测功能开关。可用功能清单由应用维护，库里只存每人的开关状态。

import (
	"context"
	"errors"
	"slices"
)

var (
	ErrBetaOnly            = errors.New("identity: beta features are not available for this account")
	ErrBetaFeatureNotFound = errors.New("identity: beta feature not found")
)

// BetaFeatureKeys 是当前可用的内测功能 key（按显示顺序）。功能上线时在这里加 key。当前为空：界面显示空态。
var BetaFeatureKeys []string

// BetaFeature 是一个功能及本人的开关（没有保存过 = false）。
type BetaFeature struct {
	Key     string
	Enabled bool
}

// BetaFeatureStore 是可选的内测开关能力。
type BetaFeatureStore interface {
	// LoadBetaFeatures 返回本人保存过的开关，键为功能 key。
	LoadBetaFeatures(ctx context.Context, userID string) (map[string]bool, error)
	// SaveBetaFeature 按主键 upsert。
	SaveBetaFeature(ctx context.Context, userID, key string, enabled bool) error
}

// hasBetaAccess 只有 beta / internal 账号可以看到和修改内测功能。
func hasBetaAccess(user User) bool {
	return user.AccessTier == "beta" || user.AccessTier == "internal"
}

// BetaFeatures 返回可用清单中每个功能及本人的开关。standard 账号返回 ErrBetaOnly。
func (s Service) BetaFeatures(ctx context.Context, userID string) ([]BetaFeature, error) {
	store, ok := s.Store.(BetaFeatureStore)
	if !ok {
		return nil, ErrAccountUnsupported
	}
	user, err := s.CurrentUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !hasBetaAccess(user) {
		return nil, ErrBetaOnly
	}
	saved, err := store.LoadBetaFeatures(ctx, userID)
	if err != nil {
		return nil, err
	}
	items := make([]BetaFeature, 0, len(BetaFeatureKeys))
	for _, key := range BetaFeatureKeys {
		items = append(items, BetaFeature{Key: key, Enabled: saved[key]})
	}
	return items, nil
}

// SetBetaFeature 打开或关闭一个功能。standard 账号返回 ErrBetaOnly；key 不在可用清单里返回 ErrBetaFeatureNotFound。
func (s Service) SetBetaFeature(ctx context.Context, userID, key string, enabled bool) (BetaFeature, error) {
	store, ok := s.Store.(BetaFeatureStore)
	if !ok {
		return BetaFeature{}, ErrAccountUnsupported
	}
	user, err := s.CurrentUser(ctx, userID)
	if err != nil {
		return BetaFeature{}, err
	}
	if !hasBetaAccess(user) {
		return BetaFeature{}, ErrBetaOnly
	}
	if !slices.Contains(BetaFeatureKeys, key) {
		return BetaFeature{}, ErrBetaFeatureNotFound
	}
	if err := store.SaveBetaFeature(ctx, userID, key, enabled); err != nil {
		return BetaFeature{}, err
	}
	return BetaFeature{Key: key, Enabled: enabled}, nil
}
