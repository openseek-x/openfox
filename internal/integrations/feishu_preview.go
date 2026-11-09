package integrations

// 飞书「链接预览」回调（url.preview.get）。预览千人千面：只有绑定了飞书账号、且有权读取该任务的成员
// 才能看到任务标题，其余一律返回不含标题的通用文案。

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

const (
	previewGenericTitle = "OpenFox 任务"
	previewTitleLimit   = 80
)

// PreviewEvent 是 url.preview.get 事件里用到的字段。
type PreviewEvent struct {
	Event struct {
		Operator struct {
			OpenID string `json:"open_id"`
		} `json:"operator"`
		Context struct {
			URL string `json:"url"`
		} `json:"context"`
	} `json:"event"`
}

// ParsePreviewURL 解析任务链接：origin 必须等于 publicURL 的 origin，路径形如 /o/{org}/spaces/{space}/tasks，
// 查询参数 task 是任务显示编号。不匹配返回 ok = false。
func ParsePreviewURL(publicURL, raw string) (organizationID, displayID string, ok bool) {
	base, err := url.Parse(strings.TrimRight(publicURL, "/"))
	if err != nil || base.Host == "" {
		return "", "", false
	}
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" || target.User != nil {
		return "", "", false
	}
	if !strings.EqualFold(target.Scheme, base.Scheme) || !strings.EqualFold(target.Host, base.Host) {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(target.Path, "/"), "/")
	if len(parts) != 5 || parts[0] != "o" || parts[2] != "spaces" || parts[4] != "tasks" || parts[1] == "" || parts[3] == "" {
		return "", "", false
	}
	display := strings.TrimSpace(target.Query().Get("task"))
	if display == "" || len(display) > 64 {
		return "", "", false
	}
	return strings.ToLower(parts[1]), display, true
}

// PreviewMember 是按 open_id 找到的、本组织本应用下有效的成员。
type PreviewMember struct {
	UserID               string
	Role                 policy.OrganizationRole
	AuthorizationVersion uint64
}

// FeishuPreviewLookup 在组织 worker 事务里读取预览开关，并把 operator 的 open_id 映射到成员。
// enabled = false 或成员不存在（未绑定、已断开）时 member 为 nil。
func FeishuPreviewLookup(ctx context.Context, tx pgx.Tx, appConnectionID, openID string) (enabled bool, member *PreviewMember, err error) {
	err = tx.QueryRow(ctx, `
		SELECT COALESCE((settings->>'link_preview_enabled')::boolean, true)
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND provider IN ('feishu', 'lark')
		   AND scope = 'ORGANIZATION' AND status = 'ACTIVE'`, appConnectionID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil || !enabled || openID == "" {
		return enabled, nil, err
	}
	var userID, role string
	var version int64
	err = tx.QueryRow(ctx, `
		SELECT m.user_id::text, m.role, m.authorization_version
		  FROM openfox.integration_connections p
		  JOIN openfox.organization_members m
		    ON m.organization_id = p.organization_id AND m.user_id = p.owner_user_id AND m.status = 'ACTIVE'
		 WHERE p.organization_id = openfox_private.current_organization_id() AND p.provider IN ('feishu', 'lark') AND p.scope = 'PERSONAL'
		   AND p.parent_connection_id = $1::uuid AND p.external_id = $2 AND p.status = 'ACTIVE'`,
		appConnectionID, openID).Scan(&userID, &role, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil, nil
	}
	if err != nil {
		return true, nil, err
	}
	parsed, err := membership.ParseRole(role)
	if err != nil {
		return true, nil, nil
	}
	return true, &PreviewMember{UserID: userID, Role: parsed, AuthorizationVersion: uint64(version)}, nil
}

// FeishuPreviewTitle 必须在以该成员为主体的组织事务里调用（行级读取依赖 current_actor_id）。
// 任务不存在或成员无权读取时返回 ok = false。
func (s Service) FeishuPreviewTitle(ctx context.Context, tx pgx.Tx, organizationID string, member PreviewMember, displayID string) (string, bool, error) {
	actor := collaboration.Actor{UserID: member.UserID, Role: member.Role, AuthorizationVersion: member.AuthorizationVersion}
	cs := collaboration.Service{}
	spaceID, taskID, err := cs.FindTaskByDisplayID(ctx, tx, organizationID, displayID, actor)
	if errors.Is(err, collaboration.ErrTaskNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	// 与空间列表 / 我的任务一致的读权限判断（collaboration.SpaceReadableSQL）。
	var readable bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.spaces s
		                WHERE s.organization_id = openfox_private.current_organization_id() AND s.id = $2::uuid AND `+collaboration.SpaceReadableSQL+`)`,
		strings.ToUpper(string(member.Role)), spaceID).Scan(&readable); err != nil {
		return "", false, err
	}
	if !readable {
		return "", false, nil
	}
	task, err := cs.GetTask(ctx, tx, organizationID, spaceID, taskID, actor)
	if errors.Is(err, collaboration.ErrTaskNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	title := []rune(strings.TrimSpace(task.Title))
	if len(title) > previewTitleLimit {
		title = append(title[:previewTitleLimit], '…')
	}
	return task.DisplayID + " · " + string(title) + " · " + task.StatusName, true, nil
}

func previewResponse(title string) []byte {
	body, _ := json.Marshal(map[string]any{"inline": map[string]string{"title": title}})
	return body
}

var previewEmpty = []byte("{}")
