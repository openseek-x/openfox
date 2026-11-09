package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/platform/postgres"
)

func (s PostgresStore) LoadNotificationChannels(ctx context.Context, userID string) (map[string]NotificationChannel, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	saved := map[string]NotificationChannel{}
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT channel, enabled, disabled_events, COALESCE(direct_messages,channel NOT IN ('feishu','email','wecom'))
			FROM openfox.user_notification_channels
			WHERE user_id = $1::uuid
		`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item NotificationChannel
			if err := rows.Scan(&item.Channel, &item.Enabled, &item.DisabledEvents, &item.DirectMessages); err != nil {
				return err
			}
			item.Customized = true
			if item.DisabledEvents == nil {
				item.DisabledEvents = []string{}
			}
			saved[item.Channel] = item
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}

func (s PostgresStore) SaveNotificationChannel(ctx context.Context, userID string, channel *NotificationChannel) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO openfox.user_notification_channels (user_id, channel, enabled, disabled_events, direct_messages, updated_at)
			VALUES ($1::uuid, $2, $3, $4, $5, clock_timestamp())
			ON CONFLICT (user_id, channel) DO UPDATE
			   SET enabled = EXCLUDED.enabled,
                   direct_messages = COALESCE(EXCLUDED.direct_messages,openfox.user_notification_channels.direct_messages),
			       disabled_events = EXCLUDED.disabled_events,
			       updated_at = EXCLUDED.updated_at
            RETURNING COALESCE(direct_messages,channel NOT IN ('feishu','email','wecom'))
		`, userID, channel.Channel, channel.Enabled, channel.DisabledEvents, channel.DirectMessages).Scan(&channel.DirectMessages)
	})
}

func (s PostgresStore) DeleteNotificationChannel(ctx context.Context, userID, channel string) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM openfox.user_notification_channels
			WHERE user_id = $1::uuid AND channel = $2
		`, userID, channel)
		return err
	})
}
