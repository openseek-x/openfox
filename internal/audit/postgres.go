package audit

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// PGXWriter persists a validated event in the caller's existing transaction.
// Keeping the transaction boundary with the business write prevents an audit
// record from claiming a mutation that later rolls back.
type PGXWriter struct {
	Tx pgx.Tx
}

func (writer PGXWriter) WriteAudit(ctx context.Context, event Event) error {
	if writer.Tx == nil {
		return ErrNilWriter
	}
	metadata := event.SafeMetadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = writer.Tx.Exec(ctx, `
		INSERT INTO openfox.audit_events (
			id, organization_id, actor_id, action, decision, reason,
			resource_kind, resource_id, request_id, authorization_version,
			safe_metadata, created_at
		) VALUES (
			$1::uuid, $2::uuid, $3::uuid, $4, $5, NULLIF($6, ''),
			$7, $8, $9, $10, $11::jsonb, $12
		)`, event.ID, event.OrganizationID, event.ActorID, event.Action,
		event.Decision, event.Reason, event.Resource.Kind, event.Resource.ID,
		event.RequestID, int64(event.AuthorizationVersion), encoded, event.CreatedAt)
	return err
}
