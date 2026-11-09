package imports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/idempotency"
	"openfox/internal/membership"
	"openfox/internal/spacebundle"
)

// 克隆任务的服务端接口。权限由调用方（API 层）在源组织与目标组织的租户事务里分别校验：
// 发起人必须是源组织所有者 / 管理员并能管理该空间，同时是目标组织所有者 / 管理员。
// worker 执行时在目标组织重新校验发起人身份，并在开始阶段另开一个源组织事务重新校验源组织身份。

const (
	cloneFailureShown = 20
)

// NormalizeCloneRequest 校验并补全副本名称 / 标识符：空值沿用源空间。
func NormalizeCloneRequest(req CloneRequest, sourceName, sourcePrefix string) (name, prefix string, err error) {
	name = strings.TrimSpace(req.Name)
	if name == "" {
		name = sourceName
	}
	if n := utf8.RuneCountInString(name); n < 1 || n > collaboration.MaxSpaceNameRunes {
		return "", "", ErrInvalidRequestWith("副本名称需要 1–32 个字符")
	}
	prefix = strings.TrimSpace(req.Prefix)
	if prefix == "" {
		prefix = sourcePrefix
	}
	prefix, err = collaboration.NormalizeIdentifier(prefix)
	if err != nil {
		return "", "", err
	}
	return name, prefix, nil
}

// CloneTargetCheck 是目标组织里的命名检查结果。
type CloneTargetCheck struct {
	OrganizationName string
	NameTaken        bool
	PrefixTaken      bool
}

// CheckCloneTarget 在目标组织事务里检查副本名称与标识符是否已被占用（含已删除空间的历史标识符）。
func CheckCloneTarget(ctx context.Context, tx pgx.Tx, name, prefix string) (CloneTargetCheck, error) {
	var c CloneTargetCheck
	if err := tx.QueryRow(ctx, `
		SELECT o.name,
		       EXISTS (SELECT 1 FROM openfox.spaces s
		                WHERE s.organization_id = o.id AND s.deleted_at IS NULL AND lower(s.name) = lower($1)),
		       EXISTS (SELECT 1 FROM openfox.spaces s WHERE s.organization_id = o.id AND lower(s.prefix) = lower($2))
		       OR EXISTS (SELECT 1 FROM openfox.space_prefix_history h WHERE h.organization_id = o.id AND lower(h.prefix) = lower($2))
		  FROM openfox.organizations o
		 WHERE o.id = openfox_private.current_organization_id()`, name, prefix).Scan(&c.OrganizationName, &c.NameTaken, &c.PrefixTaken); err != nil {
		return c, err
	}
	return c, nil
}

// BuildClonePreview 由源空间计数与目标命名检查生成预览（纯函数）。
func BuildClonePreview(sum spacebundle.Summary, name, prefix, targetOrgID string, check CloneTargetCheck) ClonePreview {
	p := ClonePreview{
		SourceName: sum.SpaceName, SourcePrefix: sum.SpacePrefix,
		TargetOrganizationID: targetOrgID, TargetOrganizationName: check.OrganizationName,
		TargetName: name, TargetPrefix: prefix, NameConflict: check.NameTaken || check.PrefixTaken,
		Counts: CloneCounts{Spaces: 1, Tasks: sum.Tasks, Projects: sum.Projects, Comments: sum.Comments, Activities: sum.Activities,
			TaskTypes: sum.TaskTypes, TaskStatuses: sum.Statuses, Labels: sum.Labels, Agents: sum.Agents, CustomFields: sum.Fields},
		Blockers: []CloneIssue{}, Warnings: []CloneIssue{}, AcceptedRisks: []CloneIssue{}, Skipped: []CloneIssue{},
	}
	if check.NameTaken {
		p.Blockers = append(p.Blockers, CloneIssue{Code: "name_taken", Message: "目标组织已有同名空间，请确认副本名称。", Count: 0})
	}
	if check.PrefixTaken {
		p.Blockers = append(p.Blockers, CloneIssue{Code: "identifier_taken", Message: "目标组织已有相同标识符的空间，请确认副本标识符。", Count: 0})
	}
	over := sum.Over()
	if over.Tasks {
		p.Blockers = append(p.Blockers, CloneIssue{Code: "task_limit", Message: fmt.Sprintf("任务数超过 %d 条上限（当前 %d 条）。", spacebundle.MaxTasks, sum.Tasks), Count: sum.Tasks})
	}
	if over.Comments {
		p.Blockers = append(p.Blockers, CloneIssue{Code: "comment_limit", Message: fmt.Sprintf("评论数超过 %d 条上限（当前 %d 条）。", spacebundle.MaxComments, sum.Comments), Count: sum.Comments})
	}
	if over.Activities {
		p.Blockers = append(p.Blockers, CloneIssue{Code: "activity_limit", Message: fmt.Sprintf("动态数超过 %d 条上限（当前 %d 条）。", spacebundle.MaxActivities, sum.Activities), Count: sum.Activities})
	}
	if sum.Agents > 0 || sum.AgentAssigned > 0 {
		p.Warnings = append(p.Warnings, CloneIssue{Code: "agents_need_reconfigure", Message: "需要重新配置 Agent。", Count: sum.Agents})
	}
	if sum.Comments > 0 || sum.Activities > 0 {
		p.Warnings = append(p.Warnings, CloneIssue{Code: "actor_history_kept", Message: "历史原始操作者标识可能会作为只读历史记录保留。", Count: sum.Activities + sum.Comments})
	}
	p.AcceptedRisks = append(p.AcceptedRisks, CloneIssue{Code: "people_external",
		Message: "按邮箱对应不到目标组织活跃成员的人员，会保留为目标组织的外部账号。", Count: 0})
	if sum.AgentAssigned > 0 {
		p.AcceptedRisks = append(p.AcceptedRisks, CloneIssue{Code: "agent_assignee_removed",
			Message: "处理人是 Agent 的任务在副本里不保留 Agent 处理人，并在任务动态里留一条只读记录。", Count: sum.AgentAssigned})
	}
	skip := func(code, msg string, n int) {
		if n > 0 {
			p.Skipped = append(p.Skipped, CloneIssue{Code: code, Message: msg, Count: n})
		}
	}
	skip("agents", "Agent（需要在目标组织重新选择电脑和 Runtime）", sum.Agents)
	if sum.UploadedSpaceIcon {
		skip("space_icon", "上传的空间图片图标（副本使用默认图标，可重新上传）", 1)
	}
	skip("links", "任务链接与附件", sum.Links)
	skip("git_links", "Git 关联", sum.GitLinks)
	skip("issue_links", "Issue 同步关联", sum.IssueLinks)
	skip("automations", "自动化", sum.Automations)
	skip("documents", "文档库", sum.Documents)
	skip("skills", "技能", sum.Skills)
	skip("repos", "代码仓库", sum.Repos)
	skip("iterations", "迭代", sum.Iterations)
	skip("cross_space_relations", "跨空间的任务关联", sum.CrossSpaceRelations)
	p.CanStart = len(p.Blockers) == 0
	return p
}

// CloneStartInput 是 CreateCloneJob 的输入。
type CloneStartInput struct {
	JobID                string
	IdempotencyKey       string
	SourceOrganizationID string
	SourceOrgName        string
	SourceSpaceID        string
	Preview              ClonePreview
	BundleKey            string
	Request              CloneRequest
}

// CreateCloneJob 在目标组织事务里登记幂等键并创建 QUEUED 的克隆任务。
// replayed 为 true 表示同键同请求已经创建过任务，返回的是首次创建的任务（调用方应删除刚写出的重复快照包）。
func (s Service) CreateCloneJob(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, in CloneStartInput) (job CloneJob, replayed bool, err error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return CloneJob{}, false, err
	}
	if !CanManage(actor.Role) {
		return CloneJob{}, false, ErrForbidden
	}
	claim, err := idempotency.Begin(ctx, tx, "space.clone", in.IdempotencyKey,
		cloneIdempotencyPayload(in.SourceOrganizationID, in.SourceSpaceID, in.Preview.TargetName, in.Preview.TargetPrefix))
	if err != nil {
		return CloneJob{}, false, err
	}
	if claim.Replay {
		job, err = s.GetClone(ctx, tx, organizationID, actor, claim.ResourceID)
		return job, true, err
	}
	// 命名在事务内再校验一次（预览之后可能已被占用）。
	check, err := CheckCloneTarget(ctx, tx, in.Preview.TargetName, in.Preview.TargetPrefix)
	if err != nil {
		return CloneJob{}, false, err
	}
	if check.PrefixTaken {
		return CloneJob{}, false, collaboration.ErrIdentifierTaken
	}
	if check.NameTaken {
		return CloneJob{}, false, ErrCloneNameTaken
	}
	p := in.Preview
	doc, err := json.Marshal(cloneDoc{
		SourceOrganizationName: in.SourceOrgName, SourceSpaceName: p.SourceName, SourcePrefix: p.SourcePrefix,
		TargetOrganizationName: check.OrganizationName, TargetName: p.TargetName, TargetPrefix: p.TargetPrefix,
		Counts: p.Counts, Skipped: p.Skipped,
	})
	if err != nil {
		return CloneJob{}, false, err
	}
	operationID := newOperationID()
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.import_jobs (
			organization_id, id, source, source_label, file_name, status, preview, total_count, operation_id,
			created_by, started_by, started_at, source_organization_id, source_space_id, bundle_key)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, 'QUEUED', $5::jsonb, $6, $7,
		        $8::uuid, $8::uuid, clock_timestamp(), $9::uuid, $10::uuid, $11)`,
		in.JobID, SourceSpaceClone, truncateBytes(p.SourceName, 255), truncateBytes(p.SourceName, 255), doc, p.Counts.Tasks, operationID,
		actor.UserID, in.SourceOrganizationID, in.SourceSpaceID, in.BundleKey); err != nil {
		return CloneJob{}, false, err
	}
	if err := idempotency.Complete(ctx, tx, claim, in.JobID); err != nil {
		return CloneJob{}, false, err
	}
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "space.clone.started", "", in.JobID, operationID,
		map[string]string{"source": SourceSpaceClone, "result": "queued", "space_id": in.SourceSpaceID}); err != nil {
		return CloneJob{}, false, err
	}
	job, err = s.GetClone(ctx, tx, organizationID, actor, in.JobID)
	return job, false, err
}

// RecordCloneSourceAudit 在源组织事务里记一条克隆开始的审计（资源是源空间）。
func RecordCloneSourceAudit(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, spaceID, jobID string) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actor.UserID, Action: "space.clone.started", Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: "space", OrganizationID: organizationID, SpaceID: spaceID, ID: spaceID},
		RequestID: jobID, AuthorizationVersion: actor.AuthorizationVersion,
		SafeMetadata: map[string]string{"source": SourceSpaceClone, "result": "queued", "resource_kind": "space"}, CreatedAt: time.Now().UTC(),
	})
}

const cloneJobColumns = `
	j.id::text, j.status, j.phase, j.source_organization_id::text, j.source_space_id::text, COALESCE(j.space_id::text, ''),
	COALESCE(s.name, ''), j.total_count, j.created_count, j.skipped_count, j.failed_count, j.warnings, j.error_code, j.error_message,
	j.operation_id, j.created_at, j.started_at, j.finished_at, j.version, j.created_by::text, j.preview`

// GetClone 返回克隆任务。只有发起人且仍是该组织所有者 / 管理员可读：非发起人的管理员得到 ErrNotFound。
func (s Service) GetClone(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (CloneJob, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return CloneJob{}, err
	}
	if !CanManage(actor.Role) {
		return CloneJob{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) {
		return CloneJob{}, ErrNotFound
	}
	var j CloneJob
	var spaceID, createdBy string
	var warnings, preview []byte
	err := tx.QueryRow(ctx, `SELECT `+cloneJobColumns+`
		  FROM openfox.import_jobs j
		  LEFT JOIN openfox.spaces s ON s.organization_id = j.organization_id AND s.id = j.space_id
		 WHERE j.organization_id = openfox_private.current_organization_id() AND j.id = $1::uuid AND j.source = 'space_clone'`, id).
		Scan(&j.ID, &j.Status, &j.Phase, &j.SourceOrganizationID, &j.SourceSpaceID, &spaceID, &j.TargetSpaceName, &j.TotalCount, &j.CreatedCount,
			&j.SkippedCount, &j.FailedCount, &warnings, &j.ErrorCode, &j.ErrorMessage, &j.OperationID, &j.CreatedAt, &j.StartedAt,
			&j.FinishedAt, &j.Version, &createdBy, &preview)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && createdBy != actor.UserID) {
		return CloneJob{}, ErrNotFound
	}
	if err != nil {
		return CloneJob{}, err
	}
	j.TargetOrganizationID = organizationID
	if spaceID != "" {
		j.TargetSpaceID = &spaceID
	}
	var doc cloneDoc
	_ = json.Unmarshal(preview, &doc)
	j.SourceSpaceName = doc.SourceSpaceName
	if j.TargetSpaceName == "" {
		j.TargetSpaceName = doc.TargetName
	}
	j.Warnings = []Warning{}
	_ = json.Unmarshal(warnings, &j.Warnings)
	if j.Warnings == nil {
		j.Warnings = []Warning{}
	}
	j.Cancellable = j.Status == StatusQueued || j.Status == StatusRunning
	j.Failures = []CloneFailure{}
	rows, err := tx.Query(ctx, `
		SELECT external_key, error FROM openfox.import_items
		 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid AND status = 'FAILED'
		 ORDER BY ordinal LIMIT $2`, id, cloneFailureShown)
	if err != nil {
		return CloneJob{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var f CloneFailure
		if err := rows.Scan(&f.Ref, &f.Error); err != nil {
			return CloneJob{}, err
		}
		j.Failures = append(j.Failures, f)
	}
	return j, rows.Err()
}

// CancelClone 取消克隆：排队中直接取消；执行中请求取消（worker 在批次边界停止并清理）；其它状态幂等返回。
// bundleKey 非空表示快照包已不再需要，调用方在事务提交后删除。
func (s Service) CancelClone(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string) (job CloneJob, bundleKey string, err error) {
	if _, err = s.GetClone(ctx, tx, organizationID, actor, id); err != nil {
		return CloneJob{}, "", err
	}
	var status string
	if err = tx.QueryRow(ctx, `
		SELECT status, bundle_key FROM openfox.import_jobs
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, id).Scan(&status, &bundleKey); err != nil {
		return CloneJob{}, "", err
	}
	switch status {
	case StatusQueued:
		if _, err = tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET status = 'CANCELLED', finished_at = clock_timestamp(), phase = '', lease_owner = '', lease_expires_at = NULL,
			       cancel_requested_at = clock_timestamp(), bundle_key = '', version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
			return CloneJob{}, "", err
		}
	case StatusRunning:
		bundleKey = ""
		if _, err = tx.Exec(ctx, `
			UPDATE openfox.import_jobs
			   SET status = 'CANCEL_REQUESTED', cancel_requested_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
			return CloneJob{}, "", err
		}
	default:
		bundleKey = ""
	}
	job, err = s.GetClone(ctx, tx, organizationID, actor, id)
	return job, bundleKey, err
}

func cloneIdempotencyPayload(sourceOrg, sourceSpace, name, prefix string) any {
	return struct {
		SourceOrg, SourceSpace, Name, Prefix string
	}{sourceOrg, sourceSpace, name, prefix}
}

// ErrNoReplay 表示这个幂等键还没有创建过克隆任务（FindCloneReplay 用它回滚事务，不留下幂等记录）。
var ErrNoReplay = errors.New("imports: no replay")

// FindCloneReplay 只查找同键同请求已创建的克隆任务：命中返回该任务；没有命中返回 ErrNoReplay，调用方的事务随之回滚。
// 副本创建后名称 / 标识符已被占用，重试同一个请求时要先于命名冲突检查返回首次创建的任务。
func (s Service) FindCloneReplay(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, key, sourceOrg, sourceSpace, name, prefix string) (CloneJob, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return CloneJob{}, err
	}
	if !CanManage(actor.Role) {
		return CloneJob{}, ErrForbidden
	}
	claim, err := idempotency.Begin(ctx, tx, "space.clone", key, cloneIdempotencyPayload(sourceOrg, sourceSpace, name, prefix))
	if err != nil {
		return CloneJob{}, err
	}
	if !claim.Replay {
		return CloneJob{}, ErrNoReplay
	}
	return s.GetClone(ctx, tx, organizationID, actor, claim.ResourceID)
}
