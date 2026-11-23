package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"openfox/internal/runtime"
	"strings"
	"testing"
)

type toolFreeFake struct{ *fakeAdapter }

func (a toolFreeFake) StartToolFree(ctx context.Context, req runtime.RunRequest) (runtime.Execution, error) {
	return a.fakeAdapter.Start(ctx, req)
}

func TestTaskDraftRejectsAdapterWithoutToolControl(t *testing.T) {
	a := &fakeAdapter{}
	s := Supervisor{config: Config{Adapter: a}, slots: make(chan struct{}, 1)}
	_, err := s.DraftTask(context.Background(), json.RawMessage(`{"runtime":"codex","prompt":"draft"}`))
	if !errors.Is(err, runtime.ErrUnsupported) || a.startCount() != 0 {
		t.Fatalf("started %d times, error %v", a.startCount(), err)
	}
}

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
			s := Supervisor{config: Config{Adapter: toolFreeFake{a}}, slots: make(chan struct{}, 1)}
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
