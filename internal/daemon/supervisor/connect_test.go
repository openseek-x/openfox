package supervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon/workdir"
)

func TestReservedEnvironment(t *testing.T) {
	for _, name := range []string{"CODEX_HOME", "HOME", "PATH", "OPENFOX_TOKEN", "CODEX_SANDBOX", "DYLD_INSERT_LIBRARIES", "LD_PRELOAD"} {
		if !ReservedEnvironment(name) {
			t.Errorf("%s should be reserved", name)
		}
	}
	// 大小写按原样匹配；普通变量不受影响。
	for _, name := range []string{"OPENAI_API_KEY", "home", "Path", "MY_CODEX_HOME", "GITHUB_TOKEN", "LDFLAGS"} {
		if ReservedEnvironment(name) {
			t.Errorf("%s should not be reserved", name)
		}
	}
}

func TestSupervisorUsesSharedTaskScope(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.extend = func(runContext *RunContext) {
		runContext.SharedTaskCrewID = "crew-one"
		runContext.TaskID = "task-one"
	}
	h := newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	key := workdir.BindingKey{OrganizationID: testOrg, SpaceID: "space", AgentID: "crew:crew-one", ConversationID: "task:task-one"}
	binding, ok := h.supervisor.config.Workdir.Bindings().Lookup(key)
	if !ok || binding.ActualPath == "" {
		t.Fatal("supervisor did not bind the crew task workspace")
	}
	center.mu.Lock()
	path := center.lastContextPath
	center.mu.Unlock()
	if !strings.Contains(path, "shared_task_dir=1") {
		t.Fatal("device did not advertise shared-directory support")
	}
}

func TestSupervisorSharedDirectoryWaitsForReleaseOrTimesOut(t *testing.T) {
	previousLimit, previousPoll := directWaitLimit, directWaitPoll
	directWaitLimit, directWaitPoll = 400*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { directWaitLimit, directWaitPoll = previousLimit, previousPoll })
	for _, release := range []bool{true, false} {
		t.Run(map[bool]string{true: "release", false: "timeout"}[release], func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.offerAvailable = false
			center.extend = func(c *RunContext) { c.SharedTaskCrewID = "crew"; c.TaskID = "task" }
			h := newHarness(t, center, adapter, "")
			key := workdir.BindingKey{OrganizationID: testOrg, SpaceID: "space", AgentID: "crew:crew", ConversationID: "task:task"}
			held, err := h.supervisor.config.Workdir.Prepare(context.Background(), workdir.Request{Shared: true, Key: key, Mode: workdir.ModeNoRepo})
			if err != nil {
				t.Fatal(err)
			}
			defer held.Release()
			center.mu.Lock()
			center.offerAvailable = true
			center.mu.Unlock()
			h.supervisor.HandleMessage(dispatchWake())
			waitFor(t, "context fetched", func() bool { center.mu.Lock(); defer center.mu.Unlock(); return center.contexts > 0 })
			if adapter.startCount() != 0 {
				t.Fatal("started while shared task directory was occupied")
			}
			if release {
				_ = held.Release()
				waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
			} else {
				waitFor(t, "bounded shared directory wait", func() bool { return len(center.snapshot().prepareFailed) == 1 })
				if adapter.startCount() != 0 || center.snapshot().prepareFailed[0] != "WORKTREE_PREP_FAILED" {
					t.Fatal("busy workspace was not rejected safely")
				}
			}
		})
	}
}

func TestSanitizeCustomArgs(t *testing.T) {
	args, dropped := sanitizeCustomArgs([]string{
		"-c", "model_reasoning_effort=high",
		"--config", "tools.web_search=true",
		"-c model_verbosity=low",
		"-c", "sandbox_mode=danger-full-access",
		"--config", "approval_policy=never",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c", "model_context_window=",
		"-c",
	})
	want := []string{"-c", "model_reasoning_effort=high", "-c", "tools.web_search=true", "-c", "model_verbosity=low"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	wantDropped := []string{"sandbox_mode", "approval_policy", "", "model_context_window", ""}
	if !reflect.DeepEqual(dropped, wantDropped) {
		t.Fatalf("dropped = %q, want %q", dropped, wantDropped)
	}
}

func TestBuildRunSettingsGitAuthorPartial(t *testing.T) {
	settings := buildRunSettings(RunContext{GitAuthor: &GitAuthor{Name: "", Email: "fox@example.com"}})
	if settings.environment["GIT_AUTHOR_EMAIL"] != "fox@example.com" || settings.environment["GIT_COMMITTER_EMAIL"] != "fox@example.com" {
		t.Fatalf("environment = %v", settings.environment)
	}
	if _, ok := settings.environment["GIT_AUTHOR_NAME"]; ok {
		t.Fatal("empty author name must not override the computer's Git config")
	}
	settings = buildRunSettings(RunContext{GitAuthor: &GitAuthor{Name: "Fox <x>", Email: "fox@example.com"}})
	if len(settings.warnings) != 1 || len(settings.environment) != 0 {
		t.Fatalf("invalid author settings = %+v", settings)
	}
}

func TestRedactorHandlesJSONEscapes(t *testing.T) {
	r := newRedactor([]string{`pa"ss\word`, "abc", "longer-secret", "longer"})
	if got := r.text(`x pa"ss\word longer-secret abc`); got != "x [REDACTED] [REDACTED] abc" {
		t.Fatalf("text = %q", got)
	}
	payload := r.json([]byte(`{"text":"pa\"ss\\word and longer-secret"}`))
	if string(payload) != `{"text":"[REDACTED] and [REDACTED]"}` {
		t.Fatalf("json = %s", payload)
	}
}

func TestSupervisorAppliesAgentEnvironmentArgsAndAuthor(t *testing.T) {
	const secret = "s3cr3t-value-123"
	center, adapter := newCenter(), &fakeAdapter{output: "the key is " + secret}
	center.extend = func(runContext *RunContext) {
		runContext.Environment = []EnvironmentVariable{
			{Name: "SERVICE_KEY", Value: secret},
			{Name: "HOME", Value: "/tmp/evil-home"},
			{Name: "DYLD_INSERT_LIBRARIES", Value: "/tmp/evil.dylib"},
			{Name: "PATH", Value: "/tmp/evil-bin"},
			{Name: "OPENFOX_DEVICE_TOKEN", Value: "forged-token"},
		}
		runContext.CustomArgs = []string{"-c", "model_reasoning_effort=high", "-c", "sandbox_mode=danger-full-access"}
		runContext.GitAuthor = &GitAuthor{Name: "Fox Agent", Email: "fox@example.com"}
		runContext.WorkdirIsolation = IsolationWorktree
	}
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	request := adapter.requests[0]
	adapter.mu.Unlock()
	wantEnv := map[string]string{
		"SERVICE_KEY": secret, "GIT_AUTHOR_NAME": "Fox Agent", "GIT_AUTHOR_EMAIL": "fox@example.com",
		"GIT_COMMITTER_NAME": "Fox Agent", "GIT_COMMITTER_EMAIL": "fox@example.com",
	}
	if !reflect.DeepEqual(request.Environment, wantEnv) {
		t.Fatalf("environment = %v", request.Environment)
	}
	if !reflect.DeepEqual(request.Args, []string{"-c", "model_reasoning_effort=high"}) {
		t.Fatalf("args = %v", request.Args)
	}
	// Daemon 自身环境不受影响。
	if os.Getenv("SERVICE_KEY") != "" || os.Getenv("GIT_AUTHOR_NAME") == "Fox Agent" {
		t.Fatal("agent environment leaked into the daemon process")
	}
	snapshot := center.snapshot()
	if result := snapshot.results[0]; result.Status != "SUCCEEDED" || strings.Contains(result.Output, secret) || !strings.Contains(result.Output, "[REDACTED]") {
		t.Fatalf("result = %+v", result)
	}
	warnings := map[string]bool{}
	for _, event := range snapshot.accepted {
		for _, value := range []string{secret, "/tmp/evil-home", "/tmp/evil.dylib", "/tmp/evil-bin", "forged-token", "danger-full-access"} {
			if strings.Contains(string(event.Payload), value) {
				t.Fatalf("event %s leaked %q: %s", event.Type, value, event.Payload)
			}
		}
		if event.Type == "warning" {
			for _, name := range []string{"HOME", "DYLD_INSERT_LIBRARIES", "PATH", "OPENFOX_DEVICE_TOKEN", "sandbox_mode"} {
				if strings.Contains(string(event.Payload), `"name":"`+name+`"`) {
					warnings[name] = true
				}
			}
		}
	}
	if len(warnings) != 5 {
		t.Fatalf("warning events cover %v, want 5 names", warnings)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func TestSupervisorDirectIsolationRunsInRepository(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	center, adapter := newCenter(), &fakeAdapter{}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	center.extend = func(runContext *RunContext) { runContext.WorkdirIsolation = IsolationDirect }
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	adapter.mu.Lock()
	path := adapter.requests[0].WorkingDirectory
	adapter.mu.Unlock()
	if path != repo {
		t.Fatalf("DIRECT run used %s, want repository %s", path, repo)
	}
	if list := gitOutput(t, repo, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("DIRECT run created a worktree:\n%s", list)
	}
	// 不建会话绑定，也不登记为可清理的 worktree。
	if _, ok := h.supervisor.config.Workdir.Bindings().Lookup(workdir.BindingKey{OrganizationID: testOrg, SpaceID: "space", AgentID: "agent", ConversationID: "conversation"}); ok {
		t.Fatal("DIRECT run created a conversation binding")
	}
	if workspaces, _ := h.journal.Workspaces(context.Background(), 10); len(workspaces) != 0 {
		t.Fatalf("DIRECT run registered workspaces %v", workspaces)
	}
}

func TestSupervisorDirectIsolationQueuesThenRejectsBusyRepository(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	previousLimit, previousPoll := directWaitLimit, directWaitPoll
	directWaitLimit, directWaitPoll = 400*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { directWaitLimit, directWaitPoll = previousLimit, previousPoll })

	center, adapter := newCenter(), &fakeAdapter{}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	center.extend = func(runContext *RunContext) { runContext.WorkdirIsolation = IsolationDirect }
	center.offerAvailable = false
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	// 另一个 Run 占用仓库目录。
	held, err := h.supervisor.config.Workdir.PrepareDirectRun(context.Background(), workdir.BindingKey{OrganizationID: testOrg, SpaceID: "s", ConversationID: "other"}, repo)
	if err != nil {
		t.Fatal(err)
	}
	center.mu.Lock()
	center.offerAvailable = true
	center.mu.Unlock()
	h.supervisor.HandleMessage(dispatchWake())
	waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
	if got := center.snapshot().prepareFailed[0]; got != "WORKTREE_PREP_FAILED" || adapter.startCount() != 0 {
		t.Fatalf("prepare-failed = %q, starts = %d", got, adapter.startCount())
	}
	_ = held.Release()
}

func TestSupervisorDirectIsolationWaitsForRelease(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	previousPoll := directWaitPoll
	directWaitPoll = 20 * time.Millisecond
	t.Cleanup(func() { directWaitPoll = previousPoll })

	center, adapter := newCenter(), &fakeAdapter{}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	center.extend = func(runContext *RunContext) { runContext.WorkdirIsolation = IsolationDirect }
	center.offerAvailable = false
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	held, err := h.supervisor.config.Workdir.PrepareDirectRun(context.Background(), workdir.BindingKey{OrganizationID: testOrg, SpaceID: "s", ConversationID: "other"}, repo)
	if err != nil {
		t.Fatal(err)
	}
	center.mu.Lock()
	center.offerAvailable = true
	center.mu.Unlock()
	h.supervisor.HandleMessage(dispatchWake())
	time.Sleep(200 * time.Millisecond)
	if adapter.startCount() != 0 {
		t.Fatal("DIRECT run started while the repository was busy")
	}
	_ = held.Release()
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	if len(center.snapshot().prepareFailed) != 0 {
		t.Fatalf("prepare-failed = %v", center.snapshot().prepareFailed)
	}
}

func TestSupervisorCleanupStorage(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	center, adapter := newCenter(), &fakeAdapter{}
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	h.stop()
	ctx := context.Background()
	workspaces, err := h.journal.Workspaces(ctx, 10)
	if err != nil || len(workspaces) != 1 {
		t.Fatalf("workspaces = %v, %v", workspaces, err)
	}
	worktree := workspaces[0].Path
	branch := gitOutput(t, worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if err := os.WriteFile(filepath.Join(worktree, "big.bin"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	// 最近刚用过：不清理。
	result, err := h.supervisor.CleanupStorage(ctx)
	if err != nil || result.Removed.Worktrees != 0 {
		t.Fatalf("fresh cleanup = %+v, %v", result, err)
	}
	ageWorkspaces(t, h.journal)
	// 有未提交改动（未跟踪文件）：保留。
	result, err = h.supervisor.CleanupStorage(ctx)
	if err != nil || result.Removed.Worktrees != 0 || result.SkippedDirty != 1 {
		t.Fatalf("dirty cleanup = %+v, %v", result, err)
	}
	// 被 .gitignore 忽略的构建产物不算改动。
	if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("big.bin\n.gitignore\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 活动 Run（目录被锁定）：跳过并计数。
	held, err := h.supervisor.config.Workdir.Prepare(ctx, workdir.Request{
		Key:  workdir.BindingKey{OrganizationID: testOrg, SpaceID: "space", AgentID: "agent", ConversationID: "conversation"},
		Mode: workdir.ModeSpecifiedGit, SourcePath: repo,
	})
	if err != nil {
		t.Fatal(err)
	}
	if held.Path != worktree {
		t.Fatalf("reopened %s, want %s", held.Path, worktree)
	}
	result, err = h.supervisor.CleanupStorage(ctx)
	if err != nil || result.SkippedActive != 1 || result.Removed.Worktrees != 0 {
		t.Fatalf("active cleanup = %+v, %v", result, err)
	}
	_ = held.Release()
	// 已结束且干净：删除 worktree，分支保留。
	homes := 0
	h.supervisor.config.CleanupHomes = func(time.Time) (int, int64) { homes++; return 2, 10 }
	result, err = h.supervisor.CleanupStorage(ctx)
	if err != nil || result.Removed.Worktrees != 1 || result.Removed.CodexHomes != 2 || result.FreedBytes < 4096 || homes != 1 {
		t.Fatalf("cleanup = %+v, %v", result, err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists: %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); got == "" {
		t.Fatal("branch was deleted with the worktree")
	}
	if workspaces, _ := h.journal.Workspaces(ctx, 10); len(workspaces) != 0 {
		t.Fatalf("workspace record kept: %v", workspaces)
	}
	// 下一个 Run 重新准备而不是复用已删除的路径。
	if _, ok := h.supervisor.config.Workdir.Bindings().Lookup(workdir.BindingKey{OrganizationID: testOrg, SpaceID: "space", AgentID: "agent", ConversationID: "conversation"}); ok {
		t.Fatal("binding to the removed worktree was kept")
	}
}

func TestSupervisorCleanupRefusesPathsOutsideRootAndSymlinks(t *testing.T) {
	requireGit(t)
	center := newCenter()
	center.offerAvailable = false
	h := newHarness(t, center, &fakeAdapter{}, "")
	h.stop()
	ctx := context.Background()
	outside := gitRepository(t)
	root := h.supervisor.config.Workdir.ManagedRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "git-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	for index, path := range []string{outside, link} {
		if err := h.journal.RecordWorkspace(ctx, path, outside, "run-"+string(rune('a'+index)), 1); err != nil {
			t.Fatal(err)
		}
	}
	ageWorkspaces(t, h.journal)
	result, err := h.supervisor.CleanupStorage(ctx)
	if err != nil || result.Removed.Worktrees != 0 {
		t.Fatalf("cleanup = %+v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(outside, ".git")); err != nil {
		t.Fatalf("outside repository was touched: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink was removed: %v", err)
	}
}

func TestJournalPruneCountsRows(t *testing.T) {
	journal, err := OpenJournal(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	ctx := context.Background()
	journal.now = func() time.Time { return time.Now().Add(-8 * 24 * time.Hour) }
	if _, err := journal.Begin(ctx, testOrg, "old-run", 1, "offer"); err != nil {
		t.Fatal(err)
	}
	if err := journal.Mark(ctx, "old-run", 1, JournalFinished, ""); err != nil {
		t.Fatal(err)
	}
	journal.now = time.Now
	if _, err := journal.Begin(ctx, testOrg, "new-run", 1, "offer"); err != nil {
		t.Fatal(err)
	}
	rows, err := journal.Prune(ctx)
	if err != nil || rows != 1 {
		t.Fatalf("prune = %d, %v", rows, err)
	}
	if state, _ := journal.State(ctx, "new-run", 1); state != JournalClaiming {
		t.Fatalf("active row pruned: %q", state)
	}
}

func ageWorkspaces(t *testing.T, journal *Journal) {
	t.Helper()
	if _, err := journal.db.Exec(`UPDATE workspaces SET last_used_at = ?`, time.Now().Add(-48*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
}
