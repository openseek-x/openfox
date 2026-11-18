package stage12e

// D-09：一台电脑加入多个组织（仅所有者、无需对方管理员确认）、多组织领取共用本机并发上限、
// 对方组织移除授权后不再领取、非所有者与非成员组织不能加入。

import (
	"net/http"
	"slices"
	"testing"
)

type orgRow struct {
	OrganizationID string  `json:"organization_id"`
	Role           string  `json:"role"`
	GrantStatus    *string `json:"grant_status"`
	Current        bool    `json:"current"`
	CanJoin        bool    `json:"can_join"`
	CanRevoke      bool    `json:"can_revoke"`
	CanRemove      bool    `json:"can_remove"`
	RejoinBlocked  bool    `json:"rejoin_blocked"`
}

type orgList struct {
	Owner         bool     `json:"owner"`
	Organizations []orgRow `json:"organizations"`
}

func findOrg(list orgList, id string) *orgRow {
	for index := range list.Organizations {
		if list.Organizations[index].OrganizationID == id {
			return &list.Organizations[index]
		}
	}
	return nil
}

func (e *qcEnv) deviceOrganizations(device devDevice) []string {
	e.t.Helper()
	var body struct {
		OrganizationIDs []string `json:"organization_ids"`
	}
	response := e.anonymous(http.MethodGet, "/v1/device/organizations", device.session, nil)
	if response.Status != http.StatusOK {
		e.t.Fatalf("device organizations = %d %s", response.Status, response.Body)
	}
	response.decode(e.t, &body)
	return body.OrganizationIDs
}

func TestDeviceJoinOrganizationOwnerOnly(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	join := "devices/" + device.ID + "/join"

	// 所有者看到自己所在的全部组织：otherOrg（MEMBER）可加入，guestOrg（GUEST）不可。
	var list orgList
	env.owner().must(http.StatusOK, http.MethodGet, "devices/"+device.ID+"/organizations", nil, &list)
	if !list.Owner {
		t.Fatal("owner flag missing")
	}
	if row := findOrg(list, env.org); row == nil || !row.Current || row.CanJoin || row.GrantStatus == nil || *row.GrantStatus != "ACTIVE" {
		t.Fatalf("current org row = %+v", row)
	}
	if row := findOrg(list, env.otherOrg); row == nil || !row.CanJoin || row.CanRevoke || row.GrantStatus != nil {
		t.Fatalf("other org row = %+v", row)
	}
	if row := findOrg(list, env.guestOrg); row == nil || row.CanJoin {
		t.Fatalf("guest org row = %+v", row)
	}
	// 非所有者（本组织 ADMIN）只看到当前组织一行，且不能把电脑加入任何组织。
	var adminList orgList
	env.admin().must(http.StatusOK, http.MethodGet, "devices/"+device.ID+"/organizations", nil, &adminList)
	if adminList.Owner || len(adminList.Organizations) != 1 || !adminList.Organizations[0].CanRevoke {
		t.Fatalf("admin view = %+v", adminList)
	}
	env.admin().expectError(http.StatusNotFound, "", http.MethodPost, join, nil)
	// 对方组织的 OWNER 也不能把别人的电脑拉进自己的组织（不是所有者 → 404）。
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, in(env.otherOrg, join), nil)
	// 不是目标组织成员：电脑所有者也不能把电脑加入（admin 只属于 org）。
	adminDevice := env.registerDevice(env.admin(), env.org)
	if response := env.admin().do(http.MethodPost, in(env.otherOrg, "devices/"+adminDevice.ID+"/join"), nil); response.Status != http.StatusNotFound && response.Status != http.StatusForbidden {
		t.Fatalf("join a non-member organization = %d %s", response.Status, response.Body)
	}
	if env.count(`SELECT count(*) FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.otherOrg, adminDevice.ID) != 0 {
		t.Fatal("grant created in a non-member organization")
	}
	// 所有者在目标组织是访客 → 403。
	env.owner().expectError(http.StatusForbidden, "", http.MethodPost, in(env.guestOrg, join), nil)
	// 所有者加入 otherOrg：201，再次加入幂等 200。
	var joined struct {
		Device  deviceDetail `json:"device"`
		Created bool         `json:"created"`
	}
	env.owner().must(http.StatusCreated, http.MethodPost, in(env.otherOrg, join), nil, &joined)
	if !joined.Created || joined.Device.ID != device.ID || joined.Device.GrantStatus != "ACTIVE" {
		t.Fatalf("join = %+v", joined)
	}
	env.owner().must(http.StatusOK, http.MethodPost, in(env.otherOrg, join), nil, &joined)
	if joined.Created {
		t.Fatal("second join not idempotent")
	}
	var scope string
	env.scalar(&scope, `SELECT scope FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.otherOrg, device.ID)
	if scope != "PERSONAL" {
		t.Fatalf("new grant scope = %s, want PERSONAL", scope)
	}
	if got := env.deviceOrganizations(device); len(got) != 2 || !slices.Contains(got, env.org) || !slices.Contains(got, env.otherOrg) {
		t.Fatalf("device organizations = %v", got)
	}
	if env.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'device.organization_joined'
		AND resource_id = $2`, env.otherOrg, device.ID) != 1 {
		t.Fatal("join audit missing")
	}
	// 对方组织 OWNER 可以撤销本组织内的授权；之后这台电脑在该组织的设备端点被拒、不再出现在设备组织列表。
	env.owner().must(http.StatusOK, http.MethodGet, "devices/"+device.ID+"/organizations", nil, &list)
	if row := findOrg(list, env.otherOrg); row == nil || row.CanRevoke || !row.CanRemove {
		t.Fatalf("owner view of other org after join = %+v (owner is only MEMBER there)", row)
	}
	env.outsider().must(http.StatusNoContent, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/revoke"), nil, nil)
	if got := env.deviceOrganizations(device); len(got) != 1 || got[0] != env.org {
		t.Fatalf("device organizations after revoke = %v", got)
	}
	offers := env.anonymous(http.MethodGet, "/v1/device/organizations/"+env.otherOrg+"/offers", device.session, nil)
	if offers.Status == http.StatusOK {
		t.Fatalf("offers in revoked org = %d %s", offers.Status, offers.Body)
	}
	// 被对方组织管理员撤销后，所有者不能自行加回；管理员重新允许后才能加入（授权恢复 ACTIVE，视为新加入 → 201）。
	env.owner().expectError(http.StatusConflict, "DEVICE_REMOVED_BY_ADMIN", http.MethodPost, in(env.otherOrg, join), nil)
	env.outsider().must(http.StatusNoContent, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/allow-rejoin"), nil, nil)
	env.owner().must(http.StatusCreated, http.MethodPost, in(env.otherOrg, join), nil, &joined)
	if joined.Device.GrantStatus != "ACTIVE" {
		t.Fatalf("rejoin = %+v", joined.Device)
	}
}

func TestMultiOrganizationClaimsShareLocalCapacity(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	env.owner().must(http.StatusCreated, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/join"), nil, nil)
	// 授权范围改为组织，让 otherOrg 的 Agent 能绑定（所有者在 otherOrg 是 MEMBER）。
	env.exec(`UPDATE openfox.device_grants SET scope = 'ORGANIZATION', scope_user_id = NULL WHERE organization_id = $1::uuid AND device_id = $2::uuid`,
		env.otherOrg, device.ID)
	env.exec(`UPDATE openfox.physical_devices SET max_slots = 1, last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device.ID)
	worker := env.worker()
	spaceA, agentA := env.seedAgent(env.owner(), env.org, qcOwnerID, device.ID)
	spaceB, agentB := env.seedAgent(env.outsider(), env.otherOrg, qcOutsiderID, device.ID)
	runA := env.queueRun(env.org, spaceA, agentA, qcOwnerID)
	runB := env.queueRun(env.otherOrg, spaceB, agentB, qcOutsiderID)
	if offers := env.pass(worker, env.org); offers[runA] == "" {
		t.Fatalf("org run not offered (wait %q)", env.waitReason(env.org, runA))
	}
	// 本机总并发上限跨组织共用：otherOrg 的 Run 因电脑容量排队。
	if offers := env.pass(worker, env.otherOrg); offers[runB] != "" {
		t.Fatal("other org run offered although the computer is full")
	}
	if reason := env.waitReason(env.otherOrg, runB); reason != "COMPUTER_CONCURRENCY" {
		t.Fatalf("other org wait reason = %q", reason)
	}
	// 两个组织的 offer 端点都对这台电脑开放（daemon 对每个组织分别领取）。
	for _, org := range []string{env.org, env.otherOrg} {
		if response := env.anonymous(http.MethodGet, "/v1/device/organizations/"+org+"/offers", device.session, nil); response.Status != http.StatusOK {
			t.Fatalf("offers in %s = %d %s", org, response.Status, response.Body)
		}
	}
	// 释放 org 的预留后，otherOrg 的 Run 可以拿到名额。
	env.exec(`UPDATE openfox.runs SET state = 'FAILED', finished_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, runA)
	env.exec(`UPDATE openfox.capacity_reservations SET created_at = clock_timestamp() - interval '5 minutes' WHERE organization_id = $1::uuid AND run_id = $2::uuid`, env.org, runA)
	env.pass(worker, env.org)
	if offers := env.pass(worker, env.otherOrg); offers[runB] == "" {
		t.Fatalf("other org run not offered after release (wait %q)", env.waitReason(env.otherOrg, runB))
	}
}

// 对方组织的 Owner/Admin 在本组织内移除这台电脑后：该组织的排队 Run 不再派给它，设备侧领取被拒，
// 设备的组织列表不再包含该组织；原组织不受影响。
func TestOtherOrganizationRemovalStopsClaims(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	env.owner().must(http.StatusCreated, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/join"), nil, nil)
	env.exec(`UPDATE openfox.device_grants SET scope = 'ORGANIZATION', scope_user_id = NULL WHERE organization_id = $1::uuid AND device_id = $2::uuid`,
		env.otherOrg, device.ID)
	env.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device.ID)
	worker := env.worker()
	spaceA, agentA := env.seedAgent(env.owner(), env.org, qcOwnerID, device.ID)
	spaceB, agentB := env.seedAgent(env.outsider(), env.otherOrg, qcOutsiderID, device.ID)
	runB := env.queueRun(env.otherOrg, spaceB, agentB, qcOutsiderID)
	// 本组织的普通成员（不是所有者、不是管理员）不能移除。
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, "devices/"+device.ID+"/remove-from-organization", nil)
	env.outsider().must(http.StatusOK, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/remove-from-organization"), nil, nil)
	if got := env.deviceOrganizations(device); len(got) != 1 || got[0] != env.org {
		t.Fatalf("device organizations after removal = %v", got)
	}
	if response := env.anonymous(http.MethodGet, "/v1/device/organizations/"+env.otherOrg+"/offers", device.session, nil); response.Status == http.StatusOK {
		t.Fatalf("removed device can still list offers: %s", response.Body)
	}
	// 移除前已排队的 Run 不会再派给这台电脑（移除时绑定的 Agent 被归档、活跃 Run 被取消）。
	if offers := env.pass(worker, env.otherOrg); offers[runB] != "" {
		t.Fatal("run offered to a computer removed from the organization")
	}
	var stateB string
	env.scalar(&stateB, `SELECT state FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.otherOrg, runB)
	if stateB == "QUEUED" || stateB == "PREPARING" || stateB == "RUNNING" {
		t.Fatalf("run in the removed organization is still active: %s", stateB)
	}
	// 原组织照常领取。
	runA := env.queueRun(env.org, spaceA, agentA, qcOwnerID)
	if offers := env.pass(worker, env.org); offers[runA] == "" {
		t.Fatalf("original organization run not offered (wait %q)", env.waitReason(env.org, runA))
	}
	if response := env.anonymous(http.MethodGet, "/v1/device/organizations/"+env.org+"/offers", device.session, nil); response.Status != http.StatusOK {
		t.Fatalf("offers in the original organization = %d %s", response.Status, response.Body)
	}
}

// 被对方组织的 Owner/Admin 移除后，电脑所有者不能自行加回；该组织的 Owner/Admin「重新允许」后才能再加入（不自动加入）。
// 所有者自己退出不产生该状态。其他组织不受影响；非管理员不能重新允许。
func TestRemovedByManagerBlocksRejoinUntilAllowed(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	join := in(env.otherOrg, "devices/"+device.ID+"/join")
	allow := in(env.otherOrg, "devices/"+device.ID+"/allow-rejoin")
	removedList := func(client *qcClient, org string) []string {
		t.Helper()
		var page struct {
			Devices []struct {
				DeviceID string `json:"device_id"`
				Name     string `json:"name"`
			} `json:"devices"`
		}
		client.must(http.StatusOK, http.MethodGet, in(org, "removed-devices"), nil, &page)
		ids := []string{}
		for _, item := range page.Devices {
			ids = append(ids, item.DeviceID)
		}
		return ids
	}
	blockedAt := func(org string) bool {
		var blocked bool
		env.scalar(&blocked, `SELECT removed_by_manager_at IS NOT NULL FROM openfox.device_grants
			WHERE organization_id = $1::uuid AND device_id = $2::uuid`, org, device.ID)
		return blocked
	}
	otherRow := func() *orgRow {
		var list orgList
		env.owner().must(http.StatusOK, http.MethodGet, "devices/"+device.ID+"/organizations", nil, &list)
		return findOrg(list, env.otherOrg)
	}

	// 一、所有者自己退出（所有者在 otherOrg 只是 MEMBER）：不产生状态，可直接再加入。
	env.owner().must(http.StatusCreated, http.MethodPost, join, nil, nil)
	env.owner().must(http.StatusOK, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/remove-from-organization"), nil, nil)
	if blockedAt(env.otherOrg) || len(removedList(env.outsider(), env.otherOrg)) != 0 {
		t.Fatal("owner leaving on their own must not mark the computer as removed by a manager")
	}
	if row := otherRow(); row == nil || !row.CanJoin || row.RejoinBlocked {
		t.Fatalf("row after owner left = %+v", row)
	}
	env.owner().must(http.StatusCreated, http.MethodPost, join, nil, nil)

	// 二、对方组织 OWNER 移除：所有者再加入被拒（409 DEVICE_REMOVED_BY_ADMIN），授权不变。
	// 既有可见性规则下，组织管理员看不到别人个人范围的电脑，所以先由所有者把范围改为组织（这里直接写库）。
	env.exec(`UPDATE openfox.device_grants SET scope = 'ORGANIZATION', scope_user_id = NULL WHERE organization_id = $1::uuid AND device_id = $2::uuid`,
		env.otherOrg, device.ID)
	env.outsider().must(http.StatusOK, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/remove-from-organization"), nil, nil)
	if !blockedAt(env.otherOrg) {
		t.Fatal("removal by a manager was not recorded")
	}
	env.owner().expectError(http.StatusConflict, "DEVICE_REMOVED_BY_ADMIN", http.MethodPost, join, nil)
	if env.count(`SELECT count(*) FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid AND status = 'ACTIVE'`,
		env.otherOrg, device.ID) != 0 {
		t.Fatal("blocked join activated the grant")
	}
	if row := otherRow(); row == nil || row.CanJoin || !row.RejoinBlocked {
		t.Fatalf("row after manager removal = %+v", row)
	}
	if got := env.deviceOrganizations(device); len(got) != 1 || got[0] != env.org {
		t.Fatalf("device organizations = %v", got)
	}
	// 其他组织不受影响：原组织的授权仍 ACTIVE、没有标记，原组织的移除列表为空。
	if blockedAt(env.org) || len(removedList(env.owner(), env.org)) != 0 {
		t.Fatal("another organization was affected")
	}
	if detail := env.device(env.owner(), env.org, device.ID); detail.GrantStatus != "ACTIVE" {
		t.Fatalf("original organization grant = %s", detail.GrantStatus)
	}

	// 三、只有该组织的 Owner/Admin 能看列表与重新允许：所有者在 otherOrg 是 MEMBER → 403；org 的管理员不是 otherOrg 成员 → 拒绝。
	env.owner().expectError(http.StatusForbidden, "", http.MethodGet, in(env.otherOrg, "removed-devices"), nil)
	env.owner().expectError(http.StatusForbidden, "", http.MethodPost, allow, nil)
	if response := env.admin().do(http.MethodPost, allow, nil); response.Status != http.StatusNotFound && response.Status != http.StatusForbidden {
		t.Fatalf("allow by a non-member = %d %s", response.Status, response.Body)
	}
	if !blockedAt(env.otherOrg) {
		t.Fatal("a non-manager cleared the removal mark")
	}
	if got := removedList(env.outsider(), env.otherOrg); len(got) != 1 || got[0] != device.ID {
		t.Fatalf("removed devices seen by the manager = %v", got)
	}

	// 四、管理员重新允许：不自动加入；之后所有者可以加入；重复允许返回 404。
	env.outsider().must(http.StatusNoContent, http.MethodPost, allow, nil, nil)
	if blockedAt(env.otherOrg) || len(removedList(env.outsider(), env.otherOrg)) != 0 {
		t.Fatal("allow did not clear the removal mark")
	}
	if got := env.deviceOrganizations(device); len(got) != 1 {
		t.Fatalf("allow must not join automatically: %v", got)
	}
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, allow, nil)
	if env.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'device.rejoin_allowed'
		AND resource_id = $2 AND actor_id = $3::uuid`, env.otherOrg, device.ID, qcOutsiderID) != 1 {
		t.Fatal("allow audit missing")
	}
	env.owner().must(http.StatusCreated, http.MethodPost, join, nil, nil)
	if got := env.deviceOrganizations(device); len(got) != 2 {
		t.Fatalf("device organizations after allowed rejoin = %v", got)
	}

	// 五、管理员用撤销接口踢出同样算「被管理员移除」（重新加入后是个人范围，管理员看不到它，但撤销接口不依赖可见性）。
	env.outsider().must(http.StatusNoContent, http.MethodPost, in(env.otherOrg, "devices/"+device.ID+"/revoke"), nil, nil)
	env.owner().expectError(http.StatusConflict, "DEVICE_REMOVED_BY_ADMIN", http.MethodPost, join, nil)

	// 六、管理员移除自己的电脑属于自行退出：不记标记，可直接再加入。
	own := env.registerDevice(env.outsider(), env.otherOrg)
	env.outsider().must(http.StatusNoContent, http.MethodPost, in(env.otherOrg, "devices/"+own.ID+"/revoke"), nil, nil)
	env.outsider().must(http.StatusCreated, http.MethodPost, in(env.otherOrg, "devices/"+own.ID+"/join"), nil, nil)
}
