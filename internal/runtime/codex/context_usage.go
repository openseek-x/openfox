package codex

import (
	"encoding/json"
	"strings"

	"openfox/internal/runtime"
)

const maxContextTokens int64 = 9007199254740991 // JavaScript's largest exact integer.

func contextToken(raw json.RawMessage, positive bool) *int64 {
	var value int64
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 || value > maxContextTokens || (positive && value == 0) {
		return nil
	}
	return &value
}

func contextModel(raw json.RawMessage, redact func(string) string) *string {
	var model string
	if json.Unmarshal(raw, &model) != nil || strings.TrimSpace(model) == "" || len(model) > 128 {
		return nil
	}
	if redact != nil {
		model = redact(model)
	}
	return &model
}

// Invalid optional counters produce unknown values without failing execution.
// Require both IDs: a pooled host must not attach another turn's sample to a Run.
func (s *appServerSession) contextUsageUpdated(raw json.RawMessage) []runtime.Event {
	var params struct {
		ThreadID   string          `json:"threadId"`
		TurnID     string          `json:"turnId"`
		TokenUsage json.RawMessage `json:"tokenUsage"`
	}
	if json.Unmarshal(raw, &params) != nil || s.threadID == "" || s.turnID == "" || params.ThreadID != s.threadID || params.TurnID != s.turnID {
		return nil
	}
	var usage struct {
		Last   json.RawMessage `json:"last"`
		Window json.RawMessage `json:"modelContextWindow"`
	}
	if len(params.TokenUsage) == 0 || string(params.TokenUsage) == "null" || json.Unmarshal(params.TokenUsage, &usage) != nil {
		return nil
	}
	var last struct {
		Total json.RawMessage `json:"totalTokens"`
	}
	_ = json.Unmarshal(usage.Last, &last)
	requested := s.model
	if s.redact != nil {
		requested = s.redact(requested)
	}
	payload, _ := json.Marshal(runtime.ContextUsage{
		Schema: 1, Runtime: RuntimeName, RequestedModel: requested, Model: s.reportedModel,
		CurrentTokens: contextToken(last.Total, false), WindowTokens: contextToken(usage.Window, true),
	})
	return []runtime.Event{{Type: runtime.EventContextUsage, Payload: payload, At: s.clock()}}
}
