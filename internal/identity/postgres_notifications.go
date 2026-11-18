package identity

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/platform/postgres"
)

const notificationChannelColumns = `channel,enabled,disabled_events,
 COALESCE(direct_messages,channel IN ('desktop','browser')),event_overrides,muted_groups,revision`

func scanNotificationChannel(row pgx.Row) (NotificationChannel, error) {
	var item NotificationChannel
	err := row.Scan(&item.Channel, &item.Enabled, &item.DisabledEvents, &item.DirectMessages, &item.EventOverrides, &item.MutedGroups, &item.Revision)
	item.Customized = true
	return item, err
}
func (s PostgresStore) LoadNotificationChannels(ctx context.Context, userID string) (map[string]NotificationChannel, error) {
	if s.Pool == nil {
		return nil, postgres.ErrNilPool
	}
	saved := map[string]NotificationChannel{}
	err := s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+notificationChannelColumns+` FROM openfox.user_notification_channels WHERE user_id=$1::uuid`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanNotificationChannel(rows)
			if err != nil {
				return err
			}
			saved[item.Channel] = item
		}
		return rows.Err()
	})
	return saved, err
}
func (s PostgresStore) SaveNotificationChannel(ctx context.Context, userID string, channel *NotificationChannel) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		item, err := scanNotificationChannel(tx.QueryRow(ctx, `
   INSERT INTO openfox.user_notification_channels(user_id,channel,enabled,disabled_events,direct_messages)
   VALUES($1::uuid,$2,$3,$4,$5)
   ON CONFLICT(user_id,channel) DO UPDATE SET enabled=EXCLUDED.enabled,
    disabled_events=EXCLUDED.disabled_events,
    direct_messages=COALESCE(EXCLUDED.direct_messages,openfox.user_notification_channels.direct_messages),
    revision=nextval('openfox.notification_channel_revision'),updated_at=clock_timestamp()
   RETURNING `+notificationChannelColumns, userID, channel.Channel, channel.Enabled, channel.DisabledEvents, channel.DirectMessages))
		if err == nil {
			item.Available = channel.Available
			*channel = item
		}
		return err
	})
}
func (s PostgresStore) PatchNotificationChannel(ctx context.Context, userID, channel string, patch NotificationChannelPatch) (NotificationChannel, error) {
	if s.Pool == nil {
		return NotificationChannel{}, postgres.ErrNilPool
	}
	overrides := patch.EventOverrides
	if overrides == nil {
		overrides = map[string]*bool{}
	}
	encoded, err := json.Marshal(overrides)
	if err != nil {
		return NotificationChannel{}, err
	}
	var item NotificationChannel
	err = s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var invalid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jsonb_object_keys($1::jsonb) AS supplied(key)
    WHERE NOT EXISTS(SELECT 1 FROM openfox.inbox_policy_keys k WHERE k.key=supplied.key AND k.group_key IS NOT NULL))
    OR EXISTS(SELECT 1 FROM unnest($2::text[]) AS supplied(key)
    WHERE NOT EXISTS(SELECT 1 FROM openfox.inbox_policy_keys k WHERE k.key=supplied.key AND k.group_key IS NULL))`, string(encoded), patch.MutedGroups).Scan(&invalid); err != nil {
			return err
		}
		if invalid {
			return ErrInvalidNotificationSettings
		}
		if *patch.Revision == 0 {
			item, err = scanNotificationChannel(tx.QueryRow(ctx, `
    INSERT INTO openfox.user_notification_channels(user_id,channel,enabled,disabled_events,direct_messages,event_overrides,muted_groups)
    VALUES($1::uuid,$2,COALESCE($3,$2 NOT IN ('email','wecom','slack')),'{}',COALESCE($4,$2 IN ('desktop','browser')),$5::jsonb,COALESCE($6,'{}'::text[]))
    ON CONFLICT DO NOTHING RETURNING `+notificationChannelColumns, userID, channel, patch.Enabled, patch.DirectMessages, string(encoded), patch.MutedGroups))
		} else {
			item, err = scanNotificationChannel(tx.QueryRow(ctx, `
    UPDATE openfox.user_notification_channels SET enabled=COALESCE($3,enabled),direct_messages=COALESCE($4,direct_messages),
     event_overrides=event_overrides||$5::jsonb,muted_groups=COALESCE($6,muted_groups),revision=nextval('openfox.notification_channel_revision'),updated_at=clock_timestamp()
    WHERE user_id=$1::uuid AND channel=$2 AND revision=$7 RETURNING `+notificationChannelColumns, userID, channel, patch.Enabled, patch.DirectMessages, string(encoded), patch.MutedGroups, *patch.Revision))
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotificationRevisionConflict
		}
		return err
	})
	return item, err
}
func (s PostgresStore) DeleteNotificationChannel(ctx context.Context, userID, channel string, revision *int64) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	return s.Pool.WithTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel=$2 AND ($3::bigint IS NULL OR revision=$3)`, userID, channel, revision)
		if err != nil {
			return err
		}
		if revision != nil && tag.RowsAffected() == 0 {
			if *revision != 0 {
				return ErrNotificationRevisionConflict
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.user_notification_channels WHERE user_id=$1::uuid AND channel=$2)`, userID, channel).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return ErrNotificationRevisionConflict
			}
		}
		return nil
	})
}
