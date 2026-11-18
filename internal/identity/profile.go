package identity

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxFullNameLength = 80
	maxBioLength      = 280
)

var (
	ErrInvalidProfile = errors.New("identity: profile is invalid")
	ErrAvatarNotFound = errors.New("identity: avatar not found")
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ProfilePatch 只改出现的字段（nil = 保持原值）。
type ProfilePatch struct {
	FullName *string
	Bio      *string
}

// Avatar 是头像文件在存储中的位置与元数据。
type Avatar struct {
	Key         string
	ContentType string
	SHA256      string
	UpdatedAt   time.Time
}

// ProfileStore 是可选的资料能力：修改资料、替换头像、按权限读取头像。
type ProfileStore interface {
	UpdateProfile(context.Context, string, ProfilePatch) (User, error)
	// ReplaceAvatar 写入新头像并返回旧头像的存储键（没有旧头像时为空串）。
	ReplaceAvatar(context.Context, string, Avatar) (User, string, error)
	// FindAvatar 只在查看者本人或与对方同在一个活跃组织时返回头像，否则 ErrAvatarNotFound。
	FindAvatar(context.Context, string, string) (Avatar, error)
}

// UpdateProfile 校验并保存全名与简介。全名去掉首尾空白后为 1–80 个字符，简介不超过 280 个字符。
func (s Service) UpdateProfile(ctx context.Context, userID string, patch ProfilePatch) (User, error) {
	store, ok := s.Store.(ProfileStore)
	if !ok {
		return User{}, ErrAccountUnsupported
	}
	if strings.TrimSpace(userID) == "" {
		return User{}, ErrUserNotFound
	}
	normalized := ProfilePatch{}
	if patch.FullName != nil {
		name := strings.TrimSpace(*patch.FullName)
		if length := utf8.RuneCountInString(name); length < 1 || length > maxFullNameLength {
			return User{}, ErrInvalidProfile
		}
		normalized.FullName = &name
	}
	if patch.Bio != nil {
		if utf8.RuneCountInString(*patch.Bio) > maxBioLength {
			return User{}, ErrInvalidProfile
		}
		normalized.Bio = patch.Bio
	}
	return store.UpdateProfile(ctx, userID, normalized)
}

// ReplaceAvatar 保存已写入存储的头像元数据，返回旧头像的存储键，由调用方在提交后尽力删除。
func (s Service) ReplaceAvatar(ctx context.Context, userID string, key, contentType, sha256 string) (User, string, error) {
	store, ok := s.Store.(ProfileStore)
	if !ok || s.Now == nil {
		return User{}, "", ErrAccountUnsupported
	}
	return store.ReplaceAvatar(ctx, userID, Avatar{
		Key: key, ContentType: contentType, SHA256: sha256, UpdatedAt: s.Now().UTC(),
	})
}

// AvatarForViewer 返回 targetID 的头像，viewerID 必须是本人或与其同在一个活跃组织。
func (s Service) AvatarForViewer(ctx context.Context, viewerID, targetID string) (Avatar, error) {
	store, ok := s.Store.(ProfileStore)
	if !ok {
		return Avatar{}, ErrAccountUnsupported
	}
	if !validUUID(viewerID) || !validUUID(targetID) {
		return Avatar{}, ErrAvatarNotFound
	}
	return store.FindAvatar(ctx, viewerID, targetID)
}

// AvatarKey 是头像在存储中的键：按用户分目录，文件名为内容哈希，同内容的文件天然幂等。
func AvatarKey(userID, sha256 string) string {
	return "avatars/" + userID + "/" + sha256
}

func validUUID(value string) bool {
	return uuidPattern.MatchString(value)
}
