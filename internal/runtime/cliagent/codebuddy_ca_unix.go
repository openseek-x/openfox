//go:build unix

package cliagent

import (
	"os"

	"golang.org/x/sys/unix"
)

// Nonblocking open prevents a file replaced with a FIFO from hanging PREPARE.
// The opened descriptor is checked for a regular, bounded file before reading.
func openCodeBuddyCAFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "codebuddy-ca"), nil
}
