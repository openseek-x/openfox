package stage12i

// 演练 C：备份恢复完整演练（K-06、NF-09）。按 deploy/backup.sh、deploy/verify-backup.sh 与
// deploy/restore-runbook.md：造数据 → 备份（数据库加产物卷）→ 校验 → 恢复到全新的临时集群与空的产物目录 →
// 轮换中心纪元 → 启动中心 → 验证数据、产物、组织归属与租户隔离 → 让仍持有旧纪元的 daemon 连接恢复后的中心。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
)

// restoreRolesSQL 与 deploy/restore-runbook.md「恢复」第 3 步、迁移 000001 的角色定义一致：
// 角色是集群级对象，不在 pg_dump 的数据库归档里，必须在 pg_restore 之前建好。
const restoreRolesSQL = `
CREATE ROLE openfox_migrator NOLOGIN NOSUPERUSER NOCREATEDB NOREPLICATION NOBYPASSRLS;
CREATE ROLE openfox_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
CREATE ROLE openfox_context NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS NOINHERIT;
GRANT openfox_app TO openfox_context;
`

func deployScript(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "deploy", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// runTool 运行脚本或 PostgreSQL 工具，返回合并输出与耗时；失败即终止用例。
func (s *stack) runTool(env []string, name string, args ...string) (string, time.Duration) {
	s.t.Helper()
	begin := time.Now()
	cmd := exec.Command(name, args...)
	cmd.Env = append(append([]string{}, env...), "TMPDIR="+filepath.Join(s.dir, "tmp"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("%s %v: %v\n%s", filepath.Base(name), args, err, output)
	}
	return string(output), time.Since(begin)
}

// get 以指定用户取一个绝对路径，返回状态码与响应体。
func (s *stack) get(userID, path string) (int, []byte) {
	s.t.Helper()
	return s.login(userID).do(http.MethodGet, path, nil)
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func TestBackupRestoreDrill(t *testing.T) {
	source, sourceDSN := newMigratedCluster(t, "openfox_src")
	s := newStack(t, sourceDSN, 8*time.Minute)
	otherOrg := s.seed()
	s.startServer()
	e := &crashEnv{stack: s, device: s.registerDevice("d")}
	e.daemon = s.startDaemon(e.device)
	s.waitOnline(e.device)
	e.space, e.agent = s.createAgent(e.device)

	// ---- 1. 造数据：任务、对话（含 Agent 回复）、一个 Run 产物文件、另一个组织的空间 ----
	var task struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	s.owner().must(http.StatusCreated, http.MethodPost, "spaces/"+e.space+"/tasks", map[string]any{"title": "Stage12i backup drill task"}, &task)
	conversation, run := e.gateRun()
	// Run 执行期间由设备侧接口上传一个产物（与 daemon 发布产物走同一个中心接口与存储）。
	artifactBody := []byte("# stage12i backup drill\n\nartifact body " + newID(t) + "\n")
	token, err := os.ReadFile(filepath.Join(e.device.StateDir, daemon.DeviceTokenFileName))
	if err != nil {
		t.Fatal(err)
	}
	deviceClient := &supervisor.Client{BaseURL: daemon.APIBaseURL(s.center), Token: daemon.StaticTokenProvider(strings.TrimSpace(string(token)))}
	uploaded, err := deviceClient.UploadRunArtifact(s.ctx, s.org, run.ID, run.Generation, newID(t), "report.md", "text/markdown", artifactBody)
	if err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
	e.touch("release1")
	e.touch("release2")
	e.waitFor("artifact run succeeded", 60*time.Second, func() bool { return e.run(run.ID).State == "SUCCEEDED" })
	artifactPath := "/api/v1/organizations/" + s.org + "/runs/" + run.ID + "/artifacts/" + uploaded.ID
	if status, body := s.get(drillOwnerID, artifactPath); status != http.StatusOK || !bytes.Equal(body, artifactBody) {
		t.Fatalf("artifact before backup = %d %q", status, body)
	}
	var otherSpace struct {
		ID string `json:"id"`
	}
	s.login(drillOutsiderID).must(http.StatusCreated, http.MethodPost, "/api/v1/organizations/"+otherOrg+"/spaces",
		map[string]any{"name": "Other org space", "visibility": "SHARED"}, &otherSpace)
	// 再留一个执行中的 Run（Runtime 卡住）：它代表「备份时刻仍在途的工作」，daemon 与 Runtime 在整个恢复过程中保持运行。
	_ = os.Remove(e.statePath("hang-pid"))
	inflightConversation := e.createConversation(e.space, e.agent)
	e.nextMode("hang")
	e.mustPost(inflightConversation, e.agent, "in flight at backup time")
	var inflight runRow
	var runtimePID int
	e.waitFor("in-flight run running", 60*time.Second, func() bool {
		inflight, runtimePID = e.latestRun(inflightConversation), e.statePID("hang-pid")
		return inflight.State == "RUNNING" && runtimePID > 0
	})
	oldEpoch := s.text(`SELECT center_epoch::text FROM openfox.center_state WHERE id = 1`)
	counts := `SELECT (SELECT count(*) FROM openfox.organizations)::text || '/' || (SELECT count(*) FROM openfox.organization_members)::text || '/' ||
		(SELECT count(*) FROM openfox.tasks)::text || '/' || (SELECT count(*) FROM openfox.messages)::text || '/' ||
		(SELECT count(*) FROM openfox.runs)::text || '/' || (SELECT count(*) FROM openfox.run_events)::text || '/' ||
		(SELECT count(*) FROM openfox.audit_events)::text || '/' || (SELECT count(*) FROM openfox_meta.schema_migrations)::text`
	storageFiles := s.text(`SELECT count(*)::text FROM openfox.artifacts WHERE run_id = $1::uuid`, run.ID)
	if storageFiles != "1" {
		t.Fatalf("run artifacts = %s", storageFiles)
	}

	// ---- 2. 备份：按手册先停止写入（停止中心），再备份数据库与产物卷，并校验 ----
	e.stopCenter("backup")
	centerStoppedAt := time.Now()
	before := s.text(counts)
	backupDir := filepath.Join(s.dir, "backup")
	output, backupTook := s.runTool(os.Environ(), deployScript(t, "backup.sh"), s.urlFile, s.storage, backupDir)
	t.Logf("backup.sh 用时 %s：%s", backupTook.Round(10*time.Millisecond), strings.TrimSpace(output))
	output, verifyTook := s.runTool(os.Environ(), deployScript(t, "verify-backup.sh"), backupDir)
	t.Logf("verify-backup.sh 用时 %s：%s", verifyTook.Round(10*time.Millisecond), strings.TrimSpace(output))
	metadata, _ := os.ReadFile(filepath.Join(backupDir, "metadata.txt"))
	t.Logf("备份内容：%s；数据行数（组织/成员/任务/消息/Run/事件/审计/迁移台账）=%s", strings.Join(strings.Fields(string(metadata)), " "), before)
	// 校验脚本能发现损坏：改动产物归档的一个副本后校验必须失败。
	corrupt := filepath.Join(s.dir, "backup-corrupt")
	s.runTool(os.Environ(), "cp", "-R", backupDir, corrupt)
	archive, err := os.OpenFile(filepath.Join(corrupt, "artifacts.tar"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = archive.Write([]byte("tampered"))
	_ = archive.Close()
	if out, err := exec.Command(deployScript(t, "verify-backup.sh"), corrupt).CombinedOutput(); err == nil {
		t.Fatalf("verify-backup.sh accepted a tampered backup: %s", out)
	}

	// ---- 3. 恢复到全新的集群与空的产物目录（原集群、原产物目录保持不动）----
	restoreBegin := time.Now()
	targetDir, err := os.MkdirTemp("/tmp", "of12i-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(targetDir) })
	target := newCluster(t, targetDir)
	target.run("createdb", "openfox_restored")
	target.run("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-d", "openfox_restored", "-c", restoreRolesSQL)
	_, restoreTook := s.runTool(target.env(), "pg_restore", "--exit-on-error", "--dbname", "openfox_restored", filepath.Join(backupDir, "database.dump"))
	restoredStorage := filepath.Join(s.dir, "storage-restored")
	if err := os.MkdirAll(restoredStorage, 0o700); err != nil {
		t.Fatal(err)
	}
	s.runTool(os.Environ(), "tar", "-xf", filepath.Join(backupDir, "artifacts.tar"), "-C", restoredStorage)
	// 手册第 5 步：从恢复后的产物目录重新计算校验和，与 storage.sha256 逐项比对。
	manifest, err := os.ReadFile(filepath.Join(backupDir, "storage.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		want, relative, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("manifest line %q", line)
		}
		if got := fileSHA256(t, filepath.Join(restoredStorage, relative)); got != want {
			t.Fatalf("restored file %s checksum mismatch", relative)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("the backup contains no storage files")
	}
	// 手册第 6 步：迁移台账与应用兼容性；只前进迁移（这里应为 0 个待应用）。
	target.migrate("openfox_restored")
	// 切换到恢复目标：新的数据库地址文件与产物目录；原集群继续运行但不再被使用。
	_ = s.superuser.Close(context.Background())
	s.dsn, s.storage = target.dsn("openfox_restored"), restoredStorage
	if err := os.WriteFile(s.urlFile, []byte(s.dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.connect()
	if after := s.text(counts); after != before {
		t.Fatalf("row counts after restore = %s, before = %s", after, before)
	}
	s.expect("true", `SELECT (has_table_privilege('openfox_app', 'openfox.tasks', 'SELECT') AND NOT has_table_privilege('openfox_app', 'openfox_meta.schema_migrations', 'SELECT'))::text`)
	s.expect("openfox_migrator", `SELECT nspowner::regrole::text FROM pg_namespace WHERE nspname = 'openfox_private'`)
	s.expect("0", `SELECT count(*)::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'openfox' AND c.relkind = 'r' AND c.relname IN ('tasks', 'messages', 'runs', 'organizations') AND NOT (c.relrowsecurity AND c.relforcerowsecurity)`)
	// 手册第 7 步：接受设备之前轮换中心纪元。
	newEpoch := s.text(`UPDATE openfox.center_state SET center_epoch = center_epoch + 1, updated_at = clock_timestamp() WHERE id = 1 RETURNING center_epoch::text`)
	t.Logf("恢复：pg_restore 用时 %s，产物 %d 个文件校验和一致，迁移台账 %s 条；中心纪元 %s → %s；恢复总用时 %s", restoreTook.Round(10*time.Millisecond), checked,
		s.text(`SELECT count(*)::text FROM openfox_meta.schema_migrations`), oldEpoch, newEpoch, time.Since(restoreBegin).Round(100*time.Millisecond))

	// ---- 4. 启动恢复后的中心（同一监听地址），验证数据与产物 ----
	// 会话 cookie 是否仍然有效不作假设：全部重新登录。
	s.clients = map[string]*client{}
	s.startServer()
	t.Logf("中心停机（备份 + 恢复）共 %s", time.Since(centerStoppedAt).Round(100*time.Millisecond))
	var restoredTask struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	s.owner().must(http.StatusOK, http.MethodGet, "spaces/"+e.space+"/tasks/"+task.ID, nil, &restoredTask)
	if restoredTask.Title != "Stage12i backup drill task" {
		t.Fatalf("restored task = %+v", restoredTask)
	}
	if status, body := s.get(drillOwnerID, "/api/v1/organizations/"+s.org+"/conversations/"+conversation+"/messages"); status != http.StatusOK ||
		!strings.Contains(string(body), `"OK"`) || !strings.Contains(string(body), "gate") {
		t.Fatalf("restored messages = %d %s", status, body)
	}
	if status, body := s.get(drillOwnerID, artifactPath); status != http.StatusOK || !bytes.Equal(body, artifactBody) {
		t.Fatalf("restored artifact = %d %q", status, body)
	}
	// 组织归属完整：成员与角色不变，数据仍属于原组织。
	s.expect("OWNER,MEMBER", `SELECT string_agg(role, ',' ORDER BY role DESC) FROM openfox.organization_members WHERE organization_id = $1::uuid`, s.org)
	s.expect(s.org, `SELECT organization_id::text FROM openfox.tasks WHERE id = $1::uuid`, task.ID)
	s.expect(s.org, `SELECT organization_id::text FROM openfox.artifacts WHERE id = $1::uuid`, uploaded.ID)
	// 租户隔离仍生效：另一个组织的用户读不到本组织的任务、对话与产物；本组织的 Owner 也读不到对方的空间。
	for _, path := range []string{
		"/api/v1/organizations/" + s.org + "/spaces/" + e.space + "/tasks/" + task.ID,
		"/api/v1/organizations/" + s.org + "/conversations/" + conversation + "/messages",
		artifactPath,
		// 用自己的组织前缀访问别的组织的资源 ID：行级安全下应当查不到。
		"/api/v1/organizations/" + otherOrg + "/spaces/" + e.space + "/tasks/" + task.ID,
		"/api/v1/organizations/" + otherOrg + "/runs/" + run.ID + "/artifacts/" + uploaded.ID,
	} {
		if status, body := s.get(drillOutsiderID, path); status != http.StatusForbidden && status != http.StatusNotFound {
			t.Fatalf("outsider GET %s = %d %s", path, status, body)
		}
	}
	if status, body := s.get(drillOwnerID, "/api/v1/organizations/"+otherOrg+"/spaces/"+otherSpace.ID); status != http.StatusForbidden && status != http.StatusNotFound {
		t.Fatalf("owner GET other org space = %d %s", status, body)
	}
	if status, body := s.get(drillOutsiderID, "/api/v1/organizations/"+otherOrg+"/spaces/"+otherSpace.ID); status != http.StatusOK {
		t.Fatalf("outsider GET own space = %d %s", status, body)
	}
	// 应用角色下的行级安全：不带租户上下文读不到任何任务。
	tx, err := s.superuser.Begin(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var visible int
	if _, err := tx.Exec(s.ctx, `SET LOCAL ROLE openfox_app`); err != nil {
		t.Fatal(err)
	}
	// 没有租户上下文时，策略要么让查询看不到任何行，要么直接报错；两者都说明行级安全在恢复后仍然强制生效。
	if err := tx.QueryRow(s.ctx, `SELECT count(*) FROM openfox.tasks`).Scan(&visible); err == nil && visible != 0 {
		t.Fatalf("openfox_app without tenant context sees %d tasks", visible)
	}
	_ = tx.Rollback(s.ctx)
	t.Log("恢复后：任务、对话消息、产物字节一致；组织成员与归属不变；跨组织读取被拒绝（403/404）；应用角色无租户上下文读不到任务")

	// ---- 5. 仍持有旧纪元的 daemon 连接恢复后的中心 ----
	// daemon 与卡住的 Runtime 在备份、恢复期间一直在运行，本机配置里的中心纪元仍是旧值。
	if e.daemon.exited() {
		t.Fatalf("daemon exited during the restore\n%s", tail(e.daemon.log))
	}
	socket := filepath.Join(e.device.StateDir, "daemon.sock")
	var status daemon.Status
	reconcile := s.waitFor("old-epoch daemon is told to reconcile", 60*time.Second, func() bool {
		queryContext, cancel := context.WithTimeout(s.ctx, 3*time.Second)
		defer cancel()
		var err error
		status, err = daemon.QuerySocket(queryContext, socket)
		return err == nil && status.RemoteState == daemon.RemoteReconcile
	})
	encoded, _ := json.Marshal(status)
	t.Logf("中心恢复后 %s 旧纪元 daemon 进入 %s：%s", reconcile.Round(10*time.Millisecond), status.RemoteState, encoded)
	// 给 daemon 两个租约周期：观察它是否还能带着旧状态继续执行（续租、领取新任务）。
	e.mustPost(conversation, e.agent, "after restore")
	calls := e.invocations()
	time.Sleep(35 * time.Second)
	restoredInflight := e.run(inflight.ID)
	online := s.text(`SELECT (last_seen_at > clock_timestamp() - interval '30 seconds')::text FROM openfox.physical_devices WHERE id = $1::uuid`, e.device.ID)
	queued := e.latestRun(conversation)
	t.Logf("恢复后 35 秒：在途 Run=%s 待核对=%v case=%s evidence=%s；Runtime 存活=%v；电脑 30 秒内有心跳=%s；新消息的 Run=%s（%s）；Runtime 调用 %d→%d",
		restoredInflight.State, restoredInflight.Reconcile, e.caseOf(inflight.ID), e.evidenceOf(inflight.ID), e.ownedRuntimeAlive(runtimePID), online,
		queued.State, queued.ID, calls, e.invocations())
	if e.invocations() != calls {
		t.Fatalf("the old-epoch daemon started new work after the restore (invocations %d → %d)", calls, e.invocations())
	}
	if queued.ID != run.ID && queued.State != "QUEUED" {
		t.Fatalf("a run was dispatched to the old-epoch daemon: %+v", queued)
	}
	if online != "false" {
		t.Fatalf("the old-epoch device still heartbeats successfully (online=%s)", online)
	}
	if restoredInflight.State != "RUNNING" {
		t.Fatalf("pre-restore in-flight run changed state without an explicit decision: %+v", restoredInflight)
	}
	// 已知缺口（见 docs/development-records/2026-11-12-stage12i-drills.md「发现但未修的缺陷」）：纪元只在心跳上校验，
	// 设备侧的续租接口不校验，所以旧纪元 daemon 虽然被要求核对、领不到新任务、显示离线，却仍能为恢复前的在途 Run 续租。
	// 这里如实记录观察到的状态而不把它当作通过条件；手册第 9 步要求管理员在放行设备前显式处理这些 Run，下面验证该步骤可行。
	leaseAlive := s.text(`SELECT (lease_expires_at > clock_timestamp())::text FROM openfox.runs WHERE id = $1::uuid`, inflight.ID)
	t.Logf("已知缺口：恢复前的在途 Run 在旧纪元 daemon 上继续被续租（租约有效=%s、待核对=%v）", leaseAlive, restoredInflight.Reconcile)
	// 手册第 9 步：管理员显式取消恢复前的在途 Run；daemon 在下一次续租时得知取消并停掉 Runtime。
	begin := time.Now()
	if status, body := s.owner().doWith(http.MethodPost, "runs/"+inflight.ID+"/cancel", map[string]any{}, "Idempotency-Key", "stage12i-restore-"+inflight.ID); status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("cancel in-flight run = %d %s", status, body)
	}
	var settled runRow
	s.waitFor("pre-restore in-flight run is closed after the explicit cancel", 90*time.Second, func() bool {
		settled = e.run(inflight.ID)
		return settled.State == "CANCELED" || settled.State == "FAILED"
	})
	s.waitFor("runtime stopped", 30*time.Second, func() bool { return !e.ownedRuntimeAlive(runtimePID) })
	// 只看这次 Run 自己的预留：恢复后那条新消息的 Run 可能在电脑被判定离线之前拿到过 offer 与预留，不计入这里。
	s.expect("1", `SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status = 'RELEASED'`, inflight.ID)
	s.expect("0", `SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status <> 'RELEASED'`, inflight.ID)
	if after := e.latestRun(conversation); e.invocations() != calls || after.State == "RUNNING" || after.State == "SUCCEEDED" {
		t.Fatalf("the old-epoch daemon executed new work: run=%+v invocations %d → %d", after, calls, e.invocations())
	}
	t.Logf("管理员取消后 %s 在途 Run=%s，Runtime 已停止，预留释放 1 次", time.Since(begin).Round(100*time.Millisecond), settled.State)

	// 原集群与原产物目录未被改动（恢复只写新目标）。
	sourceCounts := strings.TrimSpace(source.run("psql", "-X", "-At", "-d", "openfox_src", "-c", "SELECT center_epoch FROM openfox.center_state WHERE id = 1"))
	if sourceCounts != oldEpoch {
		t.Fatalf("source center epoch changed: %s", sourceCounts)
	}
	if elapsed, ok := e.daemon.signalAndWait(syscall.SIGTERM, 40*time.Second); !ok {
		t.Fatalf("daemon did not stop after SIGTERM (%s)", elapsed)
	}
	e.reapRuntimes()
}
