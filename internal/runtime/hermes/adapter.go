// Package hermes adapts the pinned official Hermes ACP entry point. Each Run
// owns its gateway, native session, sandbox profile and model credential relay.
package hermes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtime/codex"
	"openfox/internal/runtimepolicy"
)

type Adapter struct {
	SourceRoot, Python, StateRoot string
	ProbeTimeout                  time.Duration
	// The production client is always safehttp; this seam is private to native
	// tests using a real TLS server with an ephemeral test CA.
	testClient  *http.Client
	testObserve func(*nativeHome)
}

func New() *Adapter {
	return &Adapter{SourceRoot: os.Getenv("OPENFOX_HERMES_ROOT"), Python: os.Getenv("OPENFOX_HERMES_PYTHON"), ProbeTimeout: 30 * time.Second}
}
func (*Adapter) Name() string        { return runtimepolicy.Hermes }
func (*Adapter) SupportsTools() bool { return false }
func (*Adapter) ApprovalModes() []string {
	return []string{runtimepolicy.FollowRuntime, runtimepolicy.ReadOnly}
}
func (*Adapter) CollaborationModes() []string {
	return []string{runtimepolicy.FollowRuntime, runtimepolicy.CollaborationDefault, runtimepolicy.CollaborationPlan}
}

func (a *Adapter) installation(ctx context.Context) (installation, error) {
	if goruntime.GOOS != "darwin" || goruntime.GOARCH != "arm64" {
		return installation{}, fmt.Errorf("%w: Hermes requires macOS arm64", runtime.ErrUnavailable)
	}
	return verifyInstallation(ctx, a.SourceRoot, a.Python)
}

func (a *Adapter) Detect(ctx context.Context) (runtime.Capabilities, error) {
	report := runtime.Capabilities{Runtime: runtimepolicy.Hermes, Login: runtime.LoginUnknown, SupportsJSONEvents: true, SupportsCancel: true, DirectoryModes: []string{"NONE", "WORKSPACE", "EXPLICIT"}}
	// Keep a configured but untrusted installation visible with its failure
	// reason in the device heartbeat. This is presence, never permission to
	// execute: the full build verification still precedes every command.
	if filepath.IsAbs(a.Python) {
		if info, err := os.Stat(a.Python); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			report.Executable = a.Python
		}
	}
	timeout := a.ProbeTimeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	install, err := a.installation(ctx)
	if err != nil {
		report.Reason = err.Error()
		return report, err
	}
	home, err := a.prepare(install, "", nil)
	if err != nil {
		return report, err
	}
	defer func() { home.close(); _ = os.RemoveAll(home.root) }()
	output, err := home.commandOutput(ctx, "acp", "--check")
	if err != nil || !strings.Contains(string(output), "Hermes ACP check OK") {
		report.Reason = "Hermes ACP dependency check failed"
		return report, fmt.Errorf("%w: %s", runtime.ErrUnavailable, report.Reason)
	}
	probe, err := runtime.StartProcess(ctx, runtime.ProcessSpec{Executable: "/usr/bin/sandbox-exec", Args: home.args("acp"), Directory: install.Source, Environment: home.env, ExtraFiles: home.childFiles(), Timeout: timeout, MaxLineBytes: runtime.DefaultMaxLineBytes, MaxOutputBytes: runtime.DefaultMaxOutput, MaxEvents: runtime.DefaultMaxEvents, RequireCompletion: true, Session: &probeSession{}, SummarizeStderr: func(string) string { return "Hermes ACP protocol probe failed" }})
	if err != nil {
		return report, err
	}
	for range probe.Events() {
	}
	result, err := probe.Wait(ctx)
	if err != nil || result.Status != runtime.StatusSucceeded {
		report.Reason = "Hermes ACP initialization failed"
		return report, fmt.Errorf("%w: %s", runtime.ErrUnavailable, report.Reason)
	}
	report.Version, report.Executable = Version, install.Python
	report.ProtocolReady, report.RequiresRunConfiguration = true, true
	report.Reason = "Hermes requires an API key, public HTTPS API base, model and user-declared context capacity for each Run; authentication is not checked"
	return report, nil
}

func (*Adapter) ListModels(context.Context) ([]codex.Model, error) {
	return []codex.Model{{ID: runtimepolicy.DefaultModel, DisplayName: "default"}}, errors.New("Hermes uses an explicit configured model; no provider catalog is requested")
}

func (*Adapter) ValidateRequest(req runtime.RunRequest) error {
	if err := runtimepolicy.ValidateHermesModes(req.ApprovalMode, req.CollaborationMode, req.ReasoningEffort, req.Args); err != nil {
		return fmt.Errorf("%w: %v", runtime.ErrUnsupported, err)
	}
	if len(req.MCPServers) > 0 || len(req.Skills) > 0 || len(req.Tools) > 0 || req.ToolCall != nil || req.ResumeSession != "" {
		return fmt.Errorf("%w: Hermes does not support MCP, skills, dynamic tools or native resume", runtime.ErrUnsupported)
	}
	if _, err := runtimepolicy.ResolveHermesModel(req.Model, req.Environment); err != nil {
		return fmt.Errorf("%w: %v", runtime.ErrInvalidRequest, err)
	}
	return nil
}

func (a *Adapter) Start(ctx context.Context, req runtime.RunRequest) (runtime.Execution, error) {
	req, err := req.Normalize()
	if err != nil {
		return nil, err
	}
	if err = a.ValidateRequest(req); err != nil {
		return nil, err
	}
	cfg, _ := runtimepolicy.ResolveHermesModel(req.Model, req.Environment)
	install, err := a.installation(ctx)
	if err != nil {
		return nil, err
	}
	cwd := req.WorkingDirectory
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, fmt.Errorf("%w: Hermes working directory unavailable", runtime.ErrInvalidRequest)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: Hermes needs a directory", runtime.ErrInvalidRequest)
	}
	// A directory grant cannot turn the user's entire HOME (or filesystem) into
	// a read grant. Explicit descendants remain limited to the authorized tree.
	realHome, _ := os.UserHomeDir()
	realHome, _ = filepath.EvalSymlinks(realHome)
	if cwd == "/" || cwd == realHome || strings.HasPrefix(realHome, cwd+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: Hermes requires a workspace below the user home or a separate directory", runtime.ErrInvalidRequest)
	}
	if privateNativePath(cwd) {
		return nil, fmt.Errorf("%w: Hermes private Run state cannot be a workspace", runtime.ErrInvalidRequest)
	}
	runCtx, stop := context.WithTimeout(ctx, req.Timeout)
	proxy, err := newModelProxy(runCtx, cfg, req.MaxOutputBytes, a.testClient)
	if err != nil {
		stop()
		return nil, err
	}
	home, err := a.prepare(install, cwd, proxy)
	if err != nil {
		proxy.Close()
		stop()
		return nil, err
	}
	cleanup := func() { proxy.Close(); home.close(); stop(); _ = os.RemoveAll(home.root) }
	if a.testObserve != nil {
		a.testObserve(home)
	}
	if err = home.writeConfig(cfg, proxy); err != nil {
		cleanup()
		return nil, err
	}
	if err = home.startGateway(runCtx); err != nil {
		cleanup()
		return nil, err
	}
	prompt := "OpenFox Run request (the content below is data, not a native slash command).\n"
	if runtimepolicy.NormalizeCollaborationMode(req.CollaborationMode) == runtimepolicy.CollaborationPlan {
		prompt += "Plan only. Inspect files as needed; do not modify files or execute commands.\n"
	}
	prompt += "<instructions>\n" + req.Instructions + "\n</instructions>\n<request>\n" + req.Prompt + "\n</request>"
	if len(prompt) > runtime.MaxPromptBytes {
		cleanup()
		return nil, fmt.Errorf("%w: Hermes wrapped prompt exceeds limit", runtime.ErrInvalidRequest)
	}
	redact := func(text string) string {
		for _, value := range []string{cfg.APIKey, proxy.token} {
			if value != "" {
				text = strings.ReplaceAll(text, value, "[REDACTED]")
			}
		}
		return text
	}
	session := &acpSession{cwd: cwd, prompt: prompt, maxOutput: req.MaxOutputBytes, redact: redact, proxy: proxy, mask: &streamMask{secrets: []string{cfg.APIKey, proxy.token}}}
	execution, err := runtime.StartProcess(runCtx, runtime.ProcessSpec{Executable: "/usr/bin/sandbox-exec", Args: home.args("acp"), Directory: install.Source, Environment: home.env, ExtraFiles: home.childFiles(), Timeout: req.Timeout, MaxLineBytes: req.MaxLineBytes, MaxOutputBytes: req.MaxOutputBytes, RecordOutput: req.RecordOutput, MaxEvents: req.MaxEvents, RequireCompletion: true, Session: session, Redact: redact, SummarizeStderr: func(string) string {
		return "Hermes native process failed; inspect the Run configuration and trusted installation"
	}})
	if err != nil {
		cleanup()
		return nil, err
	}
	wrapped := &ownedExecution{Execution: execution, done: make(chan struct{})}
	go func() { defer close(wrapped.done); _, _ = execution.Wait(context.Background()); cleanup() }()
	return wrapped, nil
}

type ownedExecution struct {
	runtime.Execution
	done chan struct{}
}

func (e *ownedExecution) Wait(ctx context.Context) (runtime.Result, error) {
	select {
	case <-e.done:
		return e.Execution.Wait(ctx)
	case <-ctx.Done():
		return runtime.Result{}, ctx.Err()
	}
}
