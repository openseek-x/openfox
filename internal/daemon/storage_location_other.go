//go:build !darwin

package daemon

import (
	"os"
)

func storageLocation(root string) (string, error) { return root, nil }
func checkedDirectory(dir string) bool            { st, err := os.Lstat(dir); return err == nil && st.IsDir() }
func (m Manager) RelocateStorage(string, DeviceConfig) (string, error) {
	return "", ErrServiceUnavailable
}
func lockStorageRun(string, []string) (*os.File, error) { return nil, nil }
