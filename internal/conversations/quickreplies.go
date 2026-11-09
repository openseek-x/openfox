package conversations

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
	"openfox/internal/policy"
)

// 快捷回复：个人（仅本人）与组织（全员可读，所有者 / 管理员维护）两级，条数各 50。

const (
	QuickReplyPersonal = "PERSONAL"
	QuickReplyOrg      = "ORG"

	MaxQuickRepliesPerScope = 50
	MaxQuickReplyTitle      = 40
	MaxQuickReplyBody       = 2000
)

// ErrQuickReplyLimit 表示该级别的快捷回复已达上限。
var ErrQuickReplyLimit = errors.New("conversations: quick reply limit reached")

type QuickReply struct {
	ID          string    `json:"id"`
	Scope       string    `json:"scope"`
	OwnerUserID string    `json:"owner_user_id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Position    int       `json:"position"`
	Version     uint64    `json:"version"`
	CanEdit     bool      `json:"can_edit"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type QuickReplyList struct {
	Personal     []QuickReply `json:"personal"`
	Organization []QuickReply `json:"organization"`
}

type QuickReplyInput struct {
	ID    string
	Scope string
	Title string
	Body  string
}

type QuickReplyPatch struct {
	Version uint64
	Title   *string
	Body    *string
}

func cleanQuickReplyText(title, body string) (string, string, error) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if title == "" || body == "" || utf8.RuneCountInString(title) > MaxQuickReplyTitle || utf8.RuneCountInString(body) > MaxQuickReplyBody {
		return "", "", ErrInvalidRequest
	}
	return title, body, nil
}

func (s Service) quickReplyActor(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) error {
	return s.CheckActor(ctx, tx, organizationID, actor)
}

// canManage 判断操作人能否维护某一级别：个人只有本人（行已按 owner 过滤），组织需要所有者 / 管理员。
func canManageQuickReplyScope(scope string, actor Actor) bool {
	return scope == QuickReplyPersonal || actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin
}

const quickReplyColumns = `id::text, scope, owner_user_id::text, title, body, position, version, created_at, updated_at`

func scanQuickReply(row interface{ Scan(...any) error }, actor Actor) (QuickReply, error) {
	var item QuickReply
	var version int64
	if err := row.Scan(&item.ID, &item.Scope, &item.OwnerUserID, &item.Title, &item.Body, &item.Position, &version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return QuickReply{}, err
	}
	item.Version = uint64(version)
	item.CanEdit = canManageQuickReplyScope(item.Scope, actor)
	return item, nil
}

// ListQuickReplies 返回本人的个人快捷回复与组织快捷回复（各自按 position 排序）。
func (s Service) ListQuickReplies(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) (QuickReplyList, error) {
	if err := s.quickReplyActor(ctx, tx, organizationID, actor); err != nil {
		return QuickReplyList{}, err
	}
	rows, err := tx.Query(ctx, `
		SELECT `+quickReplyColumns+`
		  FROM openfox.quick_replies
		 WHERE organization_id = openfox_private.current_organization_id() AND deleted_at IS NULL
		   AND (scope = 'ORG' OR owner_user_id = $1::uuid)
		 ORDER BY scope DESC, position, created_at, id`, actor.UserID)
	if err != nil {
		return QuickReplyList{}, err
	}
	defer rows.Close()
	list := QuickReplyList{Personal: []QuickReply{}, Organization: []QuickReply{}}
	for rows.Next() {
		item, err := scanQuickReply(rows, actor)
		if err != nil {
			return QuickReplyList{}, err
		}
		if item.Scope == QuickReplyOrg {
			list.Organization = append(list.Organization, item)
		} else {
			list.Personal = append(list.Personal, item)
		}
	}
	return list, rows.Err()
}

// lockQuickReplyScope 串行化同一级别的写入（计数、排序），避免并发突破上限或排序交错。
func lockQuickReplyScope(ctx context.Context, tx pgx.Tx, scope, ownerID string) error {
	key := "quick_reply:" + scope
	if scope == QuickReplyPersonal {
		key += ":" + ownerID
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(openfox_private.current_organization_id()::text || $1, 0))`, key)
	return err
}

// CreateQuickReply 新建快捷回复。同一个 id 重复提交且内容相同时返回已有记录，内容不同返回 ErrConflict。
func (s Service) CreateQuickReply(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input QuickReplyInput) (QuickReply, error) {
	if err := s.quickReplyActor(ctx, tx, organizationID, actor); err != nil {
		return QuickReply{}, err
	}
	title, body, err := cleanQuickReplyText(input.Title, input.Body)
	if err != nil || !membership.ValidateUUID(input.ID) || (input.Scope != QuickReplyPersonal && input.Scope != QuickReplyOrg) {
		return QuickReply{}, ErrInvalidRequest
	}
	if !canManageQuickReplyScope(input.Scope, actor) {
		return QuickReply{}, ErrForbidden
	}
	if err := lockQuickReplyScope(ctx, tx, input.Scope, actor.UserID); err != nil {
		return QuickReply{}, err
	}
	existing, err := s.loadQuickReplyAny(ctx, tx, input.ID, actor)
	if err == nil {
		if existing.Scope == input.Scope && existing.OwnerUserID == actor.UserID && existing.Title == title && existing.Body == body {
			return existing, nil
		}
		return QuickReply{}, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return QuickReply{}, err
	}
	var count, next int
	if err := tx.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(position) + 1, 0) FROM openfox.quick_replies
		 WHERE organization_id = openfox_private.current_organization_id() AND deleted_at IS NULL
		   AND scope = $1 AND ($1 = 'ORG' OR owner_user_id = $2::uuid)`, input.Scope, actor.UserID).Scan(&count, &next); err != nil {
		return QuickReply{}, err
	}
	if count >= MaxQuickRepliesPerScope {
		return QuickReply{}, ErrQuickReplyLimit
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO openfox.quick_replies (organization_id, id, scope, owner_user_id, title, body, position)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3::uuid, $4, $5, $6)
		RETURNING `+quickReplyColumns, input.ID, input.Scope, actor.UserID, title, body, next)
	item, err := scanQuickReply(row, actor)
	if err != nil {
		return QuickReply{}, mapError(err)
	}
	return item, nil
}

// loadQuickReplyAny 读取一条未删除、且对操作人可见的快捷回复（组织级全员可见，个人级仅本人）。
func (s Service) loadQuickReplyAny(ctx context.Context, tx pgx.Tx, id string, actor Actor) (QuickReply, error) {
	if !membership.ValidateUUID(id) {
		return QuickReply{}, ErrNotFound
	}
	item, err := scanQuickReply(tx.QueryRow(ctx, `
		SELECT `+quickReplyColumns+` FROM openfox.quick_replies
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND deleted_at IS NULL AND (scope = 'ORG' OR owner_user_id = $2::uuid)`, id, actor.UserID), actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return QuickReply{}, ErrNotFound
	}
	return item, err
}

// UpdateQuickReply 编辑标题 / 正文（乐观锁 version）。
func (s Service) UpdateQuickReply(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, id string, patch QuickReplyPatch) (QuickReply, error) {
	if err := s.quickReplyActor(ctx, tx, organizationID, actor); err != nil {
		return QuickReply{}, err
	}
	current, err := s.loadQuickReplyAny(ctx, tx, id, actor)
	if err != nil {
		return QuickReply{}, err
	}
	if !current.CanEdit {
		return QuickReply{}, ErrForbidden
	}
	if patch.Version == 0 || (patch.Title == nil && patch.Body == nil) {
		return QuickReply{}, ErrInvalidRequest
	}
	title, body := current.Title, current.Body
	if patch.Title != nil {
		title = *patch.Title
	}
	if patch.Body != nil {
		body = *patch.Body
	}
	if title, body, err = cleanQuickReplyText(title, body); err != nil {
		return QuickReply{}, err
	}
	row := tx.QueryRow(ctx, `
		UPDATE openfox.quick_replies
		   SET title = $3, body = $4, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $2 AND deleted_at IS NULL
		RETURNING `+quickReplyColumns, id, int64(patch.Version), title, body)
	item, err := scanQuickReply(row, actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return QuickReply{}, ErrVersionConflict
	}
	return item, err
}

// DeleteQuickReply 软删除；已删除或不可见的返回 ErrNotFound。
func (s Service) DeleteQuickReply(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, id string) (QuickReply, error) {
	if err := s.quickReplyActor(ctx, tx, organizationID, actor); err != nil {
		return QuickReply{}, err
	}
	current, err := s.loadQuickReplyAny(ctx, tx, id, actor)
	if err != nil {
		return QuickReply{}, err
	}
	if !current.CanEdit {
		return QuickReply{}, ErrForbidden
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.quick_replies SET deleted_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, id)
	if err != nil {
		return QuickReply{}, err
	}
	if tag.RowsAffected() != 1 {
		return QuickReply{}, ErrNotFound
	}
	return current, nil
}

// ReorderQuickReplies 按 ids 的顺序重排一个级别。ids 必须恰好是该级别当前所有条目（多、少、重复都返回 ErrVersionConflict，
// 让前端刷新后重试）。
func (s Service) ReorderQuickReplies(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, scope string, ids []string) ([]QuickReply, error) {
	if err := s.quickReplyActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if (scope != QuickReplyPersonal && scope != QuickReplyOrg) || len(ids) > MaxQuickRepliesPerScope {
		return nil, ErrInvalidRequest
	}
	if !canManageQuickReplyScope(scope, actor) {
		return nil, ErrForbidden
	}
	for _, id := range ids {
		if !membership.ValidateUUID(id) {
			return nil, ErrInvalidRequest
		}
	}
	if err := lockQuickReplyScope(ctx, tx, scope, actor.UserID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM openfox.quick_replies
		 WHERE organization_id = openfox_private.current_organization_id() AND deleted_at IS NULL
		   AND scope = $1 AND ($1 = 'ORG' OR owner_user_id = $2::uuid)`, scope, actor.UserID)
	if err != nil {
		return nil, err
	}
	current := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		current[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != len(current) {
		return nil, ErrVersionConflict
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !current[id] || seen[id] {
			return nil, ErrVersionConflict
		}
		seen[id] = true
	}
	for position, id := range ids {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.quick_replies SET position = $2, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND position <> $2`, id, position); err != nil {
			return nil, err
		}
	}
	list, err := s.ListQuickReplies(ctx, tx, organizationID, actor)
	if err != nil {
		return nil, err
	}
	if scope == QuickReplyOrg {
		return list.Organization, nil
	}
	return list.Personal, nil
}
