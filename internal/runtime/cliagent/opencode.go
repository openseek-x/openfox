package cliagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"openfox/internal/runtime"
	"openfox/internal/runtime/mcpconfig"
	"openfox/internal/runtimepolicy"
)

// OpenCode 适配。协议出处（2026-11 核对）：
//   - https://opencode.ai/docs/cli/ ：`opencode run`、`--format json`、`--session`、`--model provider/model`、`opencode models`
//   - 官方源码 packages/opencode/src/cli/cmd/run.ts（anomalyco/opencode，原 sst/opencode）：
//     JSON 模式每行 {type, timestamp, sessionID, part}，type 为 step_start / step_finish / text / reasoning / tool_use / error；
//     text 与 reasoning 在 part.time.end 后才发出（整段，不是增量）；tool_use 在调用结束（completed / error）后一次性发出；
//     没有显式的终态事件：进程在会话 idle 后退出，权限请求在非 --auto 时自动拒绝。
//   - 环境变量 OPENCODE_PERMISSION（内联权限 JSON）、OPENCODE_CONFIG_CONTENT（内联配置，用于注入 remote MCP）。
//
// 提示作为 `--` 之后的位置参数传入（上限 maxPromptViaArgs）。
func openCodeProtocol() protocol {
	return protocol{
		name: NameOpenCode, display: "OpenCode",
		executables: []string{"opencode"}, envPath: "OPENFOX_OPENCODE_PATH",
		wellKnown:   []string{".opencode/bin", ".local/bin", ".npm-global/bin", ".bun/bin", ".volta/bin"},
		envPrefixes: []string{"OPENCODE_"},
		envSuffixes: []string{"_API_KEY", "_BASE_URL", "_ACCESS_KEY_ID", "_SECRET_ACCESS_KEY", "_AUTH_TOKEN"},
		versionArgs: []string{"--version"},
		listModels:  openCodeModels,
		defaultModels: []Model{
			{ID: "default", DisplayName: "Default", IsDefault: true},
		},
		build: buildOpenCode, parse: parseOpenCode, finalize: finalizeOpenCode, supportsTools: true,
		stderrSummary: openCodeDiagnostic,
		approvalModes: []string{runtimepolicy.ReadOnly, runtimepolicy.WorkspaceWrite, runtimepolicy.FullAccess},
		// 现有调用方式是 `opencode run --format json [--model] [--session] -- <prompt>`，仓库代码与已有记录
		// （stage7f / 8d-1 引用的 opencode.ai/docs/cli、/docs/agents 的 Permissions、run.ts 事件格式）都没有确认
		// 选择内置 plan / build agent 的参数（例如 --agent），因此只支持 FOLLOW_RUNTIME（collaborationModes 为空），
		// DEFAULT / PLAN 在 Start 里失败，不会忽略后照常执行。
	}
}

var openCodeErrorField = regexp.MustCompile(`(?:^|\s)error\.error=("(?:\\.|[^"\\])*")`)

func openCodeDiagnostic(line string) string {
	if match := openCodeErrorField.FindStringSubmatch(line); len(match) == 2 {
		if message, err := strconv.Unquote(match[1]); err == nil {
			return message
		}
	}
	return line
}

// openCodeModels 解析 `opencode models` 的输出：每行 provider/model。
func openCodeModels(ctx context.Context, a *Adapter, path string, env map[string]string) ([]Model, error) {
	output, err := runProbe(ctx, path, env, "models")
	if err != nil {
		return nil, err
	}
	var models []Model
	for _, line := range strings.Split(output, "\n") {
		id := strings.TrimSpace(line)
		if id == "" || strings.ContainsAny(id, " \t") || !strings.Contains(id, "/") || strings.HasPrefix(id, "-") {
			continue
		}
		models = append(models, Model{ID: id, DisplayName: id})
	}
	return models, nil
}

// openCodePermission 返回各权限档位的 OPENCODE_PERMISSION（权限键含义见 https://opencode.ai/docs/agents 的 Permissions）。
// FOLLOW_RUNTIME 必须与改动前逐字一致；external_directory 控制「工作目录之外的文件读写」。
// 限制：OpenCode 没有目录级的 bash 沙箱，WORKSPACE_WRITE 只能靠 external_directory 拒绝工作目录之外的文件访问。
func openCodePermission(mode string) string {
	switch mode {
	case runtimepolicy.ReadOnly:
		return `{"edit":"deny","bash":"deny","webfetch":"allow","external_directory":"deny"}`
	case runtimepolicy.WorkspaceWrite:
		return `{"edit":"allow","bash":"allow","webfetch":"allow","external_directory":"deny"}`
	case runtimepolicy.FullAccess:
		return `{"edit":"allow","bash":"allow","webfetch":"allow","external_directory":"allow"}`
	default:
		return `{"edit":"allow","bash":"allow","webfetch":"allow"}`
	}
}

func buildOpenCode(a *Adapter, req runtime.RunRequest, bridge *bridgeInfo, stateDir string, _ *session) (invocation, error) {
	prompt := req.Prompt
	if req.Instructions != "" {
		prompt = req.Instructions + "\n\n" + prompt
	}
	if len(prompt) > maxPromptViaArgs {
		return invocation{}, fmtInvalid("prompt is too large for OpenCode (limit %d bytes)", maxPromptViaArgs)
	}
	// JSON stdout does not include provider retry failures. Keep ERROR diagnostics
	// on stderr so a bounded timeout can explain the underlying provider failure.
	args := []string{"run", "--format", "json", "--print-logs", "--log-level", "ERROR"}
	if model := normalizeModel(req.Model); model != "" {
		args = append(args, "--model", model)
	}
	if req.ResumeSession != "" {
		args = append(args, "--session", req.ResumeSession)
	}
	args = append(args, "--", prompt)
	// 非交互运行无法回答权限请求（OpenCode 会自动拒绝），因此显式放行常用工具，与 Codex 的 workspace-write 对应。
	env := map[string]string{"OPENCODE_PERMISSION": openCodePermission(req.ApprovalMode)}
	// OpenCode merges environment, project, home and global configs. Isolate
	// each source to keep unbound servers out of the CLI, including default runs.
	servers := map[string]any{}
	for name, entry := range req.MCPServers {
		servers[name] = entry
	}
	if bridge != nil {
		servers[bridgeServerName] = map[string]any{"type": "remote", "url": bridge.url, "enabled": true}
	}
	env["OPENCODE_DISABLE_PROJECT_CONFIG"] = "1"
	env["OPENCODE_DISABLE_DEFAULT_PLUGINS"] = "1"
	env["OPENCODE_DISABLE_CLAUDE_CODE"] = "1"
	env["OPENCODE_CONFIG_DIR"] = stateDir
	env["OPENCODE_TEST_HOME"] = stateDir
	env["OPENCODE_TEST_MANAGED_CONFIG_DIR"] = stateDir
	env["XDG_CONFIG_HOME"] = stateDir
	env["OPENCODE_CONFIG"] = ""
	env["OPENCODE_CONFIG_CONTENT"] = "{}"
	config := map[string]any{}
	if stateDir != "" {
		base := a.baseEnv("")
		if err := isolateOpenCodeAuth(base, stateDir, env); err != nil {
			return invocation{}, err
		}
		modelConfig, err := mcpconfig.OpenCodeModelSettings(mcpconfig.Location{Home: base["HOME"], Env: func(key string) string { return base[key] }})
		if err != nil {
			return invocation{}, err
		}
		config = modelConfig
		// The OpenFox model session store is shared only across this daemon's
		// runs. An active console account in the personal DB can load remote MCP.
		sessionDir, err := a.sessionStoreDir()
		if err != nil {
			return invocation{}, err
		}
		env["OPENCODE_DB"] = filepath.Join(sessionDir, "opencode.db")
	}
	if len(servers) > 0 {
		config["mcp"] = servers
	}
	if len(config) > 0 {
		encoded, err := json.Marshal(config)
		if err != nil {
			return invocation{}, err
		}
		path := filepath.Join(stateDir, "openfox-config.json")
		if err = os.WriteFile(path, encoded, 0600); err != nil {
			return invocation{}, err
		}
		env["OPENCODE_CONFIG"] = path
	}
	return invocation{args: args, env: env}, nil
}

type openCodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Text   string `json:"text"`
		Tool   string `json:"tool"`
		CallID string `json:"callID"`
		Reason string `json:"reason"`
		State  struct {
			Status   string         `json:"status"`
			Input    map[string]any `json:"input"`
			Output   any            `json:"output"`
			Error    any            `json:"error"`
			Metadata struct {
				Exit *int `json:"exit"`
			} `json:"metadata"`
		} `json:"state"`
		Tokens struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

func parseOpenCode(s *session, line []byte) ([]runtime.Event, bool) {
	var event openCodeEvent
	if json.Unmarshal(line, &event) != nil {
		return nil, false
	}
	events := s.setSession(event.SessionID)
	switch event.Type {
	case "text":
		s.pendingText.WriteString(event.Part.Text)
	case "reasoning":
		events = append(events, s.flushPending()...)
		events = append(events, s.textStep(runtime.StepKindThinking, event.Part.Text)...)
	case "tool_use":
		events = append(events, s.flushPending()...)
		id := event.Part.CallID
		if id == "" {
			id = event.Part.ID
		}
		step := s.toolStep(event.Part.Tool, event.Part.State.Input)
		state, output := runtime.StepDone, anyText(event.Part.State.Output)
		if event.Part.State.Status == "error" {
			state, output = runtime.StepFailed, anyText(event.Part.State.Error)
		} else if exit := event.Part.State.Metadata.Exit; exit != nil && *exit != 0 && step.Kind == runtime.StepKindExec {
			state = runtime.StepFailed
		}
		events = append(events, s.finishStep(id, step, state, output, event.Part.State.Metadata.Exit)...)
	case "step_finish":
		t := event.Part.Tokens
		s.addUsage(t.Input, t.Cache.Read, t.Cache.Write, t.Output, t.Reasoning)
		// reason 为 tool-calls 表示后面还有一步；其他（stop / length 等）视为这一轮结束。
		if event.Part.Reason == "tool-calls" || event.Part.Reason == "tool_calls" {
			events = append(events, s.flushPending()...)
			break
		}
		s.lastError = ""
		return append(events, s.complete(s.takePending())...), true
	case "error":
		message := event.Error.Data.Message
		if message == "" {
			message = event.Error.Name
		}
		if message == "" {
			message = "OpenCode reported an error"
		}
		s.lastError = message
		events = append(events, runtime.Event{Type: runtime.EventWarning, Text: s.redact(message)})
	}
	return events, false
}

// finalizeOpenCode：进程退出而没有 step_finish（官方已知会丢失最后一个 step_finish）时，
// 有错误按失败处理；有文本回复按成功处理；什么都没有交给进程层判定协议错误。
func finalizeOpenCode(s *session) []runtime.Event {
	if s.lastError != "" {
		return s.fail(s.lastError)
	}
	if text := s.takePending(); text != "" {
		return s.complete(text)
	}
	return nil
}

// anyText 把 state.output / state.error（字符串或对象）转为文本。
func anyText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}
