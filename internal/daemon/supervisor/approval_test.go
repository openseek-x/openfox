package supervisor

import "testing"

// 适配器不支持中心要求的权限档位：在准备阶段失败（RUNTIME_UNAVAILABLE），不领取启动许可、不启动进程。
func TestSupervisorFailsUnsupportedApprovalModeBeforeStarting(t *testing.T) {
	for _, mode := range []string{"READ_ONLY", "WORKSPACE_WRITE", "FULL_ACCESS"} {
		t.Run(mode, func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.extend = func(runContext *RunContext) { runContext.ApprovalMode = mode }
			newHarness(t, center, adapter, "")
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != "RUNTIME_UNAVAILABLE" || snapshot.starts != 0 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v starts=%d processes=%d", snapshot.prepareFailed, snapshot.starts, adapter.startCount())
			}
		})
	}
}

// 旧中心没有 approval_mode 字段（空串）：按 FOLLOW_RUNTIME 执行，行为与之前完全一致。
func TestSupervisorTreatsMissingApprovalModeAsFollowRuntime(t *testing.T) {
	for _, mode := range []string{"", "FOLLOW_RUNTIME"} {
		center, adapter := newCenter(), &fakeAdapter{}
		center.extend = func(runContext *RunContext) { runContext.ApprovalMode = mode }
		newHarness(t, center, adapter, "")
		waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
		adapter.mu.Lock()
		got := adapter.requests[0].ApprovalMode
		adapter.mu.Unlock()
		if got != "FOLLOW_RUNTIME" {
			t.Fatalf("mode %q reached the adapter as %q", mode, got)
		}
	}
}

// 声明支持该档位的适配器收到档位并执行。
func TestSupervisorPassesSupportedApprovalModeToAdapter(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{approvalModes: []string{"FOLLOW_RUNTIME", "READ_ONLY"}}
	center.extend = func(runContext *RunContext) { runContext.ApprovalMode = "READ_ONLY" }
	newHarness(t, center, adapter, "")
	waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
	adapter.mu.Lock()
	got := adapter.requests[0].ApprovalMode
	adapter.mu.Unlock()
	if got != "READ_ONLY" {
		t.Fatalf("adapter got mode %q", got)
	}
}
