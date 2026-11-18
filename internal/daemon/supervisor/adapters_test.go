package supervisor

import (
	"context"
	"testing"

	"openfox/internal/runtime"
)

type namedAdapter struct{ name string }

func (a namedAdapter) Name() string { return a.name }
func (a namedAdapter) Detect(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}
func (a namedAdapter) Start(context.Context, runtime.RunRequest) (runtime.Execution, error) {
	return nil, runtime.ErrUnavailable
}

// Run 按 RunContext.Runtime 选择适配器：主适配器优先，其次额外适配器，都没有时为 nil。
func TestAdapterForSelectsByRuntimeName(t *testing.T) {
	s := &Supervisor{config: Config{Adapter: namedAdapter{"codex"}, Extra: []runtime.Adapter{namedAdapter{"claude-code"}, nil, namedAdapter{"pi"}}}}
	for _, name := range []string{"codex", "claude-code", "pi"} {
		if got := s.adapterFor(name); got == nil || got.Name() != name {
			t.Fatalf("adapterFor(%q) = %v", name, got)
		}
	}
	if s.adapterFor("opencode") != nil || s.adapterFor("") != nil {
		t.Fatal("unknown runtime must not resolve")
	}
}

func TestJournalWorkspaceKinds(t *testing.T) {
	ctx := context.Background()
	journal, err := OpenJournal(ctx, t.TempDir()+"/runs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	for _, path := range []string{"/w/a", "/w/b"} {
		if err := journal.RecordWorkspace(ctx, path, "/repo", "run-1", 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.SetWorkspaceKind(ctx, "/w/a", "task"); err != nil {
		t.Fatal(err)
	}
	kinds, err := journal.WorkspaceKinds(ctx, 10)
	if err != nil || len(kinds) != 1 || kinds["/w/a"] != "task" {
		t.Fatalf("kinds = %v err=%v", kinds, err)
	}
	// 重新打开（模拟旧库已有 kind 列）不报错、数据保留。
	path := journal.Path()
	_ = journal.Close()
	again, err := OpenJournal(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if kinds, _ := again.WorkspaceKinds(ctx, 10); kinds["/w/a"] != "task" {
		t.Fatalf("kinds after reopen = %v", kinds)
	}
}
