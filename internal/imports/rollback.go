package imports

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

// RollbackModified 是回滚预览里「导入后被改过」的任务。
type RollbackModified struct {
	TaskID    string    `json:"task_id"`
	DisplayID string    `json:"display_id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RollbackCleanup struct {
	Statuses   int `json:"statuses"`
	Labels     int `json:"labels"`
	Iterations int `json:"iterations"`
}

// RollbackPreview 对应契约 ImportRollbackPreview。
type RollbackPreview struct {
	ProjectCount  int                `json:"project_count,omitempty"`
	Eligible      bool               `json:"eligible"`
	Reason        string             `json:"reason"`
	Deadline      *time.Time         `json:"deadline"`
	CreatedCount  int                `json:"created_count"`
	ModifiedTotal int                `json:"modified_total"`
	Modified      []RollbackModified `json:"modified"`
	Cleanup       RollbackCleanup    `json:"cleanup"`
	// SpaceCreated 为 true 表示目标空间由本次导入创建；回滚只删除任务，空间保留。
	SpaceCreated bool `json:"space_created"`
}

const maxModifiedShown = 50

// removableIterationSQL 是「迭代仍存在，且没有不属于本次创建的未删除任务在使用」的条件（r 为资源行，$1 为 job ID）。
const removableIterationSQL = `EXISTS (SELECT 1 FROM openfox.iterations i
	 WHERE i.organization_id = r.organization_id AND i.id = r.resource_id)
	 AND NOT EXISTS (SELECT 1 FROM openfox.tasks t
	  WHERE t.organization_id = r.organization_id AND t.iteration_id = r.resource_id AND t.deleted_at IS NULL
	    AND NOT EXISTS (SELECT 1 FROM openfox.import_mappings m
	         WHERE m.organization_id = t.organization_id AND m.created_job_id = $1::uuid AND NOT m.sync_created AND m.task_id = t.id AND m.released_at IS NULL))`

// createdTaskSQL 是「本 job 创建且映射仍有效」的任务条件，$1 为 job ID（t 为任务别名）。
const createdByJobSQL = `EXISTS (SELECT 1 FROM openfox.import_mappings m
	 WHERE m.organization_id = t.organization_id AND m.created_job_id = $1::uuid AND NOT m.sync_created AND m.task_id = t.id AND m.released_at IS NULL)`

const liveImportProjectsSQL = `SELECT count(*) FROM openfox.import_created_resources r JOIN openfox.projects p ON p.organization_id=r.organization_id AND p.id=r.resource_id WHERE r.organization_id=openfox_private.current_organization_id() AND r.job_id=j.id
 AND r.kind='PROJECT' AND p.version=1 AND p.status<>'canceled'
 AND NOT EXISTS(SELECT 1 FROM openfox.project_spaces ps WHERE ps.organization_id=p.organization_id AND ps.project_id=p.id AND ps.space_id<>r.space_id)
 AND NOT EXISTS(SELECT 1 FROM openfox.tasks pt WHERE pt.organization_id=p.organization_id AND pt.project_id=p.id AND pt.deleted_at IS NULL
  AND NOT EXISTS(SELECT 1 FROM openfox.import_mappings pm WHERE pm.organization_id=pt.organization_id AND pm.created_job_id=j.id AND pm.task_id=pt.id AND NOT pm.sync_created AND pm.released_at IS NULL))`

func (s Service) liveCreated(ctx context.Context, tx pgx.Tx, jobID string) (int, int, error) {
	var n, projects int
	err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM openfox.tasks t
         WHERE t.organization_id=j.organization_id AND t.deleted_at IS NULL AND `+createdByJobSQL+`), (`+liveImportProjectsSQL+`)
        FROM openfox.import_jobs j WHERE j.organization_id=openfox_private.current_organization_id() AND j.id=$1::uuid`,
		jobID).Scan(&n, &projects)
	return n, projects, err
}

// RollbackPreview 回滚预览：可否回滚、将删除的任务数、导入后被改过的任务与将清理的状态 / 标签数。
func (s Service) RollbackPreview(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (RollbackPreview, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return RollbackPreview{}, err
	}
	if !CanManage(actor.Role) {
		return RollbackPreview{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) {
		return RollbackPreview{}, ErrNotFound
	}
	var status string
	var finishedAt *time.Time
	var spaceCreated bool
	err := tx.QueryRow(ctx, `
		SELECT status, finished_at, space_created FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND source <> 'space_clone'`, id).Scan(&status, &finishedAt, &spaceCreated)
	if err == pgx.ErrNoRows {
		return RollbackPreview{}, ErrNotFound
	}
	if err != nil {
		return RollbackPreview{}, err
	}
	created, projects, err := s.liveCreated(ctx, tx, id)
	if err != nil {
		return RollbackPreview{}, err
	}
	info := rollbackInfo(status, finishedAt, created+projects, time.Now())
	preview := RollbackPreview{Eligible: info.Eligible, Reason: info.Reason, Deadline: info.Deadline, CreatedCount: created, ProjectCount: projects,
		Modified: []RollbackModified{}, SpaceCreated: spaceCreated}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.import_items i
		  JOIN openfox.tasks t ON t.organization_id = i.organization_id AND t.id = i.task_id
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.job_id = $1::uuid AND i.status = 'CREATED'
		   AND t.deleted_at IS NULL AND t.version > i.task_version`, id).Scan(&preview.ModifiedTotal); err != nil {
		return RollbackPreview{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, sp.prefix, t.sequence, t.title, t.updated_at
		  FROM openfox.import_items i
		  JOIN openfox.tasks t ON t.organization_id = i.organization_id AND t.id = i.task_id
		  JOIN openfox.spaces sp ON sp.organization_id = t.organization_id AND sp.id = t.space_id
		 WHERE i.organization_id = openfox_private.current_organization_id() AND i.job_id = $1::uuid AND i.status = 'CREATED'
		   AND t.deleted_at IS NULL AND t.version > i.task_version
		 ORDER BY t.updated_at DESC, t.id LIMIT $2`, id, maxModifiedShown)
	if err != nil {
		return RollbackPreview{}, err
	}
	for rows.Next() {
		var m RollbackModified
		var prefix string
		var seq int64
		if err := rows.Scan(&m.TaskID, &prefix, &seq, &m.Title, &m.UpdatedAt); err != nil {
			rows.Close()
			return RollbackPreview{}, err
		}
		m.DisplayID = fmt.Sprintf("%s-%d", prefix, seq)
		preview.Modified = append(preview.Modified, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return RollbackPreview{}, err
	}
	// 清理估算：资源仅被本次创建的任务使用（回滚后无人使用）。
	if err := tx.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE r.kind = 'LABEL' AND r.cleaned_at IS NULL AND NOT EXISTS (
		       SELECT 1 FROM openfox.task_labels tl JOIN openfox.tasks t ON t.organization_id = tl.organization_id AND t.id = tl.task_id
		        WHERE tl.organization_id = r.organization_id AND tl.label_id = r.resource_id AND t.deleted_at IS NULL
		          AND NOT `+createdByJobSQL+`)),
		  count(*) FILTER (WHERE r.kind = 'STATUS' AND r.cleaned_at IS NULL AND NOT EXISTS (
		       SELECT 1 FROM openfox.tasks t
		        WHERE t.organization_id = r.organization_id AND t.status_id = r.resource_id AND t.deleted_at IS NULL
		          AND NOT `+createdByJobSQL+`)
		     AND NOT EXISTS (SELECT 1 FROM openfox.task_statuses ts
		        WHERE ts.organization_id = r.organization_id AND ts.id = r.resource_id AND ts.is_default)
		     AND NOT EXISTS (SELECT 1 FROM openfox.workflow_statuses ws
		        WHERE ws.organization_id = r.organization_id AND ws.status_id = r.resource_id AND (ws.is_default OR NOT EXISTS (
		             SELECT 1 FROM openfox.import_created_resources w
		              WHERE w.organization_id = ws.organization_id AND w.job_id = r.job_id AND w.kind = 'WORKFLOW_STATUS'
		                AND w.resource_id = ws.status_id AND w.task_type_id = ws.task_type_id))))
		  FROM openfox.import_created_resources r
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid`, id).
		Scan(&preview.Cleanup.Labels, &preview.Cleanup.Statuses); err != nil {
		return RollbackPreview{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.import_created_resources r
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'ITERATION'
		   AND r.cleaned_at IS NULL AND `+removableIterationSQL, id).Scan(&preview.Cleanup.Iterations); err != nil {
		return RollbackPreview{}, err
	}
	return preview, nil
}

// Rollback 在一个组织事务内回滚：软删除本次创建的任务、释放映射、清理本次新建且已无人使用的工作流状态 / 标签 / 状态。
func (s Service) Rollback(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, version int64) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || version < 1 {
		return Job{}, ErrNotFound
	}
	// 事务级标记：回滚改任务 / 状态不触发自动化。
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', $1, true)`, id); err != nil {
		return Job{}, err
	}
	var status string
	var finishedAt *time.Time
	var jobVersion int64
	var spaceID, operationID string
	err := tx.QueryRow(ctx, `
		SELECT status, finished_at, version, space_id::text, operation_id FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND source <> 'space_clone' FOR UPDATE`, id).
		Scan(&status, &finishedAt, &jobVersion, &spaceID, &operationID)
	if err == pgx.ErrNoRows {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if jobVersion != version {
		return Job{}, ErrVersionConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.import_sync_links SET state='DISCONNECTED',version=version+1,lease=NULL WHERE organization_id=openfox_private.current_organization_id() AND job_id=$1::uuid AND state<>'DISCONNECTED'`, id); err != nil {
		return Job{}, err
	}
	created, projects, err := s.liveCreated(ctx, tx, id)
	if err != nil {
		return Job{}, err
	}
	if info := rollbackInfo(status, finishedAt, created+projects, time.Now()); !info.Eligible {
		return Job{}, withDetail(ErrRollbackUnavailable, "%s", info.Reason)
	}
	var running bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.runs r JOIN openfox.tasks t ON t.organization_id = r.organization_id AND t.id = r.task_id
		  WHERE r.organization_id = openfox_private.current_organization_id() AND r.state NOT IN ('SUCCEEDED', 'FAILED', 'CANCELED')
		    AND `+createdByJobSQL+`)`, id).Scan(&running); err != nil {
		return Job{}, err
	}
	if running {
		return Job{}, ErrRunActive
	}
	// 3. 不属于本次创建、但父任务是本次创建任务的未删除任务：解除父子关系。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks c SET parent_task_id = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.deleted_at IS NULL AND c.parent_task_id IS NOT NULL
		   AND EXISTS (SELECT 1 FROM openfox.import_mappings m
		                WHERE m.organization_id = c.organization_id AND m.created_job_id = $1::uuid AND NOT m.sync_created AND m.task_id = c.parent_task_id
		                  AND m.released_at IS NULL)
		   AND NOT EXISTS (SELECT 1 FROM openfox.import_mappings m
		                    WHERE m.organization_id = c.organization_id AND m.created_job_id = $1::uuid AND NOT m.sync_created AND m.task_id = c.id
		                      AND m.released_at IS NULL)`, id); err != nil {
		return Job{}, err
	}
	// 4. 软删除 + 动态 task.deleted（source_id 用 operation_id，同任务唯一）。
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, safe_diff, source_id)
		SELECT t.organization_id, t.space_id, t.id, $2::uuid, 'task.deleted', '{"reason":"import_rollback"}'::jsonb, $3 || ':' || t.id::text
		  FROM openfox.tasks t
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.deleted_at IS NULL AND `+createdByJobSQL,
		id, actor.UserID, operationID); err != nil {
		return Job{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks t SET deleted_at = clock_timestamp(), deleted_by = $2::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.deleted_at IS NULL AND `+createdByJobSQL,
		id, actor.UserID); err != nil {
		return Job{}, err
	}
	// 5. 释放映射。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_mappings SET released_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND created_job_id = $1::uuid AND NOT sync_created AND released_at IS NULL`, id); err != nil {
		return Job{}, err
	}
	// 6. 清理资源。
	if err := s.cleanupResources(ctx, tx, id); err != nil {
		return Job{}, err
	}
	if err := cleanupFileColumns(ctx, tx, id); err != nil {
		return Job{}, err
	}
	// 7. 状态。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs SET status = 'ROLLED_BACK', rolled_back_at = clock_timestamp(), rolled_back_by = $2::uuid,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id, actor.UserID); err != nil {
		return Job{}, err
	}
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "import.rolled_back", spaceID, id, operationID,
		map[string]string{"result": fmt.Sprintf("created_count=%d", created)}); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, organizationID, actor, id)
}

// cleanupResources 按顺序清理：WORKFLOW_STATUS（该类型下已无未删除任务在用且不是默认）→ LABEL（无人使用则归档）→ STATUS（无人使用、非默认、无工作流引用则归档）。
func (s Service) cleanupResources(ctx context.Context, tx pgx.Tx, jobID string) error {
	if err := s.cleanupIterations(ctx, tx, jobID); err != nil {
		return err
	}
	// 受影响类型的版本前进，使持有旧版本的工作流编辑冲突。
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.task_types tt SET version = version + 1
		 WHERE tt.organization_id = openfox_private.current_organization_id()
		   AND EXISTS (SELECT 1 FROM openfox.import_created_resources r
		                WHERE r.organization_id = tt.organization_id AND r.job_id = $1::uuid AND r.kind = 'WORKFLOW_STATUS'
		                  AND r.cleaned_at IS NULL AND r.task_type_id = tt.id)`, jobID); err != nil {
		return err
	}
	// 先删能删的工作流行（资源 = 状态 ID + 任务类型），并给对应资源记 cleaned_at。
	if _, err := tx.Exec(ctx, `
		WITH removable AS (
			SELECT r.resource_id AS rid, ws.status_id, ws.task_type_id
			  FROM openfox.import_created_resources r
			  JOIN openfox.workflow_statuses ws ON ws.organization_id = r.organization_id AND ws.task_type_id = r.task_type_id
			       AND ws.status_id = r.resource_id
			 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid
			   AND r.kind = 'WORKFLOW_STATUS' AND r.cleaned_at IS NULL AND NOT ws.is_default
			   AND NOT EXISTS (SELECT 1 FROM openfox.tasks t
			        WHERE t.organization_id = r.organization_id AND t.type_id = ws.task_type_id AND t.status_id = ws.status_id
			          AND t.deleted_at IS NULL)
		), deleted AS (
			DELETE FROM openfox.workflow_statuses ws USING removable x
			 WHERE ws.organization_id = openfox_private.current_organization_id()
			   AND ws.task_type_id = x.task_type_id AND ws.status_id = x.status_id
			RETURNING ws.status_id
		)
		UPDATE openfox.import_created_resources r SET cleaned_at = clock_timestamp()
		  FROM removable x
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'WORKFLOW_STATUS'
		   AND r.resource_id = x.rid AND r.task_type_id = x.task_type_id`, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.labels l SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		  FROM openfox.import_created_resources r
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'LABEL'
		   AND r.cleaned_at IS NULL AND l.organization_id = r.organization_id AND l.id = r.resource_id AND l.archived_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM openfox.task_labels tl JOIN openfox.tasks t ON t.organization_id = tl.organization_id AND t.id = tl.task_id
		        WHERE tl.organization_id = l.organization_id AND tl.label_id = l.id AND t.deleted_at IS NULL)`, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_created_resources r SET cleaned_at = clock_timestamp()
		  FROM openfox.labels l
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'LABEL'
		   AND r.cleaned_at IS NULL AND l.organization_id = r.organization_id AND l.id = r.resource_id AND l.archived_at IS NOT NULL`, jobID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.task_statuses ts SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		  FROM openfox.import_created_resources r
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'STATUS'
		   AND r.cleaned_at IS NULL AND ts.organization_id = r.organization_id AND ts.id = r.resource_id
		   AND ts.archived_at IS NULL AND NOT ts.is_default
		   AND NOT EXISTS (SELECT 1 FROM openfox.tasks t WHERE t.organization_id = ts.organization_id AND t.status_id = ts.id AND t.deleted_at IS NULL)
		   AND NOT EXISTS (SELECT 1 FROM openfox.workflow_statuses ws WHERE ws.organization_id = ts.organization_id AND ws.status_id = ts.id)`, jobID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.import_created_resources r SET cleaned_at = clock_timestamp()
		  FROM openfox.task_statuses ts
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'STATUS'
		   AND r.cleaned_at IS NULL AND ts.organization_id = r.organization_id AND ts.id = r.resource_id AND ts.archived_at IS NOT NULL`, jobID)
	return err
}

// cleanupIterations 删除本次新建、且已没有未删除任务使用的迭代（回滚已软删除导入的任务）：
// 先清除已软删除任务上的引用，再删迭代链接与迭代，顺序与 DeleteIteration 的外键一致。
func (s Service) cleanupIterations(ctx context.Context, tx pgx.Tx, jobID string) error {
	rows, err := tx.Query(ctx, `
		SELECT r.resource_id::text FROM openfox.import_created_resources r
		 WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'ITERATION'
		   AND r.cleaned_at IS NULL AND EXISTS (SELECT 1 FROM openfox.iterations i
		        WHERE i.organization_id = r.organization_id AND i.id = r.resource_id)
		   AND NOT EXISTS (SELECT 1 FROM openfox.tasks t
		        WHERE t.organization_id = r.organization_id AND t.iteration_id = r.resource_id AND t.deleted_at IS NULL)`, jobID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.tasks SET iteration_id = NULL
			 WHERE organization_id = openfox_private.current_organization_id() AND iteration_id = ANY($1::uuid[])`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.iteration_links
			 WHERE organization_id = openfox_private.current_organization_id() AND iteration_id = ANY($1::uuid[])`, ids); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.iterations
			 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, ids); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.import_created_resources SET cleaned_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND kind = 'ITERATION'
		   AND cleaned_at IS NULL AND resource_id = ANY($2::uuid[])`, jobID, ids)
	return err
}
