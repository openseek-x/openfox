package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"

	domainruntime "openfox/internal/runtime"
)

// installFakeCodex 在 dir 下写一个可执行的 codex；broken=true 时 --version 失败。
func installFakeCodex(t *testing.T, dir string, version string, broken bool) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "codex")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) printf 'codex-cli %s\\n' '" + version + "' ;;\n  login) echo 'Logged in using fixture' ;;\n  app-server) echo 'codex app-server' ;;\nesac\nexit 0\n"
	if broken {
		script = "#!/bin/sh\nexit 3\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeDiscovery(home string, env map[string]string, goos string, shellResult string) *discovery {
	return &discovery{
		getenv: func(key string) string { return env[key] },
		home:   home, goos: goos,
		loginShell: func(context.Context, string) string { return shellResult },
	}
}

func discoveredPath(t *testing.T, adapter *Adapter) string {
	t.Helper()
	report, err := adapter.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v (report %+v)", err, report)
	}
	return report.Executable
}

func TestDiscoveryFindsChatGPTBundledCLIWhenPathIsMinimal(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	home := t.TempDir()
	want := installFakeCodex(t, filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex-cli", "bin"), "0.1.0", false)
	adapter := New("")
	adapter.discovery = fakeDiscovery(home, map[string]string{"PATH": "/usr/bin:/bin"}, "darwin", "")
	if got := discoveredPath(t, adapter); got != want {
		t.Fatalf("executable = %s, want %s", got, want)
	}
}

func TestDiscoveryOrderAndProbeFailedFallback(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	home := t.TempDir()
	pathDir := filepath.Join(home, "path-bin")
	nvmOld := filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin")
	nvmNew := filepath.Join(home, ".nvm", "versions", "node", "v22.1.0", "bin")
	shellDir := filepath.Join(home, "shell-bin")

	// PATH 中的候选 --version 失败：必须跳过，而不是直接报错。
	installFakeCodex(t, pathDir, "", true)
	shellCodex := installFakeCodex(t, shellDir, "0.2.0", false)
	installFakeCodex(t, nvmOld, "0.0.1", false)
	newest := installFakeCodex(t, nvmNew, "0.0.2", false)

	t.Run("login shell before well-known directories", func(t *testing.T) {
		adapter := New("")
		adapter.discovery = fakeDiscovery(home, map[string]string{"PATH": pathDir, "SHELL": "/bin/sh"}, "darwin", shellCodex)
		if got := discoveredPath(t, adapter); got != shellCodex {
			t.Fatalf("executable = %s, want login shell result %s", got, shellCodex)
		}
	})
	t.Run("newest nvm version when shell finds nothing", func(t *testing.T) {
		adapter := New("")
		adapter.discovery = fakeDiscovery(home, map[string]string{"PATH": pathDir, "SHELL": "/bin/sh"}, "darwin", "")
		if got := discoveredPath(t, adapter); got != newest {
			t.Fatalf("executable = %s, want %s", got, newest)
		}
	})
}

func TestExplicitExecutableDoesNotFallBack(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX shell")
	}
	home := t.TempDir()
	installFakeCodex(t, filepath.Join(home, ".local", "bin"), "0.1.0", false)
	for name, configure := range map[string]func(*Adapter, map[string]string){
		"flag": func(a *Adapter, _ map[string]string) { a.Executable = filepath.Join(home, "missing", "codex") },
		"env": func(_ *Adapter, env map[string]string) {
			env["OPENFOX_CODEX_PATH"] = filepath.Join(home, "missing", "codex")
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := map[string]string{"PATH": "/usr/bin:/bin"}
			adapter := New("")
			configure(adapter, env)
			adapter.discovery = fakeDiscovery(home, env, "darwin", "")
			if _, err := adapter.Detect(context.Background()); !errors.Is(err, domainruntime.ErrUnavailable) {
				t.Fatalf("Detect error = %v, want ErrUnavailable without fallback", err)
			}
		})
	}
}

func TestDiscoveryReportsNotFound(t *testing.T) {
	adapter := New("")
	adapter.discovery = fakeDiscovery(t.TempDir(), map[string]string{"PATH": t.TempDir()}, "linux", "")
	_, err := adapter.Detect(context.Background())
	if !errors.Is(err, domainruntime.ErrUnavailable) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("Detect error = %v", err)
	}
}

func TestWithExecutableDirPrependsOnce(t *testing.T) {
	env := withExecutableDir(map[string]string{"PATH": "/usr/bin:/bin"}, "/home/u/.nvm/versions/node/v22/bin/codex")
	if env["PATH"] != "/home/u/.nvm/versions/node/v22/bin:/usr/bin:/bin" {
		t.Fatalf("PATH = %s", env["PATH"])
	}
	again := withExecutableDir(env, "/home/u/.nvm/versions/node/v22/bin/codex")
	if again["PATH"] != env["PATH"] {
		t.Fatalf("PATH duplicated: %s", again["PATH"])
	}
	if empty := withExecutableDir(map[string]string{}, "/opt/codex/bin/codex"); !strings.HasPrefix(empty["PATH"], "/opt/codex/bin:/usr/bin") {
		t.Fatalf("empty PATH = %s", empty["PATH"])
	}
}

func TestLoginShellLookupParsesMarker(t *testing.T) {
	if stdruntime.GOOS == "windows" {
		t.Skip("POSIX shell only")
	}
	dir := t.TempDir()
	codexPath := installFakeCodex(t, dir, "0.1.0", false)
	// 假 shell：忽略 -ilc，先打印杂散输出，再执行脚本（PATH 指向 fixture 目录）。
	shell := filepath.Join(dir, "fake-shell")
	script := "#!/bin/sh\necho 'motd noise'\nPATH='" + dir + "':/usr/bin:/bin\nexport PATH\nshift\nexec /bin/sh -c \"$1\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := loginShellLookup(context.Background(), shell); got != codexPath {
		t.Fatalf("login shell lookup = %q, want %q", got, codexPath)
	}
}
