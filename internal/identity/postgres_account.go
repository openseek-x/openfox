package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
)

// userProfileColumns 是读取 User 时的列顺序，与 scanUser 对应。
const userProfileColumns = `id::text, email, full_name, bio, avatar_updated_at, access_tier`

func scanUser(row pgx.Row) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.Email, &user.FullName, &user.Bio, &user.AvatarUpdatedAt, &user.AccessTier)
	return user, err
}

func (s PostgresStore) FindUser(ctx context.Context, userID string) (User, error) {
	if s.Pool == nil {
		return User{}, postgres.ErrNilPool
	}
	var user User
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		user, err = scanUser(tx.QueryRow(ctx, `
			SELECT `+userProfileColumns+`
			FROM openfox.users
			WHERE id = $1::uuid
		`, userID))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	return user, err
}

// RevokeSession 只更新未吊销的会话；重复吊销不报错，保证注销幂等。
func (s PostgresStore) RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.user_sessions
			   SET revoked_at = GREATEST($2, issued_at)
			 WHERE token_hash = $1 AND revoked_at IS NULL
		`, tokenHash, now)
		return err
	})
}
