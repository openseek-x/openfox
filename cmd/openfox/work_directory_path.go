package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/commands"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/workdirpath"
)

func (h *commandHandlers) workspacePath(ctx context.Context, raw json.RawMessage) (any, error) {
	request, err := workdirpath.Parse(raw)
	org, _, ok := commands.FromContext(ctx)
	if err != nil || !ok || org != request.OrganizationID || !request.Live(h.clock()) {
		return nil, commands.ErrInvalidParams
	}
	if h.workDirectory == nil {
		return nil, &commands.CodedError{Code: "WORKSPACE_UNAVAILABLE"}
	}
	path, err := h.workDirectory(ctx, request.Scope)
	if err != nil {
		return nil, &commands.CodedError{Code: "WORKSPACE_UNAVAILABLE"}
	}
	envelope, err := workdirpath.Seal(request, path)
	if err != nil {
		return nil, &commands.CodedError{Code: "WORKSPACE_UNAVAILABLE"}
	}
	return envelope, nil
}

func runWorkDirectoryPath(args []string, deps computerDeps) error {
	flags := flag.NewFlagSet("computer workspace-path", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configDir := flags.String("config-dir", "", "local configuration directory")
	var scope workdirpath.Scope
	flags.StringVar(&scope.OrganizationID, "organization-id", "", "organization")
	flags.StringVar(&scope.DeviceID, "device-id", "", "execution device")
	flags.StringVar(&scope.SpaceID, "space-id", "", "space")
	flags.StringVar(&scope.ConversationID, "conversation-id", "", "conversation")
	flags.StringVar(&scope.AgentID, "agent-id", "", "agent")
	flags.StringVar(&scope.RunID, "run-id", "", "run")
	flags.Int64Var(&scope.Generation, "generation", 0, "run generation")
	flags.StringVar(&scope.UserID, "user-id", "", "authorized requesting user")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !scope.Valid() {
		return errors.New("invalid workspace-path request")
	}
	reply := json.NewEncoder(deps.stdout)
	fail := func() error { return reply.Encode(map[string]any{"ok": false, "reason": "workspace_unavailable"}) }
	if *configDir == "" {
		dir, err := daemon.DefaultConfigDir()
		if err != nil {
			return fail()
		}
		*configDir = dir
	}
	cfg, err := daemon.LoadDeviceConfig(filepath.Join(*configDir, daemon.DeviceConfigFileName))
	if err != nil || cfg.DeviceID != scope.DeviceID || cfg.OrganizationID != scope.OrganizationID {
		return fail()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	path, err := supervisor.ReadWorkDirectoryPath(ctx, filepath.Join(*configDir, "runs.db"), scope)
	if err != nil {
		return fail()
	}
	return reply.Encode(map[string]any{"ok": true, "path": path})
}
