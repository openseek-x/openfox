package hermes

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeHermesSandbox(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	a := New()
	install, err := a.installation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	outsideDir, _ := filepath.EvalSymlinks(t.TempDir())
	outside := filepath.Join(outsideDir, "outside")
	_ = os.WriteFile(outside, []byte("outside-canary"), 0600)
	_ = os.WriteFile(filepath.Join(work, ".env"), []byte("secret-canary"), 0600)
	h, err := a.prepare(install, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(h.root)
	defer h.close()
	pathMap := map[string]string{"outside": outside, "project_env": filepath.Join(work, ".env"), "record": filepath.Join(install.Site, "openai-2.54.0.dist-info/RECORD"), "workspace_write": filepath.Join(work, "forbidden"), "workspace_read": filepath.Join(work, "ordinary.txt")}
	_ = os.WriteFile(pathMap["workspace_read"], []byte("readable"), 0600)
	pathMap["alias_workspace_read"] = strings.TrimPrefix(pathMap["workspace_read"], "/private")
	pathMap["alias_project_env"] = strings.TrimPrefix(pathMap["project_env"], "/private")
	pathMap["alias_outside"] = strings.TrimPrefix(outside, "/private")
	for _, name := range []string{".ssh", ".aws", ".hermes", "__pycache__"} {
		_ = os.Mkdir(filepath.Join(work, name), 0700)
		file := filepath.Join(work, name, "canary")
		_ = os.WriteFile(file, []byte("blocked"), 0600)
		pathMap[name] = file
	}
	paths, _ := json.Marshal(pathMap)
	code := `import os,json,subprocess
r={}
paths=json.loads(` + stringQuote(string(paths)) + `)
for k,p in paths.items():
 try:
  f=os.open(p,os.O_WRONLY|os.O_CREAT if k=='workspace_write' else os.O_RDONLY);os.close(f);r[k]=0
 except OSError as e:r[k]=e.errno
for p in ['/bin/bash','/bin/echo','/bin/launchctl']:
 try:subprocess.run([p,'-c','exit 0'],check=False,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL);r[p]=0
 except OSError as e:r[p]=e.errno
r['bash_exists']=os.path.isfile('/bin/bash')
r['bash_access']=os.access('/bin/bash',os.X_OK)
print(json.dumps(r))`
	cmd := exec.Command("/usr/bin/sandbox-exec", "-f", h.sandbox, install.Python, "-c", code)
	cmd.Dir = install.Source
	cmd.Env = h.env
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox probe: %v %s", err, data)
	}
	t.Logf("sandbox results=%s", data)
	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		t.Fatal("invalid probe output")
	}
	for _, name := range []string{"outside", "project_env", "alias_project_env", "alias_outside", "record", "workspace_write", "/bin/bash", "/bin/echo", "/bin/launchctl", ".ssh", ".aws", ".hermes", "__pycache__"} {
		if result[name] != float64(1) {
			t.Errorf("%s must be denied: %v", name, result[name])
		}
	}
	if result["workspace_read"] != float64(0) {
		t.Error("positive control: ordinary workspace file must be readable")
	}
	if result["alias_workspace_read"] != float64(0) {
		t.Error("standard alias positive control must be readable")
	}
	if result["bash_exists"] != true || result["bash_access"] != false {
		t.Error("unexpected shell discovery boundary")
	}
}

func stringQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
