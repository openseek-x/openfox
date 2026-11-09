package supervisor

import "testing"

// 适配器不支持中心要求的协作模式时在准备阶段失败（RUNTIME_UNAVAILABLE），不领取启动许可、不启动进程。
func TestSupervisorFailsUnsupportedCollaborationModeBeforeStarting(t *testing.T) {
	for _, mode := range []string{"DEFAULT", "PLAN"} {
		t.Run(mode, func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.extend = func(runContext *RunContext) { runContext.CollaborationMode = mode }
			newHarness(t, center, adapter, "")
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != "RUNTIME_UNAVAILABLE" || snapshot.starts != 0 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v starts=%d processes=%d", snapshot.prepareFailed, snapshot.starts, adapter.startCount())
			}
		})
	}
}

// 旧中心没有 collaboration_mode 字段（空串）或是历史值 FOLLOW_AGENT：按 FOLLOW_RUNTIME 照常执行。
func TestSupervisorTreatsMissingCollaborationModeAsFollowRuntime(t *testing.T) {
	for _, mode := range []string{"", "FOLLOW_RUNTIME", "FOLLOW_AGENT"} {
		center, adapter := newCenter(), &fakeAdapter{}
		center.extend = func(runContext *RunContext) { runContext.CollaborationMode = mode }
		newHarness(t, center, adapter, "")
		waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
		adapter.mu.Lock()
		got := adapter.requests[0].CollaborationMode
		adapter.mu.Unlock()
		if got != "FOLLOW_RUNTIME" {
			t.Fatalf("mode %q reached the adapter as %q", mode, got)
		}
	}
}

// 声明支持该模式的适配器收到模式并执行。
func TestSupervisorPassesSupportedCollaborationModeToAdapter(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{collaborationModes: []string{"FOLLOW_RUNTIME", "DEFAULT", "PLAN"}}
	center.extend = func(runContext *RunContext) { runContext.CollaborationMode = "PLAN" }
	newHarness(t, center, adapter, "")
	waitFor(t, "process start", func() bool { return adapter.startCount() == 1 })
	adapter.mu.Lock()
	got := adapter.requests[0].CollaborationMode
	adapter.mu.Unlock()
	if got != "PLAN" {
		t.Fatalf("adapter got mode %q", got)
	}
}
