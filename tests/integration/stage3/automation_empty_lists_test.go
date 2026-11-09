package stage3

import (
	"bytes"
	"net/http"
	"testing"
)

// 列表接口在没有数据时必须返回空数组：契约里 automations / runs / deliveries 都是数组，
// 旧实现把 nil 切片编码成 null，前端 flatMap 后读 item.status 白屏。
func TestAutomationListsEncodeEmptyArrays(t *testing.T) {
	env := newS3Env(t)
	ensureDirectory(env)
	agent := env.insertAgent(env.space.ID)
	withWebhookServer(env)
	manager := env.manager()

	// 空间里还没有自动化
	list := manager.must(http.StatusOK, http.MethodGet, automationsPath(env.space.ID), nil, nil)
	if !bytes.Contains(list.Body, []byte(`"automations":[]`)) {
		t.Fatalf("empty automation list body = %s, want \"automations\":[]", list.Body)
	}

	// 加了 Webhook 触发器、还没有任何投递与运行
	created := env.createAutomation(agent, map[string]any{"kind": "webhook", "enabled": true, "config": map[string]any{}})
	base := automationsPath(env.space.ID) + "/" + created.Automation.ID

	deliveries := manager.must(http.StatusOK, http.MethodGet, base+"/deliveries", nil, nil)
	if !bytes.Contains(deliveries.Body, []byte(`"deliveries":[]`)) {
		t.Fatalf("empty deliveries body = %s, want \"deliveries\":[]", deliveries.Body)
	}
	runs := manager.must(http.StatusOK, http.MethodGet, base+"/runs", nil, nil)
	if !bytes.Contains(runs.Body, []byte(`"runs":[]`)) {
		t.Fatalf("empty runs body = %s, want \"runs\":[]", runs.Body)
	}
	detail := manager.must(http.StatusOK, http.MethodGet, base, nil, nil)
	for _, want := range []string{`"notify_user_ids":[`, `"ip_allowlist":[]`} {
		if !bytes.Contains(detail.Body, []byte(want)) {
			t.Fatalf("automation detail body = %s, want %s", detail.Body, want)
		}
	}
}
