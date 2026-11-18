package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"openfox/internal/devices"
)

const developmentToolTimeout = 2 * time.Second
const developmentToolOutputLimit = 4096

// developmentToolOutput bounds combined stdout/stderr without allowing a verbose
// command to allocate unbounded memory. CommandContext still bounds its lifetime.
type developmentToolOutput struct {
	data      []byte
	truncated bool
}

func (b *developmentToolOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := developmentToolOutputLimit - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}

// Only fixed version commands are supported. Resolve against the daemon's PATH;
// do not run a shell, install tools, or load the user's shell startup scripts.
func probeDevelopmentTools(ctx context.Context) []devices.DevelopmentTool {
	result := make([]devices.DevelopmentTool, 0, 4)
	for _, spec := range []struct {
		name     string
		binaries []string
	}{
		{"node", []string{"node"}}, {"npm", []string{"npm"}},
		{"python", []string{"python3", "python"}}, {"uv", []string{"uv"}},
	} {
		result = append(result, probeDevelopmentTool(ctx, spec.name, spec.binaries, developmentToolTimeout))
	}
	return result
}

func probeDevelopmentTool(ctx context.Context, name string, binaries []string, timeout time.Duration) (result devices.DevelopmentTool) {
	result.Name, result.Status = name, "MISSING"
	defer func() { result.CheckedAt = time.Now().UTC().Truncate(time.Second) }()
	for _, binary := range binaries {
		path, err := exec.LookPath(binary)
		if errors.Is(err, exec.ErrNotFound) {
			continue
		}
		if err != nil || !filepath.IsAbs(path) {
			result.Status = "ERROR"
			return result
		}
		result.Path, result.Status = path, "ERROR"
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		executable, args, err := developmentToolCommand(name, path, runtime.GOOS, exec.LookPath)
		if err != nil {
			return result
		}
		cmd := exec.CommandContext(probeCtx, executable, args...)
		configureDevelopmentToolProcess(cmd)
		cmd.WaitDelay = 250 * time.Millisecond
		// Preserve the daemon environment (including version-manager PATH), but do
		// not allow Node startup injection during a read-only version request.
		cmd.Env = make([]string, 0, len(os.Environ()))
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "NODE_OPTIONS=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		var output developmentToolOutput
		cmd.Stdout, cmd.Stderr = &output, &output
		err = cmd.Run()
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			result.Status = "TIMEOUT"
			return result
		}
		if err != nil || output.truncated {
			return result
		}
		version := strings.TrimSpace(strings.SplitN(string(output.data), "\n", 2)[0])
		if version == "" || len(version) > 256 || strings.IndexFunc(version, unicode.IsControl) >= 0 {
			return result
		}
		result.Version, result.Status = version, "AVAILABLE"
		return result
	}
	return result
}

// npm's official Windows shim executes node.exe + node_modules/npm/bin/npm-cli.js.
// Resolve those fixed files rather than executing npm.cmd through a shell.
// https://github.com/npm/cli/blob/latest/bin/npm.cmd
func developmentToolCommand(name, path, goos string, lookPath func(string) (string, error)) (string, []string, error) {
	if goos != "windows" || name != "npm" || !strings.EqualFold(filepath.Ext(path), ".cmd") {
		return path, []string{"--version"}, nil
	}
	cli := filepath.Join(filepath.Dir(path), "node_modules", "npm", "bin", "npm-cli.js")
	if info, err := os.Stat(cli); err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("npm CLI script is unavailable")
	}
	node := filepath.Join(filepath.Dir(path), "node.exe")
	if info, err := os.Stat(node); err != nil || !info.Mode().IsRegular() {
		var err error
		node, err = lookPath("node.exe")
		if err != nil || !filepath.IsAbs(node) || !strings.EqualFold(filepath.Ext(node), ".exe") {
			return "", nil, errors.New("npm Node executable is unavailable")
		}
	}
	return node, []string{cli, "--version"}, nil
}
