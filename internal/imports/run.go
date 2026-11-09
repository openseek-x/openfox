package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
)

// ---------- 警告累计 ----------

const maxWarningKinds = 50

type warnAcc struct {
	list []Warning
	idx  map[string]int
}

func newWarnAcc(existing []Warning) *warnAcc {
	a := &warnAcc{idx: map[string]int{}}
	for _, w := range existing {
		a.idx[w.Code] = len(a.list)
		a.list = append(a.list, w)
	}
	return a
}

// add 同 code 合并累计 count；最多 50 种。
func (a *warnAcc) add(code, message string) {
	if i, ok := a.idx[code]; ok {
		a.list[i].Count++
		return
	}
	if len(a.list) >= maxWarningKinds {
		return
	}
	message, _ = TruncateRunes(message, 300)
	a.idx[code] = len(a.list)
	a.list = append(a.list, Warning{Code: code, Message: message, Count: 1})
}

// ---------- 认领与批次循环 ----------

type claimed struct {
	ID string
	// Source 是任务来源；space_clone 走克隆流程。
	Source string
	// Draft 表示认领的是拉取中的 API 草稿（走拉取流程），否则是排队 / 执行中的导入。
	Draft bool
}

// claim 认领一个可执行的 job（QUEUED / RUNNING / CANCEL_REQUESTED，租约空 / 过期 / 属于本实例，retry_at 已到）。
func (w *Worker) claim(ctx context.Context, organizationID string) (claimed, bool, error) {
	var c claimed
	var found bool
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var attempts int
		var status, phase string
		err := tx.QueryRow(ctx, `
			WITH picked AS (
				SELECT id, lease_owner AS prev_owner FROM openfox.import_jobs
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND (status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED', 'CLEANING') OR (status = 'DRAFT' AND phase = 'fetching'))
				   AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp() OR lease_owner = $1)
				   AND (retry_at IS NULL OR retry_at <= clock_timestamp())
				 ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED
			)
			UPDATE openfox.import_jobs j
			   SET lease_owner = $1, lease_expires_at = clock_timestamp() + make_interval(secs => $2),
			       attempts = j.attempts + CASE WHEN p.prev_owner <> $1 THEN 1 ELSE 0 END,
			       status = CASE WHEN j.status = 'QUEUED' THEN 'RUNNING' ELSE j.status END,
			       phase = CASE WHEN j.status = 'QUEUED' THEN 'validating' ELSE j.phase END,
			       retry_at = NULL, updated_at = clock_timestamp()
			  FROM picked p
			 WHERE j.organization_id = openfox_private.current_organization_id() AND j.id = p.id
			RETURNING j.id::text, j.attempts, j.status, j.phase, j.source`, w.instance(), leaseDuration.Seconds()).Scan(&c.ID, &attempts, &status, &phase, &c.Source)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		c.Draft = status == StatusDraft
		limit := maxAttempts
		if c.Source == SourceSpaceClone {
			limit = cloneMaxAttempts
		}
		if attempts > limit {
			found = false
			if c.Draft {
				return discardFetch(ctx, tx, c.ID, "TOO_MANY_ATTEMPTS", "拉取重试次数过多，已停止")
			}
			if c.Source == SourceSpaceClone {
				return failClone(ctx, tx, c.ID, status, "TOO_MANY_ATTEMPTS", "克隆多次重试后仍然失败，已停止并清理。")
			}
			return failJob(ctx, tx, c.ID, "TOO_MANY_ATTEMPTS", "导入重试次数过多，已停止")
		}
		return nil
	})
	return c, found, err
}

func (w *Worker) executeImports(ctx context.Context, organizationID string) error {
	job, ok, err := w.claim(ctx, organizationID)
	if err != nil || !ok {
		return err
	}
	if job.Draft {
		return w.fetchPass(ctx, organizationID, job.ID)
	}
	if job.Source == SourceSpaceClone {
		return w.executeClone(ctx, organizationID, job.ID)
	}
	maxBatches := w.MaxBatches
	if maxBatches <= 0 {
		maxBatches = maxBatchesPass
	}
	for i := 0; i < maxBatches; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stop := false
		err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			stop, err = w.processBatch(ctx, tx, organizationID, job.ID)
			return err
		})
		if err != nil {
			// 批次整体失败（已回滚）：记录错误并退避，累计尝试次数，超限后在 claim 里置为 FAILED。
			w.logger().Warn("import_batch_failed", "organization_id", organizationID, "import_id", job.ID, "error", err.Error())
			_ = w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
				_, e := tx.Exec(ctx, `
					UPDATE openfox.import_jobs
					   SET attempts = attempts + 1, lease_owner = '', lease_expires_at = NULL, error_message = left($2, 1000),
					       retry_at = clock_timestamp() + make_interval(secs => LEAST(60, power(2, LEAST(attempts, 6))::int)),
					       updated_at = clock_timestamp()
					 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
					   AND status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED')`, job.ID, err.Error())
				return e
			})
			return err
		}
		if stop {
			return nil
		}
	}
	return nil
}

// failJob 把 job 置为 FAILED 并清租约。
func failJob(ctx context.Context, tx pgx.Tx, jobID, code, message string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = 'FAILED', error_code = $2, error_message = left($3, 1000), finished_at = clock_timestamp(), phase = '',
		       lease_owner = '', lease_expires_at = NULL, credentials_sealed = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, code, message)
	return err
}

type runJob struct {
	Source                                                         string
	ID, SpaceID, Status, Phase, OperationID, StartedBy, LeaseOwner string
	SpaceCreated                                                   bool
	Warnings                                                       []Warning
	Cfg                                                            Config
	Preview                                                        previewDoc
}

type batchCtx struct {
	orgID    string
	job      runJob
	actor    collaboration.Actor
	collab   collaboration.Service
	plan     plan
	settings settings
	resolved Resolved
	warns    *warnAcc
	delta    struct{ created, updated, skipped, failed int }
}

type settings struct {
	location       *time.Location
	priorityLevels int
}

func loadSettings(ctx context.Context, tx pgx.Tx, spaceID string) (settings, error) {
	var tz string
	var st settings
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(s.iteration_timezone, ''), 'Asia/Shanghai'), o.priority_levels
		  FROM openfox.spaces s JOIN openfox.organizations o ON o.id = s.organization_id
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $1::uuid`, spaceID).Scan(&tz, &st.priorityLevels); err != nil {
		return st, err
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc, _ = time.LoadLocation("Asia/Shanghai")
	}
	st.location = loc
	return st, nil
}

// cancelFinish 把 CANCEL_REQUESTED 收尾为 CANCELLED。
func cancelFinish(ctx context.Context, tx pgx.Tx, organizationID string, j runJob) error {
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = 'CANCELLED', finished_at = clock_timestamp(), phase = '', lease_owner = '', lease_expires_at = NULL,
		       credentials_sealed = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, j.ID); err != nil {
		return err
	}
	return recordAudit(ctx, tx, organizationID, j.StartedBy, 1, "import.cancelled", j.SpaceID, j.ID, j.OperationID,
		map[string]string{"status": StatusCancelRequested})
}

// processBatch 处理一批（一个事务）。返回 stop=true 表示本轮应停止（已结束、被取消或租约丢失）。
func (w *Worker) processBatch(ctx context.Context, tx pgx.Tx, organizationID, jobID string) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', $1, true)`, jobID); err != nil {
		return true, err
	}
	var j runJob
	var warnings, config, preview []byte
	var startedBy *string
	err := tx.QueryRow(ctx, `
		SELECT id::text, space_id::text, status, phase, operation_id, started_by::text, lease_owner, space_created, warnings, config, preview, source
		  FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, jobID).
		Scan(&j.ID, &j.SpaceID, &j.Status, &j.Phase, &j.OperationID, &startedBy, &j.LeaseOwner, &j.SpaceCreated, &warnings, &config, &preview, &j.Source)
	if err != nil {
		return true, err
	}
	// 租约 CAS：不属于本实例（已被他人接手）则放弃。
	if j.LeaseOwner != w.instance() {
		return true, nil
	}
	if j.Status != StatusRunning && j.Status != StatusCancelRequested {
		return true, nil
	}
	if startedBy != nil {
		j.StartedBy = *startedBy
	}
	if j.Status == StatusCancelRequested {
		return true, cancelFinish(ctx, tx, organizationID, j)
	}
	_ = json.Unmarshal(warnings, &j.Warnings)
	if err := json.Unmarshal(config, &j.Cfg); err != nil {
		return true, err
	}
	j.Cfg.normalize()
	if err := json.Unmarshal(preview, &j.Preview); err != nil {
		return true, err
	}

	// 发起人必须仍是活跃的所有者 / 管理员。
	var role, memberStatus string
	var authVersion int64
	err = tx.QueryRow(ctx, `
		SELECT role, status, authorization_version FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, j.StartedBy).Scan(&role, &memberStatus, &authVersion)
	actorRole := policy.OrganizationRole(strings.ToLower(role))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (memberStatus != "ACTIVE" || !CanManage(actorRole))) {
		return true, failJob(ctx, tx, j.ID, "IMPORTER_FORBIDDEN", "发起导入的成员已不是组织所有者或管理员")
	}
	if err != nil {
		return true, err
	}
	var spaceOK bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id()
		  AND id = $1::uuid AND deleted_at IS NULL AND retired_at IS NULL)`, j.SpaceID).Scan(&spaceOK); err != nil {
		return true, err
	}
	if !spaceOK {
		return true, failJob(ctx, tx, j.ID, "SPACE_UNAVAILABLE", "目标空间已删除或已退役")
	}

	b := &batchCtx{orgID: organizationID, job: j, warns: newWarnAcc(j.Warnings),
		actor: collaboration.Actor{UserID: j.StartedBy, Role: actorRole, AuthorizationVersion: uint64(authVersion)}}
	b.plan = buildPlan(j.Source, headersOf(j.Preview), j.Cfg.Columns)
	if b.settings, err = loadSettings(ctx, tx, j.SpaceID); err != nil {
		return true, err
	}
	if j.Preview.Resolved != nil {
		b.resolved = *j.Preview.Resolved
	}

	switch j.Phase {
	case "validating", "":
		if err := w.validate(ctx, tx, b); err != nil {
			return true, err
		}
		return false, w.saveBatch(ctx, tx, b, "importing", false)
	case "importing":
		done, err := w.importBatch(ctx, tx, b)
		if err != nil {
			return true, err
		}
		phase := "importing"
		if done {
			phase = "linking"
		}
		return false, w.saveBatch(ctx, tx, b, phase, false)
	case "linking":
		done, err := w.linkBatch(ctx, tx, b)
		if err != nil {
			return true, err
		}
		if err := w.saveBatch(ctx, tx, b, "linking", done); err != nil {
			return true, err
		}
		return done, nil
	default:
		return true, failJob(ctx, tx, j.ID, "INVALID_PHASE", "导入处于未知阶段")
	}
}

// saveBatch 写计数、警告、阶段并续租约；finish 为 true 时收尾为 SUCCEEDED。
func (w *Worker) saveBatch(ctx context.Context, tx pgx.Tx, b *batchCtx, phase string, finish bool) error {
	warningsJSON, _ := json.Marshal(b.warns.list)
	if b.warns.list == nil {
		warningsJSON = []byte("[]")
	}
	if finish {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET status = 'SUCCEEDED', finished_at = clock_timestamp(), phase = '', lease_owner = '', lease_expires_at = NULL,
			       credentials_sealed = NULL, created_count = created_count + $2, updated_count = updated_count + $3,
			       skipped_count = skipped_count + $4, failed_count = failed_count + $5, warnings = $6::jsonb,
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			b.job.ID, b.delta.created, b.delta.updated, b.delta.skipped, b.delta.failed, warningsJSON)
		return err
	}
	previewJSON, _ := json.Marshal(b.job.Preview)
	_, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET phase = $2, created_count = created_count + $3, updated_count = updated_count + $4,
		       skipped_count = skipped_count + $5, failed_count = failed_count + $6, warnings = $7::jsonb, preview = $8::jsonb,
		       lease_expires_at = clock_timestamp() + make_interval(secs => $9), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		b.job.ID, phase, b.delta.created, b.delta.updated, b.delta.skipped, b.delta.failed, warningsJSON, previewJSON, leaseDuration.Seconds())
	return err
}

// itemErrorText 把写入错误转成中文条目错误（截断 1000 字）。
func itemErrorText(err error) string {
	var rule *collaboration.RuleError
	msg := ""
	switch {
	case errors.As(err, &rule):
		msg = "写入失败：" + rule.Message
	case errors.Is(err, collaboration.ErrInvalidWorkflow):
		msg = "写入失败：状态不在任务类型的工作流内"
	case errors.Is(err, collaboration.ErrInvalidTask):
		msg = "写入失败：任务内容不合法"
	default:
		msg = "写入失败：" + err.Error()
	}
	msg, _ = TruncateRunes(msg, 1000)
	return msg
}

func fmtSprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// failClone 终止克隆任务：清理阶段失败 → CLEANUP_FAILED；其它阶段 → 转入清理（CLEANING），由下一轮清理已创建的数据。
// message 必须是可以直接给用户看的文案（原始错误只写日志）。
func failClone(ctx context.Context, tx pgx.Tx, jobID, status, code, message string) error {
	if status == StatusCleaning {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET status = 'CLEANUP_FAILED', error_code = 'CLEANUP_FAILED', error_message = $2, finished_at = clock_timestamp(), phase = '',
			       lease_owner = '', lease_expires_at = NULL, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, "清理没有完成，请联系管理员并提供操作编号。")
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = 'CLEANING', phase = 'cleaning', error_code = $2, error_message = $3, attempts = 0, lease_owner = '', lease_expires_at = NULL,
		       preview = preview || jsonb_build_object('cause', COALESCE(NULLIF(preview->>'cause', ''), 'failed')),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, code, message)
	return err
}
