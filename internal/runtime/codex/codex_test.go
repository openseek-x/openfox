package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"

	domainruntime "openfox/internal/runtime"
)

// fakeAppServer 是遵循 codex app-server 协议的假 CLI。请求 ID 固定（1 initialize、2 thread/start、
// 3 turn/start），脚本按顺序读请求、按模式回复。收到的请求追加到 $FAKE_LOG；
// 进程看到的 CODEX_HOME 及其中的文件名写入 $FAKE_LOG.home，供断言使用。
const fakeAppServer = `#!/bin/sh
mode="$OPENFOX_CODEX_FIXTURE_MODE"
case "$1" in
  --version) printf '%s\n' 'codex-cli 0.0.1-fixture'; exit 0 ;;
  login)
    if [ "$mode" = "logged_out" ]; then echo 'Not logged in'; else echo 'Logged in using fixture'; fi
    exit 0 ;;
  app-server)
    if [ "$2" = "--help" ]; then echo 'Run the app server: codex app-server'; exit 0; fi ;;
  *) exit 2 ;;
esac
log="$FAKE_LOG"
{ echo "$CODEX_HOME"; ls "$CODEX_HOME"; } > "$log.home"
next() { IFS= read -r line || exit 0; printf '%s\n' "$line" >> "$log"; }
if [ "$mode" = "silent" ]; then while IFS= read -r line; do :; done; exit 0; fi
next; echo '{"id":1,"result":{"userAgent":"fixture","codexHome":"/x","platformFamily":"unix","platformOs":"macos"}}'
next; next
if [ "$mode" = "models" ]; then
  echo '{"id":2,"result":{"data":[{"id":"gpt-a","model":"gpt-a","displayName":"GPT A","hidden":false,"isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"high"},{"reasoningEffort":"xhigh"},{"reasoningEffort":"unknown"}]},{"id":"secret-internal","model":"secret-internal","displayName":"Hidden","hidden":true,"isDefault":false}],"nextCursor":"c2"}}'
  next
  echo '{"id":3,"result":{"data":[{"id":"","model":"gpt-b","displayName":"","hidden":false,"isDefault":false}],"nextCursor":null}}'
  while IFS= read -r line; do printf '%s\n' "$line" >> "$log"; done; exit 0
fi
if [ "$mode" = "models_error" ]; then echo '{"id":2,"error":{"code":-32000,"message":"not signed in"}}'; next; exit 0; fi
if [ "$mode" = "thread_error" ]; then echo '{"id":2,"error":{"code":-32000,"message":"model not available"}}'; next; exit 0; fi
echo '{"id":2,"result":{"thread":{"id":"th-1"}}}'
next; echo '{"id":3,"result":{"turn":{"id":"tu-1","status":"inProgress"}}}'
echo '{"method":"turn/started","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"inProgress"}}}'
case "$mode" in
  success|noexit)
    echo '{"method":"configWarning","params":{"summary":"ignoring 1 unrecognized setting","details":null}}'
    echo '{"method":"error","params":{"error":{"message":"Reconnecting... 2/5"},"willRetry":true,"threadId":"th-1","turnId":"tu-1"}}'
    echo '{"method":"some/future/notification","params":{}}'
    echo '{"method":"item/completed","params":{"threadId":"other","turnId":"x","item":{"type":"agentMessage","text":"from another thread","phase":null}}}'
    echo '{"method":"item/completed","params":{"threadId":"th-1","turnId":"tu-1","item":{"type":"agentMessage","text":"thinking about it","phase":"commentary"}}}'
    echo '{"method":"item/completed","params":{"threadId":"th-1","turnId":"tu-1","item":{"type":"commandExecution","command":"ls","status":"completed","exitCode":0}}}'
    echo '{"method":"item/completed","params":{"threadId":"th-1","turnId":"tu-1","item":{"type":"agentMessage","text":"OK token=fixture-secret","phase":"final_answer"}}}'
    echo "refreshed" > "$CODEX_HOME/auth.json"
    echo '{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"completed","error":null}}}'
    if [ "$mode" = "noexit" ]; then sleep 30; fi ;;
  fatal)
    echo '{"method":"error","params":{"error":{"message":"stream disconnected before completion"},"willRetry":false,"threadId":"th-1","turnId":"tu-1"}}' ;;
  turn_failed)
    echo '{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"failed","error":{"message":"usage limit reached"}}}}' ;;
  approval)
    echo '{"id":"srv-1","method":"item/commandExecution/requestApproval","params":{"threadId":"th-1","turnId":"tu-1","itemId":"i"}}' ;;
  early_exit) exit 0 ;;
  slow)
    next
    echo '{"id":4,"result":{}}'
    echo '{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"tu-1","status":"interrupted","error":null}}}' ;;
esac
while IFS= read -r line; do printf '%s\n' "$line" >> "$log"; done
exit 0
`

type fixture struct {
	adapter *Adapter
	log     string
	source  string
	root    string
}

func newFixture(t *testing.T, mode string) fixture {
	t.Helper()
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codex-fixture.sh")
	if err := os.WriteFile(path, []byte(fakeAppServer), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "user-codex-home")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "auth.json"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte("model = \"personal\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "requests.log")
	adapter := New(path)
	adapter.SupportedVersions = []string{"0.0.1-fixture"}
	adapter.HomeRoot = filepath.Join(dir, "homes")
	adapter.Environment = map[string]string{
		"OPENFOX_CODEX_FIXTURE_MODE": mode, "FAKE_LOG": log, "CODEX_HOME": source,
		"OPENFOX_TEST_TOKEN": "fixture-secret",
	}
	return fixture{adapter: adapter, log: log, source: source, root: adapter.HomeRoot}
}

func (f fixture) run(t *testing.T, request domainruntime.RunRequest) (domainruntime.Result, []domainruntime.Event) {
	t.Helper()
	if request.Prompt == "" {
		request.Prompt = "say OK"
	}
	if request.WorkingDirectory == "" {
		request.WorkingDirectory = t.TempDir()
	}
	if request.Timeout == 0 {
		request.Timeout = 20 * time.Second
	}
	execution, err := f.adapter.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var events []domainruntime.Event
	for event := range execution.Events() {
		events = append(events, event)
	}
	result, err := execution.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result, events
}

func (f fixture) requests(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func countEvents(events []domainruntime.Event, kind domainruntime.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == kind {
			count++
		}
	}
	return count
}

func TestAppServerRunSendsPoliciesAndUsesIsolatedHome(t *testing.T) {
	f := newFixture(t, "success")
	workdir := t.TempDir()
	result, events := f.run(t, domainruntime.RunRequest{Instructions: "be brief", Model: "fixture-model", WorkingDirectory: workdir})
	if result.Status != domainruntime.StatusSucceeded {
		t.Fatalf("result = %#v", result)
	}
	// 只有本线程的最终回复进入正文；commentary、命令、告警只作为过程事件；密钥被脱敏。
	if strings.TrimSpace(result.Output) != "OK token=[REDACTED]" {
		t.Fatalf("output = %q", result.Output)
	}
	if got := countEvents(events, domainruntime.EventWarning); got != 2 {
		t.Fatalf("warning events = %d, want 2 (config warning + retrying error)", got)
	}
	if countEvents(events, domainruntime.EventFailed) != 0 || countEvents(events, domainruntime.EventStarted) != 1 || countEvents(events, domainruntime.EventTool) != 2 {
		t.Fatalf("unexpected events: %#v", events)
	}
	requests := f.requests(t)
	for _, want := range []string{
		`"method":"initialize"`, `{"method":"initialized"}`, `"method":"thread/start"`, `"method":"turn/start"`,
		`"approvalPolicy":"never"`, `"sandbox":"workspace-write"`, `"ephemeral":true`,
		`"developerInstructions":"be brief"`, `"model":"fixture-model"`,
		`"type":"workspaceWrite"`, `"writableRoots":["` + workdir + `"]`, `"networkAccess":true`,
		`"text":"say OK"`,
	} {
		if !strings.Contains(requests, want) {
			t.Fatalf("requests missing %s:\n%s", want, requests)
		}
	}
	// 独立 CODEX_HOME：不是用户目录，只含凭据副本与生成的配置，Wait 返回时已删除。
	homeInfo, err := os.ReadFile(f.log + ".home")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(homeInfo))
	if len(lines) != 3 || lines[0] == f.source || !strings.HasPrefix(lines[0], f.root) || lines[1] != "auth.json" || lines[2] != "config.toml" {
		t.Fatalf("codex home = %q", homeInfo)
	}
	if _, err := os.Stat(lines[0]); !os.IsNotExist(err) {
		t.Fatalf("isolated codex home was not removed: %v", err)
	}
	// Codex 刷新的凭据写回用户目录，权限保持 0600；用户自己的 config.toml 不受影响。
	if auth, _ := os.ReadFile(filepath.Join(f.source, "auth.json")); strings.TrimSpace(string(auth)) != "refreshed" {
		t.Fatalf("source auth.json = %q", auth)
	}
	if info, err := os.Stat(filepath.Join(f.source, "auth.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("source auth.json mode = %v, %v", info, err)
	}
	if config, _ := os.ReadFile(filepath.Join(f.source, "config.toml")); string(config) != "model = \"personal\"\n" {
		t.Fatalf("source config.toml changed: %q", config)
	}
}

func TestAppServerFailureModes(t *testing.T) {
	previous := appServerHandshakeTimeout
	appServerHandshakeTimeout = 300 * time.Millisecond
	defer func() { appServerHandshakeTimeout = previous }()
	for _, tc := range []struct {
		mode, code, text string
	}{
		{"fatal", "RUNTIME_FAILED", "stream disconnected"},
		{"turn_failed", "RUNTIME_FAILED", "usage limit reached"},
		{"thread_error", "RUNTIME_FAILED", "model not available"},
		{"approval", "APPROVAL_REQUIRED", ""},
		{"early_exit", "PROTOCOL_ERROR", ""},
		{"silent", "PROTOCOL_ERROR", "handshake timed out"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFixture(t, tc.mode)
			result, _ := f.run(t, domainruntime.RunRequest{})
			if result.Status != domainruntime.StatusFailed || result.ErrorCode != tc.code || !strings.Contains(result.Error, tc.text) {
				t.Fatalf("result = %#v", result)
			}
			if result.Output != "" {
				t.Fatalf("failed run leaked output %q", result.Output)
			}
			entries, _ := os.ReadDir(f.root)
			if len(entries) != 0 {
				t.Fatalf("isolated codex homes left behind: %v", entries)
			}
			// 失败的 Run 不写回凭据。
			if auth, _ := os.ReadFile(filepath.Join(f.source, "auth.json")); string(auth) != "original" {
				t.Fatalf("source auth.json = %q", auth)
			}
		})
	}
}

func TestAppServerCancelInterruptsTurn(t *testing.T) {
	f := newFixture(t, "slow")
	execution, err := f.adapter.Start(context.Background(), domainruntime.RunRequest{Prompt: "slow task", WorkingDirectory: t.TempDir(), Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	events := execution.Events()
	for event := range events {
		if event.Type == domainruntime.EventStarted {
			break
		}
	}
	go func() {
		for range events {
		}
	}()
	started := time.Now()
	if err := execution.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := execution.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domainruntime.StatusCancelled {
		t.Fatalf("result = %#v", result)
	}
	// 协议层中断后 app-server 自行收尾，不需要等信号升级。
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	if !strings.Contains(f.requests(t), `"method":"turn/interrupt","params":{"threadId":"th-1","turnId":"tu-1"}`) {
		t.Fatalf("turn/interrupt not sent:\n%s", f.requests(t))
	}
}

func TestAppServerSucceedsWhenServerIgnoresStdinClose(t *testing.T) {
	f := newFixture(t, "noexit")
	started := time.Now()
	result, _ := f.run(t, domainruntime.RunRequest{})
	if result.Status != domainruntime.StatusSucceeded || strings.TrimSpace(result.Output) != "OK token=[REDACTED]" {
		t.Fatalf("result = %#v", result)
	}
	// 终态后等待 sessionExitGrace（5s）再结束进程，而不是等脚本 sleep 30。
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Fatalf("run took %s", elapsed)
	}
}

func TestStartRequiresLoginCredentialsFile(t *testing.T) {
	f := newFixture(t, "success")
	if err := os.Remove(filepath.Join(f.source, "auth.json")); err != nil {
		t.Fatal(err)
	}
	_, err := f.adapter.Start(context.Background(), domainruntime.RunRequest{Prompt: "x", WorkingDirectory: t.TempDir(), Timeout: time.Second})
	if !errors.Is(err, domainruntime.ErrUnavailable) || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("Start error = %v", err)
	}
}

func TestStartRejectsCallerCodexHome(t *testing.T) {
	f := newFixture(t, "success")
	_, err := f.adapter.Start(context.Background(), domainruntime.RunRequest{
		Prompt: "x", WorkingDirectory: t.TempDir(), Timeout: time.Second,
		Environment: map[string]string{"CODEX_HOME": "/tmp/other"}, EnvironmentAllowlist: []string{"CODEX_HOME"},
	})
	if !errors.Is(err, domainruntime.ErrInvalidRequest) {
		t.Fatalf("Start error = %v", err)
	}
}

func TestCodexNotLoggedInIsNotRunnable(t *testing.T) {
	f := newFixture(t, "logged_out")
	report, err := f.adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Login != domainruntime.LoginLoggedOut || report.Runnable {
		t.Fatalf("expected logged-out non-runnable report, got %#v", report)
	}
}

func TestDetectReportsAppServerCapability(t *testing.T) {
	f := newFixture(t, "success")
	report, err := f.adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Runnable || report.Login != domainruntime.LoginLoggedIn || !report.SupportsJSONEvents || !report.SupportsCancel || report.SupportsResume {
		t.Fatalf("unexpected capabilities: %#v", report)
	}
}

func TestInstalledCodexCapabilityProbe(t *testing.T) {
	report, err := New("").Detect(context.Background())
	if errors.Is(err, domainruntime.ErrUnavailable) {
		t.Skip("codex CLI is not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	// 未配置版本名单时不按版本拦截，是否可运行只取决于登录状态。
	if report.Version == "" || report.Executable == "" || !report.SupportsJSONEvents ||
		report.Runnable != (report.Login == domainruntime.LoginLoggedIn) {
		t.Fatalf("incomplete installed Codex report: %#v", report)
	}
}

func TestDetectVersionAllowListIsOptional(t *testing.T) {
	f := newFixture(t, "success")
	// 名单里没有当前版本：不可运行，并给出版本原因
	f.adapter.SupportedVersions = []string{"9.9.9"}
	report, err := f.adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Runnable || !strings.Contains(report.Reason, "supported version") {
		t.Fatalf("allow-list miss: %#v", report)
	}
	// 没有名单：已登录即可运行
	f.adapter.SupportedVersions = nil
	report, err = f.adapter.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Runnable || report.Reason != "" {
		t.Fatalf("no allow-list: %#v", report)
	}
}

func TestListModelsPagesAndSkipsHidden(t *testing.T) {
	f := newFixture(t, "models")
	models, err := f.adapter.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "gpt-a", DisplayName: "GPT A", IsDefault: true, ReasoningEfforts: []string{"DEFAULT", "HIGH", "EXTRA_HIGH"}}, {ID: "gpt-b", DisplayName: "gpt-b", ReasoningEfforts: []string{"DEFAULT"}}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v", models)
	}
	requests := f.requests(t)
	if !strings.Contains(requests, `"method":"model/list","params":{"includeHidden":false,"limit":100}`) || !strings.Contains(requests, `"cursor":"c2"`) {
		t.Fatalf("requests:\n%s", requests)
	}
	if strings.Contains(requests, "thread/start") || strings.Contains(requests, "turn/start") {
		t.Fatalf("model listing must not start a turn:\n%s", requests)
	}
	if entries, _ := os.ReadDir(f.root); len(entries) != 0 {
		t.Fatalf("isolated codex homes left behind: %v", entries)
	}
}

func TestListModelsReportsServerError(t *testing.T) {
	f := newFixture(t, "models_error")
	if _, err := f.adapter.ListModels(context.Background()); !errors.Is(err, domainruntime.ErrUnavailable) || !strings.Contains(err.Error(), "not signed in") {
		t.Fatalf("ListModels error = %v", err)
	}
}

func TestDefaultModelIsNotSentToCodex(t *testing.T) {
	for _, model := range []string{"default", " Default ", ""} {
		f := newFixture(t, "success")
		result, _ := f.run(t, domainruntime.RunRequest{Model: model})
		if result.Status != domainruntime.StatusSucceeded {
			t.Fatalf("model %q: result = %#v", model, result)
		}
		if strings.Contains(f.requests(t), `"model":`) {
			t.Fatalf("model %q was sent to codex:\n%s", model, f.requests(t))
		}
	}
}
