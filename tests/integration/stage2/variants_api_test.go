package stage2

import (
	"net/http"
	"testing"
)

func TestAgentVariantAPIPermissionsAndReplay(t *testing.T) {
	e := newAgEnv(t)
	manager := e.client(agManagerID)
	member := e.client(agMemberID)
	owner := e.client(agOwnerID)
	other := e.client(agOutsiderID)
	space := e.createSpace("SHARED")
	parent := e.createAgent(manager, space, "SHARED", nil)
	path := agentPath(space, parent.ID) + "/variants"
	body := map[string]any{"space_id": space}
	member.expectError(http.StatusForbidden, "", http.MethodPost, path, body)
	other.expectError(http.StatusNotFound, "", http.MethodPost, path, body)
	private := e.createAgent(manager, space, "PRIVATE", nil)
	owner.expectError(http.StatusNotFound, "", http.MethodPost, agentPath(space, private.ID)+"/variants", body)
	const key = "variant-api-once"
	var first, replay agAgent
	manager.must(http.StatusCreated, http.MethodPost, path, body, &first, "Idempotency-Key", key)
	response := manager.must(http.StatusOK, http.MethodPost, path, body, &replay, "Idempotency-Key", key)
	if response.Header.Get("Idempotent-Replayed") != "true" || replay.ID != first.ID || first.Visibility != "PRIVATE" {
		t.Fatalf("replay=%+v first=%+v header=%s", replay, first, response.Header.Get("Idempotent-Replayed"))
	}
	manager.expectError(http.StatusConflict, "IDEMPOTENCY_CONFLICT", http.MethodPost, path,
		map[string]any{"space_id": space, "name": "different"}, "Idempotency-Key", key)
	manager.expectError(http.StatusConflict, "INVALID_VARIANT_PARENT", http.MethodPost, agentPath(space, first.ID)+"/variants", body)
	manager.expectError(http.StatusConflict, "INVALID_VARIANT_PARENT", http.MethodPost, agentPath(space, parent.ID)+"/inherit", map[string]any{"groups": []string{"instructions"}})
}
