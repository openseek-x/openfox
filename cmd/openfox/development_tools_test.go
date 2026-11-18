package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/storage"
	"openfox/internal/devices"
)

func toolScript(t *testing.T, dir, name, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script fixture")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDevelopmentToolsCommandsAndChinesePaths(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "工具 bin")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("NODE_OPTIONS", "must-not-be-inherited")
	for name, version := range map[string]string{"node": "v22.0.0", "npm": "11.0.0", "python": "Python 3.13.0", "uv": "uv 0.9.0"} {
		toolScript(t, dir, name, "[ \"$#\" = 1 ] && [ \"$1\" = --version ] && [ -z \"$NODE_OPTIONS\" ] || exit 8\nprintf '%s\\n' '"+version+"'\n")
	}
	tools := probeDevelopmentTools(context.Background())
	if len(tools) != 4 {
		t.Fatalf("tools=%+v", tools)
	}
	for _, tool := range tools {
		if tool.Status != "AVAILABLE" || tool.CheckedAt.IsZero() || !strings.HasPrefix(tool.Path, dir) {
			t.Fatalf("tool=%+v", tool)
		}
	}
	if tools[2].Version != "Python 3.13.0" {
		t.Fatalf("python fallback=%+v", tools[2])
	}
	toolScript(t, dir, "python3", "printf 'Python 3.14.0\\n'\n")
	if got := probeDevelopmentTools(context.Background())[2]; got.Version != "Python 3.14.0" {
		t.Fatalf("python preference=%+v", got)
	}
}

func TestDevelopmentToolFailureBounds(t *testing.T) {
	for _, tc := range []struct{ name, script, want string }{
		{"missing", "", "MISSING"},
		{"failed", "printf 'private failure detail' >&2; exit 1", "ERROR"},
		{"empty", "exit 0", "ERROR"},
		{"timeout", "/bin/sleep 10 & wait", "TIMEOUT"},
		{"too_long", "i=0; while [ $i -lt 5000 ]; do printf x; i=$((i+1)); done", "ERROR"},
		{"control", "printf 'v1\\033[0m\\n'", "ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			if tc.script != "" {
				toolScript(t, dir, "node", tc.script)
			}
			start := time.Now()
			timeout := developmentToolTimeout
			if tc.name == "timeout" {
				timeout = 50 * time.Millisecond
			}
			got := probeDevelopmentTool(context.Background(), "node", []string{"node"}, timeout)
			if got.Status != tc.want || got.Version != "" || got.CheckedAt.IsZero() {
				t.Fatalf("got=%+v", got)
			}
			if time.Since(start) > timeout+time.Second {
				t.Fatal("probe exceeded bounded lifetime")
			}
		})
	}
}

func TestDevelopmentToolCanceled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	toolScript(t, dir, "node", "exec /bin/sleep 10")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	got := probeDevelopmentTool(ctx, "node", []string{"node"}, time.Second)
	if got.Status != "ERROR" || time.Since(start) > time.Second {
		t.Fatalf("canceled=%+v", got)
	}
}

func TestDevelopmentToolsSurviveConcurrentCapabilityUpdates(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	tools := []devices.DevelopmentTool{{Name: "node", Version: "v22.0.0", Path: "/工具/node", Status: "AVAILABLE", CheckedAt: time.Now().UTC().Truncate(time.Second)}}
	r := &capabilityReporter{source: source, prober: &fakeProber{}, developmentToolsProbe: func(context.Context) []devices.DevelopmentTool { return tools }}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); r.refresh(context.Background()) }()
		go func() { defer wg.Done(); r.setStorage(storage.Stats{TotalBytes: 12}) }()
	}
	wg.Wait()
	hb, err := source.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(hb.Capabilities, &doc); err != nil {
		t.Fatal(err)
	}
	if len(devices.ParseDevelopmentTools(hb.Capabilities)) != 1 || doc["storage"] == nil || doc["runtimes"] == nil {
		t.Fatalf("capabilities=%s", hb.Capabilities)
	}
	h := commandHandlers{reporter: r, heartbeat: &countingHeartbeat{}}
	if _, err := h.runtimeProbe(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	hb, _ = source.Current(context.Background())
	if len(devices.ParseDevelopmentTools(hb.Capabilities)) != 1 {
		t.Fatal("manual refresh lost tools")
	}
}

func TestDevelopmentToolsWithoutRuntime(t *testing.T) {
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{})
	r := &capabilityReporter{source: source, developmentToolsProbe: func(context.Context) []devices.DevelopmentTool {
		return []devices.DevelopmentTool{{Name: "node", Status: "MISSING", CheckedAt: time.Now().UTC()}}
	}}
	r.refresh(context.Background())
	hb, _ := source.Current(context.Background())
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(hb.Capabilities, &doc)
	if len(devices.ParseDevelopmentTools(hb.Capabilities)) != 1 || len(doc) != 1 {
		t.Fatalf("remote-only=%s", hb.Capabilities)
	}
}

func TestDevelopmentToolsWindowsNpmShim(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "工具 bin & path")
	cli := filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js")
	if err := os.MkdirAll(filepath.Dir(cli), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("// controlled fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(dir, "node.exe")
	if err := os.WriteFile(node, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "npm.cmd")
	noLookup := func(string) (string, error) { t.Fatal("adjacent node.exe should win"); return "", nil }
	exe, args, err := developmentToolCommand("npm", shim, "windows", noLookup)
	if err != nil || exe != node || len(args) != 2 || args[0] != cli || args[1] != "--version" {
		t.Fatalf("command=%q %q err=%v", exe, args, err)
	}
	if err := os.Remove(node); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(t.TempDir(), "node.exe")
	exe, args, err = developmentToolCommand("npm", shim, "windows", func(name string) (string, error) {
		if name != "node.exe" {
			t.Fatal(name)
		}
		return fallback, nil
	})
	if err != nil || exe != fallback || args[0] != cli {
		t.Fatalf("fallback=%q %q err=%v", exe, args, err)
	}
	if _, _, err := developmentToolCommand("npm", shim, "windows", func(string) (string, error) { return "node.cmd", nil }); err == nil {
		t.Fatal("relative/shim node accepted")
	}
	if err := os.Remove(cli); err != nil {
		t.Fatal(err)
	}
	if _, _, err := developmentToolCommand("npm", shim, "windows", noLookup); err == nil {
		t.Fatal("missing npm CLI accepted")
	}
	exe, args, err = developmentToolCommand("npm", shim, "linux", noLookup)
	if err != nil || exe != shim || len(args) != 1 {
		t.Fatal("non-Windows command changed")
	}
}
