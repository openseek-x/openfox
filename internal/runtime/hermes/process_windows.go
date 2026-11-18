//go:build windows

package hermes

import (
	"os"
	"os/exec"
)

func openWorkspace(path string) (*os.File, error) { return os.Open(path) }

func configureGroup(*exec.Cmd)                      {}
func stopGroup(cmd *exec.Cmd, done <-chan struct{}) { _ = cmd.Process.Kill(); <-done }
