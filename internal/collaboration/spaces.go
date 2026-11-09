package collaboration

// 「空间设置与生命周期」：空间详情、新建（含复制配置 / 推荐模板）、资料与开关、标识符、个人排序、
// 影响预估、停用 / 恢复、软删除。
//
// 权限由接口层判定（读 = 空间可读，写 = 空间管理）；这里只做数据规则。
// 已删除空间（deleted_at 非空）在所有读取里都按不存在处理。
// 标识符：spaces 与 space_prefix_history 各有 (organization_id, lower(prefix)) 唯一索引，但两张表之间没有约束，
// 因此所有改动标识符的事务先取组织级 advisory lock（spacePrefixLockSQL），再在同一事务里检查两张表。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"openfox/internal/idempotency"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

var (
	// ErrSpaceRetired：已停用空间只读，任务、评论、会话消息、建 Agent 等对象写入一律拒绝。
	ErrSpaceRetired      = NewRuleError(http.StatusConflict, "SPACE_RETIRED", "the space is retired and read-only")
	ErrSpaceNotRetired   = NewRuleError(http.StatusConflict, "SPACE_NOT_RETIRED", "the space is not retired")
	ErrIdentifierTaken   = NewRuleError(http.StatusConflict, "IDENTIFIER_TAKEN", "the space identifier is already in use")
	ErrInvalidIdentifier = NewRuleError(http.StatusBadRequest, "INVALID_IDENTIFIER", "identifier must be 2 to 7 letters or digits")
	ErrNoCanceledStatus  = NewRuleError(http.StatusBadRequest, "NO_CANCELED_STATUS", "a task type has no canceled status in its workflow")
	ErrInvalidTarget     = NewRuleError(http.StatusBadRequest, "INVALID_TARGET_SPACE", "target space is not writable")
	ErrConfirmMismatch   = NewRuleError(http.StatusBadRequest, "CONFIRM_MISMATCH", "confirm_name does not match the space name")
	ErrLastSpace         = NewRuleError(http.StatusConflict, "LAST_SPACE", "the last space of the organization cannot be deleted")
)

const (
	// MaxSpaceNameRunes 是新建 / 修改空间名称的上限。
	MaxSpaceNameRunes = 32
	// MaxSpaceOrder 是个人排序一次提交的空间数上限。
	MaxSpaceOrder = 500
	// maxSpaceList 限制空间列表的结果数。
	maxSpaceList = 1000
	// maxRetireTasks 限制一次停用处理（取消 / 移动）的任务数，超出时拒绝而不是部分处理。
	maxRetireTasks = 5000
	// maxTemplateKeys 与契约 template_keys maxItems 一致。
	maxTemplateKeys = 20
)

// SpacePurposes 是空间用途的全部取值（与 spaces_purpose_valid 约束一致）。
var SpacePurposes = templatePurposeOrder

// spacePrefixLockSQL 串行化同一组织内所有标识符写入（新建空间、修改标识符），见文件头说明。
const spacePrefixLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('openfox.space_prefix:' || openfox_private.current_organization_id()::text, 0))`

// spaceLifecycleLockSQL 串行化同一组织内的空间删除，保证「最后一个空间不能删」在并发下成立。
const spaceLifecycleLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('openfox.space_lifecycle:' || openfox_private.current_organization_id()::text, 0))`

// SpaceFeatures 是空间功能开关。
type SpaceFeatures struct {
	Views      bool `json:"views"`
	Projects   bool `json:"projects"`
	Iterations bool `json:"iterations"`
	Triage     bool `json:"triage"`
}

// SpaceFeaturesPatch 是开关的部分修改；nil 表示不变。
type SpaceFeaturesPatch struct {
	Views      *bool `json:"views"`
	Projects   *bool `json:"projects"`
	Iterations *bool `json:"iterations"`
	Triage     *bool `json:"triage"`
}

func (p *SpaceFeaturesPatch) apply(current SpaceFeatures) SpaceFeatures {
	if p == nil {
		return current
	}
	for _, item := range []struct {
		value  *bool
		target *bool
	}{{p.Views, &current.Views}, {p.Projects, &current.Projects}, {p.Iterations, &current.Iterations}, {p.Triage, &current.Triage}} {
		if item.value != nil {
			*item.target = *item.value
		}
	}
	return current
}

// SpaceEstimation 是空间估时设置。
type SpaceEstimation struct {
	Scale            string `json:"scale"`
	Extended         bool   `json:"extended"`
	AllowZero        bool   `json:"allow_zero"`
	CountUnestimated bool   `json:"count_unestimated"`
}

// SpaceDetail 是契约 SpaceDetail 的领域对象（列表与详情共用）。
type SpaceDetail struct {
	Space
	Icon            *string
	Purpose         string
	Features        SpaceFeatures
	MultiAssignee   bool
	Estimation      SpaceEstimation
	RetiredAt       *time.Time
	DeletedAt       *time.Time
	Position        *int
	MyRole          *string
	TriageFavorited bool
	CanManage       bool
	MemberCount     int
	TaskTypeCount   int
	LabelCount      int
	TaskCount       int
}

// Retired 表示空间已停用（status = ARCHIVED 且未删除）。
func (d SpaceDetail) Retired() bool { return d.Status == "ARCHIVED" && d.DeletedAt == nil }

// IsObjectWrite 判断策略动作是否为空间内对象的写入（停用空间拒绝这类动作）。
func IsObjectWrite(action policy.Action) bool {
	return action == policy.ActionCreateObject || action == policy.ActionUpdateObject || action == policy.ActionDeleteObject
}

// RequireWritable 在空间已停用时返回 ErrSpaceRetired。LoadSpaceAccess 不返回已删除空间，因此 ARCHIVED 即停用。
func (a SpaceAccess) RequireWritable() error {
	if a.Space.Status == "ARCHIVED" {
		return ErrSpaceRetired
	}
	return nil
}

// NormalizeIdentifier 校验新格式标识符（2–7 位字母数字）并转大写。
func NormalizeIdentifier(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) < 2 || len(value) > 7 {
		return "", ErrInvalidIdentifier
	}
	for _, character := range value {
		if !((character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
			return "", ErrInvalidIdentifier
		}
	}
	return value, nil
}

// identifierCandidates 由空间名称生成候选标识符：多个单词取首字母，否则取前几个字母；不足两位时用 SP。
// 候选依次为 基础值、基础值 + 2..99；全部被占用时由调用方回退到随机值。
func identifierCandidates(name string) []string {
	words := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool {
		return !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	})
	base := ""
	if len(words) >= 2 {
		for _, word := range words {
			if len(base) < 4 {
				base += word[:1]
			}
		}
	} else if len(words) == 1 {
		base = words[0]
		if len(base) > 4 {
			base = base[:4]
		}
	}
	if len(base) < 2 {
		base = "SP" + base
		if len(base) > 4 {
			base = base[:4]
		}
	}
	candidates := []string{base}
	for number := 2; number < 100; number++ {
		suffix := fmt.Sprint(number)
		candidates = append(candidates, base+suffix)
	}
	return candidates
}

func randomIdentifier() string {
	return strings.ToUpper("S" + randomPrefixSuffix(6))
}

// identifierOwner 返回占用该标识符（现值或历史值，不区分大小写）的空间 ID 与名称；excludeSpaceID 的空间不计。
// 已删除空间的标识符同样保留（旧编号不再可达，但不复用，避免链接指向别的空间）。
func identifierOwner(ctx context.Context, tx pgx.Tx, prefix, excludeSpaceID string) (string, string, error) {
	var spaceID, name string
	err := tx.QueryRow(ctx, `
		SELECT s.id::text, s.name FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id()
		   AND lower(s.prefix) = lower($1) AND s.id IS DISTINCT FROM NULLIF($2, '')::uuid
		UNION ALL
		SELECT s.id::text, s.name FROM openfox.space_prefix_history h
		  JOIN openfox.spaces s ON s.organization_id = h.organization_id AND s.id = h.space_id
		 WHERE h.organization_id = openfox_private.current_organization_id()
		   AND lower(h.prefix) = lower($1) AND h.space_id IS DISTINCT FROM NULLIF($2, '')::uuid
		 LIMIT 1`, prefix, excludeSpaceID).Scan(&spaceID, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return spaceID, name, err
}

// identifierTakenError 只在冲突空间对操作人可读时给出空间名。
func identifierTakenError(ctx context.Context, tx pgx.Tx, actor Actor, spaceID, name string) error {
	readable, err := spaceReadable(ctx, tx, actor, spaceID)
	if err != nil {
		return err
	}
	if readable {
		return ErrIdentifierTaken.WithDetails(map[string]any{"space_name": name})
	}
	return ErrIdentifierTaken
}

// spaceReadable 按 SpaceReadableSQL 判断当前操作人能否读该空间（含已停用，不含已删除）。
func spaceReadable(ctx context.Context, tx pgx.Tx, actor Actor, spaceID string) (bool, error) {
	var readable bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $2::uuid
		   AND `+SpaceReadableSQL+`)`, strings.ToUpper(string(actor.Role)), spaceID).Scan(&readable)
	return readable, err
}

// IdentifierCheck 对应契约 SpaceIdentifierCheck。
type IdentifierCheck struct {
	Prefix    string  `json:"prefix"`
	Valid     bool    `json:"valid"`
	Available bool    `json:"available"`
	SpaceName *string `json:"space_name"`
}

// CheckIdentifier 预检标识符；spaceID 非空时本空间的现值与历史值视为可用。
func (s Service) CheckIdentifier(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, prefix, spaceID string) (IdentifierCheck, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return IdentifierCheck{}, err
	}
	if spaceID != "" && !membership.ValidateUUID(spaceID) {
		return IdentifierCheck{}, invalidInput("space_id is invalid")
	}
	normalized, err := NormalizeIdentifier(prefix)
	if err != nil {
		return IdentifierCheck{Prefix: strings.ToUpper(strings.TrimSpace(prefix))}, nil
	}
	result := IdentifierCheck{Prefix: normalized, Valid: true}
	ownerID, ownerName, err := identifierOwner(ctx, tx, normalized, spaceID)
	if err != nil {
		return IdentifierCheck{}, err
	}
	if ownerID == "" {
		result.Available = true
		return result, nil
	}
	readable, err := spaceReadable(ctx, tx, actor, ownerID)
	if err != nil {
		return IdentifierCheck{}, err
	}
	if readable {
		result.SpaceName = &ownerName
	}
	return result, nil
}

// spaceDetailSelect 读取空间详情所需的全部列；当前用户的排序与显式成员关系通过 LEFT JOIN 取得。
const spaceDetailSelect = `
	SELECT s.organization_id::text, s.id::text, s.prefix, s.name, s.visibility, s.status, s.version,
	       s.created_at, s.updated_at, s.icon, s.purpose, s.views_enabled, s.projects_enabled,
	       s.iterations_enabled, s.triage_enabled, s.multi_assignee, s.estimation_scale, s.estimation_extended,
	       s.estimation_allow_zero, s.estimation_count_unestimated, s.retired_at, s.deleted_at, o.position,
	       COALESCE(me.role, ''), COALESCE(me.status, ''),
	       EXISTS (SELECT 1 FROM openfox.triage_favorites f WHERE f.organization_id = s.organization_id AND f.space_id = s.id AND f.user_id = openfox_private.current_actor_id()),
	       (SELECT count(*) FROM openfox.space_members m
	         WHERE m.organization_id = s.organization_id AND m.space_id = s.id AND m.status = 'ACTIVE')::int,
	       (SELECT count(*) FROM openfox.task_types tt
	         WHERE tt.organization_id = s.organization_id AND tt.space_id = s.id AND tt.archived_at IS NULL)::int,
	       (SELECT count(*) FROM openfox.labels l
	         WHERE l.organization_id = s.organization_id AND l.space_id = s.id AND l.archived_at IS NULL)::int,
	       (SELECT count(*) FROM openfox.tasks t
	         WHERE t.organization_id = s.organization_id AND t.space_id = s.id AND t.deleted_at IS NULL)::int
	  FROM openfox.spaces s
	  LEFT JOIN openfox.space_user_order o
	    ON o.organization_id = s.organization_id AND o.space_id = s.id
	   AND o.user_id = openfox_private.current_actor_id()
	  LEFT JOIN openfox.space_members me
	    ON me.organization_id = s.organization_id AND me.space_id = s.id
	   AND me.user_id = openfox_private.current_actor_id()
	 WHERE s.organization_id = openfox_private.current_organization_id() AND s.deleted_at IS NULL`

func scanSpaceDetail(row interface{ Scan(...any) error }, actor Actor) (SpaceDetail, error) {
	var item SpaceDetail
	var version int64
	var position *int32
	var memberRole, memberStatus string
	if err := row.Scan(&item.OrganizationID, &item.ID, &item.Prefix, &item.Name, &item.Visibility, &item.Status,
		&version, &item.CreatedAt, &item.UpdatedAt, &item.Icon, &item.Purpose, &item.Features.Views,
		&item.Features.Projects, &item.Features.Iterations, &item.Features.Triage, &item.MultiAssignee,
		&item.Estimation.Scale, &item.Estimation.Extended, &item.Estimation.AllowZero,
		&item.Estimation.CountUnestimated, &item.RetiredAt, &item.DeletedAt, &position, &memberRole, &memberStatus,
		&item.TriageFavorited, &item.MemberCount, &item.TaskTypeCount, &item.LabelCount, &item.TaskCount); err != nil {
		return SpaceDetail{}, err
	}
	var err error
	if item.Version, err = positiveVersion(version); err != nil {
		return SpaceDetail{}, err
	}
	if position != nil {
		value := int(*position)
		item.Position = &value
	}
	activeMember := strings.EqualFold(memberStatus, "ACTIVE")
	if activeMember {
		role := strings.ToUpper(memberRole)
		item.MyRole = &role
	}
	item.CanManage = actor.Role == policy.OrganizationRoleOwner || actor.Role == policy.OrganizationRoleAdmin ||
		(activeMember && (strings.EqualFold(memberRole, "OWNER") || strings.EqualFold(memberRole, "EDITOR")))
	return item, nil
}

// GetSpaceDetail 读取单个空间详情（含当前用户的排序、角色与管理权限）；已删除空间返回 ErrSpaceNotFound。
// 调用方负责读权限判断。
func (s Service) GetSpaceDetail(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) (SpaceDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceDetail{}, err
	}
	if !membership.ValidateUUID(spaceID) {
		return SpaceDetail{}, ErrSpaceNotFound
	}
	item, err := scanSpaceDetail(tx.QueryRow(ctx, spaceDetailSelect+` AND s.id = $1::uuid`, spaceID), actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceDetail{}, ErrSpaceNotFound
	}
	return item, err
}

const (
	SpaceListActive  = "active"
	SpaceListRetired = "retired"
	SpaceListAll     = "all"
)

// ListSpaceDetails 列出当前用户可读的空间（永不包含已删除空间）。
// 排序：有个人 position 的在前按 position，其余按 created_at, id。
func (s Service) ListSpaceDetails(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, status string) ([]SpaceDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	var statusFilter string
	switch status {
	case "", SpaceListActive:
		statusFilter = ` AND s.status = 'ACTIVE'`
	case SpaceListRetired:
		statusFilter = ` AND s.status = 'ARCHIVED'`
	case SpaceListAll:
	default:
		return nil, invalidInput("status must be active, retired or all")
	}
	// spaceDetailSelect 不带参数；SpaceReadableSQL 使用 $1（组织角色）。
	rows, err := tx.Query(ctx, spaceDetailSelect+statusFilter+` AND `+SpaceReadableSQL+`
		 ORDER BY o.position NULLS LAST, s.created_at, s.id
		 LIMIT $2`, strings.ToUpper(string(actor.Role)), maxSpaceList)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]SpaceDetail, 0)
	for rows.Next() {
		item, err := scanSpaceDetail(rows, actor)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func normalizeSpaceName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > MaxSpaceNameRunes {
		return "", invalidInput("name must be 1 to 32 characters")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", invalidInput("name contains control characters")
		}
	}
	return value, nil
}

func normalizePurpose(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "other", nil
	}
	for _, candidate := range SpacePurposes {
		if candidate == value {
			return value, nil
		}
	}
	return "", invalidInput("purpose is invalid")
}

func normalizeIcon(value string) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if !validDisplayToken(value) {
		return nil, invalidInput("icon is invalid")
	}
	if strings.HasPrefix(value, "emoji:") && strings.TrimSpace(strings.TrimPrefix(value, "emoji:")) == "" {
		return nil, invalidInput("icon is invalid")
	}
	return &value, nil
}

// SpaceCreateInput 是新建空间（向导）的输入。
type SpaceCreateInput struct {
	Name            string
	Prefix          string
	Visibility      string
	Purpose         string
	Icon            string
	Features        *SpaceFeaturesPatch
	MultiAssignee   *bool
	CopyFromSpaceID string
	TemplateKeys    []string
	IdempotencyKey  string
}

// CreateSpaceDetail 新建空间：创建者成为空间 OWNER；可从可读空间复制配置，或按推荐模板追加任务类型。
// 只允许组织 OWNER / ADMIN（沿用现有规则）。返回的 bool 为 true 表示幂等重放。
func (s Service) CreateSpaceDetail(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input SpaceCreateInput) (SpaceDetail, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceDetail{}, false, err
	}
	if actor.Role != policy.OrganizationRoleOwner && actor.Role != policy.OrganizationRoleAdmin {
		return SpaceDetail{}, false, ErrActionForbidden
	}
	name, err := normalizeSpaceName(input.Name)
	if err != nil {
		return SpaceDetail{}, false, err
	}
	visibility := "PRIVATE"
	if strings.TrimSpace(input.Visibility) != "" {
		if visibility, err = ParseVisibility(input.Visibility); err != nil {
			return SpaceDetail{}, false, err
		}
	}
	purpose, err := normalizePurpose(input.Purpose)
	if err != nil {
		return SpaceDetail{}, false, err
	}
	icon, err := normalizeIcon(input.Icon)
	if err != nil {
		return SpaceDetail{}, false, err
	}
	if err := requireSpaceIcon(ctx, tx, icon); err != nil {
		return SpaceDetail{}, false, err
	}
	prefix := strings.TrimSpace(input.Prefix)
	if prefix != "" {
		if prefix, err = NormalizeIdentifier(prefix); err != nil {
			return SpaceDetail{}, false, err
		}
	}
	copyFrom := strings.ToLower(strings.TrimSpace(input.CopyFromSpaceID))
	if copyFrom != "" && !membership.ValidateUUID(copyFrom) {
		return SpaceDetail{}, false, ErrSpaceNotFound
	}
	if len(input.TemplateKeys) > maxTemplateKeys {
		return SpaceDetail{}, false, invalidInput("template_keys supports at most 20 keys")
	}
	if copyFrom != "" && len(input.TemplateKeys) > 0 {
		return SpaceDetail{}, false, invalidInput("copy_from_space_id and template_keys cannot be combined")
	}
	templates := make([]TaskTypeTemplate, 0, len(input.TemplateKeys))
	seenTemplates := map[string]bool{}
	for _, key := range input.TemplateKeys {
		template, ok := FindTaskTypeTemplate(strings.TrimSpace(key))
		if !ok {
			return SpaceDetail{}, false, invalidInput("unknown template key")
		}
		if !seenTemplates[template.Key] {
			seenTemplates[template.Key] = true
			templates = append(templates, template)
		}
	}
	features := input.Features.apply(SpaceFeatures{Views: true, Projects: true})
	if features.Triage {
		// 新空间没有任务，只需确认组织有待分拣状态。
		if err := switchTriage(ctx, tx, "", true, actor); err != nil {
			return SpaceDetail{}, false, err
		}
	}
	multiAssignee := true
	if input.MultiAssignee != nil {
		multiAssignee = *input.MultiAssignee
	}
	claim, err := idempotency.Begin(ctx, tx, "space.create", input.IdempotencyKey, struct {
		Name, Prefix, Visibility, Purpose, Icon, CopyFrom string
		Features                                          SpaceFeatures
		MultiAssignee                                     bool
		Templates                                         []string
	}{name, prefix, visibility, purpose, strings.TrimSpace(input.Icon), copyFrom, features, multiAssignee, input.TemplateKeys})
	if err != nil {
		return SpaceDetail{}, false, err
	}
	if claim.Replay {
		item, err := s.GetSpaceDetail(ctx, tx, organizationID, claim.ResourceID, actor)
		return item, true, err
	}
	var source SpaceEstimation
	if copyFrom != "" {
		readable, err := spaceReadable(ctx, tx, actor, copyFrom)
		if err != nil {
			return SpaceDetail{}, false, err
		}
		if !readable {
			return SpaceDetail{}, false, ErrSpaceNotFound
		}
		if source, err = loadSpaceEstimation(ctx, tx, copyFrom, ""); err != nil {
			return SpaceDetail{}, false, err
		}
	} else {
		source = SpaceEstimation{Scale: EstimationNotUsed}
	}
	if _, err := tx.Exec(ctx, spacePrefixLockSQL); err != nil {
		return SpaceDetail{}, false, err
	}
	if prefix != "" {
		ownerID, ownerName, err := identifierOwner(ctx, tx, prefix, "")
		if err != nil {
			return SpaceDetail{}, false, err
		}
		if ownerID != "" {
			return SpaceDetail{}, false, identifierTakenError(ctx, tx, actor, ownerID, ownerName)
		}
	} else {
		for _, candidate := range append(identifierCandidates(name), randomIdentifier(), randomIdentifier(), randomIdentifier()) {
			if len(candidate) > 7 {
				continue
			}
			ownerID, _, err := identifierOwner(ctx, tx, candidate, "")
			if err != nil {
				return SpaceDetail{}, false, err
			}
			if ownerID == "" {
				prefix = candidate
				break
			}
		}
		if prefix == "" {
			return SpaceDetail{}, false, ErrIdentifierTaken
		}
	}
	spaceID, err := membership.NewID()
	if err != nil {
		return SpaceDetail{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.spaces (
			organization_id, id, prefix, name, visibility, icon, purpose, views_enabled, projects_enabled,
			iterations_enabled, triage_enabled, multi_assignee, estimation_scale, estimation_extended,
			estimation_allow_zero, estimation_count_unestimated
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		          $12, $13, $14, $15)`,
		spaceID, prefix, name, visibility, icon, purpose, features.Views, features.Projects, features.Iterations,
		features.Triage, multiAssignee, source.Scale, source.Extended, source.AllowZero, source.CountUnestimated); err != nil {
		if isUniqueViolation(err) {
			return SpaceDetail{}, false, ErrIdentifierTaken
		}
		return SpaceDetail{}, false, mapDatabaseError(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.space_members (organization_id, space_id, user_id, role, status)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'OWNER', 'ACTIVE')`,
		spaceID, actor.UserID); err != nil {
		return SpaceDetail{}, false, mapDatabaseError(err)
	}
	copied := false
	if copyFrom != "" {
		if copied, err = copySpaceConfiguration(ctx, tx, copyFrom, spaceID, actor); err != nil {
			return SpaceDetail{}, false, err
		}
	}
	if !copied {
		if err := seedDefaultTaskType(ctx, tx, spaceID); err != nil {
			return SpaceDetail{}, false, err
		}
	}
	if err := createTemplateTypes(ctx, tx, organizationID, spaceID, templates); err != nil {
		return SpaceDetail{}, false, err
	}
	if err := idempotency.Complete(ctx, tx, claim, spaceID); err != nil {
		return SpaceDetail{}, false, err
	}
	item, err := s.GetSpaceDetail(ctx, tx, organizationID, spaceID, actor)
	return item, false, err
}

// createTemplateTypes 按推荐模板追加任务类型（工作流 = 全部未归档状态，默认第一个 TODO）；与已有类型重名的跳过。
func createTemplateTypes(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, templates []TaskTypeTemplate) error {
	if len(templates) == 0 {
		return nil
	}
	statuses, err := loadStatuses(ctx, tx, organizationID)
	if err != nil {
		return err
	}
	selected, defaultStatus, err := selectWorkflow(statuses, nil, "")
	if errors.Is(err, ErrInvalidWorkflow) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, template := range templates {
		typeID, err := membership.NewID()
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_types (organization_id, space_id, id, name, description, icon, enabled, is_default)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, NULLIF($5, ''), true, false)
			ON CONFLICT DO NOTHING`, spaceID, typeID, template.Name, template.Description, template.Icon)
		if err != nil {
			return mapDatabaseError(err)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		for index, status := range selected {
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.workflow_statuses (
					organization_id, space_id, task_type_id, status_id, position, is_default
				) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5)`,
				spaceID, typeID, status.ID, index, status.ID == defaultStatus); err != nil {
				return mapDatabaseError(err)
			}
		}
	}
	return nil
}

// copySpaceConfiguration 复制任务类型（含工作流与字段绑定）、空间标签分组与空间标签（未归档）；估时设置在插入空间时已复制。
// 不复制任务、成员、仓库。分组名在组织内唯一（label_groups_name_unique），重名时追加序号。
// 返回 false 表示来源没有可复制的任务类型（调用方改为创建默认类型）。
func copySpaceConfiguration(ctx context.Context, tx pgx.Tx, sourceID, targetID string, actor Actor) (bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM openfox.task_types
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND archived_at IS NULL
		 ORDER BY created_at, id`, sourceID)
	if err != nil {
		return false, err
	}
	typeIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		typeIDs = append(typeIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, sourceType := range typeIDs {
		newID, err := membership.NewID()
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_types (organization_id, space_id, id, name, description, icon, enabled, is_default)
			SELECT organization_id, $2::uuid, $3::uuid, name, description, icon, enabled, is_default
			  FROM openfox.task_types
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			sourceType, targetID, newID); err != nil {
			return false, mapDatabaseError(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.workflow_statuses (organization_id, space_id, task_type_id, status_id, position, is_default)
			SELECT ws.organization_id, $2::uuid, $3::uuid, ws.status_id, ws.position, ws.is_default
			  FROM openfox.workflow_statuses ws
			  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
			 WHERE ws.organization_id = openfox_private.current_organization_id() AND ws.task_type_id = $1::uuid
			   AND ts.archived_at IS NULL`, sourceType, targetID, newID); err != nil {
			return false, mapDatabaseError(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.task_type_fields (organization_id, space_id, task_type_id, field_id, position, created_by)
			SELECT f.organization_id, $2::uuid, $3::uuid, f.field_id, f.position, $4::uuid
			  FROM openfox.task_type_fields f
			  JOIN openfox.custom_fields cf ON cf.organization_id = f.organization_id AND cf.id = f.field_id
			 WHERE f.organization_id = openfox_private.current_organization_id() AND f.task_type_id = $1::uuid
			   AND cf.archived_at IS NULL`, sourceType, targetID, newID, actor.UserID); err != nil {
			return false, mapDatabaseError(err)
		}
	}
	if len(typeIDs) > 0 {
		// 来源的默认类型可能已被归档而没复制到：没有默认类型时把第一个启用的类型设为默认。
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types SET is_default = true
			 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
			   AND id = (SELECT id FROM openfox.task_types
			              WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
			                AND enabled ORDER BY created_at, id LIMIT 1)
			   AND NOT EXISTS (SELECT 1 FROM openfox.task_types
			                    WHERE organization_id = openfox_private.current_organization_id()
			                      AND space_id = $1::uuid AND is_default AND archived_at IS NULL)`, targetID); err != nil {
			return false, mapDatabaseError(err)
		}
	}
	groupMap, err := copySpaceLabelGroups(ctx, tx, sourceID, targetID, actor)
	if err != nil {
		return false, err
	}
	labelRows, err := tx.Query(ctx, `
		SELECT name, description, color, group_id::text FROM openfox.labels
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND archived_at IS NULL
		 ORDER BY created_at, id
		 LIMIT $2`, sourceID, MaxLabels)
	if err != nil {
		return false, err
	}
	type labelCopy struct {
		name, description string
		color, groupID    *string
	}
	labels := make([]labelCopy, 0)
	for labelRows.Next() {
		var item labelCopy
		if err := labelRows.Scan(&item.name, &item.description, &item.color, &item.groupID); err != nil {
			labelRows.Close()
			return false, err
		}
		labels = append(labels, item)
	}
	labelRows.Close()
	if err := labelRows.Err(); err != nil {
		return false, err
	}
	for _, label := range labels {
		var groupID *string
		if label.groupID != nil {
			if mapped, ok := groupMap[*label.groupID]; ok {
				groupID = &mapped
			}
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.labels (organization_id, space_id, id, name, description, color, group_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, gen_random_uuid(), $2, $3, $4, $5::uuid)`,
			targetID, label.name, label.description, label.color, groupID); err != nil {
			return false, configDatabaseError(err)
		}
	}
	return len(typeIDs) > 0, nil
}

// copySpaceLabelGroups 复制来源空间的标签分组，返回 旧分组 ID → 新分组 ID。
func copySpaceLabelGroups(ctx context.Context, tx pgx.Tx, sourceID, targetID string, actor Actor) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, name, description, color FROM openfox.label_groups
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		 ORDER BY created_at, id
		 LIMIT $2`, sourceID, MaxLabelGroups)
	if err != nil {
		return nil, err
	}
	type groupCopy struct{ id, name, description, color string }
	groups := make([]groupCopy, 0)
	for rows.Next() {
		var item groupCopy
		if err := rows.Scan(&item.id, &item.name, &item.description, &item.color); err != nil {
			rows.Close()
			return nil, err
		}
		groups = append(groups, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make(map[string]string, len(groups))
	for _, group := range groups {
		newID, err := membership.NewID()
		if err != nil {
			return nil, err
		}
		inserted := false
		for attempt := 1; attempt <= 20 && !inserted; attempt++ {
			name := group.name
			if attempt > 1 {
				suffix := fmt.Sprintf(" %d", attempt)
				runes := []rune(name)
				if len(runes)+len([]rune(suffix)) > 50 {
					runes = runes[:50-len([]rune(suffix))]
				}
				name = string(runes) + suffix
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO openfox.label_groups (organization_id, id, name, description, color, created_by, space_id)
				VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5::uuid, $6::uuid)
				ON CONFLICT DO NOTHING`, newID, name, group.description, group.color, actor.UserID, targetID)
			if err != nil {
				return nil, configDatabaseError(err)
			}
			inserted = tag.RowsAffected() == 1
		}
		if inserted {
			result[group.id] = newID
		}
	}
	return result, nil
}

// lockLiveSpace 锁定未删除空间并校验版本。
func lockLiveSpace(ctx context.Context, tx pgx.Tx, spaceID string, version uint64) (Space, *time.Time, error) {
	if !membership.ValidateUUID(spaceID) {
		return Space{}, nil, ErrSpaceNotFound
	}
	if version == 0 {
		return Space{}, nil, ErrInvalidVersion
	}
	var space Space
	var currentVersion int64
	var retiredAt *time.Time
	err := tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, prefix, name, visibility, status, version, created_at, updated_at, retired_at
		  FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL
		   FOR UPDATE`, spaceID).Scan(&space.OrganizationID, &space.ID, &space.Prefix, &space.Name, &space.Visibility,
		&space.Status, &currentVersion, &space.CreatedAt, &space.UpdatedAt, &retiredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Space{}, nil, ErrSpaceNotFound
	}
	if err != nil {
		return Space{}, nil, err
	}
	if uint64(currentVersion) != version {
		return Space{}, nil, ErrVersionConflict
	}
	space.Version = uint64(currentVersion)
	return space, retiredAt, nil
}

// SpacePatch 是 PATCH /spaces/{s} 的输入。
type SpacePatch struct {
	Version       uint64
	Name          *string
	Icon          OptionalString
	Visibility    *string
	Purpose       *string
	Features      *SpaceFeaturesPatch
	MultiAssignee *bool
}

// UpdateSpaceSettings 修改空间资料与功能开关（乐观锁）；已停用空间也可以修改。
func (s Service) UpdateSpaceSettings(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, patch SpacePatch) (SpaceDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceDetail{}, err
	}
	if _, _, err := lockLiveSpace(ctx, tx, spaceID, patch.Version); err != nil {
		return SpaceDetail{}, err
	}
	current, err := s.GetSpaceDetail(ctx, tx, organizationID, spaceID, actor)
	if err != nil {
		return SpaceDetail{}, err
	}
	name := current.Name
	if patch.Name != nil {
		if name, err = normalizeSpaceName(*patch.Name); err != nil {
			return SpaceDetail{}, err
		}
	}
	icon := current.Icon
	if patch.Icon.Set {
		icon = nil
		if patch.Icon.Value != nil {
			if icon, err = normalizeIcon(*patch.Icon.Value); err != nil {
				return SpaceDetail{}, err
			}
		}
		if err := requireSpaceIcon(ctx, tx, icon); err != nil {
			return SpaceDetail{}, err
		}
	}
	visibility := current.Visibility
	if patch.Visibility != nil {
		if visibility, err = ParseVisibility(*patch.Visibility); err != nil {
			return SpaceDetail{}, err
		}
	}
	purpose := current.Purpose
	if patch.Purpose != nil {
		if strings.TrimSpace(*patch.Purpose) == "" {
			return SpaceDetail{}, invalidInput("purpose is invalid")
		}
		if purpose, err = normalizePurpose(*patch.Purpose); err != nil {
			return SpaceDetail{}, err
		}
	}
	features := patch.Features.apply(current.Features)
	if features.Triage != current.Features.Triage {
		if err := switchTriage(ctx, tx, spaceID, features.Triage, actor); err != nil {
			return SpaceDetail{}, err
		}
	}
	multiAssignee := current.MultiAssignee
	if patch.MultiAssignee != nil {
		multiAssignee = *patch.MultiAssignee
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.spaces
		   SET name = $2, icon = $3, visibility = $4, purpose = $5, views_enabled = $6, projects_enabled = $7,
		       iterations_enabled = $8, triage_enabled = $9, multi_assignee = $10,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $11 AND deleted_at IS NULL`,
		spaceID, name, icon, visibility, purpose, features.Views, features.Projects, features.Iterations,
		features.Triage, multiAssignee, int64(patch.Version))
	if err != nil {
		return SpaceDetail{}, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return SpaceDetail{}, ErrVersionConflict
	}
	return s.GetSpaceDetail(ctx, tx, organizationID, spaceID, actor)
}

// ChangeIdentifier 修改空间标识符：旧值写入历史（同一空间已有该历史值时保留原行），新值不能被其他空间的现值或历史值占用。
// 改回本空间自己的旧值是允许的（历史行保留，见规格 §4）。仅大小写不同时不写历史。
func (s Service) ChangeIdentifier(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, version uint64, prefix string) (SpaceDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceDetail{}, err
	}
	normalized, err := NormalizeIdentifier(prefix)
	if err != nil {
		return SpaceDetail{}, err
	}
	if _, err := tx.Exec(ctx, spacePrefixLockSQL); err != nil {
		return SpaceDetail{}, err
	}
	space, _, err := lockLiveSpace(ctx, tx, spaceID, version)
	if err != nil {
		return SpaceDetail{}, err
	}
	if space.Prefix == normalized {
		return s.GetSpaceDetail(ctx, tx, organizationID, spaceID, actor)
	}
	ownerID, ownerName, err := identifierOwner(ctx, tx, normalized, spaceID)
	if err != nil {
		return SpaceDetail{}, err
	}
	if ownerID != "" {
		return SpaceDetail{}, identifierTakenError(ctx, tx, actor, ownerID, ownerName)
	}
	if !strings.EqualFold(space.Prefix, normalized) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.space_prefix_history (organization_id, space_id, prefix, replaced_by)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3::uuid)
			ON CONFLICT DO NOTHING`, spaceID, space.Prefix, actor.UserID); err != nil {
			return SpaceDetail{}, mapDatabaseError(err)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.spaces SET prefix = $2, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $3 AND deleted_at IS NULL`, spaceID, normalized, int64(version))
	if err != nil {
		if isUniqueViolation(err) {
			return SpaceDetail{}, ErrIdentifierTaken
		}
		return SpaceDetail{}, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return SpaceDetail{}, ErrVersionConflict
	}
	return s.GetSpaceDetail(ctx, tx, organizationID, spaceID, actor)
}

// PutSpaceOrder 整体替换当前用户的侧栏空间顺序；不可读或已删除的 ID → 400。
func (s Service) PutSpaceOrder(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceIDs []string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if len(spaceIDs) > MaxSpaceOrder {
		return invalidInput("space_ids supports at most 500 ids")
	}
	ids := make([]string, 0, len(spaceIDs))
	seen := make(map[string]bool, len(spaceIDs))
	for _, raw := range spaceIDs {
		id := strings.ToLower(strings.TrimSpace(raw))
		if !membership.ValidateUUID(id) || seen[id] {
			return invalidInput("space_ids must be unique space ids")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	var readable int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = ANY($2::uuid[])
		   AND `+SpaceReadableSQL, strings.ToUpper(string(actor.Role)), ids).Scan(&readable); err != nil {
		return err
	}
	if readable != len(ids) {
		return invalidInput("space_ids contains a space that is not available")
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.space_user_order
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND user_id = openfox_private.current_actor_id() AND NOT (space_id = ANY($1::uuid[]))`, ids); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	positions := make([]int32, len(ids))
	for index := range ids {
		positions[index] = int32(index)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.space_user_order (organization_id, user_id, space_id, position)
		SELECT openfox_private.current_organization_id(), openfox_private.current_actor_id(), o.space_id, o.position
		  FROM unnest($1::uuid[], $2::int[]) AS o(space_id, position)
		ON CONFLICT (organization_id, user_id, space_id)
		DO UPDATE SET position = EXCLUDED.position, updated_at = clock_timestamp()`, ids, positions)
	return mapDatabaseError(err)
}

// SpaceImpact 对应契约 SpaceImpact。
type SpaceImpact struct {
	OpenTasks   int `json:"open_tasks"`
	TotalTasks  int `json:"total_tasks"`
	Projects    int `json:"projects"`
	ActiveRuns  int `json:"active_runs"`
	Agents      int `json:"agents"`
	SpaceGrants int `json:"space_grants"`
}

// openTaskSQL 是「未完成任务」的条件（t 为任务别名）。
const openTaskSQL = `t.deleted_at IS NULL AND t.status NOT IN ('DONE', 'CANCELLED', 'DUPLICATE')`

// GetSpaceImpact 统计停用 / 删除前的影响（调用方负责空间管理权限）。
func (s Service) GetSpaceImpact(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) (SpaceImpact, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceImpact{}, err
	}
	var impact SpaceImpact
	err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM openfox.tasks t
		         WHERE t.organization_id = s.organization_id AND t.space_id = s.id AND `+openTaskSQL+`)::int,
		       (SELECT count(*) FROM openfox.tasks t
		         WHERE t.organization_id = s.organization_id AND t.space_id = s.id AND t.deleted_at IS NULL)::int,
		       (SELECT count(*) FROM openfox.runs r
		         WHERE r.organization_id = s.organization_id AND r.space_id = s.id
		           AND r.state IN ('QUEUED', 'PREPARING', 'RUNNING'))::int,
		       (SELECT count(*) FROM openfox.agents a
		         WHERE a.organization_id = s.organization_id AND a.space_id = s.id AND a.archived_at IS NULL)::int,
		       (SELECT count(*) FROM openfox.device_grants g
		         WHERE g.organization_id = s.organization_id AND g.scope = 'SPACE' AND g.scope_space_id = s.id
		           AND g.status = 'ACTIVE')::int
		  FROM openfox.spaces s
		 WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $1::uuid
		   AND s.deleted_at IS NULL`, spaceID).Scan(&impact.OpenTasks, &impact.TotalTasks, &impact.ActiveRuns,
		&impact.Agents, &impact.SpaceGrants)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceImpact{}, ErrSpaceNotFound
	}
	return impact, err
}

const (
	RetireCancel = "cancel"
	RetireKeep   = "keep"
	RetireMove   = "move"
)

// RetireInput 是停用空间的输入。
type RetireInput struct {
	Version       uint64
	OpenTasks     string
	TargetSpaceID string
	RequestID     string
}

// RetirePlan 是停用前的校验结果：空间已锁定、参数已校验。调用方随后取消 Run，再调用 ApplyRetire。
type RetirePlan struct {
	SpaceID string
	Input   RetireInput
}

// PrepareRetire 锁定空间、校验版本与处置方式。move 的目标空间写权限由调用方在此之前校验。
func (s Service) PrepareRetire(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input RetireInput) (RetirePlan, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return RetirePlan{}, err
	}
	switch input.OpenTasks {
	case RetireCancel, RetireKeep:
		if input.TargetSpaceID != "" {
			return RetirePlan{}, invalidInput("target_space_id is only used with move")
		}
	case RetireMove:
		input.TargetSpaceID = strings.ToLower(strings.TrimSpace(input.TargetSpaceID))
		if !membership.ValidateUUID(input.TargetSpaceID) || input.TargetSpaceID == strings.ToLower(spaceID) {
			return RetirePlan{}, ErrInvalidTarget
		}
	default:
		return RetirePlan{}, invalidInput("open_tasks must be cancel, keep or move")
	}
	// move 时两个空间行按 id 升序加锁，与单任务移动（PrepareMoveTask）一致，避免反方向的移动互相等待。
	targetFirst := input.OpenTasks == RetireMove && input.TargetSpaceID < strings.ToLower(spaceID)
	if targetFirst {
		if err := lockSpaceRow(ctx, tx, input.TargetSpaceID); err != nil {
			return RetirePlan{}, err
		}
	}
	space, _, err := lockLiveSpace(ctx, tx, spaceID, input.Version)
	if err != nil {
		return RetirePlan{}, err
	}
	if space.Status == "ARCHIVED" {
		return RetirePlan{}, ErrSpaceRetired
	}
	if input.OpenTasks == RetireMove && !targetFirst {
		if err := lockSpaceRow(ctx, tx, input.TargetSpaceID); err != nil {
			return RetirePlan{}, err
		}
	}
	return RetirePlan{SpaceID: space.ID, Input: input}, nil
}

// lockSpaceRow 以 FOR UPDATE 锁一个空间行（不存在时不报错：调用方随后的校验会给出具体错误）。
func lockSpaceRow(ctx context.Context, tx pgx.Tx, spaceID string) error {
	_, err := tx.Exec(ctx, `
		SELECT 1 FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   FOR UPDATE`, spaceID)
	return err
}

// RetireResult 汇总停用的影响。
type RetireResult struct {
	CanceledTasks int
	MovedTasks    int
}

// ApplyRetire 处理未完成任务并把空间设为停用（status = ARCHIVED）。须在 PrepareRetire 之后、同一事务内调用。
func (s Service) ApplyRetire(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, plan RetirePlan) (RetireResult, error) {
	var result RetireResult
	var err error
	switch plan.Input.OpenTasks {
	case RetireCancel:
		result.CanceledTasks, err = cancelOpenTasks(ctx, tx, plan.SpaceID, actor, plan.Input.RequestID)
	case RetireMove:
		result.MovedTasks, err = moveOpenTasks(ctx, tx, plan.SpaceID, plan.Input.TargetSpaceID)
	}
	if err != nil {
		return RetireResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.spaces
		   SET status = 'ARCHIVED', retired_at = clock_timestamp(), retired_by = $2::uuid,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $3 AND status = 'ACTIVE' AND deleted_at IS NULL`,
		plan.SpaceID, actor.UserID, int64(plan.Input.Version))
	if err != nil {
		return RetireResult{}, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return RetireResult{}, ErrVersionConflict
	}
	return result, nil
}

// openTask 是停用处理需要的未完成任务信息。
type openTask struct {
	ID, TypeID, TypeName, Category, ParentID string
	StatusID                                 string
	Sequence                                 int64
}

func loadOpenTasks(ctx context.Context, tx pgx.Tx, spaceID string) ([]openTask, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.type_id::text, tt.name, t.status, COALESCE(t.parent_task_id::text, ''), t.status_id::text,
		       t.sequence
		  FROM openfox.tasks t
		  JOIN openfox.task_types tt ON tt.organization_id = t.organization_id AND tt.id = t.type_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
		   AND `+openTaskSQL+`
		 ORDER BY t.sequence
		 LIMIT $2`, spaceID, maxRetireTasks+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]openTask, 0)
	for rows.Next() {
		var item openTask
		if err := rows.Scan(&item.ID, &item.TypeID, &item.TypeName, &item.Category, &item.ParentID, &item.StatusID,
			&item.Sequence); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) > maxRetireTasks {
		return nil, NewRuleError(http.StatusConflict, "TOO_MANY_TASKS", "too many open tasks to process at once")
	}
	return items, nil
}

// cancelOpenTasks 把未完成任务改为各自类型工作流里的取消状态（CANCELLED 分类中 position 最小的）；
// 任一类型没有取消状态时整体拒绝。每个任务写一条 task.status_changed 动态。
func cancelOpenTasks(ctx context.Context, tx pgx.Tx, spaceID string, actor Actor, requestID string) (int, error) {
	tasks, err := loadOpenTasks(ctx, tx, spaceID)
	if err != nil || len(tasks) == 0 {
		return 0, err
	}
	canceled := map[string]string{}
	for _, task := range tasks {
		if _, ok := canceled[task.TypeID]; ok {
			continue
		}
		var statusID string
		err := tx.QueryRow(ctx, `
			SELECT ws.status_id::text
			  FROM openfox.workflow_statuses ws
			  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
			 WHERE ws.organization_id = openfox_private.current_organization_id() AND ws.task_type_id = $1::uuid
			   AND ts.category = 'CANCELLED' AND ts.archived_at IS NULL
			 ORDER BY ws.position, ts.position, ts.id
			 LIMIT 1`, task.TypeID).Scan(&statusID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNoCanceledStatus
		}
		if err != nil {
			return 0, err
		}
		canceled[task.TypeID] = statusID
	}
	ids := make([]string, len(tasks))
	statuses := make([]string, len(tasks))
	for index, task := range tasks {
		ids[index] = task.ID
		statuses[index] = canceled[task.TypeID]
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.tasks t
		   SET status_id = m.status_id, version = t.version + 1, updated_at = clock_timestamp()
		  FROM unnest($2::uuid[], $3::uuid[]) AS m(task_id, status_id)
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
		   AND t.id = m.task_id AND t.deleted_at IS NULL`, spaceID, ids, statuses)
	if err != nil {
		return 0, mapDatabaseError(err)
	}
	if int(tag.RowsAffected()) != len(tasks) {
		return 0, ErrConflict
	}
	diffs := make([]string, len(tasks))
	sources := make([]string, len(tasks))
	for index, task := range tasks {
		payload, err := json.Marshal(map[string]any{"from_status_id": task.StatusID, "to_status_id": statuses[index],
			"reason": "space_retired"})
		if err != nil {
			return 0, err
		}
		diffs[index] = string(payload)
		sources[index] = requestID + ":retire:" + task.ID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id)
		SELECT openfox_private.current_organization_id(), $1::uuid, m.task_id, gen_random_uuid(), $2::uuid,
		       'task.status_changed', m.diff::jsonb, m.source_id
		  FROM unnest($3::uuid[], $4::text[], $5::text[]) AS m(task_id, diff, source_id)`,
		spaceID, actor.UserID, ids, diffs, sources); err != nil {
		return 0, mapDatabaseError(err)
	}
	return len(tasks), nil
}

// targetType 是目标空间的一个可用任务类型及其工作流。
type targetType struct {
	ID, Name        string
	IsDefault       bool
	DefaultStatusID string
	// byCategory：分类 → 该分类中 position 最小的状态。
	byCategory map[string]string
	// statuses：工作流里的全部状态（单任务移动时先试同一状态）。
	statuses map[string]bool
}

// mapStatus 把一个状态映射到本类型的工作流：keepSame 时先保留同一状态，再取同分类中 position 最小的，
// 都没有时用工作流默认状态。移动任务（mapTask）与恢复已归档类型的任务共用。
func (k targetType) mapStatus(statusID, category string, keepSame bool) string {
	switch {
	case keepSame && k.statuses[statusID]:
		return statusID
	case k.byCategory[category] != "":
		return k.byCategory[category]
	default:
		return k.DefaultStatusID
	}
}

func loadTargetTypes(ctx context.Context, tx pgx.Tx, spaceID string) ([]targetType, error) {
	rows, err := tx.Query(ctx, `
		SELECT tt.id::text, tt.name, tt.is_default, ws.status_id::text, ts.category, ws.is_default
		  FROM openfox.task_types tt
		  JOIN openfox.workflow_statuses ws ON ws.organization_id = tt.organization_id AND ws.task_type_id = tt.id
		  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
		 WHERE tt.organization_id = openfox_private.current_organization_id() AND tt.space_id = $1::uuid
		   AND tt.enabled AND tt.archived_at IS NULL AND ts.archived_at IS NULL
		 ORDER BY tt.is_default DESC, tt.created_at, tt.id, ws.position, ts.position`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]targetType, 0)
	index := map[string]int{}
	for rows.Next() {
		var typeID, name, statusID, category string
		var typeDefault, statusDefault bool
		if err := rows.Scan(&typeID, &name, &typeDefault, &statusID, &category, &statusDefault); err != nil {
			return nil, err
		}
		position, ok := index[typeID]
		if !ok {
			items = append(items, targetType{ID: typeID, Name: name, IsDefault: typeDefault, byCategory: map[string]string{},
				statuses: map[string]bool{}})
			position = len(items) - 1
			index[typeID] = position
		}
		item := &items[position]
		item.statuses[statusID] = true
		if _, exists := item.byCategory[category]; !exists {
			item.byCategory[category] = statusID
		}
		if statusDefault {
			item.DefaultStatusID = statusID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	usable := items[:0]
	for _, item := range items {
		if item.DefaultStatusID != "" {
			usable = append(usable, item)
		}
	}
	return usable, nil
}

// moveOpenTasks 把未完成任务移到目标空间（见契约 retireSpace 的说明）。规则与单任务移动共用 moveTasks（task_move.go），
// 但不做单任务移动特有的字段映射与别名：状态按分类映射，估时 / 专项 / 分拣台字段保持原值，旧编号不保留。
func moveOpenTasks(ctx context.Context, tx pgx.Tx, sourceID, targetID string) (int, error) {
	tasks, err := loadOpenMovingTasks(ctx, tx, sourceID)
	if err != nil || len(tasks) == 0 {
		return 0, err
	}
	if _, err := moveTasks(ctx, tx, moveTasksInput{SourceID: sourceID, TargetID: targetID, Tasks: tasks}); err != nil {
		return 0, err
	}
	return len(tasks), nil
}

// RestoreSpace 恢复已停用空间为 ACTIVE（不恢复被取消的任务）。
func (s Service) RestoreSpace(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, version uint64) (SpaceDetail, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SpaceDetail{}, err
	}
	space, _, err := lockLiveSpace(ctx, tx, spaceID, version)
	if err != nil {
		return SpaceDetail{}, err
	}
	if space.Status != "ARCHIVED" {
		return SpaceDetail{}, ErrSpaceNotRetired
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.spaces
		   SET status = 'ACTIVE', retired_at = NULL, retired_by = NULL, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $2 AND deleted_at IS NULL`, spaceID, int64(version))
	if err != nil {
		return SpaceDetail{}, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return SpaceDetail{}, ErrVersionConflict
	}
	return s.GetSpaceDetail(ctx, tx, organizationID, spaceID, actor)
}

// PrepareDelete 锁定空间、校验版本与确认名，并检查组织里还有其他未删除空间。
// 组织级 advisory lock 串行化并发删除，保证不会把最后两个空间同时删掉。
func (s Service) PrepareDelete(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, version uint64, confirmName string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, spaceLifecycleLockSQL); err != nil {
		return err
	}
	space, _, err := lockLiveSpace(ctx, tx, spaceID, version)
	if err != nil {
		return err
	}
	if confirmName != space.Name {
		return ErrConfirmMismatch
	}
	var others int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.spaces
		 WHERE organization_id = openfox_private.current_organization_id() AND id <> $1::uuid AND deleted_at IS NULL`,
		spaceID).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastSpace
	}
	return nil
}

// DeleteResult 汇总软删除的连带处理。
type DeleteResult struct {
	ArchivedAgents int
	RevokedGrants  int
}

// ApplyDelete 在 PrepareDelete 与取消 Run 之后执行：归档该空间未归档的 Agent、吊销 SPACE 范围电脑授权、写 deleted_at。
// agentIDs 由调用方先查出（ListLiveAgentIDs）并已取消其 Run。
func (s Service) ApplyDelete(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, version uint64, agentIDs []string) (DeleteResult, error) {
	var result DeleteResult
	if len(agentIDs) > 0 {
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.agents
			   SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
			   AND id = ANY($2::uuid[]) AND archived_at IS NULL`, spaceID, agentIDs)
		if err != nil {
			return DeleteResult{}, err
		}
		result.ArchivedAgents = int(tag.RowsAffected())
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.device_grants
		   SET status = 'REVOKED', permission_version = permission_version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND scope = 'SPACE' AND scope_space_id = $1::uuid AND status = 'ACTIVE'`, spaceID)
	if err != nil {
		return DeleteResult{}, err
	}
	result.RevokedGrants = int(tag.RowsAffected())
	tag, err = tx.Exec(ctx, `
		UPDATE openfox.spaces
		   SET deleted_at = clock_timestamp(), deleted_by = $2::uuid, status = 'ARCHIVED',
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND version = $3 AND deleted_at IS NULL`, spaceID, actor.UserID, int64(version))
	if err != nil {
		return DeleteResult{}, mapDatabaseError(err)
	}
	if tag.RowsAffected() != 1 {
		return DeleteResult{}, ErrVersionConflict
	}
	return result, nil
}

// ListLiveAgentIDs 返回空间里未归档的 Agent（按 id 排序，供删除时先取消 Run 再归档）。
func ListLiveAgentIDs(ctx context.Context, tx pgx.Tx, spaceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text FROM openfox.agents
		 WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid
		   AND archived_at IS NULL
		 ORDER BY id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SpaceGrantDevices 返回指向该空间的 ACTIVE SPACE 范围授权的电脑（删除后用于审计）。
func SpaceGrantDevices(ctx context.Context, tx pgx.Tx, spaceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT device_id::text FROM openfox.device_grants
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND scope = 'SPACE' AND scope_space_id = $1::uuid AND status = 'ACTIVE'
		 ORDER BY device_id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, rows.Err()
}
