package supervisor

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
)

func TestSelectedWorkspacesPersistAndEnforceScope(t *testing.T) {
	requireGit(t)
	ctx := t.Context()
	repo := gitRepository(t)
	initial := gitOutput(t, repo, "rev-parse", "HEAD")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "state", "journal.db")
	manager := func() (*runState, *Journal) {
		t.Helper()
		m, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "managed"), AllowNonGit: true})
		if err != nil {
			t.Fatal(err)
		}
		j, err := OpenJournal(ctx, statePath)
		if err != nil {
			t.Fatal(err)
		}
		return &runState{supervisor: &Supervisor{config: Config{DeviceID: "device", Journal: j, Workdir: m, DirectoryRoots: map[string]string{"repo": repo}}}, logger: slog.Default()}, j
	}
	r, j := manager()
	source := RunContext{OrganizationID: "org", SpaceID: "space", TaskID: "task", AgentID: "a1", ConversationID: "source", DirectoryMode: "EXPLICIT", DirectoryRoots: []string{"repo"}}
	get := func(rc RunContext) *workdir.PreparedWorkspace {
		t.Helper()
		w, e := r.prepareConversationWorkspace(ctx, rc)
		if e != nil {
			t.Fatal(e)
		}
		return w
	}
	parent := get(source)
	// The original conversation has a local commit plus an uncommitted file, neither in origin's HEAD.
	gitOutput(t, parent.Path, "-c", "user.name=test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "source-only")
	head := gitOutput(t, parent.Path, "rev-parse", "HEAD")
	if head == initial {
		t.Fatal("source did not diverge")
	}
	if err := os.WriteFile(filepath.Join(parent.Path, "marker.txt"), []byte("draft"), 0600); err != nil {
		t.Fatal(err)
	}
	parent.Release()
	reuse := source
	reuse.ConversationID = "reuse"
	reuse.AgentID = "a2"
	reuse.WorkspaceSelection = &WorkspaceSelection{ComputerID: "device", Mode: "REUSE", SourceConversationID: "source", SourceDirectory: "repo", SourceAgentID: "a1"}
	reused := get(reuse)
	if reused.Path != parent.Path {
		t.Fatalf("wrong source: %s", reused.Path)
	}
	proc := exec.CommandContext(ctx, "git", "status", "--porcelain")
	proc.Dir = reused.Path
	output, err := proc.Output()
	if err != nil || !strings.Contains(string(output), "marker.txt") {
		t.Fatalf("git in reused cwd: %s %v", output, err)
	}
	reused.Release()
	from := reuse
	from.ConversationID = "baseline"
	from.WorkspaceSelection = &WorkspaceSelection{ComputerID: "device", Mode: "BASELINE", SourceConversationID: "source", SourceDirectory: "repo", SourceAgentID: "a1"}
	fork := get(from)
	if fork.Path == parent.Path || gitOutput(t, fork.Path, "rev-parse", "HEAD") != head {
		t.Fatal("incorrect source HEAD baseline")
	}
	if _, err = os.Stat(filepath.Join(fork.Path, "marker.txt")); !os.IsNotExist(err) {
		t.Fatal("baseline included uncommitted file")
	}
	fork.Release()
	gitOutput(t, repo, "branch", "old", initial)
	custom := from
	custom.ConversationID = "branch"
	custom.WorkspaceSelection = &WorkspaceSelection{ComputerID: "device", Mode: "BASELINE", SourceConversationID: "source", SourceDirectory: "repo", SourceAgentID: "a1", BaseRef: "old"}
	exact := get(custom)
	if gitOutput(t, exact.Path, "rev-parse", "HEAD") != initial {
		t.Fatal("custom branch ignored")
	}
	exact.Release()
	same := from
	same.ConversationID = "from-full-sha"
	same.WorkspaceSelection = &WorkspaceSelection{ComputerID: "device", Mode: "WORKTREE", Directory: "repo", BaseRef: initial}
	pinned := get(same)
	if gitOutput(t, pinned.Path, "rev-parse", "HEAD") != initial {
		t.Fatal("SHA ignored")
	}
	pinned.Release()
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	r, j = manager()
	defer j.Close()
	after := get(source)
	if after.Path != parent.Path {
		t.Fatal("lost default source binding across daemon restart")
	}
	after.Release()
	after = get(from)
	if after.Path != fork.Path {
		t.Fatal("lost selected binding across restart")
	}
	after.Release()
	for _, kind := range []string{"removed permission", "wrong org", "wrong task", "wrong device", "wrong space", "wrong logical source", "wrong agent", "unknown source", "unknown branch"} {
		t.Run(kind, func(t *testing.T) {
			denied := reuse
			denied.ConversationID = "denied"
			input := *reuse.WorkspaceSelection
			switch kind {
			case "removed permission":
				denied.DirectoryRoots = nil
			case "wrong org":
				denied.OrganizationID = "other"
			case "wrong task":
				denied.TaskID = "other"
			case "wrong device":
				input.ComputerID = "other"
			case "wrong space":
				denied.SpaceID = "other"
			case "wrong logical source":
				input.SourceDirectory = "other"
			case "wrong agent":
				input.SourceAgentID = "other"
			case "unknown source":
				input.SourceConversationID = "missing"
			case "unknown branch":
				input.Mode = "BASELINE"
				input.BaseRef = "missing"
			}
			denied.WorkspaceSelection = &input
			if w, err := r.prepareConversationWorkspace(ctx, denied); err == nil {
				w.Release()
				t.Fatal("started from rejected selection")
			}
		})
	}
}

func TestDirectorySelectionPassesActualCWDToRuntime(t *testing.T) {
	repo := gitRepository(t)
	if err := os.Mkdir(filepath.Join(repo, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	adapter := &fakeAdapter{onStart: func(req runtime.RunRequest) {
		cmd := exec.Command("pwd", "-P")
		cmd.Dir = req.WorkingDirectory
		output, err := cmd.Output()
		if err != nil {
			got <- "failed"
			return
		}
		got <- strings.TrimSpace(string(output))
	}}
	center := newCenter()
	center.directoryMode = "EXPLICIT"
	center.directoryRoots = []string{"repo"}
	center.extend = func(rc *RunContext) {
		rc.WorkspaceSelection = &WorkspaceSelection{Mode: "DIRECT", ComputerID: "test-device", Directory: "repo/sub"}
	}
	h := newHarnessWithRoots(t, center, adapter, "", map[string]string{"repo": repo}, "test-device")
	waitFor(t, "selected result", func() bool { return len(h.center.snapshot().results) == 1 })
	if actual := <-got; actual != filepath.Join(repo, "sub") {
		t.Fatalf("actual process cwd %q", actual)
	}
}

func TestContextAdvertisesWorkspaceProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-OpenFox-Conversation-Workdir") != "1" {
			t.Error("context capability missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	c := Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "test", nil }}
	if _, err := c.Context(t.Context(), "org", "run", 1); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceRefusesSubtreeGitParentAndUnregisteredRoots(t *testing.T) {
	repo := gitRepository(t)
	sub := filepath.Join(repo, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := workdir.ResolveDirectory("team", map[string]string{"team": sub}, []string{"team"})
	if err != nil || d.IsGitRoot(t.Context()) {
		t.Fatalf("subtree treated as parent repo: %v", err)
	}
	if _, err := workdir.ResolveDirectory("team/private", map[string]string{"team": repo, "team/private": sub}, []string{"team"}); !errors.Is(err, workdir.ErrPathOutsideAllowed) {
		t.Fatalf("nested alias escaped: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(sub, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := workdir.ResolveDirectory("team/escape", map[string]string{"team": sub}, []string{"team"}); !errors.Is(err, workdir.ErrPathOutsideAllowed) {
		t.Fatalf("symlink escaped: %v", err)
	}
}
