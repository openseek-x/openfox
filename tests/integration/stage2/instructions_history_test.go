package stage2

import (
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"openfox/internal/agents"
)

type instructionHistoryPage struct {
	Configs []agents.ConfigVersion `json:"config_versions"`
	Next    *uint64                `json:"next_before_version"`
}

// Real HTTP sessions and tenant-scoped PostgreSQL transactions; no external runtime is contacted.
func TestInstructionHistoryPaginationRestoreAndAccess(t *testing.T) {
	e := newAgEnv(t)
	manager, member, outsider := e.client(agManagerID), e.client(agMemberID), e.client(agOutsiderID)
	space := e.createSpace("SHARED")
	initial := e.configBody(map[string]any{"instructions": "# 旧指令\n\n- 中文"})
	created := e.createAgent(manager, space, "SHARED", map[string]any{"config": initial})
	path := agentPath(space, created.ID) + "/config-versions"
	var old instructionHistoryPage
	manager.must(http.StatusOK, http.MethodGet, path, nil, &old)
	if len(old.Configs) != 1 || old.Configs[0].Instructions != initial["instructions"] {
		t.Fatalf("initial history = %+v", old)
	}
	current := e.configBody(map[string]any{"instructions": "new", "model": "current-model", "chat_concurrency": "MANUAL", "chat_concurrency_value": 3, "workdir_isolation": "DIRECT", "custom_args": []string{"--test"}, "expected_agent_version": created.Version})
	var updated agents.Agent
	manager.must(http.StatusCreated, http.MethodPost, path, current, &updated)
	// Restore only the instruction input while retaining the complete current runtime configuration.
	current["instructions"], current["expected_agent_version"] = old.Configs[0].Instructions, updated.Version
	var restored agents.Agent
	manager.must(http.StatusCreated, http.MethodPost, path, current, &restored)
	if restored.CurrentConfigVersion != 3 || restored.Config.Instructions != old.Configs[0].Instructions || restored.Config.Model != "current-model" || restored.Config.ComputerID != e.deviceID || restored.Config.GrantID != e.grantID || restored.Config.ChatConcurrency != "MANUAL" || !reflect.DeepEqual(restored.Config.CustomArgs, []string{"--test"}) {
		t.Fatalf("restore = %+v", restored.Config)
	}
	// A stale retry cannot create a duplicate version.
	manager.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPost, path, current)
	var page instructionHistoryPage
	manager.must(http.StatusOK, http.MethodGet, path+"?limit=2", nil, &page)
	if len(page.Configs) != 2 || page.Configs[0].Version != 3 || page.Configs[1].Version != 2 || page.Next == nil || *page.Next != 2 {
		t.Fatalf("page = %+v", page)
	}
	var last instructionHistoryPage
	manager.must(http.StatusOK, http.MethodGet, fmt.Sprintf("%s?limit=2&before_version=%d", path, *page.Next), nil, &last)
	if len(last.Configs) != 1 || last.Next != nil || !reflect.DeepEqual(last.Configs[0], old.Configs[0]) {
		t.Fatalf("immutable final page = %+v", last)
	}
	manager.must(http.StatusOK, http.MethodGet, path+"?limit=2&before_version=1", nil, &last)
	if len(last.Configs) != 0 || last.Next != nil {
		t.Fatalf("empty page = %+v", last)
	}
	// New versions between page reads do not duplicate or skip older versions.
	current["expected_agent_version"], current["instructions"] = restored.Version, ""
	manager.must(http.StatusCreated, http.MethodPost, path, current, &updated)
	manager.must(http.StatusOK, http.MethodGet, path+"?limit=2&before_version=2", nil, &last)
	if len(last.Configs) != 1 || last.Configs[0].Version != 1 {
		t.Fatal("unstable keyset page")
	}
	for _, suffix := range []string{"?limit=0", "?limit=101", "?limit=1&before_version=-1", "?before_version=9223372036854775808"} {
		manager.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodGet, path+suffix, nil)
	}
	member.must(http.StatusOK, http.MethodGet, path+"?limit=20", nil, &last)
	member.expectError(http.StatusForbidden, "", http.MethodPost, path, current)
	outsider.expectError(http.StatusNotFound, "", http.MethodGet, path+"?limit=20", nil)
	private := e.createAgent(manager, space, "PRIVATE", nil)
	for _, client := range []*agClient{member, e.client(agOwnerID), e.client(agAdminID)} {
		client.expectError(http.StatusNotFound, "", http.MethodGet, agentPath(space, private.ID)+"/config-versions?limit=20", nil)
	}
}

func TestInstructionHistoryConcurrentRestore(t *testing.T) {
	e := newAgEnv(t)
	manager := e.client(agManagerID)
	space := e.createSpace("SHARED")
	created := e.createAgent(manager, space, "SHARED", nil)
	path := agentPath(space, created.ID) + "/config-versions"
	body := e.configBody(map[string]any{"instructions": "same restored instructions", "expected_agent_version": created.Version})
	statuses := make([]int, 4)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) { defer wg.Done(); statuses[i] = manager.do(http.MethodPost, path, body).Status }(i)
	}
	wg.Wait()
	success := 0
	for _, status := range statuses {
		if status == http.StatusCreated {
			success++
		} else if status != http.StatusConflict {
			t.Fatalf("unexpected statuses: %v", statuses)
		}
	}
	if success != 1 {
		t.Fatalf("successful restores = %d (%v)", success, statuses)
	}
	var history instructionHistoryPage
	manager.must(http.StatusOK, http.MethodGet, path+"?limit=20", nil, &history)
	if len(history.Configs) != 2 || history.Configs[0].Version != 2 {
		t.Fatalf("concurrent history = %+v", history)
	}
}
