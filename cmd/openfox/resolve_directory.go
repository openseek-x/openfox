package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/workdir"
)

// runResolveDirectory only reads this computer's configuration and local filesystem.
// The local caller receives the canonical path only on success.
func runResolveDirectory(args []string, deps computerDeps) error {
	flags := flag.NewFlagSet("computer resolve-directory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configDir := flags.String("config-dir", "", "local configuration directory")
	device := flags.String("device-id", "", "expected execution device")
	org := flags.String("organization-id", "", "expected device organization")
	selected := flags.String("path", "", "logical name or native directory selection")
	var allowed stringListFlag
	flags.Var(&allowed, "allow-root", "current Agent's allowed logical root; repeatable")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *device == "" || *org == "" || *selected == "" {
		return errors.New("usage: openfox computer resolve-directory --device-id id --organization-id id --path directory [--allow-root name] [--config-dir dir]")
	}
	reply := json.NewEncoder(deps.stdout)
	fail := func(reason string) error { return reply.Encode(map[string]any{"ok": false, "reason": reason}) }
	if *configDir == "" {
		dir, err := daemon.DefaultConfigDir()
		if err != nil {
			return fail("unavailable")
		}
		*configDir = dir
	}
	cfg, err := daemon.LoadDeviceConfig(filepath.Join(*configDir, daemon.DeviceConfigFileName))
	if errors.Is(err, os.ErrNotExist) {
		return fail("not_registered")
	}
	if err != nil {
		return fail("unavailable")
	}
	if cfg.DeviceID != *device || cfg.OrganizationID != *org {
		return fail("wrong_device")
	}
	roots, err := parseDirectoryRoots(directoryRootFlags(cfg.DirectoryRoots), cfg.WorkspaceRoot)
	if err != nil {
		return fail("unavailable")
	}
	directory, err := workdir.LocalDirectory(*selected, roots, allowed)
	if errors.Is(err, workdir.ErrPathOutsideAllowed) {
		return fail("outside_allowed_roots")
	}
	if err != nil {
		return fail("unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return reply.Encode(map[string]any{"ok": true, "path": directory.Path, "directory": directory.Name, "is_git": directory.IsGitRoot(ctx), "root_match": directory.Path == directory.Root})
}
