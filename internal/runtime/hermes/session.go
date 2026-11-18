package hermes

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"openfox/internal/runtime"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type acpSession struct {
	cwd, prompt, id string
	phase           int
	interrupted     bool
	text            strings.Builder
	maxOutput       int
	redact          func(string) string
	mask            *streamMask
	toolKinds       map[string]string
	proxy           *modelProxy
	usage           *runtime.Usage
	mu              sync.Mutex
	timer           *time.Timer
}

func rpcSend(send runtime.Sender, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return send(data)
}

func rpcRequest(send runtime.Sender, id int, method string, params any) error {
	return rpcSend(send, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (s *acpSession) Begin(send runtime.Sender, fail func(error)) error {
	s.mu.Lock()
	s.timer = time.AfterFunc(30*time.Second, func() { fail(fmt.Errorf("%w: Hermes ACP initialization timed out", runtime.ErrProtocol)) })
	s.mu.Unlock()
	s.phase = 1
	return rpcRequest(send, 101, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]any{"name": "openfox", "version": "1"}})
}

func (s *acpSession) stopTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
}

func (s *acpSession) HandleLine(line []byte, send runtime.Sender) ([]runtime.Event, bool, error) {
	var m rpcMessage
	if json.Unmarshal(line, &m) != nil || m.JSONRPC != "2.0" {
		return nil, false, fmt.Errorf("%w: Hermes emitted invalid JSON-RPC", runtime.ErrProtocol)
	}
	if m.Method != "" {
		if len(m.ID) > 0 {
			return s.clientRequest(m, send)
		}
		if m.Method == "session/update" {
			return s.update(m.Params)
		}
		return nil, false, nil
	}
	var id int
	if json.Unmarshal(m.ID, &id) != nil || id != 100+s.phase {
		return nil, false, fmt.Errorf("%w: unexpected Hermes ACP response", runtime.ErrProtocol)
	}
	if len(m.Error) > 0 && string(m.Error) != "null" {
		s.stopTimer()
		text := "Hermes rejected the native request"
		if err := s.proxy.Err(); err != nil {
			text = err.Error()
		}
		return s.finish(runtime.EventFailed, text), true, nil
	}
	switch s.phase {
	case 1:
		var result struct {
			ProtocolVersion int                   `json:"protocolVersion"`
			AgentInfo       struct{ Name string } `json:"agentInfo"`
		}
		if json.Unmarshal(m.Result, &result) != nil || result.ProtocolVersion != 1 || result.AgentInfo.Name != "hermes-agent" {
			return nil, false, fmt.Errorf("%w: unexpected Hermes ACP identity", runtime.ErrProtocol)
		}
		// agentInfo.version may be 0.0.0. Trust was established before execution
		// using the embedded build manifest, never this self-reported value.
		s.phase = 2
		return nil, false, rpcRequest(send, 102, "session/new", map[string]any{"cwd": s.cwd, "mcpServers": []any{}})
	case 2:
		var result struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(m.Result, &result) != nil || result.SessionID == "" || len(result.SessionID) > 256 || strings.ContainsAny(result.SessionID, "\n\r\x00") {
			return nil, false, fmt.Errorf("%w: invalid Hermes session", runtime.ErrProtocol)
		}
		s.id, s.phase = result.SessionID, 3
		s.stopTimer()
		if s.interrupted {
			return s.finish(runtime.EventCancelled, ""), true, nil
		}
		err := rpcRequest(send, 103, "session/prompt", map[string]any{"sessionId": s.id, "prompt": []any{map[string]string{"type": "text", "text": s.prompt}}})
		return []runtime.Event{{Type: runtime.EventStarted}}, false, err
	case 3:
		var result struct {
			StopReason string `json:"stopReason"`
			Usage      *struct {
				Input  int64 `json:"inputTokens"`
				Output int64 `json:"outputTokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(m.Result, &result) != nil {
			return nil, false, fmt.Errorf("%w: invalid Hermes prompt result", runtime.ErrProtocol)
		}
		if result.Usage != nil && result.Usage.Input >= 0 && result.Usage.Output >= 0 {
			s.usage = &runtime.Usage{Input: result.Usage.Input, Output: result.Usage.Output}
		}
		if result.StopReason == "cancelled" || s.interrupted {
			return s.finish(runtime.EventCancelled, ""), true, nil
		}
		if result.StopReason != "end_turn" {
			return s.finish(runtime.EventFailed, "Hermes ended without a complete response"), true, nil
		}
		if err := s.proxy.FatalErr(); err != nil {
			return s.finish(runtime.EventFailed, err.Error()), true, nil
		}
		return s.finish(runtime.EventCompleted, ""), true, nil
	default:
		return nil, false, fmt.Errorf("%w: invalid Hermes ACP state", runtime.ErrProtocol)
	}
}

func (s *acpSession) clientRequest(m rpcMessage, send runtime.Sender) ([]runtime.Event, bool, error) {
	if m.Method != "session/request_permission" {
		_ = rpcSend(send, map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "OpenFox does not expose this capability"}})
		return nil, false, fmt.Errorf("%w: Hermes requested an unsupported client capability", runtime.ErrUnsupported)
	}
	var params struct {
		SessionID string                            `json:"sessionId"`
		Options   []struct{ Kind, OptionID string } `json:"options"`
	}
	if json.Unmarshal(m.Params, &params) != nil || s.id == "" || params.SessionID != s.id || len(params.Options) > 16 {
		return nil, false, fmt.Errorf("%w: invalid Hermes permission request", runtime.ErrProtocol)
	}
	outcome := map[string]string{"outcome": "cancelled"}
	for _, option := range params.Options {
		if option.Kind == "reject_once" && option.OptionID != "" {
			outcome = map[string]string{"outcome": "selected", "optionId": option.OptionID}
			break
		}
	}
	// An RPC error does not reject the operation in native Hermes; use the ACP
	// rejection result. No approval UI can elevate this read-only runtime.
	err := rpcSend(send, map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": map[string]any{"outcome": outcome}})
	return []runtime.Event{{Type: runtime.EventWarning, Text: "Hermes operation denied by the read-only policy"}}, false, err
}

func (s *acpSession) update(raw json.RawMessage) ([]runtime.Event, bool, error) {
	var params struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate, ToolCallID, Kind, Status, Title string
			Content                                        json.RawMessage
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil || s.id == "" || params.SessionID != s.id {
		return nil, false, fmt.Errorf("%w: Hermes update targets another session", runtime.ErrProtocol)
	}
	u := params.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var content struct{ Type, Text string }
		if json.Unmarshal(u.Content, &content) != nil || content.Type != "text" {
			return nil, false, fmt.Errorf("%w: Hermes non-text response is not supported", runtime.ErrUnsupported)
		}
		text := s.redact(content.Text)
		if s.mask != nil {
			text = s.mask.feed(content.Text)
		}
		if s.text.Len()+len(text) > s.maxOutput {
			return nil, false, runtime.ErrOutputLimit
		}
		s.text.WriteString(text)
		if text == "" {
			return nil, false, nil
		}
		return []runtime.Event{stepEvent(runtime.Step{ID: "hermes-answer", Kind: runtime.StepKindAgentText, State: runtime.StepRunning, Append: text})}, false, nil
	case "tool_call", "tool_call_update":
		if s.toolKinds == nil {
			s.toolKinds = map[string]string{}
		}
		if u.Kind != "" {
			s.toolKinds[u.ToolCallID] = u.Kind
		} else {
			u.Kind = s.toolKinds[u.ToolCallID]
		}
		kind := runtime.StepKindRead
		state := runtime.StepRunning
		if u.Kind == "search" {
			kind = runtime.StepKindSearch
		}
		if u.Kind == "edit" {
			kind = runtime.StepKindWriteFile
		}
		if u.Kind == "execute" {
			kind = runtime.StepKindExec
		}
		if u.Status == "completed" {
			state = runtime.StepDone
		}
		if u.Status == "failed" {
			state = runtime.StepFailed
		}
		if u.Kind == "execute" || u.Kind == "edit" && u.Status != "" {
			state = runtime.StepDenied
		}
		if u.ToolCallID == "" || len(u.ToolCallID) > 256 {
			return nil, false, runtime.ErrProtocol
		}
		return []runtime.Event{stepEvent(runtime.Step{ID: u.ToolCallID, Kind: kind, State: state, Text: s.redact(u.Title)})}, false, nil
	default:
		// Commands/config/model lists are not exposed; context size is a user
		// declaration, not a new measured usage capability.
		return nil, false, nil
	}
}

func stepEvent(step runtime.Step) runtime.Event {
	data, _ := json.Marshal(map[string]any{"schema": runtime.StepSchemaVersion, "step": step})
	return runtime.Event{Type: runtime.EventStep, Payload: data}
}

func (s *acpSession) finish(kind runtime.EventType, text string) []runtime.Event {
	s.stopTimer()
	var tail string
	if s.mask != nil {
		tail = s.mask.finish()
		s.text.WriteString(tail)
	}
	state := runtime.StepDone
	if kind == runtime.EventFailed {
		state = runtime.StepFailed
	}
	if kind == runtime.EventCancelled {
		state = runtime.StepInterrupted
	}
	result := []runtime.Event{stepEvent(runtime.Step{ID: "hermes-answer", Kind: runtime.StepKindAgentText, State: state, Append: tail})}
	if s.text.Len() > 0 {
		result = append(result, runtime.Event{Type: runtime.EventOutput, Text: s.text.String()})
	}
	return append(result, runtime.Event{Type: kind, Text: text})
}

func (s *acpSession) Interrupt(send runtime.Sender) bool {
	s.interrupted = true
	s.stopTimer()
	if s.id == "" {
		return false
	}
	err := rpcSend(send, map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]string{"sessionId": s.id}})
	s.proxy.Close()
	return err == nil
}

func (s *acpSession) Usage() *runtime.Usage           { return s.usage }
func (s *acpSession) Finalize(string) []runtime.Event { s.stopTimer(); return nil }
