package stage12e

// D-05：并发上限取中心上限与本机上限（daemon 上报的 --max-runs）的较小值；中心值不反向覆盖本机上报。

import "testing"

func TestEffectiveSlotsTakeTheSmallerLimit(t *testing.T) {
	env := newQcEnv(t)
	device := env.registerDevice(env.owner(), env.org)
	env.exec(`UPDATE openfox.physical_devices SET max_slots = 4 WHERE id = $1::uuid`, device.ID)
	// 旧 daemon：不上报本机上限 → 生效 = 中心上限。
	env.mustHeartbeat(device, map[string]any{})
	if detail := env.device(env.owner(), env.org, device.ID); detail.LocalMaxRuns != nil || detail.EffectiveMaxSlots != 4 {
		t.Fatalf("legacy detail = %+v", detail)
	}
	// 新 daemon 上报 --max-runs 1：生效 1；派发只给出 1 个名额。
	env.mustHeartbeat(device, map[string]any{"local_max_runs": 1})
	detail := env.device(env.owner(), env.org, device.ID)
	if detail.LocalMaxRuns == nil || *detail.LocalMaxRuns != 1 || detail.MaxSlots != 4 || detail.EffectiveMaxSlots != 1 {
		t.Fatalf("detail = %+v", detail)
	}
	worker := env.worker()
	space, agent := env.seedAgent(env.owner(), env.org, qcOwnerID, device.ID)
	first, second := env.queueRun(env.org, space, agent, qcOwnerID), env.queueRun(env.org, space, agent, qcOwnerID)
	offers := env.pass(worker, env.org)
	if (offers[first] == "") == (offers[second] == "") {
		t.Fatalf("offers = %v, want exactly one", offers)
	}
	waiting := second
	if offers[first] == "" {
		waiting = first
	}
	if reason := env.waitReason(env.org, waiting); reason != "COMPUTER_CONCURRENCY" {
		t.Fatalf("wait reason = %q", reason)
	}
	// 中心上限更小时取中心值；中心值不会反向覆盖本机上报。
	env.mustHeartbeat(device, map[string]any{"local_max_runs": 8})
	if detail := env.device(env.owner(), env.org, device.ID); detail.EffectiveMaxSlots != 4 || *detail.LocalMaxRuns != 8 {
		t.Fatalf("center smaller = %+v", detail)
	}
	// 越界的本机值视为未上报。
	env.mustHeartbeat(device, map[string]any{"local_max_runs": 5000})
	if detail := env.device(env.owner(), env.org, device.ID); detail.LocalMaxRuns != nil {
		t.Fatalf("out-of-range local limit kept: %+v", detail)
	}
}
