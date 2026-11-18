package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"openfox/internal/daemon/storage"
)

func TestScopedCleanupPreviewsAndRemovesOnlyConfirmedWorkspace(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	center := newCenter()
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	h := newHarnessWithRoots(t, center, &fakeAdapter{}, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	h.stop()
	ctx := context.Background()
	ws, err := h.journal.Workspaces(ctx, 10)
	if err != nil || len(ws) != 1 {
		t.Fatalf("workspaces %v %v", ws, err)
	}
	path := ws[0].Path
	days := 0
	f := storage.CleanupFilter{Action: "preview", Days: &days, Categories: []string{"task", "conversation"}}
	preview := func(org string) CleanupPreview {
		t.Helper()
		v, e := h.supervisor.FilteredCleanup(ctx, org, f)
		if e != nil {
			t.Fatal(e)
		}
		return v.(CleanupPreview)
	}
	if p := preview("other"); p.Worktrees != 0 {
		t.Fatal("cross-organization match")
	}
	if p := preview(testOrg); p.Worktrees != 1 {
		t.Fatalf("preview %+v", p)
	}
	dirty := filepath.Join(path, "local-draft.txt")
	if err = os.WriteFile(dirty, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if p := preview(testOrg); p.Worktrees != 0 {
		t.Fatal("dirty workspace eligible")
	}
	if err = os.Remove(dirty); err != nil {
		t.Fatal(err)
	}
	f.PreviewID = preview(testOrg).PreviewID
	f.Action = "clean"
	ageWorkspaces(t, h.journal)
	if _, err = h.supervisor.FilteredCleanup(ctx, testOrg, f); !errors.Is(err, ErrCleanupChanged) {
		t.Fatalf("changed preview: %v", err)
	}
	f.Action = "preview"
	f.PreviewID = ""
	f.PreviewID = preview(testOrg).PreviewID
	f.Action = "clean"
	r, err := h.supervisor.FilteredCleanup(ctx, testOrg, f)
	if err != nil || r.(CleanupResult).Removed.Worktrees != 1 {
		t.Fatalf("cleanup %v %v", r, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disposable worktree still present")
	}
}

// 按会话限定：跨会话/跨组织不匹配，运行中的 Run 不进入预览，清理只删本会话的工作区。
func TestConversationCleanupIsolationAndActiveProtection(t *testing.T) {
	requireGit(t)
	repo := gitRepository(t)
	center := newCenter()
	center.directoryMode, center.directoryRoots = "EXPLICIT", []string{"team/openfox"}
	h := newHarnessWithRoots(t, center, &fakeAdapter{}, "", map[string]string{"team/openfox": repo})
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	h.stop()
	ctx := context.Background()
	ws, err := h.journal.Workspaces(ctx, 10)
	if err != nil || len(ws) != 1 {
		t.Fatalf("workspaces %v %v", ws, err)
	}
	path := ws[0].Path
	var stored string
	if err = h.journal.db.QueryRow(`SELECT conversation_id FROM workspaces WHERE path=?`, path).Scan(&stored); err != nil || stored != "conversation" {
		t.Fatalf("workspace conversation %q %v", stored, err)
	}
	days := 0
	filter := func(conversation string) storage.CleanupFilter {
		return storage.CleanupFilter{Action: "preview", Days: &days, Categories: []string{"task", "conversation"}, ConversationID: conversation}
	}
	preview := func(org, conversation string) CleanupPreview {
		t.Helper()
		v, e := h.supervisor.FilteredCleanup(ctx, org, filter(conversation))
		if e != nil {
			t.Fatal(e)
		}
		return v.(CleanupPreview)
	}
	if preview(testOrg, "other-conversation").Worktrees != 0 {
		t.Fatal("cross-conversation match")
	}
	if preview("other", "conversation").Worktrees != 0 {
		t.Fatal("cross-organization match")
	}
	p := preview(testOrg, "conversation")
	if p.Worktrees != 1 {
		t.Fatalf("preview %+v", p)
	}
	for _, item := range p.Items {
		if item.Name != "" && filepath.IsAbs(item.Name) {
			t.Fatalf("path leaked in item %+v", item)
		}
	}
	// 本地日志仍显示运行中：不进入预览。
	if _, err = h.journal.db.Exec(`UPDATE run_journal SET state='RUNNING'`); err != nil {
		t.Fatal(err)
	}
	if preview(testOrg, "conversation").Worktrees != 0 {
		t.Fatal("running run eligible")
	}
	if _, err = h.journal.db.Exec(`UPDATE run_journal SET state='FINISHED'`); err != nil {
		t.Fatal(err)
	}
	// 内存中的活动 Run 同样保护。
	h.supervisor.mu.Lock()
	h.supervisor.active[ws[0].LastRunID] = nil
	h.supervisor.mu.Unlock()
	if preview(testOrg, "conversation").Worktrees != 0 {
		t.Fatal("active run eligible")
	}
	h.supervisor.mu.Lock()
	delete(h.supervisor.active, ws[0].LastRunID)
	h.supervisor.mu.Unlock()

	clean := filter("conversation")
	clean.Action, clean.PreviewID = "clean", preview(testOrg, "conversation").PreviewID
	// 预览凭据与会话绑定：换会话不可复用。
	other := clean
	other.ConversationID = "other-conversation"
	if _, err = h.supervisor.FilteredCleanup(ctx, testOrg, other); !errors.Is(err, ErrCleanupChanged) {
		t.Fatalf("preview reused across conversations: %v", err)
	}
	r, err := h.supervisor.FilteredCleanup(ctx, testOrg, clean)
	if err != nil || r.(CleanupResult).Removed.Worktrees != 1 {
		t.Fatalf("cleanup %v %v", r, err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("worktree still present")
	}
}

// 回填：路径唯一匹配才写入；有歧义、组织不符、无事实的保持 NULL；重复升级幂等且不覆盖已有值。
func TestWorkspaceConversationBackfillIsIdempotent(t *testing.T) {
	ctx := context.Background()
	journal, err := OpenJournal(ctx, filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	// 还原为升级前的表结构。
	if _, err = journal.db.Exec(`ALTER TABLE workspaces DROP COLUMN conversation_id`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/w/unique", "/w/ambiguous", "/w/otherorg", "/w/nofact"} {
		if _, err = journal.db.Exec(`INSERT INTO workspaces(path,repository_root,last_run_id,last_generation,last_used_at,organization_id) VALUES(?,?,?,?,?,?)`, p, "/r", "run", 1, 1, "org"); err != nil {
			t.Fatal(err)
		}
	}
	fact := func(path, org, conversation string) {
		t.Helper()
		if _, err = journal.db.Exec(`INSERT INTO conversation_directory_facts(device_id,organization_id,space_id,conversation_id,agent_id,run_id,generation,path) VALUES('d',?,'s',?,?,'run',1,?)`, org, conversation, path, path); err != nil {
			t.Fatal(err)
		}
	}
	fact("/w/unique", "org", "c1")
	fact("/w/ambiguous", "org", "c1")
	fact("/w/ambiguous", "org", "c2")
	fact("/w/otherorg", "elsewhere", "c3")
	read := func() map[string]string {
		t.Helper()
		rows, e := journal.db.Query(`SELECT path, COALESCE(conversation_id,'<null>') FROM workspaces`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var path, c string
			if e = rows.Scan(&path, &c); e != nil {
				t.Fatal(e)
			}
			got[path] = c
		}
		return got
	}
	if err = journal.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got["/w/unique"] != "c1" || got["/w/ambiguous"] != "<null>" || got["/w/otherorg"] != "<null>" || got["/w/nofact"] != "<null>" {
		t.Fatalf("backfill %v", got)
	}
	// 重复升级：已有值不被覆盖，仍为 NULL 的行保持 NULL。
	if _, err = journal.db.Exec(`UPDATE workspaces SET conversation_id='c9' WHERE path='/w/unique'`); err != nil {
		t.Fatal(err)
	}
	if err = journal.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got = read()
	if got["/w/unique"] != "c9" || got["/w/ambiguous"] != "<null>" || got["/w/otherorg"] != "<null>" || got["/w/nofact"] != "<null>" {
		t.Fatalf("second upgrade %v", got)
	}
}
