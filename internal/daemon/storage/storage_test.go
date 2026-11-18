package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureCountsCategoriesWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")
	homes := filepath.Join(root, "codex-homes")
	write(t, filepath.Join(workspaces, "git-a-1", "file"), 100)
	write(t, filepath.Join(workspaces, "git-b-2", "nested", "file"), 50)
	write(t, filepath.Join(workspaces, "office-1", "notes"), 10)
	write(t, filepath.Join(homes, "codex-home-1", "auth.json"), 7)
	write(t, filepath.Join(homes, "other", "ignored"), 1000)
	write(t, filepath.Join(root, "runs.db"), 20)
	write(t, filepath.Join(root, "runs.db-wal"), 5)
	outside := filepath.Join(t.TempDir(), "big")
	write(t, outside, 10_000)
	if err := os.Symlink(outside, filepath.Join(workspaces, "git-a-1", "link")); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	stats := Measure(context.Background(), Paths{
		WorkspaceRoot: workspaces, WorktreePrefix: "git-", CodexHomeRoot: homes, CodexHomePrefix: "codex-home-",
		JournalPath: filepath.Join(root, "runs.db"),
	}, now)
	if stats.WorktreeCount != 2 || stats.WorktreeBytes != 150 || stats.CodexHomeBytes != 7 || stats.JournalBytes != 25 ||
		stats.TotalBytes != 150+10+7+25 || stats.Partial || !stats.MeasuredAt.Equal(now) {
		t.Fatalf("stats = %+v", stats)
	}
	encoded, _ := json.Marshal(stats)
	if strings.Contains(string(encoded), root) || strings.Contains(string(encoded), "partial") {
		t.Fatalf("stats JSON leaks paths or default partial: %s", encoded)
	}
}

func TestMeasureMarksPartialAtEntryLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(root, "git-x", "f"+string(rune('a'+i))), 1)
	}
	walker := &Walker{MaxEntries: 5}
	walker.Size(context.Background(), root)
	if !walker.Partial() {
		t.Fatal("walker did not stop at the entry limit")
	}
}

func TestWithinResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "git-a")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	link := filepath.Join(root, "git-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if !Within(root, inside) || Within(root, link) || Within(root, root) || Within(root, filepath.Join(root, "..")) || Within(root, filepath.Join(root, "missing")) {
		t.Fatal("Within returned an unexpected result")
	}
}

func TestMeasureCategoriesByWorktreeKind(t *testing.T) {
	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")
	homes := filepath.Join(root, "codex-homes")
	attachments := filepath.Join(root, "attachments")
	write(t, filepath.Join(workspaces, "git-task-1", "f"), 100)
	write(t, filepath.Join(workspaces, "git-task-2", "f"), 50)
	write(t, filepath.Join(workspaces, "git-chat-1", "f"), 30)
	write(t, filepath.Join(workspaces, "git-auto", "f"), 17)
	write(t, filepath.Join(workspaces, "git-unknown-1", "f"), 20)
	write(t, filepath.Join(workspaces, "office-1", "notes"), 10)
	write(t, filepath.Join(homes, "codex-home-1", "auth.json"), 7)
	write(t, filepath.Join(attachments, "a.bin"), 5)
	write(t, filepath.Join(root, "runs.db"), 3)
	kinds := map[string]string{
		filepath.Join(workspaces, "git-task-1"): "task", filepath.Join(workspaces, "git-task-2"): "task",
		filepath.Join(workspaces, "git-chat-1"): "conversation",
		filepath.Join(workspaces, "git-auto"):   "automation",
	}
	paths := Paths{
		WorkspaceRoot: workspaces, WorktreePrefix: "git-", CodexHomeRoot: homes, CodexHomePrefix: "codex-home-",
		JournalPath: filepath.Join(root, "runs.db"), CacheDirs: []string{attachments, filepath.Join(root, "missing")},
		KindOf: func(path string) string { return kinds[path] },
	}
	stats := Measure(context.Background(), paths, time.Unix(1_700_000_000, 0))
	want := []Category{
		{Key: CategoryCache, Bytes: 7 + 5},
		{Key: CategoryTask, Bytes: 150, Count: 2},
		{Key: CategoryConversation, Bytes: 30, Count: 1},
		{Key: CategoryAutomation, Bytes: 17, Count: 1},
		{Key: CategoryOther, Bytes: 20 + 10 + 3},
	}
	if len(stats.Categories) != len(want) {
		t.Fatalf("categories = %+v", stats.Categories)
	}
	var sum int64
	for index, category := range stats.Categories {
		if category != want[index] {
			t.Fatalf("category %d = %+v, want %+v", index, category, want[index])
		}
		sum += category.Bytes
	}
	if sum != stats.TotalBytes || stats.TotalBytes != 100+50+30+20+10+7+5+3+17 {
		t.Fatalf("sum %d total %d", sum, stats.TotalBytes)
	}
	// 总量字段保持原语义：缓存目录计入总量，但不改变 codex_home_bytes。
	if stats.CodexHomeBytes != 7 || stats.WorktreeBytes != 217 || stats.WorktreeCount != 5 {
		t.Fatalf("stats = %+v", stats)
	}
	encoded, _ := json.Marshal(stats)
	if strings.Contains(string(encoded), root) {
		t.Fatalf("stats JSON leaks paths: %s", encoded)
	}
}

func TestMeasureWithoutClassifierOmitsCategories(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "workspaces", "git-a-1", "f"), 10)
	stats := Measure(context.Background(), Paths{WorkspaceRoot: filepath.Join(root, "workspaces"), WorktreePrefix: "git-"}, time.Unix(1, 0))
	encoded, _ := json.Marshal(stats)
	if len(stats.Categories) != 0 || strings.Contains(string(encoded), "categories") {
		t.Fatalf("stats = %s", encoded)
	}
}
