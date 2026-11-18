package stage10b

// 再次运行接口的 Idempotency-Key（操作名 task.agent_run）：同键同参数重放不再入队，返回相同的 202 并带 Idempotent-Replayed；
// 同键不同参数 409 IDEMPOTENCY_CONFLICT；不带键时每次都入队（与原行为一致）。

import (
	"bytes"
	"net/http"
	"testing"
)

func TestStartTaskAgentRunIdempotency(t *testing.T) {
	s := newRunSpace(t)
	e := s.env
	task, first := s.assignedTask()
	path := runsPath(s.id, task.ID)
	assignedEvents := func() int {
		return e.count(`SELECT count(*) FROM openfox.task_agent_events WHERE task_id = $1::uuid AND kind = 'ASSIGNED'`, task.ID)
	}
	before := assignedEvents()
	key := "agent-run-" + qcSuffix(t)
	body := map[string]any{"agent_id": s.agent, "assignment_conversation": "new"}

	firstResponse := e.member().must(http.StatusAccepted, http.MethodPost, path, body, nil, "Idempotency-Key", key)
	if firstResponse.Header.Get("Idempotent-Replayed") != "" {
		t.Fatal("first call marked as replayed")
	}
	replay := e.member().must(http.StatusAccepted, http.MethodPost, path, body, nil, "Idempotency-Key", key)
	if replay.Header.Get("Idempotent-Replayed") != "true" || !bytes.Equal(bytes.TrimSpace(replay.Body), bytes.TrimSpace(firstResponse.Body)) {
		t.Fatalf("replay = %v %s, want Idempotent-Replayed and body %s", replay.Header, replay.Body, firstResponse.Body)
	}
	if n := assignedEvents() - before; n != 1 {
		t.Fatalf("ASSIGNED events after replay = %d, want 1", n)
	}
	// 重放不再降级：worker 建出的新会话在之后的重放后仍是唯一打开的指派会话，只有一条消息。
	e.dispatchOnce()
	second := s.openAssignment(task.ID)
	if second == first || e.assignMessages(second) != 1 {
		t.Fatalf("after replay: open=%s messages=%d", second, e.assignMessages(second))
	}
	e.member().must(http.StatusAccepted, http.MethodPost, path, body, nil, "Idempotency-Key", key)
	if got := s.openAssignment(task.ID); got != second {
		t.Fatalf("replay demoted conversation: open=%s want %s", got, second)
	}

	// 同键不同参数：409，不入队（未传 assignment_conversation 按 reuse 计入指纹，与 new 不同）。
	e.member().expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path,
		map[string]any{"agent_id": s.other, "assignment_conversation": "new"}, "Idempotency-Key", key)
	e.member().expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path,
		map[string]any{"agent_id": s.agent}, "Idempotency-Key", key)
	// 非法键：400。
	e.member().expectError(http.StatusBadRequest, "", http.MethodPost, path, body, "Idempotency-Key", "bad key!")
	if n := assignedEvents() - before; n != 1 {
		t.Fatalf("ASSIGNED events after conflicts = %d, want 1", n)
	}
	// 键按操作人隔离：别人用同一个键是独立的请求。
	if other := e.owner().must(http.StatusAccepted, http.MethodPost, path, map[string]any{"agent_id": s.agent}, nil,
		"Idempotency-Key", key); other.Header.Get("Idempotent-Replayed") != "" {
		t.Fatal("another actor's key was treated as a replay")
	}
	// 不带键：每次都入队。
	e.member().must(http.StatusAccepted, http.MethodPost, path, map[string]any{"agent_id": s.agent}, nil)
	e.member().must(http.StatusAccepted, http.MethodPost, path, map[string]any{"agent_id": s.agent}, nil)
	if n := assignedEvents() - before; n != 4 {
		t.Fatalf("ASSIGNED events = %d, want 4 (1 keyed + 1 other actor + 2 without key)", n)
	}
}
