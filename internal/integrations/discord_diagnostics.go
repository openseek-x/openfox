package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"time"
)

type DiscordDelivery struct {
	ID               string     `json:"id"`
	ChannelID        string     `json:"channel_id"`
	ReplyToMessageID string     `json:"reply_to_message_id"`
	State            string     `json:"state"`
	Reason           string     `json:"reason"`
	CreatedAt        time.Time  `json:"created_at"`
	FinishedAt       *time.Time `json:"finished_at,omitempty"`
}
type DiscordDeliveryPage struct {
	Items []DiscordDelivery `json:"items"`
	Next  string            `json:"next"`
}

func (s Service) DiscordDeliveries(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot, after string) (DiscordDeliveryPage, error) {
	out := DiscordDeliveryPage{Items: []DiscordDelivery{}}
	if _, e := managedDiscord(ctx, tx, org, actor, bot, false); e != nil {
		return out, e
	}
	var cursor *time.Time
	if after != "" {
		if !membership.ValidateUUID(after) {
			return out, ErrInvalidRequest
		}
		var t time.Time
		e := tx.QueryRow(ctx, `SELECT created_at FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND id=$2::uuid`, bot, after).Scan(&t)
		if errors.Is(e, pgx.ErrNoRows) {
			return out, ErrInvalidRequest
		}
		if e != nil {
			return out, e
		}
		cursor = &t
	}
	rows, e := tx.Query(ctx, `SELECT id::text,chat_id,reply_to_remote_id,CASE WHEN status='FAILED' AND last_error='delivery_unknown' THEN 'UNKNOWN' ELSE status END,last_error,created_at,finished_at FROM openfox.im_reply_outbox WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND ($2::timestamptz IS NULL OR (created_at,id)<($2::timestamptz,NULLIF($3,'')::uuid)) ORDER BY created_at DESC,id DESC LIMIT 101`, bot, cursor, after)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v DiscordDelivery
		if e = rows.Scan(&v.ID, &v.ChannelID, &v.ReplyToMessageID, &v.State, &v.Reason, &v.CreatedAt, &v.FinishedAt); e != nil {
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > 100 {
		out.Items = out.Items[:100]
		out.Next = out.Items[99].ID
	}
	return out, nil
}
