package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/runtime"
)

const (
	attachmentOne = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	attachmentTwo = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

func TestSafeAttachmentName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":         "report.pdf",
		"../../etc/passwd":   "passwd",
		`C:\dir\win.txt`:     "win.txt",
		"a\x00b.txt":         "a_b.txt",
		"..":                 "attachment",
		"":                   "attachment",
		"  spaced name.md  ": "spaced name.md",
	}
	for input, want := range cases {
		if got := safeAttachmentName(input); got != want {
			t.Errorf("safeAttachmentName(%q) = %q, want %q", input, got, want)
		}
	}
	long := strings.Repeat("长", 80) + ".txt"
	got := safeAttachmentName(long)
	if len(got) > maxAttachmentName || !strings.HasSuffix(got, ".txt") {
		t.Errorf("long name = %q (%d bytes)", got, len(got))
	}
}

// attachmentSnapshot 是 Run 进行中（Start 时）观察到的附件状态。
type attachmentSnapshot struct {
	content   string
	readErr   error
	path      string
	workspace string
}

func TestSupervisorDownloadsAttachmentsOutsideWorkdir(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	const content = "hello attachment"
	center.attachmentFiles = map[string]string{attachmentOne: content}
	center.extend = func(rc *RunContext) {
		rc.Attachments = []RunAttachment{{ID: attachmentOne, Name: "../notes.txt", ContentType: "text/plain; charset=utf-8", SizeBytes: int64(len(content))}}
	}
	observed := make(chan attachmentSnapshot, 1)
	adapter.onStart = func(request runtime.RunRequest) {
		// 提示里列出的是绝对路径；Run 进行中文件存在且内容正确。
		path := ""
		for _, line := range strings.Split(request.Prompt, "\n") {
			if rest, ok := strings.CutPrefix(line, "- "); ok && strings.Contains(rest, "notes.txt") {
				path, _, _ = strings.Cut(rest, "（")
			}
		}
		data, err := os.ReadFile(path)
		observed <- attachmentSnapshot{content: string(data), readErr: err, path: path, workspace: request.WorkingDirectory}
	}
	harness := newHarness(t, center, adapter, "")
	waitFor(t, "run started", func() bool { return adapter.startCount() == 1 })
	snapshot := <-observed
	if snapshot.readErr != nil || snapshot.content != content {
		t.Fatalf("attachment = %q err = %v path = %q", snapshot.content, snapshot.readErr, snapshot.path)
	}
	root := harness.supervisor.config.AttachmentRoot
	if !filepath.IsAbs(snapshot.path) || !strings.HasPrefix(snapshot.path, filepath.Join(root, testRun)+string(filepath.Separator)) {
		t.Fatalf("path %q must be absolute and under %s/%s", snapshot.path, root, testRun)
	}
	// 绝不能落在工作目录里（DIRECT 隔离时工作目录就是用户的真实仓库）。
	if rel, err := filepath.Rel(snapshot.workspace, snapshot.path); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("attachment %q is inside the workspace %q", snapshot.path, snapshot.workspace)
	}
	if filepath.Base(snapshot.path) != "notes.txt" {
		t.Fatalf("file name must be sanitized, got %q", snapshot.path)
	}
	adapter.mu.Lock()
	prompt := adapter.requests[0].Prompt
	adapter.mu.Unlock()
	if !strings.Contains(prompt, snapshot.path) || !strings.HasSuffix(prompt, "say OK") {
		t.Fatalf("prompt = %q", prompt)
	}
	if calls := center.snapshot().attachmentCalls; len(calls) != 1 || !strings.HasSuffix(calls[0], "?generation=1") {
		t.Fatalf("calls = %v", calls)
	}
	// Run 结束后整个 Run 目录被删除。
	waitFor(t, "result uploaded", func() bool { return len(center.snapshot().results) == 1 })
	waitFor(t, "attachment dir removed", func() bool {
		_, err := os.Stat(filepath.Join(root, testRun))
		return os.IsNotExist(err)
	})
	// 工作目录里也没有留下任何附件目录。
	if _, err := os.Stat(filepath.Join(snapshot.workspace, ".openfox-attachments")); !os.IsNotExist(err) {
		t.Fatalf("workspace must not contain .openfox-attachments, stat err = %v", err)
	}
}

func TestSupervisorWithoutAttachmentsLeavesPromptUnchanged(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	harness := newHarness(t, center, adapter, "")
	waitFor(t, "run started", func() bool { return adapter.startCount() == 1 })
	adapter.mu.Lock()
	prompt := adapter.requests[0].Prompt
	adapter.mu.Unlock()
	if strings.Contains(prompt, "附件") || len(center.snapshot().attachmentCalls) != 0 {
		t.Fatalf("prompt = %q", prompt)
	}
	if _, err := os.Stat(harness.supervisor.config.AttachmentRoot); !os.IsNotExist(err) {
		t.Fatalf("no attachment root should be created without attachments, stat err = %v", err)
	}
}

func TestSupervisorAttachmentFailureIsPrepareFailed(t *testing.T) {
	cases := map[string]func(*fakeCenter){
		"http error":    func(c *fakeCenter) { c.attachmentStatus = map[string]int{attachmentTwo: 409} },
		"missing":       func(c *fakeCenter) { delete(c.attachmentFiles, attachmentTwo) },
		"size mismatch": func(c *fakeCenter) { c.attachmentFiles[attachmentTwo] = "short" },
		"larger than announced": func(c *fakeCenter) {
			c.attachmentFiles[attachmentTwo] = "this is much longer than sixteen bytes"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			center, adapter := newCenter(), &fakeAdapter{}
			center.attachmentFiles = map[string]string{attachmentOne: "hello attachment", attachmentTwo: "hello attachment"}
			mutate(center)
			center.extend = func(rc *RunContext) {
				rc.Attachments = []RunAttachment{
					{ID: attachmentOne, Name: "a.txt", ContentType: "text/plain", SizeBytes: 16},
					{ID: attachmentTwo, Name: "b.txt", ContentType: "text/plain", SizeBytes: 16},
				}
			}
			harness := newHarness(t, center, adapter, "")
			waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
			snapshot := center.snapshot()
			if snapshot.prepareFailed[0] != "PREPARE_FAILED" || snapshot.starts != 0 || adapter.startCount() != 0 {
				t.Fatalf("prepare-failed=%v starts=%d processes=%d", snapshot.prepareFailed, snapshot.starts, adapter.startCount())
			}
			// 第二个附件失败时，第一个已下载的文件也一并清理。
			waitFor(t, "attachment dir removed", func() bool {
				_, err := os.Stat(filepath.Join(harness.supervisor.config.AttachmentRoot, testRun))
				return os.IsNotExist(err)
			})
		})
	}
}

func TestSupervisorRejectsInvalidAttachmentManifest(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	center.extend = func(rc *RunContext) {
		rc.Attachments = []RunAttachment{{ID: "../escape", Name: "a.txt", SizeBytes: 1}}
	}
	harness := newHarness(t, center, adapter, "")
	waitFor(t, "prepare-failed", func() bool { return len(center.snapshot().prepareFailed) == 1 })
	if len(center.snapshot().attachmentCalls) != 0 || adapter.startCount() != 0 {
		t.Fatalf("calls=%v processes=%d", center.snapshot().attachmentCalls, adapter.startCount())
	}
	if entries, _ := os.ReadDir(harness.supervisor.config.AttachmentRoot); len(entries) != 0 {
		t.Fatalf("attachment root must stay empty, got %v", entries)
	}
}

func TestRemoveStaleAttachmentDirs(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	old, fresh := filepath.Join(root, "old-run"), filepath.Join(root, "fresh-run")
	for _, dir := range []string{old, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, "att"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-staleAttachmentsAfter - time.Minute)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	removeStaleAttachmentDirs(root, now)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("stale dir must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh dir must stay: %v", err)
	}
}

func TestAttachmentRootDefault(t *testing.T) {
	s := &Supervisor{}
	if got := s.attachmentRoot(); got != filepath.Join(os.TempDir(), "openfox-attachments") {
		t.Fatalf("default root = %q", got)
	}
	s.config.AttachmentRoot = "/var/lib/openfox/attachments"
	if got := s.attachmentRoot(); got != "/var/lib/openfox/attachments" {
		t.Fatalf("configured root = %q", got)
	}
}
