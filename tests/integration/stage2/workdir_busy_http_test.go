package stage2

// 阶段 12d：设备「目录占用」回报端点 POST /v1/device/organizations/{org}/runs/{run}/workdir-busy 的 HTTP 集成测试
// （真实处理器：设备会话鉴权、租户事务、错误映射）。放在 stage2 包里是为了复用这里完整的设备注册与签名会话夹具。

import (
	"net/http"
	"strconv"
	"testing"
)

type workdirBusyResponse struct {
	Run struct {
		State      string `json:"state"`
		Generation int64  `json:"generation"`
		WaitReason string `json:"wait_reason"`
	} `json:"run"`
	Requeued bool `json:"requeued"`
}

func TestDeviceWorkdirBusyHTTP(t *testing.T) {
	f := newChatFixture(t, nil)
	e := f.env
	base := "/v1/device/organizations/" + e.org
	runID := f.queueRun(f.task())
	offer, ok := f.pass()[runID]
	if !ok {
		t.Fatal("run was not offered")
	}
	expectCode(t, e.deviceCall(f.device, http.MethodPost, base+"/offers/"+offer.OfferID+"/claim", map[string]any{}), http.StatusOK, "")
	path := base + "/runs/" + runID + "/workdir-busy"
	report := func(device devDevice, generation int) agResponse {
		t.Helper()
		return e.deviceCall(device, http.MethodPost, path, map[string]any{"generation": generation})
	}
	runRow := func() (state, wait string, generation, version int64) {
		t.Helper()
		if err := e.superuser.QueryRow(e.ctx, `SELECT state, wait_reason, generation, version FROM openfox.runs WHERE id = $1::uuid`, runID).
			Scan(&state, &wait, &generation, &version); err != nil {
			t.Fatal(err)
		}
		return
	}

	// 设备鉴权：不带设备会话、或带用户会话（没有设备令牌）都被拒绝，Run 不变。
	if response := e.anonymous(http.MethodPost, path, "", map[string]any{"generation": 1}); response.Status != http.StatusUnauthorized {
		t.Fatalf("anonymous report = %d %s", response.Status, response.Body)
	}
	if response := e.anonymous(http.MethodPost, path, "not-a-device-token", map[string]any{"generation": 1}); response.Status != http.StatusUnauthorized {
		t.Fatalf("bogus token report = %d %s", response.Status, response.Body)
	}
	// 同组织的另一台设备：不是它领取的 Run，按不存在处理（404，不泄露存在性）。
	other := e.registerDevice(e.issueToken(e.client(agOwnerID), "ORGANIZATION").InstallToken)
	expectCode(t, report(other, 1), http.StatusNotFound, "NOT_FOUND")
	// 参数与代次：0 无效；不是当前代次（也不是刚放回排队的下一代）返回 GENERATION_CONFLICT。
	expectCode(t, report(f.device, 0), http.StatusBadRequest, "INVALID_REQUEST")
	expectCode(t, report(f.device, 7), http.StatusConflict, "GENERATION_CONFLICT")
	if state, _, generation, _ := runRow(); state != "PREPARING" || generation != 1 {
		t.Fatalf("rejected reports changed the run: %s gen %d", state, generation)
	}

	// 成功：放回排队，generation 加 1。
	first := report(f.device, 1)
	expectCode(t, first, http.StatusOK, "")
	var body workdirBusyResponse
	first.decode(t, &body)
	if !body.Requeued || body.Run.State != "QUEUED" || body.Run.WaitReason != "WORKDIR_BUSY" || body.Run.Generation != 2 {
		t.Fatalf("first report = %s", first.Body)
	}
	_, _, _, version := runRow()
	// 重放同一代次的回报：200，同样的结果，Run 不再变化。
	again := report(f.device, 1)
	expectCode(t, again, http.StatusOK, "")
	var replay workdirBusyResponse
	again.decode(t, &replay)
	if !replay.Requeued || replay.Run.Generation != 2 {
		t.Fatalf("replayed report = %s", again.Body)
	}
	if _, _, generation, after := runRow(); generation != 2 || after != version {
		t.Fatalf("replay changed the run: gen %d version %d -> %d", generation, version, after)
	}
	// 旧代次的其他设备接口被拒绝（例如 prepare-failed）。
	expectCode(t, e.deviceCall(f.device, http.MethodPost, base+"/runs/"+runID+"/prepare-failed",
		map[string]any{"generation": 1, "failure_code": "PREPARE_FAILED", "detail": "stale"}), http.StatusConflict, "GENERATION_CONFLICT")

	// 退避期满后以新代次再派发、领取；用户请求取消后，回报返回 409 CANCEL_REQUESTED，设备改报 prepare-failed(CANCELED)。
	e.exec(`UPDATE openfox.runs SET updated_at = updated_at - interval '1 minute' WHERE id = $1::uuid`, runID)
	offer, ok = f.pass()[runID]
	if !ok || offer.Generation != 2 {
		t.Fatalf("requeued run offer = %+v %v", offer, ok)
	}
	expectCode(t, e.deviceCall(f.device, http.MethodPost, base+"/offers/"+offer.OfferID+"/claim", map[string]any{}), http.StatusOK, "")
	cancel := e.client(agOwnerID).do(http.MethodPost, "runs/"+runID+"/cancel", map[string]any{}, "Idempotency-Key", agNewID(t))
	expectCode(t, cancel, http.StatusOK, "")
	expectCode(t, report(f.device, 2), http.StatusConflict, "CANCEL_REQUESTED")
	if state, _, generation, _ := runRow(); state != "CANCEL_REQUESTED" || generation != 2 {
		t.Fatalf("report after cancel changed the run: %s gen %d", state, generation)
	}
	expectCode(t, e.deviceCall(f.device, http.MethodPost, base+"/runs/"+runID+"/prepare-failed",
		map[string]any{"generation": 2, "failure_code": "CANCELED", "detail": "canceled before start"}), http.StatusOK, "")
	if state, _, _, _ := runRow(); state != "CANCELED" {
		t.Fatalf("run state after prepare-failed = %s", state)
	}
	// 终态后再回报：409 RUN_STATE_CONFLICT。
	expectCode(t, report(f.device, 2), http.StatusConflict, "RUN_STATE_CONFLICT")
}

// 阶段 12d：DELETE 任务接口在活动 Run 全部是任务门禁 Run 时先取消它们再删除（204）；有其他活动 Run 时仍 409 RUN_ACTIVE。
func TestDeleteTaskWithGateRunHTTP(t *testing.T) {
	f := newChatFixture(t, nil)
	e := f.env
	owner := e.client(agOwnerID)
	deleteTask := func(taskID string) agResponse {
		t.Helper()
		var version int64
		e.scalar(&version, `SELECT version FROM openfox.tasks WHERE id = $1::uuid`, taskID)
		return owner.do(http.MethodDelete, "spaces/"+f.space+"/tasks/"+taskID+"?version="+strconv.FormatInt(version, 10), nil)
	}
	runState := func(runID string) string {
		t.Helper()
		var state string
		e.scalar(&state, `SELECT state FROM openfox.runs WHERE id = $1::uuid`, runID)
		return state
	}

	gatedTask := f.task()
	gatedRun := f.queueRun(gatedTask)
	e.exec(`UPDATE openfox.runs SET wait_reason = 'TASK_GATE' WHERE id = $1::uuid`, gatedRun)
	expectCode(t, deleteTask(gatedTask), http.StatusNoContent, "")
	if state := runState(gatedRun); state != "CANCELED" {
		t.Fatalf("gate run after task delete = %s", state)
	}

	busyTask := f.task()
	busyRun := f.queueRun(busyTask)
	e.exec(`UPDATE openfox.runs SET wait_reason = 'AGENT_CONCURRENCY' WHERE id = $1::uuid`, busyRun)
	expectCode(t, deleteTask(busyTask), http.StatusConflict, "RUN_ACTIVE")
	if state := runState(busyRun); state != "QUEUED" {
		t.Fatalf("queued run after rejected delete = %s", state)
	}
}
