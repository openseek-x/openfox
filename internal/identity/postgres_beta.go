package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
)

func (s PostgresStore) LoadBetaFeatures(ctx context.Context, userID string) (map[string]bool, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	saved := map[string]bool{}
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT feature, enabled
			FROM openfox.user_beta_features
			WHERE user_id = $1::uuid
		`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var feature string
			var enabled bool
			if err := rows.Scan(&feature, &enabled); err != nil {
				return err
			}
			saved[feature] = enabled
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func (s PostgresStore) SaveBetaFeature(ctx context.Context, userID, key string, enabled bool) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO openfox.user_beta_features (user_id, feature, enabled, updated_at)
			VALUES ($1::uuid, $2, $3, clock_timestamp())
			ON CONFLICT (user_id, feature) DO UPDATE
			   SET enabled = EXCLUDED.enabled,
			       updated_at = EXCLUDED.updated_at
		`, userID, key, enabled)
		return err
	})
}
