package integrations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// SyncSlackDeployment runs once before HTTP and dispatch start. All server
// instances must use the same configuration; secrets are not hot-reloaded.
// Only local gates change. Reinstallation and signed URL verification remain
// separate requirements, and historical/UNKNOWN deliveries are retained.
func (s Service) SyncSlackDeployment(ctx context.Context, pool SyncRunner) error {
	if pool == nil {
		return errors.New("slack: nil deployment store")
	}
	fingerprint := s.SlackDeploymentFingerprint()
	w := IMWorker{Pool: pool, Service: s}
	var after any
	for page := 0; page < 1000; page++ {
		var orgs []string
		err := pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.slack_deployment_organizations($1::uuid,$2)`, after, fingerprint)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var org string
				if err = rows.Scan(&org); err != nil {
					return err
				}
				orgs = append(orgs, org)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, org := range orgs {
			err = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
				// The predicate is re-evaluated under PostgreSQL's row lock: a concurrent
				// successful OAuth with the current fingerprint cannot be overwritten.
				rows, err := tx.Query(ctx, `UPDATE openfox.slack_apps SET deployment_fingerprint=$1,state='NEEDS_REAUTH',status_reason='deployment_configuration_changed',generation=generation+1,version=version+1,url_verified_at=NULL,activated_at=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND kind='CENTRAL' AND state NOT IN ('DISCONNECTED','CANCELLED','ORPHANED_APP') AND deployment_fingerprint IS DISTINCT FROM $1 RETURNING connection_id::text`, fingerprint)
				if err != nil {
					return err
				}
				var ids []string
				for rows.Next() {
					var id string
					if err = rows.Scan(&id); err != nil {
						rows.Close()
						return err
					}
					ids = append(ids, id)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
				for _, id := range ids {
					if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='slack_configuration_changed',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'DISCONNECTED'`, id); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			after = org
		}
		if len(orgs) < 100 {
			return nil
		}
	}
	return errors.New("slack: deployment synchronization page budget exceeded")
}
