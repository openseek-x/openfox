package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/platform/postgres"
)

// LoadPreferences 没有行时返回默认值（Version = 0）。
func (s PostgresStore) LoadPreferences(ctx context.Context, userID string) (Preferences, error) {
	if s.Pool == nil {
		return Preferences{}, postgres.ErrNilPool
	}
	prefs := defaultPreferences()
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var shortcuts string
		err := tx.QueryRow(ctx, `
			SELECT theme, locale, outline_color, open_session_on_assign, open_session_on_quick_command,
			       send_shortcut, command_shortcuts::text, version, density
			  FROM openfox.user_preferences
			 WHERE user_id = $1::uuid
		`, userID).Scan(&prefs.Theme, &prefs.Locale, &prefs.OutlineColor, &prefs.OpenSessionOnAssign,
			&prefs.OpenSessionOnQuickCommand, &prefs.SendShortcut, &shortcuts, &prefs.Version, &prefs.Density)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		prefs.CommandShortcuts = map[string]string{}
		return json.Unmarshal([]byte(shortcuts), &prefs.CommandShortcuts)
	})
	if err != nil {
		return Preferences{}, err
	}
	return prefs, nil
}

// SavePreferences 版本为 0 时插入（主键冲突即已有人先保存）；否则按版本条件更新，0 行即版本冲突。
func (s PostgresStore) SavePreferences(ctx context.Context, userID string, expectedVersion int64, next Preferences) (Preferences, error) {
	if s.Pool == nil {
		return Preferences{}, postgres.ErrNilPool
	}
	shortcuts, err := json.Marshal(next.CommandShortcuts)
	if err != nil {
		return Preferences{}, err
	}
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if expectedVersion == 0 {
			err := tx.QueryRow(ctx, `
				INSERT INTO openfox.user_preferences (
					user_id, theme, locale, outline_color, open_session_on_assign, open_session_on_quick_command,
					send_shortcut, command_shortcuts, version, updated_at, density
				) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::jsonb, 1, clock_timestamp(), $9)
				RETURNING version
			`, userID, next.Theme, next.Locale, next.OutlineColor, next.OpenSessionOnAssign,
				next.OpenSessionOnQuickCommand, next.SendShortcut, string(shortcuts), next.Density).Scan(&next.Version)
			if isUniqueViolation(err) {
				return ErrPreferencesVersionConflict
			}
			return err
		}
		err := tx.QueryRow(ctx, `
			UPDATE openfox.user_preferences
			   SET theme = $3,
			       locale = $4,
			       outline_color = $5,
			       open_session_on_assign = $6,
			       open_session_on_quick_command = $7,
			       send_shortcut = $8,
			       command_shortcuts = $9::jsonb,
			       density = $10,
			       version = version + 1,
			       updated_at = clock_timestamp()
			 WHERE user_id = $1::uuid AND version = $2
			RETURNING version
		`, userID, expectedVersion, next.Theme, next.Locale, next.OutlineColor, next.OpenSessionOnAssign,
			next.OpenSessionOnQuickCommand, next.SendShortcut, string(shortcuts), next.Density).Scan(&next.Version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPreferencesVersionConflict
		}
		return err
	})
	if err != nil {
		return Preferences{}, err
	}
	return next, nil
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}
