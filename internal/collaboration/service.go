// Package collaboration contains the Phase 0 space, workflow, and task
// application boundary. It assumes the caller has already established a
// trusted tenant transaction; it never treats request identifiers as proof of
// authorization.
package collaboration

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

var (
	ErrInvalidCursor       = errors.New("collaboration: invalid cursor")
	ErrInvalidPageSize     = errors.New("collaboration: invalid page size")
	ErrInvalidPrefix       = errors.New("collaboration: invalid space prefix")
	ErrInvalidVisibility   = errors.New("collaboration: invalid visibility")
	ErrInvalidCategory     = errors.New("collaboration: invalid status category")
	ErrInvalidPriority     = errors.New("collaboration: invalid priority")
	ErrInvalidName         = errors.New("collaboration: invalid name")
	ErrInvalidTask         = errors.New("collaboration: invalid task")
	ErrInvalidVersion      = errors.New("collaboration: invalid version")
	ErrInvalidStatus       = errors.New("collaboration: invalid status")
	ErrInvalidWorkflow     = errors.New("collaboration: invalid workflow")
	ErrSpaceNotFound       = errors.New("collaboration: space not found")
	ErrSpaceMemberNotFound = errors.New("collaboration: space member not found")
	ErrTaskNotFound        = errors.New("collaboration: task not found")
	ErrTaskTypeNotFound    = errors.New("collaboration: task type not found")
	ErrStatusNotFound      = errors.New("collaboration: status not found")
	ErrLabelNotFound       = errors.New("collaboration: label not found")
	ErrConflict            = errors.New("collaboration: conflict")
	ErrVersionConflict     = errors.New("collaboration: version conflict")
	ErrStatusInUse         = errors.New("collaboration: status is in use")
	ErrStatusRequired      = errors.New("collaboration: status is required for its category")
	ErrActionForbidden     = errors.New("collaboration: action forbidden")
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 100
)

type Actor struct {
	UserID               string
	Role                 policy.OrganizationRole
	AuthorizationVersion uint64
}

type Space struct {
	OrganizationID string
	ID             string
	Prefix         string
	Name           string
	Visibility     string
	Status         string
	Version        uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type SpaceMember struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type SpaceAccess struct {
	Space      Space
	Membership *SpaceMemberAccess
}

type SpaceMemberAccess struct {
	UserID string
	Role   string
	Status string
}

type TaskStatus struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Category    string     `json:"category"`
	Position    int        `json:"position"`
	Color       string     `json:"color,omitempty"`
	Icon        string     `json:"icon,omitempty"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
	Version     uint64     `json:"version"`
	CreatedAt   time.Time  `json:"created_at"`
	// 以下字段只在状态列表 / 详情视图（statuses_ext.go）中填充。
	IsDefault          bool              `json:"is_default"`
	CollapsedByDefault bool              `json:"collapsed_by_default"`
	TaskCount          int64             `json:"task_count"`
	TypeUsage          []StatusTypeUsage `json:"type_usage"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

type WorkflowStatus struct {
	StatusID string `json:"status_id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Position int    `json:"position"`
	Default  bool   `json:"default"`
}

type TaskType struct {
	OrganizationID string           `json:"organization_id"`
	SpaceID        string           `json:"space_id"`
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Description    string           `json:"description"`
	Icon           string           `json:"icon,omitempty"`
	Enabled        bool             `json:"enabled"`
	IsDefault      bool             `json:"is_default"`
	ArchivedAt     *time.Time       `json:"archived_at,omitempty"`
	Version        uint64           `json:"version"`
	CreatedAt      time.Time        `json:"created_at"`
	Statuses       []WorkflowStatus `json:"statuses"`
	// FieldCount / TaskCount：绑定的未归档字段数、未删除任务数。
	FieldCount int `json:"field_count"`
	TaskCount  int `json:"task_count"`
}

type Task struct {
	OrganizationID   string
	SpaceID          string
	SpacePrefix      string
	ID               string
	DisplayID        string
	Sequence         int64
	TypeID           string
	TypeName         string
	StatusID         string
	StatusName       string
	StatusCategory   string
	Title            string
	Description      string
	Acceptance       string
	AssigneeUserID   string
	ExecutorAgentID  string
	Priority         string
	Version          uint64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Labels           []string
	StartDate        string // YYYY-MM-DD，空串表示未设置
	DueDate          string
	ParentTaskID     string
	Parent           *TaskParent
	Rank             string // 有效排序键（旧任务按 sequence 推导）
	Assignees        []TaskAssignee
	LabelItems       []TaskLabelItem
	SubtaskCount     int
	SubtaskDoneCount int
	CreatedBy        string
	CreatedByEmail   string
	TypeIcon         string
	StatusColor      string
	Estimate         *float64 // 估时；nil = 未估时
	IterationID      string
	IterationNumber  int
	ProjectID        string
	ProjectName      string
	// 分拣台状态：TriageEnteredAt 非空表示任务在分拣台里。
	TriageEnteredAt    *time.Time
	TriageSource       string
	TriageSnoozedUntil *time.Time
	DuplicateOfID      string
	DuplicateOf        *TaskParent // 仅单个任务详情填充
	// ExternalAssignee 是导入时创建的外部账号处理人；没有时为 nil。
	ExternalAssignee *TaskExternalAssignee
	// Favorited 表示当前用户是否收藏了该任务（个人偏好）。
	Favorited bool
}

// TaskExternalAssignee 对应契约 TaskExternalAssignee。
type TaskExternalAssignee struct {
	ID                string `json:"id"`
	Provider          string `json:"provider"`
	Name              string `json:"name"`
	Email             string `json:"email"`
	PendingInvitation bool   `json:"pending_invitation"`
}

type Activity struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	ActorID   string          `json:"actor_id"`
	Kind      string          `json:"kind"`
	SafeDiff  json.RawMessage `json:"safe_diff"`
	SourceID  string          `json:"source_id"`
	CreatedAt time.Time       `json:"created_at"`
}

type Cursor struct {
	CreatedAt time.Time
	ID        string
}

type Page[T any] struct {
	Items      []T
	NextCursor string
}

type SpaceMemberInput struct {
	Role   string
	Status string
}

type TaskStatusUpdateInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Position    *int    `json:"position"`
	Color       *string `json:"color"`
	Icon        *string `json:"icon"`
	Archived    *bool   `json:"archived"`
	Version     uint64  `json:"version"`
	// CollapsedByDefault 控制列表视图是否默认折叠该状态分组。
	CollapsedByDefault *bool `json:"collapsed_by_default"`
}

type TaskStatusInput struct {
	Name        string
	Description string
	Category    string
	// Position 为空时追加到所在类别末尾。
	Position       *int
	Color          string
	Icon           string
	IdempotencyKey string
}

type TaskTypeInput struct {
	Name            string
	Description     string
	Icon            string
	Enabled         *bool
	Default         bool
	StatusIDs       []string
	DefaultStatusID string
}

type TaskTypeUpdateInput struct {
	Name            *string
	Description     *string
	Icon            *string
	Enabled         *bool
	Default         *bool
	Archived        *bool
	StatusIDs       *[]string
	DefaultStatusID *string
	Version         uint64
}

func ParsePageSize(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return DefaultPageSize, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 1 || parsed > MaxPageSize {
		return 0, ErrInvalidPageSize
	}
	return parsed, nil
}

func EncodeCursor(cursor Cursor) (string, error) {
	if cursor.CreatedAt.IsZero() || !membership.ValidateUUID(cursor.ID) {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(struct {
		Version   int       `json:"v"`
		CreatedAt time.Time `json:"created_at"`
		ID        string    `json:"id"`
	}{Version: 1, CreatedAt: cursor.CreatedAt.UTC(), ID: strings.ToLower(cursor.ID)})
	if err != nil {
		return "", ErrInvalidCursor
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func ParseCursor(value string) (Cursor, error) {
	if strings.TrimSpace(value) == "" || len(value) > 512 {
		return Cursor{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var decoded struct {
		Version   int       `json:"v"`
		CreatedAt time.Time `json:"created_at"`
		ID        string    `json:"id"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil || decoded.Version != 1 || decoded.CreatedAt.IsZero() || !membership.ValidateUUID(decoded.ID) {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{CreatedAt: decoded.CreatedAt.UTC(), ID: strings.ToLower(decoded.ID)}, nil
}

func NormalizePrefix(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 2 || len(value) > 32 {
		return "", ErrInvalidPrefix
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			if index == 0 && (character < 'a' || character > 'z') {
				return "", ErrInvalidPrefix
			}
			continue
		}
		return "", ErrInvalidPrefix
	}
	return value, nil
}

func DefaultPrefix(name string) string {
	var builder strings.Builder
	lastSeparator := false
	for _, character := range strings.ToLower(strings.TrimSpace(name)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			builder.WriteRune(character)
			lastSeparator = false
			continue
		}
		if !lastSeparator && builder.Len() > 0 {
			builder.WriteByte('-')
			lastSeparator = true
		}
	}
	value := strings.Trim(builder.String(), "-")
	if len(value) > 24 {
		value = strings.TrimRight(value[:24], "-")
	}
	// 名称中没有可用的 ASCII 字符（例如纯中文名）时，回退为 "sp" + 随机后缀，
	// 避免同一组织内多个此类空间都生成 "space" 而撞上唯一约束。
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		return "sp" + randomPrefixSuffix(4) + value
	}
	if len(value) < 2 {
		value += "-1"
	}
	return value
}

func randomPrefixSuffix(length int) string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return strings.Repeat("x", length)
	}
	for index := range bytes {
		bytes[index] = alphabet[int(bytes[index])%len(alphabet)]
	}
	return string(bytes)
}

func ParseVisibility(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "PRIVATE":
		return "PRIVATE", nil
	case "SHARED", "PUBLIC":
		return "SHARED", nil
	default:
		return "", ErrInvalidVisibility
	}
}

func ParseCategory(value string) (string, error) {
	value = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(value), "-", "_"), " ", "_"))
	switch value {
	case "TRIAGE", "BACKLOG", "TODO", "IN_PROGRESS", "DONE", "CANCELLED", "DUPLICATE":
		return value, nil
	case "CANCELED":
		return "CANCELLED", nil
	default:
		return "", ErrInvalidCategory
	}
}

func ParsePriority(value string) (string, error) {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "NONE", "LOWEST", "LOW", "NORMAL", "HIGH", "URGENT":
		return value, nil
	default:
		return "", ErrInvalidPriority
	}
}

func IsTerminalCategory(category string) bool {
	category, _ = ParseCategory(category)
	return category == "DONE" || category == "CANCELLED" || category == "DUPLICATE"
}

func (Service) CheckActor(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) error {
	return (membership.Service{}).CheckActor(ctx, tx, organizationID, membership.ActorSnapshot{
		UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion,
	})
}

// LoadSpaceAccess returns the space and the authenticated actor's explicit
// space membership. Organization membership is checked separately through
// CheckActor at the service operation boundary.
func (s Service) LoadSpaceAccess(ctx context.Context, tx pgx.Tx, organizationID, spaceID, userID string) (SpaceAccess, error) {
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(userID) {
		return SpaceAccess{}, ErrSpaceNotFound
	}
	var access SpaceAccess
	var version int64
	var memberID, memberRole, memberStatus string
	err := tx.QueryRow(ctx, `
		SELECT s.organization_id::text, s.id::text, s.prefix, s.name, s.visibility, s.status,
		       s.version, s.created_at, s.updated_at,
		       COALESCE(sm.user_id::text, ''), COALESCE(sm.role, ''), COALESCE(sm.status, '')
		FROM openfox.spaces s
		LEFT JOIN openfox.space_members sm
		  ON sm.organization_id = s.organization_id AND sm.space_id = s.id AND sm.user_id = $2::uuid
		WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $1::uuid
		  AND s.deleted_at IS NULL
	`, spaceID, userID).Scan(&access.Space.OrganizationID, &access.Space.ID, &access.Space.Prefix,
		&access.Space.Name, &access.Space.Visibility, &access.Space.Status, &version,
		&access.Space.CreatedAt, &access.Space.UpdatedAt, &memberID, &memberRole, &memberStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpaceAccess{}, ErrSpaceNotFound
	}
	if err != nil {
		return SpaceAccess{}, err
	}
	access.Space.Version, err = positiveVersion(version)
	if err != nil {
		return SpaceAccess{}, err
	}
	if memberID != "" {
		access.Membership = &SpaceMemberAccess{UserID: memberID, Role: memberRole, Status: memberStatus}
	}
	return access, nil
}

type Service struct {
	Now func() time.Time
}

// seedDefaultTaskType gives a new space the same default "Task" type that
// migration 000007 backfilled for existing spaces: every active organization
// status in position order, defaulting to the first TODO status. Without it a
// freshly created space cannot accept tasks. Organizations without a usable
// TODO status are left untyped rather than failing space creation.
func seedDefaultTaskType(ctx context.Context, tx pgx.Tx, spaceID string) error {
	statuses, err := loadStatuses(ctx, tx, "")
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
	typeID, err := membership.NewID()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_types (organization_id, space_id, id, name, description, enabled, is_default)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'Task', '', true, true)
	`, spaceID, typeID); err != nil {
		return mapDatabaseError(err)
	}
	for index, status := range selected {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.workflow_statuses (
				organization_id, space_id, task_type_id, status_id, position, is_default
			) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5)
		`, spaceID, typeID, status.ID, index, status.ID == defaultStatus); err != nil {
			return mapDatabaseError(err)
		}
	}
	return nil
}

func (s Service) UpdateStatus(ctx context.Context, tx pgx.Tx, organizationID, statusID string, actor Actor, input TaskStatusUpdateInput) (TaskStatus, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskStatus{}, err
	}
	if !membership.ValidateUUID(statusID) || input.Version == 0 {
		return TaskStatus{}, ErrInvalidVersion
	}
	if input.Name != nil && (strings.TrimSpace(*input.Name) == "" || len([]rune(strings.TrimSpace(*input.Name))) > 100) {
		return TaskStatus{}, ErrInvalidName
	}
	if input.Position != nil && *input.Position < 0 {
		return TaskStatus{}, ErrInvalidStatus
	}
	if input.Description != nil && len([]rune(*input.Description)) > 1000 {
		return TaskStatus{}, ErrInvalidStatus
	}
	if input.Color != nil && !validDisplayToken(*input.Color) {
		return TaskStatus{}, ErrInvalidStatus
	}
	if input.Icon != nil && !validDisplayToken(*input.Icon) {
		return TaskStatus{}, ErrInvalidStatus
	}
	if input.Archived != nil && *input.Archived {
		var lockedID string
		if err := tx.QueryRow(ctx, `
			SELECT id::text FROM openfox.task_statuses
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
			FOR UPDATE
		`, statusID).Scan(&lockedID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return TaskStatus{}, ErrStatusNotFound
			}
			return TaskStatus{}, err
		}
		var organizationDefault bool
		if err := tx.QueryRow(ctx, `
			SELECT is_default FROM openfox.task_statuses
			WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		`, statusID).Scan(&organizationDefault); err != nil {
			return TaskStatus{}, err
		}
		if organizationDefault {
			return TaskStatus{}, ErrStatusDefaultCategory
		}
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.workflow_statuses
			WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid
			  AND is_default
		`, statusID).Scan(&count); err != nil {
			return TaskStatus{}, err
		}
		if count > 0 {
			return TaskStatus{}, ErrStatusInUse
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types tt
			   SET version = version + 1
			 WHERE tt.organization_id = openfox_private.current_organization_id()
			   AND EXISTS (
				   SELECT 1 FROM openfox.workflow_statuses ws
				   WHERE ws.organization_id = tt.organization_id
				     AND ws.task_type_id = tt.id AND ws.status_id = $1::uuid
			   )
		`, statusID); err != nil {
			return TaskStatus{}, err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.workflow_statuses
			WHERE organization_id = openfox_private.current_organization_id() AND status_id = $1::uuid
		`, statusID); err != nil {
			return TaskStatus{}, err
		}
	}
	var current TaskStatus
	var currentVersion int64
	if err := tx.QueryRow(ctx, `
		SELECT id::text, name, description, category, position, COALESCE(color, ''),
		       COALESCE(icon, ''), archived_at, version, created_at
		FROM openfox.task_statuses
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		FOR UPDATE
	`, statusID).Scan(&current.ID, &current.Name, &current.Description, &current.Category, &current.Position,
		&current.Color, &current.Icon, &current.ArchivedAt, &currentVersion, &current.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TaskStatus{}, ErrStatusNotFound
		}
		return TaskStatus{}, err
	}
	if uint64(currentVersion) != input.Version {
		return TaskStatus{}, ErrVersionConflict
	}
	if input.Archived != nil && *input.Archived && current.ArchivedAt == nil {
		var activeInCategory int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.task_statuses
			WHERE organization_id = openfox_private.current_organization_id()
			  AND category = $1 AND archived_at IS NULL
		`, current.Category).Scan(&activeInCategory); err != nil {
			return TaskStatus{}, err
		}
		if activeInCategory <= 1 {
			return TaskStatus{}, ErrStatusRequired
		}
	}
	name := current.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	description := current.Description
	if input.Description != nil {
		description = *input.Description
	}
	position := current.Position
	if input.Position != nil {
		position = *input.Position
	}
	color := current.Color
	if input.Color != nil {
		color = strings.TrimSpace(*input.Color)
	}
	icon := current.Icon
	if input.Icon != nil {
		icon = strings.TrimSpace(*input.Icon)
	}
	archivedAt := current.ArchivedAt
	if input.Archived != nil {
		if *input.Archived {
			now := time.Now().UTC()
			archivedAt = &now
		} else {
			archivedAt = nil
		}
	}
	var updated TaskStatus
	var version int64
	err := tx.QueryRow(ctx, `
		UPDATE openfox.task_statuses
		   SET name = $1, description = $2, position = $3,
		       color = NULLIF($4, ''), icon = NULLIF($5, ''), archived_at = $6,
		       collapsed_by_default = COALESCE($9, collapsed_by_default),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $7::uuid AND version = $8
		 RETURNING id::text, name, description, category, position, COALESCE(color, ''),
		           COALESCE(icon, ''), archived_at, version, created_at
	`, name, description, position, color, icon, archivedAt, statusID, int64(input.Version), input.CollapsedByDefault).Scan(
		&updated.ID, &updated.Name, &updated.Description, &updated.Category, &updated.Position,
		&updated.Color, &updated.Icon, &updated.ArchivedAt, &version, &updated.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskStatus{}, ErrVersionConflict
	}
	if err != nil {
		// 重名映射为 409 NAME_TAKEN，与其他全局配置一致。
		return TaskStatus{}, configDatabaseError(err)
	}
	updated.Version, err = positiveVersion(version)
	if err != nil {
		return TaskStatus{}, err
	}
	// 返回带默认标记、任务数和类型使用情况的完整视图。
	return s.statusView(ctx, tx, actor, updated.ID)
}

func (s Service) ListTaskTypes(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor) ([]TaskType, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if !membership.ValidateUUID(spaceID) {
		return nil, ErrSpaceNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT tt.id::text, tt.name, tt.description, COALESCE(tt.icon, ''),
		       tt.enabled, tt.is_default, tt.archived_at, tt.version, tt.created_at,
		       COALESCE(ws.status_id::text, ''), COALESCE(ts.name, ''),
		       COALESCE(ts.category, ''), COALESCE(ws.position, 0), COALESCE(ws.is_default, false),
		       (SELECT count(*) FROM openfox.task_type_fields f
		          JOIN openfox.custom_fields cf ON cf.organization_id = f.organization_id AND cf.id = f.field_id
		         WHERE f.organization_id = tt.organization_id AND f.task_type_id = tt.id AND cf.archived_at IS NULL)::int,
		       (SELECT count(*) FROM openfox.tasks t
		         WHERE t.organization_id = tt.organization_id AND t.type_id = tt.id AND t.deleted_at IS NULL)::int
		FROM openfox.task_types tt
		LEFT JOIN openfox.workflow_statuses ws
	  ON ws.organization_id = tt.organization_id AND ws.task_type_id = tt.id
		LEFT JOIN openfox.task_statuses ts
	  ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
		WHERE tt.organization_id = openfox_private.current_organization_id()
		  AND tt.space_id = $1::uuid
		ORDER BY tt.created_at, tt.id, ws.position, ws.status_id
	`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type indexEntry struct{ index int }
	indexes := make(map[string]indexEntry)
	items := make([]TaskType, 0)
	for rows.Next() {
		var item TaskType
		var version int64
		var statusID, statusName, category string
		var position int
		var isDefault bool
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.Icon, &item.Enabled, &item.IsDefault,
			&item.ArchivedAt, &version, &item.CreatedAt,
			&statusID, &statusName, &category, &position, &isDefault, &item.FieldCount, &item.TaskCount); err != nil {
			return nil, err
		}
		item.OrganizationID = organizationID
		item.SpaceID = spaceID
		item.Version, err = positiveVersion(version)
		if err != nil {
			return nil, err
		}
		entry, exists := indexes[item.ID]
		if !exists {
			item.Statuses = make([]WorkflowStatus, 0)
			items = append(items, item)
			entry = indexEntry{index: len(items) - 1}
			indexes[item.ID] = entry
		}
		if statusID != "" {
			items[entry.index].Statuses = append(items[entry.index].Statuses, WorkflowStatus{
				StatusID: statusID, Name: statusName, Category: category,
				Position: position, Default: isDefault,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrSpaceNotFound
	}
	return items, nil
}

func (s Service) CreateTaskType(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, actor Actor, input TaskTypeInput) (TaskType, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskType{}, err
	}
	if strings.TrimSpace(input.Name) == "" || len([]rune(strings.TrimSpace(input.Name))) > 100 {
		return TaskType{}, ErrInvalidName
	}
	if len([]rune(input.Description)) > 1000 || !validDisplayToken(input.Icon) {
		return TaskType{}, ErrInvalidStatus
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.Default && !enabled {
		return TaskType{}, ErrInvalidWorkflow
	}
	statuses, err := loadStatuses(ctx, tx, organizationID)
	if err != nil {
		return TaskType{}, err
	}
	selected, defaultStatus, err := selectWorkflow(statuses, input.StatusIDs, input.DefaultStatusID)
	if err != nil {
		return TaskType{}, err
	}
	typeID, err := membership.NewID()
	if err != nil {
		return TaskType{}, err
	}
	var item TaskType
	var version int64
	if input.Default {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types
			   SET is_default = false, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND space_id = $1::uuid AND is_default AND archived_at IS NULL
		`, spaceID); err != nil {
			return TaskType{}, err
		}
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.task_types (
			organization_id, space_id, id, name, description, icon, enabled, is_default
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4,
		          NULLIF($5, ''), $6, $7)
		RETURNING organization_id::text, space_id::text, id::text, name, description,
		          COALESCE(icon, ''), enabled, is_default, archived_at, version, created_at
	`, spaceID, typeID, strings.TrimSpace(input.Name), input.Description, input.Icon, enabled, input.Default).Scan(
		&item.OrganizationID, &item.SpaceID, &item.ID, &item.Name, &item.Description, &item.Icon,
		&item.Enabled, &item.IsDefault, &item.ArchivedAt, &version, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskType{}, ErrTaskTypeNotFound
	}
	if err != nil {
		return TaskType{}, mapDatabaseError(err)
	}
	item.Version, err = positiveVersion(version)
	if err != nil {
		return TaskType{}, err
	}
	for index, status := range selected {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.workflow_statuses (
				organization_id, space_id, task_type_id, status_id, position, is_default
			) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5)
		`, spaceID, typeID, status.ID, index, status.ID == defaultStatus); err != nil {
			return TaskType{}, mapDatabaseError(err)
		}
		item.Statuses = append(item.Statuses, WorkflowStatus{StatusID: status.ID, Name: status.Name,
			Category: status.Category, Position: index, Default: status.ID == defaultStatus})
	}
	return item, nil
}

func (s Service) UpdateTaskType(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID string, actor Actor, input TaskTypeUpdateInput) (TaskType, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskType{}, err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(typeID) || input.Version == 0 {
		return TaskType{}, ErrInvalidVersion
	}
	var item TaskType
	var version int64
	if err := tx.QueryRow(ctx, `
		SELECT organization_id::text, space_id::text, id::text, name, description,
		       COALESCE(icon, ''), enabled, is_default, archived_at, version, created_at
		FROM openfox.task_types
		WHERE organization_id = openfox_private.current_organization_id()
		  AND space_id = $1::uuid AND id = $2::uuid
		FOR UPDATE
	`, spaceID, typeID).Scan(&item.OrganizationID, &item.SpaceID, &item.ID, &item.Name, &item.Description,
		&item.Icon, &item.Enabled, &item.IsDefault, &item.ArchivedAt, &version, &item.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TaskType{}, ErrTaskTypeNotFound
		}
		return TaskType{}, err
	}
	if uint64(version) != input.Version {
		return TaskType{}, ErrVersionConflict
	}
	if input.Name != nil && (strings.TrimSpace(*input.Name) == "" || len([]rune(strings.TrimSpace(*input.Name))) > 100) {
		return TaskType{}, ErrInvalidName
	}
	if input.Description != nil && len([]rune(*input.Description)) > 1000 {
		return TaskType{}, ErrInvalidStatus
	}
	if input.Icon != nil && !validDisplayToken(*input.Icon) {
		return TaskType{}, ErrInvalidStatus
	}
	enabled := item.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	isDefault := item.IsDefault
	if input.Default != nil {
		isDefault = *input.Default
	}
	// 默认类型不能停用或归档：返回 409 DEFAULT_TYPE_REQUIRED；需先把另一个类型设为默认。
	if item.IsDefault && (input.Default == nil || *input.Default) &&
		((input.Enabled != nil && !*input.Enabled) || (input.Archived != nil && *input.Archived)) {
		return TaskType{}, ErrDefaultTypeRequired
	}
	if input.Archived != nil && *input.Archived {
		isDefault = false
	}
	if isDefault && !enabled {
		return TaskType{}, ErrInvalidWorkflow
	}
	if !isDefault && item.IsDefault {
		var otherDefault bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM openfox.task_types
				WHERE organization_id = openfox_private.current_organization_id()
				  AND space_id = $1::uuid AND id <> $2::uuid
				  AND is_default AND enabled AND archived_at IS NULL
			)
		`, spaceID, typeID).Scan(&otherDefault); err != nil {
			return TaskType{}, err
		}
		if !otherDefault {
			return TaskType{}, ErrDefaultTypeRequired
		}
	}
	var selected []TaskStatus
	var defaultStatus string
	if input.StatusIDs != nil {
		statuses, err := loadStatuses(ctx, tx, organizationID)
		if err != nil {
			return TaskType{}, err
		}
		selected, defaultStatus, err = selectWorkflow(statuses, *input.StatusIDs, valueOrEmpty(input.DefaultStatusID))
		if err != nil {
			return TaskType{}, err
		}
		selectedIDs := make([]string, 0, len(selected))
		for _, status := range selected {
			selectedIDs = append(selectedIDs, status.ID)
		}
		var inUse int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.tasks t
			WHERE t.organization_id = openfox_private.current_organization_id()
			  AND t.space_id = $1::uuid AND t.type_id = $2::uuid
			  AND NOT (t.status_id = ANY($3::uuid[]))
		`, spaceID, typeID, selectedIDs).Scan(&inUse); err != nil {
			return TaskType{}, err
		}
		if inUse > 0 {
			return TaskType{}, ErrStatusInUse
		}
	}
	if input.Archived != nil && *input.Archived {
		var inUse int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM openfox.tasks
			WHERE organization_id = openfox_private.current_organization_id() AND type_id = $1::uuid
		`, typeID).Scan(&inUse); err != nil {
			return TaskType{}, err
		}
		if inUse > 0 {
			return TaskType{}, ErrStatusInUse
		}
	}
	if input.StatusIDs != nil {
		if _, err := tx.Exec(ctx, `
			DELETE FROM openfox.workflow_statuses
			WHERE organization_id = openfox_private.current_organization_id()
			  AND task_type_id = $1::uuid
		`, typeID); err != nil {
			return TaskType{}, err
		}
		for index, status := range selected {
			if _, err := tx.Exec(ctx, `
				INSERT INTO openfox.workflow_statuses (
					organization_id, space_id, task_type_id, status_id, position, is_default
				) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5)
			`, spaceID, typeID, status.ID, index, status.ID == defaultStatus); err != nil {
				return TaskType{}, mapDatabaseError(err)
			}
		}
	}
	name := item.Name
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	description := item.Description
	if input.Description != nil {
		description = *input.Description
	}
	icon := item.Icon
	if input.Icon != nil {
		icon = strings.TrimSpace(*input.Icon)
	}
	archivedAt := item.ArchivedAt
	if input.Archived != nil {
		if *input.Archived {
			now := time.Now().UTC()
			archivedAt = &now
		} else {
			archivedAt = nil
		}
	}
	if isDefault {
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.task_types
			   SET is_default = false, version = version + 1
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND space_id = $1::uuid AND id <> $2::uuid AND is_default AND archived_at IS NULL
		`, spaceID, typeID); err != nil {
			return TaskType{}, err
		}
	}
	err := tx.QueryRow(ctx, `
		UPDATE openfox.task_types
		   SET name = $1, description = $2, icon = NULLIF($3, ''), enabled = $4,
		       is_default = $5, archived_at = $6, version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $7::uuid AND id = $8::uuid AND version = $9
		 RETURNING organization_id::text, space_id::text, id::text, name, description,
		           COALESCE(icon, ''), enabled, is_default, archived_at, version, created_at
	`, name, description, icon, enabled, isDefault, archivedAt, spaceID, typeID, int64(input.Version)).Scan(
		&item.OrganizationID, &item.SpaceID, &item.ID, &item.Name, &item.Description, &item.Icon,
		&item.Enabled, &item.IsDefault, &item.ArchivedAt, &version, &item.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskType{}, ErrVersionConflict
	}
	if err != nil {
		return TaskType{}, mapDatabaseError(err)
	}
	item.Version, err = positiveVersion(version)
	if err != nil {
		return TaskType{}, err
	}
	items, err := s.ListTaskTypes(ctx, tx, organizationID, spaceID, actor)
	if err != nil {
		return TaskType{}, err
	}
	for _, candidate := range items {
		if candidate.ID == typeID {
			candidate.Version = item.Version
			candidate.Name = item.Name
			candidate.Description = item.Description
			candidate.Icon = item.Icon
			candidate.Enabled = item.Enabled
			candidate.IsDefault = item.IsDefault
			candidate.ArchivedAt = item.ArchivedAt
			return candidate, nil
		}
	}
	return item, nil
}

func (s Service) ListActivities(ctx context.Context, tx pgx.Tx, organizationID, taskID string, actor Actor, limit int, cursor *Cursor) (Page[Activity], error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Page[Activity]{}, err
	}
	if !membership.ValidateUUID(taskID) || limit < 1 || limit > MaxPageSize {
		return Page[Activity]{}, ErrInvalidPageSize
	}
	args := []any{taskID, limit + 1}
	condition := "organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid"
	if cursor != nil {
		condition += " AND (created_at, id) > ($3::timestamptz, $4::uuid)"
		args = append(args, cursor.CreatedAt, cursor.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, task_id::text, actor_id::text, kind, safe_diff, source_id, created_at
		FROM openfox.task_activities
		WHERE `+condition+`
		ORDER BY created_at, id
		LIMIT $2`, args...)
	if err != nil {
		return Page[Activity]{}, err
	}
	defer rows.Close()
	page := Page[Activity]{Items: make([]Activity, 0, limit)}
	for rows.Next() {
		var item Activity
		if err := rows.Scan(&item.ID, &item.TaskID, &item.ActorID, &item.Kind, &item.SafeDiff, &item.SourceID, &item.CreatedAt); err != nil {
			return Page[Activity]{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[Activity]{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.Items = page.Items[:limit]
		page.NextCursor, err = EncodeCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		if err != nil {
			return Page[Activity]{}, err
		}
	}
	return page, nil
}

func (s Service) RecordActivity(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, kind, sourceID string, safeDiff map[string]any) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) || strings.TrimSpace(sourceID) == "" {
		return ErrInvalidTask
	}
	if kind == "" || len(kind) > 64 || !validActivityKind(kind) {
		return ErrInvalidTask
	}
	payload, err := json.Marshal(safeDiff)
	if err != nil {
		return ErrInvalidTask
	}
	activityID, err := membership.NewID()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.task_activities (
			organization_id, space_id, task_id, id, actor_id, kind, safe_diff, source_id
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid,
		          $4::uuid, $5, $6::jsonb, $7)
	`, spaceID, taskID, activityID, actor.UserID, kind, payload, sourceID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func (s Service) loadSpace(ctx context.Context, tx pgx.Tx, organizationID, spaceID, userID string, forUpdate bool) (Space, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return Space{}, err
	}
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF s"
	}
	var space Space
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT s.organization_id::text, s.id::text, s.prefix, s.name, s.visibility, s.status,
		       s.version, s.created_at, s.updated_at
		FROM openfox.spaces s
		WHERE s.organization_id = openfox_private.current_organization_id()
		  AND s.id = $1::uuid AND s.deleted_at IS NULL`+lock, spaceID).Scan(&space.OrganizationID, &space.ID, &space.Prefix,
		&space.Name, &space.Visibility, &space.Status, &version, &space.CreatedAt, &space.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Space{}, ErrSpaceNotFound
	}
	if err != nil {
		return Space{}, err
	}
	space.Version, err = positiveVersion(version)
	return space, err
}

func nextTaskSequence(ctx context.Context, tx pgx.Tx, organizationID, spaceID string) (int64, error) {
	if err := tenancy.RequireOrganization(ctx, organizationID); err != nil {
		return 0, err
	}
	var sequence int64
	err := tx.QueryRow(ctx, `
		UPDATE openfox.spaces
		   SET next_task_number = next_task_number + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		RETURNING next_task_number - 1
	`, spaceID).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrSpaceNotFound
	}
	if err != nil {
		return 0, err
	}
	return sequence, nil
}

type workflow struct {
	defaultStatusID string
	statusIDs       map[string]bool
}

func (w workflow) contains(statusID string) bool { return w.statusIDs[statusID] }

func loadWorkflow(ctx context.Context, tx pgx.Tx, organizationID, spaceID, typeID string) (workflow, error) {
	rows, err := tx.Query(ctx, `
		SELECT ws.status_id::text, ws.is_default
		FROM openfox.workflow_statuses ws
		JOIN openfox.task_types tt ON tt.organization_id = ws.organization_id AND tt.id = ws.task_type_id
		JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
		WHERE ws.organization_id = openfox_private.current_organization_id()
		  AND ws.space_id = $1::uuid AND ws.task_type_id = $2::uuid
		  AND tt.enabled AND tt.archived_at IS NULL AND ts.archived_at IS NULL
	`, spaceID, typeID)
	if err != nil {
		return workflow{}, err
	}
	defer rows.Close()
	result := workflow{statusIDs: make(map[string]bool)}
	for rows.Next() {
		var statusID string
		var isDefault bool
		if err := rows.Scan(&statusID, &isDefault); err != nil {
			return workflow{}, err
		}
		result.statusIDs[statusID] = true
		if isDefault {
			result.defaultStatusID = statusID
		}
	}
	if err := rows.Err(); err != nil {
		return workflow{}, err
	}
	if len(result.statusIDs) == 0 || result.defaultStatusID == "" {
		return workflow{}, ErrTaskTypeNotFound
	}
	return result, nil
}

func defaultTypeID(ctx context.Context, tx pgx.Tx, organizationID, spaceID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.task_types
		WHERE organization_id = openfox_private.current_organization_id()
		  AND space_id = $1::uuid AND enabled AND archived_at IS NULL
		ORDER BY is_default DESC, created_at, id LIMIT 1
	`, spaceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTaskTypeNotFound
	}
	return id, err
}

func loadStatuses(ctx context.Context, tx pgx.Tx, organizationID string) ([]TaskStatus, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, name, description, category, position, COALESCE(color, ''),
		       COALESCE(icon, ''), archived_at, version, created_at
		FROM openfox.task_statuses
		WHERE organization_id = openfox_private.current_organization_id() AND archived_at IS NULL
		ORDER BY position, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TaskStatus, 0)
	for rows.Next() {
		item, err := scanStatus(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func selectWorkflow(statuses []TaskStatus, selectedIDs []string, defaultID string) ([]TaskStatus, string, error) {
	if len(selectedIDs) == 0 {
		selectedIDs = make([]string, 0, len(statuses))
		for _, status := range statuses {
			selectedIDs = append(selectedIDs, status.ID)
		}
	}
	byID := make(map[string]TaskStatus, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	selected := make([]TaskStatus, 0, len(selectedIDs))
	seen := make(map[string]bool)
	for _, id := range selectedIDs {
		if !membership.ValidateUUID(id) || seen[id] {
			return nil, "", ErrInvalidWorkflow
		}
		status, ok := byID[id]
		if !ok || status.ArchivedAt != nil {
			return nil, "", ErrInvalidWorkflow
		}
		seen[id] = true
		selected = append(selected, status)
	}
	if defaultID == "" {
		for _, status := range selected {
			if status.Category == "TODO" {
				defaultID = status.ID
				break
			}
		}
	}
	defaultStatus, ok := byID[defaultID]
	if !ok || !seen[defaultID] || IsTerminalCategory(defaultStatus.Category) {
		return nil, "", ErrInvalidWorkflow
	}
	return selected, defaultID, nil
}

func (s Service) loadTaskNames(ctx context.Context, tx pgx.Tx, task *Task) error {
	return tx.QueryRow(ctx, `
		SELECT tt.name, ts.name, ts.category
		FROM openfox.task_types tt
		JOIN openfox.task_statuses ts ON ts.organization_id = tt.organization_id
		WHERE tt.organization_id = openfox_private.current_organization_id()
		  AND tt.id = $1::uuid AND ts.id = $2::uuid
	`, task.TypeID, task.StatusID).Scan(&task.TypeName, &task.StatusName, &task.StatusCategory)
}

func scanStatus(row interface{ Scan(...any) error }) (TaskStatus, error) {
	var item TaskStatus
	var version int64
	err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Category, &item.Position,
		&item.Color, &item.Icon, &item.ArchivedAt, &version, &item.CreatedAt)
	if err != nil {
		return TaskStatus{}, err
	}
	item.Version, err = positiveVersion(version)
	return item, err
}

func displayID(prefix string, sequence int64) string {
	return fmt.Sprintf("%s-%d", prefix, sequence)
}

func positiveVersion(value int64) (uint64, error) {
	if value <= 0 {
		return 0, ErrInvalidVersion
	}
	return uint64(value), nil
}

func parseSpaceMemberRole(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "OWNER":
		return "OWNER", nil
	case "ADMINISTRATOR", "ADMIN", "EDITOR":
		return "EDITOR", nil
	case "MEMBER":
		return "MEMBER", nil
	case "VISITOR", "VIEWER":
		return "VIEWER", nil
	default:
		return "", ErrInvalidStatus
	}
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validDisplayToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 64 {
		return value == ""
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validateStatusDisplay(description, color, icon string) (string, string, string, error) {
	description = strings.TrimSpace(description)
	color = strings.TrimSpace(color)
	icon = strings.TrimSpace(icon)
	if len([]rune(description)) > 1000 || !validDisplayToken(color) || !validDisplayToken(icon) {
		return "", "", "", ErrInvalidStatus
	}
	return description, color, icon, nil
}

func validActivityKind(value string) bool {
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			if index == 0 && (character < 'a' || character > 'z') {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func mapDatabaseError(err error) error {
	if IsPriorityLevelViolation(err) {
		return ErrPriorityLevelUnavailable
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.ConstraintName == "task_assignees_reserved_capacity":
			return errTooManyAssignees
		case pgErr.Code == "23505":
			return ErrConflict
		case pgErr.Code == "23503":
			return ErrInvalidTask
		case pgErr.Code == "23514":
			return ErrInvalidTask
		}
	}
	return err
}

// NewID is kept local to the domain package for activity/task IDs generated
// outside a request handler.
func NewID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}
