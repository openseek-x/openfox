//go:build darwin

package runtime

import "golang.org/x/sys/unix"

// processStartToken 返回进程启动时间（微秒），用于区分被复用的 PID。
func processStartToken(pid int) (int64, bool) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(info.Proc.P_pid) != pid {
		return 0, false
	}
	start := info.Proc.P_starttime
	token := int64(start.Sec)*1_000_000 + int64(start.Usec)
	return token, token > 0
}
