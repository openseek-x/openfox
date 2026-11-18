//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package main

import "os/exec"

func configureDevelopmentToolProcess(cmd *exec.Cmd) {}
