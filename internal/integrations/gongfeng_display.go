package integrations

import (
	"context"
	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/gongfeng"
)

// Repository IDs scope both uniqueness and mutations; paths are display data.
// A rename never moves an existing task association to a new project that later
// reuses the old path. Existing Azure repository_display provides this split.
func refreshGongfengDisplay(ctx context.Context, tx pgx.Tx, r GongfengRepository) error {
	key := gongfengRepositoryKey(r.ProjectID)
	_, err := tx.Exec(ctx, `UPDATE openfox.integration_sync_links SET external_project_name=$2,
 settings=settings||jsonb_build_object('repository_url',$3::text,'repository_private',COALESCE($4::boolean,false)),updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND external_project_id=$5`, r.SyncLinkID, r.Path, gongfeng.Origin+"/"+r.Path, r.Private, key)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.task_git_links SET repository_display=$2,repository_private=COALESCE($4::boolean,false),
 url=CASE WHEN kind='PULL_REQUEST' THEN $3||'/merge_requests/'||number::text ELSE $3||'/commit/'||sha END,
 updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND provider='gongfeng' AND repository=$1
 AND (repository_display<>$2 OR repository_private IS DISTINCT FROM COALESCE($4::boolean,false))`, key, r.Path, gongfeng.Origin+"/"+r.Path, r.Private)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE openfox.task_issue_links SET url=$2||'/issues/'||issue_number::text,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND sync_link_id=$1::uuid AND repository=$3 AND unlinked_at IS NULL
 AND url<>$2||'/issues/'||issue_number::text`, r.SyncLinkID, gongfeng.Origin+"/"+r.Path, key)
	return err
}
