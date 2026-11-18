package stage12i

// 演练 B：优雅停止联合验证（K-05）。有运行中的 Run 时分别停止中心、daemon 与数据库，
// 记录实际观察到的退出时间与 Run 的最终状态。

import (
	"context"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/reconciliation"
)

// gateRun 发起一次 gate 模式的 Run 并等它输出第一段、进入执行中。
func (e *crashEnv) gateRun() (conversation string, run runRow) {
	e.t.Helper()
	for _, name := range []string{"gate-started", "release1", "release2"} {
		_ = os.Remove(e.statePath(name))
	}
	conversation = e.createConversation(e.space, e.agent)
	e.nextMode("gate")
	e.mustPost(conversation, e.agent, "gate")
	e.waitFor("gate run running", 60*time.Second, func() bool {
		run = e.latestRun(conversation)
		_, err := os.Stat(e.statePath("gate-started"))
		return run.State == "RUNNING" && err == nil
	})
	e.waitFor("first output event stored", 30*time.Second, func() bool { return e.outputStored(run.ID, "gate step one") })
	return conversation, run
}

func (e *crashEnv) outputStored(runID, text string) bool {
	return e.text(`SELECT count(*)::text FROM openfox.run_events WHERE run_id = $1::uuid AND safe_payload::text LIKE '%' || $2 || '%'`, runID, text) != "0"
}

func (e *crashEnv) expectReply(conversation, want string) {
	e.t.Helper()
	e.expect(want, `SELECT body FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'AGENT'
		ORDER BY seq DESC LIMIT 1`, e.org, conversation)
}

// stopCenter 对中心发 SIGTERM 并返回退出耗时与退出错误（nil 表示退出码 0）。
func (e *crashEnv) stopCenter(label string) time.Duration {
	e.t.Helper()
	pid := e.server.pid()
	elapsed, ok := e.server.signalAndWait(syscall.SIGTERM, 40*time.Second)
	if !ok {
		e.t.Fatalf("[%s] center pid=%d did not exit within %s after SIGTERM", label, pid, elapsed)
	}
	e.t.Logf("[%s] 中心 pid=%d SIGTERM 后 %s 退出，退出错误=%v", label, pid, elapsed.Round(10*time.Millisecond), e.server.err)
	return elapsed
}

func TestCenterGracefulStopWithRunningRun(t *testing.T) {
	e := newCrashEnv(t)

	t.Run("在途请求排空，短暂停机后 daemon 重连续传事件，Run 正常完成", func(t *testing.T) {
		conversation, run := e.gateRun()
		// 在途请求：超级用户事务锁住一个空间行，PATCH 该空间的请求会阻塞在行锁上；SIGTERM 之后再放锁。
		var idle struct {
			ID string `json:"id"`
		}
		e.owner().must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "Idle " + newID(t)[:6], "visibility": "SHARED"}, &idle)
		version := e.text(`SELECT version::text FROM openfox.spaces WHERE id = $1::uuid`, idle.ID)
		locker, err := pgx.Connect(e.ctx, e.dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer locker.Close(context.Background())
		tx, err := locker.Begin(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(e.ctx, `SELECT 1 FROM openfox.spaces WHERE id = $1::uuid FOR UPDATE`, idle.ID); err != nil {
			t.Fatal(err)
		}
		type outcome struct {
			status  int
			body    string
			elapsed time.Duration
		}
		inflight := make(chan outcome, 1)
		owner := e.owner()
		go func() {
			begin := time.Now()
			status, body := owner.do(http.MethodPatch, "spaces/"+idle.ID, map[string]any{"version": mustUint(version), "name": "Renamed during shutdown"})
			inflight <- outcome{status, string(body), time.Since(begin)}
		}()
		e.waitFor("in-flight request blocked on the row lock", 10*time.Second, func() bool {
			return e.text(`SELECT count(*)::text FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query ILIKE '%spaces%'`) != "0"
		})
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_ = tx.Commit(context.Background())
		}()
		stoppedAt := time.Now()
		elapsed := e.stopCenter("short")
		got := <-inflight
		if got.status != http.StatusOK {
			t.Fatalf("in-flight request = %d %s", got.status, got.body)
		}
		e.expect("Renamed during shutdown", `SELECT name FROM openfox.spaces WHERE id = $1::uuid`, idle.ID)
		if elapsed < time.Second || e.server.err != nil {
			t.Fatalf("center exit = %s, %v (expected to wait for the in-flight request and exit 0)", elapsed, e.server.err)
		}
		t.Logf("[short] 在途 PATCH 用时 %s 返回 %d（SIGTERM 后才放锁），中心等它完成后退出", got.elapsed.Round(10*time.Millisecond), got.status)
		if e.status("/healthz") != 0 {
			t.Fatal("center still answers after exit")
		}
		// 停机期间 Runtime 继续执行并产生新输出；daemon 与 Runtime 都还活着。
		e.touch("release1")
		time.Sleep(2 * time.Second)
		if e.daemon.exited() || len(e.orphanRuntimes()) != 1 {
			t.Fatalf("daemon exited=%v runtimes=%v during the outage", e.daemon.exited(), e.orphanRuntimes())
		}
		e.startServer()
		outage := time.Since(stoppedAt)
		resumed := e.waitFor("event produced during the outage is uploaded after reconnect", 60*time.Second, func() bool { return e.outputStored(run.ID, "gate step two") })
		t.Logf("[short] 停机 %s 后中心恢复；恢复后 %s 停机期间产生的事件已续传入库", outage.Round(100*time.Millisecond), resumed.Round(10*time.Millisecond))
		e.touch("release2")
		var final runRow
		finished := e.waitFor("run succeeded", 60*time.Second, func() bool {
			final = e.run(run.ID)
			return final.State != "RUNNING"
		})
		if final.State != "SUCCEEDED" || final.Reconcile {
			t.Fatalf("run = %+v\n%s", final, e.diagnostics())
		}
		e.expectReply(conversation, "OK")
		e.expectReleasedOnce(run.ID, e.device.ID)
		e.expect("0", `SELECT count(*)::text FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, run.ID)
		// 设备事件从 1 连续上报；中心生成的终态事件使用独立的高位区间。
		e.expect("true", `SELECT (count(*) = count(DISTINCT sequence) AND min(sequence) = 1 AND max(sequence) = count(*))::text FROM openfox.run_events WHERE run_id = $1::uuid AND sequence <= $2`, run.ID, reconciliation.MaxDeviceEventSequence)
		e.expect("1", `SELECT count(*)::text FROM openfox.run_events WHERE run_id = $1::uuid AND sequence > $2 AND type = 'run.result'`, run.ID, reconciliation.MaxDeviceEventSequence)
		t.Logf("[short] 放行后 %s Run=%s，无核对案，预留释放 1 次，设备事件 %s 条序号连续，中心终态事件 1 条", finished.Round(10*time.Millisecond), final.State,
			e.text(`SELECT count(*)::text FROM openfox.run_events WHERE run_id = $1::uuid AND sequence <= $2`, run.ID, reconciliation.MaxDeviceEventSequence))
	})

	t.Run("停机超过租约：daemon 在本地租约到期后停掉 Runtime，中心恢复后 Run 自动失败并释放对话", func(t *testing.T) {
		conversation, run := e.gateRun()
		stoppedAt := time.Now()
		e.stopCenter("long")
		// 续不上租：daemon 在本地租约期限（15 秒）过后主动停掉 Runtime，避免在失去中心授权的情况下继续执行。
		stopped := e.waitFor("daemon stops the runtime after the local lease deadline", 60*time.Second, func() bool { return len(e.orphanRuntimes()) == 0 })
		t.Logf("[long] 中心停止后 %s daemon 停掉 Runtime（本地租约 %s）；journal=%+v", stopped.Round(100*time.Millisecond), leaseTTL, e.journalRow(e.device, run.ID))
		if e.daemon.exited() {
			t.Fatal("daemon exited during the center outage")
		}
		time.Sleep(time.Until(stoppedAt.Add(22 * time.Second)))
		e.startServer()
		outage := time.Since(stoppedAt)
		// 中心恢复：租约已过期 → 待核对；daemon 补报「进程已退出」的确定证据 → 自动失败、释放对话。
		var final runRow
		recovered := e.waitFor("run auto-failed after the center came back", 90*time.Second, func() bool {
			final = e.run(run.ID)
			return final.State == "FAILED"
		})
		t.Logf("[long] 停机 %s；中心恢复后 %s Run=%s failure_code=%s case=%s evidence=%s", outage.Round(100*time.Millisecond),
			recovered.Round(10*time.Millisecond), final.State, final.FailureCode, e.caseOf(run.ID), e.evidenceOf(run.ID))
		if final.FailureCode != "UNKNOWN_PROCESS" || final.Reconcile {
			t.Fatalf("run = %+v", final)
		}
		if got := e.evidenceOf(run.ID); got != "PROCESS_EXITED" {
			t.Fatalf("evidence = %s", got)
		}
		e.expect("1", `SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, run.ID)
		e.expectReleasedOnce(run.ID, e.device.ID)
		// 停机前已上传的事件仍在；没有自动重试；对话恢复可输入。
		if !e.outputStored(run.ID, "gate step one") {
			t.Fatal("events uploaded before the outage were lost")
		}
		e.expectNoRetryThenUsable("long", conversation)
	})

	if elapsed, ok := e.daemon.signalAndWait(syscall.SIGTERM, 30*time.Second); !ok {
		t.Fatalf("daemon did not stop after SIGTERM (%s)", elapsed)
	}
	if orphans := e.orphanRuntimes(); len(orphans) != 0 {
		t.Fatalf("orphan runtime processes: %v", orphans)
	}
}

func mustUint(text string) uint64 {
	var value uint64
	for _, digit := range strings.TrimSpace(text) {
		value = value*10 + uint64(digit-'0')
	}
	return value
}

func TestDaemonGracefulStopWithRunningRun(t *testing.T) {
	e := newCrashEnv(t)
	_ = os.Remove(e.statePath("slow-pid"))
	conversation := e.createConversation(e.space, e.agent)
	e.nextMode("slow")
	e.mustPost(conversation, e.agent, "slow")
	var run runRow
	var runtimePID int
	e.waitFor("slow run running", 60*time.Second, func() bool {
		run, runtimePID = e.latestRun(conversation), e.statePID("slow-pid")
		return run.State == "RUNNING" && runtimePID > 0
	})
	daemonPID := e.daemon.pid()
	elapsed, ok := e.daemon.signalAndWait(syscall.SIGTERM, 60*time.Second)
	if !ok {
		t.Fatalf("daemon pid=%d did not exit within %s after SIGTERM\n%s", daemonPID, elapsed, tail(e.daemon.log))
	}
	t.Logf("daemon pid=%d SIGTERM 后 %s 退出，退出错误=%v", daemonPID, elapsed.Round(10*time.Millisecond), e.daemon.err)
	if e.daemon.err != nil {
		t.Fatalf("daemon exit error: %v\n%s", e.daemon.err, tail(e.daemon.log))
	}
	// daemon 退出时 Run 已经收尾：按失败上报（DAEMON_SHUTDOWN），不是用户取消；无孤儿进程。
	final := e.run(run.ID)
	if orphans := e.orphanRuntimes(); len(orphans) != 0 || e.ownedRuntimeAlive(runtimePID) {
		t.Fatalf("orphan runtime processes after daemon stop: %v", orphans)
	}
	summary := e.text(`SELECT COALESCE(r.failure_code, '') || '|' || COALESCE((SELECT string_agg(x.summary, ' ') FROM openfox.run_results x WHERE x.run_id = r.id), '')
		FROM openfox.runs r WHERE r.id = $1::uuid`, run.ID)
	t.Logf("daemon 退出时 Run=%s failure/summary=%s 待核对=%v journal=%+v", final.State, summary, final.Reconcile, e.journalRow(e.device, run.ID))
	if final.State != "FAILED" || final.Reconcile {
		t.Fatalf("run = %+v\n%s", final, e.diagnostics())
	}
	if !strings.Contains(summary, "DAEMON_SHUTDOWN") {
		t.Fatalf("run summary = %q", summary)
	}
	e.expectReleasedOnce(run.ID, e.device.ID)
	e.expect("0", `SELECT count(*)::text FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, run.ID)
	if row := e.journalRow(e.device, run.ID); row.State != "FINISHED" {
		t.Fatalf("journal = %+v", row)
	}
	// 重新启动：不产生核对上报；同一对话（同一工作目录绑定）可以继续使用，说明目录已释放。
	e.restartDaemon()
	e.expectNoRetryThenUsable("daemon-stop", conversation)
	e.expect("0", `SELECT count(*)::text FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, run.ID)
	if elapsed, ok := e.daemon.signalAndWait(syscall.SIGTERM, 30*time.Second); !ok {
		t.Fatalf("daemon did not stop after SIGTERM (%s)", elapsed)
	}
}

func TestDatabaseOutageReadinessAndRecovery(t *testing.T) {
	c, dsn := newMigratedCluster(t, "openfox_drill")
	s := newStack(t, dsn, 5*time.Minute)
	s.seed()
	s.startServer()
	e := &crashEnv{stack: s, device: s.registerDevice("d")}
	e.daemon = s.startDaemon(e.device)
	s.waitOnline(e.device)
	e.space, e.agent = s.createAgent(e.device)
	// 基线：派发一次成功。
	conversation := e.createConversation(e.space, e.agent)
	e.mustPost(conversation, e.agent, "before outage")
	if run := e.waitNewRunDone(conversation, ""); run.State != "SUCCEEDED" {
		t.Fatalf("baseline run = %+v", run)
	}

	// 停库（pg_ctl stop -m fast，只停本用例自建的临时集群）。
	_ = s.superuser.Close(context.Background())
	stoppedAt := time.Now()
	c.stop()
	notReady := s.waitFor("/ready reports not ready", 30*time.Second, func() bool { return s.status("/ready") == http.StatusServiceUnavailable })
	if live := s.status("/healthz"); live != http.StatusOK {
		t.Fatalf("/healthz during the outage = %d", live)
	}
	t.Logf("停库后 %s /ready=503，/healthz=200", notReady.Round(10*time.Millisecond))
	// 停库期间保持 12 秒：中心与 daemon 进程都不退出，/healthz 始终存活。
	for time.Since(stoppedAt) < 12*time.Second {
		if s.server.exited() || e.daemon.exited() {
			t.Fatalf("process exited during the database outage: center=%v daemon=%v\n%s", s.server.exited(), e.daemon.exited(), tail(s.server.log))
		}
		if ready, live := s.status("/ready"), s.status("/healthz"); ready != http.StatusServiceUnavailable || live != http.StatusOK {
			t.Fatalf("during outage /ready=%d /healthz=%d", ready, live)
		}
		time.Sleep(time.Second)
	}
	// 恢复数据库：/ready 自动恢复，不需要重启中心。
	c.start()
	startedAt := time.Now()
	s.connect()
	ready := s.waitFor("/ready recovers", 60*time.Second, func() bool { return s.status("/ready") == http.StatusOK })
	t.Logf("停库共 %s；数据库恢复后 %s /ready=200（中心未重启，pid=%d）", startedAt.Sub(stoppedAt).Round(100*time.Millisecond), ready.Round(10*time.Millisecond), s.server.pid())
	// 后续派发正常：电脑重新在线，新消息被派发并成功。
	online := s.waitOnline(e.device)
	begin := time.Now()
	previous := e.latestRun(conversation).ID
	e.mustPost(conversation, e.agent, "after outage")
	run := e.waitNewRunDone(conversation, previous)
	if run.State != "SUCCEEDED" {
		t.Fatalf("run after recovery = %+v\n%s", run, s.diagnostics())
	}
	e.expectReleasedOnce(run.ID, e.device.ID)
	t.Logf("恢复后 %s 电脑在线；新消息 %s 内派发并成功（Run=%s）", online.Round(10*time.Millisecond), time.Since(begin).Round(10*time.Millisecond), run.State)
	if s.server.exited() || e.daemon.exited() {
		t.Fatal("center or daemon exited")
	}
	if elapsed, ok := e.daemon.signalAndWait(syscall.SIGTERM, 30*time.Second); !ok {
		t.Fatalf("daemon did not stop after SIGTERM (%s)", elapsed)
	}
}
