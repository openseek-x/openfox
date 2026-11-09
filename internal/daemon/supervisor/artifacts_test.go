package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"openfox/internal/runtime"
)

// 发布工具的路径校验、类型判定、去重与上限、与 supervisor 的接线。

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

// artifactRoots 返回已解析符号链接的工作目录与产物目录（macOS 的临时目录在 /var → /private/var 之下）。
func artifactRoots(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, out := filepath.Join(base, "work"), filepath.Join(base, "out")
	for _, dir := range []string{work, out} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return work, out
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rejection(t *testing.T, err error) string {
	t.Helper()
	var typed *artifactRejection
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want a rejection", err)
	}
	return typed.reason
}

func TestInspectArtifactAcceptsFilesInBothRoots(t *testing.T) {
	work, out := artifactRoots(t)
	writeFile(t, filepath.Join(work, "docs", "report.md"), []byte("# 结果\n"))
	writeFile(t, filepath.Join(out, "chart.png"), pngHeader)
	roots := []string{work, out}

	file, err := inspectArtifact("docs/report.md", roots, maxArtifactFileBytes)
	if err != nil || file.Name != "report.md" || file.MIME != "text/markdown" || len(file.SHA256) != 64 {
		t.Fatalf("relative = %+v, %v", file, err)
	}
	file, err = inspectArtifact(filepath.Join(out, "chart.png"), roots, maxArtifactFileBytes)
	if err != nil || file.MIME != "image/png" || !bytes.Equal(file.Content, pngHeader) {
		t.Fatalf("artifact dir = %+v, %v", file.MIME, err)
	}
	// 工作目录内部指向工作目录内部的符号链接可以发布（按真实文件名）。
	if err := os.Symlink(filepath.Join(work, "docs", "report.md"), filepath.Join(work, "link.md")); err != nil {
		t.Fatal(err)
	}
	if file, err = inspectArtifact("link.md", roots, maxArtifactFileBytes); err != nil || file.Name != "report.md" {
		t.Fatalf("inner symlink = %+v, %v", file.Name, err)
	}
}

func TestInspectArtifactRejections(t *testing.T) {
	work, out := artifactRoots(t)
	outside := filepath.Join(filepath.Dir(work), "outside.txt")
	writeFile(t, outside, []byte("secret"))
	writeFile(t, filepath.Join(work, ".env"), []byte("TOKEN=x"))
	writeFile(t, filepath.Join(work, ".env.local"), []byte("TOKEN=x"))
	writeFile(t, filepath.Join(work, "server.pem"), []byte("pem"))
	writeFile(t, filepath.Join(work, "deploy.key"), []byte("key"))
	writeFile(t, filepath.Join(work, "id_rsa"), []byte("key"))
	writeFile(t, filepath.Join(work, "id_ed25519.pub"), []byte("key"))
	writeFile(t, filepath.Join(work, ".git", "config"), []byte("[core]"))
	writeFile(t, filepath.Join(work, "nested", ".ssh", "known_hosts"), []byte("host"))
	writeFile(t, filepath.Join(work, "big.txt"), bytes.Repeat([]byte("a"), 2048))
	if err := os.MkdirAll(filepath.Join(work, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	// 名字无害、指向 .env 的符号链接：解析后的真实文件是敏感文件。
	if err := os.Symlink(filepath.Join(work, ".env"), filepath.Join(work, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	// 指向工作目录外整个目录的符号链接目录。
	if err := os.Symlink(filepath.Dir(work), filepath.Join(out, "parent")); err != nil {
		t.Fatal(err)
	}
	roots := []string{work, out}
	cases := []struct{ path, want string }{
		{"", "path is required"},
		{"a\x00b", "path is invalid"},
		{"../outside.txt", "outside the managed run roots"},
		{"docs/../../outside.txt", "outside the managed run roots"},
		{outside, "outside the managed run roots"},
		{"/etc/hosts", "outside the managed run roots"},
		{"escape.txt", "outside the managed run roots"},
		{filepath.Join(out, "parent", "outside.txt"), "outside the managed run roots"},
		{"../missing-outside.txt", "outside the managed run roots"},
		{work, "outside the managed run roots"},
		{".env", "secret"},
		{".env.local", "secret"},
		{"server.pem", "secret"},
		{"deploy.key", "secret"},
		{"id_rsa", "secret"},
		{"id_ed25519.pub", "secret"},
		{".git/config", "secret"},
		{"nested/.ssh/known_hosts", "secret"},
		{"notes.txt", "secret"},
		{"folder", "directory"},
		{"missing.txt", "file not found"},
		{"big.txt", "larger than"},
	}
	for _, tc := range cases {
		_, err := inspectArtifact(tc.path, roots, 1024)
		if reason := rejection(t, err); !strings.Contains(reason, tc.want) {
			t.Errorf("%q: reason = %q, want %q", tc.path, reason, tc.want)
		}
	}
	if _, err := inspectArtifact("x.txt", nil, 1024); !strings.Contains(rejection(t, err), "no artifact directories") {
		t.Fatalf("no roots: %v", err)
	}
}

func TestClassifyArtifactIsConservative(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		want    string
	}{
		{"a.md", []byte("# t"), "text/markdown"},
		{"a.csv", []byte("a,b\n1,2"), "text/csv"},
		{"main.go", []byte("package main"), "text/plain"},
		{"Makefile", []byte("all:"), "text/plain"},
		{"page.html", []byte("<script>alert(1)</script>"), "text/plain"},
		{"icon.svg", []byte(`<svg onload="alert(1)"/>`), "text/plain"},
		{"a.png", pngHeader, "image/png"},
		// 扩展名与内容不符：一律只能下载。
		{"fake.png", []byte("<html>"), octetStream},
		{"photo.jpg", pngHeader, octetStream},
		{"bin.txt", []byte("a\x00b"), octetStream},
		{"latin.txt", []byte{0xff, 0xfe}, octetStream},
		{"archive.zip", []byte("PK\x03\x04"), octetStream},
		{"noext", []byte("text"), octetStream},
	}
	for _, tc := range cases {
		if got := classifyArtifact(tc.name, tc.content); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestArtifactDisplayName(t *testing.T) {
	cases := map[[2]string]string{
		{"", "report.md"}:           "report.md",
		{"周报", "report.md"}:         "周报.md",
		{"Summary.MD", "report.md"}: "Summary.MD",
		{"a/b\x01c", "x.csv"}:       "b_c.csv",
		{"notes", "Makefile"}:       "notes",
	}
	for input, want := range cases {
		if got := artifactDisplayName(input[0], input[1]); got != want {
			t.Errorf("%q + %q = %q, want %q", input[0], input[1], got, want)
		}
	}
}

// fakeUploads 是可编程的上传器：记录调用，errs 依次作为返回错误（用完后成功）。
type fakeUploads struct {
	mu    sync.Mutex
	calls []string
	errs  []error
}

func (f *fakeUploads) upload(_ context.Context, artifactID, name, mimeType string, content []byte) (UploadedArtifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return UploadedArtifact{}, err
		}
	}
	return UploadedArtifact{ID: artifactID, Name: name, SizeBytes: int64(len(content)), MIMEType: mimeType}, nil
}

func publishArgs(path, title string) json.RawMessage {
	encoded, _ := json.Marshal(map[string]string{"path": path, "title": title})
	if title == "" {
		encoded, _ = json.Marshal(map[string]string{"path": path})
	}
	return encoded
}

func TestArtifactPublisherDedupLimitAndErrors(t *testing.T) {
	work, out := artifactRoots(t)
	uploads := &fakeUploads{}
	publisher := newArtifactPublisher([]string{work, out}, out, uploads.upload)
	writeFile(t, filepath.Join(work, "a.md"), []byte("same"))
	writeFile(t, filepath.Join(out, "b.md"), []byte("same"))

	text, ok := publisher.call(context.Background(), publishArgs("a.md", "周报"))
	if !ok || !strings.Contains(text, `"name":"周报.md"`) || !strings.Contains(text, `"previewable":true`) {
		t.Fatalf("first = %q %v", text, ok)
	}
	// 内容相同的另一个文件：返回已有产物，不再上传。
	text, ok = publisher.call(context.Background(), publishArgs(filepath.Join(out, "b.md"), ""))
	if !ok || !strings.Contains(text, `"already_published":true`) || len(uploads.calls) != 1 || len(publisher.ids()) != 1 {
		t.Fatalf("dedup = %q %v calls=%d", text, ok, len(uploads.calls))
	}
	for _, args := range []json.RawMessage{nil, json.RawMessage(`{"path":1}`), json.RawMessage(`{"path":"a.md","extra":1}`)} {
		if text, ok := publisher.call(context.Background(), args); ok || !strings.Contains(text, "Invalid arguments") {
			t.Fatalf("args %s = %q %v", args, text, ok)
		}
	}
	if text, ok := publisher.call(context.Background(), publishArgs("../x", "")); ok || !strings.Contains(text, "outside the managed run roots") {
		t.Fatalf("escape = %q", text)
	}
	// 上限：第 11 个不同内容的文件被拒绝。
	for i := 1; i < maxRunArtifacts; i++ {
		writeFile(t, filepath.Join(out, "f.txt"), []byte(strings.Repeat("x", i)))
		if text, ok := publisher.call(context.Background(), publishArgs(filepath.Join(out, "f.txt"), "")); !ok {
			t.Fatalf("publish %d = %q", i, text)
		}
	}
	writeFile(t, filepath.Join(out, "f.txt"), []byte("eleventh"))
	if text, ok := publisher.call(context.Background(), publishArgs(filepath.Join(out, "f.txt"), "")); ok || !strings.Contains(text, "limit of 10") {
		t.Fatalf("limit = %q", text)
	}
	if got := publisher.ids(); len(got) != maxRunArtifacts {
		t.Fatalf("ids = %d", len(got))
	}
	var nilPublisher *artifactPublisher
	if nilPublisher.ids() != nil {
		t.Fatal("nil publisher has ids")
	}
}

func TestArtifactPublisherUploadErrors(t *testing.T) {
	work, out := artifactRoots(t)
	writeFile(t, filepath.Join(work, "a.txt"), []byte("a"))
	cases := []struct {
		errs  []error
		want  string
		ok    bool
		calls int
	}{
		{[]error{&APIError{Status: http.StatusConflict, Code: "STATE_CONFLICT"}}, "no longer active", false, 1},
		{[]error{&APIError{Status: http.StatusRequestEntityTooLarge, Code: "RESOURCE_LIMIT"}}, "organization's artifact limit", false, 1},
		{[]error{&APIError{Status: http.StatusServiceUnavailable}, &APIError{Status: http.StatusServiceUnavailable}}, "rejected the upload (503", false, 2},
		// 暂时性错误重试一次后成功。
		{[]error{errors.New("connection reset")}, "Artifact published", true, 2},
	}
	for _, tc := range cases {
		uploads := &fakeUploads{errs: tc.errs}
		publisher := newArtifactPublisher([]string{work, out}, out, uploads.upload)
		text, ok := publisher.call(context.Background(), publishArgs("a.txt", ""))
		if ok != tc.ok || !strings.Contains(text, tc.want) || len(uploads.calls) != tc.calls {
			t.Errorf("errs %v: %q ok=%v calls=%d", tc.errs, text, ok, len(uploads.calls))
		}
		if !tc.ok && len(publisher.ids()) != 0 {
			t.Errorf("failed upload recorded an id")
		}
	}
}

func TestBuildArtifactSectionMentionsDirectoryAndRule(t *testing.T) {
	section := buildArtifactSection("/state/run-artifacts/run-1")
	for _, want := range []string{"openfox_publish_artifact", "/state/run-artifacts/run-1", "does not deliver", "10 artifacts", "20 MB"} {
		if !strings.Contains(section, want) {
			t.Errorf("section missing %q", want)
		}
	}
	tool := publishTool()
	if _, err := (runtime.RunRequest{Prompt: "p", Tools: []runtime.Tool{tool}}).Normalize(); err != nil {
		t.Fatal(err)
	}
	// 段落计入 prompt 预算，排在最前。
	prompt := buildPrompt(RunContext{Prompt: "go"}, section)
	if !strings.HasPrefix(prompt, "## Delivering files") || !strings.HasSuffix(prompt, "go") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func lastRequest(adapter *fakeAdapter) runtime.RunRequest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.requests[len(adapter.requests)-1]
}

func TestSupervisorWithoutToolSupportDeclaresNoPublishTool(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{}
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	request := lastRequest(adapter)
	if len(request.Tools) != 0 || request.ToolCall != nil || request.ArtifactDirectory != "" || strings.Contains(request.Prompt, "openfox_publish_artifact") {
		t.Fatalf("tools=%d dir=%q", len(request.Tools), request.ArtifactDirectory)
	}
	if center.snapshot().results[0].ArtifactIDs != nil {
		t.Fatal("artifact ids sent without the tool")
	}
}

// TestSupervisorPublishesArtifactsAndReportsIDs：支持工具的 Runtime 声明发布工具（没有任务也声明）；
// Run 进行中调用工具上传到中心，结果带上 artifact_ids；Run 结束后产物目录被删除。
func TestSupervisorPublishesArtifactsAndReportsIDs(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{tools: true}
	var toolText string
	var toolOK bool
	var artifactDir string
	adapter.onStart = func(request runtime.RunRequest) {
		artifactDir = request.ArtifactDirectory
		writeFile(t, filepath.Join(request.ArtifactDirectory, "结果.csv"), []byte("a,b\n1,2\n"))
		toolText, toolOK = request.ToolCall(context.Background(), publishToolName, "call-1", publishArgs(filepath.Join(request.ArtifactDirectory, "结果.csv"), ""))
		if text, ok := request.ToolCall(context.Background(), "openfox_task_view", "call-2", nil); ok || text != "unknown tool" {
			t.Errorf("task tool without task = %q %v", text, ok)
		}
	}
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	request := lastRequest(adapter)
	if len(request.Tools) != 1 || request.Tools[0].Name != publishToolName || !strings.Contains(request.Prompt, request.ArtifactDirectory) {
		t.Fatalf("tools=%+v", request.Tools)
	}
	if !toolOK || !strings.Contains(toolText, `"name":"结果.csv"`) {
		t.Fatalf("tool = %q %v", toolText, toolOK)
	}
	snapshot := center.snapshot()
	if len(snapshot.artifactUploads) != 1 {
		t.Fatalf("uploads = %d", len(snapshot.artifactUploads))
	}
	upload := snapshot.artifactUploads[0]
	if upload.Generation != "1" || upload.MIME != "text/csv" || upload.Name != "结果.csv" || string(upload.Body) != "a,b\n1,2\n" {
		t.Fatalf("upload = %+v", upload)
	}
	if ids := snapshot.results[0].ArtifactIDs; len(ids) != 1 || ids[0] != upload.ID {
		t.Fatalf("artifact ids = %v, want [%s]", ids, upload.ID)
	}
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Fatalf("artifact dir still exists: %v", err)
	}
}

// TestSupervisorPublishToolWithTaskTools：有任务时任务工具照旧转发中心，发布工具追加在后面。
func TestSupervisorPublishToolWithTaskTools(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{tools: true}
	center.extend = func(rc *RunContext) { rc.Task = testTask() }
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	request := lastRequest(adapter)
	if len(request.Tools) != 5 || request.Tools[4].Name != publishToolName {
		t.Fatalf("tools = %d", len(request.Tools))
	}
	if text, ok := request.ToolCall(context.Background(), "openfox_task_comment", "c1", json.RawMessage(`{"body":"hi"}`)); !ok || text != "done" {
		t.Fatalf("task tool = %q %v", text, ok)
	}
	// 没有发布任何产物：结果不带 artifact_ids（与旧中心完全一致）。
	if ids := center.snapshot().results[0].ArtifactIDs; ids != nil {
		t.Fatalf("artifact ids = %v", ids)
	}
}

// TestSupervisorPublishRejectedAfterRunConflict：中心以 409 拒绝上传时工具返回明确原因，结果不带该产物。
func TestSupervisorPublishRejectedAfterRunConflict(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{tools: true}
	center.artifactStatus = http.StatusConflict
	var toolText string
	adapter.onStart = func(request runtime.RunRequest) {
		writeFile(t, filepath.Join(request.WorkingDirectory, "out.txt"), []byte("x"))
		toolText, _ = request.ToolCall(context.Background(), publishToolName, "c1", publishArgs("out.txt", ""))
	}
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	if !strings.Contains(toolText, "no longer active") || strings.Contains(toolText, "device-token") {
		t.Fatalf("tool = %q", toolText)
	}
	if ids := center.snapshot().results[0].ArtifactIDs; ids != nil {
		t.Fatalf("artifact ids = %v", ids)
	}
}

// TestSupervisorDropsRejectedArtifactIDs：中心以 INVALID_REQUEST 拒绝 artifact_ids 时去掉后重试，结果仍送达。
func TestSupervisorDropsRejectedArtifactIDs(t *testing.T) {
	center, adapter := newCenter(), &fakeAdapter{tools: true}
	center.rejectArtifactIDs = true
	adapter.onStart = func(request runtime.RunRequest) {
		writeFile(t, filepath.Join(request.WorkingDirectory, "out.md"), []byte("# x"))
		_, _ = request.ToolCall(context.Background(), publishToolName, "c1", publishArgs("out.md", ""))
	}
	newHarness(t, center, adapter, "")
	waitFor(t, "result upload", func() bool { return len(center.snapshot().results) == 1 })
	snapshot := center.snapshot()
	if len(snapshot.artifactUploads) != 1 || snapshot.results[0].ArtifactIDs != nil || snapshot.results[0].Status != "SUCCEEDED" {
		t.Fatalf("uploads=%d result=%+v", len(snapshot.artifactUploads), snapshot.results[0])
	}
}

func TestResultInputOmitsEmptyArtifactIDs(t *testing.T) {
	encoded, _ := json.Marshal(ResultInput{Status: "SUCCEEDED"})
	if strings.Contains(string(encoded), "artifact_ids") {
		t.Fatalf("encoded = %s", encoded)
	}
	encoded, _ = json.Marshal(ResultInput{Status: "SUCCEEDED", ArtifactIDs: []string{"a"}})
	if !strings.Contains(string(encoded), `"artifact_ids":["a"]`) {
		t.Fatalf("encoded = %s", encoded)
	}
}
