package imports

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/audit"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// LinkedUser 是外部账号关联的成员。
type LinkedUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ExternalAccount 对应契约 ExternalAccount。
type ExternalAccount struct {
	ID                string      `json:"id"`
	Provider          string      `json:"provider"`
	ExternalUserID    string      `json:"external_user_id"`
	DisplayName       string      `json:"display_name"`
	Email             string      `json:"email"`
	SourceLabel       string      `json:"source_label"`
	LinkedUser        *LinkedUser `json:"linked_user"`
	LinkMethod        string      `json:"link_method"`
	SuggestedUserID   *string     `json:"suggested_user_id"`
	PendingInvitation bool        `json:"pending_invitation"`
	TaskCount         int         `json:"task_count"`
	CreatedAt         time.Time   `json:"created_at"`
	Version           int64       `json:"version"`
}

// ListAccountsParams 是外部账号列表的筛选与分页参数。
type ListAccountsParams struct {
	Q, Provider, Source, Link string
	Limit                     int
	Cursor                    string
}

// AccountList 对应 listExternalAccounts 的响应。
type AccountList struct {
	Accounts   []ExternalAccount `json:"accounts"`
	NextCursor string            `json:"next_cursor"`
	Total      int               `json:"total"`
	Unlinked   int               `json:"unlinked"`
	Providers  []string          `json:"providers"`
	Sources    []string          `json:"sources"`
	CanManage  bool              `json:"can_manage"`
}

const (
	defaultAccountPage = 50
	maxAccountPage     = 200
	maxLinkChanges     = 200
)

// accountSelect 读取账号及其关联成员、建议成员与待接受邀请；WHERE 与排序由调用方追加。
const accountSelect = `
	SELECT a.id::text, a.provider, a.external_user_id, a.display_name, a.email, a.source_label,
	       COALESCE(a.linked_user_id::text, ''), COALESCE(lu.email, ''), COALESCE(NULLIF(lu.full_name, ''), lu.email, ''),
	       a.link_method, COALESCE(sg.user_id::text, ''),
	       (a.invitation_id IS NOT NULL AND a.linked_user_id IS NULL AND EXISTS (
	            SELECT 1 FROM openfox.invitations i WHERE i.id = a.invitation_id AND i.status = 'PENDING')),
	       a.created_at, a.version
	  FROM openfox.external_accounts a
	  LEFT JOIN openfox.users lu ON lu.id = a.linked_user_id
	  LEFT JOIN LATERAL (
	       SELECT m.user_id FROM openfox.organization_members m JOIN openfox.users mu ON mu.id = m.user_id
	        WHERE a.linked_user_id IS NULL AND a.email <> '' AND m.organization_id = a.organization_id AND m.status = 'ACTIVE'
	          AND mu.normalized_email = lower(btrim(a.email))
	        ORDER BY m.created_at, m.user_id LIMIT 1) sg ON true
	 WHERE a.organization_id = openfox_private.current_organization_id()`

func scanAccounts(rows pgx.Rows) ([]ExternalAccount, error) {
	defer rows.Close()
	accounts := []ExternalAccount{}
	for rows.Next() {
		var a ExternalAccount
		var linkedID, linkedEmail, linkedName, suggested string
		if err := rows.Scan(&a.ID, &a.Provider, &a.ExternalUserID, &a.DisplayName, &a.Email, &a.SourceLabel,
			&linkedID, &linkedEmail, &linkedName, &a.LinkMethod, &suggested, &a.PendingInvitation, &a.CreatedAt, &a.Version); err != nil {
			return nil, err
		}
		if linkedID != "" {
			a.LinkedUser = &LinkedUser{ID: linkedID, Email: linkedEmail, Name: linkedName}
		}
		a.SuggestedUserID = strPtr(suggested)
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

// fillTaskCounts 批量统计处理人仍是这些外部账号的未删除任务数（一次查询，无 N+1）。
func fillTaskCounts(ctx context.Context, tx pgx.Tx, accounts []ExternalAccount) error {
	if len(accounts) == 0 {
		return nil
	}
	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = a.ID
	}
	rows, err := tx.Query(ctx, `
		SELECT external_assignee_id::text, count(*) FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND deleted_at IS NULL
		   AND external_assignee_id = ANY($1::uuid[])
		 GROUP BY external_assignee_id`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return err
		}
		counts[id] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].TaskCount = counts[accounts[i].ID]
	}
	return nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListAccounts 返回外部账号：所有成员可读（访客 403），按创建时间倒序，cursor 分页。
func (s Service) ListAccounts(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, p ListAccountsParams) (AccountList, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return AccountList{}, err
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return AccountList{}, ErrForbidden
	}
	limit := p.Limit
	if limit == 0 {
		limit = defaultAccountPage
	}
	if limit < 0 || limit > maxAccountPage || len(p.Q) > 100 || len(p.Provider) > 32 || len(p.Source) > 255 ||
		(p.Link != "" && p.Link != "linked" && p.Link != "unlinked") {
		return AccountList{}, ErrInvalidRequest
	}
	// 筛选条件（不含游标）：$1 起依次追加。
	where := ""
	args := []any{}
	add := func(cond string, value any) {
		args = append(args, value)
		where += " AND " + strings.ReplaceAll(cond, "$$", fmt.Sprintf("$%d", len(args)))
	}
	if q := strings.TrimSpace(p.Q); q != "" {
		add(`(a.display_name ILIKE $$ ESCAPE '\' OR a.email ILIKE $$ ESCAPE '\' OR a.external_user_id ILIKE $$ ESCAPE '\')`, "%"+escapeLike(q)+"%")
	}
	if p.Provider != "" {
		add(`a.provider = $$`, p.Provider)
	}
	if p.Source != "" {
		add(`a.source_label = $$`, p.Source)
	}
	switch p.Link {
	case "linked":
		where += " AND a.linked_user_id IS NOT NULL"
	case "unlinked":
		where += " AND a.linked_user_id IS NULL"
	}
	result := AccountList{CanManage: CanManage(actor.Role), Providers: []string{}, Sources: []string{}}
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE a.linked_user_id IS NULL) FROM openfox.external_accounts a
		 WHERE a.organization_id = openfox_private.current_organization_id()`+where, args...).Scan(&result.Total, &result.Unlinked); err != nil {
		return AccountList{}, err
	}
	query := accountSelect + where
	if p.Cursor != "" {
		at, id, err := decodeCursor(p.Cursor)
		if err != nil {
			return AccountList{}, err
		}
		args = append(args, at, id)
		query += fmt.Sprintf(" AND (a.created_at, a.id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
	}
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, query+fmt.Sprintf(" ORDER BY a.created_at DESC, a.id DESC LIMIT $%d", len(args)), args...)
	if err != nil {
		return AccountList{}, err
	}
	if result.Accounts, err = scanAccounts(rows); err != nil {
		return AccountList{}, err
	}
	if len(result.Accounts) > limit {
		result.Accounts = result.Accounts[:limit]
		last := result.Accounts[limit-1]
		result.NextCursor = encodeCursor(last.CreatedAt, last.ID)
	}
	if err := fillTaskCounts(ctx, tx, result.Accounts); err != nil {
		return AccountList{}, err
	}
	for _, q := range []struct {
		sql    string
		target *[]string
	}{
		{`SELECT DISTINCT provider FROM openfox.external_accounts WHERE organization_id = openfox_private.current_organization_id() ORDER BY 1`, &result.Providers},
		{`SELECT DISTINCT source_label FROM openfox.external_accounts WHERE organization_id = openfox_private.current_organization_id() AND source_label <> '' ORDER BY 1`, &result.Sources},
	} {
		rows, err := tx.Query(ctx, q.sql)
		if err != nil {
			return AccountList{}, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return AccountList{}, err
			}
			*q.target = append(*q.target, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return AccountList{}, err
		}
	}
	return result, nil
}

// LinkChange 是一项关联修改：UserID 为空表示取消关联。
type LinkChange struct {
	ID      string
	Version int64
	UserID  *string
}

// ConflictError 是带冲突账号 ID 的版本冲突（errors.Is(err, ErrVersionConflict) 成立）。
type ConflictError struct{ IDs []string }

func (e *ConflictError) Error() string { return ErrVersionConflict.Error() }
func (e *ConflictError) Unwrap() error { return ErrVersionConflict }

// SaveLinksResult 对应 saveExternalAccountLinks 的响应。
type SaveLinksResult struct {
	Accounts        []ExternalAccount `json:"accounts"`
	ReassignedTasks int               `json:"reassigned_tasks"`
}

// SaveLinks 批量保存关联：只有所有者 / 管理员，一个事务，按 id 排序加锁；任一 version 不符整体失败。
func (s Service) SaveLinks(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, changes []LinkChange) (SaveLinksResult, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SaveLinksResult{}, err
	}
	if !CanManage(actor.Role) {
		return SaveLinksResult{}, ErrForbidden
	}
	if len(changes) == 0 || len(changes) > maxLinkChanges {
		return SaveLinksResult{}, ErrInvalidRequest
	}
	byID := map[string]LinkChange{}
	var ids, userIDs []string
	for _, c := range changes {
		c.ID = strings.ToLower(c.ID)
		if !membership.ValidateUUID(c.ID) || c.Version < 1 {
			return SaveLinksResult{}, ErrInvalidRequest
		}
		if _, dup := byID[c.ID]; dup {
			return SaveLinksResult{}, ErrInvalidRequest
		}
		if c.UserID != nil {
			u := strings.ToLower(*c.UserID)
			if !membership.ValidateUUID(u) {
				return SaveLinksResult{}, ErrInvalidRequest
			}
			c.UserID = &u
			userIDs = append(userIDs, u)
		}
		byID[c.ID] = c
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	// 关联的目标必须是活跃成员。
	if users := uniqueStrings(userIDs); len(users) > 0 {
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.organization_members
			 WHERE organization_id = openfox_private.current_organization_id() AND user_id = ANY($1::uuid[]) AND status = 'ACTIVE'`,
			users).Scan(&n); err != nil {
			return SaveLinksResult{}, err
		}
		if n != len(users) {
			return SaveLinksResult{}, withDetail(ErrInvalidRequest, "关联的成员不存在或已停用")
		}
	}
	type current struct {
		version int64
		linked  string
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, version, COALESCE(linked_user_id::text, '') FROM openfox.external_accounts
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])
		 ORDER BY id FOR UPDATE`, ids)
	if err != nil {
		return SaveLinksResult{}, err
	}
	state := map[string]current{}
	for rows.Next() {
		var id string
		var c current
		if err := rows.Scan(&id, &c.version, &c.linked); err != nil {
			rows.Close()
			return SaveLinksResult{}, err
		}
		state[id] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SaveLinksResult{}, err
	}
	if len(state) != len(ids) {
		return SaveLinksResult{}, ErrNotFound
	}
	var conflicts []string
	for _, id := range ids {
		if state[id].version != byID[id].Version {
			conflicts = append(conflicts, id)
		}
	}
	if len(conflicts) > 0 {
		return SaveLinksResult{}, &ConflictError{IDs: conflicts}
	}
	// 转任务不触发自动化。
	if _, err := tx.Exec(ctx, `SELECT set_config('openfox.import_job', 'link', true)`); err != nil {
		return SaveLinksResult{}, err
	}
	reassigned := 0
	for _, id := range ids {
		c, cur := byID[id], state[id]
		switch {
		case c.UserID == nil && cur.linked == "":
			continue
		case c.UserID != nil && cur.linked == *c.UserID:
			continue // 关联到同一个成员：幂等，不改版本也不重复转任务
		case c.UserID == nil:
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.external_accounts SET linked_user_id = NULL, linked_at = NULL, link_method = '',
				       version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id); err != nil {
				return SaveLinksResult{}, err
			}
			if err := recordAccountAudit(ctx, tx, organizationID, actor, "external_account.unlinked", id, map[string]string{}); err != nil {
				return SaveLinksResult{}, err
			}
		default:
			if _, err := tx.Exec(ctx, `
				UPDATE openfox.external_accounts SET linked_user_id = $2::uuid, linked_at = clock_timestamp(), link_method = 'manual',
				       version = version + 1, updated_at = clock_timestamp()
				 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id, *c.UserID); err != nil {
				return SaveLinksResult{}, err
			}
			n, err := reassignTasks(ctx, tx, id, *c.UserID, actor.UserID)
			if err != nil {
				return SaveLinksResult{}, err
			}
			reassigned += n
			if err := recordAccountAudit(ctx, tx, organizationID, actor, "external_account.linked", id,
				map[string]string{"result": "manual"}); err != nil {
				return SaveLinksResult{}, err
			}
		}
	}
	rows, err = tx.Query(ctx, accountSelect+` AND a.id = ANY($1::uuid[]) ORDER BY a.created_at DESC, a.id DESC`, ids)
	if err != nil {
		return SaveLinksResult{}, err
	}
	accounts, err := scanAccounts(rows)
	if err != nil {
		return SaveLinksResult{}, err
	}
	if err := fillTaskCounts(ctx, tx, accounts); err != nil {
		return SaveLinksResult{}, err
	}
	return SaveLinksResult{Accounts: accounts, ReassignedTasks: reassigned}, nil
}

func recordAccountAudit(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, action, accountID string, meta map[string]string) error {
	eventID, err := membership.NewID()
	if err != nil {
		return err
	}
	return audit.Record(ctx, audit.PGXWriter{Tx: tx}, audit.Event{
		ID: eventID, OrganizationID: organizationID, ActorID: actor.UserID, Action: action, Decision: "allow", Reason: "allowed",
		Resource:  audit.ResourceRef{Kind: "external_account", OrganizationID: organizationID, ID: accountID},
		RequestID: newOperationID(), AuthorizationVersion: actor.AuthorizationVersion, SafeMetadata: meta, CreatedAt: time.Now().UTC(),
	})
}

// reassignTasks 把处理人是该外部账号的未删除任务转给成员（worker 与手动关联共用）：
// 成员不能读任务所在空间的任务保持外部处理人，不报错。调用方已在事务里设置 openfox.import_job 标记。
func reassignTasks(ctx context.Context, tx pgx.Tx, accountID, userID, actorID string) (int, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.space_id::text FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id AND s.deleted_at IS NULL
		  JOIN openfox.organization_members om ON om.organization_id = t.organization_id AND om.user_id = $2::uuid AND om.status = 'ACTIVE'
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.external_assignee_id = $1::uuid AND t.deleted_at IS NULL
		   AND (EXISTS (SELECT 1 FROM openfox.space_members sm
		                 WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id AND sm.user_id = om.user_id AND sm.status = 'ACTIVE')
		        OR (s.visibility = 'SHARED' AND om.role <> 'GUEST' AND NOT EXISTS (
		                 SELECT 1 FROM openfox.space_members sm
		                  WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id AND sm.user_id = om.user_id AND sm.status <> 'ACTIVE')))
		 ORDER BY t.id FOR UPDATE OF t`, accountID, userID)
	if err != nil {
		return 0, err
	}
	var taskIDs, spaceIDs []string
	for rows.Next() {
		var id, space string
		if err := rows.Scan(&id, &space); err != nil {
			rows.Close()
			return 0, err
		}
		taskIDs = append(taskIDs, id)
		spaceIDs = append(spaceIDs, space)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(taskIDs) == 0 {
		return 0, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_assignees (organization_id, space_id, task_id, kind, user_id, created_by)
		SELECT openfox_private.current_organization_id(), x.space_id, x.id, 'HUMAN', $3::uuid, $4::uuid
		  FROM unnest($1::uuid[], $2::uuid[]) AS x(id, space_id)
		ON CONFLICT DO NOTHING`, taskIDs, spaceIDs, userID, actorID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, actor_id, kind, safe_diff, source_id)
		SELECT openfox_private.current_organization_id(), x.space_id, x.id, $3::uuid, 'task.assignee_linked',
		       jsonb_build_object('external_account_id', $4::text), 'link:' || $4::text || ':' || x.id::text
		  FROM unnest($1::uuid[], $2::uuid[]) AS x(id, space_id)
		ON CONFLICT DO NOTHING`, taskIDs, spaceIDs, actorID, accountID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.tasks t SET assignee_id = COALESCE(t.assignee_id, $2::uuid), external_assignee_id = NULL,
		       version = t.version + 1, updated_at = clock_timestamp()
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = ANY($1::uuid[])`, taskIDs, userID); err != nil {
		return 0, err
	}
	return len(taskIDs), nil
}
