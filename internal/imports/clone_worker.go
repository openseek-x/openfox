package imports

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/spacebundle"
	"openfox/internal/tenancy"
)

// 克隆任务的后台执行。阶段（import_jobs.phase）：
//
//	validating     读快照包头、确认来源一致
//	creating_space 建空间并解析组织级配置（状态 / 类型 / 标签 / 字段 / 专项 / 关联类型 / 人员），一个事务
//	importing      分批创建任务（SAVEPOINT 隔离单条失败）
//	linking        分批设置父子关系（parents）与任务关联（relations）
//	comments       分批写评论（comments）与只读动态历史（activities），最后收尾为 SUCCEEDED
//	cleaning       失败 / 取消后的清理（status = CLEANING）
//
// 所有写入都在目标组织的租户事务内，并带 openfox.import_job 标记（抑制自动化、通知、Issue 同步入队）；
// 源组织只在开始阶段另开一个短事务重新校验发起人的所有者 / 管理员身份。

const (
	// cloneMaxAttempts 是同一任务连续失败（瞬时错误）的重试上限，超过后转入清理；成功的批次会把计数清零。
	cloneMaxAttempts   = 5
	cloneTaskBatch     = 100
	cloneLinkBatch     = 200
	cloneCommentBatch  = 200
	cloneActivityBatch = 500
)

// cloneInfo 是 worker 在批次之前读取的任务概要。
type cloneInfo struct {
	status, phase, startedBy, sourceOrg string
	sourceChecked                       bool
}

func (w *Worker) bundleOpener(key string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return w.Storage.Open(context.Background(), key) }
}

// executeClone 是克隆任务的批次循环（与 executeImports 对应）。
func (w *Worker) executeClone(ctx context.Context, organizationID, jobID string) error {
	maxBatches := w.MaxBatches
	if maxBatches <= 0 {
		maxBatches = maxBatchesPass
	}
	for i := 0; i < maxBatches; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var info cloneInfo
		if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			var doc []byte
			var startedBy *string
			if err := tx.QueryRow(ctx, `
				SELECT status, phase, started_by::text, source_organization_id::text, preview
				  FROM openfox.import_jobs
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID).
				Scan(&info.status, &info.phase, &startedBy, &info.sourceOrg, &doc); err != nil {
				return err
			}
			if startedBy != nil {
				info.startedBy = *startedBy
			}
			var d cloneDoc
			_ = json.Unmarshal(doc, &d)
			info.sourceChecked = d.State.SourceChecked
			return nil
		}); err != nil {
			return err
		}
		// 需要时在源组织另开一个事务重新校验发起人身份（不和目标组织事务混用）。
		verdict := ""
		checked := false
		if info.status == StatusRunning && !info.sourceChecked && info.startedBy != "" {
			checked = true
			ok, err := w.sourceStillAllowed(ctx, info.sourceOrg, info.startedBy)
			if err != nil {
				return err
			}
			if !ok {
				verdict = "SOURCE_FORBIDDEN"
			}
		}
		stop := false
		var deleteKey string
		err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			stop, deleteKey, err = w.cloneBatch(ctx, tx, organizationID, jobID, checked, verdict)
			return err
		})
		if err != nil {
			// 原始错误只写日志；任务上只保存可读文案，前端不会看到内部错误。
			if !isTransientCloneError(err) {
				w.logger().Error("clone_batch_failed_permanently", "organization_id", organizationID, "import_id", jobID, "error", err.Error())
				if perr := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
					var status string
					if err := tx.QueryRow(ctx, `
						SELECT status FROM openfox.import_jobs
						 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, jobID).Scan(&status); err != nil {
						return err
					}
					switch status {
					case StatusQueued, StatusRunning, StatusCancelRequested, StatusCleaning:
						return failClone(ctx, tx, jobID, status, "CLONE_FAILED", "克隆过程中出现内部错误，已停止并清理。请联系管理员并提供操作编号。")
					}
					return nil
				}); perr != nil {
					return perr
				}
				continue
			}
			w.logger().Warn("clone_batch_failed", "organization_id", organizationID, "import_id", jobID, "error", err.Error())
			_ = w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `
					UPDATE openfox.import_jobs
					   SET attempts = attempts + 1, lease_owner = '', lease_expires_at = NULL,
					       error_message = '克隆暂时失败，正在重试。',
					       retry_at = clock_timestamp() + make_interval(secs => LEAST(60, power(2, LEAST(attempts, 6))::int)),
					       updated_at = clock_timestamp()
					 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
					   AND status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED', 'CLEANING')`, jobID)
				return e
			})
			return err
		}
		if deleteKey != "" && w.Storage != nil {
			if err := w.Storage.Delete(ctx, deleteKey); err != nil {
				w.logger().Warn("clone_bundle_delete_failed", "import_id", jobID, "error", err.Error())
			}
		}
		if stop {
			return nil
		}
	}
	return nil
}

// sourceStillAllowed 在源组织的租户事务里确认发起人仍是活跃的所有者 / 管理员。
func (w *Worker) sourceStillAllowed(ctx context.Context, sourceOrg, userID string) (bool, error) {
	allowed := false
	selection := tenancy.OrganizationSelection{OrganizationID: sourceOrg, ActorID: WorkerActorID, AuthorizationVersion: 1}
	err := w.Pool.RunForOrganization(ctx, sourceOrg, tenancy.SourceOrganizationWorker,
		tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) { return selection, nil }),
		func(ctx context.Context, tx pgx.Tx) error {
			var role, status string
			err := tx.QueryRow(ctx, `
				SELECT role, status FROM openfox.organization_members
				 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, userID).Scan(&role, &status)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			allowed = status == "ACTIVE" && CanManage(policy.OrganizationRole(strings.ToLower(role)))
			return nil
		})
	return allowed, err
}

type cloneJobRow struct {
	ID, Status, Phase, StartedBy, LeaseOwner, SpaceID, BundleKey, SourceOrg, SourceSpace, OperationID string
	Warnings                                                                                          []Warning
	Doc                                                                                               cloneDoc
}

// cloneBatch 处理一批（一个事务）。stop=true 表示本轮应停止；deleteKey 非空表示事务提交后应删除快照包。
func (w *Worker) cloneBatch(ctx context.Context, tx pgx.Tx, organizationID, jobID string, sourceChecked bool, sourceVerdict string) (stop bool, deleteKey string, err error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', $1, true)`, jobID); err != nil {
		return true, "", err
	}
	var j cloneJobRow
	var warnings, preview []byte
	var startedBy, spaceID *string
	err = tx.QueryRow(ctx, `
		SELECT id::text, status, phase, started_by::text, lease_owner, space_id::text, bundle_key,
		       source_organization_id::text, source_space_id::text, operation_id, warnings, preview
		  FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, jobID).
		Scan(&j.ID, &j.Status, &j.Phase, &startedBy, &j.LeaseOwner, &spaceID, &j.BundleKey, &j.SourceOrg, &j.SourceSpace, &j.OperationID, &warnings, &preview)
	if err != nil {
		return true, "", err
	}
	if j.LeaseOwner != w.instance() {
		return true, "", nil
	}
	if startedBy != nil {
		j.StartedBy = *startedBy
	}
	if spaceID != nil {
		j.SpaceID = *spaceID
	}
	_ = json.Unmarshal(warnings, &j.Warnings)
	if err := json.Unmarshal(preview, &j.Doc); err != nil {
		return true, "", err
	}
	switch j.Status {
	case StatusRunning, StatusCancelRequested, StatusCleaning:
	default:
		return true, "", nil
	}

	// 发起人必须仍是目标组织活跃的所有者 / 管理员。
	var role, memberStatus string
	var authVersion int64
	merr := tx.QueryRow(ctx, `
		SELECT role, status, authorization_version FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, j.StartedBy).Scan(&role, &memberStatus, &authVersion)
	actorRole := policy.OrganizationRole(strings.ToLower(role))
	forbidden := errors.Is(merr, pgx.ErrNoRows) || (merr == nil && (memberStatus != "ACTIVE" || !CanManage(actorRole)))
	if merr != nil && !errors.Is(merr, pgx.ErrNoRows) {
		return true, "", merr
	}
	b := &cloneBatchCtx{orgID: organizationID, job: j, warns: newWarnAcc(j.Warnings),
		actor: collaboration.Actor{UserID: j.StartedBy, Role: actorRole, AuthorizationVersion: uint64(authVersion)}}

	if j.Status == StatusCleaning {
		return w.cloneCleanup(ctx, tx, b)
	}
	if j.Status == StatusCancelRequested {
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "cancelled", "CANCELLED", "克隆已取消")
	}
	if forbidden {
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", "IMPORTER_FORBIDDEN", "发起克隆的成员已不是目标组织所有者或管理员")
	}
	if sourceChecked && sourceVerdict != "" {
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", sourceVerdict, "发起克隆的成员已不是源组织所有者或管理员")
	}
	if w.Storage == nil {
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", "STORAGE_UNAVAILABLE", "服务器没有配置文件存储，无法读取快照包")
	}
	if sourceChecked {
		b.job.Doc.State.SourceChecked = true
	}

	step := func() (bool, error) {
		switch j.Phase {
		case "validating", "":
			return false, w.cloneValidate(ctx, b)
		case "creating_space":
			return false, w.cloneCreateSpace(ctx, tx, b)
		case "importing":
			return false, w.cloneImportTasks(ctx, tx, b)
		case "linking":
			return false, w.cloneLink(ctx, tx, b)
		case "comments":
			return w.cloneComments(ctx, tx, b)
		default:
			return false, errBadBundle("克隆处于未知阶段")
		}
	}
	finished, err := step()
	var bad *badBundleError
	switch {
	case errors.As(err, &bad):
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", bad.code, bad.msg)
	case errors.Is(err, spacebundle.ErrUnsupportedVersion), errors.Is(err, spacebundle.ErrInvalid),
		errors.Is(err, spacebundle.ErrTruncated), errors.Is(err, spacebundle.ErrTooLarge):
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", "BUNDLE_INVALID", "快照包已损坏或版本不受支持")
	case errors.Is(err, os.ErrNotExist):
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", "BUNDLE_MISSING", "快照包不存在")
	case errors.Is(err, collaboration.ErrIdentifierTaken):
		return false, "", w.cloneBeginCleanup(ctx, tx, b, "failed", "IDENTIFIER_TAKEN", "目标组织已有相同标识符的空间")
	case err != nil:
		return true, "", err
	}
	if finished {
		return true, j.BundleKey, w.cloneSave(ctx, tx, b, "", true)
	}
	return false, "", w.cloneSave(ctx, tx, b, b.nextPhase, false)
}

type cloneBatchCtx struct {
	orgID string
	job   cloneJobRow
	actor collaboration.Actor
	warns *warnAcc
	delta struct{ created, skipped, failed int }
	// nextPhase 是本批结束后的阶段（空串表示不变）。
	nextPhase string
	// spaceCreatedID 是本批新建的空间。
	spaceCreatedID string
}

type badBundleError struct{ code, msg string }

func (e *badBundleError) Error() string { return e.code + ": " + e.msg }

func errBadBundle(msg string) error { return &badBundleError{code: "BUNDLE_INVALID", msg: msg} }

// cloneSave 写阶段、计数、警告与进度（preview.state）；finish 为 true 时收尾为 SUCCEEDED 并清空快照包引用。
func (w *Worker) cloneSave(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, phase string, finish bool) error {
	warningsJSON, _ := json.Marshal(b.warns.list)
	if b.warns.list == nil {
		warningsJSON = []byte("[]")
	}
	doc, err := json.Marshal(b.job.Doc)
	if err != nil {
		return err
	}
	if finish {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET status = 'SUCCEEDED', finished_at = clock_timestamp(), phase = '', lease_owner = '', lease_expires_at = NULL,
			       created_count = created_count + $2, skipped_count = skipped_count + $3, failed_count = failed_count + $4,
			       warnings = $5::jsonb, preview = $6::jsonb, bundle_key = '', version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			b.job.ID, b.delta.created, b.delta.skipped, b.delta.failed, warningsJSON, doc)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, b.orgID, b.actor.UserID, b.actor.AuthorizationVersion, "space.clone.completed", b.job.SpaceID, b.job.ID,
			b.job.OperationID, map[string]string{"source": SourceSpaceClone, "result": "succeeded", "space_id": b.job.SourceSpace})
	}
	if phase == "" {
		phase = b.job.Phase
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET phase = $2, attempts = 0, created_count = created_count + $3, skipped_count = skipped_count + $4, failed_count = failed_count + $5,
		       warnings = $6::jsonb, preview = $7::jsonb, lease_expires_at = clock_timestamp() + make_interval(secs => $8),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		b.job.ID, phase, b.delta.created, b.delta.skipped, b.delta.failed, warningsJSON, doc, leaseDuration.Seconds())
	return err
}

// cloneBeginCleanup 把任务转入清理（CLEANING）：记录原因与错误，之后的批次执行清理。
func (w *Worker) cloneBeginCleanup(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx, cause, code, message string) error {
	b.job.Doc.Cause = cause
	doc, err := json.Marshal(b.job.Doc)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = 'CLEANING', phase = 'cleaning', error_code = $2, error_message = left($3, 1000), preview = $4::jsonb,
		       lease_expires_at = clock_timestamp() + make_interval(secs => $5), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		b.job.ID, code, message, doc, leaseDuration.Seconds())
	return err
}

// cloneCleanup 清理本次克隆创建的数据（一个事务）：软删除任务与空间、释放来源映射、归档未被使用的组织级配置。
// 清理结束后：创建过东西 → CLEANED；什么都没创建 → 按原因 FAILED / CANCELLED。
func (w *Worker) cloneCleanup(ctx context.Context, tx pgx.Tx, b *cloneBatchCtx) (bool, string, error) {
	jobID := b.job.ID
	spaceID := b.job.SpaceID
	if spaceID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.tasks SET deleted_at = clock_timestamp(), deleted_by = $2::uuid, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND deleted_at IS NULL`,
			spaceID, b.actor.UserID); err != nil {
			return true, "", err
		}
		// 克隆写入的任务关联、工作流与字段挂载随空间一起清掉，这样未被使用的状态 / 字段 / 关联类型才能归档。
		for _, stmt := range []string{
			`DELETE FROM openfox.task_relations WHERE organization_id = openfox_private.current_organization_id() AND source_space_id = $1::uuid`,
			`DELETE FROM openfox.workflow_statuses WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`,
			`DELETE FROM openfox.task_type_fields WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid`,
		} {
			if _, err := tx.Exec(ctx, stmt, spaceID); err != nil {
				return true, "", err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.spaces
			   SET deleted_at = clock_timestamp(), deleted_by = $2::uuid, status = 'ARCHIVED', version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`,
			spaceID, b.actor.UserID); err != nil {
			return true, "", err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_mappings SET released_at = clock_timestamp(), updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND created_job_id = $1::uuid AND released_at IS NULL`, jobID); err != nil {
		return true, "", err
	}
	if err := (Service{}).cleanupResources(ctx, tx, jobID); err != nil {
		return true, "", err
	}
	if err := cloneCleanupConfig(ctx, tx, jobID); err != nil {
		return true, "", err
	}
	status := StatusCleaned
	if !b.job.Doc.State.Created && spaceID == "" {
		status = StatusFailed
		if b.job.Doc.Cause == "cancelled" {
			status = StatusCancelled
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = $2, finished_at = clock_timestamp(), phase = '', lease_owner = '', lease_expires_at = NULL, bundle_key = '',
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, status); err != nil {
		return true, "", err
	}
	if err := recordAudit(ctx, tx, b.orgID, b.actor.UserID, b.actor.AuthorizationVersion, "space.clone.cleaned", spaceID, jobID,
		b.job.OperationID, map[string]string{"source": SourceSpaceClone, "result": strings.ToLower(status), "space_id": b.job.SourceSpace}); err != nil {
		return true, "", err
	}
	return true, b.job.BundleKey, nil
}

// cloneCleanupConfig 清理克隆新建且已无人使用的自定义字段、专项与关联类型。
func cloneCleanupConfig(ctx context.Context, tx pgx.Tx, jobID string) error {
	stmts := []string{
		// 自定义字段：没有未删除任务的取值、也没有挂在其它空间任务类型上 → 归档。
		`UPDATE openfox.custom_fields f SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		   FROM openfox.import_created_resources r
		  WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'CUSTOM_FIELD'
		    AND r.cleaned_at IS NULL AND f.organization_id = r.organization_id AND f.id = r.resource_id AND f.archived_at IS NULL
		    AND NOT EXISTS (SELECT 1 FROM openfox.task_field_values v JOIN openfox.tasks t ON t.organization_id = v.organization_id AND t.id = v.task_id
		                     WHERE v.organization_id = f.organization_id AND v.field_id = f.id AND t.deleted_at IS NULL)
		    AND NOT EXISTS (SELECT 1 FROM openfox.task_type_fields tf WHERE tf.organization_id = f.organization_id AND tf.field_id = f.id)`,
		// 专项：没有未删除任务引用 → 标记为已取消（专项没有归档字段）。
		`UPDATE openfox.projects p SET status = 'canceled', version = version + 1, updated_at = clock_timestamp()
		   FROM openfox.import_created_resources r
		  WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'PROJECT'
		    AND r.cleaned_at IS NULL AND p.organization_id = r.organization_id AND p.id = r.resource_id
		    AND NOT EXISTS (SELECT 1 FROM openfox.tasks t WHERE t.organization_id = p.organization_id AND t.project_id = p.id AND t.deleted_at IS NULL)`,
		// 关联类型：没有任何关联在用 → 删除。
		`DELETE FROM openfox.task_relation_types rt USING openfox.import_created_resources r
		  WHERE r.organization_id = openfox_private.current_organization_id() AND r.job_id = $1::uuid AND r.kind = 'RELATION_TYPE'
		    AND r.cleaned_at IS NULL AND rt.organization_id = r.organization_id AND rt.id = r.resource_id
		    AND NOT EXISTS (SELECT 1 FROM openfox.task_relations x WHERE x.organization_id = rt.organization_id AND x.relation_type_id = rt.id)`,
		`UPDATE openfox.import_created_resources SET cleaned_at = clock_timestamp()
		  WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid
		    AND kind IN ('CUSTOM_FIELD', 'PROJECT', 'RELATION_TYPE') AND cleaned_at IS NULL`,
	}
	for _, stmt := range stmts {
		if _, err := tx.Exec(ctx, stmt, jobID); err != nil {
			return err
		}
	}
	return nil
}

// isTransientCloneError 判断错误是否值得重试：超时 / 取消、网络与连接错误、序列化失败 / 死锁、锁等待超时、资源不足。
// 其它错误（查询没有结果、约束冲突、数据不一致等）重试不会改变结果，直接转入清理。
func isTransientCloneError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case strings.HasPrefix(pgErr.Code, "08"), strings.HasPrefix(pgErr.Code, "53"), strings.HasPrefix(pgErr.Code, "57"):
			return true
		case pgErr.Code == "40001", pgErr.Code == "40P01", pgErr.Code == "55P03":
			return true
		}
	}
	return false
}
