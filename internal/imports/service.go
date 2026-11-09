package imports

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/invitations"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// detailError 给哨兵错误附带一段中文说明（handler 用作 message）。
type detailError struct {
	base error
	msg  string
}

func (e detailError) Error() string { return e.base.Error() + ": " + e.msg }
func (e detailError) Unwrap() error { return e.base }

// ErrInvalidRequestWith 返回带中文说明的 ErrInvalidRequest（handler 用作 400 的 message）。
func ErrInvalidRequestWith(msg string) error { return detailError{base: ErrInvalidRequest, msg: msg} }

func withDetail(base error, format string, args ...any) error {
	return detailError{base: base, msg: fmt.Sprintf(format, args...)}
}

// Detail 取出错误里的说明文字，没有时返回空串。
func Detail(err error) string {
	var d detailError
	if errors.As(err, &d) {
		return d.msg
	}
	return ""
}

// CanManage 只有组织所有者 / 管理员可以上传、配置、开始、取消与回滚导入。
func CanManage(role policy.OrganizationRole) bool {
	return role == policy.OrganizationRoleOwner || role == policy.OrganizationRoleAdmin
}

// Service 不持有状态；tx 必须是已设置租户上下文的组织事务。
type Service struct{}

// RollbackInfo 对应契约 ImportJob.rollback。
type RollbackInfo struct {
	Eligible bool       `json:"eligible"`
	Reason   string     `json:"reason"`
	Deadline *time.Time `json:"deadline"`
}

type InvitationResult struct {
	SourceID string `json:"source_id"`
	Email    string `json:"email"`
	Result   string `json:"result"`
}

type Failure struct {
	Ordinal     int    `json:"ordinal"`
	ExternalKey string `json:"external_key"`
	Error       string `json:"error"`
}

// Job 对应契约 ImportJob。
type Job struct {
	SyncTicket     string             `json:"sync_ticket,omitempty"`
	Sync           *ProjectSync       `json:"sync,omitempty"`
	ID             string             `json:"id"`
	SpaceID        *string            `json:"space_id"`
	SpaceName      string             `json:"space_name"`
	NewSpace       *NewSpace          `json:"new_space"`
	SpaceCreated   bool               `json:"space_created"`
	Fetch          *FetchInfo         `json:"fetch"`
	API            *JobAPI            `json:"api"`
	Source         string             `json:"source"`
	SourceLabel    string             `json:"source_label"`
	FileName       string             `json:"file_name"`
	Status         string             `json:"status"`
	Phase          string             `json:"phase"`
	OperationID    string             `json:"operation_id"`
	TotalCount     int                `json:"total_count"`
	ProcessedCount int                `json:"processed_count"`
	CreatedCount   int                `json:"created_count"`
	UpdatedCount   int                `json:"updated_count"`
	SkippedCount   int                `json:"skipped_count"`
	FailedCount    int                `json:"failed_count"`
	Warnings       []Warning          `json:"warnings"`
	ErrorCode      string             `json:"error_code"`
	ErrorMessage   string             `json:"error_message"`
	CreatedBy      string             `json:"created_by"`
	CreatedByEmail string             `json:"created_by_email"`
	CreatedAt      time.Time          `json:"created_at"`
	StartedAt      *time.Time         `json:"started_at"`
	FinishedAt     *time.Time         `json:"finished_at"`
	RolledBackAt   *time.Time         `json:"rolled_back_at"`
	RetryAt        *time.Time         `json:"retry_at"`
	Version        int64              `json:"version"`
	ConfigVersion  int64              `json:"config_version"`
	Rollback       RollbackInfo       `json:"rollback"`
	Preview        *Preview           `json:"preview,omitempty"`
	Config         *Config            `json:"config,omitempty"`
	Analysis       *Analysis          `json:"analysis,omitempty"`
	Invitations    []InvitationResult `json:"invitations,omitempty"`
	Failures       []Failure          `json:"failures,omitempty"`
}

// Space 返回目标空间 ID；选择新建空间且尚未开始导入时为空串。
func (j Job) Space() string {
	if j.SpaceID == nil {
		return ""
	}
	return *j.SpaceID
}

// previewDoc 是 import_jobs.preview 的完整内容：契约 ImportPreview 加服务端附加键。
func ImportSiteOrigin(site, project string) ProjectOrigin {
	sum := sha256.Sum256([]byte(site + "\x00" + project))
	return ProjectOrigin{Project: project, Instance: fmt.Sprintf("%x", sum)}
}

type ProjectOrigin struct {
	Project    string `json:"project"`
	Connection string `json:"connection"`
	Instance   string `json:"instance"`
}
type previewDoc struct {
	Origin ProjectOrigin `json:"project_origin,omitempty"`
	Preview
	Analysis    *Analysis          `json:"analysis,omitempty"`
	Invitations []InvitationResult `json:"invitations,omitempty"`
	Resolved    *Resolved          `json:"resolved,omitempty"`
	// 6c-2：API 导入的连接参数与拉取进度；新建空间的参数。
	API      *APIInfo   `json:"api,omitempty"`
	Fetch    *FetchInfo `json:"fetch,omitempty"`
	NewSpace *NewSpace  `json:"new_space,omitempty"`
}

// Resolved 是 validating 阶段解析出的映射表，之后各批直接读取。
type Resolved struct {
	Epics         map[string]fileEpic     `json:"epics,omitempty"`
	Statuses      map[string]string       `json:"statuses"`        // 小写来源状态 → 状态 ID
	Labels        map[string]string       `json:"labels"`          // 小写标签名 → 标签 ID
	Types         map[string]string       `json:"types"`           // 小写来源类型 → 类型 ID
	MigratedLabel string                  `json:"migrated_label"`  // 「Migrated」标签 ID
	Users         map[string]ResolvedUser `json:"users"`           // 来源用户标识 → 处理方式
	Actor         string                  `json:"actor,omitempty"` // 保留
	// KeepPrefix 是保留票号时采用的前缀（大写）；Iterations 是 Sprint ID → 迭代 ID。
	KeepPrefix string            `json:"keep_prefix,omitempty"`
	Iterations map[string]string `json:"iterations,omitempty"`
}

// ResolvedUser 是一个来源用户的落地方式：成员 ID 或外部账号 ID（都空表示不导入）。
type ResolvedUser struct {
	UserID            string `json:"user_id,omitempty"`
	ExternalAccountID string `json:"external_account_id,omitempty"`
	// Name 是来源平台上的姓名，评论作者未映射到成员时用于正文前缀。
	Name string `json:"name,omitempty"`
}

const (
	rollbackWindow  = 7 * 24 * time.Hour
	maxFailureShown = 20
	defaultPageSize = 5
	maxPageSize     = 50
)

func newOperationID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 12)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return "op_" + string(b)
}

func recordAudit(ctx context.Context, tx pgx.Tx, organizationID, actorID string, authVersion uint64, action, spaceID, jobID, operationID string, meta map[string]string) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	if meta == nil {
		meta = map[string]string{}
	}
	meta["resource_kind"] = "import_job"
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actorID, Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: "import_job", OrganizationID: organizationID, SpaceID: spaceID, ID: jobID},
		RequestID: operationID, AuthorizationVersion: authVersion, SafeMetadata: meta, CreatedAt: time.Now().UTC(),
	})
}

// ---------- 读取 ----------

// jobColumns 的 %s 位置放 preview / config 的取值表达式（列表接口不取大 jsonb）。
const jobColumnsTemplate = `
	j.id::text, COALESCE(j.space_id::text, ''), COALESCE(s.name, ''), j.space_created, j.source, j.source_label, j.file_name, j.status, j.phase, j.operation_id,
	j.total_count, j.created_count, j.updated_count, j.skipped_count, j.failed_count, j.warnings, j.error_code, j.error_message,
	j.created_by::text, COALESCE(u.email, ''), j.created_at, j.started_at, j.finished_at, j.rolled_back_at, j.retry_at,
	j.version, j.config_version,
	CASE WHEN j.status IN ('SUCCEEDED', 'FAILED', 'CANCELLED') THEN
	     (SELECT count(*) FROM openfox.import_mappings m
	       JOIN openfox.tasks t ON t.organization_id = m.organization_id AND t.id = m.task_id AND t.deleted_at IS NULL
	      WHERE m.organization_id = j.organization_id AND m.created_job_id = j.id AND NOT m.sync_created AND m.released_at IS NULL) + (` + liveImportProjectsSQL + `)
	     ELSE 0 END,
	%s, %s, CASE WHEN sl.job_id IS NULL THEN NULL ELSE jsonb_build_object('status_mappings',sl.status_mappings,'last_checked',sl.last_checked,'checking',(sl.state='ACTIVE' AND (sl.lease IS NOT NULL OR sl.next_check<=clock_timestamp())),'direction',sl.direction,'state',sl.state,'version',sl.version,'last_success',sl.last_success,'error_code',sl.error_code) END`

const jobFrom = `
	  FROM openfox.import_jobs j
 LEFT JOIN openfox.import_sync_links sl ON sl.organization_id=j.organization_id AND sl.job_id=j.id
	  LEFT JOIN openfox.spaces s ON s.organization_id = j.organization_id AND s.id = j.space_id
	  LEFT JOIN openfox.users u ON u.id = j.created_by`

func jobColumns(withBlobs bool) string {
	if withBlobs {
		return fmt.Sprintf(jobColumnsTemplate, "j.preview", "j.config")
	}
	// 列表不取大 jsonb，只带 API 导入的连接参数（preview.api）。
	return fmt.Sprintf(jobColumnsTemplate, "jsonb_build_object('api', j.preview->'api')", "'{}'::jsonb")
}

// jobAPIOf 从 preview 取出 API 导入的连接参数；文件导入返回 nil。
func jobAPIOf(preview []byte) *JobAPI {
	var doc struct {
		API *APIInfo `json:"api"`
	}
	if len(preview) == 0 || json.Unmarshal(preview, &doc) != nil || doc.API == nil {
		return nil
	}
	a := doc.API
	return &JobAPI{Deployment: a.Deployment, SiteURL: a.SiteURL, ProjectKey: a.ProjectKey, JQL: a.JQL, AuthKind: a.AuthKind, AuthUser: a.AuthUser}
}

type jobRaw struct {
	job     Job
	preview []byte
	config  []byte
	created int
}

func scanJob(row pgx.Row) (jobRaw, error) {
	var r jobRaw
	var warnings, syncJSON []byte
	j := &r.job
	var spaceID string
	err := row.Scan(&j.ID, &spaceID, &j.SpaceName, &j.SpaceCreated, &j.Source, &j.SourceLabel, &j.FileName, &j.Status, &j.Phase, &j.OperationID,
		&j.TotalCount, &j.CreatedCount, &j.UpdatedCount, &j.SkippedCount, &j.FailedCount, &warnings, &j.ErrorCode, &j.ErrorMessage,
		&j.CreatedBy, &j.CreatedByEmail, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.RolledBackAt, &j.RetryAt,
		&j.Version, &j.ConfigVersion, &r.created, &r.preview, &r.config, &syncJSON)
	if err != nil {
		return r, err
	}
	if len(syncJSON) > 0 {
		if err := json.Unmarshal(syncJSON, &j.Sync); err != nil {
			return r, err
		}
	}
	if spaceID != "" {
		j.SpaceID = &spaceID
	}
	j.API = jobAPIOf(r.preview)
	j.ProcessedCount = j.CreatedCount + j.UpdatedCount + j.SkippedCount + j.FailedCount
	j.Warnings = []Warning{}
	if len(warnings) > 0 {
		_ = json.Unmarshal(warnings, &j.Warnings)
		if j.Warnings == nil {
			j.Warnings = []Warning{}
		}
	}
	j.Rollback = rollbackInfo(j.Status, j.FinishedAt, r.created, time.Now())
	return r, nil
}

// rollbackInfo 判定可回滚性：状态、7 天窗口、是否创建过未删除的任务。
func rollbackInfo(status string, finishedAt *time.Time, liveCreated int, now time.Time) RollbackInfo {
	info := RollbackInfo{}
	if finishedAt != nil {
		d := finishedAt.Add(rollbackWindow)
		info.Deadline = &d
	}
	switch {
	case status == StatusRolledBack:
		info.Reason = "rolled_back"
	case status != StatusSucceeded && status != StatusFailed && status != StatusCancelled:
		info.Reason = "not_finished"
	case finishedAt == nil || !now.Before(finishedAt.Add(rollbackWindow)):
		info.Reason = "expired"
	case liveCreated == 0:
		info.Reason = "nothing_created"
	default:
		info.Eligible = true
	}
	return info
}

// visibility 返回非管理员追加的可见性条件；$1 永远是大写组织角色。
func visibility(actor collaboration.Actor) (string, error) {
	switch {
	case CanManage(actor.Role):
		// $1（角色）必须出现在语句里，否则 PostgreSQL 无法推断参数类型。
		return " AND $1::text IS NOT NULL", nil
	case actor.Role == policy.OrganizationRoleGuest:
		return "", ErrForbidden
	default:
		return " AND j.status NOT IN ('DRAFT', 'DISCARDED') AND " + collaboration.SpaceReadableSQL, nil
	}
}

func roleArg(actor collaboration.Actor) string { return strings.ToUpper(string(actor.Role)) }

// ListResult 对应 listImports 的响应。
type ListResult struct {
	Imports    []Job  `json:"imports"`
	NextCursor string `json:"next_cursor"`
	HasActive  bool   `json:"has_active"`
	CanManage  bool   `json:"can_manage"`
	Total      int    `json:"total"`
}

func encodeCursor(createdAt time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func decodeCursor(value string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", ErrInvalidRequest
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 || !membership.ValidateUUID(parts[1]) {
		return time.Time{}, "", ErrInvalidRequest
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", ErrInvalidRequest
	}
	return at, parts[1], nil
}

// List 返回导入记录（不含草稿与已丢弃），按 (created_at, id) 倒序。
func (s Service) List(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, limit int, cursor string) (ListResult, error) {
	return s.ListForConnection(ctx, tx, organizationID, actor, limit, cursor, "")
}

func (s Service) ListForConnection(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, limit int, cursor, connection string) (ListResult, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return ListResult{}, err
	}
	if _, err := visibility(actor); err != nil {
		return ListResult{}, err
	}
	if limit == 0 {
		limit = defaultPageSize
	}
	if limit < 0 || limit > maxPageSize {
		return ListResult{}, ErrInvalidRequest
	}
	where := ` WHERE j.organization_id = openfox_private.current_organization_id() AND j.status NOT IN ('DRAFT', 'DISCARDED') AND j.source <> 'space_clone'`
	if CanManage(actor.Role) {
		where += " AND $1::text IS NOT NULL"
	} else {
		where += " AND " + collaboration.SpaceReadableSQL
	}
	if connection != "" && !CanManage(actor.Role) {
		return ListResult{}, ErrForbidden
	}
	if connection != "" && !membership.ValidateUUID(connection) {
		return ListResult{}, ErrInvalidRequest
	}
	where += " AND ($2::text='' OR (sl.connection_id=NULLIF($2,'')::uuid AND sl.state<>'DISCONNECTED' AND j.status='SUCCEEDED'))"
	result := ListResult{Imports: []Job{}, CanManage: CanManage(actor.Role)}
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(bool_or(j.status IN ('QUEUED', 'RUNNING', 'CANCEL_REQUESTED')), false)`+jobFrom+where,
		roleArg(actor), connection).Scan(&result.Total, &result.HasActive); err != nil {
		return ListResult{}, err
	}
	args := []any{roleArg(actor), connection, limit + 1}
	query := `SELECT ` + jobColumns(false) + jobFrom + where
	if cursor != "" {
		at, id, err := decodeCursor(cursor)
		if err != nil {
			return ListResult{}, err
		}
		query += ` AND (j.created_at, j.id) < ($4::timestamptz, $5::uuid)`
		args = append(args, at, id)
	}
	rows, err := tx.Query(ctx, query+` ORDER BY j.created_at DESC, j.id DESC LIMIT $3`, args...)
	if err != nil {
		return ListResult{}, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanJob(rows)
		if err != nil {
			return ListResult{}, err
		}
		result.Imports = append(result.Imports, r.job)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	if len(result.Imports) > limit {
		result.Imports = result.Imports[:limit]
		last := result.Imports[limit-1]
		result.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	return result, nil
}

// Get 返回导入详情（含预览、配置、DRAFT 的分析、邀请结果与失败样例）。
func (s Service) Get(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	vis, err := visibility(actor)
	if err != nil {
		return Job{}, err
	}
	if !membership.ValidateUUID(id) {
		return Job{}, ErrNotFound
	}
	r, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns(true)+jobFrom+`
		 WHERE j.organization_id = openfox_private.current_organization_id() AND j.id = $2::uuid AND j.source <> 'space_clone'`+vis,
		roleArg(actor), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	return s.decorate(ctx, tx, r)
}

func (s Service) decorate(ctx context.Context, tx pgx.Tx, r jobRaw) (Job, error) {
	job := r.job
	var doc previewDoc
	if len(r.preview) > 0 {
		if err := json.Unmarshal(r.preview, &doc); err != nil {
			return Job{}, err
		}
	}
	if doc.Columns == nil {
		doc.Columns = []PreviewColumn{}
	}
	if doc.RowErrors == nil {
		doc.RowErrors = []RowError{}
	}
	preview := doc.Preview
	job.Preview = &preview
	job.NewSpace = doc.NewSpace
	job.Fetch = doc.Fetch
	if job.SpaceName == "" && doc.NewSpace != nil {
		job.SpaceName = doc.NewSpace.Name
	}
	var cfg Config
	if len(r.config) > 0 {
		if err := json.Unmarshal(r.config, &cfg); err != nil {
			return Job{}, err
		}
	}
	cfg.normalize()
	job.Config = &cfg
	if job.Status == StatusDraft {
		job.Analysis = doc.Analysis
	}
	job.Invitations = doc.Invitations
	if job.Failures == nil && job.FailedCount > 0 {
		rows, err := tx.Query(ctx, `
			SELECT ordinal, external_key, error FROM openfox.import_items
			 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND status = 'FAILED'
			 ORDER BY ordinal LIMIT $2`, job.ID, maxFailureShown)
		if err != nil {
			return Job{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var f Failure
			if err := rows.Scan(&f.Ordinal, &f.ExternalKey, &f.Error); err != nil {
				return Job{}, err
			}
			job.Failures = append(job.Failures, f)
		}
		if err := rows.Err(); err != nil {
			return Job{}, err
		}
	}
	return job, nil
}

func (c *Config) normalize() {
	if c.Columns == nil {
		c.Columns = []ColumnMapping{}
	}
	if c.Statuses == nil {
		c.Statuses = []StatusMapping{}
	}
	if c.Users == nil {
		c.Users = []UserMapping{}
	}
}

// lockedJob 是锁行后的核心字段。
type lockedJob struct {
	ID, SpaceID, Status, Phase, OperationID, Source string
	CreatedBy                                       string
	SpaceCreated                                    bool
	SourceLabel                                     string
	HasCredentials                                  bool
	Version, ConfigVersion                          int64
	Config                                          Config
	Preview                                         previewDoc
}

func lockJob(ctx context.Context, tx pgx.Tx, id string) (lockedJob, error) {
	var j lockedJob
	var config, preview []byte
	err := tx.QueryRow(ctx, `
		SELECT id::text, COALESCE(space_id::text, ''), status, phase, operation_id, source, created_by::text, space_created, source_label, credentials_sealed IS NOT NULL, version, config_version, config, preview
		  FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND source <> 'space_clone' FOR UPDATE`, id).
		Scan(&j.ID, &j.SpaceID, &j.Status, &j.Phase, &j.OperationID, &j.Source, &j.CreatedBy, &j.SpaceCreated, &j.SourceLabel, &j.HasCredentials, &j.Version, &j.ConfigVersion, &config, &preview)
	if errors.Is(err, pgx.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal(config, &j.Config); err != nil {
		return j, err
	}
	j.Config.normalize()
	if err := json.Unmarshal(preview, &j.Preview); err != nil {
		return j, err
	}
	return j, nil
}

// ---------- 上传 ----------

const (
	sampleCount   = 3
	sampleRunes   = 200
	maxRawRowSize = 1 << 20
	insertChunk   = 500
)

// Upload 创建 DRAFT 导入：写 job、COPY 全部原始行、预填建议映射并计算首次分析。
// 调用方（handler）已确认空间存在且当前成员可写。
func (s Service) Upload(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, spaceID, fileName string, file ParsedFile) (Job, error) {
	return s.upload(ctx, tx, organizationID, actor, spaceID, nil, fileName, file)
}

// UploadNewSpace 与 Upload 相同，但目标是「新建空间」：草稿没有空间，开始导入时才创建。
func (s Service) UploadNewSpace(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, newSpace NewSpace, fileName string, file ParsedFile) (Job, error) {
	return s.upload(ctx, tx, organizationID, actor, "", &newSpace, fileName, file)
}

func (s Service) upload(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, spaceID string, newSpace *NewSpace, fileName string, file ParsedFile) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if newSpace == nil {
		if !membership.ValidateUUID(spaceID) {
			return Job{}, ErrSpaceNotFound
		}
		var ok bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.spaces WHERE organization_id = openfox_private.current_organization_id()
			  AND id = $1::uuid AND deleted_at IS NULL AND retired_at IS NULL)`, spaceID).Scan(&ok); err != nil {
			return Job{}, err
		}
		if !ok {
			return Job{}, ErrSpaceNotFound
		}
	}
	name := path.Base(strings.ReplaceAll(fileName, "\\", "/"))
	if name == "." || name == "/" {
		name = ""
	}
	name, _ = TruncateRunes(name, 255)

	source := file.Source
	if source == "" {
		source = SourceJiraCSV
	}
	cfg := SuggestedConfig(file.Headers)
	if source == SourceJiraCSV {
		enabled := true
		cfg.Options.FileProjects = &enabled
	}
	if strings.HasSuffix(source, "_csv") || source == "tapd_excel" {
		suggestFileCustomFields(file.Headers, cfg.Columns)
	}
	preview := previewDoc{Preview: buildPreview(file, cfg), Origin: file.Origin}
	if newSpace != nil {
		// 没给前缀时取 Issue key 列首个非空值的前缀。
		fallback := ""
		for _, c := range cfg.Columns {
			if c.Field != FieldSourceKey {
				continue
			}
			for _, row := range file.Rows {
				if p, _, ok := ParseSourceKey(row.Cells[c.Index]); ok {
					fallback = p
					break
				}
			}
			break
		}
		normalized, err := NormalizeNewSpace(*newSpace, fallback)
		if err != nil {
			return Job{}, err
		}
		preview.NewSpace = &normalized
	}
	jobID, err := membership.NewID()
	if err != nil {
		return Job{}, err
	}
	operationID := newOperationID()

	// 原始行先序列化并校验单行体积；插入时分批 unnest（RLS 表不支持 COPY）。
	raws := make([]string, len(file.Rows))
	for i, row := range file.Rows {
		raw, err := json.Marshal(map[string]any{"cells": row.Cells, "line": row.Line})
		if err != nil {
			return Job{}, err
		}
		if len(raw) > maxRawRowSize {
			return Job{}, withDetail(ErrInvalidFile, "第 %d 行内容过大", row.Line)
		}
		raws[i] = string(raw)
	}
	// 先写 job（preview 稍后在分析后更新），再 COPY 条目。
	configJSON, _ := json.Marshal(cfg)
	previewJSON, _ := json.Marshal(preview)
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.import_jobs (organization_id, id, space_id, source, source_label, file_name, status, config, preview,
		                                 total_count, operation_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, NULLIF($2, '')::uuid, $9, $10, $3, 'DRAFT', $4::jsonb, $5::jsonb,
		        $6, $7, $8::uuid)`, jobID, spaceID, name, configJSON, previewJSON, len(file.Rows), operationID, actor.UserID, source, fileSourceName(source)); err != nil {
		return Job{}, err
	}
	for start := 0; start < len(raws); start += insertChunk {
		end := min(start+insertChunk, len(raws))
		ordinals := make([]int32, 0, end-start)
		ids := make([]string, 0, end-start)
		for i := start; i < end; i++ {
			ordinals = append(ordinals, int32(i))
			ids = append(ids, fmt.Sprintf("row-%d", i))
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.import_items (organization_id, job_id, ordinal, external_id, raw)
			SELECT openfox_private.current_organization_id(), $1::uuid, v.ordinal, v.eid, v.raw::jsonb
			  FROM unnest($2::int[], $3::text[], $4::text[]) AS v(ordinal, eid, raw)`,
			jobID, ordinals, ids, raws[start:end]); err != nil {
			return Job{}, err
		}
	}
	analysis, infos, err := computeAnalysis(ctx, tx, jobID, spaceID, file.Headers, cfg, false, source)
	if err != nil {
		return Job{}, err
	}
	applyAnalysisToPreview(&preview, analysis, infos)
	previewJSON, _ = json.Marshal(preview)
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs SET preview = $2::jsonb
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, previewJSON); err != nil {
		return Job{}, err
	}
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "import.uploaded", spaceID, jobID, operationID,
		map[string]string{"source": source}); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, organizationID, actor, jobID)
}

func buildPreview(file ParsedFile, cfg Config) Preview {
	suggested := make([]string, len(file.Headers))
	for _, c := range cfg.Columns {
		if c.Index >= 0 && c.Index < len(suggested) {
			suggested[c.Index] = c.Field
		}
	}
	columns := make([]PreviewColumn, len(file.Headers))
	for i, h := range file.Headers {
		col := PreviewColumn{Index: i, Header: h, Samples: []string{}, SuggestedField: suggested[i]}
		for _, row := range file.Rows {
			if len(col.Samples) >= sampleCount {
				break
			}
			if v := strings.TrimSpace(row.Cells[i]); v != "" {
				v, _ = TruncateRunes(v, sampleRunes)
				col.Samples = append(col.Samples, v)
			}
		}
		columns[i] = col
	}
	rowErrors := file.RowErrors
	if rowErrors == nil {
		rowErrors = []RowError{}
	}
	if len(rowErrors) > maxRowErrors {
		rowErrors = rowErrors[:maxRowErrors]
	}
	return Preview{Columns: columns, TotalRows: len(file.Rows), ImportableRows: len(file.Rows), SkippedRows: file.SkippedRows,
		RowErrors: rowErrors}
}

// applyAnalysisToPreview 用最新分析刷新预览里随列映射变化的统计，并保存分析。
func applyAnalysisToPreview(preview *previewDoc, analysis Analysis, infos []rowInfo) {
	invalid := 0
	var errs []RowError
	for _, info := range infos {
		if info.Invalid {
			invalid++
		}
	}
	preview.FailedRows = invalid
	preview.ImportableRows = len(infos) - invalid
	_ = errs
	preview.Analysis = &analysis
}

// computeAnalysis 读取全部原始行，按配置分类并分析。
func computeAnalysis(ctx context.Context, tx pgx.Tx, jobID, spaceID string, headers []string, cfg Config, api bool, source string) (Analysis, []rowInfo, error) {
	rows, err := loadItems(ctx, tx, jobID)
	if err != nil {
		return Analysis{}, nil, err
	}
	l, err := loadLookups(ctx, tx, spaceID)
	if err != nil {
		return Analysis{}, nil, err
	}
	if api {
		if l.jiraVerified, err = loadJiraVerified(ctx, tx, jobID); err != nil {
			return Analysis{}, nil, err
		}
	}
	if api || source == "zentao_api" || source == "tapd_api" || source == "pingcode_api" || source == "teambition_api" || source == "shortcut_api" {
		if err := tx.QueryRow(ctx, `SELECT coalesce(preview->'project_origin'->>'instance','') FROM openfox.import_jobs WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, jobID).Scan(&l.instance); err != nil {
			return Analysis{}, nil, err
		}
	}
	p := buildPlan(source, headers, cfg.Columns)
	infos := classify(p, rows, cfg.Options)
	a, err := analyze(ctx, tx, spaceID, cfg, infos, l, api, sourceSystem(source))
	a.Capabilities.ProjectSync = l.instance != ""
	return a, infos, err
}

func loadItems(ctx context.Context, tx pgx.Tx, jobID string) ([]rawItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT ordinal, raw FROM openfox.import_items
		 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid ORDER BY ordinal`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []rawItem
	for rows.Next() {
		var ordinal int
		var raw []byte
		if err := rows.Scan(&ordinal, &raw); err != nil {
			return nil, err
		}
		var doc struct {
			Cells  []string `json:"cells"`
			Line   int      `json:"line"`
			Record *Record  `json:"record"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, err
		}
		items = append(items, rawItem{Ordinal: ordinal, Line: doc.Line, Cells: doc.Cells, Record: doc.Record})
	}
	return items, rows.Err()
}

// ---------- 配置 ----------

var validFields = map[string]bool{
	FieldIgnore: true, FieldSourceID: true, FieldSourceKey: true, FieldTitle: true, FieldType: true, FieldStatus: true,
	FieldPriority: true, FieldAssignee: true, FieldReporter: true, FieldCreator: true, FieldDescription: true, FieldParent: true,
	FieldLabels: true, FieldComment: true, FieldCreatedAt: true, FieldUpdatedAt: true, FieldStartDate: true, FieldDueDate: true,
	FieldStoryPoints: true, FieldSourceURL: true, FieldRelations: true, FieldAttachment: true, FieldSprint: true,
	FieldResolution: true, FieldWatchers: true, FieldOriginalEstimate: true, FieldCustom: true, FieldProject: true,
}

var validCategories = map[string]bool{"BACKLOG": true, "TODO": true, "IN_PROGRESS": true, "DONE": true, "CANCELLED": true, "DUPLICATE": true}

func invalidConfig(format string, args ...any) error {
	return withDetail(ErrInvalidConfig, format, args...)
}

func (s Service) validateConfig(ctx context.Context, tx pgx.Tx, cfg *Config, columnCount int) error {
	cfg.normalize()
	if len(cfg.Columns) > MaxColumns || len(cfg.Statuses) > 500 || len(cfg.Users) > 2000 {
		return invalidConfig("映射数量超过上限")
	}
	customNames := map[string]bool{}
	seenCol := map[int]bool{}
	for _, c := range cfg.Columns {
		if c.Index < 0 || c.Index >= columnCount {
			return invalidConfig("列序号 %d 超出文件列数", c.Index)
		}
		if seenCol[c.Index] {
			return invalidConfig("列 %d 重复出现", c.Index)
		}
		seenCol[c.Index] = true
		if !validFields[c.Field] {
			return invalidConfig("未知的任务字段 %q", c.Field)
		}
		if c.Field == FieldCustom {
			name := strings.ToLower(strings.TrimSpace(c.CustomName))
			if name == "" || customNames[name] || utf8.RuneCountInString(name) > 64 {
				return invalidConfig("自定义字段名称为空、重复或超过 64 字")
			}
			customNames[name] = true
		}
		if utf8.RuneCountInString(c.CustomName) > 100 {
			return invalidConfig("自定义字段名过长")
		}
	}
	var statusIDs []string
	seenSource := map[string]bool{}
	for i := range cfg.Statuses {
		st := &cfg.Statuses[i]
		st.Source = strings.TrimSpace(st.Source)
		if st.Source == "" || utf8.RuneCountInString(st.Source) > 200 {
			return invalidConfig("来源状态名不合法")
		}
		k := strings.ToLower(st.Source)
		if seenSource[k] {
			return invalidConfig("来源状态「%s」重复", st.Source)
		}
		seenSource[k] = true
		st.NewName = strings.TrimSpace(st.NewName)
		hasNew := st.NewName != "" || st.NewCategory != ""
		switch {
		case st.StatusID != "" && hasNew:
			return invalidConfig("状态「%s」不能同时映射到已有状态和新建状态", st.Source)
		case st.StatusID != "":
			if !membership.ValidateUUID(st.StatusID) {
				return invalidConfig("状态 ID 不合法")
			}
			statusIDs = append(statusIDs, st.StatusID)
		case hasNew:
			if st.NewName == "" || st.NewCategory == "" {
				return invalidConfig("新建状态需要同时提供名称和类别")
			}
			if utf8.RuneCountInString(st.NewName) > 100 {
				return invalidConfig("新建状态名称过长")
			}
			if st.NewCategory == "TRIAGE" || !validCategories[st.NewCategory] {
				return invalidConfig("新建状态类别不合法")
			}
		default:
			return invalidConfig("状态「%s」没有指定目标", st.Source)
		}
	}
	if len(statusIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT count(DISTINCT id) FROM openfox.task_statuses
			 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[]) AND archived_at IS NULL`,
			statusIDs).Scan(&n); err != nil {
			return err
		}
		distinct := map[string]bool{}
		for _, id := range statusIDs {
			distinct[strings.ToLower(id)] = true
		}
		if n != len(distinct) {
			return invalidConfig("映射的状态不存在或已归档")
		}
	}
	var memberIDs []string
	seenUser := map[string]bool{}
	for i := range cfg.Users {
		u := &cfg.Users[i]
		if u.SourceID == "" || len(u.SourceID) > 255 {
			return invalidConfig("来源用户标识不合法")
		}
		if seenUser[u.SourceID] {
			return invalidConfig("来源用户 %q 重复", u.SourceID)
		}
		seenUser[u.SourceID] = true
		switch u.Mode {
		case "member":
			if !membership.ValidateUUID(u.UserID) {
				return invalidConfig("映射到成员需要 user_id")
			}
			memberIDs = append(memberIDs, strings.ToLower(u.UserID))
		case "invite":
			email := invitations.NormalizeEmail(u.Email)
			if email == "" || len(email) > 320 || !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\r\n") {
				return invalidConfig("邀请需要合法的邮箱")
			}
			u.Email = email
		case "external", "skip":
		default:
			return invalidConfig("未知的成员映射方式 %q", u.Mode)
		}
	}
	if len(memberIDs) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.organization_members
			 WHERE organization_id = openfox_private.current_organization_id() AND user_id = ANY($1::uuid[]) AND status = 'ACTIVE'`,
			uniqueStrings(memberIDs)).Scan(&n); err != nil {
			return err
		}
		if n != len(uniqueStrings(memberIDs)) {
			return invalidConfig("映射的成员不存在或已停用")
		}
	}
	return nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func headersOf(p previewDoc) []string {
	headers := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		headers[i] = c.Header
	}
	return headers
}

// PutConfig 保存草稿配置（锁行 + config_version CAS），重算分析并返回最新草稿。
func (s Service) PutConfig(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, configVersion int64, cfg Config) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || configVersion < 1 {
		return Job{}, ErrNotFound
	}
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return Job{}, err
	}
	if j.Status != StatusDraft || j.Phase == "fetching" {
		return Job{}, ErrImportState
	}
	if j.ConfigVersion != configVersion {
		return Job{}, ErrVersionConflict
	}
	if err := s.validateConfig(ctx, tx, &cfg, len(j.Preview.Columns)); err != nil {
		return Job{}, err
	}
	analysis, infos, err := computeAnalysis(ctx, tx, j.ID, j.SpaceID, headersOf(j.Preview), cfg, j.Preview.API != nil, j.Source)
	if err != nil {
		return Job{}, err
	}
	if err := checkCapabilities(cfg.Options, analysis.Capabilities); err != nil {
		return Job{}, err
	}
	applyAnalysisToPreview(&j.Preview, analysis, infos)
	configJSON, _ := json.Marshal(cfg)
	previewJSON, _ := json.Marshal(j.Preview)
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET config = $2::jsonb, preview = $3::jsonb, config_version = config_version + 1, version = version + 1,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, j.ID, configJSON, previewJSON); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, organizationID, actor, j.ID)
}

// checkCapabilities 保证只开启当前能力允许的选项（保留票号、导入迭代）。
func checkCapabilities(opts Options, caps Capabilities) error {
	if opts.ContinuousSync && !caps.ProjectSync {
		return invalidConfig("当前来源不支持持续同步")
	}
	if opts.KeepNumbers && !caps.KeepNumbers {
		return invalidConfig("只有导入到新空间时才能保留票号")
	}
	if opts.ImportSprints && !caps.Sprints {
		return invalidConfig("当前来源或目标空间不支持导入迭代")
	}
	return nil
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}
