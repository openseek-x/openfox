package integrations

import (
	"context"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

func (s Service) SlackDeliveries(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, appID, cursor string) (SlackDeliveriesPage, error) {
	out := SlackDeliveriesPage{Deliveries: []SlackDelivery{}}
	if cursor != "" && !membership.ValidateUUID(cursor) {
		return out, ErrInvalidRequest
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	app, e := loadSlackApp(ctx, tx, appID)
	if e != nil {
		return out, e
	}
	if e = slackAppManager(ctx, tx, org, current, app); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT id::text,app_id::text,source_kind,part,state,status_reason,channel_id,thread_ts,remote_ts,created_at,version FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) ORDER BY id LIMIT 51`, appID, cursor)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var d SlackDelivery
		if e = rows.Scan(&d.ID, &d.AppID, &d.SourceKind, &d.Part, &d.State, &d.StatusReason, &d.ChannelID, &d.ThreadTS, &d.RemoteTS, &d.CreatedAt, &d.Version); e != nil {
			return out, e
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Deliveries) > 50 {
		out.Deliveries = out.Deliveries[:50]
		out.NextCursor = out.Deliveries[49].ID
	}
	return out, nil
}
