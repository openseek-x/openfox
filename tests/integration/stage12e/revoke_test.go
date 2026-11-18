package stage12e

// D-01 授权撤销：撤销后派发被拒、设备侧领取被拒；撤销权限语义不变（仅组织 Owner/Admin）。

import (
	"net/http"
	"testing"
)

func TestRevokedGrantStopsDispatch(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	env.exec(`UPDATE openfox.physical_devices SET last_seen_at = clock_timestamp() WHERE id = $1::uuid`, device.ID)
	worker := env.worker()
	space, agent := env.seedAgent(env.owner(), env.org, qcOwnerID, device.ID)
	run := env.queueRun(env.org, space, agent, qcOwnerID)
	env.admin().must(http.StatusNoContent, http.MethodPost, "devices/"+device.ID+"/revoke", nil, nil)
	if offers := env.pass(worker, env.org); offers[run] != "" {
		t.Fatal("run offered to a revoked computer")
	}
	// 排队中的 Run 走既有的 AUTHORIZATION_REVOKED 路径结束。
	var state, failure string
	env.superuser.QueryRow(env.ctx, `SELECT state, COALESCE(failure_code, '') FROM openfox.runs WHERE organization_id = $1::uuid AND id = $2::uuid`,
		env.org, run).Scan(&state, &failure)
	if state != "FAILED" || failure != "AUTHORIZATION_REVOKED" {
		t.Fatalf("queued run after revoke = %s %s, want FAILED AUTHORIZATION_REVOKED", state, failure)
	}
	if response := env.anonymous(http.MethodGet, "/v1/device/organizations/"+env.org+"/offers", device.session, nil); response.Status == http.StatusOK {
		t.Fatalf("revoked device can list offers: %s", response.Body)
	}
	// 普通成员不能撤销（权限语义不变）。
	other := env.registerDevice(env.owner(), env.org)
	env.member().expectError(http.StatusForbidden, "", http.MethodPost, "devices/"+other.ID+"/revoke", nil)
}
