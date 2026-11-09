package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"openfox/internal/daemon/commands"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
)

func writeSkill(t *testing.T, source, name string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(source, "skills", name, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func skillHandlers(t *testing.T, prober *fakeProber, source string) *commandHandlers {
	t.Helper()
	handlers, _, _ := newHandlers(prober)
	handlers.userSkillDirs = func() ([]string, error) { return []string{filepath.Join(source, "skills")}, nil }
	return handlers
}

func TestSkillScanCommand(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "zeta", map[string]string{"SKILL.md": "---\ndescription: Z skill\n---\n", "a/b.txt": "hi"})
	writeSkill(t, source, "alpha", map[string]string{"SKILL.md": "---\ndescription: A skill\n---\n"})
	writeSkill(t, source, "nomd", map[string]string{"readme": "x"})
	handlers := skillHandlers(t, &fakeProber{report: runtime.Capabilities{Login: runtime.LoginLoggedIn, Runnable: true}}, source)
	value, err := handlers.skillScan(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	result := value.(skillScanResult)
	if !result.Supported || result.Runtime != "codex" || len(result.Skills) != 2 || result.Skills[0].Name != "alpha" ||
		result.Skills[1].Description != "Z skill" || result.Skills[1].FileCount != 2 || !result.Skills[1].Importable {
		t.Fatalf("result = %+v", result)
	}
	if _, err := handlers.skillScan(context.Background(), json.RawMessage(`{"x":1}`)); !errors.Is(err, commands.ErrInvalidParams) {
		t.Fatalf("params error = %v", err)
	}
	// 没有 skills 目录：supported 但为空，且序列化为 [] 而非 null。
	handlers.userSkillDirs = func() ([]string, error) { return []string{filepath.Join(t.TempDir(), "skills")}, nil }
	value, _ = handlers.skillScan(context.Background(), nil)
	if encoded, _ := json.Marshal(value); !strings.Contains(string(encoded), `"skills":[]`) {
		t.Fatalf("encoded = %s", encoded)
	}
}

func TestSkillScanUnsupportedWithoutCodex(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "alpha", map[string]string{"SKILL.md": "x"})
	handlers := skillHandlers(t, &fakeProber{detectErr: errors.New("runtime: unavailable: Codex executable not found")}, source)
	value, err := handlers.skillScan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(value); string(encoded) != `{"supported":false,"skills":[]}` {
		t.Fatalf("encoded = %s", encoded)
	}
}

// fakeSkillCenter 模拟中心的 uploadDeviceImportedSkill。
type fakeSkillCenter struct {
	mu       sync.Mutex
	uploads  []supervisor.SkillBundle
	statuses map[string]string // 名称 → 返回的 status，缺省 imported
	cancelAt string            // 收到该名称时返回 409 IMPORT_CANCELED
}

func (c *fakeSkillCenter) server(t *testing.T) *supervisor.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/device/organizations/org-1/commands/cmd-1/skills", func(w http.ResponseWriter, r *http.Request) {
		var bundle supervisor.SkillBundle
		if err := json.NewDecoder(r.Body).Decode(&bundle); err != nil {
			t.Errorf("decode: %v", err)
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if bundle.Name == c.cancelAt {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"IMPORT_CANCELED"}}`))
			return
		}
		c.uploads = append(c.uploads, bundle)
		status := "imported"
		if value, ok := c.statuses[bundle.Name]; ok {
			status = value
		} else if bundle.ErrorCode != "" {
			status = "failed"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": bundle.Name, "status": status, "reason": "", "skill_id": nil})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &supervisor.Client{BaseURL: server.URL, Token: func(context.Context) (string, error) { return "device-token", nil }}
}

func importContext() context.Context {
	return commands.WithIdentityForTest(context.Background(), "org-1", "cmd-1")
}

func TestSkillImportCommand(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "alpha", map[string]string{"SKILL.md": "---\nname: alpha\n---\nbody", "refs/a.txt": "A"})
	writeSkill(t, source, "taken", map[string]string{"SKILL.md": "x"})
	writeSkill(t, source, "nomd", map[string]string{"readme": "x"})
	center := &fakeSkillCenter{statuses: map[string]string{"taken": "skipped"}}
	handlers := skillHandlers(t, &fakeProber{}, source)
	handlers.uploader = center.server(t)
	params := `{"space_id":"space-1","names":["alpha","taken","missing","nomd","alpha","../etc"]}`
	value, err := handlers.skillImport(importContext(), json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	if result := value.(skillImportResult); result.Imported != 1 || result.Skipped != 1 || result.Failed != 3 {
		t.Fatalf("result = %+v", result)
	}
	// 按 names 顺序上传，重复名称只传一次。
	names := []string{}
	for _, upload := range center.uploads {
		names = append(names, upload.Name)
	}
	if strings.Join(names, ",") != "alpha,taken,missing,nomd,../etc" {
		t.Fatalf("uploads = %v", names)
	}
	alpha := center.uploads[0]
	if alpha.ErrorCode != "" || len(alpha.Files) != 2 {
		t.Fatalf("alpha = %+v", alpha)
	}
	for _, file := range alpha.Files {
		content, err := base64.StdEncoding.DecodeString(file.ContentBase64)
		if err != nil || (file.Path == "refs/a.txt" && string(content) != "A") {
			t.Fatalf("file %+v: %v", file, err)
		}
	}
	for index, want := range map[int]string{2: "NOT_FOUND", 3: "SKILL_INVALID", 4: "SKILL_INVALID"} {
		upload := center.uploads[index]
		if upload.ErrorCode != want || upload.Files == nil || len(upload.Files) != 0 {
			t.Fatalf("upload %d = %+v, want %s with empty files", index, upload, want)
		}
	}
}

func TestSkillImportStopsWhenCanceled(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeSkill(t, source, name, map[string]string{"SKILL.md": "x"})
	}
	center := &fakeSkillCenter{cancelAt: "b"}
	handlers := skillHandlers(t, &fakeProber{}, source)
	handlers.uploader = center.server(t)
	value, err := handlers.skillImport(importContext(), json.RawMessage(`{"space_id":"s","names":["a","b","c"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result := value.(skillImportResult); result.Imported != 1 || len(center.uploads) != 1 {
		t.Fatalf("result = %+v uploads = %d", result, len(center.uploads))
	}
}

func TestSkillImportRejectsBadParams(t *testing.T) {
	handlers := skillHandlers(t, &fakeProber{}, t.TempDir())
	handlers.uploader = (&fakeSkillCenter{}).server(t)
	tooMany := `{"space_id":"s","names":["` + strings.Repeat(`a","`, 20) + `a"]}`
	for _, params := range []string{`{}`, `{"space_id":"s","names":[]}`, `{"names":["a"]}`, `{"space_id":"s","names":["a"],"x":1}`, `[]`, tooMany} {
		if _, err := handlers.skillImport(importContext(), json.RawMessage(params)); !errors.Is(err, commands.ErrInvalidParams) {
			t.Errorf("%s → %v", params, err)
		}
	}
}

func TestSkillImportUploadRejectedFailsCommand(t *testing.T) {
	source := t.TempDir()
	writeSkill(t, source, "a", map[string]string{"SKILL.md": "x"})
	handlers := skillHandlers(t, &fakeProber{}, source)
	handlers.uploader = (&fakeSkillCenter{}).server(t)
	// 命令 ID 不匹配 → 中心 404（永久错误）→ 命令失败，不继续上传。
	ctx := commands.WithIdentityForTest(context.Background(), "org-1", "other")
	_, err := handlers.skillImport(ctx, json.RawMessage(`{"space_id":"s","names":["a"]}`))
	var coded *commands.CodedError
	if !errors.As(err, &coded) || coded.Code != "UPLOAD_REJECTED" {
		t.Fatalf("err = %v", err)
	}
}

func TestSkillScanAndImportUseBothDirectories(t *testing.T) {
	home, codexHome := t.TempDir(), t.TempDir()
	agents := filepath.Join(home, ".agents", "skills")
	writeAt := func(root, name, content string) {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	legacy := filepath.Join(codexHome, "skills")
	writeAt(agents, "shared", "---\ndescription: agents\n---\n")
	writeAt(agents, "new", "x")
	writeAt(legacy, "shared", "---\ndescription: legacy\n---\n")
	writeAt(legacy, "old", "x")
	writeAt(legacy, ".system", "x")
	adapter := &codex.Adapter{Environment: map[string]string{"HOME": home, "CODEX_HOME": codexHome}}
	handlers, _, _ := newHandlers(&fakeProber{report: runtime.Capabilities{Login: runtime.LoginLoggedIn, Runnable: true}})
	handlers.userSkillDirs = adapter.UserSkillDirs
	value, err := handlers.skillScan(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, item := range value.(skillScanResult).Skills {
		names = append(names, item.Name+":"+item.Description)
	}
	if strings.Join(names, ",") != "new:,old:,shared:agents" {
		t.Fatalf("scan = %v", names)
	}
	center := &fakeSkillCenter{}
	handlers.uploader = center.server(t)
	if _, err := handlers.skillImport(importContext(), json.RawMessage(`{"space_id":"s","names":["shared","old",".system"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(center.uploads) != 3 || center.uploads[0].ErrorCode != "" || center.uploads[1].ErrorCode != "" || center.uploads[2].ErrorCode == "" {
		t.Fatalf("uploads = %+v", center.uploads)
	}
	content, _ := base64.StdEncoding.DecodeString(center.uploads[0].Files[0].ContentBase64)
	if !strings.Contains(string(content), "agents") {
		t.Fatalf("shared imported from legacy: %q", content)
	}
}
