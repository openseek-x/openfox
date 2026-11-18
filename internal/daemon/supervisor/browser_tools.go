package supervisor

import (
	"context"
	"encoding/json"
	"path/filepath"

	localbrowser "openfox/internal/daemon/browser"
	"openfox/internal/runtime"
)

func withBrowserTools(broker *localbrowser.Broker, run localbrowser.Run, runContext RunContext, tools []runtime.Tool, previous runtime.ToolCall) ([]runtime.Tool, runtime.ToolCall, string) {
	if broker == nil || runContext.AutomationOrigin || run.ActorID == "" || run.ConversationID == "" {
		return tools, previous, ""
	}
	switch runContext.Runtime {
	case "codex", "claude-code", "opencode":
	default:
		return tools, previous, ""
	}
	readOnly := runContext.ApprovalMode == "READ_ONLY" || runContext.CollaborationMode == "PLAN"
	tools = append(tools, localbrowser.Tools(readOnly)...)
	call := func(ctx context.Context, name, id string, args json.RawMessage) runtime.ToolResult {
		switch name {
		case "openfox_browser_tabs", "openfox_browser_observe", "openfox_browser_act", "openfox_browser_files":
			return broker.Call(ctx, run, name, id, args)
		}
		if previous != nil {
			return previous(ctx, name, id, args)
		}
		return runtime.TextResult("unknown tool", false)
	}
	return tools, call, localbrowser.Prompt
}

func (r *runState) browserRun(runContext RunContext, workspace, artifactDir string) (localbrowser.Run, func()) {
	run := r.browserRunIdentity(runContext)
	files := &browserFiles{roots: resolveRoots([]string{workspace, artifactDir}), artifactDir: artifactDir, transferRoot: filepath.Join(r.supervisor.config.BrowserRoot, "transfers"), downloadRoot: filepath.Join(r.supervisor.config.BrowserRoot, "downloads")}
	run.Invoke = files.invoke
	return run, files.close
}

func (r *runState) browserRunIdentity(rc RunContext) localbrowser.Run {
	return localbrowser.Run{OrganizationID: r.offer.OrganizationID, ConversationID: rc.ConversationID, ActorID: rc.ActorID, ID: r.offer.RunID, Generation: r.offer.Generation, ClientMessageIDs: append([]string(nil), rc.BrowserClientMessageIDs...)}
}
