package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	localbrowser "openfox/internal/daemon/browser"
	"openfox/internal/runtime"
)

type browserFileHost struct {
	execute func(localbrowser.Request) runtime.ToolResult
	calls   int
}

func (h *browserFileHost) Execute(_ context.Context, r localbrowser.Request) runtime.ToolResult {
	h.calls++
	return h.execute(r)
}
func (*browserFileHost) Close() {}

func TestBrowserUploadsSnapshotSafeFilesUntilRunEnds(t *testing.T) {
	work, out := artifactRoots(t)
	f := &browserFiles{roots: []string{work, out}, artifactDir: out, transferRoot: filepath.Join(filepath.Dir(work), "browser", "transfers")}
	t.Cleanup(f.close)
	writeFile(t, filepath.Join(work, "report.txt"), []byte("original"))
	writeFile(t, filepath.Join(work, ".env"), []byte("secret"))
	outside := filepath.Join(t.TempDir(), "private.txt")
	writeFile(t, outside, []byte("private"))
	if err := os.Symlink(outside, filepath.Join(work, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	var staged string
	host := &browserFileHost{execute: func(r localbrowser.Request) runtime.ToolResult {
		if len(r.Files) != 1 || r.Files[0].Path == filepath.Join(work, "report.txt") {
			t.Fatal("source path was not staged")
		}
		staged = r.Files[0].Path
		if data, err := os.ReadFile(staged); err != nil || string(data) != "original" {
			t.Fatalf("snapshot=%s error=%v", data, err)
		}
		writeFile(t, filepath.Join(work, "report.txt"), []byte("changed"))
		return runtime.TextResult("completed", true)
	}}
	request := localbrowser.Request{Tool: "openfox_browser_files", Arguments: json.RawMessage(`{"action":"upload","paths":["report.txt"]}`)}
	if result := f.invoke(context.Background(), host, request); !result.OK {
		t.Fatal(result)
	}
	if data, _ := os.ReadFile(staged); string(data) != "original" {
		t.Fatal("snapshot deleted or changed before submit")
	}
	for _, name := range []string{".env", "escape.txt", outside} {
		request.Arguments, _ = json.Marshal(map[string]any{"action": "upload", "paths": []string{name}})
		if result := f.invoke(context.Background(), host, request); result.OK {
			t.Fatalf("allowed %s", name)
		}
	}
	if host.calls != 1 {
		t.Fatal("unsafe file reached host")
	}
	request.ReadOnly = true
	request.Arguments = json.RawMessage(`{"action":"upload","paths":["report.txt"]}`)
	if result := f.invoke(context.Background(), host, request); result.OK {
		t.Fatal("readonly upload")
	}
	f.close()
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("snapshot leaked: %v", err)
	}
}

func TestBrowserDownloadsBindRunAndCannotEscapeArtifactDirectory(t *testing.T) {
	work, out := artifactRoots(t)
	f := &browserFiles{roots: []string{work, out}, artifactDir: out, downloadRoot: filepath.Join(filepath.Dir(work), "browser", "downloads")}
	const run = "10000000-0000-4000-8000-000000000001"
	const fileID = "10000000-0000-4000-8000-000000000002"
	file := filepath.Join(f.downloadRoot, run+"-1", "report.txt")
	writeFile(t, file, []byte("download"))
	inspected, err := inspectArtifact(file, resolveRoots([]string{filepath.Dir(file)}), maxArtifactFileBytes)
	if err != nil {
		t.Fatal(err)
	}
	host := &browserFileHost{execute: func(localbrowser.Request) runtime.ToolResult {
		body, _ := json.Marshal(map[string]any{"file": localbrowser.StagedFile{Path: file, Name: "report.txt", Size: 8, SHA256: inspected.SHA256}})
		return runtime.TextResult(string(body), true)
	}}
	request := localbrowser.Request{Tool: "openfox_browser_files", RunID: run, Generation: 2, Arguments: json.RawMessage(`{"action":"get","file_id":"` + fileID + `"}`)}
	if result := f.invoke(context.Background(), host, request); result.OK {
		t.Fatal("other generation's file accepted")
	}
	request.Generation = 1
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(out, "browser-downloads")); err != nil {
		t.Fatal(err)
	}
	if result := f.invoke(context.Background(), host, request); result.OK {
		t.Fatal("write escaped through symlink")
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatal("outside directory changed")
	}
	if err := os.Remove(filepath.Join(out, "browser-downloads")); err != nil {
		t.Fatal(err)
	}
	result := f.invoke(context.Background(), host, request)
	if !result.OK {
		t.Fatal(result)
	}
	var downloaded map[string]string
	_ = json.Unmarshal([]byte(result.Text), &downloaded)
	if data, err := os.ReadFile(downloaded["path"]); err != nil || string(data) != "download" {
		t.Fatalf("content %q err %v", data, err)
	}
	previousCalls := host.calls
	if repeated := f.invoke(context.Background(), host, request); repeated.Text != result.Text || host.calls != previousCalls {
		t.Fatal("get was not idempotent")
	}
}
