package integrations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

func (s Service) TAPDImportSource(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, credentials ProviderTokenInput) (ProjectSource, error) {
	if err := s.CheckProviderToken(ctx, tx, org, actor, "tapd", credentials); err != nil {
		return ProjectSource{}, err
	}
	return ProjectSource{Provider: "tapd", auth: credentials}, nil
}

type pendingTAPDConnection struct {
	Credentials    ProviderTokenInput
	Identity, Name string
}

// The job envelope can only be opened for this organization and draft.
func (s Service) StageTAPDConnection(ctx context.Context, tx pgx.Tx, org, job, actor string, credentials ProviderTokenInput, identity, name string) error {
	plain, err := json.Marshal(pendingTAPDConnection{credentials, identity, name})
	if err != nil {
		return err
	}
	blob, key, err := s.seal(plain, "tapd-import:"+org+":"+job)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE openfox.import_jobs SET credentials_sealed=$3,credentials_key_id=$4 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND created_by=$2::uuid AND source='tapd_api' AND status='DRAFT' AND credentials_sealed IS NULL`, job, actor, blob, key)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return err
}

// Call inside the import-start transaction so a rejected or repeated start cannot
// create an additional binding or lose the draft credentials.
func (s Service) BindPendingTAPDImport(ctx context.Context, tx pgx.Tx, org, job string, actor collaboration.Actor) error {
	if !membership.ValidateUUID(job) {
		return ErrNotFound
	}
	var blob []byte
	var author string
	err := tx.QueryRow(ctx, `SELECT credentials_sealed,created_by::text FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND source='tapd_api' AND status='DRAFT' AND credentials_sealed IS NOT NULL FOR UPDATE`, job).Scan(&blob, &author)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if author != actor.UserID || !CanManage(actor.Role) {
		return ErrForbidden
	}
	raw, err := s.open(blob, "tapd-import:"+org+":"+job)
	if err != nil {
		return err
	}
	var pending pendingTAPDConnection
	if json.Unmarshal(raw, &pending) != nil || !pending.Credentials.validate("tapd") {
		return ErrNeedsReauth
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='tapd' AND scope='ORGANIZATION' AND lower(external_id)=lower($1) AND status<>'DISCONNECTED' FOR UPDATE`, pending.Identity).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		var bound Connection
		bound, err = s.SaveProviderToken(ctx, tx, org, actor, "tapd", pending.Credentials, pending.Identity, pending.Name)
		id = bound.ID
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		src, err := s.ProjectSource(ctx, tx, org, actor, "tapd", id)
		if err != nil {
			return err
		}
		if src.auth != pending.Credentials {
			return ErrVersionConflict
		}
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.import_jobs SET credentials_sealed=NULL,credentials_key_id='',preview=CASE WHEN preview ? 'project_origin' THEN jsonb_set(preview,'{project_origin,connection}',to_jsonb($2::text)) ELSE preview END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, job, id)
	return err
}
