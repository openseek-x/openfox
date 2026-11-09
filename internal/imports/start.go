package imports

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/invitations"
	"openfox/internal/membership"
)

// PendingInvite 是开始导入后需要在事务提交后发送邀请的来源用户。
type PendingInvite struct {
	SourceID  string
	Email     string
	AccountID string
}

// StartResult 是 Start 第一段的结果：Job 为开始后的状态，Invites 交给 handler 做第二段（创建邀请并发邮件）。
type StartResult struct {
	Job     Job
	Invites []PendingInvite
	// Started 为 false 表示导入早已开始（幂等返回），不需要再发邀请。
	Started bool
}

func truncateBytes(s string, n int) string {
	t, _ := TruncateRunes(s, n)
	return t
}

// Start 第一段（在组织事务内）：锁行、CAS、重算分析、回填条目、upsert 外部账号、进入 QUEUED。
func (s Service) Start(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, configVersion int64, confirmOverwrite bool) (StartResult, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return StartResult{}, err
	}
	if !CanManage(actor.Role) {
		return StartResult{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || configVersion < 1 {
		return StartResult{}, ErrNotFound
	}
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return StartResult{}, err
	}
	if j.Status == StatusDiscarded {
		return StartResult{}, ErrImportState
	}
	if j.Status != StatusDraft {
		job, err := s.Get(ctx, tx, organizationID, actor, id)
		return StartResult{Job: job}, err
	}
	if j.ConfigVersion != configVersion {
		return StartResult{}, ErrVersionConflict
	}
	// API 草稿还在拉取条目（或凭据尚未清除）时不能开始。
	if j.Phase == "fetching" || j.HasCredentials {
		return StartResult{}, ErrImportState
	}
	cfg := j.Config
	analysis, infos, err := computeAnalysis(ctx, tx, j.ID, j.SpaceID, headersOf(j.Preview), cfg, j.Preview.API != nil, j.Source)
	if err != nil {
		return StartResult{}, err
	}
	if err := checkCapabilities(cfg.Options, analysis.Capabilities); err != nil {
		return StartResult{}, err
	}
	if len(analysis.Issues) > 0 {
		return StartResult{}, withDetail(ErrConfigIncomplete, "%s", analysis.Issues[0].Message)
	}
	if cfg.Options.Overwrite && analysis.Overwrite.Modified > 0 && !confirmOverwrite {
		return StartResult{}, withDetail(ErrOverwriteConfirmation, "将覆盖 %d 条本地修改，需要确认", analysis.Overwrite.Modified)
	}

	// 选择了新建空间：此时才创建（创建者成为空间所有者），之后的流程与已有空间一致。
	if j.SpaceID == "" {
		if j.Preview.NewSpace == nil {
			return StartResult{}, withDetail(ErrConfigIncomplete, "没有选择目标空间")
		}
		spaceID, err := s.createImportSpace(ctx, tx, organizationID, actor, *j.Preview.NewSpace, cfg, analysis)
		if err != nil {
			return StartResult{}, err
		}
		j.SpaceID = spaceID
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.import_jobs SET space_id = $2::uuid, space_created = true
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, j.ID, spaceID); err != nil {
			return StartResult{}, err
		}
	}

	if cfg.Options.ContinuousSync {
		o := j.Preview.Origin
		if o.Project == "" || !membership.ValidateUUID(o.Connection) {
			return StartResult{}, ErrConfigIncomplete
		}
		_, err := tx.Exec(ctx, `INSERT INTO openfox.import_sync_links(organization_id,job_id,space_id,connection_id,project_id)
            VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4)`, id, j.SpaceID, o.Connection, o.Project)
		if isUniqueViolation(err, "import_sync_live_project") {
			return StartResult{}, withDetail(ErrImportActive, "该项目已启用同步")
		}
		if err != nil {
			return StartResult{}, err
		}
	}
	// 回填条目：external_id / key / parent 与初始状态。
	n := len(infos)
	ordinals := make([]int32, n)
	eids := make([]string, n)
	keys := make([]string, n)
	parents := make([]string, n)
	statuses := make([]string, n)
	actions := make([]string, n)
	errs := make([]string, n)
	failed, skipped := 0, 0
	for i, info := range infos {
		ordinals[i] = int32(info.Ordinal)
		eids[i] = "row-" + itoa(info.Ordinal)
		statuses[i] = "PENDING"
		switch {
		case info.Invalid:
			statuses[i], actions[i], errs[i] = "FAILED", "skipped_invalid", info.ErrMsg
			failed++
		case info.Filtered:
			statuses[i], actions[i] = "SKIPPED", "skipped_filtered"
			eids[i] = info.Data.SourceID
			skipped++
		default:
			eids[i] = info.Data.SourceID
		}
		if !info.Invalid {
			keys[i] = truncateBytes(info.Data.SourceKey, 255)
			parents[i] = truncateBytes(info.Data.ParentID, 255)
		}
	}
	if n > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.import_items i
			   SET external_id = v.eid, external_key = v.ekey, parent_external_id = v.pid, status = v.st, action = v.act, error = v.err,
			       processed_at = CASE WHEN v.st = 'PENDING' THEN NULL ELSE clock_timestamp() END
			  FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[])
			       AS v(ord, eid, ekey, pid, st, act, err)
			 WHERE i.organization_id = openfox_private.current_organization_id() AND i.job_id = $1::uuid AND i.ordinal = v.ord`,
			j.ID, ordinals, eids, keys, parents, statuses, actions, errs); err != nil {
			return StartResult{}, err
		}
	}

	invites, results, err := s.upsertExternalAccounts(ctx, tx, organizationID, actor, cfg, analysis, j.SourceLabel, sourceSystem(j.Source))
	if err != nil {
		return StartResult{}, err
	}
	j.Preview.Analysis = &analysis
	j.Preview.Invitations = results
	previewJSON, _ := json.Marshal(j.Preview)
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.import_jobs
		   SET status = 'QUEUED', started_at = clock_timestamp(), started_by = $2::uuid, total_count = $3,
		       failed_count = $4, skipped_count = $5, preview = $6::jsonb, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
		j.ID, actor.UserID, n, failed, skipped, previewJSON); err != nil {
		if isUniqueViolation(err, "import_jobs_space_active_unique") {
			return StartResult{}, ErrImportActive
		}
		return StartResult{}, err
	}
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "import.started", j.SpaceID, j.ID, j.OperationID,
		map[string]string{"source": j.Source}); err != nil {
		return StartResult{}, err
	}
	job, err := s.Get(ctx, tx, organizationID, actor, j.ID)
	return StartResult{Job: job, Invites: invites, Started: true}, err
}

func itoa(n int) string { return strconv.Itoa(n) }

// upsertExternalAccounts 为 invite / external 与没有映射的来源用户建立外部账号（member / skip 不建）。
// invite 且邮箱已是成员时直接关联并记 already_member。
func (s Service) upsertExternalAccounts(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, cfg Config, analysis Analysis, sourceLabel, provider string) ([]PendingInvite, []InvitationResult, error) {
	modes := map[string]UserMapping{}
	for _, u := range cfg.Users {
		modes[u.SourceID] = u
	}
	var invites []PendingInvite
	results := []InvitationResult{}
	for _, au := range analysis.Users {
		m, mapped := modes[au.SourceID]
		mode := "external"
		if mapped {
			mode = m.Mode
		}
		if mode == "member" || mode == "skip" || len(au.SourceID) > 255 {
			continue
		}
		// 来源邮箱（API 来源有）保存到账号上，成员页据此提示「可自动关联」；邀请的邮箱优先。
		email := strings.TrimSpace(au.Email)
		if mode == "invite" {
			email = m.Email
		}
		if len(email) > 320 {
			email = ""
		}
		name := truncateBytes(strings.TrimSpace(au.Name), 200)
		if name == "" {
			name = truncateBytes(au.SourceID, 200)
		}
		var accountID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO openfox.external_accounts (organization_id, provider, external_user_id, display_name, email, created_by, source_label)
			VALUES (openfox_private.current_organization_id(), $1, $2, $3, $4, $5::uuid, $6)
			ON CONFLICT (organization_id, provider, lower(external_user_id)) DO UPDATE
			   SET display_name = EXCLUDED.display_name,
			       source_label = CASE WHEN openfox.external_accounts.source_label = '' THEN EXCLUDED.source_label
			                           ELSE openfox.external_accounts.source_label END,
			       email = CASE WHEN EXCLUDED.email <> '' THEN EXCLUDED.email ELSE openfox.external_accounts.email END,
			       updated_at = clock_timestamp(), version = openfox.external_accounts.version + 1
			RETURNING id::text`, provider, au.SourceID, name, email, actor.UserID, truncateBytes(sourceLabel, 255)).Scan(&accountID); err != nil {
			return nil, nil, err
		}
		// 成员已在「第三方账号绑定」页验证过同站点同标识的 Jira 身份：直接关联（verified），不再邀请。
		var verifiedUser string
		verr := tx.QueryRow(ctx, `
			UPDATE openfox.external_accounts a SET linked_user_id = c.owner_user_id, linked_at = clock_timestamp(), link_method = 'verified',
			       updated_at = clock_timestamp(), version = a.version + 1
			  FROM openfox.integration_connections c
			  JOIN openfox.organization_members m ON m.organization_id = c.organization_id AND m.user_id = c.owner_user_id AND m.status = 'ACTIVE'
			 WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = $1::uuid AND a.linked_user_id IS NULL
			   AND c.organization_id = a.organization_id AND a.provider = 'jira' AND c.provider = a.provider AND c.scope = 'PERSONAL' AND c.status <> 'DISCONNECTED'
			   AND lower(c.external_id) = lower(a.external_user_id)
			   AND lower(regexp_replace(c.settings->>'site_url', '^https://', '')) = lower(a.source_label)
			RETURNING a.linked_user_id::text`, accountID).Scan(&verifiedUser)
		switch {
		case verr == nil:
			if err := recordAccountAudit(ctx, tx, organizationID, actor, "external_account.linked", accountID,
				map[string]string{"result": "verified"}); err != nil {
				return nil, nil, err
			}
			continue
		case !errors.Is(verr, pgx.ErrNoRows):
			return nil, nil, verr
		}
		if mode != "invite" {
			continue
		}
		var memberID string
		err := tx.QueryRow(ctx, `
			SELECT m.user_id::text FROM openfox.organization_members m JOIN openfox.users u ON u.id = m.user_id
			 WHERE m.organization_id = openfox_private.current_organization_id() AND u.normalized_email = $1 AND m.status = 'ACTIVE'`,
			email).Scan(&memberID)
		switch {
		case err == nil:
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.external_accounts SET linked_user_id = $2::uuid, linked_at = COALESCE(linked_at, clock_timestamp()),
				       link_method = 'email', updated_at = clock_timestamp(), version = version + 1
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND linked_user_id IS NULL`,
				accountID, memberID); err != nil {
				return nil, nil, err
			}
			results = append(results, InvitationResult{SourceID: au.SourceID, Email: email, Result: "already_member"})
		case errors.Is(err, pgx.ErrNoRows):
			invites = append(invites, PendingInvite{SourceID: au.SourceID, Email: email, AccountID: accountID})
		default:
			return nil, nil, err
		}
	}
	return invites, results, nil
}

// RecordInvitations 是 Start 第二段的收尾（短事务）：把邀请 ID 写到外部账号，把结果合并进 preview.invitations。
func (s Service) RecordInvitations(ctx context.Context, tx pgx.Tx, jobID string, links map[string]string, results []InvitationResult) error {
	for accountID, invitationID := range links {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.external_accounts SET invitation_id = $2::uuid, updated_at = clock_timestamp(), version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND linked_user_id IS NULL`,
			accountID, invitationID); err != nil {
			return err
		}
	}
	j, err := lockJob(ctx, tx, jobID)
	if err != nil {
		return err
	}
	j.Preview.Invitations = append(j.Preview.Invitations, results...)
	previewJSON, _ := json.Marshal(j.Preview)
	_, err = tx.Exec(ctx, `
		UPDATE openfox.import_jobs SET preview = $2::jsonb
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, jobID, previewJSON)
	return err
}

// FindPendingInvitation 查出邮箱当前 PENDING 邀请的 ID（创建邀请冲突时关联已有邀请）。
func (s Service) FindPendingInvitation(ctx context.Context, tx pgx.Tx, email string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.invitations
		 WHERE organization_id = openfox_private.current_organization_id() AND normalized_email = $1
		   AND status = 'PENDING' AND expires_at > clock_timestamp() ORDER BY issued_at DESC LIMIT 1`,
		invitations.NormalizeEmail(email)).Scan(&id)
	return id, err
}

// Cancel 取消导入：DRAFT 丢弃，QUEUED 直接取消，RUNNING 请求取消（由 worker 收尾），终态幂等。
func (s Service) Cancel(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, id string, version int64) (Job, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Job{}, err
	}
	if !CanManage(actor.Role) {
		return Job{}, ErrForbidden
	}
	if !membership.ValidateUUID(id) || version < 1 {
		return Job{}, ErrNotFound
	}
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return Job{}, err
	}
	switch j.Status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusRolledBack, StatusDiscarded, StatusCancelRequested:
		return s.Get(ctx, tx, organizationID, actor, id)
	}
	// 执行中每批都会递增 version，取消意图明确，只有草稿校验版本（避免用户点取消时频繁 409）。
	if j.Status == StatusDraft && j.Version != version {
		return Job{}, ErrVersionConflict
	}
	switch j.Status {
	case StatusDraft:
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.import_items SET raw = '{}'::jsonb
			 WHERE organization_id = openfox_private.current_organization_id() AND job_id = $1::uuid`, j.ID); err != nil {
			return Job{}, err
		}
		// 拉取中的 API 草稿同时清除凭据、释放租约。
		_, err = tx.Exec(ctx, `
			UPDATE openfox.import_jobs SET status = 'DISCARDED', phase = '', credentials_sealed = NULL, lease_owner = '',
			       lease_expires_at = NULL, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, j.ID)
	case StatusQueued:
		_, err = tx.Exec(ctx, `
			UPDATE openfox.import_jobs SET status = 'CANCELLED', finished_at = clock_timestamp(), lease_owner = '', lease_expires_at = NULL,
			       phase = '', credentials_sealed = NULL, version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, j.ID)
	case StatusRunning:
		_, err = tx.Exec(ctx, `
			UPDATE openfox.import_jobs SET status = 'CANCEL_REQUESTED', cancel_requested_at = clock_timestamp(),
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, j.ID)
	default:
		return Job{}, ErrImportState
	}
	if err != nil {
		return Job{}, err
	}
	if err := recordAudit(ctx, tx, organizationID, actor.UserID, actor.AuthorizationVersion, "import.cancelled", j.SpaceID, j.ID, j.OperationID,
		map[string]string{"status": j.Status}); err != nil {
		return Job{}, err
	}
	return s.Get(ctx, tx, organizationID, actor, j.ID)
}

// createImportSpace 在开始导入时创建「新建空间」：用途为软件开发；开了导入迭代时打开迭代功能并设为手动模式；
// 来源任务类型与推荐模板同名的，随空间一起创建对应的任务类型。
func (s Service) createImportSpace(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, ns NewSpace, cfg Config, analysis Analysis) (string, error) {
	input := collaboration.SpaceCreateInput{Name: ns.Name, Prefix: ns.Prefix, Visibility: ns.Visibility, Purpose: "software_development"}
	if cfg.Options.ImportSprints {
		enabled := true
		input.Features = &collaboration.SpaceFeaturesPatch{Iterations: &enabled}
	}
	seen := map[string]bool{}
	for _, t := range analysis.Types {
		for _, template := range collaboration.TaskTypeTemplates {
			if strings.EqualFold(template.Name, t.Source) && !seen[template.Key] && len(input.TemplateKeys) < 20 {
				seen[template.Key] = true
				input.TemplateKeys = append(input.TemplateKeys, template.Key)
			}
		}
	}
	detail, _, err := (collaboration.Service{}).CreateSpaceDetail(ctx, tx, organizationID, actor, input)
	if err != nil {
		return "", err
	}
	if cfg.Options.ImportSprints {
		// 导入的迭代按来源日期手动创建，不能由自动模式重排。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.spaces SET iteration_mode = 'manual', updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, detail.ID); err != nil {
			return "", err
		}
	}
	return detail.ID, nil
}
