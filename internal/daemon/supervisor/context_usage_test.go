package supervisor

import (
	"encoding/json"
	"testing"
	"time"

	"openfox/internal/runtime"
)

func TestContextUsageUsesExistingEventEnvelope(t *testing.T) {
	zero, window := int64(0), int64(124518)
	data, err := json.Marshal(runtime.ContextUsage{Schema: 1, Runtime: "codex", CurrentTokens: &zero, WindowTokens: &window})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	input := eventInput(2, 7, runtime.Event{Type: runtime.EventContextUsage, Payload: data, At: at})
	var body struct {
		Data runtime.ContextUsage `json:"data"`
		Text string               `json:"text"`
	}
	if err := json.Unmarshal(input.Payload, &body); err != nil {
		t.Fatal(err)
	}
	if input.Generation != 2 || input.Sequence != 7 || input.Type != "context_usage" || !input.OccurredAt.Equal(at) || body.Data.CurrentTokens == nil || *body.Data.CurrentTokens != 0 || body.Data.WindowTokens == nil || *body.Data.WindowTokens != window || body.Text != "" {
		t.Fatalf("input=%+v body=%+v", input, body)
	}
}
