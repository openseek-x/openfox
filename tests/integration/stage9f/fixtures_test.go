package stage9f

// 快捷指令测试用的 Agent / 小队 / 派发辅助。Agent 直接写库（独立电脑在线 + 组织授权 + 配置版本 1），可被派发执行。

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/dispatch"
	"openfox/internal/tenancy"
)

// seedAgent 在 spaceID 建一个可执行的 Agent。visibility 为 PRIVATE 时属于 owner。
func (e *qcEnv) seedAgent(spaceID, owner, visibility string) string {
	e.t.Helper()
	device, agent := qcNewID(e.t), qcNewID(e.t)
	e.exec(`INSERT INTO openfox.physical_devices (id, device_key, max_slots, last_seen_at) VALUES ($1::uuid, $2, 4, clock_timestamp())`,
		device, "s9f-device-"+qcSuffix(e.t))
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

// seedCrew 在 spaceID 建小队（第一个成员为队长）。
func (e *qcEnv) seedCrew(spaceID, owner, visibility string, members ...string) string {
	e.t.Helper()
	crew := qcNewID(e.t)
	leader := any(nil)
	if len(members) > 0 {
		leader = members[0]
	}
	e.exec(`INSERT INTO openfox.agent_crews (organization_id, id, space_id, owner_user_id, name, visibility, leader_agent_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7::uuid)`, e.org, crew, spaceID, owner, "Crew "+qcSuffix(e.t), visibility, leader)
	for position, agent := range members {
		e.exec(`INSERT INTO openfox.agent_crew_members (organization_id, crew_id, agent_id, position) VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`,
			e.org, crew, agent, position)
	}
	return crew
}

// dispatchOnce 跑一轮派发（把会话输入物化成 Run）。
func (e *qcEnv) dispatchOnce() {
	e.t.Helper()
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	worker := &dispatch.Worker{Pool: e.database, Store: store, Hub: dispatch.NewHub()}
	if _, err := worker.RunOrganization(e.ctx, e.org); err != nil {
		e.t.Fatal(err)
	}
}

type qcTarget struct {
	Kind              string          `json:"kind"`
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Avatar            json.RawMessage `json:"avatar"`
	Available         bool            `json:"available"`
	UnavailableReason string          `json:"unavailable_reason"`
}

type qcCommand struct {
	ID          string   `json:"id"`
	SpaceID     string   `json:"space_id"`
	OwnerUserID string   `json:"owner_user_id"`
	Visibility  string   `json:"visibility"`
	Name        string   `json:"name"`
	IconKind    *string  `json:"icon_kind"`
	Icon        *string  `json:"icon"`
	IconURL     *string  `json:"icon_url"`
	AgentID     *string  `json:"agent_id"`
	CrewID      *string  `json:"crew_id"`
	Target      qcTarget `json:"target"`
	Prompt      string   `json:"prompt"`
	Position    int      `json:"position"`
	Version     int      `json:"version"`
	CanEdit     bool     `json:"can_edit"`
}

type qcList struct {
	Private []qcCommand `json:"private"`
	Shared  []qcCommand `json:"shared"`
}

func commandsPath(spaceID string) string { return "spaces/" + spaceID + "/quick-commands" }

func commandIDs(items []qcCommand) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}

func (c *qcClient) list(t *testing.T, spaceID string) qcList {
	t.Helper()
	var out qcList
	c.must(http.StatusOK, http.MethodGet, commandsPath(spaceID), nil, &out)
	return out
}

// runTaskContext 以执行电脑身份领取 Run 并读取运行上下文，返回其中的任务快照（没有时为 nil）。
func (e *qcEnv) runTaskContext(runID string) *dispatch.RunTask {
	e.t.Helper()
	var offerID, deviceID string
	e.scalar(&offerID, `SELECT COALESCE(offer_id::text, '') FROM openfox.runs WHERE id = $1::uuid`, runID)
	e.scalar(&deviceID, `SELECT computer_id::text FROM openfox.runs WHERE id = $1::uuid`, runID)
	if offerID == "" {
		e.t.Fatalf("run %s has no offer", runID)
	}
	store, err := dispatch.NewStore(dispatch.Config{LeaseTTL: 30 * time.Second, LeaseRenew: 5 * time.Second})
	if err != nil {
		e.t.Fatal(err)
	}
	selection := tenancy.OrganizationSelection{OrganizationID: e.org, ActorID: deviceID, AuthorizationVersion: 1}
	selector := tenancy.OrganizationSelectorFunc(func(context.Context, string) (tenancy.OrganizationSelection, error) {
		return selection, nil
	})
	var result dispatch.RunContext
	err = e.database.RunForOrganization(e.ctx, e.org, tenancy.SourceDeviceRequest, selector, func(ctx context.Context, tx pgx.Tx) error {
		lease, err := store.Claim(ctx, tx, e.org, deviceID, offerID)
		if err != nil {
			return err
		}
		result, err = store.Context(ctx, tx, e.org, deviceID, lease.RunID, lease.Generation)
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return result.Task
}

func (c *qcClient) create(t *testing.T, spaceID string, body map[string]any) qcCommand {
	t.Helper()
	var out qcCommand
	c.must(http.StatusCreated, http.MethodPost, commandsPath(spaceID), body, &out)
	return out
}
