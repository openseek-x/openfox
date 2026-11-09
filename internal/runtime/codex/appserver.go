package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

// app-server 协议（codex-cli 0.164 生成的 v2 schema）：stdio 上逐行 JSON-RPC，
// 响应不带 "jsonrpc" 字段。一次 Run 的会话顺序固定：
// initialize → initialized → thread/start → turn/start → 通知流 → turn/completed。
const (
	requestInitialize    = 1
	requestThreadStart   = 2
	requestTurnStart     = 3
	requestTurnInterrupt = 4
)

// 握手（initialize 到 turn/start 响应）上限：超过即判定 app-server 不可用，而不是等到 Run 超时。
var appServerHandshakeTimeout = 60 * time.Second

// 动态工具调用单次上限；超时按失败回复给模型，不阻塞 Run。
// 发布工具要上传最多 20MB 的文件，由 30 秒放宽到 2 分钟（与 MCP 桥的上限一致）。
var appServerToolCallTimeout = 2 * time.Minute

// toolResultLimit 限制回复给模型的工具文本长度（字节）。
const toolResultLimit = 64 * 1024

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type turnPayload struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	DurationMs *int64 `json:"durationMs"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type itemPayload struct {
	Type             string  `json:"type"`
	Text             string  `json:"text"`
	Phase            *string `json:"phase"`
	Command          string  `json:"command"`
	Status           string  `json:"status"`
	ExitCode         *int    `json:"exitCode"`
	AggregatedOutput *string `json:"aggregatedOutput"`
}

// appServerSession 实现 runtime.Session。除 handshake 计时器外，方法都由进程监督协程串行调用。
type appServerSession struct {
	prompt        string
	instructions  string
	model         string
	cwd           string
	networkAccess bool
	// approvalMode 是权限档位；空串与 FOLLOW_RUNTIME 等价（workspace-write，行为与之前完全一致）。
	approvalMode string
	redact       func(string) string

	// 动态工具（见 RunRequest.Tools）。tools 为空时不声明、不启用实验接口。
	tools    []runtime.Tool
	toolCall func(ctx context.Context, name, callID string, arguments json.RawMessage) (string, bool)
	toolCtx  context.Context
	// toolsDropped 表示 thread/start 带 dynamicTools 被拒后已降级重发。
	toolsDropped bool
	// artifactDir 是电脑端为本次 Run 新建的产物目录；非空时在 workspace-write 下加入可写根。
	artifactDir string

	threadID    string
	turnID      string
	interrupted bool

	// 执行步骤状态（见 steps.go）。now 为空时使用系统时钟，测试可替换。
	now         func() time.Time
	items       map[string]*itemTrack
	order       []string
	turnStarted time.Time
	turnEnded   bool
	planSeen    bool
	usage       *runtime.Usage

	handshakeMu sync.Mutex
	handshake   *time.Timer
}

func (s *appServerSession) SessionID() string { return s.threadID }

func (s *appServerSession) Begin(send runtime.Sender, fail func(error)) error {
	s.handshakeMu.Lock()
	s.handshake = time.AfterFunc(appServerHandshakeTimeout, func() {
		fail(fmt.Errorf("%w: codex app-server handshake timed out", runtime.ErrProtocol))
	})
	s.handshakeMu.Unlock()
	// dynamicTools 属于实验接口，只有声明了工具才开启 experimentalApi。
	var capabilities any
	if len(s.tools) > 0 {
		capabilities = map[string]any{"experimentalApi": true, "requestAttestation": false}
	}
	return sendRequest(send, requestInitialize, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "openfox_daemon", "title": "OpenFox", "version": "1"},
		"capabilities": capabilities,
	})
}

func (s *appServerSession) stopHandshake() {
	s.handshakeMu.Lock()
	defer s.handshakeMu.Unlock()
	if s.handshake != nil {
		s.handshake.Stop()
	}
}

func (s *appServerSession) HandleLine(line []byte, send runtime.Sender) ([]runtime.Event, bool, error) {
	var message rpcMessage
	if err := json.Unmarshal(line, &message); err != nil {
		return nil, false, fmt.Errorf("%w: invalid codex app-server message", runtime.ErrProtocol)
	}
	hasID := len(message.ID) > 0 && string(message.ID) != "null"
	switch {
	case hasID && message.Method == "":
		return s.handleResponse(message, send)
	case hasID:
		return s.handleServerRequest(message, send)
	case message.Method != "":
		return s.handleNotification(message)
	default:
		return nil, false, fmt.Errorf("%w: codex app-server message has neither id nor method", runtime.ErrProtocol)
	}
}

func (s *appServerSession) handleResponse(message rpcMessage, send runtime.Sender) ([]runtime.Event, bool, error) {
	id, err := strconv.Atoi(string(message.ID))
	if err != nil {
		return nil, false, fmt.Errorf("%w: unexpected codex app-server response id", runtime.ErrProtocol)
	}
	if id == requestTurnInterrupt {
		// 中断结果以 turn/completed(interrupted) 为准；中断请求本身失败时由宽限期后的信号兜底。
		return nil, false, nil
	}
	if message.Error != nil && id == requestThreadStart && len(s.tools) > 0 && !s.toolsDropped {
		// 降级：当前 Codex 不接受 dynamicTools 时去掉工具重发一次，Run 照常进行（模型无任务工具）。
		s.toolsDropped = true
		warning := runtime.Event{Type: runtime.EventWarning, Text: s.redact("codex app-server rejected dynamicTools, continuing without task tools: " + message.Error.Message),
			Payload: mustJSON(map[string]string{"kind": "dynamicTools_unsupported"})}
		return []runtime.Event{warning}, false, sendRequest(send, requestThreadStart, "thread/start", s.threadStartParams())
	}
	if message.Error != nil {
		s.stopHandshake()
		text := s.redact(fmt.Sprintf("codex app-server %s failed: %s", requestName(id), message.Error.Message))
		return []runtime.Event{{Type: runtime.EventFailed, Text: text}}, true, nil
	}
	switch id {
	case requestInitialize:
		if err := send([]byte(`{"method":"initialized"}`)); err != nil {
			return nil, false, err
		}
		return nil, false, sendRequest(send, requestThreadStart, "thread/start", s.threadStartParams())
	case requestThreadStart:
		var result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(message.Result, &result); err != nil || result.Thread.ID == "" {
			return nil, false, fmt.Errorf("%w: thread/start returned no thread id", runtime.ErrProtocol)
		}
		s.threadID = result.Thread.ID
		params := map[string]any{
			"threadId": s.threadID,
			"input":    []any{map[string]any{"type": "text", "text": s.prompt, "text_elements": []any{}}},
			"cwd":      s.cwd, "approvalPolicy": "never",
			// 沙箱策略由权限档位决定（见 sandboxPolicy）；默认只允许写工作目录，是否联网由适配器配置决定（当前默认允许）。
			"sandboxPolicy": s.sandboxPolicy(),
		}
		if s.model != "" {
			params["model"] = s.model
		}
		return nil, false, sendRequest(send, requestTurnStart, "turn/start", params)
	case requestTurnStart:
		var result struct {
			Turn turnPayload `json:"turn"`
		}
		if err := json.Unmarshal(message.Result, &result); err != nil || result.Turn.ID == "" {
			return nil, false, fmt.Errorf("%w: turn/start returned no turn id", runtime.ErrProtocol)
		}
		s.stopHandshake()
		s.turnID = result.Turn.ID
		s.turnStarted = s.clock()
		return []runtime.Event{{Type: runtime.EventStarted, Payload: mustJSON(map[string]string{"thread_id": s.threadID, "turn_id": s.turnID})}}, false, nil
	default:
		return nil, false, fmt.Errorf("%w: unexpected codex app-server response id", runtime.ErrProtocol)
	}
}

// sandboxMode 把权限档位映射为 thread/start 的 sandbox 取值（read-only / workspace-write / danger-full-access）。
// 出处：Codex app-server 协议（https://github.com/openai/codex 的 codex-rs/app-server-protocol，thread/start 的 sandbox）。
// 未知档位按最严格的 read-only 处理（失败即安全）；FOLLOW_RUNTIME 与空串是现有的 workspace-write。
func sandboxMode(mode string) string {
	switch mode {
	case "", runtimepolicy.FollowRuntime, runtimepolicy.WorkspaceWrite:
		return "workspace-write"
	case runtimepolicy.FullAccess:
		return "danger-full-access"
	default:
		return "read-only"
	}
}

// sandboxPolicy 是 turn/start 的 sandboxPolicy（type 为 readOnly / workspaceWrite / dangerFullAccess）。
// 出处同上；FOLLOW_RUNTIME 与 WORKSPACE_WRITE 的内容与改动前完全一致（可写根只有工作目录）。
func (s *appServerSession) sandboxPolicy() map[string]any {
	switch sandboxMode(s.approvalMode) {
	case "danger-full-access":
		return map[string]any{"type": "dangerFullAccess"}
	case "read-only":
		return map[string]any{"type": "readOnly"}
	default:
		roots := []string{s.cwd}
		if s.artifactDir != "" && s.artifactDir != s.cwd {
			// 产物目录是电脑端为本次 Run 新建的空目录，只多放行这一个目录；没有产物目录时与改动前一致。
			roots = append(roots, s.artifactDir)
		}
		return map[string]any{
			"type": "workspaceWrite", "writableRoots": roots, "networkAccess": s.networkAccess,
			"excludeTmpdirEnvVar": false, "excludeSlashTmp": false,
		}
	}
}

func (s *appServerSession) threadStartParams() map[string]any {
	params := map[string]any{
		"cwd": s.cwd, "approvalPolicy": "never", "sandbox": sandboxMode(s.approvalMode), "ephemeral": true,
	}
	if s.instructions != "" {
		params["developerInstructions"] = s.instructions
	}
	if s.model != "" {
		params["model"] = s.model
	}
	if len(s.tools) > 0 && !s.toolsDropped {
		specs := make([]any, 0, len(s.tools))
		for _, tool := range s.tools {
			specs = append(specs, map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
		}
		params["dynamicTools"] = specs
	}
	return params
}

// handleToolCall 处理 item/tool/call：在独立协程里执行（带超时），完成后用同一 id 回复。
// send 并发安全；事件由 item/started、item/completed 通知产生，这里不发事件。
func (s *appServerSession) handleToolCall(message rpcMessage, send runtime.Sender) error {
	var params struct {
		CallID    string          `json:"callId"`
		Tool      string          `json:"tool"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return sendToolResult(send, message.ID, "invalid tool call parameters", false)
	}
	known := false
	for _, tool := range s.tools {
		if tool.Name == params.Tool {
			known = true
		}
	}
	if s.toolCall == nil || !known {
		return sendToolResult(send, message.ID, "unknown tool: "+boundBytes(params.Tool, 128), false)
	}
	parent := s.toolCtx
	if parent == nil {
		parent = context.Background()
	}
	id := append(json.RawMessage(nil), message.ID...)
	go func() {
		ctx, cancel := context.WithTimeout(parent, appServerToolCallTimeout)
		defer cancel()
		text, ok := "tool call failed", false
		func() {
			defer func() {
				if recover() != nil {
					text, ok = "tool call failed", false
				}
			}()
			text, ok = s.toolCall(ctx, params.Tool, params.CallID, params.Arguments)
		}()
		if ctx.Err() != nil && ok {
			text, ok = "tool call timed out or was cancelled", false
		}
		// 进程已结束时写入失败，忽略即可。
		_ = sendToolResult(send, id, s.redact(boundBytes(text, toolResultLimit)), ok)
	}()
	return nil
}

func sendToolResult(send runtime.Sender, id json.RawMessage, text string, ok bool) error {
	return sendRaw(send, map[string]any{"id": id, "result": map[string]any{
		"contentItems": []any{map[string]any{"type": "inputText", "text": text}}, "success": ok,
	}})
}

// handleServerRequest 处理 app-server 发给客户端的请求。审批为 never 时不应出现审批请求；
// 一旦出现即拒绝并以 APPROVAL_REQUIRED 失败（失败即安全）。其余请求回复“不支持”。
func (s *appServerSession) handleServerRequest(message rpcMessage, send runtime.Sender) ([]runtime.Event, bool, error) {
	switch message.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		if err := sendRaw(send, map[string]any{"id": message.ID, "result": map[string]any{"decision": "decline"}}); err != nil {
			return nil, false, err
		}
		return []runtime.Event{approvalRequired()}, false, nil
	case "item/permissions/requestApproval", "applyPatchApproval", "execCommandApproval",
		"item/tool/requestUserInput", "mcpServer/elicitation/request":
		if err := replyUnsupported(send, message.ID); err != nil {
			return nil, false, err
		}
		return []runtime.Event{approvalRequired()}, false, nil
	case "item/tool/call":
		if len(s.tools) == 0 {
			// 未声明动态工具时不应收到调用，保持原行为：回复不支持。
			return nil, false, replyUnsupported(send, message.ID)
		}
		return nil, false, s.handleToolCall(message, send)
	default:
		return nil, false, replyUnsupported(send, message.ID)
	}
}

func (s *appServerSession) handleNotification(message rpcMessage) ([]runtime.Event, bool, error) {
	switch message.Method {
	case "item/started":
		var params struct {
			ThreadID    string          `json:"threadId"`
			Item        json.RawMessage `json:"item"`
			StartedAtMs int64           `json:"startedAtMs"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			// 步骤是过程信息：无法解析的开始通知只跳过，不让 Run 失败。
			return nil, false, nil
		}
		if s.threadID != "" && params.ThreadID != "" && params.ThreadID != s.threadID {
			return nil, false, nil
		}
		return s.itemStarted(params.Item, millisTime(params.StartedAtMs, s.clock())), false, nil
	case "item/completed":
		var params struct {
			ThreadID      string          `json:"threadId"`
			Item          json.RawMessage `json:"item"`
			CompletedAtMs int64           `json:"completedAtMs"`
		}
		var legacy itemPayload
		if err := json.Unmarshal(message.Params, &params); err != nil || json.Unmarshal(params.Item, &legacy) != nil {
			return nil, false, fmt.Errorf("%w: invalid item/completed notification", runtime.ErrProtocol)
		}
		if s.threadID != "" && params.ThreadID != "" && params.ThreadID != s.threadID {
			return nil, false, nil
		}
		// 先发步骤终态，再发旧事件（output / tool），两者并存。
		events := s.itemCompleted(params.Item, millisTime(params.CompletedAtMs, s.clock()))
		if event, ok := s.itemEvent(legacy); ok {
			events = append(events, event)
		}
		return events, false, nil
	case "item/commandExecution/outputDelta", "item/agentMessage/delta", "item/reasoning/summaryTextDelta",
		"item/reasoning/textDelta", "item/reasoning/summaryPartAdded":
		return s.handleDelta(message.Method, message.Params), false, nil
	case "turn/plan/updated":
		events, err := s.planUpdated(message.Params)
		return events, false, err
	case "thread/tokenUsage/updated":
		s.tokenUsageUpdated(message.Params)
		return nil, false, nil
	case "error":
		var params struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			WillRetry bool `json:"willRetry"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, false, fmt.Errorf("%w: invalid error notification", runtime.ErrProtocol)
		}
		text := s.redact(params.Error.Message)
		if params.WillRetry {
			return []runtime.Event{failureEvent(runtime.EventWarning, text)}, false, nil
		}
		// 不再重试的错误即终态：结束会话，不等待可能不会到来的 turn/completed。
		return append(s.turnEnd("failed", nil), failureEvent(runtime.EventFailed, text)), true, nil
	case "warning", "guardianWarning", "configWarning", "deprecationNotice":
		var params struct {
			Message string `json:"message"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, false, fmt.Errorf("%w: invalid %s notification", runtime.ErrProtocol, message.Method)
		}
		text := params.Message
		if text == "" {
			text = params.Summary
		}
		return []runtime.Event{{Type: runtime.EventWarning, Text: s.redact(text), Payload: mustJSON(map[string]string{"kind": message.Method})}}, false, nil
	case "turn/completed":
		var params struct {
			ThreadID string      `json:"threadId"`
			Turn     turnPayload `json:"turn"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			return nil, false, fmt.Errorf("%w: invalid turn/completed notification", runtime.ErrProtocol)
		}
		if (s.threadID != "" && params.ThreadID != s.threadID) || (s.turnID != "" && params.Turn.ID != s.turnID) {
			return nil, false, nil
		}
		duration := params.Turn.DurationMs
		if duration != nil && *duration < 0 {
			duration = nil
		}
		switch params.Turn.Status {
		case "completed":
			return append(s.turnEnd("done", duration), runtime.Event{Type: runtime.EventCompleted, Payload: mustJSON(map[string]string{"turn_id": params.Turn.ID})}), true, nil
		case "interrupted":
			if s.interrupted {
				// 用户取消：进程层按取消结束，并补发 cancelled 事件。
				return s.turnEnd("cancelled", duration), true, nil
			}
			return append(s.turnEnd("failed", duration), runtime.Event{Type: runtime.EventFailed, Text: "codex turn was interrupted"}), true, nil
		case "failed":
			if params.Turn.Error != nil && params.Turn.Error.Message != "" {
				return append(s.turnEnd("failed", duration), failureEvent(runtime.EventFailed, s.redact(params.Turn.Error.Message))), true, nil
			}
			return append(s.turnEnd("failed", duration), runtime.Event{Type: runtime.EventFailed, Text: "codex turn failed"}), true, nil
		default:
			return nil, false, nil
		}
	default:
		// app-server 有数十种通知（增量、状态、账户、MCP 等），与 Run 结果无关的一律忽略。
		return nil, false, nil
	}
}

// itemEvent 把完成的 item 映射为过程事件；只有最终回复正文（非 commentary 的 agentMessage）是 output。
func (s *appServerSession) itemEvent(item itemPayload) (runtime.Event, bool) {
	switch item.Type {
	case "agentMessage":
		if item.Phase != nil && *item.Phase == "commentary" {
			return runtime.Event{Type: runtime.EventTool, Text: s.redact(item.Text), Payload: mustJSON(map[string]string{"kind": "commentary"})}, true
		}
		return runtime.Event{Type: runtime.EventOutput, Text: s.redact(item.Text)}, true
	case "userMessage", "reasoning", "hookPrompt":
		return runtime.Event{}, false
	case "commandExecution":
		payload := map[string]any{"kind": item.Type, "status": item.Status}
		if item.ExitCode != nil {
			payload["exit_code"] = *item.ExitCode
		}
		return runtime.Event{Type: runtime.EventTool, Text: s.redact(item.Command), Payload: mustJSON(payload)}, true
	default:
		return runtime.Event{Type: runtime.EventTool, Payload: mustJSON(map[string]string{"kind": item.Type, "status": item.Status})}, true
	}
}

// Interrupt 请求 app-server 停止当前 turn；turn 尚未开始时返回 false，由进程层直接发信号。
func (s *appServerSession) Interrupt(send runtime.Sender) bool {
	if s.threadID == "" || s.turnID == "" {
		return false
	}
	if err := sendRequest(send, requestTurnInterrupt, "turn/interrupt", map[string]any{"threadId": s.threadID, "turnId": s.turnID}); err != nil {
		return false
	}
	s.interrupted = true
	return true
}

func approvalRequired() runtime.Event {
	return runtime.Event{Type: runtime.EventApprovalRequired, Critical: true, CriticalError: runtime.ErrApprovalRequired,
		Text: "runtime approval is required but interactive approval is not enabled"}
}

func requestName(id int) string {
	switch id {
	case requestInitialize:
		return "initialize"
	case requestThreadStart:
		return "thread/start"
	case requestTurnStart:
		return "turn/start"
	default:
		return "request"
	}
}

func sendRequest(send runtime.Sender, id int, method string, params any) error {
	return sendRaw(send, map[string]any{"id": id, "method": method, "params": params})
}

func replyUnsupported(send runtime.Sender, id json.RawMessage) error {
	return sendRaw(send, map[string]any{"id": id, "error": map[string]any{"code": -32601, "message": "not supported by OpenFox"}})
}

func sendRaw(send runtime.Sender, message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("%w: encode codex app-server message: %v", runtime.ErrProtocol, err)
	}
	return send(encoded)
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}
