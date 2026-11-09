package stage2

// 重新连接产生新的电脑记录后，把旧记录上的 Agent 迁到新记录（POST devices/{id}/replace）。

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type dvReplacement struct {
	DeviceID         string `json:"device_id"`
	ReplacedDeviceID string `json:"replaced_device_id"`
	MovedAgents      int    `json:"moved_agents"`
	CanceledRuns     int    `json:"canceled_runs"`
}

// dvReplaceColumns 是除 computer_id / grant_id / config_version / created_by / created_at 外必须逐列一致的配置列。
const dvReplaceColumns = `instructions, runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds,
	directory_mode, directory_roots::text, chat_concurrency, chat_concurrency_value, workdir_isolation, approval_mode,
	collaboration_mode, custom_args::text, git_author_name_mode, git_author_name, git_author_email_mode, git_author_email, env_digest`

// insertConfig 给 Agent 写入一个各列都不取默认值的配置版本 1（指向它当前绑定的电脑与授权）。
func (e *dvEnv) insertConfig(agentID, deviceID, ownerID string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.agent_config_versions (
			organization_id, agent_id, config_version, instructions, computer_id, grant_id, runtime, model,
			max_concurrency, queue_timeout_seconds, run_timeout_seconds, directory_mode, directory_roots, created_by,
			chat_concurrency, chat_concurrency_value, workdir_isolation, approval_mode, collaboration_mode,
			custom_args, git_author_name_mode, git_author_name, git_author_email_mode, git_author_email, env_digest)
		SELECT $1::uuid, $2::uuid, 1, 'be careful ' || $2, $3::uuid, g.id, 'codex', 'gpt-5',
		       3, 1800, 7200, 'EXPLICIT', '[{"name":"app","path":"/work/app"}]'::jsonb, $4::uuid,
		       'MANUAL', 4, 'DIRECT', 'FULL_ACCESS', 'PLAN', '["--flag","x"]'::jsonb,
		       'MANUAL', 'Bot', 'MANUAL', 'bot@example.test', repeat('ab', 32)
		  FROM openfox.device_grants g
		 WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid AND g.status = 'ACTIVE'`,
		e.org, agentID, deviceID, ownerID)
}

func (e *dvEnv) setOffline(deviceID string) {
	e.t.Helper()
	e.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() - interval '1 hour' WHERE id = $1::uuid`, deviceID)
}

func (e *dvEnv) grantStatus(org, deviceID string) string {
	e.t.Helper()
	var status string
	e.scalar(&status, `SELECT status FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, org, deviceID)
	return status
}

// agentState 返回 Agent 的 device_id、当前配置版本、最大配置版本与 version，用于断言「数据不变」。
func (e *dvEnv) agentState(agentID string) [4]string {
	e.t.Helper()
	var device, current, maxVersion, version string
	e.scalar(&device, `SELECT device_id::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, agentID)
	e.scalar(&current, `SELECT current_config_version::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, agentID)
	e.scalar(&version, `SELECT version::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, agentID)
	e.scalar(&maxVersion, `SELECT coalesce(max(config_version)::text, '') FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid`, e.org, agentID)
	return [4]string{device, current, maxVersion, version}
}

func (e *dvEnv) configColumns(agentID string, version int) string {
	e.t.Helper()
	var text string
	e.scalar(&text, `SELECT concat_ws('|', `+strings.ReplaceAll(dvReplaceColumns, "\n", " ")+`)
		FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND config_version = $3`,
		e.org, agentID, version)
	return text
}

func TestDevicesReplaceMovesAgents(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	task := env.createTask(shared)
	oldDevice := env.insertDevice(dvMemberID, "PERSONAL", dvMemberID)
	newDevice := env.insertDevice(dvMemberID, "PERSONAL", dvMemberID)
	env.setOffline(oldDevice)
	agentA := env.insertAgent(shared, dvMemberID, oldDevice, "PRIVATE")
	agentB := env.insertAgent(shared, dvMemberID, oldDevice, "PRIVATE")
	assistant := env.insertAgent(shared, dvMemberID, oldDevice, "PRIVATE")
	env.exec(`UPDATE openfox.agents SET builtin_kind = 'ASSISTANT' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, assistant)
	archived := env.insertAgent(shared, dvMemberID, oldDevice, "PRIVATE")
	env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, archived)
	for _, agent := range []string{agentA, agentB, assistant, archived} {
		env.insertConfig(agent, oldDevice, dvMemberID)
	}
	// 找不到当前配置版本的旧数据（agents.current_config_version 没有外键）：不迁，也不阻塞其他 Agent。
	broken := env.insertAgent(shared, dvMemberID, oldDevice, "PRIVATE")
	brokenBefore := env.agentState(broken)
	queued := env.insertRun(env.insertConversation(shared, agentA, task, dvMemberID, "PRIVATE"), dvMemberID, "QUEUED")
	// 跑过任务的 Agent 留有已释放的历史预留（000063 之前它会挡住 agents.device_id 的修改）。
	env.exec(`INSERT INTO openfox.capacity_reservations (organization_id, id, run_id, generation, device_id, agent_id, status, released_at)
		VALUES ($1::uuid, gen_random_uuid(), gen_random_uuid(), 1, $2::uuid, $3::uuid, 'RELEASED', clock_timestamp())`, env.org, oldDevice, agentA)
	running := env.insertRun(env.insertConversation(shared, agentB, "", dvMemberID, "PRIVATE"), dvMemberID, "RUNNING")

	// 其他组织里绑同一台旧电脑的 Agent 不受影响。
	otherSpace, otherAgent, otherGrant := dvNewID(t), dvNewID(t), dvNewID(t)
	env.exec(`INSERT INTO openfox.spaces (organization_id, id, name, visibility, prefix) VALUES ($1::uuid, $2::uuid, 'Other', 'SHARED', 'oth8g')`, env.otherOrg, otherSpace)
	env.exec(`INSERT INTO openfox.device_grants (id, organization_id, device_id, scope) VALUES ($1::uuid, $2::uuid, $3::uuid, 'ORGANIZATION')`, otherGrant, env.otherOrg, oldDevice)
	env.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'Other agent', 'SHARED', 1)`, env.otherOrg, otherAgent, oldDevice, otherSpace, dvOutsiderID)
	env.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model, created_by)
		VALUES ($1::uuid, $2::uuid, 1, $3::uuid, $4::uuid, 'codex', 'gpt-5', $5::uuid)`, env.otherOrg, otherAgent, oldDevice, otherGrant, dvOutsiderID)

	var newGrant string
	env.scalar(&newGrant, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, env.org, newDevice)
	before := map[string]string{}
	for _, agent := range []string{agentA, agentB, assistant} {
		before[agent] = env.configColumns(agent, 1)
	}
	archivedBefore := env.agentState(archived)

	path := "devices/" + newDevice + "/replace"
	body := map[string]any{"replaced_device_id": oldDevice}
	// 非两台电脑的所有者：同组织其他成员看不到个人授权的电脑（404），不能迁移。
	env.client(dvMember2ID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path, body)
	env.client(dvOutsiderID).expectError(http.StatusNotFound, "", http.MethodPost, path, body)
	var result dvReplacement
	env.client(dvMemberID).must(http.StatusOK, http.MethodPost, path, body, &result)
	if result != (dvReplacement{DeviceID: newDevice, ReplacedDeviceID: oldDevice, MovedAgents: 3, CanceledRuns: 2}) {
		t.Fatalf("result = %+v", result)
	}

	for _, agent := range []string{agentA, agentB, assistant} {
		if state := env.agentState(agent); state != [4]string{newDevice, "2", "2", "2"} {
			t.Fatalf("agent %s state = %v", agent, state)
		}
		if got := env.configColumns(agent, 2); got != before[agent] {
			t.Fatalf("agent %s config changed:\n got %s\nwant %s", agent, got, before[agent])
		}
		var computer, grant, createdBy string
		env.scalar(&computer, `SELECT computer_id::text FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND config_version = 2`, env.org, agent)
		env.scalar(&grant, `SELECT grant_id::text FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND config_version = 2`, env.org, agent)
		env.scalar(&createdBy, `SELECT created_by::text FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND config_version = 2`, env.org, agent)
		if computer != newDevice || grant != newGrant || createdBy != dvMemberID {
			t.Fatalf("agent %s config 2 = %s %s %s", agent, computer, grant, createdBy)
		}
		var oldVersionComputer string
		env.scalar(&oldVersionComputer, `SELECT computer_id::text FROM openfox.agent_config_versions WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND config_version = 1`, env.org, agent)
		if oldVersionComputer != oldDevice {
			t.Fatalf("version 1 must stay immutable, computer = %s", oldVersionComputer)
		}
	}
	// 已归档的 Agent 不迁。
	if state := env.agentState(archived); state != archivedBefore {
		t.Fatalf("archived agent changed: %v -> %v", archivedBefore, state)
	}
	if state := env.agentState(broken); state != brokenBefore {
		t.Fatalf("agent without config changed: %v -> %v", brokenBefore, state)
	}
	// 活跃 Run 被取消并带原因。
	var queuedState, runningState, reason string
	env.scalar(&queuedState, `SELECT state FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, queued)
	env.scalar(&runningState, `SELECT state FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, running)
	env.scalar(&reason, `SELECT coalesce(string_agg(body, ' '), '') FROM openfox.messages
		WHERE organization_id = $1::uuid AND conversation_id IN (SELECT conversation_id FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid)`, env.org, queued)
	if queuedState != "CANCELED" || runningState != "CANCEL_REQUESTED" {
		t.Fatalf("queued=%s running=%s", queuedState, runningState)
	}
	if !strings.Contains(reason, "computer was replaced") {
		t.Fatalf("queued run cancel message = %q", reason)
	}
	// 旧授权已吊销；其他组织对旧电脑的授权与 Agent 不受影响；新授权仍有效。
	if status := env.grantStatus(env.org, oldDevice); status != "REVOKED" {
		t.Fatalf("old grant = %s", status)
	}
	if status := env.grantStatus(env.org, newDevice); status != "ACTIVE" {
		t.Fatalf("new grant = %s", status)
	}
	if status := env.grantStatus(env.otherOrg, oldDevice); status != "ACTIVE" {
		t.Fatalf("other org grant = %s", status)
	}
	var otherDevice string
	var otherVersion int
	env.scalar(&otherDevice, `SELECT device_id::text FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, env.otherOrg, otherAgent)
	env.scalar(&otherVersion, `SELECT current_config_version FROM openfox.agents WHERE organization_id = $1::uuid AND id = $2::uuid`, env.otherOrg, otherAgent)
	if otherDevice != oldDevice || otherVersion != 1 {
		t.Fatalf("other org agent = %s v%d", otherDevice, otherVersion)
	}
	// 审计。
	var action, replaced, moved, canceled string
	env.scalar(&action, `SELECT action FROM openfox.audit_events WHERE organization_id = $1::uuid AND resource_id = $2 AND action = 'device.replaced'`, env.org, newDevice)
	env.scalar(&replaced, `SELECT safe_metadata->>'replaced_device_id' FROM openfox.audit_events WHERE organization_id = $1::uuid AND resource_id = $2 AND action = 'device.replaced'`, env.org, newDevice)
	env.scalar(&moved, `SELECT safe_metadata->>'moved_agents' FROM openfox.audit_events WHERE organization_id = $1::uuid AND resource_id = $2 AND action = 'device.replaced'`, env.org, newDevice)
	env.scalar(&canceled, `SELECT safe_metadata->>'canceled_runs' FROM openfox.audit_events WHERE organization_id = $1::uuid AND resource_id = $2 AND action = 'device.replaced'`, env.org, newDevice)
	if action != "device.replaced" || replaced != oldDevice || moved != "3" || canceled != "2" {
		t.Fatalf("audit = %s %s %s %s", action, replaced, moved, canceled)
	}
	// 迁移后用迁移过的 Agent 能写入新 Run（validate_run_scope 要求 Run 的电脑等于 Agent 的电脑且授权有效）。
	env.insertRun(env.insertConversation(shared, agentA, "", dvMemberID, "PRIVATE"), dvMemberID, "QUEUED")
	var newRunComputer string
	env.scalar(&newRunComputer, `SELECT computer_id::text FROM openfox.runs WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND state = 'QUEUED'`, env.org, agentA)
	if newRunComputer != newDevice {
		t.Fatalf("new run computer = %s", newRunComputer)
	}
	// 历史预留原样保留；新预留的电脑必须是 Agent 当前的电脑（000063 的触发器，错误码与原外键相同）。
	var historical int
	env.scalar(&historical, `SELECT count(*) FROM openfox.capacity_reservations WHERE organization_id = $1::uuid AND agent_id = $2::uuid AND device_id = $3::uuid`, env.org, agentA, oldDevice)
	if historical != 1 {
		t.Fatalf("historical reservations on old device = %d", historical)
	}
	_, err := env.superuser.Exec(env.ctx, `INSERT INTO openfox.capacity_reservations (organization_id, id, run_id, generation, device_id, agent_id, status, released_at)
		VALUES ($1::uuid, gen_random_uuid(), gen_random_uuid(), 1, $2::uuid, $3::uuid, 'RELEASED', clock_timestamp())`, env.org, oldDevice, agentA)
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 23503") {
		t.Fatalf("reservation on a computer other than the Agent's must fail with 23503, got %v", err)
	}
	// 重复调用：旧授权已吊销，404。
	env.client(dvMemberID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path, body)
}

func TestDevicesReplaceRejections(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	oldDevice := env.insertDevice(dvMemberID, "ORGANIZATION", "")
	newDevice := env.insertDevice(dvMemberID, "ORGANIZATION", "")
	agent := env.insertAgent(shared, dvMemberID, oldDevice, "SHARED")
	env.insertConfig(agent, oldDevice, dvMemberID)
	path := "devices/" + newDevice + "/replace"
	body := map[string]any{"replaced_device_id": oldDevice}
	unchanged := func() {
		t.Helper()
		if state := env.agentState(agent); state != [4]string{oldDevice, "1", "1", "1"} {
			t.Fatalf("agent changed: %v", state)
		}
		if status := env.grantStatus(env.org, oldDevice); status != "ACTIVE" {
			t.Fatalf("old grant = %s", status)
		}
	}

	// 请求体非法：未知字段、缺字段、非 UUID、两个 ID 相同。
	member := env.client(dvMemberID)
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path, map[string]any{"replaced_device_id": oldDevice, "extra": 1})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path, map[string]any{})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path, map[string]any{"replaced_device_id": "nope"})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, path, map[string]any{"replaced_device_id": newDevice})
	member.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "devices/nope/replace", body)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path, map[string]any{"replaced_device_id": dvNewID(t)})

	// 旧记录在线：409 DEVICE_ONLINE。
	member.expectError(http.StatusConflict, "DEVICE_ONLINE", http.MethodPost, path, body)
	unchanged()
	env.setOffline(oldDevice)

	// 组织 ADMIN / OWNER 看得到组织授权的电脑，但不是所有者：403。
	env.client(dvAdminID).expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, path, body)
	env.client(dvOwnerID).expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, path, body)
	// 两台电脑只有一台属于调用者：403。
	foreign := env.insertDevice(dvAdminID, "ORGANIZATION", "")
	member.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, "devices/"+foreign+"/replace", body)
	member.expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, path, map[string]any{"replaced_device_id": foreign})
	unchanged()

	// 授权范围不同（旧的是组织授权，新的是个人授权）：409。
	personal := env.insertDevice(dvMemberID, "PERSONAL", dvMemberID)
	member.expectError(http.StatusConflict, "GRANT_SCOPE_MISMATCH", http.MethodPost, "devices/"+personal+"/replace", body)
	unchanged()

	// 新记录被停用：不可作为迁移目标。
	env.exec(`UPDATE openfox.physical_devices SET status = 'REVOKED' WHERE id = $1::uuid`, newDevice)
	member.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path, body)
	unchanged()
	env.exec(`UPDATE openfox.physical_devices SET status = 'ACTIVE' WHERE id = $1::uuid`, newDevice)

	// 没有可迁移的 Agent：照样成功，moved_agents = 0，旧授权被吊销。
	empty := env.insertDevice(dvMemberID, "ORGANIZATION", "")
	env.setOffline(empty)
	var result dvReplacement
	member.must(http.StatusOK, http.MethodPost, path, map[string]any{"replaced_device_id": empty}, &result)
	if result.MovedAgents != 0 || result.CanceledRuns != 0 || env.grantStatus(env.org, empty) != "REVOKED" {
		t.Fatalf("empty replace = %+v", result)
	}
	// 成功路径与审计字段形状（moved_agents 为整数）。
	var typed map[string]any
	env.client(dvMemberID).must(http.StatusOK, http.MethodPost, path, body, &typed)
	if _, ok := typed["moved_agents"].(float64); !ok || !reflect.DeepEqual(typed["replaced_device_id"], oldDevice) {
		t.Fatalf("response = %v", typed)
	}
}

func TestDevicesReplaceTooManyAgents(t *testing.T) {
	env := newDvEnv(t)
	shared := env.createSpace("shared")
	oldDevice := env.insertDevice(dvMemberID, "ORGANIZATION", "")
	newDevice := env.insertDevice(dvMemberID, "ORGANIZATION", "")
	env.setOffline(oldDevice)
	// 批量写 201 个带配置版本 1 的 Agent（只有当前配置版本存在的 Agent 才计入待迁移）。
	env.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, visibility, current_config_version)
		SELECT $1::uuid, gen_random_uuid(), $2::uuid, $3::uuid, $4::uuid, 'Bulk ' || n, 'SHARED', 1
		  FROM generate_series(1, 201) AS n`, env.org, oldDevice, shared, dvMemberID)
	env.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id, runtime, model, created_by)
		SELECT a.organization_id, a.id, 1, a.device_id, g.id, 'codex', 'gpt-5', $3::uuid
		  FROM openfox.agents a
		  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE a.organization_id = $1::uuid AND a.device_id = $2::uuid`, env.org, oldDevice, dvMemberID)
	env.client(dvMemberID).expectError(http.StatusConflict, "TOO_MANY_AGENTS", http.MethodPost,
		"devices/"+newDevice+"/replace", map[string]any{"replaced_device_id": oldDevice})
	if status := env.grantStatus(env.org, oldDevice); status != "ACTIVE" {
		t.Fatalf("old grant = %s", status)
	}
}
