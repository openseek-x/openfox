package supervisor

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
)

// 阶段 12d：目录占用回报、多根 DIRECT 选择与本机路径锁兜底。

// holdDirect 以另一个会话的身份占用目录（模拟本机其他 Run）。
func holdDirect(t *testing.T, h *harness, path string) *workdir.PreparedWorkspace {
	t.Helper()
	held, err := h.supervisor.config.Workdir.PrepareDirectRun(context.Background(), workdir.BindingKey{OrganizationID: testOrg, SpaceID: "s", ConversationID: "other"}, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	return held
}

func releaseOffer(h *harness) {
	h.center.mu.Lock()
	h.center.offerAvailable = true
	h.center.mu.Unlock()
	h.supervisor.HandleMessage(dispatchWake())
}

func directCenter(roots []string, requeue bool) (*fakeCenter, *fakeAdapter) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.directoryMode, center.directoryRoots = "EXPLICIT", roots
	center.extend = func(runContext *RunContext) {
		runContext.WorkdirIsolation = IsolationDirect
		runContext.WorkdirBusyRequeue = requeue
	}
	center.offerAvailable = false
	return center, adapter
}

func TestSupervisorWorkdirBusyReportsInsteadOfWaiting(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	// 本地等待上限保持 5 分钟：若仍在本地等待，测试会超时。
	center, adapter := directCenter([]string{"team/openfox"}, true)
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	holdDirect(t, h, repo)
	releaseOffer(h)
	waitFor(t, "workdir-busy report", func() bool { return len(center.snapshot().workdirBusy) == 1 })
	h.stop()
	snapshot := center.snapshot()
	if snapshot.workdirBusy[0] != 1 || len(snapshot.prepareFailed) != 0 || snapshot.starts != 0 || adapter.startCount() != 0 {
		t.Fatalf("workdir-busy=%v prepare-failed=%v starts=%d processes=%d", snapshot.workdirBusy, snapshot.prepareFailed, snapshot.starts, adapter.startCount())
	}
	// 回报之前 prep_env 已以 interrupted 结束并送达（回报后旧代次事件会被拒绝）。
	if snapshot.eventsAtBusy[0] != len(snapshot.accepted) || snapshot.eventsAtBusy[0] == 0 {
		t.Fatalf("events at report = %v, accepted = %d", snapshot.eventsAtBusy, len(snapshot.accepted))
	}
	interrupted := false
	for _, event := range snapshot.accepted {
		if strings.Contains(string(event.Payload), runtime.StepKindPrepEnv) && strings.Contains(string(event.Payload), `"state":"`+runtime.StepInterrupted+`"`) {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatal("prep_env was not ended as interrupted before the report")
	}
	// 本地日志把该代次记为放弃，不会在本机重复执行。
	state, err := h.journal.State(context.Background(), testRun, 1)
	if err != nil || state != JournalAbandoned {
		t.Fatalf("journal state = %q, %v", state, err)
	}
}

func TestSupervisorWorkdirBusyReportOutcomes(t *testing.T) {
	requireGit(t)
	for _, tc := range []struct {
		name   string
		status int
		code   string
		want   string
	}{
		{"cancel requested", http.StatusConflict, "CANCEL_REQUESTED", "CANCELED"},
		// 中心回退到旧版本（不认识该端点）：退回旧行为。
		{"rejected", http.StatusNotFound, "NOT_FOUND", "WORKTREE_PREP_FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := gitRepository(t)
			center, adapter := directCenter([]string{"team/openfox"}, true)
			center.workdirBusyStatus, center.workdirBusyCode = tc.status, tc.code
			h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
			holdDirect(t, h, repo)
			releaseOffer(h)
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != tc.want || len(snapshot.workdirBusy) != 1 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v workdir-busy=%v processes=%d", snapshot.prepareFailed, snapshot.workdirBusy, adapter.startCount())
			}
		})
	}
}

func TestSupervisorWorkdirBusyReportRetriesTransientFailures(t *testing.T) {
	requireGit(t)
	previous := workdirBusyReportBackoff
	workdirBusyReportBackoff = 10 * time.Millisecond
	t.Cleanup(func() { workdirBusyReportBackoff = previous })
	repo := gitRepository(t)
	center, adapter := directCenter([]string{"team/openfox"}, true)
	center.workdirBusyStatus, center.workdirBusyCode = http.StatusServiceUnavailable, "NOT_READY"
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	holdDirect(t, h, repo)
	releaseOffer(h)
	waitFor(t, "bounded retries", func() bool { return len(center.snapshot().workdirBusy) == workdirBusyReportAttempts })
	time.Sleep(100 * time.Millisecond)
	snapshot := center.snapshot()
	// 有限重试后放弃，交给中心的租约过期处理；不报 prepare-failed，也不启动。
	if len(snapshot.workdirBusy) != workdirBusyReportAttempts || len(snapshot.prepareFailed) != 0 || adapter.startCount() != 0 {
		t.Fatalf("workdir-busy=%v prepare-failed=%v processes=%d", snapshot.workdirBusy, snapshot.prepareFailed, adapter.startCount())
	}
}

func TestSupervisorWithoutRequeueCapabilityKeepsLocalWait(t *testing.T) {
	requireGit(t)
	previousLimit, previousPoll := directWaitLimit, directWaitPoll
	directWaitLimit, directWaitPoll = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { directWaitLimit, directWaitPoll = previousLimit, previousPoll })
	repo := gitRepository(t)
	// 旧中心不下发 workdir_busy_requeue：保持本地等待，超时后 WORKTREE_PREP_FAILED，不回报。
	center, adapter := directCenter([]string{"team/openfox"}, false)
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	holdDirect(t, h, repo)
	releaseOffer(h)
	waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
	snapshot := center.snapshot()
	if snapshot.prepareFailed[0] != "WORKTREE_PREP_FAILED" || len(snapshot.workdirBusy) != 0 || adapter.startCount() != 0 {
		t.Fatalf("prepare-failed=%v workdir-busy=%v processes=%d", snapshot.prepareFailed, snapshot.workdirBusy, adapter.startCount())
	}
}

func TestSupervisorMultiRootDirectPicksFirstFreeRoot(t *testing.T) {
	requireGit(t)
	first, second := gitRepository(t), gitRepository(t)
	center, adapter := directCenter([]string{"team/a", "team/b"}, true)
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/a": first, "team/b": second})
	holdDirect(t, h, first)
	releaseOffer(h)
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	path := adapter.requests[0].WorkingDirectory
	adapter.mu.Unlock()
	if path != second || len(center.snapshot().workdirBusy) != 0 {
		t.Fatalf("multi-root DIRECT used %s (want free root %s), workdir-busy=%v", path, second, center.snapshot().workdirBusy)
	}
}

func TestSupervisorMultiRootDirectFirstRootWhenAllFree(t *testing.T) {
	requireGit(t)
	first, second := gitRepository(t), gitRepository(t)
	center, adapter := directCenter([]string{"team/a", "team/b"}, true)
	center.offerAvailable = true
	newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/a": first, "team/b": second})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	path := adapter.requests[0].WorkingDirectory
	adapter.mu.Unlock()
	if path != first {
		t.Fatalf("multi-root DIRECT used %s, want first root %s", path, first)
	}
}

func TestSupervisorMultiRootDirectAllBusyReports(t *testing.T) {
	requireGit(t)
	first, second := gitRepository(t), gitRepository(t)
	center, adapter := directCenter([]string{"team/a", "team/b"}, true)
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/a": first, "team/b": second})
	holdDirect(t, h, first)
	holdDirect(t, h, second)
	releaseOffer(h)
	waitFor(t, "workdir-busy report", func() bool { return len(center.snapshot().workdirBusy) == 1 })
	if adapter.startCount() != 0 || len(center.snapshot().prepareFailed) != 0 {
		t.Fatalf("processes=%d prepare-failed=%v", adapter.startCount(), center.snapshot().prepareFailed)
	}
}

func TestSupervisorMultiRootDirectAllBusyWaitsWithoutCapability(t *testing.T) {
	requireGit(t)
	previousPoll := directWaitPoll
	directWaitPoll = 20 * time.Millisecond
	t.Cleanup(func() { directWaitPoll = previousPoll })
	first, second := gitRepository(t), gitRepository(t)
	center, adapter := directCenter([]string{"team/a", "team/b"}, false)
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/a": first, "team/b": second})
	holdDirect(t, h, first)
	heldSecond := holdDirect(t, h, second)
	releaseOffer(h)
	time.Sleep(200 * time.Millisecond)
	if adapter.startCount() != 0 {
		t.Fatal("started while every root was busy")
	}
	// 第二个根释放后在同一次等待中取得它。
	_ = heldSecond.Release()
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	path := adapter.requests[0].WorkingDirectory
	adapter.mu.Unlock()
	if path != second || len(center.snapshot().workdirBusy) != 0 {
		t.Fatalf("used %s (want %s), workdir-busy=%v", path, second, center.snapshot().workdirBusy)
	}
}

func TestSupervisorMultiRootDirectSkipsUnmappedRoots(t *testing.T) {
	requireGit(t)
	second := gitRepository(t)
	center, adapter := directCenter([]string{"team/missing", "team/b"}, true)
	center.offerAvailable = true
	newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/b": second})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	path := adapter.requests[0].WorkingDirectory
	adapter.mu.Unlock()
	if path != second {
		t.Fatalf("used %s, want mapped root %s", path, second)
	}
}

func TestSupervisorMultiRootDirectNoMappedRootIsRepoUnavailable(t *testing.T) {
	center, adapter := directCenter([]string{"team/x", "team/y"}, true)
	center.offerAvailable = true
	newHarnessWithRoots(t, center, adapter, "", map[string]string{})
	waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
	if got := center.snapshot().prepareFailed[0]; got != "REPO_UNAVAILABLE" || adapter.startCount() != 0 {
		t.Fatalf("prepare-failed=%s processes=%d", got, adapter.startCount())
	}
}

// 会话已绑定第二个根时只用该根，即使第一个根空闲也不切换（I-07）；它被占用时回报而不是换根。
func TestSupervisorMultiRootDirectKeepsBoundRoot(t *testing.T) {
	requireGit(t)
	first, second := gitRepository(t), gitRepository(t)
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "free", true: "busy"}[busy], func(t *testing.T) {
			center, adapter := directCenter([]string{"team/a", "team/b"}, true)
			h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/a": first, "team/b": second}, "device-1")
			rc := RunContext{OrganizationID: testOrg, SpaceID: "space", ConversationID: "conversation", AgentID: "agent"}
			key := workdir.BindingKey{OrganizationID: testOrg, SpaceID: "space", ConversationID: "conversation", AgentID: "agent"}
			if err := h.journal.saveConversationWorkspace(context.Background(), "device-1", rc, &savedWorkspace{Directory: "team/b",
				Binding: workdir.Binding{Key: key, Mode: workdir.ModeDirect, ConfiguredPath: second, ActualPath: second, IsGit: true}}); err != nil {
				t.Fatal(err)
			}
			if busy {
				holdDirect(t, h, second)
			}
			releaseOffer(h)
			if busy {
				waitFor(t, "workdir-busy report", func() bool { return len(center.snapshot().workdirBusy) == 1 })
				if adapter.startCount() != 0 {
					t.Fatal("switched to another root instead of reporting the bound root busy")
				}
				return
			}
			waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
			adapter.mu.Lock()
			path := adapter.requests[0].WorkingDirectory
			adapter.mu.Unlock()
			if path != second {
				t.Fatalf("used %s, want bound root %s", path, second)
			}
		})
	}
}

// 本机路径锁是最后一道防线：即使中心同时派发，同一目录也只能被一个 Run 取得。
func TestDirectPathLockIsLastDefense(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: t.TempDir(), AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	held, err := manager.PrepareDirectRun(context.Background(), workdir.BindingKey{OrganizationID: testOrg, SpaceID: "s", ConversationID: "one"}, repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PrepareDirectRun(context.Background(), workdir.BindingKey{OrganizationID: testOrg, SpaceID: "s", ConversationID: "two"}, repo); !workdir.IsPathConflict(err) {
		t.Fatalf("second DIRECT run on the same directory: %v", err)
	}
	_ = held.Release()
	again, err := manager.PrepareDirectRun(context.Background(), workdir.BindingKey{OrganizationID: testOrg, SpaceID: "s", ConversationID: "two"}, repo)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Release()
}

func TestResolveWorkspaceMultiRoot(t *testing.T) {
	roots := map[string]string{"a": "/a", "b": "/b"}
	mode, source, err := resolveWorkspace(RunContext{DirectoryMode: "EXPLICIT", DirectoryRoots: []string{"a", "b"}, WorkdirIsolation: IsolationDirect}, roots)
	if err != nil || mode != workdir.ModeSpecifiedGit || source != "" {
		t.Fatalf("DIRECT multi-root = %q %q %v", mode, source, err)
	}
	// 非 DIRECT 隔离仍只支持单个根（不改变 worktree 行为）。
	if _, _, err := resolveWorkspace(RunContext{DirectoryMode: "EXPLICIT", DirectoryRoots: []string{"a", "b"}, WorkdirIsolation: "WORKTREE"}, roots); err == nil {
		t.Fatal("WORKTREE multi-root should still be rejected")
	}
}
