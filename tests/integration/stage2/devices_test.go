package stage2

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"testing"
)

type dvDevice struct {
	ID              string  `json:"id"`
	GrantID         string  `json:"grant_id"`
	OwnerUserID     *string `json:"owner_user_id"`
	OwnerEmail      *string `json:"owner_email"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	GrantStatus     string  `json:"grant_status"`
	Online          bool    `json:"online"`
	MaxSlots        int     `json:"max_slots"`
	SettingsVersion int64   `json:"settings_version"`
	GrantScope      string  `json:"grant_scope"`
	GrantSpaceID    *string `json:"grant_space_id"`
	GrantSpaceName  *string `json:"grant_space_name"`
	PermissionVer   int64   `json:"permission_version"`
	Runtimes        []struct {
		Provider string `json:"provider"`
		Status   string `json:"status"`
		Models   []struct {
			ID string `json:"id"`
		} `json:"models"`
	} `json:"runtimes"`
	AgentCount       int `json:"agent_count"`
	ActiveAgentCount int `json:"active_agent_count"`
	RunningTasks     int `json:"running_tasks"`
}

type dvDetail struct {
	dvDevice
	BoundAgents []struct {
		ID         string  `json:"id"`
		Visibility string  `json:"visibility"`
		OwnerEmail *string `json:"owner_email"`
		Online     bool    `json:"online"`
	} `json:"bound_agents"`
	CreatedAt string  `json:"created_at"`
	Timezone  *string `json:"timezone"`
}

type dvPage struct {
	Devices    []dvDevice `json:"devices"`
	NextCursor *string    `json:"next_cursor"`
}

type dvRemoval struct {
	ArchivedAgents int `json:"archived_agents"`
	CanceledRuns   int `json:"canceled_runs"`
	AffectedTasks  int `json:"affected_tasks"`
}

// visibleDeviceIDs 翻完全部页，返回某成员可见的电脑 ID（排序后），并断言分页不重复。
func (e *dvEnv) visibleDeviceIDs(userID string, limit int) []string {
	e.t.Helper()
	ids := []string{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 50; page++ {
		query := url.Values{"limit": {strconv.Itoa(limit)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var body dvPage
		e.client(userID).must(http.StatusOK, http.MethodGet, "devices?"+query.Encode(), nil, &body)
		for _, device := range body.Devices {
			if seen[device.ID] {
				e.t.Fatalf("device %s listed twice", device.ID)
			}
			seen[device.ID] = true
			ids = append(ids, device.ID)
		}
		if body.NextCursor == nil {
			sort.Strings(ids)
			return ids
		}
		cursor = *body.NextCursor
	}
	e.t.Fatal("pagination did not terminate")
	return nil
}

func sortedIDs(ids ...string) []string {
	result := append([]string{}, ids...)
	sort.Strings(result)
	return result
}

func sameIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// 可见性矩阵：ORGANIZATION = 非访客；SPACE = 空间可读者（SHARED 空间对非访客可读、私有空间需空间成员）；
// PERSONAL = 授权对象本人；所有者始终可见自己的电脑；吊销的授权与其他组织的电脑都不出现。
func TestDevicesVisibilityMatrix(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	private := env.createSpace("private")
	env.addSpaceMember(private, dvMemberID, "MEMBER", "ACTIVE")
	env.addSpaceMember(private, dvGuestID, "MEMBER", "ACTIVE")

	orgDevice := env.insertDevice(dvOwnerID, "ORGANIZATION", "")
	sharedDevice := env.insertDevice(dvOwnerID, "SPACE", shared)
	privateDevice := env.insertDevice(dvOwnerID, "SPACE", private)
	memberPersonal := env.insertDevice(dvMemberID, "PERSONAL", dvMemberID)
	guestPersonal := env.insertDevice(dvOwnerID, "PERSONAL", dvGuestID)
	revoked := env.insertDevice(dvOwnerID, "ORGANIZATION", "")
	env.exec(`UPDATE openfox.device_grants SET status = 'REVOKED' WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.org, revoked)
	// 另一个组织的电脑。
	otherDevice := dvNewID(t)
	env.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, owner_user_id, name)
		VALUES ($1::uuid, $2, 1, $3::uuid, 'Other org Mac')`, otherDevice, "dv-other-"+dvSuffix(t), dvOutsiderID)
	env.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, env.otherOrg, otherDevice)

	want := map[string][]string{
		dvOwnerID:   sortedIDs(orgDevice, sharedDevice, privateDevice, guestPersonal),
		dvAdminID:   sortedIDs(orgDevice, sharedDevice),
		dvMemberID:  sortedIDs(orgDevice, sharedDevice, privateDevice, memberPersonal),
		dvMember2ID: sortedIDs(orgDevice, sharedDevice),
		dvGuestID:   sortedIDs(privateDevice, guestPersonal),
	}
	for userID, ids := range want {
		if got := env.visibleDeviceIDs(userID, 100); !sameIDs(got, ids) {
			t.Fatalf("%s sees %v, want %v", dvRoles[userID], got, ids)
		}
		// 每页 1 台时翻页结果一致。
		if got := env.visibleDeviceIDs(userID, 1); !sameIDs(got, ids) {
			t.Fatalf("%s paged sees %v, want %v", dvRoles[userID], got, ids)
		}
	}

	// 字段：snake_case、在线、Runtime、空间名。
	var page dvPage
	env.client(dvMemberID).must(http.StatusOK, http.MethodGet, "devices", nil, &page)
	for _, device := range page.Devices {
		if device.ID == sharedDevice {
			if !device.Online || device.GrantStatus != "ACTIVE" || device.GrantScope != "SPACE" ||
				device.GrantSpaceID == nil || *device.GrantSpaceID != shared || device.GrantSpaceName == nil ||
				device.OwnerEmail == nil || *device.OwnerEmail != dvEmail(dvOwnerID) || device.SettingsVersion != 1 ||
				len(device.Runtimes) != 1 || device.Runtimes[0].Provider != "codex" || device.Runtimes[0].Status != "READY" ||
				len(device.Runtimes[0].Models) != 1 {
				t.Fatalf("shared device = %+v", device)
			}
		}
	}
	// 心跳超过 90 秒：离线。
	env.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() - interval '91 seconds' WHERE id = $1::uuid`, orgDevice)
	var detail dvDetail
	env.client(dvMemberID).must(http.StatusOK, http.MethodGet, "devices/"+orgDevice, nil, &detail)
	if detail.Online || detail.Timezone == nil || *detail.Timezone != "Asia/Shanghai" || detail.CreatedAt == "" {
		t.Fatalf("offline detail = %+v", detail)
	}

	// 不可见的电脑：详情与子资源 404。
	env.client(dvMember2ID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+privateDevice, nil)
	env.client(dvMember2ID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+privateDevice+"/removal-blockers", nil)
	env.client(dvGuestID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+orgDevice, nil)
	env.client(dvOwnerID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+revoked, nil)
	env.client(dvOwnerID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+otherDevice, nil)
	env.client(dvOwnerID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/not-a-uuid", nil)
	// 跨租户：outsider 不是测试组织成员；在自己的组织里也看不到测试组织的电脑。
	outsider := env.client(dvOutsiderID)
	if response := outsider.do(env.org, http.MethodGet, "devices", nil); response.Status != http.StatusNotFound {
		t.Fatalf("outsider list = %d %s", response.Status, response.Body)
	}
	if response := outsider.do(env.otherOrg, http.MethodGet, "devices/"+orgDevice, nil); response.Status != http.StatusNotFound {
		t.Fatalf("outsider cross-tenant get = %d %s", response.Status, response.Body)
	}
	var otherPage dvPage
	if response := outsider.do(env.otherOrg, http.MethodGet, "devices", nil); response.Status != http.StatusOK {
		t.Fatalf("outsider own list = %d", response.Status)
	} else if err := json.Unmarshal(response.Body, &otherPage); err != nil || len(otherPage.Devices) != 1 || otherPage.Devices[0].ID != otherDevice {
		t.Fatalf("outsider own list = %s", response.Body)
	}

	// 参数校验。
	env.client(dvOwnerID).expectError(http.StatusBadRequest, "INVALID_CURSOR", http.MethodGet, "devices?cursor=%25%25", nil)
	env.client(dvOwnerID).expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, "devices?limit=201", nil)

	// bound_agents：所有者看到全部；其他成员只看到自己的与可读空间里的 SHARED Agent。
	sharedAgent := env.insertAgent(shared, dvOwnerID, orgDevice, "SHARED")
	privateAgent := env.insertAgent(shared, dvOwnerID, orgDevice, "PRIVATE")
	hiddenSpaceAgent := env.insertAgent(private, dvOwnerID, orgDevice, "SHARED")
	agentIDs := func(userID string) []string {
		var detail dvDetail
		env.client(userID).must(http.StatusOK, http.MethodGet, "devices/"+orgDevice, nil, &detail)
		ids := []string{}
		for _, agent := range detail.BoundAgents {
			ids = append(ids, agent.ID)
		}
		sort.Strings(ids)
		return ids
	}
	if got := agentIDs(dvOwnerID); !sameIDs(got, sortedIDs(sharedAgent, privateAgent, hiddenSpaceAgent)) {
		t.Fatalf("owner bound agents = %v", got)
	}
	if got := agentIDs(dvMember2ID); !sameIDs(got, sortedIDs(sharedAgent)) {
		t.Fatalf("member2 bound agents = %v", got)
	}
	if got := agentIDs(dvAdminID); !sameIDs(got, sortedIDs(sharedAgent, privateAgent, hiddenSpaceAgent)) {
		t.Fatalf("admin bound agents = %v", got)
	}
}

// PATCH：仅所有者；非所有者 404；settings_version 乐观锁；参数校验。可见范围修改响应为 snake_case。
func TestDevicesSettingsAndScope(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	device := env.insertDevice(dvOwnerID, "ORGANIZATION", "")
	owner := env.client(dvOwnerID)

	var updated dvDevice
	owner.must(http.StatusOK, http.MethodPatch, "devices/"+device,
		map[string]any{"name": "  Studio Mac  ", "max_slots": 4, "settings_version": 1}, &updated)
	if updated.Name != "Studio Mac" || updated.MaxSlots != 4 || updated.SettingsVersion != 2 {
		t.Fatalf("updated = %+v", updated)
	}
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "devices/"+device,
		map[string]any{"name": "Stale", "settings_version": 1})
	// 组织管理员能看到但不是所有者：404（不暴露可管理性）。
	env.client(dvAdminID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, "devices/"+device,
		map[string]any{"name": "Admin", "settings_version": 2})
	for _, body := range []map[string]any{
		{"max_slots": 0, "settings_version": 2},
		{"max_slots": 257, "settings_version": 2},
		{"name": "   ", "settings_version": 2},
		{"settings_version": 2},
		{"name": "x", "settings_version": 0},
	} {
		owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "devices/"+device, body)
	}
	var stored string
	env.scalar(&stored, `SELECT name FROM openfox.physical_devices WHERE id = $1::uuid`, device)
	if stored != "Studio Mac" {
		t.Fatalf("stored name = %q", stored)
	}

	var scoped dvDevice
	owner.must(http.StatusOK, http.MethodPost, "devices/"+device+"/scope",
		map[string]any{"scope": "SPACE", "space_id": shared, "version": updated.PermissionVer}, &scoped)
	if scoped.GrantScope != "SPACE" || scoped.GrantSpaceID == nil || *scoped.GrantSpaceID != shared ||
		scoped.PermissionVer != updated.PermissionVer+1 || scoped.ID != device {
		t.Fatalf("scoped = %+v", scoped)
	}
	env.client(dvMemberID).expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, "devices/"+device+"/scope",
		map[string]any{"scope": "ORGANIZATION", "version": scoped.PermissionVer})
}

// 移除机器：有绑定 Agent 时 409；解绑（归档）后所有者可移除，电脑与全部授权被吊销。
func TestDevicesRemovalBlockersAndRemove(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	device := env.insertDevice(dvOwnerID, "ORGANIZATION", "")
	agent := env.insertAgent(shared, dvOwnerID, device, "SHARED")
	archived := env.insertAgent(shared, dvOwnerID, device, "SHARED")
	env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, archived)
	owner, admin, member := env.client(dvOwnerID), env.client(dvAdminID), env.client(dvMemberID)

	var blockers struct {
		Agents []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"agents"`
		Count int `json:"count"`
	}
	owner.must(http.StatusOK, http.MethodGet, "devices/"+device+"/removal-blockers", nil, &blockers)
	if blockers.Count != 1 || len(blockers.Agents) != 1 || blockers.Agents[0].ID != agent {
		t.Fatalf("blockers = %+v", blockers)
	}
	admin.must(http.StatusOK, http.MethodGet, "devices/"+device+"/removal-blockers", nil, nil)
	member.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodGet, "devices/"+device+"/removal-blockers", nil)

	admin.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodDelete, "devices/"+device, nil)
	owner.expectError(http.StatusConflict, "DEVICE_HAS_ACTIVE_AGENTS", http.MethodDelete, "devices/"+device, nil)

	admin.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, "devices/"+device+"/unbind-agents", map[string]any{})
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "devices/"+device+"/unbind-agents",
		map[string]any{"agent_ids": []string{"nope"}})
	// 只解绑不相关的 ID：幂等无变化。
	var result dvRemoval
	owner.must(http.StatusOK, http.MethodPost, "devices/"+device+"/unbind-agents",
		map[string]any{"agent_ids": []string{archived, dvNewID(t)}}, &result)
	if result != (dvRemoval{}) {
		t.Fatalf("no-op unbind = %+v", result)
	}
	// 空请求体 = 全部。
	if response := owner.do(env.org, http.MethodPost, "devices/"+device+"/unbind-agents", nil); response.Status != http.StatusOK {
		t.Fatalf("unbind all = %d %s", response.Status, response.Body)
	} else if err := json.Unmarshal(response.Body, &result); err != nil || result.ArchivedAgents != 1 {
		t.Fatalf("unbind all = %s", response.Body)
	}
	var archivedAt *string
	env.scalar(&archivedAt, `SELECT archived_at::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, agent)
	if archivedAt == nil {
		t.Fatal("unbound agent was not archived")
	}

	owner.must(http.StatusNoContent, http.MethodDelete, "devices/"+device, nil, nil)
	var deviceStatus, grantStatus string
	env.scalar(&deviceStatus, `SELECT status FROM openfox.physical_devices WHERE id = $1::uuid`, device)
	env.scalar(&grantStatus, `SELECT status FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.org, device)
	if deviceStatus != "REVOKED" || grantStatus != "REVOKED" {
		t.Fatalf("after remove device=%s grant=%s", deviceStatus, grantStatus)
	}
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+device, nil)
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "devices/"+device, nil)
	var audits int
	env.scalar(&audits, `SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND resource_id = $2
		AND action IN ('device.removed', 'device.agents_unbound')`, env.org, device)
	if audits != 3 {
		t.Fatalf("audit events = %d", audits)
	}
}

// 从组织移除：归档绑定 Agent、QUEUED 直接取消、RUNNING 请求取消、吊销本组织授权；电脑本身保持 ACTIVE。
func TestDevicesRemoveFromOrganizationArchivesAndCancels(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	task := env.createTask(shared)
	device := env.insertDevice(dvMemberID, "ORGANIZATION", "")
	agentA := env.insertAgent(shared, dvMemberID, device, "SHARED")
	agentB := env.insertAgent(shared, dvMemberID, device, "SHARED")
	queued := env.insertRun(env.insertConversation(shared, agentA, task, dvMemberID, "PRIVATE"), dvMemberID, "QUEUED")
	running := env.insertRun(env.insertConversation(shared, agentB, "", dvMemberID, "PRIVATE"), dvMemberID, "RUNNING")

	var before dvDevice
	env.client(dvMemberID).must(http.StatusOK, http.MethodGet, "devices/"+device, nil, &before)
	if before.AgentCount != 2 || before.ActiveAgentCount != 2 || before.RunningTasks != 1 {
		t.Fatalf("counts before = %+v", before)
	}

	env.client(dvGuestID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "devices/"+device+"/remove-from-organization", nil)
	env.client(dvMember2ID).expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, "devices/"+device+"/remove-from-organization", nil)
	if response := env.client(dvOutsiderID).do(env.otherOrg, http.MethodPost, "devices/"+device+"/remove-from-organization", nil); response.Status != http.StatusNotFound {
		t.Fatalf("outsider remove = %d %s", response.Status, response.Body)
	}

	var result dvRemoval
	env.client(dvAdminID).must(http.StatusOK, http.MethodPost, "devices/"+device+"/remove-from-organization", nil, &result)
	if result != (dvRemoval{ArchivedAgents: 2, CanceledRuns: 2, AffectedTasks: 1}) {
		t.Fatalf("result = %+v", result)
	}
	var queuedState, runningState, grantStatus, deviceStatus string
	var archivedCount, outbox int
	env.scalar(&queuedState, `SELECT state FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, queued)
	env.scalar(&runningState, `SELECT state FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, running)
	env.scalar(&grantStatus, `SELECT status FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.org, device)
	env.scalar(&deviceStatus, `SELECT status FROM openfox.physical_devices WHERE id = $1::uuid`, device)
	env.scalar(&archivedCount, `SELECT count(*) FROM openfox.agents WHERE organization_id = $1::uuid AND id IN ($2::uuid, $3::uuid)
		AND archived_at IS NOT NULL`, env.org, agentA, agentB)
	env.scalar(&outbox, `SELECT count(*) FROM openfox.execution_outbox WHERE organization_id = $1::uuid AND aggregate_id = $2::uuid
		AND kind = 'run.cancel'`, env.org, running)
	if queuedState != "CANCELED" || runningState != "CANCEL_REQUESTED" || grantStatus != "REVOKED" ||
		deviceStatus != "ACTIVE" || archivedCount != 2 || outbox != 1 {
		t.Fatalf("queued=%s running=%s grant=%s device=%s archived=%d outbox=%d",
			queuedState, runningState, grantStatus, deviceStatus, archivedCount, outbox)
	}
	env.client(dvMemberID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "devices/"+device, nil)
	if ids := env.visibleDeviceIDs(dvMemberID, 100); len(ids) != 0 {
		t.Fatalf("member still sees %v", ids)
	}
	// 重复调用：授权已吊销，404。
	env.client(dvAdminID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, "devices/"+device+"/remove-from-organization", nil)
}
