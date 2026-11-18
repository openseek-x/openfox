//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Version-manager shims can spawn a child. Stop the whole probe process group
// on timeout/cancellation instead of leaving that child running every refresh.
func configureDevelopmentToolProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
