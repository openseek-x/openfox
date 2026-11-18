package stage12i

// 演练 A：运行中强杀 daemon 后的恢复（阶段 12a 整链端到端）。
// 中心与 daemon 都是独立子进程，Runtime 是不读 stdin、永不结束的假 CLI（hang 模式），
// 因此 daemon 被 SIGKILL 后 Runtime 仍然存活，重启后的核对才有东西可查。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

type crashEnv struct {
	*stack
	device device
	space  string
	agent  string
	daemon *proc
}

func newCrashEnv(t *testing.T) *crashEnv {
	s := newStack(t, "", 5*time.Minute)
	s.seed()
	s.startServer()
	e := &crashEnv{stack: s, device: s.registerDevice("d")}
	e.daemon = s.startDaemon(e.device)
	s.waitOnline(e.device)
	e.space, e.agent = s.createAgent(e.device)
	return e
}

// hangAndKill 让一次 Run 进入执行中（Runtime 卡住），然后对 daemon 进程 SIGKILL。
// 返回对话、Run 与 Runtime 进程号；返回时 daemon 已退出、Runtime 仍存活。
func (e *crashEnv) hangAndKill(label string) (conversation string, run runRow, runtimePID int) {
	e.t.Helper()
	_ = os.Remove(e.statePath("hang-pid"))
	conversation = e.createConversation(e.space, e.agent)
	e.nextMode("hang")
	e.mustPost(conversation, e.agent, "hang "+label)
	e.waitFor("run running with a live runtime", 60*time.Second, func() bool {
		run = e.latestRun(conversation)
		runtimePID = e.statePID("hang-pid")
		return run.State == "RUNNING" && runtimePID > 0
	})
	if !e.ownedRuntimeAlive(runtimePID) {
		e.t.Fatalf("runtime pid %d is not the drill runtime: %q", runtimePID, commandLine(runtimePID))
	}
	e.expect("1", `SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, e.device.ID)
	// 强杀 daemon：只对本夹具启动的子进程发 SIGKILL。
	daemonPID := e.daemon.pid()
	if elapsed, ok := e.daemon.signalAndWait(syscall.SIGKILL, 10*time.Second); !ok {
		e.t.Fatalf("daemon %d did not die after SIGKILL (%s)", daemonPID, elapsed)
	}
	e.t.Logf("[%s] daemon pid=%d 已 SIGKILL；Runtime pid=%d 仍存活=%v；Run=%s generation=%d", label, daemonPID, runtimePID,
		e.ownedRuntimeAlive(runtimePID), run.ID, run.Generation)
	if !e.ownedRuntimeAlive(runtimePID) {
		e.t.Fatal("runtime died together with the daemon; the drill needs a surviving runtime")
	}
	// 本地 journal 记录的进程身份就是这个 Runtime：进程号一致、启动时间非零、独立进程组。
	row := e.journalRow(e.device, run.ID)
	if row.PID != runtimePID || row.StartToken == 0 || row.PGID != runtimePID {
		e.t.Fatalf("journal identity = %+v, runtime pid = %d", row, runtimePID)
	}
	return conversation, run, runtimePID
}

func (e *crashEnv) restartDaemon() time.Time {
	e.t.Helper()
	e.daemon = e.startDaemon(e.device)
	return time.Now()
}

// expectNoRetryThenUsable 断言没有自动重试（Run 数量与 Runtime 调用次数不变），随后在同一对话里
// 再发一条消息能被物化、派发并成功，证明对话已恢复可输入、电脑恢复可派发。
func (e *crashEnv) expectNoRetryThenUsable(label, conversation string) {
	e.t.Helper()
	runs, calls := e.runCount(conversation), e.invocations()
	time.Sleep(3 * time.Second) // 15 个调度周期（200ms）
	if got := e.runCount(conversation); got != runs || e.invocations() != calls {
		e.t.Fatalf("[%s] automatic retry detected: runs %d→%d, invocations %d→%d", label, runs, got, calls, e.invocations())
	}
	e.waitOnline(e.device)
	begin := time.Now()
	previous := e.latestRun(conversation).ID
	e.mustPost(conversation, e.agent, "follow up "+label)
	next := e.waitNewRunDone(conversation, previous)
	if next.State != "SUCCEEDED" {
		e.t.Fatalf("[%s] follow-up run = %+v\n%s", label, next, tail(e.daemon.log))
	}
	e.expect("OK", `SELECT body FROM openfox.messages WHERE organization_id = $1::uuid AND conversation_id = $2::uuid AND author_kind = 'AGENT'
		ORDER BY seq DESC LIMIT 1`, e.org, conversation)
	e.t.Logf("[%s] 无自动重试（Run 数 %d、Runtime 调用 %d 次保持不变）；同一对话后续消息 %s 内成功", label, runs, calls, time.Since(begin).Round(100*time.Millisecond))
}

func (e *crashEnv) caseOf(runID string) string {
	return e.text(`SELECT status || ':' || COALESCE(resolution, '') || ':' || COALESCE(reason_code, '') FROM openfox.run_reconciliation_cases WHERE run_id = $1::uuid`, runID)
}

func (e *crashEnv) evidenceOf(runID string) string {
	return e.text(`SELECT string_agg(kind, ',' ORDER BY observed_at) FROM openfox.run_reconciliation_evidence WHERE run_id = $1::uuid`, runID)
}

func TestDaemonCrashRecoveryEndToEnd(t *testing.T) {
	e := newCrashEnv(t)

	t.Run("旧进程存活：重启后按进程号与启动时间匹配终止，Run 自动失败", func(t *testing.T) {
		conversation, run, runtimePID := e.hangAndKill("match")
		killedAt := time.Now()
		restartedAt := e.restartDaemon()
		gone := e.waitFor("old runtime terminated by the restarted daemon", 45*time.Second, func() bool { return !e.ownedRuntimeAlive(runtimePID) })
		var final runRow
		e.waitFor("run auto-failed", 45*time.Second, func() bool {
			final = e.run(run.ID)
			return final.State == "FAILED"
		})
		t.Logf("[match] 重启后 %s 旧 Runtime 被终止；重启后 %s（强杀后 %s）Run 进入 FAILED", gone.Round(10*time.Millisecond),
			time.Since(restartedAt).Round(10*time.Millisecond), time.Since(killedAt).Round(10*time.Millisecond))
		if final.FailureCode != "UNKNOWN_PROCESS" || final.Reconcile {
			t.Fatalf("run = %+v", final)
		}
		if got := e.caseOf(run.ID); got != "RESOLVED:PROCESS_EXITED_FAILED:" && !strings.HasPrefix(got, "RESOLVED:PROCESS_EXITED_FAILED:") {
			t.Fatalf("case = %s", got)
		}
		if got := e.evidenceOf(run.ID); got != "PROCESS_EXITED" {
			t.Fatalf("evidence = %s", got)
		}
		e.expect("1", `SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, run.ID)
		e.expectReleasedOnce(run.ID, e.device.ID)
		t.Logf("[match] case=%s evidence=%s failure_code=%s；预留释放 1 次，电脑与组织占用回到 0", e.caseOf(run.ID), e.evidenceOf(run.ID), final.FailureCode)
		e.expectNoRetryThenUsable("match", conversation)
	})

	t.Run("租约过期后待核对；手动结束 Runtime 再重启：进程不存在的确定证据", func(t *testing.T) {
		conversation, run, runtimePID := e.hangAndKill("gone")
		killedAt := time.Now()
		// daemon 不在：中心在租约过期后把 Run 置为待核对并开案，对话被占用。
		pending := e.waitFor("center marks the run as reconciliation required", 60*time.Second, func() bool { return e.run(run.ID).Reconcile })
		t.Logf("[gone] 强杀后 %s 中心将 Run 置为待核对（租约 %s）；case=%s", pending.Round(100*time.Millisecond), leaseTTL, e.caseOf(run.ID))
		if state := e.run(run.ID).State; state != "RUNNING" {
			t.Fatalf("pending run state = %s", state)
		}
		// 待核对期间对话被这次 Run 占用：新消息可以写入，但不会物化出新的 Run。
		e.mustPost(conversation, e.agent, "while pending")
		time.Sleep(2 * time.Second)
		if got := e.runCount(conversation); got != 1 {
			t.Fatalf("run count while pending = %d, want 1", got)
		}
		// 手动结束 Runtime（核对命令行属于本次演练目录后才发信号），再重启 daemon。
		e.killOwnedRuntime(runtimePID)
		restartedAt := e.restartDaemon()
		var final runRow
		e.waitFor("run auto-failed", 45*time.Second, func() bool {
			final = e.run(run.ID)
			return final.State == "FAILED"
		})
		t.Logf("[gone] 重启后 %s（强杀后 %s）Run 进入 FAILED；case=%s evidence=%s", time.Since(restartedAt).Round(10*time.Millisecond),
			time.Since(killedAt).Round(100*time.Millisecond), e.caseOf(run.ID), e.evidenceOf(run.ID))
		if final.FailureCode != "UNKNOWN_PROCESS" || final.Reconcile {
			t.Fatalf("run = %+v", final)
		}
		if got := e.evidenceOf(run.ID); got != "PROCESS_NOT_FOUND" {
			t.Fatalf("evidence = %s", got)
		}
		if got := e.caseOf(run.ID); !strings.HasPrefix(got, "RESOLVED:PROCESS_EXITED_FAILED:") {
			t.Fatalf("case = %s", got)
		}
		e.expect("1", `SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, run.ID)
		e.expectReleasedOnce(run.ID, e.device.ID)
		// 待核对期间写入的那条消息在对话释放后被物化为新的 Run 并成功（这是排队输入，不是对失败 Run 的重试）。
		queued := e.waitNewRunDone(conversation, run.ID)
		if queued.State != "SUCCEEDED" {
			t.Fatalf("queued input run = %+v", queued)
		}
		e.expectNoRetryThenUsable("gone", conversation)
	})

	t.Run("启动时间不匹配（进程号被复用）：不杀进程、案保持打开，管理员经 HTTP 标记失败", func(t *testing.T) {
		conversation, run, runtimePID := e.hangAndKill("reuse")
		// daemon 已停止：把本地记录的启动时间改成不匹配，模拟进程号被另一个进程复用。
		e.journal(e.device, run.ID, func(db *sql.DB) error {
			result, err := db.ExecContext(e.ctx, `UPDATE run_journal SET start_token = start_token + 1234567 WHERE run_id = ?`, run.ID)
			if err != nil {
				return err
			}
			if rows, _ := result.RowsAffected(); rows != 1 {
				t.Fatalf("journal rows updated = %d", rows)
			}
			return nil
		})
		restartedAt := e.restartDaemon()
		opened := e.waitFor("uncertain case stays open", 60*time.Second, func() bool {
			return strings.HasPrefix(e.caseOf(run.ID), "OPEN:") && strings.HasSuffix(e.caseOf(run.ID), ":PID_REUSE") && e.run(run.ID).Reconcile
		})
		t.Logf("[reuse] 重启后 %s 案保持 OPEN、原因 PID_REUSE；evidence=%s", opened.Round(10*time.Millisecond), e.evidenceOf(run.ID))
		// 观察一个完整租约周期以上：不杀任何进程，Run 不被自动裁决。
		time.Sleep(20 * time.Second)
		if !e.ownedRuntimeAlive(runtimePID) {
			t.Fatal("the runtime process was killed although its start time did not match")
		}
		pending := e.run(run.ID)
		if pending.State != "RUNNING" || !pending.Reconcile || !strings.HasPrefix(e.caseOf(run.ID), "OPEN:") {
			t.Fatalf("pending run = %+v case = %s", pending, e.caseOf(run.ID))
		}
		e.expect("1", `SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, e.device.ID)
		t.Logf("[reuse] 重启后 %s：Runtime pid=%d 仍存活，Run=%s 待核对=%v，预留仍占用", time.Since(restartedAt).Round(time.Second), runtimePID, pending.State, pending.Reconcile)

		// 管理员经 HTTP：列表里能看到这条待核对运行；普通成员无权处理。
		var list struct {
			Runs []struct {
				RunID       string `json:"run_id"`
				ReasonCode  string `json:"reason_code"`
				CaseID      string `json:"case_id"`
				CaseVersion int64  `json:"case_version"`
			} `json:"runs"`
		}
		e.owner().must(http.StatusOK, http.MethodGet, "reconciling-runs", nil, &list)
		var caseID string
		var version int64
		for _, item := range list.Runs {
			if item.RunID == run.ID {
				caseID, version = item.CaseID, item.CaseVersion
				if item.ReasonCode != "PID_REUSE" {
					t.Fatalf("pending reason = %s", item.ReasonCode)
				}
			}
		}
		if caseID == "" {
			t.Fatalf("pending run not listed: %+v", list)
		}
		path := "runs/" + run.ID + "/reconciliation/" + caseID + "/mark-failed"
		if status, body := e.login(drillMemberID).do(http.MethodPost, path, map[string]any{"version": version, "detail": "member"}); status != http.StatusForbidden {
			t.Fatalf("member mark-failed = %d %s", status, body)
		}
		var marked struct {
			AlreadyResolved bool `json:"already_resolved"`
		}
		begin := time.Now()
		e.owner().must(http.StatusOK, http.MethodPost, path, map[string]any{"version": version, "detail": "stage12i drill: pid reuse"}, &marked)
		if marked.AlreadyResolved {
			t.Fatal("first operator call reported already resolved")
		}
		final := e.run(run.ID)
		if final.State != "FAILED" || final.Reconcile {
			t.Fatalf("run after mark-failed = %+v", final)
		}
		e.expectReleasedOnce(run.ID, e.device.ID)
		e.expect("1", `SELECT count(*)::text FROM openfox.execution_outbox WHERE kind = 'run.finished' AND aggregate_id = $1::uuid`, run.ID)
		e.expect("1", `SELECT count(*)::text FROM openfox.audit_events WHERE organization_id = $1::uuid AND action = 'run.reconciliation_resolved' AND resource_id = $2`, e.org, caseID)
		// 二次点击：已处理，不重复释放。
		status, body := e.owner().do(http.MethodPost, path, map[string]any{"version": version, "detail": "again"})
		if status != http.StatusOK || json.Unmarshal(body, &marked) != nil || !marked.AlreadyResolved {
			t.Fatalf("second mark-failed = %d %s", status, body)
		}
		e.expectReleasedOnce(run.ID, e.device.ID)
		t.Logf("[reuse] 管理员标记失败用时 %s：Run=%s failure_code=%s case=%s；成员 403；二次调用 already_resolved=true", time.Since(begin).Round(time.Millisecond),
			final.State, final.FailureCode, e.caseOf(run.ID))
		// 人工裁决不终止进程：Runtime 仍在，由演练清理（实际运维中由管理员处理该进程）。
		if !e.ownedRuntimeAlive(runtimePID) {
			t.Fatal("runtime disappeared after operator resolution")
		}
		e.killOwnedRuntime(runtimePID)
		e.expectNoRetryThenUsable("reuse", conversation)
	})

	// 收尾：正常停止 daemon，确认没有遗留本次演练的 Runtime 进程。
	if elapsed, ok := e.daemon.signalAndWait(syscall.SIGTERM, 30*time.Second); !ok {
		t.Fatalf("daemon did not stop after SIGTERM (%s)", elapsed)
	}
	if orphans := e.orphanRuntimes(); len(orphans) != 0 {
		t.Fatalf("orphan runtime processes: %v", orphans)
	}
}
