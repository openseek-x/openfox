package stage12e2

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// 分享快照测试用的 Agent 夹具：直接写库（独立电脑在线 + 组织授权 + 配置版本 1）。

// seedAgent 在 spaceID 建一个可执行的 Agent。visibility 为 PRIVATE 时属于 owner。
func (e *qcEnv) seedAgent(spaceID, owner, visibility string) string {
	e.t.Helper()
	device, agent := qcNewID(e.t), qcNewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, last_seen_at) VALUES ($1::uuid, $2, 4, clock_timestamp())`,
		device, "s12e2-device-"+qcSuffix(e.t))
	e.exec(`INSERT INTO openfox.device_grants (organization_id, device_id) VALUES ($1::uuid, $2::uuid)`, e.org, device)
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, kind, visibility, status,
		current_config_version) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'CODE', $7, 'ACTIVE', 1)`,
		e.org, agent, device, spaceID, owner, "Agent "+qcSuffix(e.t), visibility)
	e.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 2, 3600, 600, $4::uuid
		  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, e.org, agent, device, qcOwnerID)
	return agent
}

// runMessageBody 是夹具里用户消息的正文；用于断言管理员接口不泄露会话内容。
const runMessageBody = "confidential request body 12e2"

// runFixture 是一个空间加一个可执行 Agent；每个 Run 用独立会话（同一会话只能有一个活动 Run）。
type runFixture struct {
	env          *qcEnv
	space, agent string
}

func (e *qcEnv) newRunFixture() *runFixture {
	e.t.Helper()
	space := e.createSpace(nil)
	return &runFixture{env: e, space: space.ID, agent: e.seedAgent(space.ID, qcOwnerID, "PRIVATE")}
}

// device 返回夹具 Agent 所在电脑。
func (f *runFixture) device() string {
	f.env.t.Helper()
	var device string
	f.env.scalar(&device, `SELECT device_id::text FROM openfox.agents WHERE id = $1::uuid`, f.agent)
	return device
}

// sendMessage 由 owner 新建会话并 @Agent 发一条消息（产生一条 PENDING 输入），返回会话 ID。
func (f *runFixture) sendMessage() string {
	e := f.env
	e.t.Helper()
	var conv struct {
		ID string `json:"id"`
	}
	e.owner().must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": f.space, "agent_id": f.agent}, &conv)
	e.owner().must(http.StatusCreated, http.MethodPost, "conversations/"+conv.ID+"/messages",
		map[string]any{"id": qcNewID(e.t), "client_message_id": qcNewID(e.t), "body": runMessageBody, "mentions": []map[string]string{{"agent_id": f.agent}}}, nil)
	return conv.ID
}

// seedRun 直接写一个指定状态的 Run 并把会话输入挂到它上面（不启动派发器）。
// reconciling 为真时置待核对标记并让租约过期。
func (f *runFixture) seedRun(state string, reconciling bool) string {
	e := f.env
	e.t.Helper()
	conversation := f.sendMessage()
	run := qcNewID(e.t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version,
			actor_id, state, generation, reconciliation_required, started_at, lease_expires_at)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, a.device_id, g.id, 1, 1, $6::uuid, $7, 1, $8,
		       CASE WHEN $7 = 'QUEUED' THEN NULL ELSE clock_timestamp() - interval '10 minutes' END,
		       CASE WHEN $7 = 'QUEUED' THEN NULL ELSE clock_timestamp() - interval '5 minutes' END
		  FROM openfox.agents a JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE a.organization_id = $1::uuid AND a.id = $5::uuid`, e.org, run, f.space, conversation, f.agent, qcOwnerID, state, reconciling)
	e.exec(`UPDATE openfox.conversation_inputs SET status = 'ASSIGNED', run_id = $2::uuid, assigned_at = clock_timestamp()
		 WHERE organization_id = $1::uuid AND conversation_id = $3::uuid`, e.org, run, conversation)
	return run
}

// ---- 待核对的 Run、核对案与证据 ----

type reconcilingRun struct {
	fixture     *runFixture
	run, caseID string
}

// seedReconciling 写一个运行中、待核对的 Run 及其开放核对案（原因：租约过期）。
func (f *runFixture) seedReconciling() reconcilingRun {
	e := f.env
	e.t.Helper()
	run := f.seedRun("RUNNING", true)
	caseID := qcNewID(e.t)
	e.exec(`INSERT INTO openfox.run_reconciliation_cases (organization_id, id, run_id, generation, reason_code, failure_code, safe_detail)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 1, 'LEASE_EXPIRED', 'UNKNOWN_PROCESS', 'lease expired')`, e.org, caseID, run)
	return reconcilingRun{fixture: f, run: run, caseID: caseID}
}

func hashOf(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// seedEvidence 写一条 daemon 证据。withResult 为真时带结果 ID（结果确认类证据）。
func (r reconcilingRun) seedEvidence(kind, acceptedState string, withResult bool, detail string) string {
	e := r.fixture.env
	e.t.Helper()
	id := qcNewID(e.t)
	var resultID any
	if withResult {
		resultID = qcNewID(e.t)
	}
	state := map[string]string{"RESULT_CONFIRMED": "EXITED", "PROCESS_NOT_FOUND": "UNKNOWN", "HEARTBEAT": "RUNNING"}[kind]
	e.exec(`INSERT INTO openfox.run_reconciliation_evidence (organization_id, id, case_id, run_id, generation, device_generation,
			center_epoch, kind, source, device_id, supervisor_nonce, process_pid, process_state, process_exited, result_id,
			payload_hash, safe_detail, accepted_state, observed_at)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 1, 1, 1, $5, 'DAEMON', $6::uuid, $7, 4242, $8, false, $9::uuid,
		 $10, $11, $12, clock_timestamp())`,
		e.org, id, r.caseID, r.run, kind, r.fixture.device(), "nonce-SECRET-"+id, state, resultID, hashOf(id), detail, acceptedState)
	return id
}

func (r reconcilingRun) path(suffix string) string {
	return "runs/" + r.run + "/reconciliation" + suffix
}

func (r reconcilingRun) runState() (state string, reconciling bool) {
	e := r.fixture.env
	e.t.Helper()
	if err := e.superuser.QueryRow(e.ctx, `SELECT state, reconciliation_required FROM openfox.runs WHERE id = $1::uuid`, r.run).Scan(&state, &reconciling); err != nil {
		e.t.Fatal(err)
	}
	return state, reconciling
}

func (r reconcilingRun) resolutionAudits() int {
	e := r.fixture.env
	return e.count(`SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid
		AND action = 'run.reconciliation_resolved' AND resource_id = $2`, e.org, r.caseID)
}
