package stage12b

// 阶段 12b：普通对话重试接口的集成测试（幂等、拒绝规则、retry_of 关联）。
// 不启动派发器：测试直接构造一个 FAILED 的 Run 并把原输入挂到它上面，再验证重试接口写入的新输入。

import (
	"net/http"
	"strings"
	"testing"
)

type retryFixture struct {
	space, agent, conversation, run string
}

// seedFailedRun 由 owner 建会话并发消息，再写一个 FAILED 的 Run 并关联原输入。
func (e *qcEnv) seedFailedRun(failureCode string) retryFixture {
	e.t.Helper()
	space := e.createSpace(nil)
	agent := e.seedAgent(space.ID, qcOwnerID, "PRIVATE")
	var conv struct {
		ID string `json:"id"`
	}
	e.owner().must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space.ID, "agent_id": agent}, &conv)
	e.owner().must(http.StatusCreated, http.MethodPost, "conversations/"+conv.ID+"/messages",
		map[string]any{"id": qcNewID(e.t), "client_message_id": qcNewID(e.t), "body": "do it", "mentions": []map[string]string{{"agent_id": agent}}}, nil)
	run := qcNewID(e.t)
	e.exec(`INSERT INTO openfox.runs (organization_id, id, space_id, conversation_id, agent_id, computer_id, grant_id, config_version, grant_version,
			actor_id, state, generation, finished_at, failure_code)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, a.device_id, g.id, 1, 1, $6::uuid, 'FAILED', 1, clock_timestamp(), $7
		  FROM openfox.agents a JOIN openfox.device_grants g ON g.organization_id = a.organization_id AND g.device_id = a.device_id
		 WHERE a.organization_id = $1::uuid AND a.id = $5::uuid`, e.org, run, space.ID, conv.ID, agent, qcOwnerID, failureCode)
	e.exec(`UPDATE openfox.conversation_inputs SET status = 'CONSUMED', run_id = $2::uuid, assigned_at = clock_timestamp(), consumed_at = clock_timestamp()
		 WHERE organization_id = $1::uuid AND conversation_id = $3::uuid`, e.org, run, conv.ID)
	return retryFixture{space.ID, agent, conv.ID, run}
}

func retryPath(f retryFixture) string {
	return "conversations/" + f.conversation + "/runs/" + f.run + "/retries"
}

func (e *qcEnv) pendingRetryInputs(f retryFixture) int {
	return e.count(`SELECT count(*) FROM openfox.conversation_inputs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
		AND status = 'PENDING' AND source_event_id LIKE 'assistant-retry:' || $3 || ':%'`, e.org, f.conversation, f.run)
}

func TestRetryFirstSuccessAndSameKeyReplay(t *testing.T) {
	env := newQcEnv(t)
	f := env.seedFailedRun("RATE_LIMITED")
	key := "retry-key-" + qcSuffix(t)
	var first, second struct {
		RetryID string `json:"retry_id"`
		RetryOf string `json:"retry_of"`
	}
	env.owner().must(http.StatusCreated, http.MethodPost, retryPath(f), nil, &first, "Idempotency-Key", key)
	if first.RetryOf != f.run || first.RetryID == "" {
		t.Fatalf("first = %+v", first)
	}
	if got := env.pendingRetryInputs(f); got != 1 {
		t.Fatalf("pending retry inputs = %d, want 1", got)
	}
	resp := env.owner().must(http.StatusOK, http.MethodPost, retryPath(f), nil, &second, "Idempotency-Key", key)
	if second != first || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %+v header=%q", second, resp.Header.Get("Idempotent-Replayed"))
	}
	if got := env.pendingRetryInputs(f); got != 1 {
		t.Fatalf("replay must not enqueue again, pending = %d", got)
	}
}

func TestRetrySameKeyDifferentParametersConflicts(t *testing.T) {
	env := newQcEnv(t)
	f := env.seedFailedRun("RATE_LIMITED")
	key := "retry-key-" + qcSuffix(t)
	env.owner().must(http.StatusCreated, http.MethodPost, retryPath(f), nil, nil, "Idempotency-Key", key)
	other := env.seedFailedRun("RATE_LIMITED")
	env.owner().expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, retryPath(other), nil, "Idempotency-Key", key)
	env.owner().expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, retryPath(f),
		map[string]any{"workspace_selection": map[string]any{"mode": "DIRECT", "directory": "work/x"}}, "Idempotency-Key", key)
}

func TestRetryRequiresValidIdempotencyKey(t *testing.T) {
	env := newQcEnv(t)
	f := env.seedFailedRun("RATE_LIMITED")
	env.owner().expectError(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", http.MethodPost, retryPath(f), nil)
	env.owner().expectError(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", http.MethodPost, retryPath(f), nil, "Idempotency-Key", "short")
}

func TestRetryRejectsActiveAndNonFailedRuns(t *testing.T) {
	env := newQcEnv(t)
	f := env.seedFailedRun("")
	key := func() string { return "retry-key-" + qcSuffix(t) }
	env.exec(`UPDATE openfox.runs SET state = 'SUCCEEDED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, f.run)
	env.owner().expectError(http.StatusConflict, "RUN_NOT_RETRYABLE", http.MethodPost, retryPath(f), nil, "Idempotency-Key", key())
	// 原 Run 仍在运行：用另一个 FAILED 夹具改成 RUNNING（终态时间约束需要同时清空 finished_at）。
	g := env.seedFailedRun("")
	env.exec(`UPDATE openfox.runs SET state = 'RUNNING', finished_at = NULL, started_at = clock_timestamp(), lease_expires_at = clock_timestamp() + interval '1 hour'
		 WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, g.run)
	env.owner().expectError(http.StatusConflict, "RUN_ACTIVE", http.MethodPost, retryPath(g), nil, "Idempotency-Key", key())
	if env.pendingRetryInputs(g) != 0 {
		t.Fatal("rejected retry must not enqueue")
	}
}

func TestRetryRejectsCrossConversationAndCrossOrganization(t *testing.T) {
	env := newQcEnv(t)
	a := env.seedFailedRun("RATE_LIMITED")
	b := env.seedFailedRun("RATE_LIMITED")
	// Run 属于会话 a，却通过会话 b 的路径重试。
	path := "conversations/" + b.conversation + "/runs/" + a.run + "/retries"
	env.owner().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, path, nil, "Idempotency-Key", "retry-key-"+qcSuffix(t))
	// 组织外用户访问。
	env.outsider().expectError(http.StatusNotFound, "", http.MethodPost, retryPath(a), nil, "Idempotency-Key", "retry-key-"+qcSuffix(t))
	if env.pendingRetryInputs(a)+env.pendingRetryInputs(b) != 0 {
		t.Fatal("rejected retries must not enqueue")
	}
}

func TestRetryRejectsUnauthorizedAndNonAuthorCallers(t *testing.T) {
	env := newQcEnv(t)
	f := env.seedFailedRun("RATE_LIMITED")
	key := func() string { return "retry-key-" + qcSuffix(t) }
	// 无法看到私有会话的成员按不存在处理。
	env.member().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPost, retryPath(f), nil, "Idempotency-Key", key())
	env.addSpaceMember(f.space, qcMemberID, "MEMBER")
	env.exec(`UPDATE openfox.conversations SET visibility = 'SHARED' WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, f.conversation)
	// 被加为协作者（可评论）的成员能看到会话，但不是原输入作者：拒绝，不替原作者执行。
	env.owner().must(http.StatusNoContent, http.MethodPut, "conversations/"+f.conversation+"/members/"+qcMemberID, map[string]any{"access_level": "COMMENTER"}, nil)
	env.member().expectError(http.StatusForbidden, "ACTION_FORBIDDEN", http.MethodPost, retryPath(f), nil, "Idempotency-Key", key())
	if env.pendingRetryInputs(f) != 0 {
		t.Fatal("rejected retries must not enqueue")
	}
}

func TestRetryInProgressBlocksSecondRetryAndChainIsQueryable(t *testing.T) {
	env := newQcEnv(t)
	f := env.seedFailedRun("RATE_LIMITED")
	env.owner().must(http.StatusCreated, http.MethodPost, retryPath(f), nil, nil, "Idempotency-Key", "retry-key-"+qcSuffix(t))
	env.owner().expectError(http.StatusConflict, "RETRY_IN_PROGRESS", http.MethodPost, retryPath(f), nil, "Idempotency-Key", "retry-key-"+qcSuffix(t))
	var source string
	env.scalar(&source, `SELECT source_event_id FROM openfox.conversation_inputs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid
		AND status = 'PENDING' AND source_event_id LIKE 'assistant-retry:%'`, env.org, f.conversation)
	if !strings.HasPrefix(source, "assistant-retry:"+f.run+":") {
		t.Fatalf("source_event_id = %q", source)
	}
}

func TestRetryWorkspaceOverrideRules(t *testing.T) {
	env := newQcEnv(t)
	body := map[string]any{"workspace_selection": map[string]any{"mode": "DIRECT", "directory": "work/retry"}}
	// 会话已锁定目录且失败原因不是 WORKTREE_PREP_FAILED：拒绝覆盖。
	locked := env.seedFailedRun("RATE_LIMITED")
	env.exec(`UPDATE openfox.conversations SET workspace_selection = jsonb_build_object('mode', 'DIRECT', 'directory', '/tmp/a',
		'computer_id', (SELECT device_id FROM openfox.agents WHERE organization_id = $1::uuid AND id = $3::uuid))
		WHERE organization_id = $1::uuid AND id = $2::uuid`, env.org, locked.conversation, locked.agent)
	env.owner().expectError(http.StatusConflict, "WORKSPACE_OVERRIDE_NOT_ALLOWED", http.MethodPost, retryPath(locked), body, "Idempotency-Key", "retry-key-"+qcSuffix(t))
	if env.pendingRetryInputs(locked) != 0 {
		t.Fatal("rejected override must not enqueue")
	}
	// 非法的目录选择：400。
	free := env.seedFailedRun("RATE_LIMITED")
	env.owner().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, retryPath(free),
		map[string]any{"workspace_selection": map[string]any{"mode": "NOPE"}}, "Idempotency-Key", "retry-key-"+qcSuffix(t))
}
