package devices

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/collaboration"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
	"openfox/internal/runtimepolicy"
	"openfox/internal/tenancy"
)

const (
	defaultPageSize = 100
	maxPageSize     = 200
	// maxListedAgents 限制详情与阻断项里列出的 Agent 数。
	maxListedAgents = 200
	maxTimezoneLen  = 64
)

// activeRunStatesSQL 是占用电脑的 Run 状态（与 runs_active_conversation_unique 一致）。
const activeRunStatesSQL = `('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED')`

// deviceColumns / deviceFromSQL 是电脑视图的公共查询（g = 本组织授权，d = 电脑）。
// 统计列都限定在当前授权所属组织；Agent 只计托管 Agent（space_id 非空）且未归档。
var deviceColumns = `d.id::text, g.id::text, COALESCE(d.owner_user_id::text, ''), COALESCE(u.email, ''), d.name,
	       COALESCE(d.install_id, ''), d.status, g.status,
	       (d.status = 'ACTIVE' AND g.status = 'ACTIVE' AND d.last_seen_at IS NOT NULL
	        AND d.last_seen_at >= clock_timestamp() - make_interval(secs => ` +
	strconv.FormatFloat(OnlineAfter.Seconds(), 'f', -1, 64) + `)),
	       d.generation, d.max_slots, d.reserved_slots, d.last_seen_at,
	       d.protocol_major, d.protocol_minor, d.daemon_version,
	       d.operating_system, d.architecture, (d.capabilities - 'conversation_workdir_roots' - 'conversation_workdir_git_roots' - 'runtime_mcp_inventory'),
	       d.center_epoch, d.version, d.settings_version, g.scope,
	       COALESCE(g.scope_user_id::text, ''), COALESCE(g.scope_space_id::text, ''), COALESCE(gs.name, ''),
	       g.permission_version, counts.agent_count, counts.active_agent_count, counts.running_tasks,
	       d.created_at`

const deviceFromSQL = `
	  FROM openfox.device_grants g
	  JOIN openfox.physical_devices d ON d.id = g.device_id
	  LEFT JOIN openfox.users u ON u.id = d.owner_user_id
	  LEFT JOIN openfox.spaces gs ON gs.organization_id = g.organization_id AND gs.id = g.scope_space_id
	  CROSS JOIN LATERAL (
	      SELECT (SELECT count(*) FROM openfox.agents a
	               WHERE a.organization_id = g.organization_id AND a.device_id = d.id
	                 AND a.space_id IS NOT NULL AND a.archived_at IS NULL)::int AS agent_count,
	             (SELECT count(DISTINCT r.agent_id) FROM openfox.runs r
	               WHERE r.organization_id = g.organization_id AND r.computer_id = d.id
	                 AND r.state IN ` + activeRunStatesSQL + `)::int AS active_agent_count,
	             (SELECT count(DISTINCT r.task_id) FROM openfox.runs r
	               WHERE r.organization_id = g.organization_id AND r.computer_id = d.id
	                 AND r.state IN ` + activeRunStatesSQL + ` AND r.task_id IS NOT NULL)::int AS running_tasks
	  ) counts`

// deviceVisibleSQL 判断当前成员能否看到这台电脑（$1 为大写组织角色）：
//   - 电脑所有者始终可见自己的电脑；
//   - ORGANIZATION 授权：非访客成员可见；
//   - PERSONAL 授权：仅授权对象本人；
//   - SPACE 授权：该空间（ACTIVE）可读者，规则同 collaboration.SpaceReadableSQL。
const deviceVisibleSQL = `(d.owner_user_id = openfox_private.current_actor_id()
	OR (g.scope = 'ORGANIZATION' AND $1 <> 'GUEST')
	OR (g.scope = 'PERSONAL' AND g.scope_user_id = openfox_private.current_actor_id())
	OR (g.scope = 'SPACE' AND EXISTS (
	    SELECT 1 FROM openfox.spaces s
	     WHERE s.organization_id = g.organization_id AND s.id = g.scope_space_id
	       AND s.status = 'ACTIVE' AND ` + collaboration.SpaceReadableSQL + `
	)))`

type rowScanner interface {
	Scan(...any) error
}

func scanDevice(row rowScanner) (Device, time.Time, error) {
	var device Device
	var scope string
	var createdAt time.Time
	err := row.Scan(
		&device.ID, &device.GrantID, &device.OwnerUserID, &device.OwnerEmail, &device.Name,
		&device.InstallID, &device.Status, &device.GrantStatus, &device.Online,
		&device.Generation, &device.MaxSlots, &device.ReservedSlots, &device.LastSeenAt,
		&device.ProtocolMajor, &device.ProtocolMinor, &device.DaemonVersion,
		&device.OperatingSystem, &device.Architecture, &device.Capabilities,
		&device.CenterEpoch, &device.Version, &device.SettingsVersion, &scope,
		&device.GrantUserID, &device.GrantSpaceID, &device.GrantSpaceName,
		&device.PermissionVersion, &device.AgentCount, &device.ActiveAgentCount, &device.RunningTasks,
		&createdAt,
	)
	if err != nil {
		return Device{}, time.Time{}, err
	}
	device.GrantScope = Scope(scope)
	device.Runtimes = ParseRuntimes(device.Capabilities)
	device.Storage = ParseStorage(device.Capabilities)
	device.RuntimesCheckedAt = parseRuntimesCheckedAt(device.Capabilities)
	return device, createdAt, nil
}

// ParseRuntimes 从心跳 capabilities.runtimes 解析 Runtime 列表，按名称排序。
// capabilities 是设备上报的不透明 JSON，格式不符的条目直接跳过。
func ParseRuntimes(capabilities json.RawMessage) []Runtime {
	var document struct {
		Runtimes map[string]json.RawMessage `json:"runtimes"`
	}
	result := make([]Runtime, 0)
	if len(capabilities) == 0 || json.Unmarshal(capabilities, &document) != nil {
		return result
	}
	providers := make([]string, 0, len(document.Runtimes))
	for provider := range document.Runtimes {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	for _, provider := range providers {
		var item struct {
			Version  string         `json:"version"`
			Login    string         `json:"login_status"`
			Runnable bool           `json:"runnable"`
			Reason   string         `json:"reason"`
			Models   []RuntimeModel `json:"models"`
			// 适配器上报真正支持的权限档位；旧版后台服务没有该字段。
			ApprovalModes    []string `json:"approval_modes"`
			ReasoningEfforts []string `json:"reasoning_efforts"`
			// 适配器上报真正支持的协作模式；旧版后台服务没有该字段。
			CollaborationModes []string `json:"collaboration_modes"`
		}
		if json.Unmarshal(document.Runtimes[provider], &item) != nil {
			continue
		}
		runtime := Runtime{
			Provider: provider, Version: nullable(item.Version), Runnable: item.Runnable,
			Reason: nullable(item.Reason), LoginStatus: nullable(item.Login), Models: item.Models,
			ApprovalModes:      runtimepolicy.NormalizeApprovalModes(item.ApprovalModes),
			ReasoningEfforts:   runtimepolicy.NormalizeReasoningEfforts(item.ReasoningEfforts),
			CollaborationModes: runtimepolicy.NormalizeCollaborationModes(item.CollaborationModes),
		}
		if runtime.Models == nil {
			runtime.Models = []RuntimeModel{}
		}
		for index := range runtime.Models {
			model := &runtime.Models[index]
			model.ReasoningEfforts = runtimepolicy.NormalizeReasoningEfforts(model.ReasoningEfforts)
			model.DefaultReasoningEffort = runtimepolicy.NormalizeDefaultReasoningEffort(model.DefaultReasoningEffort, model.ReasoningEfforts)
		}
		switch {
		case strings.EqualFold(item.Login, "LOGGED_OUT"):
			runtime.Status = RuntimeNeedsLogin
		case item.Runnable:
			runtime.Status = RuntimeReady
		default:
			runtime.Status = RuntimeUnavailable
		}
		result = append(result, runtime)
	}
	return result
}

// parseTimezone 读取 capabilities.timezone（可选）。
func parseTimezone(capabilities json.RawMessage) string {
	var document struct {
		Timezone string `json:"timezone"`
	}
	if len(capabilities) == 0 || json.Unmarshal(capabilities, &document) != nil {
		return ""
	}
	value := strings.TrimSpace(document.Timezone)
	if len(value) > maxTimezoneLen {
		return ""
	}
	return value
}

// 游标是「注册时间(纳秒) | 电脑 ID」的 base64url，客户端只原样回传。
func encodeDeviceCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixNano(), 10) + "|" + id))
}

func decodeDeviceCursor(value string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, "", ErrInvalidCursor
	}
	id, err = normalizeUUID(id)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	nanos, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return time.Unix(0, nanos).UTC(), id, nil
}

// ListVisible 返回当前成员可见、本组织 ACTIVE 授权的电脑（按注册时间升序，keyset 分页）。
func (s PostgresStore) ListVisible(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, options ListOptions) (Page, error) {
	if s.Pool == nil {
		return Page{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return Page{}, ErrInvalidRequest
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	if limit < 1 || limit > maxPageSize {
		return Page{}, ErrInvalidRequest
	}
	var afterTime any
	afterID := ""
	if options.Cursor != "" {
		at, id, err := decodeDeviceCursor(options.Cursor)
		if err != nil {
			return Page{}, err
		}
		afterTime, afterID = at, id
	}
	page := Page{Devices: make([]Device, 0, limit)}
	err := s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+deviceColumns+deviceFromSQL+`
			 WHERE g.organization_id = openfox_private.current_organization_id()
			   AND g.status = 'ACTIVE'
			   AND `+deviceVisibleSQL+`
			   AND ($2::timestamptz IS NULL OR (d.created_at, d.id) > ($2::timestamptz, NULLIF($3, '')::uuid))
			 ORDER BY d.created_at, d.id
			 LIMIT $4`, viewer.role(), afterTime, afterID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		var lastCreated time.Time
		for rows.Next() {
			device, createdAt, err := scanDevice(rows)
			if err != nil {
				return err
			}
			if len(page.Devices) == limit {
				last := page.Devices[len(page.Devices)-1]
				cursor := encodeDeviceCursor(lastCreated, last.ID)
				page.NextCursor = &cursor
				break
			}
			page.Devices = append(page.Devices, device)
			lastCreated = createdAt
		}
		return rows.Err()
	})
	return page, err
}

// loadVisibleDevice 读取当前成员可见的电脑（本组织 ACTIVE 授权）；不可见与不存在不可区分。
func loadVisibleDevice(ctx context.Context, tx pgx.Tx, viewer Viewer, deviceID string) (Device, time.Time, error) {
	device, createdAt, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+deviceFromSQL+`
		 WHERE g.organization_id = openfox_private.current_organization_id()
		   AND g.device_id = $2::uuid AND g.status = 'ACTIVE'
		   AND `+deviceVisibleSQL, viewer.role(), deviceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Device{}, time.Time{}, ErrDeviceNotFound
	}
	return device, createdAt, err
}

// Get 返回电脑详情。bound_agents：电脑所有者与组织 OWNER/ADMIN 看到全部；
// 其他成员只看到自己拥有的 Agent，以及可读空间里的 SHARED Agent。
func (s PostgresStore) Get(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) (DeviceDetail, error) {
	if s.Pool == nil {
		return DeviceDetail{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return DeviceDetail{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return DeviceDetail{}, ErrDeviceNotFound
	}
	var detail DeviceDetail
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		device, createdAt, err := loadVisibleDevice(ctx, tx, viewer, deviceID)
		if err != nil {
			return err
		}
		detail = DeviceDetail{Device: device, CreatedAt: createdAt, Timezone: parseTimezone(device.Capabilities)}
		full := viewer.manager() || device.OwnerUserID == selection.ActorID
		rows, err := tx.Query(ctx, `
			SELECT a.id::text, a.name, a.title, a.space_id::text, COALESCE(u.email, ''), a.visibility
			  FROM openfox.agents a
			  JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
			  LEFT JOIN openfox.users u ON u.id = a.owner_user_id
			 WHERE a.organization_id = openfox_private.current_organization_id()
			   AND a.device_id = $2::uuid AND a.space_id IS NOT NULL AND a.archived_at IS NULL
			   AND ($3 OR a.owner_user_id = openfox_private.current_actor_id()
			        OR (a.visibility = 'SHARED' AND s.status = 'ACTIVE' AND `+collaboration.SpaceReadableSQL+`))
			 ORDER BY a.created_at, a.id
			 LIMIT $4`, viewer.role(), deviceID, full, maxListedAgents)
		if err != nil {
			return err
		}
		defer rows.Close()
		detail.BoundAgents = make([]BoundAgent, 0)
		for rows.Next() {
			var agent BoundAgent
			var title, ownerEmail string
			if err := rows.Scan(&agent.ID, &agent.Name, &title, &agent.SpaceID, &ownerEmail, &agent.Visibility); err != nil {
				return err
			}
			agent.Title, agent.OwnerEmail, agent.Online = nullable(title), nullable(ownerEmail), device.Online
			detail.BoundAgents = append(detail.BoundAgents, agent)
		}
		return rows.Err()
	})
	return detail, err
}

// UpdateSettings 修改名称与并发上限：由 openfox_private.update_device_settings 校验所有者、
// 本组织有效授权与 settings_version。
func (s PostgresStore) UpdateSettings(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string, input SettingsInput) (Device, error) {
	if s.Pool == nil {
		return Device{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return Device{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return Device{}, ErrDeviceNotFound
	}
	if input.ExpectedVersion < 1 || (input.Name == nil && input.MaxSlots == nil) {
		return Device{}, ErrInvalidRequest
	}
	var name, maxSlots any
	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		if trimmed == "" || len([]rune(trimmed)) > 100 || strings.IndexFunc(trimmed, isControl) >= 0 {
			return Device{}, ErrInvalidRequest
		}
		name = trimmed
	}
	if input.MaxSlots != nil {
		if *input.MaxSlots < 1 || *input.MaxSlots > 256 {
			return Device{}, ErrInvalidRequest
		}
		maxSlots = *input.MaxSlots
	}
	var result Device
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var version *int64
		if err := tx.QueryRow(ctx, `SELECT openfox_private.update_device_settings($1::uuid, $2, $3, $4)`,
			deviceID, input.ExpectedVersion, name, maxSlots).Scan(&version); err != nil {
			return mapSettingsError(err)
		}
		if version == nil {
			return ErrVersionConflict
		}
		if err := recordAudit(ctx, tx, selection, deviceID, "device.settings_updated", map[string]string{
			"version": formatInt(*version),
		}); err != nil {
			return err
		}
		result, _, err = loadVisibleDevice(ctx, tx, viewer, deviceID)
		return err
	})
	return result, err
}

func mapSettingsError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "42501":
			return ErrDeviceNotFound
		case "22023":
			return ErrInvalidRequest
		}
	}
	return err
}

// RemovalBlockers 列出本组织绑定这台电脑的未归档 Agent。只有电脑所有者与组织 OWNER/ADMIN 可查看。
func (s PostgresStore) RemovalBlockers(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) (RemovalBlockers, error) {
	if s.Pool == nil {
		return RemovalBlockers{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return RemovalBlockers{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return RemovalBlockers{}, ErrDeviceNotFound
	}
	var result RemovalBlockers
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		device, _, err := loadVisibleDevice(ctx, tx, viewer, deviceID)
		if err != nil {
			return err
		}
		if device.OwnerUserID != selection.ActorID && !viewer.manager() {
			return ErrForbidden
		}
		result, err = loadBlockers(ctx, tx, deviceID)
		return err
	})
	return result, err
}

func loadBlockers(ctx context.Context, tx pgx.Tx, deviceID string) (RemovalBlockers, error) {
	result := RemovalBlockers{Agents: make([]BlockingAgent, 0)}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text, a.name, count(*) OVER ()::int
		  FROM openfox.agents a
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.device_id = $1::uuid AND a.space_id IS NOT NULL AND a.archived_at IS NULL
		 ORDER BY a.created_at, a.id
		 LIMIT $2`, deviceID, maxListedAgents)
	if err != nil {
		return RemovalBlockers{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent BlockingAgent
		if err := rows.Scan(&agent.ID, &agent.Name, &result.Count); err != nil {
			return RemovalBlockers{}, err
		}
		result.Agents = append(result.Agents, agent)
	}
	return result, rows.Err()
}

// Remove 移除机器：仅电脑所有者；本组织仍有绑定的未归档 Agent 时拒绝；
// 调用 openfox_private.revoke_device 吊销电脑本身、全部会话与全部组织授权。
func (s PostgresStore) Remove(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string, now time.Time) error {
	if s.Pool == nil {
		return postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return ErrDeviceNotFound
	}
	return s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		device, _, err := loadVisibleDevice(ctx, tx, viewer, deviceID)
		if err != nil {
			return err
		}
		if device.OwnerUserID != selection.ActorID {
			return ErrForbidden
		}
		if err := lockActiveGrant(ctx, tx, deviceID); err != nil {
			return err
		}
		blockers, err := loadBlockers(ctx, tx, deviceID)
		if err != nil {
			return err
		}
		if blockers.Count > 0 {
			return ErrHasActiveAgents
		}
		var revoked bool
		if err := tx.QueryRow(ctx, `SELECT openfox_private.revoke_device($1::uuid, $2)`, deviceID, now.UTC()).Scan(&revoked); err != nil {
			return err
		}
		if !revoked {
			return ErrDeviceNotFound
		}
		return recordAudit(ctx, tx, selection, deviceID, "device.removed", map[string]string{"status": "REVOKED"})
	})
}

// lockActiveGrant 锁住本组织对电脑的 ACTIVE 授权。用 FOR NO KEY UPDATE：不阻塞引用授权的外键检查
// （新建 Run / Agent 只取 KEY SHARE），也不与容量函数的锁序（组织容量 → 电脑 → Agent）交叉。
func lockActiveGrant(ctx context.Context, tx pgx.Tx, deviceID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.device_grants
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND device_id = $1::uuid AND status = 'ACTIVE'
		 FOR NO KEY UPDATE`, deviceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrDeviceNotFound
	}
	return err
}

const (
	removedMessage  = "The Agent run was canceled because its computer was removed from the organization."
	unboundMessage  = "The Agent run was canceled because the Agent was unbound from its computer."
	maxUnbindAgents = 200
)

// RemoveFromOrganization 从组织移除电脑（电脑所有者或组织 OWNER/ADMIN），同一事务内：
// 取消本组织绑定 Agent 的活跃 Run → 归档这些 Agent → 吊销本组织授权。其他组织的授权不受影响。
func (s PostgresStore) RemoveFromOrganization(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string) (RemovalResult, error) {
	if s.Pool == nil {
		return RemovalResult{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return RemovalResult{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return RemovalResult{}, ErrDeviceNotFound
	}
	var result RemovalResult
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		device, _, err := loadVisibleDevice(ctx, tx, viewer, deviceID)
		if err != nil {
			return err
		}
		if device.OwnerUserID != selection.ActorID && !viewer.manager() {
			return ErrForbidden
		}
		if err := lockActiveGrant(ctx, tx, deviceID); err != nil {
			return err
		}
		result, err = archiveBoundAgents(ctx, tx, selection, deviceID, nil, removedMessage)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.device_grants
			   SET status = 'REVOKED', permission_version = permission_version + 1
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND device_id = $1::uuid AND status = 'ACTIVE'`, deviceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrDeviceNotFound
		}
		return recordAudit(ctx, tx, selection, deviceID, "device.removed_from_organization", map[string]string{
			"status": "REVOKED", "result": removalSummary(result),
		})
	})
	if err != nil {
		return RemovalResult{}, err
	}
	return result, nil
}

// UnbindAgents 解绑 Agent（仅电脑所有者）。Agent 的配置版本必须指向一台电脑，
// 因此解绑 = 取消活跃 Run 并归档；agentIDs 为空表示全部。不绑定这台电脑或已归档的 ID 被忽略。
func (s PostgresStore) UnbindAgents(ctx context.Context, selection tenancy.OrganizationSelection, viewer Viewer, deviceID string, agentIDs []string) (RemovalResult, error) {
	if s.Pool == nil {
		return RemovalResult{}, postgres.ErrNilPool
	}
	if err := validateSelection(selection); err != nil {
		return RemovalResult{}, ErrInvalidRequest
	}
	deviceID, err := normalizeUUID(deviceID)
	if err != nil {
		return RemovalResult{}, ErrDeviceNotFound
	}
	if len(agentIDs) > maxUnbindAgents {
		return RemovalResult{}, ErrInvalidRequest
	}
	filter := make([]string, 0, len(agentIDs))
	for _, id := range agentIDs {
		normalized, err := normalizeUUID(id)
		if err != nil {
			return RemovalResult{}, ErrInvalidRequest
		}
		filter = append(filter, normalized)
	}
	var result RemovalResult
	err = s.runForSelection(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		device, _, err := loadVisibleDevice(ctx, tx, viewer, deviceID)
		if err != nil {
			return err
		}
		if device.OwnerUserID != selection.ActorID {
			return ErrForbidden
		}
		if err := lockActiveGrant(ctx, tx, deviceID); err != nil {
			return err
		}
		result, err = archiveBoundAgents(ctx, tx, selection, deviceID, filter, unboundMessage)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, selection, deviceID, "device.agents_unbound", map[string]string{
			"result": removalSummary(result),
		})
	})
	if err != nil {
		return RemovalResult{}, err
	}
	return result, nil
}

// archiveBoundAgents 取消并归档本组织绑定这台电脑的未归档 Agent（filter 非空时只处理其中的 ID）。
// 先取消 Run（容量函数先加锁），再更新 agents 行，保持与派发相同的锁序。
func archiveBoundAgents(ctx context.Context, tx pgx.Tx, selection tenancy.OrganizationSelection, deviceID string, filter []string, message string) (RemovalResult, error) {
	var filterArg any
	if len(filter) > 0 {
		filterArg = filter
	}
	rows, err := tx.Query(ctx, `
		SELECT a.id::text
		  FROM openfox.agents a
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.device_id = $1::uuid AND a.space_id IS NOT NULL AND a.archived_at IS NULL
		   AND ($2::uuid[] IS NULL OR a.id = ANY($2::uuid[]))
		 ORDER BY a.id`, deviceID, filterArg)
	if err != nil {
		return RemovalResult{}, err
	}
	agentIDs := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return RemovalResult{}, err
		}
		agentIDs = append(agentIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return RemovalResult{}, err
	}
	if len(agentIDs) == 0 {
		return RemovalResult{}, nil
	}
	canceled, err := dispatch.CancelAgentRuns(ctx, tx, selection.OrganizationID, agentIDs, message)
	if err != nil {
		return RemovalResult{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.agents
		   SET archived_at = clock_timestamp(), version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = ANY($1::uuid[]) AND device_id = $2::uuid AND archived_at IS NULL`, agentIDs, deviceID)
	if err != nil {
		return RemovalResult{}, err
	}
	return RemovalResult{
		ArchivedAgents: int(tag.RowsAffected()), CanceledRuns: canceled.Canceled,
		AffectedTasks: len(canceled.TaskIDs), CancelNotices: canceled.Notices,
	}, nil
}

func removalSummary(result RemovalResult) string {
	return "agents:" + strconv.Itoa(result.ArchivedAgents) + ",runs:" + strconv.Itoa(result.CanceledRuns) +
		",tasks:" + strconv.Itoa(result.AffectedTasks)
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f
}
