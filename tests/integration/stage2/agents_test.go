package stage2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Agent 资料：跨租户与私有可见性、编辑权限、名称与版本冲突、访问权限修改、内置小助手。
func TestAgentsProfileAccess(t *testing.T) {
	e := newAgEnv(t)
	owner, admin, manager, member, outsider := e.client(agOwnerID), e.client(agAdminID), e.client(agManagerID), e.client(agMemberID), e.client(agOutsiderID)
	shared := e.createSpace("SHARED")
	other := e.createSpace("SHARED")

	created := e.createAgent(manager, shared, "SHARED", map[string]any{
		"title": "前端工程师", "avatar": map[string]any{"kind": "emoji", "value": "🦊"}, "allow_direct_chat": false,
	})
	if created.Title != "前端工程师" || !bytes.Contains(created.Avatar, []byte("emoji")) || !created.AllowAssign ||
		created.AllowDirectChat || created.ProfileVersion != 1 || !created.Online || created.ComputerName == nil || *created.ComputerName != "ag-mac" {
		t.Fatalf("created agent = %+v", created)
	}
	private := e.createAgent(manager, shared, "PRIVATE", nil)

	// 跨租户：其他组织的成员看不到，也不能借自己组织的路径访问。
	outsider.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, agentPath(shared, created.ID), nil)
	if response := outsider.doIn(e.otherOrg, http.MethodGet, agentPath(shared, created.ID), nil); response.Status != http.StatusNotFound {
		t.Fatalf("cross-tenant path = %d %s", response.Status, response.Body)
	}
	outsider.expectError(http.StatusNotFound, "", http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"title": "x", "profile_version": 1})

	// 私有 Agent：非所有者（含组织管理员）不可见、不可改。
	for _, client := range []*agClient{member, admin, owner} {
		client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, agentPath(shared, private.ID), nil)
		client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPatch, agentPath(shared, private.ID)+"/profile",
			map[string]any{"title": "x", "profile_version": 1})
	}
	manager.must(http.StatusOK, http.MethodPatch, agentPath(shared, private.ID)+"/profile",
		map[string]any{"title": "私有", "profile_version": 1}, nil)

	// 共享 Agent：空间普通成员 403；空间管理者与所有者可改。
	member.expectError(http.StatusForbidden, "", http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"title": "x", "profile_version": 1})
	var patched agAgent
	owner.must(http.StatusOK, http.MethodPatch, agentPath(shared, created.ID)+"/profile", map[string]any{
		"name": "Renamed " + agSuffix(t), "title": "  后端  ", "avatar": nil, "allow_assign": false,
		"trigger_workflow": true, "profile_version": 1,
	}, &patched)
	if patched.Title != "后端" || patched.Avatar != nil && string(patched.Avatar) != "null" || patched.AllowAssign ||
		!patched.TriggerWorkflow || patched.ProfileVersion != 2 || patched.Version != created.Version {
		t.Fatalf("patched = %+v (avatar %s)", patched, patched.Avatar)
	}
	owner.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"title": "stale", "profile_version": 1})
	owner.expectError(http.StatusConflict, "NAME_CONFLICT", http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"name": strings.ToUpper(e.getAgent(manager, shared, private.ID).Name), "profile_version": 2})
	owner.expectError(http.StatusBadRequest, "", http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"title": strings.Repeat("长", 65), "profile_version": 2})

	// 所有者只能改成能读该空间的 ACTIVE 成员（guest 不能读 SHARED 空间）。
	owner.expectError(http.StatusBadRequest, "INVALID_OWNER", http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"owner_user_id": agGuestID, "profile_version": 2})
	owner.must(http.StatusOK, http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"owner_user_id": agMemberID, "profile_version": 2}, &patched)
	if patched.OwnerUserID != agMemberID || patched.ProfileVersion != 3 {
		t.Fatalf("owner change = %+v", patched)
	}

	// 换空间：组织级授权允许；换完后旧路径 404。
	owner.must(http.StatusOK, http.MethodPatch, agentPath(shared, created.ID)+"/profile",
		map[string]any{"space_id": other, "profile_version": 3}, &patched)
	if patched.SpaceID != other {
		t.Fatalf("space change = %+v", patched)
	}
	owner.expectError(http.StatusNotFound, "", http.MethodGet, agentPath(shared, created.ID), nil)

	// 空间授权的电脑：Agent 不能换到授权之外的空间。
	e.exec(`UPDATE openfox.device_grants SET scope = 'SPACE', scope_space_id = $3::uuid
		WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, e.deviceID, other)
	owner.expectError(http.StatusConflict, "DEVICE_SCOPE_MISMATCH", http.MethodPatch, agentPath(other, created.ID)+"/profile",
		map[string]any{"space_id": shared, "profile_version": 4})
	e.exec(`UPDATE openfox.device_grants SET scope = 'ORGANIZATION', scope_space_id = NULL
		WHERE organization_id = $1::uuid AND device_id = $2::uuid`, e.org, e.deviceID)

	// 内置小助手不能通过 profile 修改。
	var assistant struct {
		Assistant agAgent `json:"assistant"`
	}
	owner.must(http.StatusOK, http.MethodPut, "assistant", map[string]any{"computer_id": e.deviceID, "grant_id": e.grantID}, &assistant)
	owner.expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPatch,
		agentPath(assistant.Assistant.SpaceID, assistant.Assistant.ID)+"/profile", map[string]any{"title": "x", "profile_version": 1})
}

// 同一 Agent 并发 PATCH profile：只有一个成功，其余 409 VERSION_CONFLICT。
func TestAgentsProfileConcurrentPatch(t *testing.T) {
	e := newAgEnv(t)
	manager := e.client(agManagerID)
	space := e.createSpace("SHARED")
	agent := e.createAgent(manager, space, "SHARED", nil)
	const workers = 8
	statuses := make([]int, workers)
	codes := make([]string, workers)
	var wg sync.WaitGroup
	for index := 0; index < workers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			response := manager.do(http.MethodPatch, agentPath(space, agent.ID)+"/profile",
				map[string]any{"title": "t" + strings.Repeat("x", index), "profile_version": agent.ProfileVersion})
			statuses[index], codes[index] = response.Status, response.code()
		}(index)
	}
	wg.Wait()
	ok := 0
	for index, status := range statuses {
		switch {
		case status == http.StatusOK:
			ok++
		case status == http.StatusConflict && codes[index] == "VERSION_CONFLICT":
		default:
			t.Fatalf("worker %d = %d %s", index, status, codes[index])
		}
	}
	if ok != 1 {
		t.Fatalf("successful patches = %d, statuses %v", ok, statuses)
	}
	if got := e.getAgent(manager, space, agent.ID).ProfileVersion; got != agent.ProfileVersion+1 {
		t.Fatalf("profile_version = %d", got)
	}
}

type agEnvList struct {
	EnvVars []struct {
		Name     string  `json:"name"`
		Value    *string `json:"value"`
		HasValue bool    `json:"has_value"`
		Version  int     `json:"version"`
	} `json:"env_vars"`
	CanViewValues    bool `json:"can_view_values"`
	SecretsAvailable bool `json:"secrets_available"`
}

// 环境变量：加密往返、库里无明文、遮盖规则、未配置主密钥、名称冲突、数量与大小上限、env_digest。
func TestAgentsEnvVars(t *testing.T) {
	e := newAgEnv(t)
	owner, admin, manager, member := e.client(agOwnerID), e.client(agAdminID), e.client(agManagerID), e.client(agMemberID)
	space := e.createSpace("SHARED")
	agent := e.createAgent(manager, space, "SHARED", nil)
	envPath := agentPath(space, agent.ID) + "/env-vars"
	const secretValue = "sk-test-PLAINTEXT-4242"

	var saved struct {
		Name    string  `json:"name"`
		Value   *string `json:"value"`
		Version int     `json:"version"`
	}
	manager.must(http.StatusOK, http.MethodPut, envPath+"/API_TOKEN", map[string]any{"value": secretValue}, &saved)
	if saved.Value == nil || *saved.Value != secretValue || saved.Version != 1 {
		t.Fatalf("saved = %+v", saved)
	}
	// 库里只有密文：既找不到明文，也能用同一主密钥解开（经接口读取）。
	var plaintextRows int
	e.scalar(&plaintextRows, `SELECT count(*) FROM openfox.agent_env_vars
		WHERE organization_id = $1::uuid AND position(convert_to($2, 'UTF8') IN value_ciphertext) > 0`, e.org, secretValue)
	if plaintextRows != 0 {
		t.Fatal("plaintext found in agent_env_vars")
	}
	var keyID string
	e.scalar(&keyID, `SELECT key_id FROM openfox.agent_env_vars WHERE organization_id = $1::uuid AND agent_id = $2::uuid`, e.org, agent.ID)
	if keyID == "" {
		t.Fatal("key_id missing")
	}

	// 所有者（manager 创建）与组织 OWNER / ADMIN 能看原值；空间普通成员只看到 null。
	for _, client := range []*agClient{manager, owner, admin} {
		var list agEnvList
		client.must(http.StatusOK, http.MethodGet, envPath, nil, &list)
		if !list.CanViewValues || !list.SecretsAvailable || len(list.EnvVars) != 1 || list.EnvVars[0].Value == nil ||
			*list.EnvVars[0].Value != secretValue {
			t.Fatalf("privileged view = %+v", list)
		}
	}
	var masked agEnvList
	member.must(http.StatusOK, http.MethodGet, envPath, nil, &masked)
	if masked.CanViewValues || len(masked.EnvVars) != 1 || masked.EnvVars[0].Value != nil || !masked.EnvVars[0].HasValue {
		t.Fatalf("member view = %+v", masked)
	}
	member.expectError(http.StatusForbidden, "", http.MethodPut, envPath+"/OTHER", map[string]any{"value": "x"})

	// 名称冲突、版本冲突、不存在、非法名称与超长值。
	manager.expectError(http.StatusConflict, "NAME_CONFLICT", http.MethodPut, envPath+"/API_TOKEN", map[string]any{"value": "again"})
	manager.must(http.StatusOK, http.MethodPut, envPath+"/API_TOKEN", map[string]any{"value": "rotated", "version": 1}, &saved)
	if saved.Version != 2 {
		t.Fatalf("updated version = %d", saved.Version)
	}
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPut, envPath+"/API_TOKEN", map[string]any{"value": "x", "version": 1})
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, envPath+"/MISSING", map[string]any{"value": "x", "version": 1})
	manager.expectError(http.StatusBadRequest, "", http.MethodPut, envPath+"/1BAD", map[string]any{"value": "x"})
	manager.expectError(http.StatusBadRequest, "", http.MethodPut, envPath+"/BIG", map[string]any{"value": strings.Repeat("v", 8193)})
	manager.must(http.StatusOK, http.MethodPut, envPath+"/MAX_SIZE", map[string]any{"value": strings.Repeat("v", 8192)}, nil)

	// env_digest：新配置版本记录当前环境变量摘要。
	var withConfig agAgent
	body := e.configBody(map[string]any{"expected_agent_version": agent.Version, "chat_concurrency": "MANUAL",
		"chat_concurrency_value": 3, "workdir_isolation": "DIRECT", "custom_args": []string{"--fast"}})
	manager.must(http.StatusCreated, http.MethodPost, agentPath(space, agent.ID)+"/config-versions", body, &withConfig)
	sum := func(value string) string {
		digest := sha256.Sum256([]byte(value))
		return hex.EncodeToString(digest[:])
	}
	wantDigest := sum("API_TOKEN=" + sum("rotated") + "\nMAX_SIZE=" + sum(strings.Repeat("v", 8192)) + "\n")
	if withConfig.Config == nil || withConfig.Config.EnvDigest != wantDigest || withConfig.Config.ChatConcurrency != "MANUAL" ||
		withConfig.Config.WorkdirIsolation != "DIRECT" || len(withConfig.Config.CustomArgs) != 1 {
		t.Fatalf("config = %+v, want digest %s", withConfig.Config, wantDigest)
	}

	// 未配置主密钥：读只返回名称与 has_value；写 409。
	plain := e.clientWithoutSecrets(agManagerID)
	var unavailable agEnvList
	plain.must(http.StatusOK, http.MethodGet, envPath, nil, &unavailable)
	if unavailable.SecretsAvailable || len(unavailable.EnvVars) != 2 || unavailable.EnvVars[0].Value != nil {
		t.Fatalf("no-secrets view = %+v", unavailable)
	}
	plain.expectError(http.StatusConflict, "SECRETS_UNAVAILABLE", http.MethodPut, envPath+"/NEW_ONE", map[string]any{"value": "x"})

	// 数量上限 100。
	e.exec(`INSERT INTO openfox.agent_env_vars (organization_id, agent_id, name, value_ciphertext, value_nonce, key_id, updated_by)
		SELECT $1::uuid, $2::uuid, 'FILL_' || n, decode(repeat('00', 16), 'hex'), decode(repeat('00', 12), 'hex'), 'k', $3::uuid
		  FROM generate_series(1, 98) AS n`, e.org, agent.ID, agManagerID)
	manager.expectError(http.StatusConflict, "LIMIT_REACHED", http.MethodPut, envPath+"/ONE_TOO_MANY", map[string]any{"value": "x"})

	// 删除：204，再删 404。
	manager.must(http.StatusNoContent, http.MethodDelete, envPath+"/API_TOKEN", nil, nil)
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, envPath+"/API_TOKEN", nil)
}

// 复制：资料 + 配置 + 环境变量（重新加密后能解密）+ 仓库；默认名称加序号；幂等重放。
func TestAgentsCopy(t *testing.T) {
	e := newAgEnv(t)
	manager, member := e.client(agManagerID), e.client(agMemberID)
	space := e.createSpace("SHARED")
	source := e.createAgent(manager, space, "SHARED", map[string]any{"name": "Reviewer " + agSuffix(t), "title": "评审"})
	manager.must(http.StatusOK, http.MethodPut, agentPath(space, source.ID)+"/env-vars/TOKEN", map[string]any{"value": "copy-me"}, nil)
	manager.must(http.StatusCreated, http.MethodPost, agentPath(space, source.ID)+"/repos",
		map[string]any{"url": "https://github.com/acme/web.git", "base_branch": "main"}, nil)

	var first agAgent
	response := manager.must(http.StatusCreated, http.MethodPost, agentPath(space, source.ID)+"/copy", nil, &first, "Idempotency-Key", "copy-key-0001")
	if first.ID == source.ID || first.Name != source.Name+" 副本" || first.Visibility != "PRIVATE" || first.OwnerUserID != agManagerID ||
		first.Title != "评审" || first.CurrentConfigVersion != 1 || first.Config == nil || first.Config.EnvDigest == "" {
		t.Fatalf("copy = %+v (%s)", first, response.Body)
	}
	var replay agAgent
	response = manager.must(http.StatusOK, http.MethodPost, agentPath(space, source.ID)+"/copy", nil, &replay, "Idempotency-Key", "copy-key-0001")
	if replay.ID != first.ID || response.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %+v %v", replay, response.Header)
	}
	manager.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, agentPath(space, source.ID)+"/copy",
		map[string]any{"name": "Other name"}, "Idempotency-Key", "copy-key-0001")
	var second agAgent
	manager.must(http.StatusCreated, http.MethodPost, agentPath(space, source.ID)+"/copy", nil, &second)
	if second.Name != source.Name+" 副本 2" {
		t.Fatalf("second copy name = %q", second.Name)
	}
	manager.expectError(http.StatusConflict, "NAME_CONFLICT", http.MethodPost, agentPath(space, source.ID)+"/copy",
		map[string]any{"name": source.Name})

	// 环境变量用新 Agent 的 AAD 重新加密：能解密，且密文与源不同。
	var list agEnvList
	manager.must(http.StatusOK, http.MethodGet, agentPath(space, first.ID)+"/env-vars", nil, &list)
	if len(list.EnvVars) != 1 || list.EnvVars[0].Value == nil || *list.EnvVars[0].Value != "copy-me" {
		t.Fatalf("copied env = %+v", list)
	}
	var sameCipher bool
	e.scalar(&sameCipher, `SELECT a.value_ciphertext = b.value_ciphertext FROM openfox.agent_env_vars a, openfox.agent_env_vars b
		WHERE a.agent_id = $1::uuid AND b.agent_id = $2::uuid AND a.name = 'TOKEN' AND b.name = 'TOKEN'`, source.ID, first.ID)
	if sameCipher {
		t.Fatal("copied ciphertext must be re-encrypted")
	}
	var repos struct {
		Repos []struct {
			URL string `json:"url"`
		} `json:"repos"`
	}
	manager.must(http.StatusOK, http.MethodGet, agentPath(space, first.ID)+"/repos", nil, &repos)
	if len(repos.Repos) != 1 || repos.Repos[0].URL != "https://github.com/acme/web.git" {
		t.Fatalf("copied repos = %+v", repos)
	}
	// 复制品是私有的：别人看不到；不能管理空间的成员不能复制。
	member.expectError(http.StatusNotFound, "", http.MethodGet, agentPath(space, first.ID), nil)
	member.expectError(http.StatusForbidden, "", http.MethodPost, agentPath(space, source.ID)+"/copy", nil)
	// 源有环境变量而未配置主密钥：409。
	e.clientWithoutSecrets(agManagerID).expectError(http.StatusConflict, "SECRETS_UNAVAILABLE", http.MethodPost,
		agentPath(space, source.ID)+"/copy", nil)
}

// Git 仓库：增删改、地址冲突、幂等创建、上限 20。
func TestAgentsRepos(t *testing.T) {
	e := newAgEnv(t)
	manager, member := e.client(agManagerID), e.client(agMemberID)
	space := e.createSpace("SHARED")
	agent := e.createAgent(manager, space, "SHARED", nil)
	repoPath := agentPath(space, agent.ID) + "/repos"
	type repo struct {
		ID         string `json:"id"`
		URL        string `json:"url"`
		BaseBranch string `json:"base_branch"`
		Position   int    `json:"position"`
		Version    int    `json:"version"`
	}
	var created, replay repo
	manager.must(http.StatusCreated, http.MethodPost, repoPath, map[string]any{"url": "git@github.com:acme/api.git"}, &created, "Idempotency-Key", "repo-key-0001")
	response := manager.must(http.StatusOK, http.MethodPost, repoPath, map[string]any{"url": "git@github.com:acme/api.git"}, &replay, "Idempotency-Key", "repo-key-0001")
	if replay.ID != created.ID || response.Header.Get("Idempotent-Replayed") != "true" || created.Position != 0 {
		t.Fatalf("repo replay = %+v / %+v", created, replay)
	}
	manager.expectError(http.StatusConflict, "REPO_CONFLICT", http.MethodPost, repoPath, map[string]any{"url": "git@github.com:acme/api.git"})
	manager.expectError(http.StatusBadRequest, "", http.MethodPost, repoPath, map[string]any{"url": "file:///etc"})
	member.expectError(http.StatusForbidden, "", http.MethodPost, repoPath, map[string]any{"url": "https://example.test/x.git"})
	var patched repo
	manager.must(http.StatusOK, http.MethodPatch, repoPath+"/"+created.ID, map[string]any{"base_branch": "develop", "version": 1}, &patched)
	if patched.BaseBranch != "develop" || patched.Version != 2 {
		t.Fatalf("patched repo = %+v", patched)
	}
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, repoPath+"/"+created.ID, map[string]any{"base_branch": "x", "version": 1})
	for index := 1; index < 20; index++ {
		manager.must(http.StatusCreated, http.MethodPost, repoPath, map[string]any{"url": "https://example.test/r" + strings.Repeat("x", index) + ".git"}, nil)
	}
	manager.expectError(http.StatusConflict, "LIMIT_REACHED", http.MethodPost, repoPath, map[string]any{"url": "https://example.test/overflow.git"})
	var list struct {
		Repos []repo `json:"repos"`
	}
	member.must(http.StatusOK, http.MethodGet, repoPath, nil, &list)
	if len(list.Repos) != 20 {
		t.Fatalf("repo count = %d", len(list.Repos))
	}
	manager.must(http.StatusNoContent, http.MethodDelete, repoPath+"/"+created.ID, nil, nil)
	manager.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, repoPath+"/"+created.ID, nil)
}

type agCrew struct {
	ID      string `json:"id"`
	SpaceID string `json:"space_id"`
	Type    string `json:"type"`
	Leader  *struct {
		ID string `json:"id"`
	} `json:"leader"`
	Members []struct {
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		Exclusive bool   `json:"exclusive"`
	} `json:"members"`
	MemberCount  int    `json:"member_count"`
	Instructions string `json:"instructions"`
	Version      int    `json:"version"`
}

// 小队：队长规则、真人成员、专属成员冲突，以及专属 / 不可单聊 / 不可指派的效果。
func TestAgentsCrews(t *testing.T) {
	e := newAgEnv(t)
	manager, member := e.client(agManagerID), e.client(agMemberID)
	space := e.createSpace("SHARED")
	other := e.createSpace("SHARED")
	leader := e.createAgent(manager, space, "SHARED", nil)
	worker := e.createAgent(manager, space, "SHARED", nil)
	spare := e.createAgent(manager, space, "SHARED", nil)
	crewsPath := "spaces/" + space + "/crews"

	// 缺队长且没有 Agent 成员：400；队长在 agent_ids 里会去重并排第一。
	manager.expectError(http.StatusBadRequest, "", http.MethodPost, crewsPath, map[string]any{"name": "No leader"})
	var crew agCrew
	manager.must(http.StatusCreated, http.MethodPost, crewsPath, map[string]any{
		"name": "Crew " + agSuffix(t), "leader_agent_id": leader.ID, "agent_ids": []string{worker.ID, leader.ID}, "user_ids": []string{agMemberID},
	}, &crew)
	if crew.Type != "crew" || crew.Leader == nil || crew.Leader.ID != leader.ID || crew.MemberCount != 3 || len(crew.Members) != 3 ||
		crew.Members[0].ID != leader.ID || crew.Members[2].Kind != "HUMAN" || crew.Members[2].ID != agMemberID {
		t.Fatalf("crew = %+v", crew)
	}
	// 旧请求：不传 leader_agent_id 取 agent_ids[0]。
	var legacy agCrew
	manager.must(http.StatusCreated, http.MethodPost, crewsPath, map[string]any{"name": "Legacy " + agSuffix(t), "agent_ids": []string{spare.ID}}, &legacy)
	if legacy.Leader == nil || legacy.Leader.ID != spare.ID {
		t.Fatalf("legacy crew = %+v", legacy)
	}
	crewPath := crewsPath + "/" + crew.ID
	var detail agCrew
	member.must(http.StatusOK, http.MethodGet, crewPath, nil, &detail)
	if detail.ID != crew.ID || detail.MemberCount != 3 {
		t.Fatalf("detail = %+v", detail)
	}
	e.client(agOutsiderID).expectError(http.StatusNotFound, "", http.MethodGet, crewPath, nil)

	// 队长不能移除；换队长后原队长可移除。
	manager.expectError(http.StatusConflict, "CREW_LEADER_REQUIRED", http.MethodDelete, crewPath+"/members/agent/"+leader.ID, nil)
	manager.must(http.StatusOK, http.MethodPatch, crewPath, map[string]any{"leader_agent_id": worker.ID, "instructions": "先评审再合并", "version": detail.Version}, &detail)
	if detail.Leader.ID != worker.ID || detail.Members[0].ID != worker.ID || detail.Instructions != "先评审再合并" {
		t.Fatalf("leader change = %+v", detail)
	}
	manager.expectError(http.StatusBadRequest, "", http.MethodPatch, crewPath, map[string]any{"leader_agent_id": spare.ID, "version": detail.Version})
	// 换空间：队长不在新空间 409。
	manager.expectError(http.StatusConflict, "CREW_LEADER_SPACE_MISMATCH", http.MethodPatch, crewPath, map[string]any{"space_id": other, "version": detail.Version})

	// 专属成员：一个 Agent 至多是一个小队的专属成员。
	manager.must(http.StatusOK, http.MethodPost, "spaces/"+space+"/crews/"+legacy.ID+"/members", map[string]any{"agent_ids": []string{leader.ID}}, nil)
	manager.must(http.StatusOK, http.MethodPatch, crewPath+"/members/agents/"+leader.ID, map[string]any{"exclusive": true}, &detail)
	manager.expectError(http.StatusConflict, "AGENT_CREW_MEMBERSHIP_CONFLICT", http.MethodPatch,
		"spaces/"+space+"/crews/"+legacy.ID+"/members/agents/"+leader.ID, map[string]any{"exclusive": true})

	// 专属 Agent 不出现在目录；不能单聊；不能被单独指派，指派小队不受影响。
	var directory struct {
		Entries []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"entries"`
	}
	manager.must(http.StatusOK, http.MethodGet, "agent-directory", nil, &directory)
	for _, entry := range directory.Entries {
		if entry.ID == leader.ID {
			t.Fatal("exclusive Agent listed in the directory")
		}
	}
	conversation := func(agentID, taskID string) agResponse {
		body := map[string]any{"space_id": space, "agent_id": agentID, "visibility": "PRIVATE"}
		if taskID != "" {
			body["task_id"] = taskID
		}
		return manager.do(http.MethodPost, "conversations", body)
	}
	if response := conversation(leader.ID, ""); response.Status != http.StatusForbidden {
		t.Fatalf("exclusive direct chat = %d %s", response.Status, response.Body)
	}
	if response := conversation(worker.ID, ""); response.Status != http.StatusCreated && response.Status != http.StatusOK {
		t.Fatalf("normal direct chat = %d %s", response.Status, response.Body)
	}
	noChat := e.createAgent(manager, space, "SHARED", map[string]any{"allow_direct_chat": false})
	if response := conversation(noChat.ID, ""); response.Status != http.StatusForbidden {
		t.Fatalf("allow_direct_chat=false = %d %s", response.Status, response.Body)
	}
	typeID := e.taskTypeID(space)
	newTask := func(assignees []map[string]any) agResponse {
		return manager.do(http.MethodPost, "spaces/"+space+"/tasks", map[string]any{"type_id": typeID, "title": "T " + agSuffix(t), "assignees": assignees})
	}
	if response := newTask([]map[string]any{{"kind": "AGENT", "id": leader.ID}}); response.Status != http.StatusBadRequest || response.code() != "AGENT_NOT_ASSIGNABLE" {
		t.Fatalf("assign exclusive = %d %s", response.Status, response.Body)
	}
	noAssign := e.createAgent(manager, space, "SHARED", map[string]any{"allow_assign": false})
	if response := newTask([]map[string]any{{"kind": "AGENT", "id": noAssign.ID}}); response.Status != http.StatusBadRequest || response.code() != "AGENT_NOT_ASSIGNABLE" {
		t.Fatalf("assign allow_assign=false = %d %s", response.Status, response.Body)
	}
	if response := newTask([]map[string]any{{"kind": "CREW", "id": crew.ID}}); response.Status != http.StatusCreated {
		t.Fatalf("assign crew = %d %s", response.Status, response.Body)
	}
	// 任务会话不受「不可单聊」限制。
	var task struct {
		ID string `json:"id"`
	}
	newTask(nil).decode(t, &task)
	if response := conversation(noChat.ID, task.ID); response.Status != http.StatusCreated && response.Status != http.StatusOK {
		t.Fatalf("task conversation with allow_direct_chat=false = %d %s", response.Status, response.Body)
	}

	// 取消专属后另一个小队可以设专属；移除真人成员幂等。
	manager.must(http.StatusOK, http.MethodPatch, crewPath+"/members/agents/"+leader.ID, map[string]any{"exclusive": false}, nil)
	manager.must(http.StatusOK, http.MethodPatch, "spaces/"+space+"/crews/"+legacy.ID+"/members/agents/"+leader.ID, map[string]any{"exclusive": true}, nil)
	manager.must(http.StatusNoContent, http.MethodDelete, crewPath+"/members/human/"+agMemberID, nil, nil)
	manager.must(http.StatusNoContent, http.MethodDelete, crewPath+"/members/human/"+agMemberID, nil, nil)
	member.expectError(http.StatusForbidden, "", http.MethodPost, crewPath+"/members", map[string]any{"agent_ids": []string{spare.ID}})
}

// 收藏：幂等、反映在 Agent 与目录上；不可见目标 404。
func TestAgentsFavorites(t *testing.T) {
	e := newAgEnv(t)
	manager, member := e.client(agManagerID), e.client(agMemberID)
	space := e.createSpace("SHARED")
	agent := e.createAgent(manager, space, "SHARED", nil)
	private := e.createAgent(manager, space, "PRIVATE", nil)
	member.must(http.StatusNoContent, http.MethodPut, "agent-favorites", map[string]any{"agent_id": agent.ID}, nil)
	member.must(http.StatusNoContent, http.MethodPut, "agent-favorites", map[string]any{"agent_id": agent.ID}, nil)
	if !e.getAgent(member, space, agent.ID).Favorite || e.getAgent(manager, space, agent.ID).Favorite {
		t.Fatal("favorite must be personal")
	}
	member.expectError(http.StatusNotFound, "", http.MethodPut, "agent-favorites", map[string]any{"agent_id": private.ID})
	member.expectError(http.StatusBadRequest, "", http.MethodPut, "agent-favorites", map[string]any{})
	member.must(http.StatusNoContent, http.MethodDelete, "agent-favorites?agent_id="+agent.ID, nil, nil)
	member.must(http.StatusNoContent, http.MethodDelete, "agent-favorites?agent_id="+agent.ID, nil, nil)
	if e.getAgent(member, space, agent.ID).Favorite {
		t.Fatal("favorite not removed")
	}
}

// 统计与运行历史：运行只对能看到会话的成员可见。
func TestAgentsStatsAndRuns(t *testing.T) {
	e := newAgEnv(t)
	manager, member := e.client(agManagerID), e.client(agMemberID)
	space := e.createSpace("SHARED")
	agent := e.createAgent(manager, space, "SHARED", nil)
	conversationID := agNewID(t)
	e.exec(`INSERT INTO openfox.conversations (organization_id, id, space_id, agent_id, owner_user_id, visibility, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, 'PRIVATE', 'Debug session')`, e.org, conversationID, space, agent.ID, agManagerID)
	for index := 0; index < 3; index++ {
		e.exec(`INSERT INTO openfox.runs (organization_id, space_id, conversation_id, agent_id, computer_id, grant_id,
				config_version, grant_version, actor_id, state, finished_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, 1, 1, $7::uuid, 'SUCCEEDED', clock_timestamp())`,
			e.org, space, conversationID, agent.ID, e.deviceID, e.grantID, agManagerID)
	}
	var stats struct {
		TaskCount   int     `json:"task_count"`
		RunCount    int     `json:"run_count"`
		InputTokens int     `json:"input_tokens"`
		LastRunAt   *string `json:"last_run_at"`
	}
	member.must(http.StatusOK, http.MethodGet, agentPath(space, agent.ID)+"/stats", nil, &stats)
	if stats.RunCount != 3 || stats.TaskCount != 0 || stats.InputTokens != 0 || stats.LastRunAt == nil {
		t.Fatalf("stats = %+v", stats)
	}
	type page struct {
		Runs []struct {
			ID                string  `json:"id"`
			ConversationTitle *string `json:"conversation_title"`
			TaskDisplayID     *string `json:"task_display_id"`
		} `json:"runs"`
		NextCursor *string `json:"next_cursor"`
	}
	var first, second page
	manager.must(http.StatusOK, http.MethodGet, agentPath(space, agent.ID)+"/runs?limit=2", nil, &first)
	if len(first.Runs) != 2 || first.NextCursor == nil || first.Runs[0].ConversationTitle == nil || *first.Runs[0].ConversationTitle != "Debug session" {
		t.Fatalf("first page = %+v", first)
	}
	manager.must(http.StatusOK, http.MethodGet, agentPath(space, agent.ID)+"/runs?limit=2&cursor="+*first.NextCursor, nil, &second)
	if len(second.Runs) != 1 || second.NextCursor != nil || second.Runs[0].ID == first.Runs[0].ID || second.Runs[0].ID == first.Runs[1].ID {
		t.Fatalf("second page = %+v", second)
	}
	var hidden page
	member.must(http.StatusOK, http.MethodGet, agentPath(space, agent.ID)+"/runs", nil, &hidden)
	if len(hidden.Runs) != 0 {
		t.Fatalf("member sees private conversation runs: %+v", hidden)
	}
	manager.expectError(http.StatusBadRequest, "", http.MethodGet, agentPath(space, agent.ID)+"/runs?cursor=bogus", nil)
}

// 成员页签：按可读空间分组、组内先小队后 Agent、我创建的优先；真人为组织 ACTIVE 成员；专属与私有 Agent 的可见性。
func TestAgentsContacts(t *testing.T) {
	e := newAgEnv(t)
	owner, manager, member, guest := e.client(agOwnerID), e.client(agManagerID), e.client(agMemberID), e.client(agGuestID)
	shared := e.createSpace("SHARED")
	private := e.createSpace("PRIVATE")
	e.exec(`DELETE FROM openfox.space_members WHERE organization_id = $1::uuid AND space_id = $2::uuid AND user_id = $3::uuid`,
		e.org, private, agMemberID)
	byOwner := e.createAgent(owner, shared, "SHARED", map[string]any{"name": "AAA owner " + agSuffix(t)})
	byManager := e.createAgent(manager, shared, "SHARED", map[string]any{"name": "ZZZ manager " + agSuffix(t)})
	managerPrivate := e.createAgent(manager, shared, "PRIVATE", nil)
	inPrivate := e.createAgent(owner, private, "SHARED", nil)
	exclusive := e.createAgent(manager, shared, "SHARED", nil)
	var crew agCrew
	manager.must(http.StatusCreated, http.MethodPost, "spaces/"+shared+"/crews", map[string]any{
		"name": "Contacts crew " + agSuffix(t), "leader_agent_id": byManager.ID, "agent_ids": []string{exclusive.ID},
	}, &crew)
	manager.must(http.StatusOK, http.MethodPatch, "spaces/"+shared+"/crews/"+crew.ID+"/members/agents/"+exclusive.ID, map[string]any{"exclusive": true}, nil)

	type contacts struct {
		Groups []struct {
			Kind    string  `json:"kind"`
			SpaceID *string `json:"space_id"`
			Items   []struct {
				Kind        string  `json:"kind"`
				ID          string  `json:"id"`
				Email       *string `json:"email"`
				CreatedByMe bool    `json:"created_by_me"`
			} `json:"items"`
		} `json:"groups"`
		Truncated bool `json:"truncated"`
	}
	load := func(client *agClient) contacts {
		var result contacts
		client.must(http.StatusOK, http.MethodGet, "contacts", nil, &result)
		return result
	}
	group := func(result contacts, spaceID string) []string {
		for _, g := range result.Groups {
			if g.Kind == "SPACE" && g.SpaceID != nil && *g.SpaceID == spaceID {
				ids := make([]string, 0, len(g.Items))
				for _, item := range g.Items {
					ids = append(ids, item.Kind+":"+item.ID)
				}
				return ids
			}
		}
		return nil
	}
	managerView := load(manager)
	sharedItems := group(managerView, shared)
	// 小队在前；manager 自己创建的 Agent（含私有）在 owner 创建的之前；专属 Agent 不出现。
	want := []string{"crew:" + crew.ID}
	if len(sharedItems) != 4 || sharedItems[0] != want[0] || sharedItems[3] != "agent:"+byOwner.ID {
		t.Fatalf("manager shared group = %v", sharedItems)
	}
	for _, id := range sharedItems {
		if id == "agent:"+exclusive.ID {
			t.Fatal("exclusive Agent listed in contacts")
		}
	}
	last := managerView.Groups[len(managerView.Groups)-1]
	if last.Kind != "HUMANS" || len(last.Items) != len(agRoles) {
		t.Fatalf("humans group = %+v", last)
	}
	memberView := load(member)
	if group(memberView, private) != nil {
		t.Fatal("member sees a private space group")
	}
	for _, id := range group(memberView, shared) {
		if id == "agent:"+managerPrivate.ID {
			t.Fatal("member sees manager's private Agent")
		}
	}
	if group(load(owner), private) == nil || !contains(group(load(owner), private), "agent:"+inPrivate.ID) {
		t.Fatal("owner misses the private space group")
	}
	// guest 没有显式空间成员关系：看不到任何空间分组，只有真人分组。
	guestView := load(guest)
	if len(guestView.Groups) != 1 || guestView.Groups[0].Kind != "HUMANS" {
		t.Fatalf("guest contacts = %+v", guestView.Groups)
	}
	// 跨租户：outsider 不能读本组织的成员页签；读自己组织的也不含本组织条目。
	e.client(agOutsiderID).expectError(http.StatusNotFound, "", http.MethodGet, "contacts", nil)
	var outsiderView contacts
	if response := e.client(agOutsiderID).doIn(e.otherOrg, http.MethodGet, "contacts", nil); response.Status != http.StatusOK {
		t.Fatalf("outsider own contacts = %d %s", response.Status, response.Body)
	} else {
		response.decode(t, &outsiderView)
	}
	for _, g := range outsiderView.Groups {
		for _, item := range g.Items {
			if item.ID == byOwner.ID || item.ID == agManagerID {
				t.Fatal("other organization sees contacts of this organization")
			}
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
