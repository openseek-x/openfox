//go:build unix

package runtime

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// 组长已退出、组内仍有成员时：不发任何信号，返回 Unverifiable，成员保持存活。
func TestTerminateRecordedProcessLeaderExitedGroupAlive(t *testing.T) {
	restore := SetRecoverGraceForTest(50*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond)
	defer restore()
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
	identity := CurrentProcessIdentity(leader)
	buf := make([]byte, 32)
	n, _ := out.Read(buf)
	_ = cmd.Wait()
	t.Cleanup(func() { _ = syscall.Kill(-leader, syscall.SIGKILL) })
	member := 0
	for _, c := range buf[:n] {
		if c >= '0' && c <= '9' {
			member = member*10 + int(c-'0')
		}
	}
	if member <= 0 || identity.PGID != leader {
		t.Skipf("setup failed: member=%d identity=%+v", member, identity)
	}
	if got := TerminateRecordedProcess(context.Background(), identity); got != TerminateUnverifiable {
		t.Fatalf("outcome = %v, want Unverifiable", got)
	}
	if err := syscall.Kill(member, 0); err != nil {
		t.Fatalf("group member must still be alive: %v", err)
	}
	// 重复调用结果相同（同一记录被多个 Run 共享时的安全性）。
	if got := TerminateRecordedProcess(context.Background(), identity); got != TerminateUnverifiable {
		t.Fatalf("second outcome = %v", got)
	}
}

// 记录的进程与进程组都已消失：重复终止得到 Gone，不发信号。
func TestTerminateRecordedProcessTwiceGone(t *testing.T) {
	restore := SetRecoverGraceForTest(50*time.Millisecond, 50*time.Millisecond, 200*time.Millisecond)
	defer restore()
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	identity := CurrentProcessIdentity(cmd.Process.Pid)
	go func() { _ = cmd.Wait() }()
	if got := TerminateRecordedProcess(context.Background(), identity); got != TerminateGone {
		t.Fatalf("first = %v", got)
	}
	if got := TerminateRecordedProcess(context.Background(), identity); got != TerminateGone {
		t.Fatalf("second = %v", got)
	}
}
