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
)

func TestNativeHermesReadBoundary(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	a := New()
	install, err := a.installation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	work, _ := filepath.EvalSymlinks(t.TempDir())
	if err = os.WriteFile(filepath.Join(work, "read.txt"), []byte("WORKSPACE_SENTINEL"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := a.prepare(install, work, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(h.root)
	defer h.close()
	outside := filepath.Join(h.root, "runtime-private.txt")
	if err = os.WriteFile(outside, []byte("RUNTIME_PRIVATE_CANARY"), 0600); err != nil {
		t.Fatal(err)
	}
	options, _ := json.Marshal(map[string]any{"cwd": work, "rg": install.RG, "workspace_fd": 3})
	prefix, _, found := strings.Cut(bootstrap, "sys.argv = [\"hermes\", *command]")
	if !found {
		t.Fatal("trusted entry point missing")
	}
	code := prefix + "\noutside = " + fmt.Sprintf("%q", outside) + `
import concurrent.futures,inspect,types
observed=[]
guard=file_operations.open
def trace_open(*args,**kwargs):
    if getattr(local,'read_guard',False):
        observed.append([frame.function for frame in inspect.stack()[:8]])
    return guard(*args,**kwargs)
file_operations.open=trace_open
def one(index):
    result=json.loads(registry.dispatch('read_file',{'path':str(workspace/'read.txt')},task_id='scope-'+str(index)))
    assert 'WORKSPACE_SENTINEL' in result.get('content',''),result
    denied=json.loads(registry.dispatch('read_file',{'path':outside},task_id='outside-'+str(index)))
    assert denied.get('host_rejected') and 'RUNTIME_PRIVATE_CANARY' not in str(denied),denied
    # Runtime/library I/O in the same thread after the tool call retains its own
    # permissions; tool reads do not grant that data to the model.
    with file_operations.open(outside) as stream:
        assert stream.read()=='RUNTIME_PRIVATE_CANARY'
    return True
with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
    assert all(pool.map(one,range(16)))
assert any('_read_file_native' in stack and 'read_file_tool' in stack and '_handle_read_file' in stack for stack in observed),observed
for name in ['write_file','patch','terminal','execute_code','delegate_task','tool_call','unknown']:
    assert AIAgent._repair_tool_call(object(),name) is None
    result=json.loads(registry.dispatch(name,{},task_id='denied'))
    assert result.get('host_rejected') and result.get('error')=='unsupported_tool',result
    calls=types.SimpleNamespace(tool_calls=[types.SimpleNamespace(function=types.SimpleNamespace(name=name))])
    try:AIAgent._execute_tool_calls(object(),calls,[],None)
    except RuntimeError:pass
    else:raise AssertionError('execution gate allowed '+name)
import gateway.session_policy as policy_module
authority=types.SimpleNamespace(runner=None,sessions={'s':types.SimpleNamespace(source=None)})
ref=types.SimpleNamespace(session_id='s')
original_policy=policy_module.policy_for_source
try:
    for safe,ignore,kanban,worker in [(True,False,None,False),(False,True,None,False),(False,False,'{}',False),(False,False,None,True)]:
        policy=types.SimpleNamespace(safe_mode=safe,ignore_user_config=ignore,kanban_json=kanban,config=lambda w=worker:{'gateway':{'managed_workers':w}})
        policy_module.policy_for_source=lambda *args,p=policy:p
        try:managed_worker.managed_policy(authority,ref)
        except RuntimeError:pass
        else:raise AssertionError('managed worker policy bypass')
finally:policy_module.policy_for_source=original_policy
print(json.dumps({'threaded_reads':16,'outside_rejected':16,'unsupported_tools_rejected':7,'policy_bypasses_rejected':4,'actual_read_stack':observed[0]}))
`
	cmd := exec.Command("/usr/bin/sandbox-exec", "-f", h.sandbox, install.Python, "-c", code, string(options), "--")
	cmd.Dir, cmd.Env, cmd.ExtraFiles = install.Source, h.env, h.childFiles()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native boundary: %v %s", err, output)
	}
	t.Logf("native boundary: %s", output)
}

func TestNativeHermesDetect(t *testing.T) {
	if os.Getenv("OPENFOX_TEST_HERMES_NATIVE") != "1" {
		t.Skip("requires isolated native installation")
	}
	a := New()
	report, err := a.Detect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.ProtocolReady || !report.RequiresRunConfiguration || report.Runnable || string(report.Login) != "UNKNOWN" || report.Version != Version {
		t.Fatalf("unexpected protocol-only capability: %+v", report)
	}
	t.Logf("fixed version=%s protocolReady=%t requiresConfiguration=%t runnable=%t login=%s", report.Version, report.ProtocolReady, report.RequiresRunConfiguration, report.Runnable, report.Login)
}
