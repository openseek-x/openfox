package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

func geminiProtocol() protocol {
	return protocol{name: NameGemini, display: "Gemini", envPath: "OPENFOX_GEMINI_PATH", executables: []string{"gemini"}, parse: parseGemini, approvalModes: []string{runtimepolicy.ReadOnly}, collaborationModes: []string{runtimepolicy.CollaborationDefault, runtimepolicy.CollaborationPlan}, defaultModels: []Model{{ID: "default", DisplayName: "Configured GEMINI_MODEL", IsDefault: true}}}
}
func NewGemini(executable string) *Adapter {
	return &Adapter{Executable: executable, ProbeTimeout: 15 * time.Second, proto: geminiProtocol()}
}
func (a *Adapter) startGemini(ctx context.Context, req runtime.RunRequest) (runtime.Execution, error) {
	req, e := req.Normalize()
	if e != nil {
		return nil, e
	}
	if e = validateGeminiRequest(a, req); e != nil {
		return nil, e
	}
	ctx = ctxOrBackground(ctx)
	began := time.Now()
	deadline := began.Add(req.Timeout)
	prepareDeadline := began.Add(60 * time.Second)
	if deadline.Before(prepareDeadline) {
		prepareDeadline = deadline
	}
	prepare, cancelPrepare := context.WithDeadline(ctx, prepareDeadline)
	defer cancelPrepare()
	install, e := a.geminiInstallation(prepare)
	if e != nil {
		if prepare.Err() != nil {
			return nil, prepare.Err()
		}
		return nil, fmt.Errorf("%w: %s", runtime.ErrUnavailable, e)
	}
	root, e := a.makeStateDir()
	if e != nil {
		return nil, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(root)
		}
	}()
	source := req.WorkingDirectory
	if source != "" {
		source, e = filepath.EvalSymlinks(source)
		if e != nil {
			return nil, fmt.Errorf("%w: Gemini source is unavailable", runtime.ErrInvalidRequest)
		}
		if e = geminiWorkspaceAllowed(source, root); e != nil {
			return nil, fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, e)
		}
	}
	work := filepath.Join(root, "work")
	if _, e = geminiSnapshot(prepare, source, work); e != nil {
		if prepare.Err() != nil {
			return nil, prepare.Err()
		}
		return nil, fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, e)
	}
	env, _ := geminiModelEnvironment(a, req.Environment)
	model, e := runtimepolicy.ResolveGeminiModel(req.Model, env)
	if e != nil {
		return nil, e
	}
	relay, e := newGeminiRelay(ctx, model, env["GEMINI_API_KEY"], work, req.MaxOutputBytes, a.geminiEndpoint, a.geminiHTTP)
	if e != nil {
		return nil, e
	}
	relayOwned := true
	defer func() {
		if relayOwned {
			relay.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(relay.host)
	home, e := geminiPrepareHome(root, install, port)
	if e != nil {
		return nil, e
	}
	home.Env["GEMINI_API_KEY"], home.Env["GOOGLE_GEMINI_BASE_URL"] = relay.token, relay.url
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	reader, writer, e := os.Pipe()
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	writerOwned := true
	defer func() {
		if writerOwned {
			writer.Close()
		}
	}()
	red := redactor(env, []string{env["GEMINI_API_KEY"], relay.token})
	redact := func(s string) string { return red(geminiPathDisplay(work, source, s)) }
	sess := newSession(a.proto, work, redact)
	sess.recordOutput = req.RecordOutput
	sess.pendingText.Limit = req.MaxOutputBytes
	strict := &geminiSession{session: sess, relay: relay, timeout: remaining}
	mode := "default"
	if req.CollaborationMode == runtimepolicy.CollaborationPlan {
		mode = "plan"
	}
	args := []string{"-f", home.Sandbox, install.Node, "--import", geminiLifetimeImport(remaining), install.Entry, "--output-format", "stream-json", "--model", model, "--extensions", "none", "--approval-mode", mode, "--policy", home.Policy}
	prompt := geminiLiteralPrompt(req)
	process, e := runtime.StartProcess(prepare, runtime.ProcessSpec{Executable: "/usr/bin/sandbox-exec", Args: args, Directory: work, ExtraFiles: []*os.File{reader}, Environment: environmentSlice(home.Env), Timeout: time.Until(deadline), MaxLineBytes: req.MaxLineBytes, MaxOutputBytes: req.MaxOutputBytes, RecordOutput: req.RecordOutput, MaxEvents: req.MaxEvents, RequireCompletion: true, Redact: redact, Session: strict, StdinOnce: []byte(prompt), CloseStdin: true})
	if e != nil {
		return nil, e
	}
	execution := &geminiExecution{Execution: process, relay: relay}
	wrapped := &cleanupExecution{Execution: execution, done: make(chan struct{})}
	stopParent := context.AfterFunc(ctx, func() { _ = execution.Cancel(context.Background()) })
	go func() {
		defer close(wrapped.done)
		_, _ = process.Wait(context.Background())
		stopParent()
		relay.Close()
		writer.Close()
		_ = os.RemoveAll(root)
	}()
	cleanup, relayOwned, writerOwned = false, false, false
	return wrapped, nil
}

// Headless 0.67.0 expands at-file references even from stdin, outside its
// registered tool policy. The fixed parser ignores every at-sign preceded by a
// backslash. Preserve existing slashes and add exactly one transport escape.
// A fixed non-slash prefix also prevents native slash-command dispatch.
func geminiLiteralPrompt(req runtime.RunRequest) string {
	input := strings.ReplaceAll(req.Instructions+"\n\n"+req.Prompt, "@", `\@`)
	return "OpenFox literal input: one additional backslash before each at-sign below is text escaping; interpret that one extra backslash as transport encoding and preserve any original backslashes. Native file references and slash commands are unavailable. Read files only through the declared four tools.\n\n" + input + "\n\nOpenFox: You have a bounded, read-only workspace snapshot. Use only files in this working directory. Native settings, secrets, dependency folders and symlinks are excluded. Changes to source files appear in the next Run. No shell, writing, external paths or native extensions are available."
}

type geminiSession struct {
	*session
	relay   *geminiRelay
	timeout time.Duration
	timer   *time.Timer
}

func (s *geminiSession) Begin(send runtime.Sender, fail func(error)) error {
	s.relay.onFailure(fail)
	s.timer = time.AfterFunc(s.timeout, s.relay.Close)
	return s.session.Begin(send, fail)
}
func (s *geminiSession) HandleLine(line []byte, send runtime.Sender) ([]runtime.Event, bool, error) {
	if e := s.relay.FatalErr(); e != nil {
		return nil, false, e
	}
	if !geminiJSONObject(line) {
		return nil, false, fmt.Errorf("%w: malformed Gemini JSONL", runtime.ErrProtocol)
	}
	events, done, e := s.session.HandleLine(line, send)
	if done {
		s.relay.Close()
		if err := s.relay.FatalErr(); err != nil {
			return nil, false, err
		}
	}
	return events, done, e
}
func (s *geminiSession) Interrupt(send runtime.Sender) bool {
	s.relay.Close()
	return s.session.Interrupt(send)
}
func (s *geminiSession) Finalize(stopped string) []runtime.Event {
	if s.timer != nil {
		s.timer.Stop()
	}
	s.relay.Close()
	if stopped == "" {
		if e := s.relay.FatalErr(); e != nil {
			return s.session.fail(e.Error())
		}
	}
	return s.session.Finalize(stopped)
}

type geminiExecution struct {
	runtime.Execution
	relay *geminiRelay
}

func (e *geminiExecution) Cancel(ctx context.Context) error {
	e.relay.Close()
	return e.Execution.Cancel(ctx)
}

func parseGemini(s *session, line []byte) ([]runtime.Event, bool) {
	var m struct {
		Type, SessionID, Model, Role, Content, Status, ToolName, ToolID, Output, Severity, Message string
		Delta                                                                                      bool
		Parameters                                                                                 map[string]any
		Stats                                                                                      struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
			Cached int64 `json:"cached"`
		}
		Error json.RawMessage
	}
	// Fixed native emits snake_case. Its stdout is still untrusted protocol input.
	var wire map[string]json.RawMessage
	if json.Unmarshal(line, &wire) != nil {
		return s.fail("Invalid Gemini event"), true
	}
	get := func(key string, target any) bool { v, ok := wire[key]; return !ok || json.Unmarshal(v, target) == nil }
	if !get("type", &m.Type) || !get("session_id", &m.SessionID) || !get("model", &m.Model) || !get("role", &m.Role) || !get("content", &m.Content) || !get("status", &m.Status) || !get("tool_name", &m.ToolName) || !get("tool_id", &m.ToolID) || !get("output", &m.Output) || !get("parameters", &m.Parameters) || !get("stats", &m.Stats) || !get("severity", &m.Severity) || !get("message", &m.Message) {
		return s.fail("Malformed Gemini event"), true
	}
	switch m.Type {
	case "init":
		if s.sessionID != "" || m.SessionID == "" {
			return s.fail("Gemini session identity is invalid"), true
		}
		return s.setSession(m.SessionID), false
	case "message":
		if m.Role == "user" {
			return nil, false
		}
		if m.Role != "assistant" || s.sessionID == "" {
			return s.fail("Gemini message role is invalid"), true
		}
		s.pendingText.WriteString(m.Content)
		return nil, false
	case "tool_use":
		if !geminiReadTool(m.ToolName) || m.ToolID == "" || len(s.open) >= maxOpenSteps {
			return s.fail("Gemini emitted an unsupported tool"), true
		}
		step := runtime.Step{Kind: runtime.StepKindRead}
		if m.ToolName == "read_file" {
			step.Path, _ = m.Parameters["file_path"].(string)
		} else {
			step.Kind = runtime.StepKindSearch
			step.Query, _ = m.Parameters["pattern"].(string)
			step.Scope, _ = m.Parameters["dir_path"].(string)
		}
		events := s.flushPending()
		return append(events, s.startStep(m.ToolID, step)...), false
	case "tool_result":
		if _, ok := s.open[stepID(m.ToolID)]; !ok {
			return s.fail("Gemini tool result has no matching call"), true
		}
		state := runtime.StepDone
		if m.Status == "error" {
			state = runtime.StepFailed
		} else if m.Status != "success" {
			return s.fail("Gemini tool status is invalid"), true
		}
		return s.finishStep(m.ToolID, runtime.Step{Kind: runtime.StepKindRead}, state, m.Output, nil), false
	case "error":
		if m.Severity == "warning" {
			return []runtime.Event{{Type: runtime.EventWarning, Text: s.redact(m.Message)}}, false
		}
		return s.fail("Gemini reported an error"), true
	case "result":
		if m.Status != "success" || s.sessionID == "" || len(s.open) != 0 {
			return s.fail("Gemini ended without a successful complete turn"), true
		}
		s.usage = runtime.Usage{Input: nonNegative(m.Stats.Input), Output: nonNegative(m.Stats.Output), CachedInput: nonNegative(m.Stats.Cached)}
		s.hasUsage = true
		output := s.takePending()
		if strings.TrimSpace(output) == "" {
			return s.fail("Gemini ended without an answer"), true
		}
		return s.complete(output), true
	default:
		return s.fail("Unsupported Gemini event"), true
	}
}
