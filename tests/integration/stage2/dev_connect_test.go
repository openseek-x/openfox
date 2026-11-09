package stage2

// 连接电脑与 Runtime 的中心侧集成测试：注册令牌范围与权限、v2 安装令牌兑换、
// Run 上下文新字段（环境变量只给执行设备）、远程命令（所有者限定、复用、CAS 领取、过期、幂等回执）、心跳 storage。
// 复用 agEnv（每个测试新建两个组织）；设备侧用真实的注册 → 挑战签名 → 会话令牌流程。

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/deviceauth"
	"openfox/internal/devices"
)

type devToken struct {
	ID           string `json:"id"`
	Token        string `json:"token"`
	InstallToken string `json:"install_token"`
	Command      string `json:"command"`
	Scope        string `json:"scope"`
	ExpiresAt    string `json:"expires_at"`
}

// devDevice 是一台经接口注册的电脑及其设备会话。
type devDevice struct {
	ID          string
	KeyID       string
	CenterEpoch int64
	private     ed25519.PrivateKey
	session     string
}

type devCommand struct {
	ID          string          `json:"id"`
	DeviceID    string          `json:"device_id"`
	Kind        string          `json:"kind"`
	Params      json.RawMessage `json:"params"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   string          `json:"error_code"`
	DeliveredAt *time.Time      `json:"delivered_at"`
	CompletedAt *time.Time      `json:"completed_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

type devCommandEnvelope struct {
	Command devCommand `json:"command"`
}

func (e *agEnv) issueToken(client *agClient, scope string) devToken {
	e.t.Helper()
	return e.issueTokenIn(client, e.org, scope)
}

// issueTokenIn 在指定组织签发注册令牌（outsider 只能在 otherOrg 签发）。
func (e *agEnv) issueTokenIn(client *agClient, org, scope string) devToken {
	e.t.Helper()
	body := map[string]any{"name": "dev-" + agSuffix(e.t)}
	if scope != "" {
		body["scope"] = scope
	}
	response := client.doIn(org, http.MethodPost, "devices/registration-tokens", body)
	if response.Status != http.StatusCreated {
		e.t.Fatalf("issue token = %d %s", response.Status, response.Body)
	}
	var token devToken
	response.decode(e.t, &token)
	return token
}

// anonymous 发送不带用户会话的请求（设备侧）；bearer 为空时不带 Authorization。
func (e *agEnv) anonymous(method, path, bearer string, body any) agResponse {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(e.ctx, method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		e.t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return agResponse{Status: response.StatusCode, Header: response.Header, Body: payload}
}

// tryRegister 用 token（v2 或旧令牌）注册一台新电脑；不检查状态码。
func (e *agEnv) tryRegister(token string) (agResponse, devDevice) {
	e.t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	device := devDevice{KeyID: "dev-key-" + agSuffix(e.t), private: private}
	response := e.anonymous(http.MethodPost, "/v1/device/register", "", map[string]string{
		"token": token, "key_id": device.KeyID, "public_key": deviceauth.EncodeBase64URL(public),
	})
	if response.Status == http.StatusCreated {
		var result struct {
			DeviceID    string `json:"device_id"`
			CenterEpoch int64  `json:"center_epoch"`
		}
		response.decode(e.t, &result)
		device.ID, device.CenterEpoch = result.DeviceID, result.CenterEpoch
	}
	return response, device
}

// registerDevice 注册并换取设备会话令牌。
func (e *agEnv) registerDevice(token string) devDevice {
	e.t.Helper()
	response, device := e.tryRegister(token)
	if response.Status != http.StatusCreated {
		e.t.Fatalf("register = %d %s", response.Status, response.Body)
	}
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		Challenge   string `json:"challenge"`
	}
	issued := e.anonymous(http.MethodPost, "/v1/device/auth/challenge", "", map[string]string{"device_id": device.ID, "key_id": device.KeyID})
	if issued.Status != http.StatusCreated {
		e.t.Fatalf("challenge = %d %s", issued.Status, issued.Body)
	}
	issued.decode(e.t, &challenge)
	nonce, err := deviceauth.DecodeBase64URL(challenge.Nonce, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	payload, err := deviceauth.DecodeBase64URL(challenge.Challenge, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	signature := ed25519.Sign(device.private, deviceauth.CanonicalPayload(challenge.ChallengeID, device.ID, device.KeyID, nonce, payload))
	session := e.anonymous(http.MethodPost, "/v1/device/auth/token", "", map[string]string{
		"challenge_id": challenge.ChallengeID, "device_id": device.ID, "key_id": device.KeyID,
		"signature": deviceauth.EncodeBase64URL(signature),
	})
	if session.Status != http.StatusCreated {
		e.t.Fatalf("token = %d %s", session.Status, session.Body)
	}
	var result struct {
		Token string `json:"token"`
	}
	session.decode(e.t, &result)
	device.session = result.Token
	return device
}

func (e *agEnv) deviceCall(device devDevice, method, path string, body any) agResponse {
	e.t.Helper()
	return e.anonymous(method, path, device.session, body)
}

func expectCode(t *testing.T, response agResponse, status int, code string) {
	t.Helper()
	if response.Status != status || (code != "" && response.code() != code) {
		t.Fatalf("response = %d %s, want %d %s", response.Status, response.Body, status, code)
	}
}

func TestDevRegistrationTokenScopeMatrix(t *testing.T) {
	e := newAgEnv(t)
	type expectation struct {
		status int
		scope  string
	}
	cases := map[string]map[string]expectation{
		agGuestID:  {"": {http.StatusForbidden, ""}, "PERSONAL": {http.StatusForbidden, ""}, "ORGANIZATION": {http.StatusForbidden, ""}},
		agMemberID: {"": {http.StatusCreated, "PERSONAL"}, "PERSONAL": {http.StatusCreated, "PERSONAL"}, "ORGANIZATION": {http.StatusForbidden, ""}},
		agAdminID:  {"": {http.StatusCreated, "ORGANIZATION"}, "PERSONAL": {http.StatusCreated, "PERSONAL"}, "ORGANIZATION": {http.StatusCreated, "ORGANIZATION"}},
		agOwnerID:  {"": {http.StatusCreated, "ORGANIZATION"}, "PERSONAL": {http.StatusCreated, "PERSONAL"}, "ORGANIZATION": {http.StatusCreated, "ORGANIZATION"}},
	}
	for userID, byScope := range cases {
		client := e.client(userID)
		for scope, want := range byScope {
			body := map[string]any{"name": "matrix-" + agSuffix(t)}
			if scope != "" {
				body["scope"] = scope
			}
			response := client.do(http.MethodPost, "devices/registration-tokens", body)
			if response.Status != want.status {
				t.Fatalf("%s scope=%q status = %d %s, want %d", agRoles[userID], scope, response.Status, response.Body, want.status)
			}
			if want.status != http.StatusCreated {
				continue
			}
			var token devToken
			response.decode(t, &token)
			if token.Scope != want.scope {
				t.Fatalf("%s scope=%q got scope %q, want %q", agRoles[userID], scope, token.Scope, want.scope)
			}
			decoded, err := devices.DecodeInstallToken(token.InstallToken)
			if err != nil || decoded.Secret != token.Token || decoded.CenterURL != "http://127.0.0.1:8080" {
				t.Fatalf("install token = %+v, %v", decoded, err)
			}
			if token.Command != "openfox computer register --install-token "+token.InstallToken {
				t.Fatalf("command = %q", token.Command)
			}
			var stored string
			e.scalar(&stored, `SELECT scope FROM openfox.device_registration_tokens WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, token.ID)
			if stored != want.scope {
				t.Fatalf("stored scope = %q, want %q", stored, want.scope)
			}
		}
	}
	e.client(agOwnerID).expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "devices/registration-tokens",
		map[string]any{"name": "bad", "scope": "SPACE"})
	// 非成员（另一个组织的 owner）看不到这个组织。
	e.client(agOutsiderID).expectError(http.StatusNotFound, "", http.MethodPost, "devices/registration-tokens",
		map[string]any{"name": "outsider"})
}

func TestDevRegisterGrantScopeAndLegacyToken(t *testing.T) {
	e := newAgEnv(t)
	// 成员签发个人令牌，用 v2 安装令牌兑换：电脑归成员所有，授权只给成员本人。
	personal := e.issueToken(e.client(agMemberID), "")
	member := e.registerDevice(personal.InstallToken)
	var scope, scopeUser, owner string
	e.scalar(&scope, `SELECT g.scope FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $2::uuid`, e.org, member.ID)
	e.scalar(&scopeUser, `SELECT COALESCE(g.scope_user_id::text, '') FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $2::uuid`, e.org, member.ID)
	e.scalar(&owner, `SELECT owner_user_id::text FROM openfox.physical_devices WHERE id = $1::uuid`, member.ID)
	if scope != "PERSONAL" || scopeUser != agMemberID || owner != agMemberID {
		t.Fatalf("personal grant = %s/%s owner %s", scope, scopeUser, owner)
	}
	var auditScope string
	e.scalar(&auditScope, `SELECT safe_metadata->>'grant_scope' FROM openfox.audit_events
		WHERE organization_id = $1::uuid AND action = 'device.registered' AND resource_id = $2`, e.org, member.ID)
	if auditScope != "PERSONAL" {
		t.Fatalf("audit grant_scope = %q", auditScope)
	}
	// 个人授权的电脑：别的成员（admin）看不到，成员本人能看到。
	e.client(agAdminID).expectError(http.StatusNotFound, "", http.MethodGet, "devices/"+member.ID, nil)
	e.client(agMemberID).must(http.StatusOK, http.MethodGet, "devices/"+member.ID, nil, nil)

	// 管理员签发组织令牌，用旧的纯令牌兑换（兼容旧 CLI）。
	organization := e.issueToken(e.client(agAdminID), "ORGANIZATION")
	legacy := e.registerDevice(organization.Token)
	e.scalar(&scope, `SELECT g.scope FROM openfox.device_grants g WHERE g.organization_id = $1::uuid AND g.device_id = $2::uuid`, e.org, legacy.ID)
	if scope != "ORGANIZATION" {
		t.Fatalf("organization grant scope = %s", scope)
	}
	// 令牌一次性：v2 与旧格式都不能再用。
	used, _ := e.tryRegister(organization.InstallToken)
	expectCode(t, used, http.StatusConflict, "REGISTRATION_CONFLICT")
	used, _ = e.tryRegister(personal.Token)
	expectCode(t, used, http.StatusConflict, "REGISTRATION_CONFLICT")
	// 格式错误的 v2 令牌是 400，不回显令牌。
	malformed, _ := e.tryRegister(devices.InstallTokenPrefix + "***")
	expectCode(t, malformed, http.StatusBadRequest, "INVALID_REQUEST")
	if strings.Contains(string(malformed.Body), "***") {
		t.Fatal("error echoes the token")
	}
}

func TestDevRegisterRejectedAfterIssuerDemotion(t *testing.T) {
	e := newAgEnv(t)
	organization := e.issueToken(e.client(agAdminID), "ORGANIZATION")
	personal := e.issueToken(e.client(agMemberID), "PERSONAL")
	e.exec(`UPDATE openfox.organization_members SET role = 'MEMBER' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, e.org, agAdminID)
	e.exec(`UPDATE openfox.organization_members SET role = 'GUEST' WHERE organization_id = $1::uuid AND user_id = $2::uuid`, e.org, agMemberID)
	response, _ := e.tryRegister(organization.InstallToken)
	expectCode(t, response, http.StatusConflict, "REGISTRATION_CONFLICT")
	response, _ = e.tryRegister(personal.InstallToken)
	expectCode(t, response, http.StatusConflict, "REGISTRATION_CONFLICT")
	var consumed int
	e.scalar(&consumed, `SELECT count(*) FROM openfox.device_registration_tokens
		WHERE organization_id = $1::uuid AND consumed_at IS NOT NULL`, e.org)
	if consumed != 0 {
		t.Fatalf("consumed tokens = %d", consumed)
	}
	// 降级后的管理员仍可签发个人令牌，但不能签发组织令牌。
	e.client(agAdminID).expectError(http.StatusForbidden, "FORBIDDEN", http.MethodPost, "devices/registration-tokens",
		map[string]any{"name": "demoted", "scope": "ORGANIZATION"})
}

func TestDevRegisterConcurrentExchangeOnce(t *testing.T) {
	e := newAgEnv(t)
	token := e.issueToken(e.client(agOwnerID), "")
	const attempts = 8
	statuses := make([]int, attempts)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := 0; index < attempts; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			public, _, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return
			}
			encoded, _ := json.Marshal(map[string]string{
				"token": token.InstallToken, "key_id": "race-" + agNewID(t)[:8], "public_key": deviceauth.EncodeBase64URL(public),
			})
			<-start
			response, err := http.Post(e.server.URL+"/v1/device/register", "application/json", bytes.NewReader(encoded))
			if err != nil {
				return
			}
			response.Body.Close()
			statuses[index] = response.StatusCode
		}(index)
	}
	close(start)
	wait.Wait()
	created, conflicts := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != attempts-1 {
		t.Fatalf("statuses = %v", statuses)
	}
	var devicesCreated int
	e.scalar(&devicesCreated, `SELECT count(*) FROM openfox.device_grants WHERE organization_id = $1::uuid`, e.org)
	// agEnv 自带 1 台电脑，加上兑换成功的 1 台。
	if devicesCreated != 2 {
		t.Fatalf("grants = %d", devicesCreated)
	}
}

// devRunFixture 建一个绑定到 device 的 Agent、一个会话和一条持有租约的 PREPARING Run。
func (e *agEnv) devRunFixture(device devDevice, configExtra map[string]any) (string, string, string) {
	e.t.Helper()
	var grantID string
	e.scalar(&grantID, `SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, device.ID)
	space := e.createSpace("shared")
	config := map[string]any{"computer_id": device.ID, "grant_id": grantID, "runtime": "codex", "model": "gpt-5"}
	for key, value := range configExtra {
		config[key] = value
	}
	var agent agAgent
	e.client(agOwnerID).must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/agents", map[string]any{
		"name": "Coder", "kind": "CODE", "visibility": "SHARED", "config": config,
	}, &agent)
	var conversation struct {
		ID string `json:"id"`
	}
	e.client(agOwnerID).must(http.StatusCreated, http.MethodPost, "conversations",
		map[string]any{"space_id": space, "agent_id": agent.ID}, &conversation)
	runID := agNewID(e.t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id,
			config_version, grant_version, actor_id, state, lease_expires_at)
		SELECT c.organization_id, $2::uuid, c.space_id, c.id, c.agent_id, a.device_id, g.id,
		       a.current_config_version, g.permission_version, $4::uuid, 'PREPARING', clock_timestamp() + interval '5 minutes'
		  FROM openfox.conversations c
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE c.organization_id = $1::uuid AND c.id = $3::uuid`, e.org, runID, conversation.ID, agOwnerID)
	return space, agent.ID, runID
}

func TestDevRunContextEnvironmentAndSettings(t *testing.T) {
	e := newAgEnv(t)
	executor := e.registerDevice(e.issueToken(e.client(agOwnerID), "ORGANIZATION").InstallToken)
	bystander := e.registerDevice(e.issueToken(e.client(agOwnerID), "ORGANIZATION").InstallToken)
	foreign := e.registerDevice(e.issueTokenIn(e.client(agOutsiderID), e.otherOrg, "").InstallToken)
	const secretValue = "sk-stage2c-PLAINTEXT-9876"
	space, agentID, runID := e.devRunFixture(executor, map[string]any{
		"workdir_isolation": "DIRECT", "custom_args": []string{"-c", "model_reasoning_effort=high", "--sandbox", "x"},
		"git_author_name_mode": "USER_AND_AGENT", "git_author_email_mode": "MANUAL", "git_author_email": "bot@example.test",
	})
	e.client(agOwnerID).must(http.StatusOK, http.MethodPut, agentPath(space, agentID)+"/env-vars/API_TOKEN",
		map[string]any{"value": secretValue}, nil)
	e.client(agOwnerID).must(http.StatusOK, http.MethodPut, agentPath(space, agentID)+"/env-vars/A_FIRST",
		map[string]any{"value": "first"}, nil)
	contextPath := "/v1/device/organizations/" + e.org + "/runs/" + runID + "/context?generation=1"

	response := e.deviceCall(executor, http.MethodGet, contextPath, nil)
	if response.Status != http.StatusOK {
		t.Fatalf("context = %d %s", response.Status, response.Body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header.Get("Cache-Control"))
	}
	var runContext struct {
		Environment []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"environment"`
		CustomArgs []string `json:"custom_args"`
		GitAuthor  *struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"git_author"`
		WorkdirIsolation string `json:"workdir_isolation"`
	}
	response.decode(t, &runContext)
	environment := make(map[string]string, len(runContext.Environment))
	for _, variable := range runContext.Environment {
		environment[variable.Name] = variable.Value
	}
	// Environment order follows database collation; delivery is keyed by name.
	if len(runContext.Environment) != 2 || environment["A_FIRST"] != "first" || environment["API_TOKEN"] != secretValue {
		t.Fatal("run context must deliver both environment variables with their original values")
	}
	if strings.Join(runContext.CustomArgs, " ") != "-c model_reasoning_effort=high --sandbox x" {
		t.Fatalf("custom_args = %v", runContext.CustomArgs)
	}
	ownerName := strings.SplitN(agEmail(agOwnerID), "@", 2)[0]
	if runContext.GitAuthor == nil || runContext.GitAuthor.Name != ownerName+" & Coder" || runContext.GitAuthor.Email != "bot@example.test" {
		t.Fatalf("git_author = %+v", runContext.GitAuthor)
	}
	if runContext.WorkdirIsolation != "DIRECT" {
		t.Fatalf("workdir_isolation = %q", runContext.WorkdirIsolation)
	}
	// 同组织的另一台电脑、另一个组织的电脑都拿不到。
	expectCode(t, e.deviceCall(bystander, http.MethodGet, contextPath, nil), http.StatusNotFound, "")
	expectCode(t, e.deviceCall(foreign, http.MethodGet, contextPath, nil), http.StatusNotFound, "")
	// 明文不进审计与事件。
	var leaks int
	e.scalar(&leaks, `SELECT (SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND safe_metadata::text LIKE '%' || $2 || '%')
		+ (SELECT count(*) FROM openfox.run_events WHERE organization_id = $1::uuid AND safe_payload::text LIKE '%' || $2 || '%')
		+ (SELECT count(*) FROM openfox.organization_events WHERE organization_id = $1::uuid AND payload::text LIKE '%' || $2 || '%')`,
		e.org, secretValue)
	if leaks != 0 {
		t.Fatalf("plaintext leaked into %d audit / event rows", leaks)
	}

	// 未配置主密钥的中心：有环境变量的 Agent → 409 SECRETS_UNAVAILABLE。
	request, err := http.NewRequestWithContext(e.ctx, http.MethodGet, e.noSecrets.URL+contextPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+executor.session)
	raw, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(raw.Body)
	raw.Body.Close()
	unavailable := agResponse{Status: raw.StatusCode, Body: body}
	expectCode(t, unavailable, http.StatusConflict, "SECRETS_UNAVAILABLE")
	if strings.Contains(string(body), secretValue) {
		t.Fatal("error body contains the secret")
	}
}

func TestDevRunContextDefaultsWithoutEnvironment(t *testing.T) {
	e := newAgEnv(t)
	executor := e.registerDevice(e.issueToken(e.client(agOwnerID), "ORGANIZATION").InstallToken)
	_, _, runID := e.devRunFixture(executor, map[string]any{"git_author_name_mode": "MACHINE", "git_author_email_mode": "MACHINE"})
	// 没有环境变量时，未配置主密钥的中心也能下发（空数组）。
	request, err := http.NewRequestWithContext(e.ctx, http.MethodGet,
		e.noSecrets.URL+"/v1/device/organizations/"+e.org+"/runs/"+runID+"/context?generation=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+executor.session)
	raw, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Body.Close()
	var runContext map[string]json.RawMessage
	if err := json.NewDecoder(raw.Body).Decode(&runContext); err != nil || raw.StatusCode != http.StatusOK {
		t.Fatalf("context = %d %v", raw.StatusCode, err)
	}
	if string(runContext["environment"]) != "[]" || string(runContext["custom_args"]) != "[]" ||
		string(runContext["git_author"]) != "null" || string(runContext["workdir_isolation"]) != `"WORKTREE"` {
		t.Fatalf("defaults = env %s args %s author %s isolation %s", runContext["environment"], runContext["custom_args"],
			runContext["git_author"], runContext["workdir_isolation"])
	}
}

func TestDevRunContextCrewInstructions(t *testing.T) {
	e := newAgEnv(t)
	executor := e.registerDevice(e.issueToken(e.client(agOwnerID), "ORGANIZATION").InstallToken)
	space, agentID, runID := e.devRunFixture(executor, map[string]any{"instructions": "Keep the Agent instructions."})
	path := "/v1/device/organizations/" + e.org + "/runs/" + runID + "/context?generation=1"
	readInstructions := func() string {
		t.Helper()
		response := e.deviceCall(executor, http.MethodGet, path, nil)
		response.mustStatus(t, http.StatusOK)
		var result struct {
			Instructions string `json:"instructions"`
		}
		response.decode(t, &result)
		return result.Instructions
	}
	if got := readInstructions(); got != "Keep the Agent instructions." {
		t.Fatalf("ordinary conversation instructions = %q", got)
	}
	owner := e.client(agOwnerID)
	var crew agCrew
	owner.must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/crews", map[string]any{
		"name": "Context crew", "leader_agent_id": agentID, "agent_ids": []string{agentID},
	}, &crew)
	crewPath := "spaces/" + space + "/crews/" + crew.ID
	owner.must(http.StatusOK, http.MethodPatch, crewPath, map[string]any{"instructions": "Return CREW_CONTEXT_731.", "version": crew.Version}, &crew)
	e.exec(`UPDATE openfox.conversations SET crew_id = $3::uuid WHERE organization_id = $1::uuid
		AND id = (SELECT conversation_id FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid)`, e.org, runID, crew.ID)
	if got := readInstructions(); !strings.Contains(got, "Keep the Agent instructions.") || !strings.Contains(got, "Return CREW_CONTEXT_731.") {
		t.Fatalf("crew and Agent instructions must both reach execution: %q", got)
	}
	owner.must(http.StatusOK, http.MethodPatch, crewPath, map[string]any{"instructions": "Return UPDATED_CREW_942.", "version": crew.Version}, &crew)
	if got := readInstructions(); !strings.Contains(got, "UPDATED_CREW_942") || strings.Contains(got, "CREW_CONTEXT_731") {
		t.Fatalf("crew instructions must refresh for the next execution: %q", got)
	}
	owner.must(http.StatusOK, http.MethodPatch, crewPath, map[string]any{"instructions": "", "version": crew.Version}, &crew)
	if got := readInstructions(); got != "Keep the Agent instructions." {
		t.Fatalf("empty crew instructions must preserve ordinary instructions: %q", got)
	}
	owner.must(http.StatusOK, http.MethodPatch, crewPath, map[string]any{"shared_task_dir": true, "version": crew.Version}, &crew)
	// Chat without a task keeps its private conversation workspace, including on old devices.
	e.deviceCall(executor, http.MethodGet, path, nil).mustStatus(t, http.StatusOK)
	var task struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/tasks", map[string]any{"title": "Shared directory task"}, &task)
	var conversation struct {
		ID string `json:"id"`
	}
	owner.must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space, "crew_id": crew.ID, "task_id": task.ID}, &conversation)
	sharedRunID := agNewID(t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, task_id, agent_id, computer_id, grant_id,
		config_version, grant_version, actor_id, state, lease_expires_at)
		SELECT organization_id, $3::uuid, space_id, $4::uuid, $5::uuid, agent_id, computer_id, grant_id,
		config_version, grant_version, actor_id, 'PREPARING', clock_timestamp() + interval '5 minutes'
		FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, runID, sharedRunID, conversation.ID, task.ID)
	path = "/v1/device/organizations/" + e.org + "/runs/" + sharedRunID + "/context?generation=1"
	expectCode(t, e.deviceCall(executor, http.MethodGet, path, nil), http.StatusConflict, "WORKSPACE_SHARING_UNSUPPORTED")
	response := e.deviceCall(executor, http.MethodGet, path+"&shared_task_dir=1", nil)
	response.mustStatus(t, http.StatusOK)
	var shared struct {
		CrewID string `json:"shared_task_crew_id"`
		TaskID string `json:"task_id"`
	}
	response.decode(t, &shared)
	if shared.CrewID != crew.ID || shared.TaskID != task.ID {
		t.Fatalf("shared task scope = %+v", shared)
	}
	owner.must(http.StatusOK, http.MethodPatch, crewPath, map[string]any{"shared_task_dir": false, "version": crew.Version}, &crew)
	response = e.deviceCall(executor, http.MethodGet, path, nil)
	response.mustStatus(t, http.StatusOK)
	shared.CrewID = ""
	response.decode(t, &shared)
	if shared.CrewID != "" {
		t.Fatal("disabled sharing still delivered a shared directory scope")
	}
}

func TestDevCommandAccessAndReuse(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	commandsPath := "devices/" + e.deviceID + "/commands"
	var first devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe"}, &first)
	if first.Command.Status != "PENDING" || first.Command.DeviceID != e.deviceID || string(first.Command.Params) != "{}" ||
		string(first.Command.Result) != "null" || first.Command.DeliveredAt != nil {
		t.Fatalf("created = %+v", first.Command)
	}
	if window := time.Until(first.Command.ExpiresAt); window < 90*time.Second || window > 2*time.Minute+5*time.Second {
		t.Fatalf("expires in %s", window)
	}
	// 同 kind 未完成：复用（200，同一个 ID）。
	var again devCommandEnvelope
	owner.must(http.StatusOK, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe", "params": map[string]any{}}, &again)
	if again.Command.ID != first.Command.ID {
		t.Fatalf("reuse id = %s, want %s", again.Command.ID, first.Command.ID)
	}
	// 并发创建同一种命令也只有一条。
	var wait sync.WaitGroup
	ids := make([]string, 6)
	for index := range ids {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response := owner.do(http.MethodPost, commandsPath, map[string]any{"kind": "storage_cleanup"})
			var envelope devCommandEnvelope
			if json.Unmarshal(response.Body, &envelope) == nil && (response.Status == http.StatusAccepted || response.Status == http.StatusOK) {
				ids[index] = envelope.Command.ID
			}
		}(index)
	}
	wait.Wait()
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("concurrent create ids = %v", ids)
		}
	}
	var open int
	e.scalar(&open, `SELECT count(*) FROM openfox.device_commands WHERE organization_id = $1::uuid AND device_id = $2::uuid
		AND status IN ('PENDING', 'DELIVERED')`, e.org, e.deviceID)
	if open != 2 {
		t.Fatalf("open commands = %d", open)
	}
	// runtime_test 参数规范化；未知键与错误 provider 400。
	var test devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{
		"kind": "runtime_test", "params": map[string]any{"provider": "codex", "model": " gpt-5 "},
	}, &test)
	var params map[string]string
	if err := json.Unmarshal(test.Command.Params, &params); err != nil || len(params) != 2 ||
		params["provider"] != "codex" || params["model"] != "gpt-5" {
		t.Fatalf("params = %s", test.Command.Params)
	}
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, commandsPath, map[string]any{"kind": "shell"})
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, commandsPath,
		map[string]any{"kind": "runtime_test", "params": map[string]any{"provider": "other"}})
	owner.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, commandsPath,
		map[string]any{"kind": "runtime_probe", "params": map[string]any{"cmd": "rm -rf /"}})
	// 非所有者（含组织管理员）与跨租户一律 404。
	commandPath := commandsPath + "/" + first.Command.ID
	for _, userID := range []string{agAdminID, agMemberID, agGuestID} {
		e.client(userID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe"})
		e.client(userID).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, commandPath, nil)
	}
	outsider := e.client(agOutsiderID)
	outsider.expectError(http.StatusNotFound, "", http.MethodGet, commandPath, nil)
	response := outsider.doIn(e.otherOrg, http.MethodGet, commandPath, nil)
	expectCode(t, response, http.StatusNotFound, "")
	response = outsider.doIn(e.otherOrg, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe"})
	expectCode(t, response, http.StatusNotFound, "")
	// 所有者读取。
	var read devCommandEnvelope
	owner.must(http.StatusOK, http.MethodGet, commandPath, nil, &read)
	if read.Command.ID != first.Command.ID || read.Command.Status != "PENDING" {
		t.Fatalf("read = %+v", read.Command)
	}
	// 授权吊销后所有者也是 404。
	e.exec(`UPDATE openfox.device_grants SET status = 'REVOKED'
		WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, e.deviceID)
	owner.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, commandPath, nil)
	var audits int
	e.scalar(&audits, `SELECT count(*) FROM openfox.audit_events WHERE organization_id = $1::uuid AND action LIKE 'device.command_requested.%'`, e.org)
	if audits != 3 {
		t.Fatalf("command audits = %d", audits)
	}
}

func TestDevCommandDeviceFlow(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	device := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	other := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	foreign := e.registerDevice(e.issueTokenIn(e.client(agOutsiderID), e.otherOrg, "").InstallToken)
	var created devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, "devices/"+device.ID+"/commands", map[string]any{"kind": "storage_cleanup"}, &created)
	commandID := created.Command.ID
	base := "/v1/device/organizations/" + e.org + "/commands"

	var pending struct {
		Commands []devCommand `json:"commands"`
	}
	listed := e.deviceCall(device, http.MethodGet, base+"?status=PENDING&limit=20", nil)
	if listed.Status != http.StatusOK {
		t.Fatalf("list = %d %s", listed.Status, listed.Body)
	}
	listed.decode(t, &pending)
	if len(pending.Commands) != 1 || pending.Commands[0].ID != commandID {
		t.Fatalf("pending = %+v", pending.Commands)
	}
	// 别的电脑看不到、领不到；别的组织的电脑在这个组织下 404。
	e.deviceCall(other, http.MethodGet, base, nil).decode(t, &pending)
	if len(pending.Commands) != 0 {
		t.Fatalf("other device sees %d commands", len(pending.Commands))
	}
	expectCode(t, e.deviceCall(other, http.MethodPost, base+"/"+commandID+"/claim", nil), http.StatusNotFound, "NOT_FOUND")
	expectCode(t, e.deviceCall(foreign, http.MethodGet, base, nil), http.StatusNotFound, "")
	expectCode(t, e.deviceCall(foreign, http.MethodPost, base+"/"+commandID+"/claim", nil), http.StatusNotFound, "")
	expectCode(t, e.deviceCall(device, http.MethodGet, base+"?status=DELIVERED", nil), http.StatusBadRequest, "INVALID_REQUEST")
	expectCode(t, e.anonymous(http.MethodGet, base, "", nil), http.StatusUnauthorized, "")
	// 回执在领取之前：409。
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+commandID+"/result",
		map[string]any{"status": "SUCCEEDED", "result": map[string]any{"freed_bytes": 0}}), http.StatusConflict, "COMMAND_CONFLICT")

	// 并发领取只有 1 次成功。
	const claims = 8
	statuses := make([]int, claims)
	codes := make([]string, claims)
	var wait sync.WaitGroup
	for index := 0; index < claims; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			response := e.deviceCall(device, http.MethodPost, base+"/"+commandID+"/claim", nil)
			statuses[index], codes[index] = response.Status, response.code()
		}(index)
	}
	wait.Wait()
	succeeded := 0
	for index, status := range statuses {
		if status == http.StatusOK {
			succeeded++
		} else if status != http.StatusConflict || codes[index] != "COMMAND_CLAIMED" {
			t.Fatalf("claim %d = %d %s", index, status, codes[index])
		}
	}
	if succeeded != 1 {
		t.Fatalf("claims succeeded = %d (%v)", succeeded, statuses)
	}
	var delivered devCommandEnvelope
	owner.must(http.StatusOK, http.MethodGet, "devices/"+device.ID+"/commands/"+commandID, nil, &delivered)
	if delivered.Command.Status != "DELIVERED" || delivered.Command.DeliveredAt == nil {
		t.Fatalf("delivered = %+v", delivered.Command)
	}
	// 领取后截止时间 = 领取 + 5 分钟执行超时 + 30 秒余量。
	if deadline := delivered.Command.ExpiresAt.Sub(*delivered.Command.DeliveredAt); deadline != 330*time.Second {
		t.Fatalf("execution deadline = %s", deadline)
	}
	// 非法回执。
	resultPath := base + "/" + commandID + "/result"
	expectCode(t, e.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED"}), http.StatusBadRequest, "INVALID_REQUEST")
	expectCode(t, e.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "FAILED"}), http.StatusBadRequest, "INVALID_REQUEST")
	expectCode(t, e.deviceCall(device, http.MethodPost, resultPath,
		map[string]any{"status": "FAILED", "error_code": "lower-case"}), http.StatusBadRequest, "INVALID_REQUEST")
	expectCode(t, e.deviceCall(device, http.MethodPost, resultPath,
		map[string]any{"status": "SUCCEEDED", "result": map[string]any{"blob": strings.Repeat("x", 17*1024)}}), http.StatusBadRequest, "INVALID_REQUEST")
	expectCode(t, e.deviceCall(other, http.MethodPost, resultPath,
		map[string]any{"status": "SUCCEEDED", "result": map[string]any{}}), http.StatusNotFound, "NOT_FOUND")
	// 成功回执，重复相同终态幂等，不同终态 409。
	result := map[string]any{"freed_bytes": 1024, "removed": map[string]any{"worktrees": 1, "codex_homes": 0, "journal_rows": 3}, "skipped_active": 1}
	var completed devCommandEnvelope
	response := e.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": result})
	if response.Status != http.StatusOK {
		t.Fatalf("result = %d %s", response.Status, response.Body)
	}
	response.decode(t, &completed)
	if completed.Command.Status != "SUCCEEDED" || completed.Command.CompletedAt == nil {
		t.Fatalf("completed = %+v", completed.Command)
	}
	expectCode(t, e.deviceCall(device, http.MethodPost, resultPath, map[string]any{"status": "SUCCEEDED", "result": result}), http.StatusOK, "")
	expectCode(t, e.deviceCall(device, http.MethodPost, resultPath,
		map[string]any{"status": "FAILED", "error_code": "CLEANUP_FAILED"}), http.StatusConflict, "COMMAND_CONFLICT")
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+commandID+"/claim", nil), http.StatusConflict, "COMMAND_CLAIMED")
	var final devCommandEnvelope
	owner.must(http.StatusOK, http.MethodGet, "devices/"+device.ID+"/commands/"+commandID, nil, &final)
	var finalResult struct {
		FreedBytes int `json:"freed_bytes"`
	}
	if err := json.Unmarshal(final.Command.Result, &finalResult); err != nil || final.Command.Status != "SUCCEEDED" || finalResult.FreedBytes != 1024 {
		t.Fatalf("final = %+v", final.Command)
	}
	// 完成后可再发起同种命令（新建）。
	var next devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, "devices/"+device.ID+"/commands", map[string]any{"kind": "storage_cleanup"}, &next)
	if next.Command.ID == commandID {
		t.Fatal("completed command was reused")
	}
	// FAILED 回执。
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+next.Command.ID+"/claim", nil), http.StatusOK, "")
	failed := e.deviceCall(device, http.MethodPost, base+"/"+next.Command.ID+"/result",
		map[string]any{"status": "FAILED", "error_code": "CLEANUP_FAILED", "result": nil})
	var failedEnvelope devCommandEnvelope
	failed.decode(t, &failedEnvelope)
	if failed.Status != http.StatusOK || failedEnvelope.Command.Status != "FAILED" || failedEnvelope.Command.ErrorCode != "CLEANUP_FAILED" {
		t.Fatalf("failed = %d %s", failed.Status, failed.Body)
	}
}

func TestDevCommandExpiry(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	device := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	base := "/v1/device/organizations/" + e.org + "/commands"
	commandsPath := "devices/" + device.ID + "/commands"
	age := func(commandID string) {
		e.exec(`UPDATE openfox.device_commands SET created_at = clock_timestamp() - interval '10 minutes',
			       expires_at = clock_timestamp() - interval '1 second'
			 WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, commandID)
	}
	// PENDING 过期：领取 409 COMMAND_EXPIRED，并落库为 EXPIRED。
	var probe devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe"}, &probe)
	age(probe.Command.ID)
	var pending struct {
		Commands []devCommand `json:"commands"`
	}
	e.deviceCall(device, http.MethodGet, base, nil).decode(t, &pending)
	if len(pending.Commands) != 0 {
		t.Fatalf("expired command listed: %+v", pending.Commands)
	}
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+probe.Command.ID+"/claim", nil), http.StatusConflict, "COMMAND_EXPIRED")
	var status string
	e.scalar(&status, `SELECT status FROM openfox.device_commands WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, probe.Command.ID)
	if status != "EXPIRED" {
		t.Fatalf("status = %s", status)
	}
	// GET 读取时标记过期；过期后同 kind 新建而不是复用。
	var test devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_test", "params": map[string]any{"provider": "codex"}}, &test)
	age(test.Command.ID)
	var read devCommandEnvelope
	owner.must(http.StatusOK, http.MethodGet, commandsPath+"/"+test.Command.ID, nil, &read)
	if read.Command.Status != "EXPIRED" || read.Command.CompletedAt == nil {
		t.Fatalf("read = %+v", read.Command)
	}
	var fresh devCommandEnvelope
	owner.must(http.StatusAccepted, http.MethodPost, commandsPath, map[string]any{"kind": "runtime_probe"}, &fresh)
	if fresh.Command.ID == probe.Command.ID {
		t.Fatal("expired command was reused")
	}
	// DELIVERED 超过执行截止：回执 409 COMMAND_EXPIRED，之后重复回执仍 409。
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+fresh.Command.ID+"/claim", nil), http.StatusOK, "")
	e.exec(`UPDATE openfox.device_commands SET created_at = clock_timestamp() - interval '10 minutes',
		       delivered_at = clock_timestamp() - interval '9 minutes', expires_at = clock_timestamp() - interval '1 second'
		 WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, fresh.Command.ID)
	resultBody := map[string]any{"status": "SUCCEEDED", "result": map[string]any{"runtimes": map[string]any{}, "checked_at": time.Now().UTC()}}
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+fresh.Command.ID+"/result", resultBody), http.StatusConflict, "COMMAND_EXPIRED")
	expectCode(t, e.deviceCall(device, http.MethodPost, base+"/"+fresh.Command.ID+"/result", resultBody), http.StatusConflict, "COMMAND_EXPIRED")
	e.scalar(&status, `SELECT status FROM openfox.device_commands WHERE organization_id = $1::uuid AND id = $2::uuid`, e.org, fresh.Command.ID)
	if status != "EXPIRED" {
		t.Fatalf("status = %s", status)
	}
}

func TestDevHeartbeatStorageAndRuntimesCheckedAt(t *testing.T) {
	e := newAgEnv(t)
	owner := e.client(agOwnerID)
	device := e.registerDevice(e.issueToken(owner, "ORGANIZATION").InstallToken)
	var before map[string]json.RawMessage
	owner.must(http.StatusOK, http.MethodGet, "devices/"+device.ID, nil, &before)
	if string(before["storage"]) != "null" || string(before["runtimes_checked_at"]) != "null" {
		t.Fatalf("before = storage %s checked %s", before["storage"], before["runtimes_checked_at"])
	}
	heartbeat := func(capabilities map[string]any) {
		response := e.deviceCall(device, http.MethodPost, "/v1/device/heartbeat", map[string]any{
			"center_epoch": device.CenterEpoch, "protocol_major": 1, "protocol_minor": 0,
			"daemon_version": "test", "operating_system": "darwin", "architecture": "arm64", "capabilities": capabilities,
		})
		if response.Status != http.StatusOK {
			t.Fatalf("heartbeat = %d %s", response.Status, response.Body)
		}
	}
	heartbeat(map[string]any{
		"runtimes": map[string]any{
			"codex": map[string]any{"runnable": true, "models": []any{}, "checked_at": "2026-10-29T08:00:00Z"},
			"other": map[string]any{"runnable": false, "models": []any{}, "checked_at": "2026-10-29T09:30:00Z"},
		},
		"storage": map[string]any{
			"total_bytes": 4096, "worktree_bytes": 2048, "worktree_count": 2, "codex_home_bytes": 1024,
			"journal_bytes": 1024, "measured_at": "2026-10-29T09:00:00Z", "partial": true,
		},
	})
	var detail struct {
		Storage *struct {
			TotalBytes    int64     `json:"total_bytes"`
			WorktreeCount int64     `json:"worktree_count"`
			MeasuredAt    time.Time `json:"measured_at"`
			Partial       bool      `json:"partial"`
		} `json:"storage"`
		RuntimesCheckedAt *time.Time `json:"runtimes_checked_at"`
	}
	owner.must(http.StatusOK, http.MethodGet, "devices/"+device.ID, nil, &detail)
	if detail.Storage == nil || detail.Storage.TotalBytes != 4096 || detail.Storage.WorktreeCount != 2 || !detail.Storage.Partial {
		t.Fatalf("storage = %+v", detail.Storage)
	}
	if detail.RuntimesCheckedAt == nil || !detail.RuntimesCheckedAt.Equal(time.Date(2026, 10, 29, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("runtimes_checked_at = %v", detail.RuntimesCheckedAt)
	}
	// 列表也带这两个字段。
	var page struct {
		Devices []map[string]json.RawMessage `json:"devices"`
	}
	owner.must(http.StatusOK, http.MethodGet, "devices", nil, &page)
	found := false
	for _, item := range page.Devices {
		if string(item["id"]) == `"`+device.ID+`"` {
			found = string(item["storage"]) != "null" && string(item["runtimes_checked_at"]) != "null"
		}
	}
	if !found {
		t.Fatal("device list lacks storage fields")
	}
	// 形状不符（负数）的 storage 视为未上报。
	heartbeat(map[string]any{"storage": map[string]any{
		"total_bytes": -1, "worktree_bytes": 0, "worktree_count": 0, "codex_home_bytes": 0, "journal_bytes": 0,
		"measured_at": "2026-10-29T09:00:00Z",
	}})
	var invalid map[string]json.RawMessage
	owner.must(http.StatusOK, http.MethodGet, "devices/"+device.ID, nil, &invalid)
	if string(invalid["storage"]) != "null" {
		t.Fatalf("invalid storage = %s", invalid["storage"])
	}
}
