package stage12e

// K-02 运行配置版本确认流转：daemon 拉取 → 校验 → 确认 → 心跳上报 → 电脑详情显示。

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"openfox/internal/daemon"
)

type runtimeConfig struct {
	ConfigVersion   int64          `json:"config_version"`
	AppliedVersion  int64          `json:"applied_version"`
	RestartRequired bool           `json:"restart_required"`
	Policy          map[string]any `json:"policy"`
}

func (e *qcEnv) runtimeConfig(client *qcClient) runtimeConfig {
	e.t.Helper()
	var config runtimeConfig
	client.must(http.StatusOK, http.MethodGet, "runtime-config", nil, &config)
	return config
}

func (e *qcEnv) patchPolicy(client *qcClient, config runtimeConfig, changes map[string]any, restart bool) qcResponse {
	e.t.Helper()
	policy := map[string]any{}
	for key, value := range config.Policy {
		policy[key] = value
	}
	for key, value := range changes {
		policy[key] = value
	}
	return client.do(http.MethodPatch, "runtime-config", map[string]any{
		"expected_version": config.ConfigVersion, "policy": policy, "restart_required": restart,
	})
}
func TestRuntimeConfigAckFlow(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	config := env.runtimeConfig(env.owner())
	if response := env.patchPolicy(env.owner(), config, map[string]any{"active_max": 3}, true); response.Status != http.StatusOK {
		t.Fatalf("patch = %d %s", response.Status, response.Body)
	}
	updated := env.runtimeConfig(env.owner())
	if updated.ConfigVersion != config.ConfigVersion+1 || updated.AppliedVersion >= updated.ConfigVersion {
		t.Fatalf("after patch = %+v", updated)
	}
	// daemon：拉取 → 校验 → 确认 → 写入心跳 capabilities.runtime_config。
	var published map[string]daemon.RuntimeConfigStatus
	sync := &daemon.RuntimeConfigSync{Client: env.auth(), Token: func(context.Context) (string, error) { return device.session, nil },
		Publish: func(status map[string]daemon.RuntimeConfigStatus) { published = status }}
	sync.Sync(env.ctx, []string{env.org})
	status, ok := published[env.org]
	if !ok || status.AppliedVersion != updated.ConfigVersion || status.Error != "" {
		t.Fatalf("published = %+v", published)
	}
	if acked := env.runtimeConfig(env.owner()); acked.AppliedVersion != updated.ConfigVersion {
		t.Fatalf("center applied_version = %d, want %d", acked.AppliedVersion, updated.ConfigVersion)
	}
	env.mustHeartbeat(device, map[string]any{"runtime_config": published})
	detail := env.device(env.owner(), env.org, device.ID)
	if detail.RuntimeConfigStatus == nil || detail.RuntimeConfigStatus.AppliedVersion != updated.ConfigVersion ||
		detail.RuntimeConfigStatus.Error != nil {
		t.Fatalf("detail status = %+v", detail.RuntimeConfigStatus)
	}
	// 启动之后才出现的、标记需重启的新版本 → 需重启。
	if response := env.patchPolicy(env.owner(), updated, map[string]any{"active_max": 4}, true); response.Status != http.StatusOK {
		t.Fatalf("patch 2 = %d", response.Status)
	}
	sync.Sync(env.ctx, []string{env.org})
	env.mustHeartbeat(device, map[string]any{"runtime_config": published})
	detail = env.device(env.owner(), env.org, device.ID)
	if detail.RuntimeConfigStatus == nil || !detail.RuntimeConfigStatus.RestartRequired {
		t.Fatalf("restart status = %+v", detail.RuntimeConfigStatus)
	}
	// 应用失败上报：未知错误码被丢弃，只显示白名单码。
	env.mustHeartbeat(device, map[string]any{"runtime_config": map[string]any{
		env.org: map[string]any{"applied_version": 2, "restart_required": false, "error": "ACK_FAILED"},
	}})
	detail = env.device(env.owner(), env.org, device.ID)
	if detail.RuntimeConfigStatus == nil || detail.RuntimeConfigStatus.Error == nil || *detail.RuntimeConfigStatus.Error != "ACK_FAILED" {
		t.Fatalf("failed status = %+v", detail.RuntimeConfigStatus)
	}
	// 旧 daemon：不上报 → null。
	env.mustHeartbeat(device, map[string]any{})
	if detail := env.device(env.owner(), env.org, device.ID); detail.RuntimeConfigStatus != nil {
		t.Fatalf("legacy status = %+v", detail.RuntimeConfigStatus)
	}
	// 其他组织的设备令牌不能读本组织配置。
	other := env.registerDevice(env.outsider(), env.otherOrg)
	if response := env.anonymous(http.MethodGet, fmt.Sprintf("/v1/device/organizations/%s/runtime-config", env.org), other.session, nil); response.Status == http.StatusOK {
		t.Fatalf("foreign device read runtime config: %s", response.Body)
	}
}
