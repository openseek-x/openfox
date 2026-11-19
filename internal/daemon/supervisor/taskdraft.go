package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

// DraftTask uses the selected local runtime in an empty temporary directory.
// Its only output is a structured draft returned to the authenticated command endpoint.
func (s *Supervisor) DraftTask(ctx context.Context, raw json.RawMessage) (any, error) {
	var rc RunContext
	if err := json.Unmarshal(raw, &rc); err != nil {
		return nil, err
	}
	adapter := s.adapterFor(rc.Runtime)
	if adapter == nil {
		return nil, runtime.ErrUnsupported
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	dir, err := os.MkdirTemp("", "openfox-task-draft-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	mode := rc.ApprovalMode
	if runtime.RequireApprovalMode(adapter.Name(), adapterApprovalModes(adapter), runtimepolicy.ReadOnly) == nil {
		mode = runtimepolicy.ReadOnly
	}
	if err := runtime.RequireApprovalMode(adapter.Name(), adapterApprovalModes(adapter), mode); err != nil {
		return nil, err
	}
	settings := buildRunSettings(rc)
	if s.config.MachineProxy != nil && rc.Runtime != runtimepolicy.Hermes {
		env, err := s.config.MachineProxy()
		if err != nil {
			return nil, err
		}
		if rc.Runtime == runtimepolicy.Gemini {
			for _, v := range env {
				if v != "" {
					return nil, runtime.ErrUnsupported
				}
			}
		} else {
			applyMachineProxy(&settings, env)
		}
	}
	run, err := adapter.Start(ctx, runtime.RunRequest{
		WorkingDirectory: dir, ApprovalMode: mode, Prompt: "Return only one JSON object with a title (1–200 characters) and description (1–10000 characters) for the requested task. Use the language of the brief. Include assignee_email or assignee_name only if explicitly specified in the brief. Use no tools.\n\n" + rc.Prompt,
		Instructions: rc.Instructions, Model: rc.Model, ReasoningEffort: rc.ReasoningEffort, Environment: settings.environment, EnvironmentAllowlist: settings.allowlist, Args: settings.args, Timeout: 5 * time.Minute,
	})
	if err != nil {
		return nil, err
	}
	for {
		select {
		case _, open := <-run.Events():
			if open {
				continue
			}
			result, err := run.Wait(ctx)
			if err != nil {
				return nil, err
			}
			if result.Status != runtime.StatusSucceeded {
				return nil, errors.New("task drafting failed")
			}
			draft, err := taskDraftArguments(result.Output)
			if err != nil {
				return nil, err
			}
			return json.RawMessage(newRedactor(settings.secrets).json(draft)), nil
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = run.Cancel(stop)
			return nil, ctx.Err()
		}
	}
}

// The device forwards only the generated content. Space and task settings stay on the server.
func taskDraftArguments(output string) (json.RawMessage, error) {
	text := strings.TrimSpace(output)
	if fenced, ok := strings.CutPrefix(text, "```json"); ok && strings.HasSuffix(fenced, "```") {
		text = strings.TrimSpace(strings.TrimSuffix(fenced, "```"))
	}
	var draft struct {
		Title         string `json:"title"`
		Description   string `json:"description"`
		AssigneeEmail string `json:"assignee_email,omitempty"`
		AssigneeName  string `json:"assignee_name,omitempty"`
	}
	if len(text) > 65536 || json.Unmarshal([]byte(text), &draft) != nil || strings.TrimSpace(draft.Title) == "" || strings.TrimSpace(draft.Description) == "" {
		return nil, errors.New("the Agent did not return a task title and description")
	}
	return json.Marshal(draft)
}
