package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

func (s Service) AsanaSource(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string) (AsanaAccess, error) {
	if err := checkPersonalActor(ctx, tx, org, actor); err != nil {
		return AsanaAccess{}, err
	}
	if !CanManage(actor.Role) {
		return AsanaAccess{}, ErrForbidden
	}
	if !s.asanaConfigured(false) {
		return AsanaAccess{}, ErrNotConfigured
	}
	return LockAsanaAccess(ctx, tx, id, 0, 0)
}

// LockAsanaAccess fences worker commits without opening credentials. Lock order
// is always grant -> parent, including late HTTP results and source inspection.
func LockAsanaAccess(ctx context.Context, tx pgx.Tx, id string, version, generation uint64) (AsanaAccess, error) {
	var out AsanaAccess
	if !membership.ValidateUUID(id) {
		return out, ErrInvalidRequest
	}
	err := tx.QueryRow(ctx, `SELECT asana_grant_id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='asana' AND scope='ORGANIZATION' AND status='ACTIVE'`, id).Scan(&out.GrantID)
	if err != nil {
		return out, asanaMissing(err)
	}
	err = tx.QueryRow(ctx, `SELECT generation FROM openfox.integration_asana_grants WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE' FOR SHARE`, out.GrantID).Scan(&out.GrantGeneration)
	if err != nil {
		return out, asanaMissing(err)
	}
	err = tx.QueryRow(ctx, `SELECT external_id,version FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='asana' AND scope='ORGANIZATION' AND status='ACTIVE' AND asana_grant_id=$2::uuid FOR SHARE`, id, out.GrantID).Scan(&out.WorkspaceID, &out.ConnectionVersion)
	if err != nil {
		return out, asanaMissing(err)
	}
	if version != 0 && out.ConnectionVersion != version || generation != 0 && out.GrantGeneration != generation {
		return out, ErrVersionConflict
	}
	out.ConnectionID = id
	return out, nil
}
