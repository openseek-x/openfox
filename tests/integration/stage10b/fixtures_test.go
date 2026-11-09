package stage10b

// Agent 与派发辅助（复制自 stage9f）。Agent 直接写库（独立电脑在线 + 组织授权 + 配置版本 1），可被派发执行。

// seedAgent 在 spaceID 建一个可执行的 Agent。visibility 为 PRIVATE 时属于 owner。
func (e *qcEnv) seedAgent(spaceID, owner, visibility string) string {
	e.t.Helper()
	device, agent := qcNewID(e.t), qcNewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, last_seen_at) VALUES ($1::uuid, $2, 4, clock_timestamp())`,
		device, "s10b-device-"+qcSuffix(e.t))
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

// dispatchOnce 跑一轮派发（处理指派事件、把会话输入物化成 Run）。
func (e *qcEnv) dispatchOnce() {
	e.t.Helper()
	if err := e.tryDispatch(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}
