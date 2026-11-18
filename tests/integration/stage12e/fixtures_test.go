package stage12e

// 阶段 12e 设备侧夹具：真实注册 → 挑战签名 → 会话令牌；本机配置目录放在 t.TempDir()（不触碰真实 ~/Library）。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/dispatch"
	"openfox/internal/platform/postgres"
)

type devDevice struct {
	ID          string
	KeyID       string
	CenterEpoch int64
	private     ed25519.PrivateKey
	session     string
}

// anonymous 发送设备侧请求（bearer 为空时不带 Authorization）。
func (e *qcEnv) anonymous(method, path, bearer string, body any) qcResponse {
	e.t.Helper()
	client := &qcClient{env: e, http: http.DefaultClient}
	headers := []string{}
	if bearer != "" {
		headers = append(headers, "Authorization", "Bearer "+bearer)
	}
	return client.do(method, path, body, headers...)
}

// registerDevice 由 issuer 在 org 签发组织级注册令牌并注册一台新电脑（所有者 = issuer），换取设备会话。
func (e *qcEnv) registerDevice(issuer *qcClient, org string) devDevice {
	e.t.Helper()
	var token struct {
		InstallToken string `json:"install_token"`
	}
	issuer.must(http.StatusCreated, http.MethodPost, in(org, "devices/registration-tokens"),
		map[string]any{"name": "s12e-" + qcSuffix(e.t), "scope": "ORGANIZATION"}, &token)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	device := devDevice{KeyID: daemon.DeviceKeyID(public), private: private}
	var registered struct {
		DeviceID    string `json:"device_id"`
		CenterEpoch int64  `json:"center_epoch"`
	}
	response := e.anonymous(http.MethodPost, "/v1/device/register", "", map[string]string{
		"token": token.InstallToken, "key_id": device.KeyID, "public_key": deviceauth.EncodeBase64URL(public),
	})
	if response.Status != http.StatusCreated {
		e.t.Fatalf("register = %d %s", response.Status, response.Body)
	}
	response.decode(e.t, &registered)
	device.ID, device.CenterEpoch = registered.DeviceID, registered.CenterEpoch
	session, err := e.auth().Session(e.ctx, device.ID, device.KeyID, device.private)
	if err != nil {
		e.t.Fatalf("session: %v", err)
	}
	device.session = session.Token
	return device
}

// auth 是指向测试服务器的设备认证客户端（测试服务器路由不带 /api 前缀）。
func (e *qcEnv) auth() daemon.AuthClient {
	return daemon.AuthClient{BaseURL: e.server.URL, HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (e *qcEnv) heartbeat(device devDevice, token string, capabilities map[string]any) qcResponse {
	e.t.Helper()
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	return e.anonymous(http.MethodPost, "/v1/device/heartbeat", token, map[string]any{
		"center_epoch": device.CenterEpoch, "protocol_major": 1, "protocol_minor": 0,
		"daemon_version": "test", "operating_system": "darwin", "architecture": "arm64", "capabilities": capabilities,
	})
}

func (e *qcEnv) mustHeartbeat(device devDevice, capabilities map[string]any) {
	e.t.Helper()
	if response := e.heartbeat(device, device.session, capabilities); response.Status != http.StatusOK {
		e.t.Fatalf("heartbeat = %d %s", response.Status, response.Body)
	}
}

// writeDaemonConfig 在临时目录写一份本机配置（与 `openfox computer register` 写出的格式相同）。
func (e *qcEnv) writeDaemonConfig(device devDevice, org string) string {
	e.t.Helper()
	dir := filepath.Join(e.t.TempDir(), "OpenFox")
	config := daemon.DeviceConfig{
		Version: daemon.DeviceConfigVersion, CenterURL: e.server.URL, WSSURL: strings.Replace(e.server.URL, "http://", "ws://", 1) + "/v1/device/connect",
		DeviceID: device.ID, OrganizationID: org, KeyID: device.KeyID,
		PrivateKeyFile: filepath.Join(dir, daemon.DeviceKeyFileName), TokenFile: filepath.Join(dir, daemon.DeviceTokenFileName),
		WorkspaceRoot: filepath.Join(dir, "workspaces"), CenterEpoch: device.CenterEpoch,
	}
	if err := daemon.WriteDeviceKey(config.PrivateKeyFile, device.private); err != nil {
		e.t.Fatal(err)
	}
	if err := daemon.WritePrivateFile(config.TokenFile, []byte(device.session+"\n")); err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(dir, daemon.DeviceConfigFileName)
	if err := daemon.WriteDeviceConfig(path, config); err != nil {
		e.t.Fatal(err)
	}
	return path
}

// keyAccepted 报告中心是否接受用 (keyID, private) 换会话。
func (e *qcEnv) keyAccepted(deviceID, keyID string, private ed25519.PrivateKey) bool {
	_, err := e.auth().Session(e.ctx, deviceID, keyID, private)
	return err == nil
}

// localKey 读取本机配置当前使用的密钥。
func localKey(t *testing.T, path string) (daemon.DeviceConfig, ed25519.PrivateKey) {
	t.Helper()
	config, err := daemon.LoadDeviceConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	private, err := daemon.ReadDeviceKey(config.PrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	return config, private
}

// ---- 派发夹具 ----

func (e *qcEnv) worker() *dispatch.Worker {
	e.t.Helper()
	database, err := postgres.New(e.ctx, postgres.Config{
		URL: os.Getenv("OPENFOX_TEST_DATABASE_URL"), MaxConns: 4, ContextRole: "openfox_context", AppRole: "openfox_app",
		StatementTimeout: 10 * time.Second, LockTimeout: 10 * time.Second,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(database.Close)
	store, err := dispatch.NewStore(dispatch.Config{})
	if err != nil {
		e.t.Fatal(err)
	}
	return &dispatch.Worker{Pool: database, Store: store}
}

// seedAgent 由 creator（org 的 OWNER）经接口在 org 建空间，再直接写库建一个绑定 deviceID 的共享 Agent（配置版本 1）。
func (e *qcEnv) seedAgent(creator *qcClient, org, ownerID, deviceID string) (space, agent string) {
	e.t.Helper()
	agent = qcNewID(e.t)
	e.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position)
		SELECT $1::uuid, 'Todo', '', 'TODO', 0 WHERE NOT EXISTS (SELECT 1 FROM openfox.task_statuses WHERE organization_id = $1::uuid)`, org)
	e.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 32)
		ON CONFLICT (organization_id) DO UPDATE SET max_slots = 32`, org)
	var created struct {
		ID string `json:"id"`
	}
	creator.must(http.StatusCreated, http.MethodPost, in(org, "spaces"),
		map[string]any{"name": "Space " + qcSuffix(e.t), "prefix": qcPrefix(e.t), "visibility": "shared"}, &created)
	space = created.ID
	e.exec(`INSERT INTO openfox.agents (organization_id, id, device_id, space_id, owner_user_id, name, kind, visibility, status,
		current_config_version) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'CODE', 'SHARED', 'ACTIVE', 1)`,
		org, agent, deviceID, space, ownerID, "Agent "+qcSuffix(e.t))
	e.exec(`INSERT INTO openfox.agent_config_versions (organization_id, agent_id, config_version, computer_id, grant_id,
		runtime, model, max_concurrency, queue_timeout_seconds, run_timeout_seconds, created_by)
		SELECT $1::uuid, $2::uuid, 1, $3::uuid, g.id, 'codex', 'default', 8, 3600, 600, $4::uuid
		  FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $3::uuid`, org, agent, deviceID, ownerID)
	return space, agent
}

// queueRun 新建一个会话并写一条 QUEUED Run（不经消息物化）。
func (e *qcEnv) queueRun(org, space, agent, actor string) string {
	e.t.Helper()
	conversation, run := qcNewID(e.t), qcNewID(e.t)
	e.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'SHARED')`, org, conversation, space, agent, actor)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id,
			config_version, grant_version, actor_id, state)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, a.id, a.device_id, g.id, 1, g.permission_version, $5::uuid, 'QUEUED'
		  FROM openfox.agents a JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE a.organization_id = $1::uuid AND a.id = $6::uuid`, org, run, space, conversation, actor, agent)
	return run
}

// pass 跑一轮组织扫描，返回本轮新建 offer 的 run_id 集合。
func (e *qcEnv) pass(worker *dispatch.Worker, org string) map[string]string {
	e.t.Helper()
	result, err := worker.RunOrganization(e.ctx, org)
	if err != nil {
		e.t.Fatalf("RunOrganization: %v", err)
	}
	offers := map[string]string{}
	for _, offer := range result.Offers {
		offers[offer.RunID] = offer.OfferID
	}
	return offers
}

func (e *qcEnv) waitReason(org, run string) string {
	e.t.Helper()
	var reason string
	e.scalar(&reason, `SELECT wait_reason FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, org, run)
	return reason
}

// deviceDetail 读取电脑详情的若干阶段 12e 字段。
type deviceDetail struct {
	ID                  string          `json:"id"`
	MaxSlots            int             `json:"max_slots"`
	LocalMaxRuns        *int            `json:"local_max_runs"`
	EffectiveMaxSlots   int             `json:"effective_max_slots"`
	ReconcilingRuns     int             `json:"reconciling_runs"`
	GrantStatus         string          `json:"grant_status"`
	Capabilities        json.RawMessage `json:"capabilities"`
	RuntimeConfigStatus *struct {
		AppliedVersion  int64   `json:"applied_version"`
		RestartRequired bool    `json:"restart_required"`
		Error           *string `json:"error"`
	} `json:"runtime_config_status"`
}

func (e *qcEnv) device(client *qcClient, org, id string) deviceDetail {
	e.t.Helper()
	var detail deviceDetail
	client.must(http.StatusOK, http.MethodGet, in(org, "devices/"+id), nil, &detail)
	return detail
}

// withTimeout 给单个并发子任务一个上限，避免测试挂死。
func withTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 30*time.Second)
}
