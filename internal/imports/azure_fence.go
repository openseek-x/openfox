package imports

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations"
)

// Source locks precede job/link locks, matching grant refresh and disconnect.
// The job's source connection is immutable once the draft is created.
func lockAzureImportSource(ctx context.Context, tx pgx.Tx, job string) (integrations.AzureAccess, error) {
	var connection string
	var version, generation uint64
	e := tx.QueryRow(ctx, `SELECT c.connection_id::text,c.connection_version,c.grant_generation FROM openfox.import_azure_crawls c JOIN openfox.import_jobs j ON j.organization_id=c.organization_id AND j.id=c.job_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.job_id=$1::uuid AND j.source='azure_devops_api'`, job).Scan(&connection, &version, &generation)
	if errors.Is(e, pgx.ErrNoRows) {
		return integrations.AzureAccess{}, nil
	}
	if e != nil {
		return integrations.AzureAccess{}, e
	}
	return integrations.LockAzureAccess(ctx, tx, connection, version, generation)
}
func recordAzureRevision(ctx context.Context, tx pgx.Tx, job, external, task string, revision int64) error {
	tag, e := tx.Exec(ctx, `UPDATE openfox.import_sync_items SET source_revision=$4 WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND external_id=$2 AND task_id=$3::uuid AND source_revision<=$4`, job, external, task, revision)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}

// Cancellation may clean up an old proof after a reauthorization; it never
// reuses the crawl's stale grant generation to restore business access.
func lockAzureImportLifecycle(ctx context.Context, tx pgx.Tx, job string) (integrations.AzureAccess, string, error) {
	var a integrations.AzureAccess
	var project string
	e := tx.QueryRow(ctx, `SELECT c.id::text,c.azure_grant_id::text,c.version,g.generation,x.project_id::text FROM openfox.import_azure_crawls x JOIN openfox.integration_connections c ON c.organization_id=x.organization_id AND c.id=x.connection_id JOIN openfox.integration_azure_grants g ON g.organization_id=c.organization_id AND g.id=c.azure_grant_id WHERE x.organization_id=openfox_private.current_organization_id() AND x.job_id=$1::uuid AND c.status='ACTIVE' AND g.status='ACTIVE'`, job).Scan(&a.ConnectionID, &a.GrantID, &a.ConnectionVersion, &a.GrantGeneration, &project)
	if errors.Is(e, pgx.ErrNoRows) {
		return a, project, nil
	}
	if e != nil {
		return a, project, e
	}
	return a, project, integrations.LockAzureHookConfiguration(ctx, tx, a)
}

// Lock the actual membership before the space row (member removal takes member
// locks first). This fence remains held through each local task write.
func LockAzureSpaceWriter(ctx context.Context, tx pgx.Tx, space, user string) error {
	var role, status string
	if e := tx.QueryRow(ctx, `SELECT role,status FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, user).Scan(&role, &status); e != nil {
		return e
	}
	if status != "ACTIVE" || (role != "OWNER" && role != "ADMIN") {
		return ErrForbidden
	}
	if space == "" {
		return nil
	}
	var memberRole, memberStatus string
	e := tx.QueryRow(ctx, `SELECT role,status FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, space, user).Scan(&memberRole, &memberStatus)
	absent := errors.Is(e, pgx.ErrNoRows)
	if e != nil && !absent {
		return e
	}
	var visibility string
	if e = tx.QueryRow(ctx, `SELECT visibility FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL AND retired_at IS NULL AND status='ACTIVE' FOR SHARE`, space).Scan(&visibility); e != nil {
		return e
	}
	if absent && visibility == "SHARED" || memberStatus == "ACTIVE" && (memberRole == "OWNER" || memberRole == "EDITOR" || memberRole == "MEMBER") {
		return nil
	}
	return ErrForbidden
}
