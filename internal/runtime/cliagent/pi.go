package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"openfox/internal/runtime"
)

// pi（badlogic/pi-mono，@mariozechner/pi-coding-agent）适配。协议出处（2026-11 核对）：
//   - packages/coding-agent/docs/json.md：`pi --mode json`，首行 session 头 {"type":"session","id",...}，
//     随后 agent_start / message_start / message_update / message_end（权威最终消息）/ turn_end /
//     tool_execution_start|end(toolCallId, toolName, args, result, isError) / agent_end / agent_settled(aborted)
//   - docs/message-types.md：AssistantMessage.stopReason = stop | toolUse | error | aborted ...，content 为 text / thinking / toolCall
//   - docs/cli.md：`--model provider/id`、`--session <path|id>`、`--list-models`、管道 stdin 会前置到首个提示
//
// 提示经 stdin 传入。pi 没有内置 MCP 客户端的命令行注入方式（MCP 由扩展 / `pi mcp add` 持久配置），
// 因此不暴露 OpenFox 任务工具。
func piProtocol() protocol {
	return protocol{
		name: NamePi, display: "pi",
		executables: []string{"pi"}, envPath: "OPENFOX_PI_PATH",
		wellKnown:   []string{".local/bin", ".npm-global/bin", ".bun/bin", ".volta/bin"},
		envPrefixes: []string{"PI_"},
		envSuffixes: []string{"_API_KEY", "_BASE_URL", "_ACCESS_KEY_ID", "_SECRET_ACCESS_KEY", "_AUTH_TOKEN"},
		versionArgs: []string{"--version"},
		listModels:  piModels,
		defaultModels: []Model{
			{ID: "default", DisplayName: "Default", IsDefault: true},
		},
		build: buildPi, parse: parsePi, finalize: finalizePi,
		// 仓库代码与已有记录（stage7f 引用的 pi-mono 文档 json.md / cli.md）都没有计划或协作模式参数，
		// 因此只支持 FOLLOW_RUNTIME（collaborationModes 为空），DEFAULT / PLAN 在 Start 里失败。
	}
}

// piModels 解析 `pi --list-models` 的表格输出：前两列为 provider 与 model，首行表头跳过。
func piModels(ctx context.Context, a *Adapter, path string, env map[string]string) ([]Model, error) {
	output, err := runProbe(ctx, path, env, "--list-models")
	if err != nil {
		return nil, err
	}
	var models []Model
	columns := 0
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "provider") && strings.EqualFold(fields[1], "model") {
			columns = len(fields)
			continue
		}
		// With no configured provider, pi prints login guidance and exits 0.
		// Only rows beneath the model-table header are model declarations.
		if columns == 0 || len(fields) != columns || strings.HasPrefix(fields[0], "-") {
			continue
		}
		id := fields[0] + "/" + fields[1]
		models = append(models, Model{ID: id, DisplayName: id})
	}
	return models, nil
}

func buildPi(a *Adapter, req runtime.RunRequest, _ *bridgeInfo, stateDir string, _ *session) (invocation, error) {
	prompt := req.Prompt
	if req.Instructions != "" {
		prompt = req.Instructions + "\n\n" + prompt
	}
	args := []string{"--mode", "json"}
	if len(req.Skills) > 0 {
		if err := runtime.WriteSkills(stateDir, req.Skills); err != nil {
			return invocation{}, err
		}
		for _, skill := range req.Skills {
			args = append(args, "--skill", filepath.Join(stateDir, "skills", skill.Name, "SKILL.md"))
		}
	}
	if model := normalizeModel(req.Model); model != "" {
		args = append(args, "--model", model)
	}
	if req.ResumeSession != "" {
		args = append(args, "--session", req.ResumeSession)
	}
	// 提示经 stdin：pi 把管道内容前置到首个提示；没有位置参数时即为完整提示。
	if len(req.MCPServers) == 0 {
		return invocation{args: append(args, "--no-mcp"), stdin: []byte(prompt)}, nil
	}
	args = append(args, "--no-approve")
	// Keep model-provider auth and model definitions. Project trust, personal
	// extensions, and the global MCP list are deliberately scoped out.
	base := a.baseEnv("")
	source := base["PI_CODING_AGENT_DIR"]
	if source == "" {
		source = filepath.Join(base["HOME"], ".pi", "agent")
	}
	// Matches Pi's native getDefaultSessionDirPath encoding, including
	// pre-existing sessions for this same workspace.
	cwd := strings.TrimLeft(req.WorkingDirectory, "/\\")
	safeCWD := strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(cwd)
	args = append(args, "--session-dir", filepath.Join(source, "sessions", "--"+safeCWD+"--"))
	for _, name := range []string{"auth.json", "models.json"} {
		if err := copyProviderFile(filepath.Join(source, name), filepath.Join(stateDir, name)); err != nil {
			return invocation{}, err
		}
	}
	config, err := json.Marshal(map[string]any{"mcpServers": req.MCPServers})
	if err != nil {
		return invocation{}, err
	}
	if err = os.WriteFile(filepath.Join(stateDir, "mcp.json"), config, 0600); err != nil {
		return invocation{}, err
	}
	return invocation{args: args, stdin: []byte(prompt), env: map[string]string{"PI_CODING_AGENT_DIR": stateDir}}, nil
}

func fmtInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, fmt.Sprintf(format, args...))
}

type piEvent struct {
	Type       string         `json:"type"`
	ID         string         `json:"id"`
	Aborted    bool           `json:"aborted"`
	WillRetry  bool           `json:"willRetry"`
	ToolCallID string         `json:"toolCallId"`
	ToolName   string         `json:"toolName"`
	Args       map[string]any `json:"args"`
	IsError    bool           `json:"isError"`
	Result     struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Message struct {
		Role         string          `json:"role"`
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
		Content      json.RawMessage `json:"content"`
		Usage        struct {
			Input      int64 `json:"input"`
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
			Reasoning  int64 `json:"reasoning"`
		} `json:"usage"`
	} `json:"message"`
	FinalError string `json:"finalError"`
}

func parsePi(s *session, line []byte) ([]runtime.Event, bool) {
	var event piEvent
	if json.Unmarshal(line, &event) != nil {
		return nil, false
	}
	var events []runtime.Event
	switch event.Type {
	case "session":
		events = append(events, s.setSession(event.ID)...)
	case "tool_execution_start":
		events = append(events, s.flushPending()...)
		events = append(events, s.startStep(event.ToolCallID, s.toolStep(event.ToolName, event.Args))...)
	case "tool_execution_end":
		state := runtime.StepDone
		if event.IsError {
			state = runtime.StepFailed
		}
		var parts []string
		for _, content := range event.Result.Content {
			if content.Type == "text" && content.Text != "" {
				parts = append(parts, content.Text)
			}
		}
		events = append(events, s.finishStep(event.ToolCallID, runtime.Step{Kind: runtime.StepKindExec, Command: event.ToolName}, state, strings.Join(parts, "\n"), nil)...)
	case "message_end":
		if event.Message.Role != "assistant" {
			break
		}
		u := event.Message.Usage
		s.addUsage(u.Input, u.CacheRead, u.CacheWrite, u.Output, u.Reasoning)
		var blocks []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		_ = json.Unmarshal(event.Message.Content, &blocks)
		for _, block := range blocks {
			switch block.Type {
			case "thinking":
				events = append(events, s.textStep(runtime.StepKindThinking, block.Thinking)...)
			case "text":
				s.pendingText.WriteString(block.Text)
			}
		}
		switch event.Message.StopReason {
		case "error":
			s.lastError = event.Message.ErrorMessage
			if s.lastError == "" {
				s.lastError = "pi reported a model error"
			}
			s.pendingText.Reset()
		case "toolUse":
			events = append(events, s.flushPending()...)
		case "stop":
			// 自动重试成功后，之前的错误作废。
			s.lastError = ""
		}
	case "auto_retry_start":
		events = append(events, runtime.Event{Type: runtime.EventWarning, Text: s.redact("pi is retrying after an error")})
	case "auto_retry_end":
		if event.FinalError != "" {
			s.lastError = event.FinalError
		}
	// agent_end 之后重试或压缩恢复可能继续；终态以 agent_settled 为准。
	case "agent_settled":
		return piTerminal(s, event.Aborted), true
	}
	return events, false
}

// piTerminal 根据最后一条助手消息给出终态；aborted 视为取消后的失败（取消本身由进程层判定为 CANCELLED）。
func piTerminal(s *session, aborted bool) []runtime.Event {
	switch {
	case aborted:
		return s.fail("pi run was aborted")
	case s.lastError != "":
		return s.fail(s.lastError)
	default:
		return s.complete(s.takePending())
	}
}

// finalizePi：进程退出而没有 agent_settled 时，有错误按失败处理，有回复文本按成功处理。
func finalizePi(s *session) []runtime.Event {
	if s.lastError != "" {
		return s.fail(s.lastError)
	}
	if text := s.takePending(); text != "" {
		return s.complete(text)
	}
	return nil
}
