package hermes

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeHermesSearchControls(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	a := New()
	install, err := a.installation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	for name, content := range map[string]string{
		"sentinel.txt": "SEARCH_SENTINEL\n", ".git/config": "GIT_CONTROL_CANARY\n",
		".git/info/exclude": "# GIT_CONTROL_CANARY\n", ".ignore": "!.hidden\n", ".hidden": "HIDDEN_CANARY\n",
		".gitignore": "ignored.txt\n", "nested/.git/marker": "", "nested/ignored.txt": "NESTED_CANARY\n",
	} {
		path := filepath.Join(work, name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := a.prepare(install, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(h.root)
	defer h.close()
	options, _ := json.Marshal(map[string]any{"cwd": work, "rg": install.RG, "workspace_fd": 3})
	prefix, _, _ := strings.Cut(bootstrap, "sys.argv = [\"hermes\", *command]")
	code := prefix + `
from tools import interrupt
stages=[]
original_init=Snapshot.__init__
def observe_init(self,*args,**kwargs):
    original_init(self,*args,**kwargs)
    stages.append(str(self.dest))
Snapshot.__init__=observe_init
def search(args,task):
    raw=registry.dispatch('search_files',args,task_id=task)
    assert not any(stage in raw for stage in stages),raw
    return json.loads(raw)
repeated=[search({'pattern':'SEARCH_SENTINEL'},'repeat') for _ in range(4)]
assert all(row.get('total_count')==1 for row in repeated[:3]),repeated
assert '_warning' in repeated[2] and repeated[3].get('error')=='repeated_search',repeated
assert len(stages)==3 and len(set(stages))==3,stages
for path in ['.git','.git/config','.GIT/CONFIG']:
    assert search({'pattern':'GIT_CONTROL_CANARY','path':path},'git-'+path).get('host_rejected')
for glob in ['*','.git/**',None]:
    args={'pattern':'GIT_CONTROL_CANARY'}
    if glob is not None:args['file_glob']=glob
    assert search(args,'glob-'+str(glob)).get('total_count')==0
assert search({'pattern':'HIDDEN_CANARY'},'hidden').get('total_count')==1
assert search({'pattern':'NESTED_CANARY'},'nested').get('total_count')==1

# A second request does not start a second copy or queue behind a busy search.
entered,release=threading.Event(),threading.Event()
original_copy=Snapshot.copy_file
def blocked_copy(self,parent,name,target):
    if name=='sentinel.txt':
        entered.set()
        assert release.wait(5)
    return original_copy(self,parent,name,target)
Snapshot.copy_file=blocked_copy
first=[]
worker=threading.Thread(target=lambda:first.append(search({'pattern':'SEARCH_SENTINEL'},'busy-first')))
worker.start()
assert entered.wait(5)
try:
    busy=search({'pattern':'SEARCH_SENTINEL'},'busy-second')
    assert busy.get('error')=='search_busy_retry_after_current_search',busy
finally:
    release.set();worker.join(5);Snapshot.copy_file=original_copy
assert not worker.is_alive() and first[0].get('total_count')==1,first

def cancelled_copy(self,parent,name,target):
    if name=='sentinel.txt':interrupt.set_interrupt(True)
    return original_copy(self,parent,name,target)
Snapshot.copy_file=cancelled_copy
try:
    cancelled=search({'pattern':'SEARCH_SENTINEL'},'copy-cancel')
    assert cancelled.get('host_rejected') and cancelled.get('error')=='cancelled',cancelled
finally:
    interrupt.set_interrupt(False);Snapshot.copy_file=original_copy

# Stop only this test's real fixed rg child to inject a silent dependency.
# The official native transport then has to timeout/interrupt and reap it.
actual_processes=file_operations_search.subprocess
native_transport=original_rg_native
observed=[]
class StopRG:
    def __getattr__(self,name):return getattr(actual_processes,name)
    def Popen(self,*args,**kwargs):
        p=actual_processes.Popen(*args,**kwargs)
        os.kill(p.pid,signal.SIGSTOP)
        observed.append(p)
        if fault=='cancel':interrupt.set_interrupt(True)
        return p
file_operations_search.subprocess=StopRG()
results=[]
try:
    for fault in ['cancel','timeout']:
        if fault=='timeout':
            original_rg_native=lambda self,argv,fetch_limit,timeout,merge_stderr=False:native_transport(self,argv,fetch_limit,.001,merge_stderr)
        result=search({'pattern':'SEARCH_SENTINEL'},'rg-'+fault)
        assert result.get('host_rejected') and 'cancelled or timed out' in result.get('error',''),result
        child=observed[-1]
        assert child.returncode is not None
        try:os.waitpid(child.pid,os.WNOHANG)
        except ChildProcessError:reaped=True
        else:reaped=False
        assert reaped
        results.append({'fault':fault,'pid':child.pid,'pgid':getattr(child,'_hermes_pgid',None),'exit':child.returncode,'reaped':reaped})
        interrupt.set_interrupt(False)
finally:
    file_operations_search.subprocess=actual_processes
    original_rg_native=native_transport
    interrupt.set_interrupt(False)
    for child in observed:
        if child.poll() is None:child.kill();child.wait(timeout=3)
assert all(not pathlib.Path(stage).exists() for stage in stages),stages
assert not search_lock.locked()
print(json.dumps({'repeated_warning_at':3,'repeated_refusal_at':4,'unique_initial_snapshots':3,'git_control_refusals':3,'git_glob_checks':3,'hidden_and_nested':True,'busy_refused':True,'copy_cancelled':True,'native_rg':results,'all_snapshots_removed':True}))
`
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-f", h.sandbox, install.Python, "-c", code, string(options), "--")
	cmd.Dir, cmd.Env, cmd.ExtraFiles = install.Source, h.env, h.childFiles()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native search controls: %v %s", err, output)
	}
	t.Logf("native search controls: %s", output)
}
