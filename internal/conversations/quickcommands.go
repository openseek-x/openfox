package conversations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// 快捷指令：按空间归属，私有（仅本人）与共享（空间可读的人都可见，创建人与空间管理员维护）两组，各 50 条。
// 触发时由前端组合现有接口：建挂任务的会话（Agent 或小队）→ 发送提示词。这里只负责指令本身的增删改查与排序。

const (
	QuickCommandPrivate = "PRIVATE"
	QuickCommandShared  = "SHARED"

	MaxQuickCommandsPerGroup = 50
	MaxQuickCommandName      = 100
	MaxQuickCommandPrompt    = 4000
	maxQuickCommandPosition  = 999

	QuickCommandTargetAgent = "AGENT"
	QuickCommandTargetCrew  = "CREW"
)

// 目标不可用的原因（读列表时返回，不报错）。
const (
	QuickCommandTargetArchived     = "ARCHIVED"      // Agent / 小队已归档
	QuickCommandTargetOutsideSpace = "OUTSIDE_SPACE" // 已换到别的空间
	QuickCommandTargetNotVisible   = "NOT_VISIBLE"   // 改成了别人的私有（对当前成员不可见）
	QuickCommandTargetInactive     = "INACTIVE"      // Agent 不是 ACTIVE
	QuickCommandTargetNoMembers    = "NO_MEMBERS"    // 小队没有对当前成员可用的成员
)

var (
	// ErrQuickCommandLimit 表示该分组的快捷指令已达上限。
	ErrQuickCommandLimit = errors.New("conversations: quick command limit reached")
	// ErrQuickCommandTarget 表示目标 Agent / 小队不可用（不区分「不存在」与「他人私有」，不泄露存在性）。
	ErrQuickCommandTarget = errors.New("conversations: quick command target is not available")
)

// QuickCommandTarget 是指令目标的展示信息。不可用时 name / avatar 可能为空（他人私有不泄露名称）。
type QuickCommandTarget struct {
	Kind              string          `json:"kind"`
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Avatar            json.RawMessage `json:"avatar"`
	Available         bool            `json:"available"`
	UnavailableReason string          `json:"unavailable_reason,omitempty"`
}

type QuickCommand struct {
	ID          string             `json:"id"`
	SpaceID     string             `json:"space_id"`
	OwnerUserID string             `json:"owner_user_id"`
	Visibility  string             `json:"visibility"`
	Name        string             `json:"name"`
	IconKind    *string            `json:"icon_kind"`
	Icon        *string            `json:"icon"`
	IconURL     *string            `json:"icon_url"`
	AgentID     *string            `json:"agent_id"`
	CrewID      *string            `json:"crew_id"`
	Target      QuickCommandTarget `json:"target"`
	Prompt      string             `json:"prompt"`
	Position    int                `json:"position"`
	Version     uint64             `json:"version"`
	CanEdit     bool               `json:"can_edit"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type QuickCommandList struct {
	Private []QuickCommand `json:"private"`
	Shared  []QuickCommand `json:"shared"`
}

// QuickCommandInput 新建；AgentID 与 CrewID 恰好一个。IconKind 为空表示没有图标。
type QuickCommandInput struct {
	ID         string
	Visibility string
	Name       string
	IconKind   string
	Icon       string
	AgentID    string
	CrewID     string
	Prompt     string
}

// QuickCommandPatch 修改；nil 表示不改。IconSet 为真时整体替换图标（IconKind 为空串清除）。
// AgentID / CrewID 任一非 nil 即替换目标，此时两者合起来必须恰好一个非空。
type QuickCommandPatch struct {
	Version    uint64
	Visibility *string
	Name       *string
	IconSet    bool
	IconKind   string
	Icon       string
	AgentID    *string
	CrewID     *string
	Prompt     *string
}

// quickCommandAccess 是操作人在空间里的能力：write = 能写空间对象（未停用、非访客），manager = 空间管理员。
type quickCommandAccess struct {
	write   bool
	manager bool
}

// spaceQuickCommandAccess 先要求空间可读（不可见 404），再判断写权限与空间管理员（复用现有空间策略）。
func (s Service) spaceQuickCommandAccess(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string) (quickCommandAccess, error) {
	if !membership.ValidateUUID(spaceID) {
		return quickCommandAccess{}, ErrNotFound
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, spaceID, policy.ActionReadObject); err != nil {
		return quickCommandAccess{}, err
	}
	var access quickCommandAccess
	access.write = s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, spaceID, policy.ActionUpdateObject) == nil
	access.manager = s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, spaceID, policy.ActionManageSpace) == nil
	return access, nil
}

// requireQuickCommandWrite 返回写空间对象的拒绝原因（停用空间 409 SPACE_RETIRED，只读成员 403），沿用现有错误码。
func (s Service) requireQuickCommandWrite(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string, action policy.Action) error {
	return s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, spaceID, action)
}

// canEditQuickCommand：私有只有本人；共享是创建人与空间管理员。两者都要求当前能写空间。
func canEditQuickCommand(visibility, ownerID string, actor Actor, access quickCommandAccess) bool {
	if !access.write {
		return false
	}
	return ownerID == actor.UserID || (visibility == QuickCommandShared && access.manager)
}

func cleanQuickCommandName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxQuickCommandName {
		return "", ErrInvalidRequest
	}
	return name, nil
}

func cleanQuickCommandPrompt(prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if utf8.RuneCountInString(prompt) > MaxQuickCommandPrompt {
		return "", ErrInvalidRequest
	}
	return prompt, nil
}

// normalizeQuickCommandIcon 校验图标，取值与视图图标一致（icon / emoji / upload）；upload 须是本组织已上传的图标。
func normalizeQuickCommandIcon(ctx context.Context, tx pgx.Tx, kind, icon string) (*string, *string, error) {
	switch kind {
	case "":
		return nil, nil, nil
	case "icon", "emoji":
		if n := utf8.RuneCountInString(icon); n < 1 || n > 64 {
			return nil, nil, ErrInvalidRequest
		}
	case "upload":
		if !membership.ValidateUUID(icon) {
			return nil, nil, ErrInvalidRequest
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.uploaded_icons
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid)`, icon).Scan(&exists); err != nil {
			return nil, nil, err
		}
		if !exists {
			return nil, nil, ErrInvalidRequest
		}
	default:
		return nil, nil, ErrInvalidRequest
	}
	return &kind, &icon, nil
}

// quickCommandSelect 读取指令与目标的当前状态（用于计算 available）。$1 固定是操作人 user id。
// crew_usable：小队里至少有一个对操作人可用的成员（规则同 crewAgents）。
const quickCommandSelect = `
	SELECT q.id::text, q.space_id::text, q.owner_user_id::text, q.visibility, q.name, q.icon_kind, q.icon,
	       q.agent_id::text, q.crew_id::text, q.prompt, q.position, q.version, q.created_at, q.updated_at,
	       COALESCE(a.name, c.name, ''), a.avatar,
	       COALESCE(a.space_id, c.space_id)::text, COALESCE(a.visibility, c.visibility, ''),
	       COALESCE(a.owner_user_id::text, c.owner_user_id::text, ''),
	       COALESCE(a.archived_at, c.archived_at) IS NOT NULL, COALESCE(a.status, 'ACTIVE'),
	       (a.id IS NOT NULL OR c.id IS NOT NULL),
	       c.id IS NULL OR EXISTS (
	           SELECT 1 FROM openfox.agent_crew_members m
	             JOIN openfox.agents ma ON ma.organization_id = m.organization_id AND ma.id = m.agent_id
	            WHERE m.organization_id = c.organization_id AND m.crew_id = c.id
	              AND ma.space_id = q.space_id AND ma.status = 'ACTIVE' AND ma.archived_at IS NULL
	              AND (ma.visibility = 'SHARED' OR ma.owner_user_id = $1::uuid))
	  FROM openfox.task_quick_commands q
	  LEFT JOIN openfox.agents a ON a.organization_id = q.organization_id AND a.id = q.agent_id
	  LEFT JOIN openfox.agent_crews c ON c.organization_id = q.organization_id AND c.id = q.crew_id
	 WHERE q.organization_id = openfox_private.current_organization_id() AND q.deleted_at IS NULL`

func scanQuickCommand(row interface{ Scan(...any) error }, organizationID string, actor Actor, access quickCommandAccess) (QuickCommand, error) {
	var item QuickCommand
	var version int64
	var avatar []byte
	var targetSpace *string
	var targetVisibility, targetOwner, targetStatus string
	var archived, found, crewUsable bool
	if err := row.Scan(&item.ID, &item.SpaceID, &item.OwnerUserID, &item.Visibility, &item.Name, &item.IconKind, &item.Icon,
		&item.AgentID, &item.CrewID, &item.Prompt, &item.Position, &version, &item.CreatedAt, &item.UpdatedAt,
		&item.Target.Name, &avatar, &targetSpace, &targetVisibility, &targetOwner, &archived, &targetStatus,
		&found, &crewUsable); err != nil {
		return QuickCommand{}, err
	}
	item.Version = uint64(version)
	item.CanEdit = canEditQuickCommand(item.Visibility, item.OwnerUserID, actor, access)
	if item.IconKind != nil && *item.IconKind == "upload" && item.Icon != nil {
		url := collaboration.IconURL(organizationID, *item.Icon)
		item.IconURL = &url
	}
	if len(avatar) > 0 {
		item.Target.Avatar = json.RawMessage(avatar)
	}
	item.Target.Kind, item.Target.ID = QuickCommandTargetAgent, deref(item.AgentID)
	if item.CrewID != nil {
		item.Target.Kind, item.Target.ID = QuickCommandTargetCrew, *item.CrewID
	}
	reason := quickCommandTargetReason(item, found, deref(targetSpace), targetVisibility, targetOwner, archived, targetStatus, crewUsable, actor)
	item.Target.Available, item.Target.UnavailableReason = reason == "", reason
	if reason == QuickCommandTargetNotVisible {
		// 他人私有的 Agent / 小队：不返回名称与头像。
		item.Target.Name, item.Target.Avatar = "", nil
	}
	return item, nil
}

// quickCommandTargetReason 按操作人视角判断目标是否可用；可用返回空串。
func quickCommandTargetReason(item QuickCommand, found bool, space, visibility, owner string, archived bool, status string, crewUsable bool, actor Actor) string {
	switch {
	case !found || (visibility == "PRIVATE" && owner != actor.UserID):
		return QuickCommandTargetNotVisible
	case archived:
		return QuickCommandTargetArchived
	case space != item.SpaceID:
		return QuickCommandTargetOutsideSpace
	case status != "ACTIVE":
		return QuickCommandTargetInactive
	case item.CrewID != nil && !crewUsable:
		return QuickCommandTargetNoMembers
	}
	return ""
}

// ListQuickCommands 返回空间里本人的私有指令与共享指令（各按 position, created_at, id 排序）。需要空间可读。
func (s Service) ListQuickCommands(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string) (QuickCommandList, error) {
	access, err := s.spaceQuickCommandAccess(ctx, tx, organizationID, actor, spaceID)
	if err != nil {
		return QuickCommandList{}, err
	}
	list := QuickCommandList{Private: []QuickCommand{}, Shared: []QuickCommand{}}
	rows, err := tx.Query(ctx, quickCommandSelect+`
		   AND q.space_id = $2::uuid AND (q.visibility = 'SHARED' OR q.owner_user_id = $1::uuid)
		 ORDER BY q.visibility, q.position, q.created_at, q.id
		 LIMIT $3`, actor.UserID, spaceID, 2*MaxQuickCommandsPerGroup)
	if err != nil {
		return QuickCommandList{}, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanQuickCommand(rows, organizationID, actor, access)
		if err != nil {
			return QuickCommandList{}, err
		}
		if item.Visibility == QuickCommandShared {
			list.Shared = append(list.Shared, item)
		} else {
			list.Private = append(list.Private, item)
		}
	}
	return list, rows.Err()
}

// loadQuickCommand 读取空间里一条对操作人可见的指令（共享，或本人私有）；他人私有、已删除、别的空间一律 ErrNotFound。
func (s Service) loadQuickCommand(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, access quickCommandAccess, spaceID, id string) (QuickCommand, error) {
	if !membership.ValidateUUID(id) {
		return QuickCommand{}, ErrNotFound
	}
	item, err := scanQuickCommand(tx.QueryRow(ctx, quickCommandSelect+`
		   AND q.space_id = $2::uuid AND q.id = $3::uuid AND (q.visibility = 'SHARED' OR q.owner_user_id = $1::uuid)`,
		actor.UserID, spaceID, id), organizationID, actor, access)
	if errors.Is(err, pgx.ErrNoRows) {
		return QuickCommand{}, ErrNotFound
	}
	return item, err
}

// validateQuickCommandTarget：Agent / 小队须在本空间、未归档、共享或本人私有；Agent 不能是内置 Agent，且须 ACTIVE。
// 不满足（含不存在、他人私有）一律 ErrQuickCommandTarget，不区分原因。
func validateQuickCommandTarget(ctx context.Context, tx pgx.Tx, actor Actor, spaceID, agentID, crewID string) error {
	if (agentID == "") == (crewID == "") {
		return ErrQuickCommandTarget
	}
	query := `
		SELECT EXISTS (SELECT 1 FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND space_id = $2::uuid
		   AND archived_at IS NULL AND status = 'ACTIVE' AND builtin_kind IS NULL
		   AND (visibility = 'SHARED' OR owner_user_id = $3::uuid))`
	id := agentID
	if crewID != "" {
		query = `
			SELECT EXISTS (SELECT 1 FROM openfox.agent_crews
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND space_id = $2::uuid
			   AND archived_at IS NULL AND (visibility = 'SHARED' OR owner_user_id = $3::uuid))`
		id = crewID
	}
	if !membership.ValidateUUID(id) {
		return ErrQuickCommandTarget
	}
	var ok bool
	if err := tx.QueryRow(ctx, query, id, spaceID, actor.UserID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrQuickCommandTarget
	}
	return nil
}

// quickCommandGroupKey 标识一个排序 / 计数分组：共享按空间，私有按空间 + 本人。
func quickCommandGroupKey(spaceID, visibility, ownerID string) string {
	key := "task_quick_command:" + spaceID + ":" + visibility
	if visibility == QuickCommandPrivate {
		key += ":" + ownerID
	}
	return key
}

// lockQuickCommandGroups 串行化分组内的写入（计数、排序、改可见性）。多个分组按键排序后加锁，避免互相等待。
func lockQuickCommandGroups(ctx context.Context, tx pgx.Tx, keys ...string) error {
	if len(keys) == 2 && keys[0] > keys[1] {
		keys[0], keys[1] = keys[1], keys[0]
	}
	for index, key := range keys {
		if index > 0 && key == keys[index-1] {
			continue
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(openfox_private.current_organization_id()::text || $1, 0))`, key); err != nil {
			return err
		}
	}
	return nil
}

// quickCommandGroupIDs 返回分组当前的条目 ID（按显示顺序）与每条的创建人。
func quickCommandGroupIDs(ctx context.Context, tx pgx.Tx, spaceID, visibility, ownerID string) ([]string, map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, owner_user_id::text FROM openfox.task_quick_commands
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND visibility = $2 AND deleted_at IS NULL AND ($2 = 'SHARED' OR owner_user_id = $3::uuid)
		 ORDER BY position, created_at, id`, spaceID, visibility, ownerID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids []string
	owners := map[string]string{}
	for rows.Next() {
		var id, owner string
		if err := rows.Scan(&id, &owner); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		owners[id] = owner
	}
	return ids, owners, rows.Err()
}

// renumberQuickCommands 把 ids 依次写成 position 0..n-1（只改变化的行）。
func renumberQuickCommands(ctx context.Context, tx pgx.Tx, ids []string) error {
	for position, id := range ids {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_quick_commands SET position = $2, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND position <> $2`, id, position); err != nil {
			return err
		}
	}
	return nil
}

// nextQuickCommandPosition 在已加锁的分组里检查上限并返回新条目的 position（追加到末尾）。
// 反复新建删除可能让 max(position) 逼近上限 999，此时先把分组重排成 0..n-1。
func nextQuickCommandPosition(ctx context.Context, tx pgx.Tx, spaceID, visibility, ownerID string) (int, error) {
	ids, _, err := quickCommandGroupIDs(ctx, tx, spaceID, visibility, ownerID)
	if err != nil {
		return 0, err
	}
	if len(ids) >= MaxQuickCommandsPerGroup {
		return 0, ErrQuickCommandLimit
	}
	var next int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(max(position) + 1, 0) FROM openfox.task_quick_commands
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND visibility = $2 AND deleted_at IS NULL AND ($2 = 'SHARED' OR owner_user_id = $3::uuid)`,
		spaceID, visibility, ownerID).Scan(&next); err != nil {
		return 0, err
	}
	if next > maxQuickCommandPosition {
		if err := renumberQuickCommands(ctx, tx, ids); err != nil {
			return 0, err
		}
		next = len(ids)
	}
	return next, nil
}

// CreateQuickCommand 新建指令（需要空间写权限）。同一个 id 重复提交且内容相同时返回已有记录，否则 ErrConflict。
func (s Service) CreateQuickCommand(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string, input QuickCommandInput) (QuickCommand, error) {
	access, err := s.spaceQuickCommandAccess(ctx, tx, organizationID, actor, spaceID)
	if err != nil {
		return QuickCommand{}, err
	}
	if err := s.requireQuickCommandWrite(ctx, tx, organizationID, actor, spaceID, policy.ActionCreateObject); err != nil {
		return QuickCommand{}, err
	}
	if !membership.ValidateUUID(input.ID) || (input.Visibility != QuickCommandPrivate && input.Visibility != QuickCommandShared) {
		return QuickCommand{}, ErrInvalidRequest
	}
	name, err := cleanQuickCommandName(input.Name)
	if err != nil {
		return QuickCommand{}, err
	}
	prompt, err := cleanQuickCommandPrompt(input.Prompt)
	if err != nil {
		return QuickCommand{}, err
	}
	iconKind, icon, err := normalizeQuickCommandIcon(ctx, tx, input.IconKind, input.Icon)
	if err != nil {
		return QuickCommand{}, err
	}
	agentID, crewID := strings.TrimSpace(input.AgentID), strings.TrimSpace(input.CrewID)
	if err := validateQuickCommandTarget(ctx, tx, actor, spaceID, agentID, crewID); err != nil {
		return QuickCommand{}, err
	}
	if err := lockQuickCommandGroups(ctx, tx, quickCommandGroupKey(spaceID, input.Visibility, actor.UserID)); err != nil {
		return QuickCommand{}, err
	}
	if existing, err := s.loadQuickCommand(ctx, tx, organizationID, actor, access, spaceID, input.ID); err == nil {
		if existing.OwnerUserID == actor.UserID && existing.Visibility == input.Visibility && existing.Name == name &&
			existing.Prompt == prompt && deref(existing.AgentID) == agentID && deref(existing.CrewID) == crewID &&
			deref(existing.IconKind) == deref(iconKind) && deref(existing.Icon) == deref(icon) {
			return existing, nil
		}
		return QuickCommand{}, ErrConflict
	} else if !errors.Is(err, ErrNotFound) {
		return QuickCommand{}, err
	}
	position, err := nextQuickCommandPosition(ctx, tx, spaceID, input.Visibility, actor.UserID)
	if err != nil {
		return QuickCommand{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_quick_commands (organization_id, id, space_id, owner_user_id, visibility, name,
			icon_kind, icon, agent_id, crew_id, prompt, position)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7,
			NULLIF($8, '')::uuid, NULLIF($9, '')::uuid, $10, $11)`,
		input.ID, spaceID, actor.UserID, input.Visibility, name, iconKind, icon, agentID, crewID, prompt, position); err != nil {
		// 同一 id 已被别人用过（或已删除）：主键冲突映射为 ErrConflict。
		return QuickCommand{}, mapError(err)
	}
	return s.loadQuickCommand(ctx, tx, organizationID, actor, access, spaceID, input.ID)
}

// editableQuickCommand 读取指令并要求操作人能维护它：不可见 404；能读但不能改时先按空间写权限给出原因
// （停用 409 SPACE_RETIRED、只读 403），否则 403。
func (s Service) editableQuickCommand(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, id string, action policy.Action) (QuickCommand, quickCommandAccess, error) {
	access, err := s.spaceQuickCommandAccess(ctx, tx, organizationID, actor, spaceID)
	if err != nil {
		return QuickCommand{}, access, err
	}
	current, err := s.loadQuickCommand(ctx, tx, organizationID, actor, access, spaceID, id)
	if err != nil {
		return QuickCommand{}, access, err
	}
	if err := s.requireQuickCommandWrite(ctx, tx, organizationID, actor, spaceID, action); err != nil {
		return QuickCommand{}, access, err
	}
	if !current.CanEdit {
		return QuickCommand{}, access, ErrForbidden
	}
	return current, access, nil
}

// QuickCommandChange 是修改前后的指令（供审计判断是否涉及共享指令）。
type QuickCommandChange struct {
	Before QuickCommand
	After  QuickCommand
}

// UpdateQuickCommand 修改指令（乐观锁 version）。改可见性会移到另一分组的末尾（受该分组上限约束）；
// 只有目标真的变化时才重新校验目标（管理员改名不会因为目标是创建人的私有 Agent 而失败）。
func (s Service) UpdateQuickCommand(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, id string, patch QuickCommandPatch) (QuickCommandChange, error) {
	current, access, err := s.editableQuickCommand(ctx, tx, organizationID, actor, spaceID, id, policy.ActionUpdateObject)
	if err != nil {
		return QuickCommandChange{}, err
	}
	if patch.Version == 0 || (patch.Visibility == nil && patch.Name == nil && !patch.IconSet && patch.AgentID == nil &&
		patch.CrewID == nil && patch.Prompt == nil) {
		return QuickCommandChange{}, ErrInvalidRequest
	}
	next := current
	if patch.Visibility != nil {
		if *patch.Visibility != QuickCommandPrivate && *patch.Visibility != QuickCommandShared {
			return QuickCommandChange{}, ErrInvalidRequest
		}
		next.Visibility = *patch.Visibility
	}
	if patch.Name != nil {
		if next.Name, err = cleanQuickCommandName(*patch.Name); err != nil {
			return QuickCommandChange{}, err
		}
	}
	if patch.Prompt != nil {
		if next.Prompt, err = cleanQuickCommandPrompt(*patch.Prompt); err != nil {
			return QuickCommandChange{}, err
		}
	}
	if patch.IconSet {
		if next.IconKind, next.Icon, err = normalizeQuickCommandIcon(ctx, tx, patch.IconKind, patch.Icon); err != nil {
			return QuickCommandChange{}, err
		}
	}
	agentID, crewID := deref(current.AgentID), deref(current.CrewID)
	if patch.AgentID != nil || patch.CrewID != nil {
		agentID, crewID = strings.TrimSpace(deref(patch.AgentID)), strings.TrimSpace(deref(patch.CrewID))
		if agentID != deref(current.AgentID) || crewID != deref(current.CrewID) {
			if err := validateQuickCommandTarget(ctx, tx, actor, spaceID, agentID, crewID); err != nil {
				return QuickCommandChange{}, err
			}
		}
	}
	// 当前分组与目标分组都加锁（私有分组属于创建人，不一定是操作人）。
	from := quickCommandGroupKey(spaceID, current.Visibility, current.OwnerUserID)
	to := quickCommandGroupKey(spaceID, next.Visibility, current.OwnerUserID)
	if err := lockQuickCommandGroups(ctx, tx, from, to); err != nil {
		return QuickCommandChange{}, err
	}
	position := current.Position
	if next.Visibility != current.Visibility {
		if position, err = nextQuickCommandPosition(ctx, tx, spaceID, next.Visibility, current.OwnerUserID); err != nil {
			return QuickCommandChange{}, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.task_quick_commands
		   SET visibility = $3, name = $4, icon_kind = $5, icon = $6, agent_id = NULLIF($7, '')::uuid,
		       crew_id = NULLIF($8, '')::uuid, prompt = $9, position = $10, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $2 AND deleted_at IS NULL`,
		id, int64(patch.Version), next.Visibility, next.Name, next.IconKind, next.Icon, agentID, crewID, next.Prompt, position)
	if err != nil {
		return QuickCommandChange{}, mapError(err)
	}
	if tag.RowsAffected() != 1 {
		return QuickCommandChange{}, ErrVersionConflict
	}
	after, err := s.loadQuickCommand(ctx, tx, organizationID, actor, access, spaceID, id)
	if errors.Is(err, ErrNotFound) {
		// 空间管理员把别人的共享指令改成私有：改完后对管理员不可见，按写入值返回（不再可编辑）。
		after = next
		after.AgentID, after.CrewID = optionalString(agentID), optionalString(crewID)
		after.Position, after.Version, after.CanEdit = position, current.Version+1, false
		err = nil
	}
	if err != nil {
		return QuickCommandChange{}, err
	}
	return QuickCommandChange{Before: current, After: after}, nil
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// DeleteQuickCommand 软删除；不可见或已删除 ErrNotFound，无权 ErrForbidden。
func (s Service) DeleteQuickCommand(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, id string) (QuickCommand, error) {
	current, _, err := s.editableQuickCommand(ctx, tx, organizationID, actor, spaceID, id, policy.ActionDeleteObject)
	if err != nil {
		return QuickCommand{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.task_quick_commands SET deleted_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, id)
	if err != nil {
		return QuickCommand{}, err
	}
	if tag.RowsAffected() != 1 {
		return QuickCommand{}, ErrNotFound
	}
	return current, nil
}

// ReorderQuickCommands 按 ids 的顺序重排一个分组里操作人可编辑的条目。ids 必须恰好是这些条目（多、少、重复都返回
// ErrVersionConflict，让前端刷新后重试）。共享分组里操作人不能改的条目保持原来的位置不动，可编辑条目在自己占的位置间换序。
func (s Service) ReorderQuickCommands(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, visibility string, ids []string) ([]QuickCommand, error) {
	access, err := s.spaceQuickCommandAccess(ctx, tx, organizationID, actor, spaceID)
	if err != nil {
		return nil, err
	}
	if (visibility != QuickCommandPrivate && visibility != QuickCommandShared) || len(ids) > MaxQuickCommandsPerGroup {
		return nil, ErrInvalidRequest
	}
	for _, id := range ids {
		if !membership.ValidateUUID(id) {
			return nil, ErrInvalidRequest
		}
	}
	if err := s.requireQuickCommandWrite(ctx, tx, organizationID, actor, spaceID, policy.ActionUpdateObject); err != nil {
		return nil, err
	}
	if err := lockQuickCommandGroups(ctx, tx, quickCommandGroupKey(spaceID, visibility, actor.UserID)); err != nil {
		return nil, err
	}
	order, owners, err := quickCommandGroupIDs(ctx, tx, spaceID, visibility, actor.UserID)
	if err != nil {
		return nil, err
	}
	editable := map[string]bool{}
	for _, id := range order {
		if canEditQuickCommand(visibility, owners[id], actor, access) {
			editable[id] = true
		}
	}
	if len(ids) != len(editable) {
		return nil, ErrVersionConflict
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !editable[id] || seen[id] {
			return nil, ErrVersionConflict
		}
		seen[id] = true
	}
	next := 0
	for index, id := range order {
		if editable[id] {
			order[index] = ids[next]
			next++
		}
	}
	if err := renumberQuickCommands(ctx, tx, order); err != nil {
		return nil, err
	}
	list, err := s.ListQuickCommands(ctx, tx, organizationID, actor, spaceID)
	if err != nil {
		return nil, err
	}
	if visibility == QuickCommandShared {
		return list.Shared, nil
	}
	return list.Private, nil
}
