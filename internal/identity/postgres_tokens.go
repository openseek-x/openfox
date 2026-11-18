package identity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
)

// CreatePersonalAccessToken 用按用户的 advisory xact lock 串行化「计数 + 插入」，保证并发创建时不超过上限。
func (s PostgresStore) CreatePersonalAccessToken(ctx context.Context, userID string, token PersonalAccessToken, tokenHash []byte, limit int) (PersonalAccessToken, error) {
	if s.Pool == nil {
		return PersonalAccessToken{}, postgres.ErrNilPool
	}
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "personal_access_tokens:"+userID); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*)
			FROM openfox.personal_access_tokens
			WHERE user_id = $1::uuid AND revoked_at IS NULL
		`, userID).Scan(&count); err != nil {
			return err
		}
		if count >= limit {
			return ErrTokenLimit
		}
		return tx.QueryRow(ctx, `
			INSERT INTO openfox.personal_access_tokens (
				user_id, name, token_prefix, token_hash, expires_at, created_at
			) VALUES ($1::uuid, $2, $3, $4, $5, $6)
			RETURNING id::text, created_at
		`, userID, token.Name, token.Prefix, tokenHash, token.ExpiresAt, token.CreatedAt).Scan(&token.ID, &token.CreatedAt)
	})
	if err != nil {
		return PersonalAccessToken{}, err
	}
	return token, nil
}

func (s PostgresStore) ListPersonalAccessTokens(ctx context.Context, userID string, now time.Time) ([]PersonalAccessToken, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	items := make([]PersonalAccessToken, 0)
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, token_prefix, created_at, expires_at, last_used_at
			FROM openfox.personal_access_tokens
			WHERE user_id = $1::uuid AND revoked_at IS NULL
			ORDER BY created_at DESC, id DESC
		`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PersonalAccessToken
			if err := rows.Scan(&item.ID, &item.Name, &item.Prefix, &item.CreatedAt, &item.ExpiresAt, &item.LastUsedAt); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s PostgresStore) RevokePersonalAccessToken(ctx context.Context, userID, tokenID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.personal_access_tokens
			   SET revoked_at = $3
			 WHERE id = $1::uuid AND user_id = $2::uuid AND revoked_at IS NULL
		`, tokenID, userID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTokenNotFound
		}
		return nil
	})
}

// AuthenticatePersonalAccessToken 与会话认证一样要求账号凭据为 ACTIVE；last_used_at 为空或早于 lastUsedBefore 时才更新。
func (s PostgresStore) AuthenticatePersonalAccessToken(ctx context.Context, tokenHash []byte, now, lastUsedBefore time.Time) (string, string, error) {
	if s.Pool == nil {
		return "", "", postgres.ErrNilPool
	}
	var tokenID, userID string
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT t.id::text, t.user_id::text
			FROM openfox.personal_access_tokens t
			JOIN openfox.password_credentials pc ON pc.user_id = t.user_id
			WHERE t.token_hash = $1
			  AND t.revoked_at IS NULL
			  AND (t.expires_at IS NULL OR t.expires_at > $2)
			  AND pc.status = 'ACTIVE'
		`, tokenHash, now).Scan(&tokenID, &userID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE openfox.personal_access_tokens
			   SET last_used_at = $2
			 WHERE id = $1::uuid
			   AND revoked_at IS NULL
			   AND (last_used_at IS NULL OR last_used_at <= $3)
		`, tokenID, now, lastUsedBefore)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrTokenNotFound
	}
	if err != nil {
		return "", "", err
	}
	return tokenID, userID, nil
}
