package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon/workdir"
	"openfox/internal/runtime"
)

func TestNativeHermesWorkspaceIsolation(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	a := New()
	install, err := a.installation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// These two profiles and canaries are owned by this test; no existing Run
	// directory is enumerated or read. A short custom StateRoot avoids fallback.
	work, err := os.MkdirTemp("/private/tmp", "r5-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(work)
	otherAdapter := New()
	otherAdapter.StateRoot = filepath.Join(work, "s")
	otherWork, _ := filepath.EvalSymlinks(t.TempDir())
	other, err := otherAdapter.prepare(install, otherWork, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.close()
	defer os.RemoveAll(other.root)
	if !strings.HasPrefix(other.root, work+"/") {
		t.Fatal("custom StateRoot fixture unexpectedly used fallback")
	}
	otherFile := filepath.Join(other.profile, "config.yaml")
	if err = os.WriteFile(otherFile, []byte("OTHER_RUN_PRIVATE_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"/private/tmp", other.root, other.profile} {
		logical := "temp/" + strings.TrimPrefix(candidate, "/private/tmp/")
		if candidate == "/private/tmp" {
			logical = "temp"
		}
		selected, err := workdir.ResolveDirectory(logical, map[string]string{"temp": "/private/tmp"}, []string{"temp"})
		if err != nil {
			t.Fatalf("registered directory fixture: %v", err)
		}
		got, err := a.prepare(install, selected.Path, nil)
		if got != nil {
			got.close()
			_ = os.RemoveAll(got.root)
		}
		if !errors.Is(err, runtime.ErrInvalidRequest) {
			t.Fatalf("private/overlapping workspace accepted: %s (%v)", candidate, err)
		}
	}
	// A normal /private/tmp project remains usable. A foreign StateRoot beneath
	// a broad project grant still cannot become model-readable through a tool.
	h, err := a.prepare(install, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(h.root)
	defer h.close()
	if err = os.WriteFile(filepath.Join(work, "ordinary.txt"), []byte("PROJECT_READ_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(map[string]any{"cwd": work, "rg": install.RG, "workspace_fd": 3})
	prefix, _, _ := strings.Cut(bootstrap, "sys.argv = [\"hermes\", *command]")
	code := prefix + "\nother_path = " + fmt.Sprintf("%q", otherFile) + `
positive=json.loads(registry.dispatch('read_file',{'path':'ordinary.txt'},task_id='positive'))
assert 'PROJECT_READ_SENTINEL' in positive.get('content',''),positive
negative=json.loads(registry.dispatch('read_file',{'path':other_path},task_id='private'))
assert negative.get('host_rejected'),negative
assert 'OTHER_RUN_PRIVATE_CANARY' not in str(negative)
print(json.dumps({'ordinary_project_read':True,'other_run_refused':negative,'workspace_refusals':3}))
`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-f", h.sandbox, install.Python, "-c", code, string(options), "--")
	cmd.Dir, cmd.Env, cmd.ExtraFiles = install.Source, h.env, h.childFiles()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native isolation: %v %s", err, output)
	}
	t.Logf("native isolation: %s", output)
}
