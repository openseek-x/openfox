//go:build windows || plan9 || wasip1

package runtime

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd, _ <-chan struct{}) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func isAbsolutePath(path string) bool {
	if len(path) >= 2 && path[1] == ':' {
		return true
	}
	return len(path) > 0 && (path[0] == '/' || path[0] == '\\')
}
