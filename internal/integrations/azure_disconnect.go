package integrations

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

// All Azure mutations lock grant → parent → identity claims → current member
// → child/session/work. Network cleanup is separate and cannot revive a parent.
func (s Service) disconnectAzure(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64, confirm string) (DisconnectResult, error) {
	var parent, grant, scope, status string
	var owner *string
	var current uint64
	err := tx.QueryRow(ctx, `SELECT coalesce(c.parent_connection_id,c.id)::text,coalesce(c.azure_grant_id,p.azure_grant_id)::text,c.scope FROM openfox.integration_connections c LEFT JOIN openfox.integration_connections p ON p.organization_id=c.organization_id AND p.id=c.parent_connection_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid AND c.provider='azure_devops'`, id).Scan(&parent, &grant, &scope)
	if err != nil {
		return DisconnectResult{}, azureMissing(err)
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_azure_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, grant).Scan(&locked); err != nil {
		return DisconnectResult{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, parent).Scan(&locked); err != nil {
		return DisconnectResult{}, err
	}
	rows, err := tx.Query(ctx, `SELECT q.subject_id FROM openfox.integration_azure_identity_claims q WHERE q.organization_id=openfox_private.current_organization_id() AND EXISTS(SELECT 1 FROM openfox.integration_connections c WHERE c.organization_id=q.organization_id AND c.azure_identity_account=q.account_id AND c.azure_identity_subject=q.subject_id AND (c.id=$1::uuid OR ($1::uuid=$2::uuid AND c.parent_connection_id=$2::uuid))) ORDER BY q.account_id,q.subject_id FOR UPDATE`, id, parent)
	if err != nil {
		return DisconnectResult{}, err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DisconnectResult{}, err
	}
	// Withdrawing one's own proof remains possible after a role becomes guest.
	if err = tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&locked); err != nil {
		return DisconnectResult{}, azureMissing(err)
	}
	if err = (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return DisconnectResult{}, err
	}
	err = tx.QueryRow(ctx, `SELECT status,owner_user_id::text,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&status, &owner, &current)
	if err != nil {
		return DisconnectResult{}, azureMissing(err)
	}
	if scope == ScopeOrganization {
		if !CanManage(actor.Role) {
			return DisconnectResult{}, ErrForbidden
		}
	} else if owner == nil || *owner != actor.UserID {
		return DisconnectResult{}, ErrNotFound
	}
	if status == StatusDisconnected {
		item, e := loadConnection(ctx, tx, id)
		return DisconnectResult{Connection: item}, e
	}
	if current != version {
		return DisconnectResult{}, ErrVersionConflict
	}
	if scope == ScopeOrganization && strings.TrimSpace(confirm) != ConfirmPhrase("Azure DevOps") {
		return DisconnectResult{}, ErrConfirmationMismatch
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='disconnected by user',disconnected_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND (id=$1::uuid OR ($1::uuid=$2::uuid AND parent_connection_id=$2::uuid)) AND status<>'DISCONNECTED'`, id, parent)
	if err != nil {
		return DisconnectResult{}, err
	}
	if scope == ScopeOrganization {
		_, err = tx.Exec(ctx, `UPDATE openfox.import_sync_links SET state='DISCONNECTED',version=version+1,lease=NULL,error_code='' WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state<>'DISCONNECTED'`, id)
		if err != nil {
			return DisconnectResult{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET status='PAUSED',status_reason='connection disconnected',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND deleted_at IS NULL AND status<>'PAUSED'`, id)
		if err != nil {
			return DisconnectResult{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET desired=false,generation=generation+1,state=CASE WHEN state='CREATE_PENDING' AND subscription_id IS NULL THEN 'DELETED' ELSE 'DELETE_PENDING' END,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),cleanup_until=clock_timestamp()+interval '24 hours',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND desired`, id)
		if err != nil {
			return DisconnectResult{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks SET secret_sealed=NULL,secret_key_id='' WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state='DELETED'`, id)
		if err != nil {
			return DisconnectResult{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_work SET state='IGNORED',last_error='connection_disconnected',lease=NULL,lease_until=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state IN ('PENDING','RUNNING')`, id)
		if err != nil {
			return DisconnectResult{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_backlinks SET state='SKIPPED',last_error='connection_disconnected',lease=NULL,lease_until=NULL WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND state IN ('PENDING','SENDING','UNKNOWN')`, id)
		if err != nil {
			return DisconnectResult{}, err
		}
		var live bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND azure_grant_id=$1::uuid AND status<>'DISCONNECTED')`, grant).Scan(&live)
		if err != nil {
			return DisconnectResult{}, err
		}
		if !live {
			// Only cleanup reads/deletes may use this grant, for at most 24h and never
			// beyond the current token expiry. Expired credentials require manual cleanup.
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants g SET status=CASE WHEN credentials_sealed IS NOT NULL AND expires_at>clock_timestamp() AND EXISTS(SELECT 1 FROM openfox.integration_azure_hooks h JOIN openfox.integration_connections c ON c.organization_id=h.organization_id AND c.id=h.connection_id WHERE h.organization_id=g.organization_id AND c.azure_grant_id=g.id AND h.state<>'DELETED') THEN 'CLEANUP' ELSE 'DISCONNECTED' END,cleanup_until=least(expires_at,clock_timestamp()+interval '24 hours'),generation=generation+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,retry_at=NULL,updated_at=clock_timestamp() WHERE g.organization_id=openfox_private.current_organization_id() AND g.id=$1::uuid`, grant)
			if err != nil {
				return DisconnectResult{}, err
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_azure_grants SET credentials_sealed=NULL,credentials_key_id='',expires_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='DISCONNECTED'`, grant)
			if err != nil {
				return DisconnectResult{}, err
			}
		}
	}
	_, err = tx.Exec(ctx, `WITH pending AS(SELECT id FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND provider='azure_devops' AND connection_id=$1::uuid AND status IN ('PENDING','SELECTING') FOR UPDATE SKIP LOCKED) UPDATE openfox.integration_oauth_states s SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL FROM pending p WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=p.id`, id)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return DisconnectResult{}, err
	}
	item, err := loadConnection(ctx, tx, id)
	return DisconnectResult{Connection: item, Changed: true}, err
}
