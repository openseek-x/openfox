//go:build !darwin && !linux && !windows

package daemon

import "os"

func lockIdentityFile(string) (*os.File, error) { return nil, ErrServiceUnavailable }
