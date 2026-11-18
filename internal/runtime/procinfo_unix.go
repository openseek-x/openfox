//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package runtime

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// InspectProcess 观察进程号是否存在及其启动时间令牌。
func InspectProcess(pid int) ProcessProbe {
	if pid <= 0 {
		return ProcessProbe{}
	}
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return ProcessProbe{}
	}
	token, ok := processStartToken(pid)
	return ProcessProbe{Exists: true, StartKnown: ok, StartToken: token}
}

// CurrentProcessIdentity 取得刚启动的子进程的身份；取不到启动时间时 StartToken 为 0。
func CurrentProcessIdentity(pid int) ProcessIdentity {
	identity := ProcessIdentity{PID: pid}
	if pid <= 0 {
		return identity
	}
	// 只有以该进程为组长（Setpgid 创建的独立进程组）时才记录进程组号；
	// 否则（例如复用宿主进程的会话）PGID=0，终止时只对该进程本身发信号，绝不按组杀。
	if pgid, err := syscall.Getpgid(pid); err == nil && pgid == pid {
		identity.PGID = pgid
	}
	if token, ok := processStartToken(pid); ok {
		identity.StartToken = token
	}
	return identity
}

func groupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// 升级终止的宽限期，定义为变量以便测试缩短。
var (
	recoverInterruptGrace = 5 * time.Second
	recoverTerminateGrace = 5 * time.Second
	recoverKillGrace      = 3 * time.Second
	recoverPollInterval   = 50 * time.Millisecond
)

// SetRecoverGraceForTest 缩短重启核对时的终止宽限期，返回恢复函数。仅供测试使用。
func SetRecoverGraceForTest(interrupt, terminate, kill time.Duration) func() {
	oldI, oldT, oldK := recoverInterruptGrace, recoverTerminateGrace, recoverKillGrace
	recoverInterruptGrace, recoverTerminateGrace, recoverKillGrace = interrupt, terminate, kill
	return func() { recoverInterruptGrace, recoverTerminateGrace, recoverKillGrace = oldI, oldT, oldK }
}

// TerminateRecordedProcess 按记录的身份终止旧进程组：先校验 (pid, 启动时间, 进程组号)，
// 再 SIGINT → SIGTERM → SIGKILL 升级，确认进程组消失后返回 TerminateGone。
// 校验不通过（PID 被复用、取不到启动时间、进程组不是以该进程为组长）时不发送任何信号。
func TerminateRecordedProcess(ctx context.Context, identity ProcessIdentity) TerminateOutcome {
	if identity.PID <= 0 || identity.PID == os.Getpid() || (identity.PGID != 0 && identity.PGID != identity.PID) {
		// 非法记录：进程组号必须为 0（只杀进程）或等于进程号（独立进程组），否则杀组可能波及 daemon 自己。
		return TerminateUnverifiable
	}
	// alive 在每一步发信号前都会重新判断：
	//   - 按组终止：只看进程组是否还有成员（组员存活期间组号不会被复用）；
	//   - 只杀单进程：进程号必须仍存在且启动时间与记录一致，否则视为已退出，
	//     这样组长在上一步退出、进程号随后被复用时不会对无关进程发信号。
	alive := func() bool {
		if identity.PGID != 0 {
			return groupAlive(identity.PGID)
		}
		current := InspectProcess(identity.PID)
		return current.Exists && current.StartKnown && current.StartToken == identity.StartToken
	}
	probe := InspectProcess(identity.PID)
	if !probe.Exists {
		if identity.PGID != 0 && groupAlive(identity.PGID) {
			// 组长已退出但进程组仍有成员：组长退出后进程号可能被无关进程复用并成为新的组长，
			// 随后又退出而留下组员，此时同一个组号上的可能是无关进程组，无法确认归属。
			// 按“不确定就不杀任何进程”处理，不发送任何信号，留给人工。
			return TerminateUnverifiable
		}
		return TerminateGone
	}
	if !probe.StartKnown || identity.StartToken == 0 {
		return TerminateUnverifiable
	}
	if probe.StartToken != identity.StartToken {
		return TerminateMismatch
	}
	return escalate(ctx, identity, alive)
}

func escalate(ctx context.Context, identity ProcessIdentity, alive func() bool) TerminateOutcome {
	target := identity.PID
	if identity.PGID != 0 {
		target = -identity.PGID
	}
	steps := []struct {
		signal syscall.Signal
		grace  time.Duration
	}{
		{syscall.SIGINT, recoverInterruptGrace},
		{syscall.SIGTERM, recoverTerminateGrace},
		{syscall.SIGKILL, recoverKillGrace},
	}
	for _, step := range steps {
		if !alive() {
			return TerminateGone
		}
		_ = syscall.Kill(target, step.signal)
		deadline := time.Now().Add(step.grace)
		for time.Now().Before(deadline) {
			if !alive() {
				return TerminateGone
			}
			select {
			case <-ctx.Done():
				return TerminateFailed
			case <-time.After(recoverPollInterval):
			}
		}
	}
	if alive() {
		return TerminateFailed
	}
	return TerminateGone
}
