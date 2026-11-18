//go:build unix

package daemon

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func lockMachineProxy(ctx context.Context, path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrMachineProxyInvalid
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, ErrMachineProxyInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
		} else if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, ErrMachineProxyInvalid
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ErrMachineProxyBusy
		case <-time.After(10 * time.Millisecond):
		}
	}
}
