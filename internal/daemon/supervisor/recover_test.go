//go:build unix

package supervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"openfox/internal/runtime"
)

// startOrphan 启动一个独立进程组的真实子进程（模拟 daemon 崩溃时遗留的 Runtime 进程）。
func startOrphan(t *testing.T) (*exec.Cmd, runtime.ProcessIdentity, chan struct{}) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }() // 回收僵尸，使存在性检查准确
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	identity := runtime.CurrentProcessIdentity(cmd.Process.Pid)
	if identity.StartToken == 0 || identity.PGID != cmd.Process.Pid {
		t.Fatalf("identity = %+v", identity)
	}
	return cmd, identity, done
}

// seedCrashedRun 在日志里写入“停在 RUNNING 的旧记录”。procKnown=false 模拟升级前的旧行。
func seedCrashedRun(t *testing.T, path string, identity *runtime.ProcessIdentity, procKnown bool) {
	t.Helper()
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if _, err := journal.Begin(context.Background(), testOrg, testRun, 1, testOffer); err != nil {
		t.Fatal(err)
	}
	if identity != nil {
		if err := journal.RecordProcess(context.Background(), testRun, 1, *identity); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.Mark(context.Background(), testRun, 1, JournalRunning, ""); err != nil {
		t.Fatal(err)
	}
	if !procKnown {
		if _, err := journal.db.Exec(`UPDATE run_journal SET proc_known = NULL`); err != nil {
			t.Fatal(err)
		}
	}
}

func recoverHarness(t *testing.T, center *fakeCenter, path string) *harness {
	t.Helper()
	restore := runtime.SetRecoverGraceForTest(100*time.Millisecond, 100*time.Millisecond, 500*time.Millisecond)
	t.Cleanup(restore)
	// 不让 fakeCenter 下发新的 offer，只观察核对。
	center.offerAvailable = false
	return newHarness(t, center, &fakeAdapter{}, path)
}

func waitReports(t *testing.T, center *fakeCenter, n int) []ProcessReportInput {
	t.Helper()
	waitFor(t, "process report", func() bool { return len(center.snapshot().reports) >= n })
	return center.snapshot().reports
}

func TestRecoverKillsMatchingProcessGroupAndReportsExited(t *testing.T) {
	_, identity, done := startOrphan(t)
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, &identity, true)
	center := newCenter()
	h := recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Kind != "PROCESS_EXITED" || !reports[0].Certain || reports[0].Generation != 1 {
		t.Fatalf("report = %+v", reports[0])
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("old process was not terminated")
	}
	waitFor(t, "report done", func() bool {
		status, _ := h.journal.ReportStatus(context.Background(), testRun, 1)
		return status == ReportDone
	})
}

func TestRecoverMissingProcessReportsNotFound(t *testing.T) {
	cmd, identity, done := startOrphan(t)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	<-done
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, &identity, true)
	center := newCenter()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Kind != "PROCESS_NOT_FOUND" || !reports[0].Certain {
		t.Fatalf("report = %+v", reports[0])
	}
}

func TestRecoverNeverStartedProcessReportsNotFoundCertain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, nil, true)
	center := newCenter()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Kind != "PROCESS_NOT_FOUND" || !reports[0].Certain {
		t.Fatalf("report = %+v", reports[0])
	}
}

func TestRecoverReusedPIDIsUncertainAndNeverKilled(t *testing.T) {
	cmd, identity, done := startOrphan(t)
	identity.StartToken++ // 记录的启动时间与真实进程不一致：等价于 PID 被复用
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, &identity, true)
	center := newCenter()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Kind != "PROCESS_NOT_FOUND" || reports[0].Certain {
		t.Fatalf("report = %+v", reports[0])
	}
	select {
	case <-done:
		t.Fatal("a process with a mismatching start time was killed")
	case <-time.After(300 * time.Millisecond):
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("process should still be alive: %v", err)
	}
}

func TestRecoverLegacyRecordIsUncertainAndNeverKilled(t *testing.T) {
	_, _, done := startOrphan(t)
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, nil, false) // 旧格式行：没有进程身份
	center := newCenter()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Certain {
		t.Fatalf("legacy record must be uncertain: %+v", reports[0])
	}
	select {
	case <-done:
		t.Fatal("process was killed from a legacy record")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRecoverUnverifiableStartTimeIsUncertain(t *testing.T) {
	_, identity, done := startOrphan(t)
	identity.StartToken = 0 // 记录里没有启动时间
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, &identity, true)
	center := newCenter()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Certain {
		t.Fatalf("report = %+v", reports[0])
	}
	select {
	case <-done:
		t.Fatal("process killed without verified start time")
	case <-time.After(200 * time.Millisecond):
	}
}

// 第一次启动上报失败；第二次启动必须沿用同一证据 ID，且不再次核对或终止进程。
func TestRecoverTwiceReusesEvidenceAndDoesNotKillAgain(t *testing.T) {
	_, identity, done := startOrphan(t)
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, &identity, true)
	center := newCenter()
	center.reportFail = 1000 // 第一次启动期间上报一律失败
	first := recoverHarness(t, center, path)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("old process was not terminated")
	}
	waitFor(t, "pending report", func() bool {
		status, _ := first.journal.ReportStatus(context.Background(), testRun, 1)
		return status == ReportPending
	})
	pending, err := first.journal.PendingReports(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	firstID := pending[0].EvidenceID
	first.stop()

	// 在旧 PID 上放一个“无关进程”：第二次启动若再次核对会误杀它。
	bystander := exec.Command("sleep", "60")
	bystander.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := bystander.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-bystander.Process.Pid, syscall.SIGKILL); _ = bystander.Wait() })
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = journal.RecordProcess(context.Background(), testRun, 1, runtime.CurrentProcessIdentity(bystander.Process.Pid))
	_ = journal.Close()

	center.mu.Lock()
	center.reportFail = 0
	center.mu.Unlock()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].EvidenceID != firstID || reports[0].Kind != "PROCESS_EXITED" || !reports[0].Certain {
		t.Fatalf("second start report = %+v (first id %s)", reports[0], firstID)
	}
	if err := syscall.Kill(bystander.Process.Pid, 0); err != nil {
		t.Fatalf("second start killed an unrelated process: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestJournalMigratesLegacyRunJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	journal, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧库：删除新列后重新打开应能自动补列，旧行进程身份视为未知。
	for _, column := range []string{"proc_known", "pid", "start_token", "pgid", "report_status", "report_id", "report_kind", "report_certain", "report_detail"} {
		if _, err := journal.db.Exec(`ALTER TABLE run_journal DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := journal.db.Exec(`INSERT INTO run_journal (run_id, generation, organization_id, offer_id, state, created_at, updated_at) VALUES (?, 1, ?, ?, 'RUNNING', 1, 1)`, testRun, testOrg, testOffer); err != nil {
		t.Fatal(err)
	}
	_ = journal.Close()
	reopened, err := OpenJournal(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	runs, err := reopened.AbandonUnfinished(context.Background())
	if err != nil || len(runs) != 1 || runs[0].ProcKnown || runs[0].EvidenceID == "" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
}

// startLeaderlessGroup 启动一个独立进程组：组长（sh）立即退出，组内留下一个仍存活的成员（sleep）。
// 返回组长身份（记录里的 pid 已不存在）与存活成员的进程号。
func startLeaderlessGroup(t *testing.T) (runtime.ProcessIdentity, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60 >/dev/null 2>&1 & echo $!")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	leader := cmd.Process.Pid
	// 趁组长尚未回收时取身份，保证 StartToken 与 PGID 有效。
	identity := runtime.CurrentProcessIdentity(leader)
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	_ = cmd.Wait()
	member := 0
	for _, c := range buf[:n] {
		if c >= '0' && c <= '9' {
			member = member*10 + int(c-'0')
		}
	}
	if member <= 0 || identity.PID != leader {
		t.Fatalf("member=%d identity=%+v", member, identity)
	}
	t.Cleanup(func() { _ = syscall.Kill(-leader, syscall.SIGKILL) })
	if runtime.InspectProcess(leader).Exists {
		t.Fatal("group leader should have exited")
	}
	return identity, member
}

// 组长已退出但进程组仍有成员：组号可能已被无关进程组复用，不得发信号，上报不确定。
func TestRecoverLeaderExitedGroupAliveIsUncertainAndNeverKilled(t *testing.T) {
	identity, member := startLeaderlessGroup(t)
	if identity.PGID == 0 {
		t.Skip("leader exited before its identity could be captured")
	}
	path := filepath.Join(t.TempDir(), "j", "journal.db")
	seedCrashedRun(t, path, &identity, true)
	center := newCenter()
	recoverHarness(t, center, path)
	reports := waitReports(t, center, 1)
	if reports[0].Kind != "PROCESS_NOT_FOUND" || reports[0].Certain {
		t.Fatalf("report = %+v, want uncertain NOT_FOUND", reports[0])
	}
	if err := syscall.Kill(member, 0); err != nil {
		t.Fatalf("group member must survive, kill(0) = %v", err)
	}
}
