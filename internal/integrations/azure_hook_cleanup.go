package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Only local references determine whether a Boards hook remains wanted. This
// maintenance path cannot create a subscription or restore a user authorization.
func (r AzureRuntime) pruneAzureBoardsHooks(ctx context.Context, org string) error {
	type target struct{ connection, project string }
	targets := []target{}
	e := r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT DISTINCT h.connection_id::text,h.project_id::text FROM openfox.integration_azure_hooks h WHERE h.organization_id=openfox_private.current_organization_id() AND h.repository_id IS NULL AND h.desired AND NOT EXISTS(SELECT 1 FROM openfox.import_sync_links l WHERE l.organization_id=h.organization_id AND l.connection_id=h.connection_id AND l.project_id=h.project_id::text AND l.state='ACTIVE') AND NOT EXISTS(SELECT 1 FROM openfox.import_azure_crawls c JOIN openfox.import_jobs j ON j.organization_id=c.organization_id AND j.id=c.job_id WHERE c.organization_id=h.organization_id AND c.connection_id=h.connection_id AND c.project_id=h.project_id AND ((j.status='DRAFT' AND j.created_at>clock_timestamp()-interval '24 hours') OR j.status IN('QUEUED','RUNNING'))) ORDER BY 1,2 LIMIT 100`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var t target
			if e = rows.Scan(&t.connection, &t.project); e != nil {
				return e
			}
			targets = append(targets, t)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, t := range targets {
		if e = r.inOrg(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			if _, _, _, e := lockAzureHookResources(ctx, tx, t.connection); e != nil {
				return e
			}
			// Recheck references under the grant lock used by new draft/start/resume.
			_, e := tx.Exec(ctx, `UPDATE openfox.integration_azure_hooks h SET desired=false,generation=generation+1,state=CASE WHEN state='CREATE_PENDING' THEN 'DELETED' ELSE 'DELETE_PENDING' END,secret_sealed=CASE WHEN state='CREATE_PENDING' THEN NULL ELSE secret_sealed END,secret_key_id=CASE WHEN state='CREATE_PENDING' THEN '' ELSE secret_key_id END,lease=NULL,lease_until=NULL,retry_at=clock_timestamp(),cleanup_until=clock_timestamp()+interval '24 hours',updated_at=clock_timestamp() WHERE h.organization_id=openfox_private.current_organization_id() AND h.connection_id=$1::uuid AND h.project_id=$2::uuid AND h.repository_id IS NULL AND h.desired AND NOT EXISTS(SELECT 1 FROM openfox.import_sync_links l WHERE l.organization_id=h.organization_id AND l.connection_id=h.connection_id AND l.project_id=h.project_id::text AND l.state='ACTIVE') AND NOT EXISTS(SELECT 1 FROM openfox.import_azure_crawls c JOIN openfox.import_jobs j ON j.organization_id=c.organization_id AND j.id=c.job_id WHERE c.organization_id=h.organization_id AND c.connection_id=h.connection_id AND c.project_id=h.project_id AND ((j.status='DRAFT' AND j.created_at>clock_timestamp()-interval '24 hours') OR j.status IN('QUEUED','RUNNING')))`, t.connection, t.project)
			return e
		}); e != nil {
			return e
		}
	}
	return nil
}
