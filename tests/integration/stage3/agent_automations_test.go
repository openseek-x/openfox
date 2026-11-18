package stage3

import (
	"net/http"
	"testing"

	"openfox/internal/automations"
)

// Agent 内嵌入口沿用空间自动化 API；过滤在客户端，保存/回读和授权仍由现有服务负责。
func TestAgentAutomationEntryUsesExistingSpaceContract(t *testing.T) {
	env := newS3Env(t)
	firstAgent, secondAgent := env.insertAgent(env.space.ID), env.insertAgent(env.space.ID)
	first := env.createAutomation(firstAgent, scheduleTrigger("daily", map[string]any{"time": "09:00"}))
	second := env.createAutomation(secondAgent, scheduleTrigger("daily", map[string]any{"time": "10:00"}))
	base := automationsPath(env.space.ID)
	var list struct {
		Automations []automations.Automation `json:"automations"`
	}
	env.manager().must(http.StatusOK, http.MethodGet, base, nil, &list)
	if len(list.Automations) != 2 {
		t.Fatalf("space list has %d entries, want both agents", len(list.Automations))
	}
	want := map[string]string{first.Automation.ID: firstAgent, second.Automation.ID: secondAgent}
	for _, item := range list.Automations {
		if item.SpaceID != env.space.ID || item.Assignee.Kind != "AGENT" || item.Assignee.ID != want[item.ID] {
			t.Fatalf("persisted automation = %+v", item)
		}
		var reloaded automations.Automation
		env.manager().must(http.StatusOK, http.MethodGet, base+"/"+item.ID, nil, &reloaded)
		if reloaded.Assignee.ID != item.Assignee.ID || reloaded.SpaceID != env.space.ID {
			t.Fatalf("detail lost space or assignee: %+v", reloaded)
		}
	}
	env.addSpaceMember(env.space.ID, s3OtherID, "VIEWER")
	env.other().must(http.StatusOK, http.MethodGet, base, nil, &list)
	for _, item := range list.Automations {
		if item.CanEdit {
			t.Fatal("read-only member may edit automation")
		}
	}
	env.other().expectError(http.StatusForbidden, "", http.MethodPost, base,
		automationBody(firstAgent, scheduleTrigger("daily", map[string]any{"time": "11:00"})))
	env.exec(`UPDATE openfox.agents SET archived_at = clock_timestamp() WHERE organization_id=$1::uuid AND id=$2::uuid`, env.org, firstAgent)
	env.manager().expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, base,
		automationBody(firstAgent, scheduleTrigger("daily", map[string]any{"time": "11:00"})))
	if got := env.count(`SELECT count(*) FROM openfox.automations WHERE organization_id=$1::uuid AND space_id=$2::uuid`, env.org, env.space.ID); got != 2 {
		t.Fatalf("rejected writes changed automation count: %d", got)
	}
}
