package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The host changes only this test's source after the tool has checked the path.
// Both directory symlink replacement and in-place mutation cross the real
// bootstrap/official handler boundary, rather than a replica of its checks.
func TestNativeHermesSnapshotRace(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	for _, kind := range []string{"read_symlink", "search_symlink", "search_change"} {
		t.Run(kind, func(t *testing.T) {
			a := New()
			install, err := a.installation(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			work, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			stable := filepath.Join(work, "stable")
			if err = os.Mkdir(stable, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(stable, "sentinel.txt"), []byte(strings.Repeat("SAFE_CONTENT\n", 10000)), 0600); err != nil {
				t.Fatal(err)
			}
			h, err := a.prepare(install, work, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(h.root)
			defer h.close()
			outside := filepath.Join(h.root, "outside")
			if err = os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(outside, "sentinel.txt"), []byte("FORBIDDEN_SNAPSHOT_CANARY"), 0600); err != nil {
				t.Fatal(err)
			}
			ready, ack := filepath.Join(h.root, "ready"), filepath.Join(h.root, "ack")
			opts, _ := json.Marshal(map[string]any{"cwd": work, "rg": install.RG, "workspace_fd": 3})
			params, _ := json.Marshal(map[string]string{"kind": kind, "root": stable, "ready": ready, "ack": ack})
			prefix, _, _ := strings.Cut(bootstrap, "sys.argv = [\"hermes\", *command]")
			code := prefix + "\nprobe=json.loads(" + fmt.Sprintf("%q", string(params)) + ")\n" + `
positive=json.loads(registry.dispatch('read_file',{'path':probe['root']+'/sentinel.txt'},task_id='positive'))
assert 'SAFE_CONTENT' in positive.get('content',''),positive
triggered=False
stages=[]
original_init=Snapshot.__init__
def observe_init(self,*args,**kwargs):
    original_init(self,*args,**kwargs)
    stages.append(str(self.dest))
Snapshot.__init__=observe_init
def barrier():
    global triggered
    if triggered:return
    triggered=True
    pathlib.Path(probe['ready']).write_text('ready')
    until=time.monotonic()+5
    while not pathlib.Path(probe['ack']).exists():
        if time.monotonic()>until:raise RuntimeError('host mutation timeout')
        time.sleep(.005)
if probe['kind']=='read_symlink':
    guard=file_operations.open
    def intercepted_open(file,*args,**kwargs):
        if str(file)==probe['root']+'/sentinel.txt':barrier()
        return guard(file,*args,**kwargs)
    file_operations.open=intercepted_open
    result=registry.dispatch('read_file',{'path':probe['root']+'/sentinel.txt'},task_id='race')
else:
    if probe['kind']=='search_symlink':
        actual_open=os.open
        def intercepted_open(path,*args,**kwargs):
            if path=='stable' and kwargs.get('dir_fd') is not None:barrier()
            return actual_open(path,*args,**kwargs)
        os.open=intercepted_open
    else:
        source_inode=os.stat(probe['root']+'/sentinel.txt').st_ino
        actual_read=os.read
        def intercepted_read(fd,length):
            data=actual_read(fd,length)
            if data and os.fstat(fd).st_ino==source_inode:barrier()
            return data
        os.read=intercepted_read
    result=registry.dispatch('search_files',{'path':probe['root'],'pattern':'FORBIDDEN_SNAPSHOT_CANARY'},task_id='race')
assert triggered
assert 'FORBIDDEN_SNAPSHOT_CANARY' not in result,result
parsed=json.loads(result)
assert parsed.get('error'),parsed
assert all(not pathlib.Path(stage).exists() for stage in stages),stages
assert not search_lock.locked()
print(json.dumps({'case':probe['kind'],'tool_result':parsed,'snapshots_removed':True,'private_canary_absent':True}))
`
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-f", h.sandbox, install.Python, "-c", code, string(opts), "--")
			cmd.Dir, cmd.Env, cmd.ExtraFiles = install.Source, h.env, h.childFiles()
			done := make(chan error, 1)
			go func() {
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					if _, err := os.Stat(ready); err == nil {
						break
					}
					time.Sleep(5 * time.Millisecond)
				}
				if _, err := os.Stat(ready); err != nil {
					done <- err
					return
				}
				var err error
				if strings.HasSuffix(kind, "symlink") {
					err = os.Rename(stable, stable+"-saved")
					if err == nil {
						err = os.Symlink(outside, stable)
					}
				} else {
					var file *os.File
					file, err = os.OpenFile(filepath.Join(stable, "sentinel.txt"), os.O_APPEND|os.O_WRONLY, 0600)
					if err == nil {
						_, err = file.WriteString("CONCURRENT_APPEND")
						_ = file.Close()
					}
				}
				if err == nil {
					err = os.WriteFile(ack, []byte("done"), 0600)
				}
				done <- err
			}()
			output, err := cmd.CombinedOutput()
			hostErr := <-done
			if err != nil || hostErr != nil {
				t.Fatalf("native=%v host=%v output=%s", err, hostErr, output)
			}
			t.Logf("native mutation: %s", output)
		})
	}
}
