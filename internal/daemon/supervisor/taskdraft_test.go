package supervisor

import (
	"context"
	"encoding/json"
	"openfox/internal/runtime"
	"strings"
	"testing"
)

func TestStandaloneTaskDraft(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		valid        bool
	}{
		{"valid", `{"title":"Fix login","description":"Document failure","priority":"HIGH"}`, true}, {"invalid", "Done", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &fakeAdapter{output: tc.output, approvalModes: []string{"READ_ONLY"}}
			a.onStart = func(r runtime.RunRequest) {
				if len(r.Tools) != 0 || r.ApprovalMode != "READ_ONLY" || !strings.Contains(r.Prompt, "JSON object") || r.WorkingDirectory == "" {
					t.Fatal("unexpected draft context")
				}
			}
			s := Supervisor{config: Config{Adapter: a}, slots: make(chan struct{}, 1)}
			data, err := s.DraftTask(context.Background(), json.RawMessage(`{"runtime":"codex","prompt":"Fix login"}`))
			if (err == nil) != tc.valid {
				t.Fatalf("result %v, %v", data, err)
			}
			if tc.valid && strings.Contains(string(data.(json.RawMessage)), "priority") {
				t.Fatal("forwarded model-selected priority")
			}
		})
	}
}
