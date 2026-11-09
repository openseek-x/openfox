package collaboration

// 视图：空间里保存的任务筛选与显示设置。personal 视图只有创建者可见，由 views 表的 RLS 保证；
// 这里的查询因此不再重复可见性过滤。空间级读写权限由接口层判定。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

var (
	ErrViewNotFound       = NewRuleError(http.StatusNotFound, "NOT_FOUND", "resource is not available")
	ErrViewsDisabled      = NewRuleError(http.StatusBadRequest, "VIEWS_DISABLED", "views are disabled for this space")
	ErrInvalidViewFilters = NewRuleError(http.StatusBadRequest, "INVALID_VIEW_FILTERS", "view filters are invalid")
	ErrViewForbidden      = NewRuleError(http.StatusForbidden, "ACTION_FORBIDDEN", "action is not allowed")
)

const maxReorderViews = 200

// viewFilterKeys 是视图 filters 允许的键：任务列表的筛选参数（排序、分页、显示类参数属于 display）。
var viewFilterKeys = []string{
	"status_id", "status_id_not", "type_id", "type_id_not", "priority", "priority_not",
	"assignee", "assignee_not", "assignee_id", "label_id", "label_id_not", "creator_id", "creator_id_not",
	"created_via_agent",
	"category", "q", "q_not", "start_from", "start_to", "start_not", "due_from", "due_to", "due_not",
	"estimate_min", "estimate_max", "estimate_none", "iteration_id", "iteration_id_not", "project_id", "project_id_not",
}

type ViewCreator struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type View struct {
	ID             string          `json:"id"`
	SpaceID        string          `json:"space_id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	IconKind       *string         `json:"icon_kind"`
	Icon           *string         `json:"icon"`
	IconURL        *string         `json:"icon_url"`
	Visibility     string          `json:"visibility"`
	Filters        json.RawMessage `json:"filters"`
	Display        json.RawMessage `json:"display"`
	CreatedBy      ViewCreator     `json:"created_by"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Pinned         bool            `json:"pinned"`
	Position       *int            `json:"position"`
	MemberViewMode *string         `json:"member_view_mode"`
	CanEdit        bool            `json:"can_edit"`
	Version        uint64          `json:"version"`
}

// ViewInput 创建视图；Icon 在 IconKind 为 upload 时是上传图标的 ID。
type ViewInput struct {
	Name        string
	Description string
	Visibility  string
	IconKind    string
	Icon        string
	Filters     json.RawMessage
	Display     json.RawMessage
}

type ViewPatch struct {
	Version     uint64
	Name        *string
	Description *string
	Visibility  *string
	// IconKind 非 nil 时整体替换图标；空串清除图标。
	IconKind *string
	Icon     string
	Filters  json.RawMessage
	Display  json.RawMessage
}

// ViewFilterValues 校验 filters：只允许 viewFilterKeys 里的键，取值交给任务列表的筛选解析器校验。
// 返回的 url.Values 与任务列表的查询参数同形。
func ViewFilterValues(raw json.RawMessage) (url.Values, error) {
	var filters map[string]any
	if len(raw) == 0 {
		return url.Values{}, nil
	}
	if err := json.Unmarshal(raw, &filters); err != nil {
		return nil, ErrInvalidViewFilters
	}
	values := url.Values{}
	for key, value := range filters {
		if !slices.Contains(viewFilterKeys, key) {
			return nil, ErrInvalidViewFilters
		}
		items, ok := value.([]any)
		if !ok {
			items = []any{value}
		}
		for _, item := range items {
			switch typed := item.(type) {
			case string:
				values.Add(key, typed)
			case bool:
				values.Add(key, strconv.FormatBool(typed))
			case float64:
				values.Add(key, strconv.FormatFloat(typed, 'f', -1, 64))
			default:
				return nil, ErrInvalidViewFilters
			}
		}
	}
	if _, err := ParseTaskFilter(values); err != nil {
		return nil, ErrInvalidViewFilters
	}
	return values, nil
}

func validateViewJSON(filters, display json.RawMessage) error {
	if len(filters) > 0 {
		if _, err := ViewFilterValues(filters); err != nil {
			return err
		}
	}
	var object map[string]any
	if len(display) > 0 && json.Unmarshal(display, &object) != nil {
		return invalidInput("display must be an object")
	}
	return nil
}

func requireViewsEnabled(ctx context.Context, tx pgx.Tx, spaceID string) error {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT views_enabled FROM openfox.spaces
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND deleted_at IS NULL`, spaceID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSpaceNotFound
	}
	if err != nil {
		return err
	}
	if !enabled {
		return ErrViewsDisabled
	}
	return nil
}

// viewSelect 读取视图、创建者与本人偏好；manager 表示当前用户能管理视图所在空间。
const viewSelect = `
	SELECT v.id::text, v.space_id::text, v.name, v.description, v.icon_kind, COALESCE(v.icon, v.icon_upload_id::text),
	       v.visibility, v.filters, v.display, v.created_by::text, u.email, v.created_at, v.updated_at,
	       COALESCE(mp.pinned, false), mp.position, mp.view_mode, v.version
	  FROM openfox.views v
	  JOIN openfox.users u ON u.id = v.created_by
	  LEFT JOIN openfox.view_member_prefs mp ON mp.organization_id = v.organization_id AND mp.view_id = v.id
	   AND mp.user_id = openfox_private.current_actor_id()
	 WHERE v.organization_id = openfox_private.current_organization_id()`

// IconURL 是浏览器取上传图标的地址：中心把 API 挂在 /api 下。
func IconURL(organizationID, iconID string) string {
	return "/api/v1/organizations/" + organizationID + "/icons/" + iconID
}

func scanView(row interface{ Scan(...any) error }, organizationID, actorID string, manager bool) (View, error) {
	var item View
	var version int64
	if err := row.Scan(&item.ID, &item.SpaceID, &item.Name, &item.Description, &item.IconKind, &item.Icon,
		&item.Visibility, &item.Filters, &item.Display, &item.CreatedBy.ID, &item.CreatedBy.Email, &item.CreatedAt,
		&item.UpdatedAt, &item.Pinned, &item.Position, &item.MemberViewMode, &version); err != nil {
		return View{}, err
	}
	var err error
	if item.Version, err = positiveVersion(version); err != nil {
		return View{}, err
	}
	if item.IconKind != nil && *item.IconKind == "upload" {
		iconURL := IconURL(organizationID, *item.Icon)
		item.IconURL = &iconURL
	}
	item.CanEdit = item.CreatedBy.ID == actorID || (item.Visibility == "space" && manager)
	return item, nil
}

func (s Service) ListViews(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string, manager bool) ([]View, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, viewSelect+` AND v.space_id = $1::uuid ORDER BY mp.position NULLS LAST, v.created_at, v.id`, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []View{}
	for rows.Next() {
		item, err := scanView(rows, organizationID, actor.UserID, manager)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ViewSpaceID 返回视图所在空间；不可见（含他人的 personal 视图）或不存在返回 ErrViewNotFound。
func (s Service) ViewSpaceID(ctx context.Context, tx pgx.Tx, viewID string) (string, error) {
	if !membership.ValidateUUID(viewID) {
		return "", ErrViewNotFound
	}
	var spaceID string
	err := tx.QueryRow(ctx, `SELECT space_id::text FROM openfox.views
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, viewID).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrViewNotFound
	}
	return spaceID, err
}

func (s Service) GetView(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, viewID string, manager bool) (View, error) {
	item, err := scanView(tx.QueryRow(ctx, viewSelect+` AND v.id = $1::uuid`, viewID), organizationID, actor.UserID, manager)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrViewNotFound
	}
	return item, err
}

// ViewTaskFilter 读取视图的筛选条件，供 tasks?view_id= 与请求里的筛选条件合并；视图必须属于该空间。
func (s Service) ViewTaskFilter(ctx context.Context, tx pgx.Tx, spaceID, viewID string) (TaskFilter, error) {
	if !membership.ValidateUUID(viewID) {
		return TaskFilter{}, ErrViewNotFound
	}
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT filters FROM openfox.views
		WHERE organization_id = openfox_private.current_organization_id() AND space_id = $1::uuid AND id = $2::uuid`,
		spaceID, viewID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskFilter{}, ErrViewNotFound
	}
	if err != nil {
		return TaskFilter{}, err
	}
	values, err := ViewFilterValues(raw)
	if err != nil {
		return TaskFilter{}, err
	}
	return ParseTaskFilter(values)
}

// NormalizeViewIcon 校验图标并返回 icon_kind、icon、icon_upload_id（文档集等对象共用）。
func NormalizeViewIcon(kind, icon string) (any, any, any, error) {
	switch kind {
	case "":
		return nil, nil, nil, nil
	case "icon", "emoji":
		if n := len([]rune(icon)); n < 1 || n > 64 {
			return nil, nil, nil, invalidInput("icon is invalid")
		}
		return kind, icon, nil, nil
	case "upload":
		if !membership.ValidateUUID(icon) {
			return nil, nil, nil, invalidInput("icon is invalid")
		}
		return kind, nil, icon, nil
	}
	return nil, nil, nil, invalidInput("icon_kind is invalid")
}

func (s Service) CreateView(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string, input ViewInput, manager bool) (View, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return View{}, err
	}
	name, err := normalizeConfigName(input.Name, 80)
	if err != nil {
		return View{}, err
	}
	if input.Visibility == "" {
		input.Visibility = "personal"
	}
	if input.Visibility != "personal" && input.Visibility != "space" {
		return View{}, invalidInput("visibility is invalid")
	}
	if err := validateViewJSON(input.Filters, input.Display); err != nil {
		return View{}, err
	}
	kind, icon, upload, err := NormalizeViewIcon(input.IconKind, input.Icon)
	if err != nil {
		return View{}, err
	}
	if err := requireViewsEnabled(ctx, tx, spaceID); err != nil {
		return View{}, err
	}
	id, err := NewID()
	if err != nil {
		return View{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.views (organization_id, space_id, id, name, description, icon_kind, icon, icon_upload_id,
		                           visibility, filters, display, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid, $8,
		        COALESCE($9::jsonb, '{}'), COALESCE($10::jsonb, '{}'), $11::uuid)`,
		spaceID, id, name, input.Description, kind, icon, upload, input.Visibility,
		nilIfEmpty(input.Filters), nilIfEmpty(input.Display), actor.UserID); err != nil {
		return View{}, mapDatabaseError(err)
	}
	return s.GetView(ctx, tx, organizationID, actor, id, manager)
}

func nilIfEmpty(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// UpdateView：创建者可改自己的视图；space 视图的管理者也可改，但可见性只有创建者能改。
func (s Service) UpdateView(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, viewID string, patch ViewPatch, manager bool) (View, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return View{}, err
	}
	if patch.Version == 0 {
		return View{}, ErrInvalidVersion
	}
	current, err := s.lockView(ctx, tx, organizationID, actor, viewID, manager)
	if err != nil {
		return View{}, err
	}
	if patch.Visibility != nil {
		if *patch.Visibility != "personal" && *patch.Visibility != "space" {
			return View{}, invalidInput("visibility is invalid")
		}
		if *patch.Visibility != current.Visibility && current.CreatedBy.ID != actor.UserID {
			return View{}, ErrViewForbidden
		}
	}
	if patch.Name != nil {
		name, nameErr := normalizeConfigName(*patch.Name, 80)
		if nameErr != nil {
			return View{}, nameErr
		}
		patch.Name = &name
	}
	if err := validateViewJSON(patch.Filters, patch.Display); err != nil {
		return View{}, err
	}
	setIcon := patch.IconKind != nil
	var kind, icon, upload any
	if setIcon {
		if kind, icon, upload, err = NormalizeViewIcon(*patch.IconKind, patch.Icon); err != nil {
			return View{}, err
		}
	}
	if err := requireViewsEnabled(ctx, tx, current.SpaceID); err != nil {
		return View{}, err
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.views
		   SET name = COALESCE($3, name), description = COALESCE($4, description), visibility = COALESCE($5, visibility),
		       icon_kind = CASE WHEN $6 THEN $7 ELSE icon_kind END, icon = CASE WHEN $6 THEN $8 ELSE icon END,
		       icon_upload_id = CASE WHEN $6 THEN $9::uuid ELSE icon_upload_id END,
		       filters = COALESCE($10::jsonb, filters), display = COALESCE($11::jsonb, display),
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`,
		viewID, int64(patch.Version), patch.Name, patch.Description, patch.Visibility, setIcon, kind, icon, upload,
		nilIfEmpty(patch.Filters), nilIfEmpty(patch.Display))
	if err != nil {
		return View{}, mapDatabaseError(err)
	}
	if result.RowsAffected() == 0 {
		return View{}, ErrVersionConflict
	}
	return s.GetView(ctx, tx, organizationID, actor, viewID, manager)
}

func (s Service) DeleteView(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, viewID string, version uint64, manager bool) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if _, err := s.lockView(ctx, tx, organizationID, actor, viewID, manager); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `DELETE FROM openfox.views
		WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`, viewID, int64(version))
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrVersionConflict
	}
	return nil
}

// lockView 锁住并返回可编辑的视图：不可见返回 404，可见但无编辑权返回 403。
func (s Service) lockView(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, viewID string, manager bool) (View, error) {
	if !membership.ValidateUUID(viewID) {
		return View{}, ErrViewNotFound
	}
	item, err := scanView(tx.QueryRow(ctx, viewSelect+` AND v.id = $1::uuid FOR UPDATE OF v`, viewID), organizationID, actor.UserID, manager)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrViewNotFound
	}
	if err != nil {
		return View{}, err
	}
	if !item.CanEdit {
		return View{}, ErrViewForbidden
	}
	return item, nil
}

// SetViewPrefs 写本人对视图的偏好；pinned 与 viewMode 为 nil 表示不改。viewMode 为空串表示清除。
func (s Service) SetViewPrefs(ctx context.Context, tx pgx.Tx, viewID string, pinned *bool, viewMode *string) error {
	if viewMode != nil && !slices.Contains([]string{"", "list", "board", "table"}, *viewMode) {
		return invalidInput("view_mode is invalid")
	}
	// 从 views 取行：RLS 保证看不见的视图（他人的 personal）插不进偏好。
	result, err := tx.Exec(ctx, `
		INSERT INTO openfox.view_member_prefs (organization_id, view_id, user_id, pinned, view_mode)
		SELECT v.organization_id, v.id, openfox_private.current_actor_id(), COALESCE($2::boolean, false), NULLIF($3::text, '')
		  FROM openfox.views v
		 WHERE v.organization_id = openfox_private.current_organization_id() AND v.id = $1::uuid
		ON CONFLICT (organization_id, view_id, user_id) DO UPDATE
		   SET pinned = COALESCE($2::boolean, view_member_prefs.pinned),
		       view_mode = CASE WHEN $3::text IS NULL THEN view_member_prefs.view_mode ELSE NULLIF($3::text, '') END,
		       updated_at = clock_timestamp()`, viewID, pinned, viewMode)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrViewNotFound
	}
	return nil
}

// ReorderViews 按给定顺序写本人的排序位置；列表里每个视图都必须在该空间且对本人可见。
func (s Service) ReorderViews(ctx context.Context, tx pgx.Tx, spaceID string, viewIDs []string) error {
	if len(viewIDs) > maxReorderViews {
		return invalidInput("view_ids supports at most 200 views")
	}
	seen := make(map[string]bool, len(viewIDs))
	for _, id := range viewIDs {
		if !membership.ValidateUUID(id) || seen[id] {
			return invalidInput("view_ids is invalid")
		}
		seen[id] = true
	}
	result, err := tx.Exec(ctx, `
		INSERT INTO openfox.view_member_prefs (organization_id, view_id, user_id, position)
		SELECT v.organization_id, v.id, openfox_private.current_actor_id(), (u.ord - 1)::int
		  FROM unnest($2::uuid[]) WITH ORDINALITY AS u(id, ord)
		  JOIN openfox.views v ON v.organization_id = openfox_private.current_organization_id() AND v.id = u.id AND v.space_id = $1::uuid
		ON CONFLICT (organization_id, view_id, user_id) DO UPDATE SET position = EXCLUDED.position, updated_at = clock_timestamp()`,
		spaceID, viewIDs)
	if err != nil {
		return err
	}
	if int(result.RowsAffected()) != len(viewIDs) {
		return ErrViewNotFound
	}
	return nil
}
