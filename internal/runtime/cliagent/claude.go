package cliagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

// Claude Code 适配。协议出处（2026-11 核对）：
//   - https://code.claude.com/docs/en/headless 与 /cli-reference：`-p`、`--output-format stream-json`（需要 --verbose）、
//     `--resume`、`--permission-mode`、`--allowedTools`、`--disallowedTools`、`--mcp-config`、`--append-system-prompt`、`claude auth status`
//   - https://code.claude.com/docs/en/agent-sdk/typescript：SDKSystemMessage(init, session_id)、SDKAssistantMessage、
//     SDKUserMessage(tool_result)、SDKResultMessage(subtype success / error_*, is_error, result, usage)
//
// 提示经 stdin 传入（`claude -p` 在无位置参数时读取 stdin）。
func claudeProtocol() protocol {
	return protocol{
		name: NameClaudeCode, display: "Claude Code",
		executables: []string{"claude"}, envPath: "OPENFOX_CLAUDE_PATH",
		wellKnown:   []string{".claude/local", ".local/bin", ".npm-global/bin", ".bun/bin", ".volta/bin"},
		envPrefixes: []string{"ANTHROPIC_", "CLAUDE_CODE_", "CLAUDE_CONFIG_DIR", "AWS_", "CLOUD_ML_", "GOOGLE_", "VERTEX_"},
		versionArgs: []string{"--version"},
		login:       claudeLogin,
		defaultModels: []Model{
			{ID: "default", DisplayName: "Default", IsDefault: true},
			{ID: "sonnet", DisplayName: "Sonnet"},
			{ID: "opus", DisplayName: "Opus"},
			{ID: "haiku", DisplayName: "Haiku"},
		},
		build: buildClaude, parse: parseClaude, supportsTools: true,
		approvalModes: []string{runtimepolicy.ReadOnly, runtimepolicy.WorkspaceWrite, runtimepolicy.FullAccess},
		// DEFAULT 即不是 plan（权限档位本身已显式传 --permission-mode）；PLAN 用 --permission-mode plan。
		collaborationModes: []string{runtimepolicy.CollaborationDefault, runtimepolicy.CollaborationPlan},
	}
}

// claudeLogin 用 `claude auth status`（已登录退出码 0，未登录 1；输出 JSON，含 loggedIn）判断。
// 只有输出明确表示未登录才返回 LoggedOut；命令不支持或超时一律 Unknown，避免误拦截。
func claudeLogin(ctx context.Context, a *Adapter, path string, env map[string]string) runtime.LoginStatus {
	output, err := runProbe(ctx, path, env, "auth", "status")
	var status struct {
		LoggedIn *bool `json:"loggedIn"`
	}
	if start := strings.Index(output, "{"); start >= 0 {
		if json.Unmarshal([]byte(output[start:]), &status) == nil && status.LoggedIn != nil {
			if *status.LoggedIn {
				return runtime.LoginLoggedIn
			}
			return runtime.LoginLoggedOut
		}
	}
	lower := strings.ToLower(output)
	if strings.Contains(lower, "not logged in") || strings.Contains(lower, "not authenticated") {
		return runtime.LoginLoggedOut
	}
	if err == nil {
		return runtime.LoginLoggedIn
	}
	return runtime.LoginUnknown
}

// claudeAllowedTools 是 -p 模式下免确认的工具（非交互无法回答确认；未列出的工具会被拒绝）。
const claudeAllowedTools = "Bash,Read,Edit,Write,MultiEdit,NotebookEdit,Glob,Grep,WebFetch,WebSearch,TodoWrite,Task"

// claudeReadOnlyTools / claudeWriteTools 是 READ_ONLY 档位的工具范围：只放行读与查询类工具，
// 并用 --disallowedTools 明确禁止会写文件或执行命令的工具（plan 模式在 -p 非交互运行中同样生效）。
const (
	claudeReadOnlyTools = "Read,Glob,Grep,WebFetch,WebSearch,TodoWrite"
	claudeWriteTools    = "Bash,Edit,Write,MultiEdit,NotebookEdit"
)

// claudePermission 返回各权限档位对应的 --permission-mode、--allowedTools 基础列表与 --disallowedTools（可空）。
// 档位含义与出处见 contracts/shared/runtime-adapter.md；FOLLOW_RUNTIME 与 WORKSPACE_WRITE 的参数相同
// （acceptEdits：自动接受工作目录内的编辑），FOLLOW_RUNTIME 必须与改动前逐字一致。
func claudePermission(mode string) (permission, allowed, disallowed string) {
	switch mode {
	case runtimepolicy.ReadOnly:
		return "plan", claudeReadOnlyTools, claudeWriteTools
	case runtimepolicy.FullAccess:
		return "bypassPermissions", claudeAllowedTools, ""
	default:
		return "acceptEdits", claudeAllowedTools, ""
	}
}

// claudeModeFlags 合并权限档位与协作模式：
//   - PLAN：--permission-mode plan，工具范围与 READ_ONLY 完全一致（只放行读与查询类工具、明确禁止写文件与执行命令的工具），
//     与任何权限档位同时设置时都以 PLAN 为准（包括 FULL_ACCESS，不报错）；
//   - DEFAULT / FOLLOW_RUNTIME：不改变权限档位的映射。Claude 的权限档位本身就通过 --permission-mode 显式传入，
//     而这些取值（acceptEdits / bypassPermissions）都不是 plan，因此 DEFAULT 与 FOLLOW_RUNTIME 的参数相同；
//     唯一例外是 READ_ONLY 档位本来就是 plan，DEFAULT 不会把它放宽成可写。
func claudeModeFlags(approvalMode, collaborationMode string) (permission, allowed, disallowed string) {
	if runtimepolicy.NormalizeCollaborationMode(collaborationMode) == runtimepolicy.CollaborationPlan {
		return claudePermission(runtimepolicy.ReadOnly)
	}
	return claudePermission(approvalMode)
}

func buildClaude(a *Adapter, req runtime.RunRequest, bridge *bridgeInfo, stateDir string, _ *session) (invocation, error) {
	permission, allowed, disallowed := claudeModeFlags(req.ApprovalMode, req.CollaborationMode)
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--permission-mode", permission}
	if model := normalizeModel(req.Model); model != "" {
		args = append(args, "--model", model)
	}
	if req.Instructions != "" {
		args = append(args, "--append-system-prompt", req.Instructions)
	}
	if req.ResumeSession != "" {
		args = append(args, "--resume", req.ResumeSession)
	}
	if req.ArtifactDirectory != "" && permission != "plan" {
		// 电脑端为本次 Run 新建的产物目录（工作目录之外）。--add-dir 只是允许访问该目录，
		// 写工具是否放行仍由权限档位决定；plan（READ_ONLY / PLAN）不加，保持只读档位与改动前一致。
		args = append(args, "--add-dir", req.ArtifactDirectory)
	}
	args = append(args, "--strict-mcp-config")
	servers := map[string]any{}
	var names []string
	for n, server := range req.MCPServers {
		servers[n] = server
		names = append(names, n)
	}
	if bridge != nil {
		servers[bridgeServerName] = map[string]any{"type": "http", "url": bridge.url}
		names = append(names, bridgeServerName)
	}
	if len(servers) > 0 {
		config, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return invocation{}, err
		}
		path := filepath.Join(stateDir, "mcp.json")
		if err = os.WriteFile(path, config, 0600); err != nil {
			return invocation{}, err
		}
		args = append(args, "--mcp-config", path)
		sort.Strings(names)
		for _, name := range names {
			allowed += ",mcp__" + name
		}
	}
	args = append(args, "--allowedTools", allowed)
	if disallowed != "" {
		args = append(args, "--disallowedTools", disallowed)
	}
	return invocation{args: args, stdin: []byte(req.Prompt), env: map[string]string{"ENABLE_CLAUDEAI_MCP_SERVERS": "false"}}, nil
}

type claudeMessage struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Error     string `json:"error"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		Input      int64 `json:"input_tokens"`
		Output     int64 `json:"output_tokens"`
		CacheRead  int64 `json:"cache_read_input_tokens"`
		CacheWrite int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     map[string]any  `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func parseClaude(s *session, line []byte) ([]runtime.Event, bool) {
	var message claudeMessage
	if json.Unmarshal(line, &message) != nil {
		return nil, false
	}
	var events []runtime.Event
	switch message.Type {
	case "system":
		switch message.Subtype {
		case "init":
			events = append(events, s.setSession(message.SessionID)...)
		case "api_retry":
			text := "API request failed, retrying"
			if message.Error != "" {
				text += ": " + message.Error
			}
			events = append(events, runtime.Event{Type: runtime.EventWarning, Text: s.redact(text)})
		}
	case "assistant":
		if message.Error != "" {
			s.lastError = message.Error
		}
		for _, block := range claudeBlocks(message.Message.Content) {
			switch block.Type {
			case "text":
				if strings.TrimSpace(block.Text) != "" {
					s.pendingText.WriteString(block.Text)
				}
			case "thinking":
				events = append(events, s.flushPending()...)
				events = append(events, s.textStep(runtime.StepKindThinking, block.Thinking)...)
			case "tool_use":
				// 工具调用之前的文本是中间说明。
				events = append(events, s.flushPending()...)
				events = append(events, s.startStep(block.ID, s.toolStep(block.Name, block.Input))...)
			}
		}
	case "user":
		for _, block := range claudeBlocks(message.Message.Content) {
			if block.Type != "tool_result" {
				continue
			}
			state := runtime.StepDone
			if block.IsError {
				state = runtime.StepFailed
			}
			events = append(events, s.finishStep(block.ToolUseID, runtime.Step{Kind: runtime.StepKindExec}, state, claudeResultText(block.Content), nil)...)
		}
	case "result":
		s.setSessionFromResult(message.SessionID)
		s.addUsage(message.Usage.Input, message.Usage.CacheRead, message.Usage.CacheWrite, message.Usage.Output, 0)
		s.pendingText.Reset()
		if message.Subtype == "success" && !message.IsError {
			return append(events, s.complete(message.Result)...), true
		}
		text := strings.TrimSpace(message.Result)
		if text == "" {
			text = s.lastError
		}
		if text == "" {
			text = "Claude Code ended with " + message.Subtype
		}
		return append(events, s.fail(text)...), true
	}
	return events, false
}

func (s *session) setSessionFromResult(id string) {
	if s.sessionID == "" && strings.TrimSpace(id) != "" && len(id) <= 256 {
		s.sessionID = strings.TrimSpace(id)
	}
}

func claudeBlocks(raw json.RawMessage) []claudeBlock {
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// claudeResultText 取 tool_result 的文本：字符串或 [{type:text,text}] 数组。
func claudeResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var out []string
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			out = append(out, part.Text)
		}
	}
	return strings.Join(out, "\n")
}

// normalizeModel：空或 "default"（不区分大小写）表示不指定模型。
func normalizeModel(model string) string {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "default") || strings.HasPrefix(model, "-") {
		return ""
	}
	return model
}
