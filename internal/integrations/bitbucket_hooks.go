package integrations

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/bitbucket"
)

// Bitbucket uses a manual hook per selected repository. The same encrypted
// callback token is recoverable by managers; seeing it never activates a hook.
func (s Service) GetBitbucketWebhook(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string) (Webhook, error) {
	if _, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, false, false); err != nil {
		return Webhook{}, err
	}
	return s.loadBitbucketWebhook(ctx, tx, org, connection)
}

func (s Service) loadBitbucketWebhook(ctx context.Context, tx pgx.Tx, org, connection string) (Webhook, error) {
	out := Webhook{Provider: ProviderBitbucket, ConnectionID: connection, ContentType: "application/json", Events: append([]string(nil), bitbucket.WebhookEvents...), Recent: []WebhookDelivery{}}
	var sealed []byte
	var status string
	if err := tx.QueryRow(ctx, `SELECT credentials_sealed,webhook_last_delivery_at,version,status FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bitbucket' AND scope='ORGANIZATION'`, connection).Scan(&sealed, &out.LastDeliveryAt, &out.Version, &status); err != nil {
		return out, bitbucketMissing(err)
	}
	if status == StatusDisconnected {
		return out, ErrNotFound
	}
	if len(sealed) > 0 {
		creds, err := s.openCredentials(org, connection, sealed)
		if err != nil {
			return out, err
		}
		if creds.WebhookToken != "" && creds.WebhookSecret != "" {
			out.PayloadURL, out.Secret = s.PayloadURL(ProviderBitbucket, creds.WebhookToken), creds.WebhookSecret
		}
	}
	rows, err := tx.Query(ctx, `SELECT event,action,status,detail,linked_count,received_at FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid ORDER BY received_at DESC,id LIMIT 10`, connection)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.Event, &d.Action, &d.Status, &d.Detail, &d.LinkedCount, &d.ReceivedAt); err != nil {
			return out, err
		}
		out.Recent = append(out.Recent, d)
	}
	return out, rows.Err()
}

// Rotation does not call the platform. Parent version and per-repository
// selection version invalidate prepared work; a signed event on the new token
// is required for EACH repository before any new backlink can be sent.
func (s Service) RotateBitbucketWebhook(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string, version uint64) (Webhook, error) {
	if version == 0 {
		return Webhook{}, ErrInvalidRequest
	}
	a, err := s.bitbucketRepositoryAccess(ctx, tx, org, connection, actor, true, true)
	if err != nil {
		return Webhook{}, err
	}
	if a.ConnectionVersion != version {
		return Webhook{}, ErrVersionConflict
	}
	var sealed []byte
	if err = tx.QueryRow(ctx, `SELECT credentials_sealed FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connection).Scan(&sealed); err != nil {
		return Webhook{}, err
	}
	creds, err := s.openCredentials(org, connection, sealed)
	if err != nil {
		return Webhook{}, err
	}
	if creds.WebhookToken, err = randomBase64URL(32); err != nil {
		return Webhook{}, err
	}
	if creds.WebhookSecret, err = randomHex(32); err != nil {
		return Webhook{}, err
	}
	raw, err := json.Marshal(creds)
	if err != nil {
		return Webhook{}, err
	}
	blob, key, err := s.sealBitbucket(raw, connectionAAD(org, a.ConnectionID))
	if err != nil {
		return Webhook{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,webhook_token_hash=$4,webhook_last_delivery_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, connection, blob, key, TokenHash(creds.WebhookToken)); err != nil {
		return Webhook{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_runtime SET hook_generation=hook_generation+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, connection); err != nil {
		return Webhook{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE openfox.integration_bitbucket_repositories SET hook_generation=0,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND status<>'REMOVED'`, connection); err != nil {
		return Webhook{}, err
	}
	return s.loadBitbucketWebhook(ctx, tx, org, connection)
}
