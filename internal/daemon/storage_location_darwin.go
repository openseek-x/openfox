//go:build darwin

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The record lives beside the stable CLI path. APFS preserves the directory's
// inode across a rename. A pending pair also lets the next process finish a
// swap interrupted before an old alias was repaired.
type storageRecord struct {
	Inode   uint64 `json:"inode"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	OldLink string `json:"old_link,omitempty"`
}

func storageRecordPath(root string) string {
	return filepath.Join(filepath.Dir(root), ".openfox-storage-location.json")
}

func readStorageRecord(root string) (storageRecord, error) {
	var r storageRecord
	b, err := readPrivateFile(storageRecordPath(root), 32*1024)
	if err != nil {
		return r, err
	}
	if json.Unmarshal(b, &r) != nil || r.Inode == 0 ||
		(r.From == "") != (r.To == "") ||
		(r.From != "" && (!filepath.IsAbs(r.From) || !filepath.IsAbs(r.To) || r.From == r.To)) {
		return r, ErrUnsafeFile
	}
	return r, nil
}

func writeStorageRecord(root string, r storageRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := WritePrivateFile(storageRecordPath(root), b); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(root))
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func isStorageDirectory(path string, inode uint64, volume int32) bool {
	var s unix.Stat_t
	return unix.Lstat(path, &s) == nil && s.Mode&unix.S_IFMT == unix.S_IFDIR &&
		s.Ino == inode && s.Dev == volume && s.Uid == uint32(os.Getuid()) && s.Mode&0077 == 0
}

func finishStorageSwap(root string, r storageRecord) error {
	var parent unix.Stat_t
	if err := unix.Stat(filepath.Dir(root), &parent); err != nil {
		return err
	}
	if isStorageDirectory(r.To, r.Inode, parent.Dev) {
		// Returning to an existing alias swaps its old link into From.
		link, err := os.Readlink(r.From)
		if err != nil {
			return ErrUnsafeFile
		}
		if link != r.To {
			if r.OldLink == "" || link != r.OldLink {
				return ErrUnsafeFile
			}
			if err := replaceStorageLink(r.From, r.To); err != nil {
				return err
			}
		}
		if root != r.From && root != r.To {
			resolved, err := filepath.EvalSymlinks(root)
			if err != nil || resolved != r.To {
				return ErrUnsafeFile
			}
			if err := replaceStorageLink(root, r.To); err != nil {
				return err
			}
		}
	} else if isStorageDirectory(r.From, r.Inode, parent.Dev) {
		// No swap took place. Remove only our self-referencing reservation.
		if link, err := os.Readlink(r.To); err == nil && link == r.To && r.OldLink == "" {
			if err := os.Remove(r.To); err != nil {
				return err
			}
		}
	} else {
		return ErrUnsafeFile
	}
	for _, dir := range []string{filepath.Dir(r.From), filepath.Dir(r.To)} {
		if err := syncDirectory(dir); err != nil {
			return err
		}
	}
	return writeStorageRecord(root, storageRecord{Inode: r.Inode})
}

// Replace a known storage alias atomically. A private temporary directory
// avoids overwriting a coincidentally named file next to a user's data.
func replaceStorageLink(alias, target string) error {
	dir, err := os.MkdirTemp(filepath.Dir(alias), ".openfox-link-")
	if err != nil {
		return err
	}
	defer os.Remove(dir)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		return err
	}
	defer os.Remove(link)
	return os.Rename(link, alias)
}

// Only the platform's managed root may be an alias, and only when the private
// record identifies the very same directory on the original data volume.
func storageLocation(root string) (string, error) {
	r, err := readStorageRecord(root)
	if errors.Is(err, os.ErrNotExist) {
		info, statErr := os.Lstat(root)
		if errors.Is(statErr, os.ErrNotExist) || (statErr == nil && info.IsDir()) {
			return root, nil
		}
		return "", ErrUnsafeFile
	}
	if err != nil {
		return "", err
	}
	if r.From != "" {
		lock, err := lockIdentityFile(storageRecordPath(root) + ".lock")
		if err != nil {
			return "", err
		}
		defer lock.Close()
		r, err = readStorageRecord(root)
		if err != nil {
			return "", err
		}
		if r.From != "" {
			if err := finishStorageSwap(root, r); err != nil {
				return "", err
			}
		}
	}
	physical, err := filepath.EvalSymlinks(root)
	var parent unix.Stat_t
	if err != nil || unix.Stat(filepath.Dir(root), &parent) != nil || !isStorageDirectory(physical, r.Inode, parent.Dev) {
		return "", ErrUnsafeFile
	}
	return physical, nil
}

func checkedDirectory(dir string) bool {
	if info, err := os.Lstat(dir); err == nil && info.IsDir() {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || dir != filepath.Join(home, "Library", "Application Support", "OpenFox") {
		return false
	}
	actual, err := storageLocation(dir)
	return err == nil && actual != dir
}

func (m Manager) RelocateStorage(parent string, expected DeviceConfig) (string, error) {
	root := filepath.Dir(m.SocketPath)
	source, err := storageLocation(root)
	if err != nil {
		return "", err
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil || !filepath.IsAbs(parent) {
		return "", ErrStorageTarget
	}
	target := filepath.Join(parent, "OpenFox")
	if source == target {
		return source, nil
	}
	if containsStoragePath(source, target) || containsStoragePath(target, source) {
		return "", ErrStorageTarget
	}
	var src, dst unix.Stat_t
	if unix.Lstat(source, &src) != nil || unix.Stat(parent, &dst) != nil ||
		!isStorageDirectory(source, src.Ino, src.Dev) || dst.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", ErrStorageTarget
	}
	if src.Dev != dst.Dev {
		return "", ErrStorageVolume
	}
	// The selected parent must not allow another account to replace the data
	// directory. Verify its ancestry, including any user-owned intermediate.
	for dir := parent; ; dir = filepath.Dir(dir) {
		var s unix.Stat_t
		if unix.Lstat(dir, &s) != nil || (s.Uid != 0 && s.Uid != uint32(os.Getuid())) || (s.Mode&0022 != 0 && s.Mode&unix.S_ISVTX == 0) {
			return "", ErrStorageTarget
		}
		if dir == "/" {
			break
		}
	}
	locationLock, err := lockIdentityFile(storageRecordPath(root) + ".lock")
	if err != nil {
		return "", ErrStorageBusy
	}
	defer locationLock.Close()
	idLock, err := LockDeviceIdentity(root)
	if err != nil {
		return "", ErrStorageBusy
	}
	defer idLock.Close()
	err = m.withServiceLock(func() error {
		config, err := LoadDeviceConfig(m.registrationConfigPath)
		if err != nil {
			return err
		}
		if config.DeviceID != expected.DeviceID || config.OrganizationID != expected.OrganizationID || config.CenterURL != expected.CenterURL {
			return ErrStorageBusy
		}
		for _, registered := range append([]string{config.WorkspaceRoot}, directoryValues(config.DirectoryRoots)...) {
			if registered == "" {
				continue
			}
			real, resolveErr := filepath.EvalSymlinks(registered)
			if resolveErr != nil {
				real = filepath.Clean(registered)
			}
			if containsStoragePath(real, target) || containsStoragePath(target, real) {
				return ErrStorageTarget
			}
		}
		actual, err := storageLocation(root)
		if err != nil || actual != source {
			return ErrStorageBusy
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// A loaded launchd job can restart while its socket is absent. 113 is
		// launchctl's "service not found" exit status; other failures are unknown.
		err = m.launchctl(ctx, "print", m.domainLabel())
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 113 {
			return ErrStorageBusy
		}
		if _, err := m.QueryStatus(ctx); !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return ErrStorageBusy
		}
		runLock, err := lockIdentityFile(filepath.Join(root, "storage-use.lock"))
		if err != nil {
			return ErrStorageBusy
		}
		defer runLock.Close()
		return swapStorage(root, source, target, src.Ino)
	})
	if errors.Is(err, ErrServiceBusy) {
		return "", ErrStorageBusy
	}
	if err != nil {
		return "", err
	}
	return target, nil
}

func containsStoragePath(parent, child string) bool {
	parent, child = strings.ToLower(parent), strings.ToLower(child)
	return parent == child || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func directoryValues(roots map[string]string) []string {
	var dirs []string
	for _, dir := range roots {
		dirs = append(dirs, dir)
	}
	return dirs
}

func swapStorage(root, source, target string, inode uint64) error {
	// A prior location can be selected to move back. Other existing entries
	// (including empty directories) belong to the user and are never replaced.
	r := storageRecord{Inode: inode, From: source, To: target}
	if _, err := os.Lstat(target); err == nil {
		actual, err := filepath.EvalSymlinks(target)
		oldLink, linkErr := os.Readlink(target)
		if err != nil || actual != source || linkErr != nil {
			return ErrStorageTarget
		}
		r.OldLink = oldLink
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeStorageRecord(root, r); err != nil {
		return err
	}
	if r.OldLink == "" {
		if err := os.Symlink(target, target); err != nil {
			_ = writeStorageRecord(root, storageRecord{Inode: inode})
			return err
		}
	}
	if err := unix.RenamexNp(source, target, unix.RENAME_SWAP); err != nil {
		if rollback := finishStorageSwap(root, r); rollback != nil {
			return rollback
		}
		return err
	}
	return finishStorageSwap(root, r)
}

// Hold for the lifetime of a foreground daemon too, even before its socket is listening.
func lockStorageRun(dir string, paths []string) (*os.File, error) {
	if len(paths) != 0 {
		physical, err := storageLocation(dir)
		if err != nil {
			return nil, err
		}
		usesStorage := false
		for _, path := range paths {
			if path == "" {
				continue
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			if containsStoragePath(dir, abs) || containsStoragePath(physical, abs) {
				usesStorage = true
				break
			}
			if real, err := filepath.EvalSymlinks(abs); err == nil && containsStoragePath(physical, real) {
				usesStorage = true
				break
			}
		}
		if !usesStorage {
			return nil, nil
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if !checkedDirectory(dir) {
		return nil, ErrUnsafeFile
	}
	return lockServiceFile(filepath.Join(dir, "storage-use.lock"), unix.LOCK_SH)
}
