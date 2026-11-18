//go:build !darwin && !linux

package daemon

import "os"

func (m Manager) withServiceLock(fn func() error) error { return fn() }
func openServiceFile(string, int) (*os.File, error)     { return nil, ErrServiceUnavailable }
func serviceFileIdentity(*os.File) (string, error)      { return "", ErrServiceUnavailable }
