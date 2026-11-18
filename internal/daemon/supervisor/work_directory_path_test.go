package supervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"openfox/internal/workdirpath"
)

func pathScope() workdirpath.Scope {
	id := "11111111-1111-4111-8111-111111111111"
	return workdirpath.Scope{OrganizationID: id, DeviceID: id, SpaceID: id, ConversationID: id, AgentID: id, RunID: id, Generation: 1, UserID: id}
}

func TestWorkspacePathFactsStayLocalAndExact(t *testing.T) {
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "state", "runs.db")
	scope := pathScope()
	if _, err := ReadWorkDirectoryPath(ctx, database, scope); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(filepath.Dir(database)); !os.IsNotExist(err) {
		t.Fatal("read created database directory")
	}
	journal, err := OpenJournal(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { journal.Close() })
	if _, err := journal.WorkDirectoryPath(ctx, scope); err == nil {
		t.Fatal("invented missing path")
	}
	directory := filepath.Join(root, "真实工作区")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	run := RunContext{OrganizationID: scope.OrganizationID, SpaceID: scope.SpaceID, ConversationID: scope.ConversationID, AgentID: scope.AgentID, RunID: scope.RunID, Generation: scope.Generation}
	if err := journal.recordWorkDirectory(ctx, scope.DeviceID, run, directory); err != nil {
		t.Fatal(err)
	}
	actual, err := ReadWorkDirectoryPath(ctx, database, scope)
	if err != nil || actual != directory {
		t.Fatal("read failed", err)
	}
	for _, change := range []func(*workdirpath.Scope){
		func(s *workdirpath.Scope) { s.OrganizationID = "22222222-2222-4222-8222-222222222222" },
		func(s *workdirpath.Scope) { s.DeviceID = "22222222-2222-4222-8222-222222222222" },
		func(s *workdirpath.Scope) { s.ConversationID = "22222222-2222-4222-8222-222222222222" },
		func(s *workdirpath.Scope) { s.AgentID = "22222222-2222-4222-8222-222222222222" },
		func(s *workdirpath.Scope) { s.RunID = "22222222-2222-4222-8222-222222222222" },
		func(s *workdirpath.Scope) { s.SpaceID = "22222222-2222-4222-8222-222222222222" },
		func(s *workdirpath.Scope) { s.Generation++ },
	} {
		other := scope
		change(&other)
		if _, err := journal.WorkDirectoryPath(ctx, other); err == nil {
			t.Fatal("scope crossed")
		}
	}
	run.Generation++
	if err := journal.recordWorkDirectory(ctx, scope.DeviceID, run, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.WorkDirectoryPath(ctx, scope); err == nil {
		t.Fatal("old generation still current")
	}
	scope.Generation++
	if err := os.Rename(directory, directory+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.WorkDirectoryPath(ctx, scope); err == nil {
		t.Fatal("replaced symlink accepted")
	}
	os.Remove(directory)
	if _, err := journal.WorkDirectoryPath(ctx, scope); err == nil {
		t.Fatal("deleted directory accepted")
	}
}

func TestWorkspacePathGoNodeInterop(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for cross-language crypto")
	}
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := x509.MarshalPKIXPublicKey(&private.PublicKey)
	der, _ := x509.MarshalPKCS8PrivateKey(private)
	request := workdirpath.Request{Scope: pathScope(), RequestID: pathScope().UserID, PublicKey: base64.StdEncoding.EncodeToString(public), ExpiresAt: time.Now().Add(30 * time.Second).UnixMilli()}
	envelope, err := workdirpath.Seal(request, `C:\Users\测试\project`)
	if err != nil {
		t.Fatal(err)
	}
	module, _ := filepath.Abs("../../../apps/desktop/work-directory.cjs")
	input, _ := json.Marshal(map[string]any{"request": request, "envelope": envelope, "key": base64.StdEncoding.EncodeToString(der), "module": module})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", `let data='';process.stdin.on('data',v=>data+=v);process.stdin.on('end',()=>{const v=JSON.parse(data);const key=require('node:crypto').createPrivateKey({key:Buffer.from(v.key,'base64'),type:'pkcs8',format:'der'});process.stdout.write(require(v.module).decrypt(v.envelope,v.request,key))})`)
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if err != nil || string(output) != `C:\Users\测试\project` {
		t.Fatal("Go/Node interop failed", err)
	}
}
