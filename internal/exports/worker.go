package exports

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/artifacts"
	"openfox/internal/collaboration"
	"openfox/internal/imports"
	"openfox/internal/policy"
	"openfox/internal/spacebundle"
	"openfox/internal/tenancy"
)

// Runner 与 imports.Runner 相同：控制面扫描 + 租户事务。
type Runner = imports.Runner

const (
	pollInterval  = 2 * time.Second
	maxBackoff    = 30 * time.Second
	scanLimit     = 100
	leaseDuration = 10 * time.Minute // 生成在一个读事务里完成，租约要覆盖最大规模的导出
	maxAttempts   = 3
	cleanupBatch  = 50
)

// 对用户可见的错误文案（原始错误只写日志）。
const (
	msgTooLarge  = "导出文件超过大小上限，请缩小范围（减少空间或任务数量）后重试。"
	msgNoSpaces  = "所选空间对你都不可读，没有可导出的内容。"
	msgForbidden = "你已不再是该组织的成员，无法完成导出。"
	msgInternal  = "导出过程中出现内部错误，已停止。请稍后重试或联系管理员并提供操作编号。"
	msgStorage   = "文件存储未配置，无法生成导出文件。"
)

// Worker 周期性扫描有导出工作的组织：执行排队中的导出、清理过期文件。
type Worker struct {
	Pool     Runner
	Logger   *slog.Logger
	Instance string
	Storage  artifacts.Storage
	// Interval 为轮询间隔（默认 2 秒）。
	Interval time.Duration
	// TempDir 是生成过程中临时文件的目录（默认系统临时目录）。
	TempDir string
	// Now 便于测试（默认 time.Now）。
	Now func() time.Time

	cursor string
}

func (w *Worker) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) instance() string {
	if w.Instance == "" {
		w.Instance = imports.NewInstanceID()
	}
	return w.Instance
}

// Run 循环直到 ctx 取消；整轮失败时等待时间逐次翻倍（上限 30 秒）。
func (w *Worker) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = pollInterval
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		err := w.RunOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			failures++
			wait := interval
			for i := 1; i < failures && wait < maxBackoff; i++ {
				wait *= 2
			}
			w.logger().Warn("export_pass_failed", "error", err.Error(), "consecutive_failures", failures)
			timer.Reset(min(wait, maxBackoff))
			continue
		}
		failures = 0
		timer.Reset(interval)
	}
}

// RunOnce 执行一轮（测试的单轮入口）。
func (w *Worker) RunOnce(ctx context.Context) error {
	if w.Pool == nil {
		return errors.New("exports: nil worker pool")
	}
	var ids []string
	err := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var after any
		if w.cursor != "" {
			after = w.cursor
		}
		rows, err := tx.Query(ctx, `SELECT organization_id::text FROM openfox_private.list_export_organizations($1::uuid, $2)`, after, scanLimit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := w.RunOrganization(ctx, id); err != nil {
			w.logger().Warn("export_organization_failed", "organization_id", id, "error", err.Error())
		}
		w.cursor = id
	}
	if len(ids) < scanLimit {
		w.cursor = ""
	}
	return nil
}

func (w *Worker) inOrganization(ctx context.Context, organizationID string, fn func(context.Context, pgx.Tx) error) error {
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: imports.WorkerActorID, AuthorizationVersion: 1}
	return w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceOrganizationWorker, staticSelector(selection), fn)
}

func staticSelector(selection tenancy.OrganizationSelection) tenancy.OrganizationSelector {
	return tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
}

// RunOrganization 先清理过期文件，再执行排队中的导出（每轮每个组织最多处理几个）。
func (w *Worker) RunOrganization(ctx context.Context, organizationID string) error {
	var first error
	if err := w.expire(ctx, organizationID); err != nil {
		w.logger().Warn("export_expire_failed", "organization_id", organizationID, "error", err.Error())
		first = err
	}
	for i := 0; i < 5; i++ {
		ran, err := w.runNext(ctx, organizationID)
		if err != nil && first == nil {
			first = err
		}
		if err != nil || !ran {
			break
		}
	}
	return first
}

// expire 把过期的导出置为 EXPIRED 并删除文件（先改状态再删文件：删除失败只会残留文件，不会让下载指向已删文件）。
func (w *Worker) expire(ctx context.Context, organizationID string) error {
	var keys []string
	if err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE openfox.export_jobs SET status = 'EXPIRED', updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id IN (
			       SELECT id FROM openfox.export_jobs
			        WHERE organization_id = openfox_private.current_organization_id() AND status = 'SUCCEEDED' AND expires_at <= clock_timestamp()
			        ORDER BY expires_at LIMIT $1 FOR UPDATE SKIP LOCKED)
			RETURNING file_key`, cleanupBatch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	if w.Storage == nil {
		return nil
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if err := w.Storage.Delete(ctx, key); err != nil {
			w.logger().Warn("export_file_delete_failed", "organization_id", organizationID, "error", err.Error())
		}
	}
	return nil
}

type claimedJob struct {
	id, format, operationID, createdBy string
	spaceIDs                           []string
	attempts                           int
	role                               string
	authVersion                        uint64
	active                             bool
}

// claim 认领一个可执行的导出（排队中，或租约已过期的运行中），写入租约并返回发起人当前的角色与授权版本。
func (w *Worker) claim(ctx context.Context, organizationID string) (*claimedJob, error) {
	var job *claimedJob
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var c claimedJob
		err := tx.QueryRow(ctx, `
			SELECT id::text, format, operation_id, created_by::text, space_ids::text[], attempts
			  FROM openfox.export_jobs
			 WHERE organization_id = openfox_private.current_organization_id() AND status IN ('QUEUED', 'RUNNING')
			   AND (lease_expires_at IS NULL OR lease_expires_at <= clock_timestamp())
			   AND (retry_at IS NULL OR retry_at <= clock_timestamp())
			 ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED`).
			Scan(&c.id, &c.format, &c.operationID, &c.createdBy, &c.spaceIDs, &c.attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.export_jobs
			   SET status = 'RUNNING', started_at = COALESCE(started_at, clock_timestamp()), lease_owner = $2,
			       lease_expires_at = clock_timestamp() + make_interval(secs => $3), retry_at = NULL,
			       updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			c.id, w.instance(), leaseDuration.Seconds()); err != nil {
			return err
		}
		var status string
		var version int64
		err = tx.QueryRow(ctx, `
			SELECT role, status, authorization_version FROM openfox.organization_members
			 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, c.createdBy).
			Scan(&c.role, &status, &version)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		c.active = err == nil && status == "ACTIVE" && version > 0
		c.authVersion = uint64(max(version, 0))
		job = &c
		return nil
	})
	return job, err
}

// runNext 认领并执行一个导出；没有可执行的导出时 ran 为 false。
func (w *Worker) runNext(ctx context.Context, organizationID string) (ran bool, err error) {
	job, err := w.claim(ctx, organizationID)
	if err != nil || job == nil {
		return false, err
	}
	if w.Storage == nil {
		return true, w.fail(ctx, organizationID, job, "STORAGE_UNAVAILABLE", msgStorage)
	}
	if !job.active {
		return true, w.fail(ctx, organizationID, job, "FORBIDDEN", msgForbidden)
	}
	result, genErr := w.generate(ctx, organizationID, job)
	switch {
	case genErr == nil:
		return true, w.succeed(ctx, organizationID, job, result)
	case ctx.Err() != nil:
		return true, ctx.Err() // 服务停止：租约到期后由其他实例继续
	case errors.Is(genErr, artifacts.ErrTooLarge) || errors.Is(genErr, spacebundle.ErrTooLarge):
		return true, w.fail(ctx, organizationID, job, "EXPORT_TOO_LARGE", msgTooLarge)
	case errors.Is(genErr, ErrNoSpaces):
		return true, w.fail(ctx, organizationID, job, "NO_READABLE_SPACES", msgNoSpaces)
	case errors.Is(genErr, ErrTooMany):
		return true, w.fail(ctx, organizationID, job, "EXPORT_TOO_LARGE", msgTooLarge)
	}
	w.logger().Error("export_generate_failed", "organization_id", organizationID, "export_id", job.id, "attempt", job.attempts+1, "error", genErr.Error())
	if job.attempts+1 >= maxAttempts {
		return true, w.fail(ctx, organizationID, job, "EXPORT_FAILED", msgInternal)
	}
	return true, w.retry(ctx, organizationID, job)
}

// ErrNoSpaces 表示所选空间发起人都读不到。
var ErrNoSpaces = errors.New("exports: no readable spaces")

type generated struct {
	key, fileName, contentType string
	size                       int64
	spaces, tasks              int
}

// generate 在发起人身份的租户事务里读取空间并生成文件，写入存储。
func (w *Worker) generate(ctx context.Context, organizationID string, job *claimedJob) (generated, error) {
	role := policy.OrganizationRole(strings.ToLower(job.role))
	actor := collaboration.Actor{UserID: job.createdBy, Role: role, AuthorizationVersion: job.authVersion}
	selection := tenancy.OrganizationSelection{OrganizationID: organizationID, ActorID: job.createdBy, AuthorizationVersion: job.authVersion}
	var out generated
	err := w.Pool.RunForOrganization(ctx, organizationID, tenancy.SourceUserRequest, staticSelector(selection),
		func(ctx context.Context, tx pgx.Tx) error {
			var err error
			out, err = w.build(ctx, tx, organizationID, actor, job)
			return err
		})
	return out, err
}

func (w *Worker) build(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, job *claimedJob) (generated, error) {
	svc := Service{}
	if err := svc.checkActor(ctx, tx, organizationID, actor); err != nil {
		return generated{}, err
	}
	// 按发起人「执行时」的读权限过滤：读不到的空间不出现在文件里。
	items, err := svc.readable(ctx, tx, organizationID, actor)
	if err != nil {
		return generated{}, err
	}
	readable := map[string]collaboration.SpaceDetail{}
	for _, item := range items {
		readable[item.ID] = item
	}
	var spaces []collaboration.SpaceDetail
	total := 0
	for _, id := range job.spaceIDs {
		if item, ok := readable[id]; ok {
			spaces = append(spaces, item)
			total += item.TaskCount
		}
	}
	if len(spaces) == 0 {
		return generated{}, ErrNoSpaces
	}
	if len(spaces) > MaxSpaces || total > MaxTasks {
		return generated{}, &TooManyError{Spaces: len(spaces), Tasks: total}
	}
	var orgName string
	if err := tx.QueryRow(ctx, `SELECT name FROM openfox.organizations WHERE id = openfox_private.current_organization_id()`).Scan(&orgName); err != nil {
		return generated{}, err
	}

	final, err := os.CreateTemp(w.TempDir, "openfox-export-*")
	if err != nil {
		return generated{}, err
	}
	defer os.Remove(final.Name())
	defer final.Close()
	limited := &limitWriter{w: final, max: artifacts.MaxArtifactBytes}

	day := w.now().UTC().Format("20060102")
	multi := len(spaces) > 1
	var zw *zip.Writer
	if multi {
		zw = zip.NewWriter(limited)
	}
	tasks := 0
	for _, sp := range spaces {
		n, err := w.writeSpace(ctx, tx, spacebundle.Org{ID: organizationID, Name: orgName}, sp, job.format, zw, limited)
		if err != nil {
			return generated{}, err
		}
		tasks += n
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			return generated{}, err
		}
	}
	out := generated{spaces: len(spaces), tasks: tasks}
	switch {
	case multi:
		out.fileName, out.contentType = fmt.Sprintf("openfox-tasks-%s-%s.zip", job.format, day), "application/zip"
	case job.format == FormatCSV:
		out.fileName, out.contentType = fmt.Sprintf("%s-tasks-%s.csv", spaces[0].Prefix, day), "text/csv; charset=utf-8"
	default:
		out.fileName, out.contentType = fmt.Sprintf("%s-snapshot-%s.jsonl.gz", spaces[0].Prefix, day), "application/gzip"
	}
	if _, err := final.Seek(0, io.SeekStart); err != nil {
		return generated{}, err
	}
	out.key = fmt.Sprintf("export/%s/%s.bin", organizationID, job.id)
	// 重试时上一次可能已写入同名对象（键不可覆盖），先删除。
	_ = w.Storage.Delete(context.WithoutCancel(ctx), out.key)
	stored, err := w.Storage.Put(ctx, out.key, final, artifacts.MaxArtifactBytes)
	if err != nil {
		return generated{}, err
	}
	out.size = stored.Size
	return out, nil
}

// writeSpace 把一个空间导出为快照包（临时文件），再按格式写入输出：
// 单空间直接写文件本身，多空间写成 zip 的一个条目。返回任务数。
func (w *Worker) writeSpace(ctx context.Context, tx pgx.Tx, org spacebundle.Org, sp collaboration.SpaceDetail, format string, zw *zip.Writer, direct io.Writer) (int, error) {
	bundle, err := os.CreateTemp(w.TempDir, "openfox-bundle-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(bundle.Name())
	defer bundle.Close()
	sw := spacebundle.NewWriter(bundle)
	opts := spacebundle.ExportOptions{MaxTasks: MaxTasks, Extras: true}
	if err := spacebundle.ExportWith(ctx, tx, org, sp.ID, sw, opts); err != nil {
		return 0, err
	}
	if err := sw.Close(); err != nil {
		return 0, err
	}
	open := func() (io.ReadCloser, error) { return os.Open(bundle.Name()) }
	tasks := sw.Counts()[spacebundle.KindTask]

	dst := direct
	if zw != nil {
		name := sp.Prefix + ".csv"
		method := zip.Deflate
		if format == FormatJSON {
			name, method = sp.Prefix+".jsonl.gz", zip.Store // 已经是 gzip，不再压缩
		}
		entry, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: w.now().UTC()})
		if err != nil {
			return 0, err
		}
		dst = entry
	}
	if format == FormatJSON {
		src, err := open()
		if err != nil {
			return 0, err
		}
		defer src.Close()
		if _, err := io.Copy(dst, src); err != nil {
			return 0, err
		}
		return tasks, nil
	}
	if _, err := WriteCSV(dst, open); err != nil {
		return 0, err
	}
	return tasks, nil
}

// limitWriter 在写入超过 max 字节时返回 artifacts.ErrTooLarge，避免为必然放不进存储的文件继续生成。
type limitWriter struct {
	w      io.Writer
	max, n int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n+int64(len(p)) > l.max {
		return 0, artifacts.ErrTooLarge
	}
	n, err := l.w.Write(p)
	l.n += int64(n)
	return n, err
}

func (w *Worker) succeed(ctx context.Context, organizationID string, job *claimedJob, g generated) error {
	expires := w.now().Add(Retention)
	err := w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.export_jobs
			   SET status = 'SUCCEEDED', space_count = $2, task_count = $3, file_key = $4, file_name = $5, content_type = $6,
			       size_bytes = $7, finished_at = clock_timestamp(), expires_at = $8, lease_owner = '', lease_expires_at = NULL,
			       error_code = '', error_message = '', updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'RUNNING' AND lease_owner = $9`,
			job.id, g.spaces, g.tasks, g.key, g.fileName, g.contentType, g.size, expires, w.instance())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("exports: lease lost")
		}
		return recordAudit(ctx, tx, organizationID, job.createdBy, job.authVersion, "task.export.completed", job.id, job.operationID,
			fmt.Sprintf("spaces=%d;format=%s;tasks=%d", g.spaces, job.format, g.tasks))
	})
	if err != nil {
		_ = w.Storage.Delete(context.WithoutCancel(ctx), g.key)
	}
	return err
}

func (w *Worker) fail(ctx context.Context, organizationID string, job *claimedJob, code, message string) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.export_jobs
			   SET status = 'FAILED', error_code = $2, error_message = $3, finished_at = clock_timestamp(), lease_owner = '',
			       lease_expires_at = NULL, updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status IN ('QUEUED', 'RUNNING')`,
			job.id, code, message); err != nil {
			return err
		}
		return recordAudit(ctx, tx, organizationID, job.createdBy, max(job.authVersion, 1), "task.export.failed", job.id, job.operationID, "error="+code)
	})
}

func (w *Worker) retry(ctx context.Context, organizationID string, job *claimedJob) error {
	return w.inOrganization(ctx, organizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE openfox.export_jobs
			   SET attempts = attempts + 1, lease_owner = '', lease_expires_at = NULL,
			       retry_at = clock_timestamp() + make_interval(secs => LEAST(60, power(2, LEAST(attempts, 5))::int)),
			       updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND status = 'RUNNING'`, job.id)
		return err
	})
}
