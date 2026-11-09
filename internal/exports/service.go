// Package exports 实现任务数据导出：发起导出（API 请求内，发起人身份）、后台 worker 生成文件、
// 下载鉴权与过期清理。文件内容来自 internal/spacebundle 的快照包读取器：JSON 就是快照包本身，CSV 由快照包转换。
package exports

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// 上限与保留期。
const (
	MaxSpaces = 20
	MaxTasks  = 100000
	Retention = 7 * 24 * time.Hour
)

const (
	FormatCSV  = "csv"
	FormatJSON = "json"

	StatusQueued    = "QUEUED"
	StatusRunning   = "RUNNING"
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusExpired   = "EXPIRED"
)

var (
	ErrForbidden    = errors.New("exports: forbidden")
	ErrNotFound     = errors.New("exports: not found")
	ErrInvalid      = errors.New("exports: invalid request")
	ErrTooMany      = errors.New("exports: too many spaces or tasks")
	ErrNotReady     = errors.New("exports: file is not ready")
	ErrExpired      = errors.New("exports: file has expired")
	ErrSpaceMissing = errors.New("exports: space is not available")
)

// TooManyError 说明超出哪一项上限，供接口返回可读的提示。
type TooManyError struct {
	Spaces, Tasks int
}

func (e *TooManyError) Error() string {
	return fmt.Sprintf("exports: %d spaces, %d tasks exceed limits", e.Spaces, e.Tasks)
}
func (e *TooManyError) Unwrap() error { return ErrTooMany }

// Request 是发起导出的请求。
type Request struct {
	SpaceIDs []string `json:"space_ids"`
	Format   string   `json:"format"`
}

// SpaceOption 是可导出的空间（发起人能读）。
type SpaceOption struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"`
	TaskCount int    `json:"task_count"`
}

// Job 对应契约 ExportJob。
type Job struct {
	ID           string     `json:"id"`
	Format       string     `json:"format"`
	Status       string     `json:"status"`
	SpaceIDs     []string   `json:"space_ids"`
	SpaceCount   int        `json:"space_count"`
	TaskCount    int        `json:"task_count"`
	FileName     string     `json:"file_name"`
	SizeBytes    int64      `json:"size_bytes"`
	ErrorCode    string     `json:"error_code"`
	ErrorMessage string     `json:"error_message"`
	OperationID  string     `json:"operation_id"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Downloadable bool       `json:"downloadable"`
}

// Download 是下载所需的最小信息。
type Download struct {
	Key         string
	FileName    string
	ContentType string
	SizeBytes   int64
}

type Service struct {
	Now func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func canManage(role policy.OrganizationRole) bool {
	return role == policy.OrganizationRoleOwner || role == policy.OrganizationRoleAdmin
}

// checkActor 校验操作人仍是组织成员；访客不能导出。
func (s Service) checkActor(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) error {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return ErrForbidden
	}
	return nil
}

func (s Service) readable(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) ([]collaboration.SpaceDetail, error) {
	return (collaboration.Service{}).ListSpaceDetails(ctx, tx, organizationID, actor, collaboration.SpaceListAll)
}

// Spaces 返回发起人能读的空间（含已停用）。所有者 / 管理员也受私有空间规则约束。
func (s Service) Spaces(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) ([]SpaceOption, error) {
	if err := s.checkActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	items, err := s.readable(ctx, tx, organizationID, actor)
	if err != nil {
		return nil, err
	}
	out := make([]SpaceOption, 0, len(items))
	for _, item := range items {
		out = append(out, SpaceOption{ID: item.ID, Name: item.Name, Prefix: item.Prefix, TaskCount: item.TaskCount})
	}
	return out, nil
}

func newOperationID() string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 12)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return "op_" + string(b)
}

// Create 登记一个 QUEUED 的导出任务（幂等键必填由接口层保证）。replayed 为 true 表示返回的是首次创建的任务。
func (s Service) Create(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, key string, req Request) (job Job, replayed bool, err error) {
	if err := s.checkActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, false, err
	}
	if req.Format != FormatCSV && req.Format != FormatJSON {
		return Job{}, false, fmt.Errorf("%w: format must be csv or json", ErrInvalid)
	}
	ids := make([]string, 0, len(req.SpaceIDs))
	seen := map[string]bool{}
	for _, id := range req.SpaceIDs {
		id = strings.ToLower(strings.TrimSpace(id))
		if !membership.ValidateUUID(id) {
			return Job{}, false, fmt.Errorf("%w: invalid space id", ErrInvalid)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return Job{}, false, fmt.Errorf("%w: select at least one space", ErrInvalid)
	}
	if len(ids) > MaxSpaces {
		return Job{}, false, &TooManyError{Spaces: len(ids)}
	}
	claim, err := idempotency.Begin(ctx, tx, "task.export", key, struct {
		SpaceIDs []string
		Format   string
	}{sortedCopy(ids), req.Format})
	if err != nil {
		return Job{}, false, err
	}
	if claim.Replay {
		job, err = s.Get(ctx, tx, organizationID, actor, claim.ResourceID)
		return job, true, err
	}
	// 只能导出自己能读的空间；读不到的当作不存在（不泄露私有空间）。
	items, err := s.readable(ctx, tx, organizationID, actor)
	if err != nil {
		return Job{}, false, err
	}
	readable := map[string]collaboration.SpaceDetail{}
	for _, item := range items {
		readable[item.ID] = item
	}
	tasks := 0
	for _, id := range ids {
		item, ok := readable[id]
		if !ok {
			return Job{}, false, ErrSpaceMissing
		}
		tasks += item.TaskCount
	}
	if len(ids) > MaxSpaces || tasks > MaxTasks {
		return Job{}, false, &TooManyError{Spaces: len(ids), Tasks: tasks}
	}
	id, err := membership.NewID()
	if err != nil {
		return Job{}, false, err
	}
	operationID := newOperationID()
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.export_jobs (organization_id, id, format, space_ids, status, space_count, task_count, operation_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3::uuid[], 'QUEUED', $4, $5, $6, $7::uuid)`,
		id, req.Format, ids, len(ids), tasks, operationID, actor.UserID); err != nil {
		return Job{}, false, err
	}
	if err := idempotency.Complete(ctx, tx, claim, id); err != nil {
		return Job{}, false, err
	}
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "task.export.requested", id, operationID,
		fmt.Sprintf("spaces=%d;format=%s;tasks=%d", len(ids), req.Format, tasks)); err != nil {
		return Job{}, false, err
	}
	job, err = s.Get(ctx, tx, organizationID, actor, id)
	return job, false, err
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

const jobColumns = `
	j.id::text, j.format, j.status, j.space_ids::text[], j.space_count, j.task_count, j.file_name, j.size_bytes,
	j.error_code, j.error_message, j.operation_id, j.created_by::text, j.created_at, j.finished_at, j.expires_at`

func scanJob(row pgx.Row, now time.Time) (Job, error) {
	var j Job
	if err := row.Scan(&j.ID, &j.Format, &j.Status, &j.SpaceIDs, &j.SpaceCount, &j.TaskCount, &j.FileName, &j.SizeBytes,
		&j.ErrorCode, &j.ErrorMessage, &j.OperationID, &j.CreatedBy, &j.CreatedAt, &j.FinishedAt, &j.ExpiresAt); err != nil {
		return Job{}, err
	}
	j.Downloadable = j.Status == StatusSucceeded && j.ExpiresAt != nil && j.ExpiresAt.After(now)
	return j, nil
}

// allowed 判断操作人能否看到该导出：发起人，或所有者 / 管理员。
func allowed(actor collaboration.Actor, createdBy string) bool {
	return actor.UserID == createdBy || canManage(actor.Role)
}

// List 返回导出记录：成员只看到自己发起的，所有者 / 管理员看到全部，最近 50 条。
func (s Service) List(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor) ([]Job, error) {
	if err := s.checkActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+jobColumns+`
		  FROM openfox.export_jobs j
		 WHERE j.organization_id = openfox_private.current_organization_id() AND ($2 OR j.created_by = $1::uuid)
		 ORDER BY j.created_at DESC, j.id DESC LIMIT 50`, actor.UserID, canManage(actor.Role))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows, now)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Get 返回单个导出。不存在 404；存在但不是发起人也不是所有者 / 管理员 403。
func (s Service) Get(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (Job, error) {
	if err := s.checkActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	if !membership.ValidateUUID(id) {
		return Job{}, ErrNotFound
	}
	j, err := scanJob(tx.QueryRow(ctx, `SELECT `+jobColumns+`
		  FROM openfox.export_jobs j
		 WHERE j.organization_id = openfox_private.current_organization_id() AND j.id = $1::uuid`, id), s.now())
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if !allowed(actor, j.CreatedBy) {
		return Job{}, ErrForbidden
	}
	return j, nil
}

// Download 返回下载信息。文件尚未生成、已失败或已过期返回 ErrNotReady（接口层为 409 / 410）。
func (s Service) Download(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (Download, error) {
	j, err := s.Get(ctx, tx, organizationID, actor, id)
	if err != nil {
		return Download{}, err
	}
	if j.Status == StatusExpired || (j.Status == StatusSucceeded && !j.Downloadable) {
		return Download{}, ErrExpired
	}
	if !j.Downloadable {
		return Download{}, ErrNotReady
	}
	var d Download
	if err := tx.QueryRow(ctx, `
		SELECT file_key, file_name, content_type, size_bytes FROM openfox.export_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id).
		Scan(&d.Key, &d.FileName, &d.ContentType, &d.SizeBytes); err != nil {
		return Download{}, err
	}
	return d, nil
}

func recordAudit(ctx context.Context, tx pgx.Tx, organizationID, actorID string, authVersion uint64, action, jobID, operationID, summary string) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	// 审计元数据只允许白名单键；空间数 / 格式 / 任务数放在 result 里。
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actorID, Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: "export_job", OrganizationID: organizationID, ID: jobID},
		RequestID: operationID, AuthorizationVersion: authVersion,
		SafeMetadata: map[string]string{"resource_kind": "export_job", "source": "task_export", "result": summary},
		CreatedAt:    time.Now().UTC(),
	})
}
