//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package runtime

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// 停止升级的宽限期：先 SIGINT 让 Runtime 自行收尾，再 SIGTERM，最后 SIGKILL。
// 定义为变量以便测试缩短。
var (
	interruptGrace = 10 * time.Second
	terminateGrace = 5 * time.Second
)

func terminateProcess(cmd *exec.Cmd, done <-chan struct{}) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGINT)
	interrupt, terminate := interruptGrace, terminateGrace
	go func() {
		timer := time.NewTimer(interrupt)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGTERM)
		}
		timer.Reset(terminate)
		select {
		case <-done:
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}()
}

func isAbsolutePath(path string) bool {
	return len(path) > 0 && path[0] == '/'
}
