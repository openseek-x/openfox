package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	domainruntime "openfox/internal/runtime"
)

// 本文件的用例调用本机真实 Codex（app-server，审批 never，workspace-write，可写根仅临时目录，允许联网），
// 只在 OFD_REAL_CODEX=1 时运行，版本由 OFD_REAL_CODEX_VERSION 指定。令牌刷新用例会改写用户的
// ~/.codex/auth.json，另需 OFD_REAL_REFRESH=1，且调用方应事先备份该文件。日志不输出凭据内容。

func realAdapter(t *testing.T) *Adapter {
	t.Helper()
	if os.Getenv("OFD_REAL_CODEX") != "1" {
		t.Skip("set OFD_REAL_CODEX=1 to run against the installed Codex")
	}
	version := strings.TrimSpace(os.Getenv("OFD_REAL_CODEX_VERSION"))
	if version == "" {
		t.Fatal("OFD_REAL_CODEX_VERSION is required")
	}
	adapter := New("")
	adapter.SupportedVersions = []string{version}
	adapter.HomeRoot = t.TempDir()
	return adapter
}

func realWorkdir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func logEvent(t *testing.T, event domainruntime.Event) {
	t.Helper()
	text := event.Text
	if len(text) > 160 {
		text = text[:160]
	}
	t.Logf("event %d %s %q %s", event.Sequence, event.Type, text, event.Payload)
}

func TestRealCodexListModels(t *testing.T) {
	adapter := realAdapter(t)
	models, err := adapter.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, model := range models {
		t.Logf("model id=%s name=%q default=%v", model.ID, model.DisplayName, model.IsDefault)
		if model.IsDefault {
			defaults++
		}
	}
	if len(models) == 0 || defaults > 1 {
		t.Fatalf("models = %#v", models)
	}
	if entries, _ := os.ReadDir(adapter.HomeRoot); len(entries) != 0 {
		t.Fatalf("codex homes left: %d", len(entries))
	}
}

func TestRealCodexCancel(t *testing.T) {
	adapter := realAdapter(t)
	marker := "127.31"
	execution, err := adapter.Start(context.Background(), domainruntime.RunRequest{
		Prompt:           "Run the shell command `sleep " + marker + "` in the current directory and wait for it to finish, then reply with exactly: DONE",
		WorkingDirectory: realWorkdir(t), Timeout: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := execution.Events()
	for event := range events {
		logEvent(t, event)
		if event.Type == domainruntime.EventStarted {
			break
		}
	}
	go func() {
		for event := range events {
			logEvent(t, event)
		}
	}()
	// 等到 sleep 进程真正出现，再取消。
	deadline := time.Now().Add(3 * time.Minute)
	for !processRunning(marker) {
		if time.Now().After(deadline) {
			t.Fatal("codex did not start the sleep command")
		}
		time.Sleep(500 * time.Millisecond)
	}
	started := time.Now()
	if err := execution.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := execution.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	t.Logf("result status=%s code=%s error=%q exit=%d cancel_took=%s", result.Status, result.ErrorCode, result.Error, result.ExitCode, elapsed.Round(100*time.Millisecond))
	if result.Status != domainruntime.StatusCancelled || elapsed > 15*time.Second {
		t.Fatalf("result = %#v after %s", result, elapsed)
	}
	time.Sleep(time.Second)
	if processRunning(marker) {
		t.Fatal("sleep command survived cancellation")
	}
	if entries, _ := os.ReadDir(adapter.HomeRoot); len(entries) != 0 {
		t.Fatalf("codex homes left: %d", len(entries))
	}
}

func processRunning(marker string) bool {
	output, _ := exec.Command("pgrep", "-f", "sleep "+marker).Output()
	return strings.TrimSpace(string(output)) != ""
}

func TestRealCodexUnknownModelFails(t *testing.T) {
	adapter := realAdapter(t)
	execution, err := adapter.Start(context.Background(), domainruntime.RunRequest{
		Prompt: "Reply with exactly: OK", Model: "openfox-nonexistent-model",
		WorkingDirectory: realWorkdir(t), Timeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	for event := range execution.Events() {
		logEvent(t, event)
	}
	result, err := execution.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("result status=%s code=%s error=%q output=%q", result.Status, result.ErrorCode, result.Error, result.Output)
	if result.Status != domainruntime.StatusFailed || result.Error == "" || result.Output != "" {
		t.Fatalf("result = %#v", result)
	}
}

// accountRefreshSession：initialize → account/read {refreshToken:true}（主动刷新令牌，不调用模型）→ 结束。
type accountRefreshSession struct{ hasAccount bool }

func (s *accountRefreshSession) Begin(send domainruntime.Sender, _ func(error)) error {
	return sendRequest(send, requestInitialize, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "openfox_daemon", "title": "OpenFox", "version": "1"}, "capabilities": nil,
	})
}

func (s *accountRefreshSession) HandleLine(line []byte, send domainruntime.Sender) ([]domainruntime.Event, bool, error) {
	var message rpcMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return nil, false, err
	}
	if len(message.ID) == 0 || message.Method != "" {
		return nil, false, nil
	}
	if message.Error != nil {
		return []domainruntime.Event{{Type: domainruntime.EventFailed, Text: message.Error.Message}}, true, nil
	}
	if id, _ := strconv.Atoi(string(message.ID)); id == requestInitialize {
		if err := send([]byte(`{"method":"initialized"}`)); err != nil {
			return nil, false, err
		}
		return nil, false, sendRequest(send, 2, "account/read", map[string]any{"refreshToken": true})
	}
	// 只判断是否有账号，不解析或输出账号信息。
	var result struct {
		Account json.RawMessage `json:"account"`
	}
	_ = json.Unmarshal(message.Result, &result)
	s.hasAccount = len(result.Account) > 0 && string(result.Account) != "null"
	return []domainruntime.Event{{Type: domainruntime.EventCompleted}}, true, nil
}

func (s *accountRefreshSession) Interrupt(domainruntime.Sender) bool { return false }

func fileFingerprint(t *testing.T, path string) (string, os.FileInfo) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(contents)), info
}

func TestRealCodexTokenRefreshWriteBack(t *testing.T) {
	adapter := realAdapter(t)
	if os.Getenv("OFD_REAL_REFRESH") != "1" {
		t.Skip("set OFD_REAL_REFRESH=1 (after backing up ~/.codex/auth.json) to refresh real credentials")
	}
	capabilities, err := adapter.Detect(context.Background())
	if err != nil || !capabilities.Runnable {
		t.Fatalf("Detect = %#v, %v", capabilities, err)
	}
	prepared, err := adapter.prepare(capabilities.Executable, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := prepared.home.sourceAuth
	beforeSum, beforeInfo := fileFingerprint(t, source)
	session := &accountRefreshSession{}
	execution, err := domainruntime.StartProcess(context.Background(), domainruntime.ProcessSpec{
		Executable: prepared.path, Args: []string{"app-server"}, Directory: prepared.home.dir, Environment: prepared.env,
		Timeout: 2 * time.Minute, MaxLineBytes: domainruntime.DefaultMaxLineBytes, MaxOutputBytes: domainruntime.DefaultMaxOutput,
		MaxEvents: domainruntime.DefaultMaxEvents, RequireCompletion: true, Redact: prepared.redact, Session: session,
	})
	if err != nil {
		prepared.home.remove()
		t.Fatal(err)
	}
	result, err := execution.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wrote, finishErr := prepared.home.finish()
	afterSum, afterInfo := fileFingerprint(t, source)
	t.Logf("refresh status=%s error=%q account=%v wrote_back=%v finish_err=%v", result.Status, result.Error, session.hasAccount, wrote, finishErr)
	t.Logf("auth.json changed=%v mtime_before=%s mtime_after=%s size_before=%d size_after=%d mode=%o",
		beforeSum != afterSum, beforeInfo.ModTime().Format(time.RFC3339), afterInfo.ModTime().Format(time.RFC3339),
		beforeInfo.Size(), afterInfo.Size(), afterInfo.Mode().Perm())
	if result.Status != domainruntime.StatusSucceeded || !session.hasAccount || finishErr != nil || !wrote || beforeSum == afterSum || afterInfo.Mode().Perm() != 0o600 {
		t.Fatal("token refresh was not written back")
	}

	// 写回的新凭据必须可用：再跑一次真实 Run。
	run, err := adapter.Start(context.Background(), domainruntime.RunRequest{Prompt: "Reply with exactly: OK", WorkingDirectory: realWorkdir(t), Timeout: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	runResult, err := run.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("run with refreshed credentials status=%s output=%q", runResult.Status, runResult.Output)
	if runResult.Status != domainruntime.StatusSucceeded || strings.TrimSpace(runResult.Output) != "OK" {
		t.Fatalf("result = %#v", runResult)
	}
}
