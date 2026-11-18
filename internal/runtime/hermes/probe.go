package hermes

import (
	"encoding/json"
	"openfox/internal/runtime"
)

// Capability discovery performs only initialize, never a model prompt or a
// gateway service operation. The process supervisor supplies its timeout.
type probeSession struct{}

func (*probeSession) Begin(send runtime.Sender, _ func(error)) error {
	return rpcRequest(send, 1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "openfox-probe", "version": "1"}})
}
func (*probeSession) HandleLine(line []byte, _ runtime.Sender) ([]runtime.Event, bool, error) {
	var message rpcMessage
	if json.Unmarshal(line, &message) != nil || message.JSONRPC != "2.0" || string(message.ID) != "1" || len(message.Error) > 0 {
		return nil, false, runtime.ErrProtocol
	}
	var result struct {
		ProtocolVersion int
		AgentInfo       struct{ Name string }
	}
	if json.Unmarshal(message.Result, &result) != nil || result.ProtocolVersion != 1 || result.AgentInfo.Name != "hermes-agent" {
		return nil, false, runtime.ErrProtocol
	}
	return []runtime.Event{{Type: runtime.EventCompleted}}, true, nil
}
func (*probeSession) Interrupt(runtime.Sender) bool { return false }
