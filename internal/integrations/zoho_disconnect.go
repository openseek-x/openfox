package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"strings"
)

// Lock order is parent → runtime → current member → child. Session cleanup uses
// SKIP LOCKED: a consumed/selected proof still fails its final parent/version check.
func (s Service) disconnectZoho(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64, confirm string) (DisconnectResult, error) {
	var parent, scope string
	e := tx.QueryRow(ctx, `SELECT coalesce(parent_connection_id,id)::text,scope FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='zoho_bugtracker'`, id).Scan(&parent, &scope)
	if e != nil {
		return DisconnectResult{}, zohoMissing(e)
	}
	var locked string
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, parent).Scan(&locked); e != nil {
		return DisconnectResult{}, e
	}
	if e = tx.QueryRow(ctx, `SELECT connection_id::text FROM openfox.integration_zoho_runtime WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid FOR UPDATE`, parent).Scan(&locked); e != nil {
		return DisconnectResult{}, e
	}
	// Own identity can still be withdrawn after the user becomes a guest.
	if e = tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&locked); e != nil {
		return DisconnectResult{}, zohoMissing(e)
	}
	if e = (collaboration.Service{}).CheckActor(ctx, tx, org, actor); e != nil {
		return DisconnectResult{}, e
	}
	var owner *string
	var status string
	var current uint64
	e = tx.QueryRow(ctx, `SELECT owner_user_id::text,status,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, id).Scan(&owner, &status, &current)
	if e != nil {
		return DisconnectResult{}, e
	}
	if scope == ScopeOrganization {
		if !CanManage(actor.Role) {
			return DisconnectResult{}, ErrForbidden
		}
	} else if owner == nil || *owner != actor.UserID {
		return DisconnectResult{}, ErrNotFound
	}
	if status == StatusDisconnected {
		c, e := loadConnection(ctx, tx, id)
		return DisconnectResult{Connection: c}, e
	}
	if current != version {
		return DisconnectResult{}, ErrVersionConflict
	}
	if scope == ScopeOrganization && strings.TrimSpace(confirm) != ConfirmPhrase("Zoho BugTracker") {
		return DisconnectResult{}, ErrConfirmationMismatch
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='DISCONNECTED',status_reason='disconnected by user',credentials_sealed=NULL,credentials_key_id='',credentials_hint='',token_expires_at=NULL,webhook_token_hash=NULL,disconnected_at=clock_timestamp(),updated_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND provider='zoho_bugtracker' AND (id=$1::uuid OR ($1::uuid=$2::uuid AND parent_connection_id=$2::uuid)) AND status<>'DISCONNECTED'`, id, parent)
	if e != nil {
		return DisconnectResult{}, e
	}
	if scope == ScopeOrganization {
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_zoho_runtime SET refresh_fingerprint=NULL,credential_generation=credential_generation+1,credentials_version=credentials_version+1,refresh_lease=NULL,refresh_lease_until=NULL,request_lease=NULL,request_lease_until=NULL,retry_at=NULL,last_error='connection_disconnected',updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid`, id)
		if e != nil {
			return DisconnectResult{}, e
		}
	}
	_, e = tx.Exec(ctx, `WITH pending AS(SELECT id FROM openfox.integration_oauth_states WHERE organization_id=openfox_private.current_organization_id() AND provider='zoho_bugtracker' AND connection_id=$1::uuid AND status IN ('PENDING','SELECTING') FOR UPDATE SKIP LOCKED) UPDATE openfox.integration_oauth_states s SET status='EXPIRED',pending_accounts='[]',pending_sealed=NULL,code_verifier_sealed=NULL FROM pending p WHERE s.organization_id=openfox_private.current_organization_id() AND s.id=p.id`, id)
	if e != nil {
		return DisconnectResult{}, e
	}
	c, e := loadConnection(ctx, tx, id)
	return DisconnectResult{Connection: c, Changed: true}, e
}
