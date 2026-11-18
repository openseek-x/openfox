package integrations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

// ProcessTelegramControls is the provider-specific step for the existing
// IMWorker. Root wires its call and organization scan after 145/146 freeze.
// The claim, HTTP and settlement use three separate phases, never a network TX.
func (w *IMWorker) ProcessTelegramControls(ctx context.Context, org string) error {
	ids := []string{}
	err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT bot_id::text FROM openfox.telegram_bot_controls WHERE organization_id=openfox_private.current_organization_id() AND state<>'DISCONNECTED'
   AND ((next_attempt_at IS NOT NULL AND next_attempt_at<=clock_timestamp()) OR (control_status='SENDING' AND control_lease_until<=clock_timestamp())) ORDER BY COALESCE(next_attempt_at,control_lease_until),bot_id LIMIT 10`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		var claim *TelegramControlClaim
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			claim, e = w.Service.ClaimTelegramControl(ctx, tx, org, id)
			return e
		})
		if err != nil {
			return err
		}
		if claim == nil {
			continue
		}
		result := w.Service.RunTelegramControl(ctx, *claim)
		err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			return w.Service.FinishTelegramControl(ctx, tx, org, *claim, result)
		})
		if err != nil && !errors.Is(err, ErrVersionConflict) {
			return err
		}
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error { return w.Service.CleanupTelegram(ctx, tx) })
}

type TelegramDeliveryDiagnostic struct {
	ID               string    `json:"id"`
	ChatID           string    `json:"chat_id"`
	ReplyToMessageID string    `json:"reply_to_message_id"`
	State            string    `json:"state"`
	LastError        string    `json:"last_error"`
	RemoteMessageID  string    `json:"remote_message_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	CanRetry         bool      `json:"can_retry"`
}

func (s Service) TelegramDeliveryDiagnostics(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID, after string) ([]TelegramDeliveryDiagnostic, error) {
	if _, err := managedTelegram(ctx, tx, org, actor, botID, false); err != nil {
		return nil, err
	}
	if after != "" && !membership.ValidateUUID(after) {
		return nil, ErrInvalidRequest
	}
	rows, err := tx.Query(ctx, `SELECT o.id::text,o.chat_id,o.reply_to_remote_id,o.status,o.last_error,o.remote_message_id,o.created_at FROM openfox.im_reply_outbox o
 WHERE o.organization_id=openfox_private.current_organization_id() AND o.bot_id=$1::uuid AND ($2='' OR o.id>NULLIF($2,'')::uuid) ORDER BY o.id LIMIT 50`, botID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TelegramDeliveryDiagnostic{}
	for rows.Next() {
		var v TelegramDeliveryDiagnostic
		if err = rows.Scan(&v.ID, &v.ChatID, &v.ReplyToMessageID, &v.State, &v.LastError, &v.RemoteMessageID, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func telegramAudit(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, action, id string) error {
	return recordIMAudit(ctx, tx, org, actor.UserID, actor.AuthorizationVersion, "integration.telegram_"+action, "integration", id, "", map[string]string{"source": ProviderTelegram, "result": action})
}
