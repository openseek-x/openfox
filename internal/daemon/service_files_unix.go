//go:build darwin || linux

package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openServiceFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrUnsafeFile
	}
	return file, nil
}

func (m Manager) withServiceLock(fn func() error) error {
	if !m.supportsService() {
		return fn()
	}
	path := m.manualPath() + ".lock"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return ErrUnsafeFile
	}
	lock, err := lockIdentityFile(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	if m.registrationConfigPath != "" {
		if err := m.validateServiceRegistration(); err != nil {
			return err
		}
	}
	return fn()
}

func serviceFileIdentity(file *os.File) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

// Closing the descriptor releases the lock, including after process termination.
func lockIdentityFile(path string) (*os.File, error) {
	file, err := openServiceFile(path, unix.O_CREAT|unix.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, ErrServiceBusy
	}
	return file, nil
}
