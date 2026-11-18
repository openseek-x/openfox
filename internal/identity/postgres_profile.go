package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
)

// UpdateProfile 只更新出现的字段（COALESCE 保持未传的列不变）。
func (s PostgresStore) UpdateProfile(ctx context.Context, userID string, patch ProfilePatch) (User, error) {
	if s.Pool == nil {
		return User{}, postgres.ErrNilPool
	}
	var user User
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		user, err = scanUser(tx.QueryRow(ctx, `
			UPDATE openfox.users
			   SET full_name = COALESCE($2, full_name),
			       bio = COALESCE($3, bio)
			 WHERE id = $1::uuid
			RETURNING `+userProfileColumns, userID, patch.FullName, patch.Bio))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return user, err
}

// ReplaceAvatar 在同一事务内锁定用户行、读取旧键并写入新头像，避免并发上传丢失旧键。
func (s PostgresStore) ReplaceAvatar(ctx context.Context, userID string, avatar Avatar) (User, string, error) {
	if s.Pool == nil {
		return User{}, "", postgres.ErrNilPool
	}
	var user User
	var previousKey *string
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			SELECT avatar_key FROM openfox.users WHERE id = $1::uuid FOR UPDATE
		`, userID).Scan(&previousKey); err != nil {
			return err
		}
		var err error
		user, err = scanUser(tx.QueryRow(ctx, `
			UPDATE openfox.users
			   SET avatar_key = $2,
			       avatar_content_type = $3,
			       avatar_sha256 = $4,
			       avatar_updated_at = $5
			 WHERE id = $1::uuid
			RETURNING `+userProfileColumns, userID, avatar.Key, avatar.ContentType, avatar.SHA256, avatar.UpdatedAt))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, "", ErrUserNotFound
	}
	if err != nil {
		return User{}, "", err
	}
	oldKey := ""
	if previousKey != nil {
		oldKey = *previousKey
	}
	return user, oldKey, nil
}

// FindAvatar 把权限判断放在 SQL 里：查询结果为空（没有头像或无权访问）与没有头像一样返回 ErrAvatarNotFound。
func (s PostgresStore) FindAvatar(ctx context.Context, viewerID, userID string) (Avatar, error) {
	if s.Pool == nil {
		return Avatar{}, postgres.ErrNilPool
	}
	var avatar Avatar
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT u.avatar_key, u.avatar_content_type, u.avatar_sha256, u.avatar_updated_at
			  FROM openfox.users u
			 WHERE u.id = $2::uuid
			   AND u.avatar_key IS NOT NULL
			   AND openfox_private.shares_active_organization($1::uuid, $2::uuid)
		`, viewerID, userID).Scan(&avatar.Key, &avatar.ContentType, &avatar.SHA256, &avatar.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Avatar{}, ErrAvatarNotFound
	}
	return avatar, err
}
