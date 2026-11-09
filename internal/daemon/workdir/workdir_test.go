package workdir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLockManagerRejectsAliasesAndParentChildOverlap(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "workspace")
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	locks := NewLockManager()
	first, err := locks.Acquire(context.Background(), parent, LockOwner{OrganizationID: "org-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := locks.Acquire(context.Background(), alias, LockOwner{OrganizationID: "org-b"}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("alias acquire error = %v, want ErrPathConflict", err)
	}
	if _, err := locks.Acquire(context.Background(), child, LockOwner{OrganizationID: "org-b"}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("child acquire error = %v, want ErrPathConflict", err)
	}
}

func TestLockManagerConcurrentSingleWinner(t *testing.T) {
	path := t.TempDir()
	locks := NewLockManager()
	const attempts = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	leases := make([]*Lease, 0, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, err := locks.Acquire(context.Background(), path, LockOwner{RunID: fmt.Sprintf("run-%d", i)})
			if err != nil {
				if !errors.Is(err, ErrPathConflict) {
					t.Errorf("unexpected acquire error: %v", err)
				}
				return
			}
			mu.Lock()
			winners++
			leases = append(leases, lease)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("winners = %d, want 1", winners)
	}
	for _, lease := range leases {
		if err := lease.Release(); err != nil {
			t.Errorf("release: %v", err)
		}
	}
}

func TestManagerBindsFirstPathAndDoesNotMigrateOnDefaultChange(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(GuardPolicy{AllowedRoots: []string{root}, AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	key := BindingKey{OrganizationID: "org-a", SpaceID: "space-a", AgentID: "agent-a", ConversationID: "conversation-a"}
	first, err := manager.Prepare(context.Background(), Request{Key: key, Mode: ModeDirect, RequestedPath: firstPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Prepare(context.Background(), Request{Key: key, Mode: ModeDirect, RequestedPath: secondPath})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	if second.Path != first.Path {
		t.Fatalf("rebound path = %q, want original %q", second.Path, first.Path)
	}
	binding, ok := manager.Bindings().Lookup(key)
	if !ok || binding.ActualPath != first.Path {
		t.Fatalf("binding = %#v, ok = %v", binding, ok)
	}
}

func TestManagerKeepsBoundPathReservedAcrossConversations(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "shared")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(GuardPolicy{AllowedRoots: []string{root}, AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	firstKey := BindingKey{OrganizationID: "org-a", SpaceID: "space-a", ConversationID: "first"}
	first, err := manager.Prepare(context.Background(), Request{Key: firstKey, Mode: ModeDirect, RequestedPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	secondKey := BindingKey{OrganizationID: "org-b", SpaceID: "space-b", ConversationID: "second"}
	if _, err := manager.Prepare(context.Background(), Request{Key: secondKey, Mode: ModeDirect, RequestedPath: path}); !errors.Is(err, ErrPathConflict) {
		t.Fatalf("second binding error = %v, want ErrPathConflict", err)
	}
}

func TestManagerRejectsProtectedAndOutsidePaths(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "protected")
	outside := t.TempDir()
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(GuardPolicy{
		AllowedRoots:   []string{root},
		ProtectedRoots: []string{protected},
		AllowNonGit:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	baseKey := BindingKey{OrganizationID: "org-a", SpaceID: "space-a", ConversationID: "protected"}
	if _, err := manager.Prepare(context.Background(), Request{Key: baseKey, Mode: ModeDirect, RequestedPath: protected}); !errors.Is(err, ErrPathProtected) {
		t.Fatalf("protected path error = %v", err)
	}
	baseKey.ConversationID = "outside"
	if _, err := manager.Prepare(context.Background(), Request{Key: baseKey, Mode: ModeDirect, RequestedPath: outside}); !errors.Is(err, ErrPathOutsideAllowed) {
		t.Fatalf("outside path error = %v", err)
	}
}

func TestManagerPreparesNoRepoWorkspace(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed")
	manager, err := NewManager(GuardPolicy{ManagedRoot: managed, AllowedRoots: []string{root}, AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Prepare(context.Background(), Request{
		Key:  BindingKey{OrganizationID: "org-a", SpaceID: "space-a", ConversationID: "office"},
		Mode: ModeNoRepo,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Release()
	if !workspace.Owned || workspace.IsGit {
		t.Fatalf("workspace ownership/git = %v/%v", workspace.Owned, workspace.IsGit)
	}
	resolvedManaged, err := canonicalPath(managed)
	if err != nil {
		t.Fatal(err)
	}
	if !pathContains(resolvedManaged, workspace.Path) {
		t.Fatalf("workspace %q is outside managed root %q", workspace.Path, resolvedManaged)
	}
	if info, err := os.Stat(workspace.Path); err != nil || !info.IsDir() {
		t.Fatalf("workspace stat = %v, info = %#v", err, info)
	}
}

func TestGitWorktreeAndExplicitResultCommit(t *testing.T) {
	root := t.TempDir()
	repo := initRepository(t, root)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(root, "managed")
	manager, err := NewManager(GuardPolicy{ManagedRoot: managed, AllowedRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Prepare(context.Background(), Request{
		Key:        BindingKey{OrganizationID: "org-a", SpaceID: "space-a", ConversationID: "git"},
		Mode:       ModeTempGit,
		SourcePath: repo,
		Branch:     "openfox/test-result",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Release()
	if workspace.Path == repo || !workspace.Owned || !workspace.IsGit {
		t.Fatalf("unexpected workspace: %#v", workspace)
	}
	if len(workspace.Diagnostics) != 0 {
		t.Fatalf("empty base unexpectedly produced diagnostics: %#v", workspace.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(workspace.Path, "dirty.txt")); err == nil {
		t.Fatal("uncommitted source file unexpectedly copied into worktree")
	}
	if err := os.WriteFile(filepath.Join(workspace.Path, "result.txt"), []byte("result\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err := manager.CommitResult(context.Background(), workspace, CommitRequest{Message: "Add result"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commit) != 40 {
		t.Fatalf("commit hash = %q", commit)
	}
	if _, err := os.Stat(filepath.Join(repo, "dirty.txt")); err != nil {
		t.Fatalf("source dirty file was changed: %v", err)
	}
}

func TestExplicitBaseFallsBackToLocalRefWithDiagnostic(t *testing.T) {
	root := t.TempDir()
	repo := initRepository(t, root)
	gitRun(t, repo, "checkout", "-b", "feature")
	manager, err := NewManager(GuardPolicy{ManagedRoot: filepath.Join(root, "managed"), AllowedRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Prepare(context.Background(), Request{
		Key:        BindingKey{OrganizationID: "org-a", SpaceID: "space-a", ConversationID: "base"},
		Mode:       ModeSpecifiedGit,
		SourcePath: repo,
		BaseRef:    "feature",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Release()
	foundFetchDiagnostic := false
	for _, diagnostic := range workspace.Diagnostics {
		if diagnostic.Code == "GIT_FETCH_FAILED" {
			foundFetchDiagnostic = true
		}
	}
	if !foundFetchDiagnostic {
		t.Fatalf("diagnostics = %#v, want fetch fallback diagnostic", workspace.Diagnostics)
	}
}

func TestDirectWorkspaceDoesNotAllowResultCommit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "direct")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(GuardPolicy{AllowedRoots: []string{root}, AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := manager.Prepare(context.Background(), Request{
		Key:           BindingKey{OrganizationID: "org-a", SpaceID: "space-a", ConversationID: "direct"},
		Mode:          ModeDirect,
		RequestedPath: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Release()
	if _, err := manager.CommitResult(context.Background(), workspace, CommitRequest{Message: "must fail"}); !errors.Is(err, ErrCommitNotAllowed) {
		t.Fatalf("commit error = %v, want ErrCommitNotAllowed", err)
	}
}

func initRepository(t *testing.T, root string) string {
	t.Helper()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "-q")
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "user.name", "OpenFox Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "-qm", "initial")
	return repo
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestManagerRebindsWhenModeChanges(t *testing.T) {
	root := t.TempDir()
	repo := initRepository(t, root)
	managed := filepath.Join(root, "managed")
	manager, err := NewManager(GuardPolicy{ManagedRoot: managed, AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	key := BindingKey{OrganizationID: "org-a", SpaceID: "space-a", AgentID: "agent-a", ConversationID: "conversation-a"}
	office, err := manager.Prepare(context.Background(), Request{Key: key, Mode: ModeNoRepo})
	if err != nil {
		t.Fatal(err)
	}
	if err := office.Release(); err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Prepare(context.Background(), Request{Key: key, Mode: ModeSpecifiedGit, SourcePath: repo})
	if err != nil {
		t.Fatal(err)
	}
	if worktree.Path == office.Path || !worktree.IsGit {
		t.Fatalf("mode change reused %q (git=%v)", worktree.Path, worktree.IsGit)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	// 旧目录不删除；同一模式再次准备复用同一 worktree。
	if _, err := os.Stat(office.Path); err != nil {
		t.Fatalf("previous workspace removed: %v", err)
	}
	again, err := manager.Prepare(context.Background(), Request{Key: key, Mode: ModeSpecifiedGit, SourcePath: repo})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Release()
	if again.Path != worktree.Path {
		t.Fatalf("same-mode prepare = %q, want %q", again.Path, worktree.Path)
	}
}
