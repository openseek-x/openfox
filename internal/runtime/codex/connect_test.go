package codex

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainruntime "openfox/internal/runtime"
)

func TestConfigOverrideArgs(t *testing.T) {
	got, err := configOverrideArgs([]string{"-c", "model_verbosity=low", "--config", "tools.web_search=true"})
	if err != nil || !reflect.DeepEqual(got, []string{"-c", "model_verbosity=low", "-c", "tools.web_search=true"}) {
		t.Fatalf("args = %v, %v", got, err)
	}
	for _, bad := range [][]string{
		{"-c"}, {"--yolo", "x=y"}, {"-c", "novalue"}, {"-c", "--dangerously-bypass=1"}, {"-c", "=x"},
	} {
		if _, err := configOverrideArgs(bad); !errors.Is(err, domainruntime.ErrInvalidRequest) {
			t.Errorf("configOverrideArgs(%q) error = %v", bad, err)
		}
	}
}

func TestRedactEnvironmentCoversRequestValues(t *testing.T) {
	redact := redactEnvironment(map[string]string{"PATH": "/usr/bin", "MY_TOKEN": "tok"}, "agent-secret", "abc", "agent-secret-longer")
	got := redact("x agent-secret-longer agent-secret abc tok /usr/bin")
	if got != "x [REDACTED] [REDACTED] abc [REDACTED] /usr/bin" {
		t.Fatalf("redacted = %q", got)
	}
}

// 包装脚本记录参数与 Agent 环境变量后去掉 -c 对，再交给假 app-server。
const argsWrapper = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_LOG.args"
printf '%s\n' "${AGENT_SECRET:-}" > "$FAKE_LOG.env"
while [ "$1" = "-c" ]; do shift 2; done
exec "$FIXTURE" "$@"
`

func TestStartPassesConfigOverridesAndRequestEnvironment(t *testing.T) {
	f := newFixture(t, "success")
	wrapper := filepath.Join(filepath.Dir(f.log), "codex-wrapper.sh")
	if err := os.WriteFile(wrapper, []byte(argsWrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	f.adapter.Environment["FIXTURE"] = f.adapter.Executable
	f.adapter.Executable = wrapper
	result, _ := f.run(t, domainruntime.RunRequest{
		Args:        []string{"-c", "model_reasoning_effort=high"},
		Environment: map[string]string{"AGENT_SECRET": "agent-value-1"}, EnvironmentAllowlist: []string{"AGENT_SECRET"},
		Timeout: 20 * time.Second,
	})
	if result.Status != domainruntime.StatusSucceeded {
		t.Fatalf("result = %#v", result)
	}
	args, err := os.ReadFile(f.log + ".args")
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Fields(string(args)); !reflect.DeepEqual(lines, []string{"-c", "model_reasoning_effort=high", "app-server"}) {
		t.Fatalf("app-server args = %q", lines)
	}
	if env, _ := os.ReadFile(f.log + ".env"); strings.TrimSpace(string(env)) != "agent-value-1" {
		t.Fatalf("agent env = %q", env)
	}
	if _, err := f.adapter.Start(t.Context(), domainruntime.RunRequest{Prompt: "x", WorkingDirectory: t.TempDir(), Args: []string{"--full-auto"}}); !errors.Is(err, domainruntime.ErrInvalidRequest) {
		t.Fatalf("unsafe args error = %v", err)
	}
}

func TestStartPassesConversationReasoningEffort(t *testing.T) {
	for _, effort := range []struct{ value, argument string }{
		{"DEFAULT", ""}, {"HIGH", "high"}, {"EXTRA_HIGH", "xhigh"}, {"ULTRA", "ultra"},
	} {
		t.Run(effort.value, func(t *testing.T) {
			fixture := newFixture(t, "success")
			wrapper := filepath.Join(filepath.Dir(fixture.log), "codex-wrapper.sh")
			if err := os.WriteFile(wrapper, []byte(argsWrapper), 0o700); err != nil {
				t.Fatal(err)
			}
			fixture.adapter.Environment["FIXTURE"] = fixture.adapter.Executable
			fixture.adapter.Executable = wrapper
			result, _ := fixture.run(t, domainruntime.RunRequest{ReasoningEffort: effort.value})
			if result.Status != domainruntime.StatusSucceeded {
				t.Fatalf("result = %+v", result)
			}
			args, err := os.ReadFile(fixture.log + ".args")
			if err != nil {
				t.Fatal(err)
			}
			want := "app-server"
			if effort.argument != "" {
				want = "-c model_reasoning_effort=" + effort.argument + " app-server"
			}
			if got := strings.Join(strings.Fields(string(args)), " "); got != want {
				t.Fatalf("args = %q, want %q", got, want)
			}
		})
	}
}

func TestRemoveStaleHomesReportsFreedBytesAndSkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, homeDirPrefix+"old")
	fresh := filepath.Join(root, homeDirPrefix+"new")
	for _, dir := range []string{stale, fresh} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), make([]byte, 64), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	link := filepath.Join(root, homeDirPrefix+"link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-staleHomeAfter - time.Hour)
	for _, path := range []string{stale, outside} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	removed, freed := RemoveStaleHomes(root, time.Now())
	if removed != 1 || freed != 64 {
		t.Fatalf("removed = %d, freed = %d", removed, freed)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh home removed: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink removed: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("symlink target removed: %v", err)
	}
}
