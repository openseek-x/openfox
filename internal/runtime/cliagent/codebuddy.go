package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"openfox/internal/runtime"
	"openfox/internal/runtime/toolbridge"
	"openfox/internal/runtimepolicy"
)

// Fixed official 2.184.0 native protocol, verified with a local synthetic model.
// https://www.codebuddy.cn/docs/cli/{cli-reference,env-vars,permission-modes,stream-json-turn-usage}
// The model-file isolation switch is verified in that release's implementation;
// other versions are deliberately unavailable until their boundary is verified.
const codeBuddyVersion = "2.184.0"

const codeBuddyReadTools = "Read,Glob,Grep"
const codeBuddyDeniedTools = "Write,Edit,Bash,PowerShell,Agent,Workflow,ExitPlanMode,Skill,SkillManage,NotebookEdit"

func codeBuddyProtocol() protocol {
	return protocol{
		name: NameCodeBuddy, display: "CodeBuddy", executables: []string{"codebuddy"},
		envPath: "OPENFOX_CODEBUDDY_PATH", envPrefixes: []string{"CODEBUDDY_"},
		versionArgs: []string{"--version"}, probe: probeCodeBuddy,
		defaultModels: []Model{{ID: "default", DisplayName: "Configured CODEBUDDY_MODEL", IsDefault: true}},
		listModels: func(context.Context, *Adapter, string, map[string]string) ([]Model, error) {
			return nil, fmt.Errorf("CodeBuddy requires an explicit model; no authenticated model catalog is probed")
		},
		build: buildCodeBuddy, parse: parseCodeBuddy,
		approvalModes:      []string{runtimepolicy.ReadOnly},
		collaborationModes: []string{runtimepolicy.CollaborationDefault, runtimepolicy.CollaborationPlan},
	}
}

func codeBuddyIsolation(root string) (map[string]string, error) {
	env := map[string]string{
		"HOME": filepath.Join(root, "home"), "CODEBUDDY_CONFIG_DIR": filepath.Join(root, "config"),
		"TMPDIR": filepath.Join(root, "tmp"), "XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME": filepath.Join(root, "xdg-data"), "XDG_CACHE_HOME": filepath.Join(root, "xdg-cache"),
		"XDG_STATE_HOME":                       filepath.Join(root, "xdg-state"),
		"CODEBUDDY_DISABLE_CUSTOM_MODELS_FILE": "1", "CODEBUDDY_CODE_DISABLE_BACKGROUND_TASKS": "1",
		"CODEBUDDY_DISABLE_SHELL_SNAPSHOT": "1", "DISABLE_TELEMETRY": "1", "CI": "1", "NO_COLOR": "1",
	}
	for _, key := range []string{"HOME", "CODEBUDDY_CONFIG_DIR", "TMPDIR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if err := os.MkdirAll(env[key], 0700); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(filepath.Join(env["CODEBUDDY_CONFIG_DIR"], "settings.json"), []byte(`{"autoUpdates":false,"disableAllHooks":true,"promptSuggestionEnabled":false,"permissions":{"defaultMode":"dontAsk"},"trustAll":true}`), 0600); err != nil {
		return nil, err
	}
	return env, nil
}

func probeCodeBuddy(ctx context.Context, a *Adapter, path string) (string, error) {
	if goruntime.GOOS != "darwin" || goruntime.GOARCH != "arm64" {
		return "", fmt.Errorf("CodeBuddy requires macOS arm64")
	}
	root, err := a.makeStateDir()
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(root)
	env, err := codeBuddyIsolation(root)
	if err != nil {
		return "", err
	}
	// Version/help never receive model credentials or native configuration from HOME.
	env["PATH"] = os.Getenv("PATH")
	version, err := runProbe(ctx, path, env, "--version")
	if err != nil || versionPattern.FindString(version) != codeBuddyVersion {
		return version, err
	}
	help, err := runProbe(ctx, path, env, "--help")
	if err != nil {
		return "", err
	}
	for _, flag := range []string{"--setting-sources", "--strict-mcp-config", "--tools", "--permission-mode", "--output-format", "--settings"} {
		if !strings.Contains(help, flag) {
			return "", fmt.Errorf("CodeBuddy required CLI option is missing")
		}
	}
	return version, nil
}

// ValidateRequest is a side-effect-free preflight used by the supervisor before
// MCP, skills or workspace preparation. Start repeats it for direct callers.
func (a *Adapter) ValidateRequest(req runtime.RunRequest) error {
	if a != nil && a.Name() == NameGemini {
		return validateGeminiRequest(a, req)
	}
	if a == nil || a.Name() != NameCodeBuddy {
		return nil
	}
	if err := runtimepolicy.ValidateCodeBuddyModes(req.ApprovalMode, req.CollaborationMode, req.ReasoningEffort, req.Args); err != nil {
		return fmt.Errorf("%w: %s", runtime.ErrUnsupported, err)
	}
	if len(req.MCPServers) != 0 || len(req.Skills) != 0 || len(req.Tools) != 0 || req.ToolCall != nil || req.ResumeSession != "" {
		return fmt.Errorf("%w: CodeBuddy MCP, skills, native tools and resume are not supported", runtime.ErrUnsupported)
	}
	env := a.baseEnv("")
	for name, value := range req.Environment {
		if runtimepolicy.ReservedEnvironment(name) {
			return fmt.Errorf("%w: CodeBuddy environment variable %s is reserved", runtime.ErrInvalidRequest, name)
		}
		env[name] = value
	}
	_, err := runtimepolicy.ResolveCodeBuddyModel(req.Model, env)
	if err != nil {
		return fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, err)
	}
	client, err := codeBuddyModelClient(codeBuddyCAEnvironment(env, req.WorkingDirectory))
	if err != nil {
		return fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, err)
	}
	client.CloseIdleConnections()
	return nil
}

func mergeCodeBuddyEnvironment(base, overrides map[string]string) map[string]string {
	env := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		env[key] = value
	}
	for key, value := range overrides {
		env[key] = value
	}
	return env
}

func buildCodeBuddy(a *Adapter, req runtime.RunRequest, _ *toolbridge.Server, root string, _ *session) (invocation, error) {
	env := a.baseEnv("")
	for key, value := range req.Environment {
		env[key] = value
	}
	model, err := runtimepolicy.ResolveCodeBuddyModel(req.Model, env)
	if err != nil {
		return invocation{}, fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, err)
	}
	isolation, err := codeBuddyIsolation(root)
	if err != nil {
		return invocation{}, err
	}
	mode := "dontAsk"
	if runtimepolicy.NormalizeCollaborationMode(req.CollaborationMode) == runtimepolicy.CollaborationPlan {
		mode = "plan"
	}
	args := []string{"-p", "--output-format", "stream-json", "--permission-mode", mode,
		"--setting-sources", "user", "--settings", `{"disableAllHooks":true,"autoUpdates":false}`,
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--model", model,
		"--tools", codeBuddyReadTools, "--allowedTools", codeBuddyReadTools, "--disallowedTools", codeBuddyDeniedTools}
	prompt := req.Prompt
	if req.Instructions != "" {
		prompt = req.Instructions + "\n\n" + prompt
	}
	return invocation{args: args, stdin: []byte(prompt), env: isolation}, nil
}

type codeBuddyUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

type codeBuddyMessage struct {
	Type           string          `json:"type"`
	Subtype        string          `json:"subtype"`
	SessionID      string          `json:"session_id"`
	IsError        bool            `json:"is_error"`
	Result         string          `json:"result"`
	Error          json.RawMessage `json:"error"`
	Errors         []string        `json:"errors"`
	TerminalReason string          `json:"terminal_reason"`
	Message        struct {
		Content []codeBuddyBlock `json:"content"`
	} `json:"message"`
	Meta struct {
		Turn *struct {
			Version int    `json:"version"`
			Scope   string `json:"scope"`
			Total   struct {
				Usage *codeBuddyUsage `json:"usage"`
			} `json:"total"`
		} `json:"codebuddy.ai/turnUsage"`
	} `json:"_meta"`
}

type codeBuddyBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     toolArguments   `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func parseCodeBuddy(s *session, line []byte) ([]runtime.Event, bool) {
	var m codeBuddyMessage
	if json.Unmarshal(line, &m) != nil {
		return nil, false
	}
	if s.sessionID != "" && m.SessionID != "" && m.SessionID != s.sessionID {
		return nil, false
	}
	if turn := m.Meta.Turn; turn != nil && turn.Version == 1 && turn.Scope == "turn" && turn.Total.Usage != nil {
		u := turn.Total.Usage
		// Every update is an absolute current-turn snapshot, never cumulative addition.
		s.usage = runtime.Usage{Input: nonNegative(u.Input), Output: nonNegative(u.Output), CachedInput: nonNegative(u.CacheRead), CacheWriteInput: nonNegative(u.CacheWrite)}
		s.hasUsage = true
	}
	var events []runtime.Event
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			return s.setSession(m.SessionID), false
		}
	case "assistant":
		for _, block := range m.Message.Content {
			switch block.Type {
			case "text":
				s.pendingText.WriteString(boundBytes(block.Text, stepTextLimit))
			case "thinking":
				events = append(events, s.flushPending()...)
				events = append(events, s.textStep(runtime.StepKindThinking, block.Thinking)...)
			case "tool_use":
				events = append(events, s.flushPending()...)
				events = append(events, s.startStep(block.ID, s.toolStep(block.Name, block.Input))...)
			}
		}
	case "user":
		for _, block := range m.Message.Content {
			if block.Type != "tool_result" {
				continue
			}
			state := runtime.StepDone
			if block.IsError {
				state = runtime.StepFailed
			}
			var text string
			if json.Unmarshal(block.Content, &text) != nil {
				var content []codeBuddyBlock
				if json.Unmarshal(block.Content, &content) == nil {
					for _, item := range content {
						text += item.Text
					}
				}
			}
			events = append(events, s.finishStep(block.ToolUseID, runtime.Step{Kind: runtime.StepKindToolCall}, state, text, nil)...)
		}
	case "result":
		if m.TerminalReason == "cancelled" || m.TerminalReason == "canceled" {
			return append(s.turnEnd("cancelled"), runtime.Event{Type: runtime.EventCancelled}), true
		}
		if m.Subtype == "success" && !m.IsError {
			output := m.Result
			if output == "" {
				output = s.takePending()
			} else {
				s.pendingText.Reset()
			}
			return s.complete(output), true
		}
		text := m.Result
		if len(m.Errors) > 0 {
			text = m.Errors[0]
		}
		if text == "" {
			text = "CodeBuddy ended with " + m.Subtype
		}
		return s.fail(text), true
	case "error":
		var text string
		if json.Unmarshal(m.Error, &text) != nil {
			text = "CodeBuddy protocol error"
		}
		return s.fail(text), true
	}
	return events, false
}
