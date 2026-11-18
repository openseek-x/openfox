package codex

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"openfox/internal/artifacts"
	"openfox/internal/runtime"
)

const sessionExportRetention = 24 * time.Hour
const maxCodexSessionSearchEntries = 100_000

var errCodexSessionNotFound = errors.New("codex session rollout not found")

// ExportSession reads only the rollout captured from this adapter's isolated CODEX_HOME.
func (a *Adapter) ExportSession(ctx context.Context, sessionID string) (runtime.SessionFile, error) {
	if a == nil || !validCodexSessionID(sessionID) || strings.TrimSpace(a.ExportRoot) == "" {
		return runtime.SessionFile{}, runtime.ErrInvalidRequest
	}
	if ctx == nil {
		ctx = context.Background()
	}
	root := filepath.Join(a.ExportRoot, sessionID)
	var source string
	entries := 0
	err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if errors.Is(walkErr, os.ErrNotExist) {
			return fs.SkipAll
		}
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxCodexSessionSearchEntries {
			return errors.New("codex staged session search limit exceeded")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
			if source != "" {
				return errors.New("multiple staged Codex rollouts match thread id")
			}
			source = file
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return runtime.SessionFile{}, err
		}
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	if source == "" {
		return runtime.SessionFile{}, errCodexSessionNotFound
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return runtime.SessionFile{}, errCodexSessionNotFound
	}
	if info.Size() < 1 || info.Size() > artifacts.MaxArtifactBytes {
		return runtime.SessionFile{}, artifacts.ErrTooLarge
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	return runtime.SessionFile{Name: "sessions/" + filepath.ToSlash(relative), Data: data}, nil
}

func (a *Adapter) copySessionToExport(homeDir, sessionID string) error {
	if a == nil {
		return runtime.ErrUnavailable
	}
	return copySessionToExport(a.ExportRoot, homeDir, sessionID)
}

func copySessionToExport(exportRoot, homeDir, sessionID string) error {
	if strings.TrimSpace(exportRoot) == "" || !validCodexSessionID(sessionID) {
		return runtime.ErrInvalidRequest
	}
	var source string
	entries := 0
	err := filepath.WalkDir(filepath.Join(homeDir, "sessions"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > maxCodexSessionSearchEntries {
			return errors.New("codex session search limit exceeded")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, "-"+sessionID+".jsonl") {
			if source != "" {
				return errors.New("multiple codex rollout files match thread id")
			}
			source = path
		}
		return nil
	})
	if err != nil {
		return runtime.ErrUnavailable
	}
	if source == "" {
		return errCodexSessionNotFound
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > artifacts.MaxArtifactBytes {
		return artifacts.ErrTooLarge
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return runtime.ErrUnavailable
	}
	relative, err := filepath.Rel(filepath.Join(homeDir, "sessions"), source)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return runtime.ErrUnavailable
	}
	if err := os.MkdirAll(exportRoot, 0o700); err != nil {
		return runtime.ErrUnavailable
	}
	if err := os.Chmod(exportRoot, 0o700); err != nil {
		return runtime.ErrUnavailable
	}
	sessionRoot := filepath.Join(exportRoot, sessionID)
	targetDir := filepath.Join(sessionRoot, filepath.Dir(relative))
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return runtime.ErrUnavailable
	}
	for dir := targetDir; ; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o700); err != nil {
			return runtime.ErrUnavailable
		}
		if dir == sessionRoot {
			break
		}
	}
	target := filepath.Join(targetDir, filepath.Base(relative))
	temp, err := os.CreateTemp(targetDir, ".codex-session-*")
	if err != nil {
		return runtime.ErrUnavailable
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return runtime.ErrUnavailable
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return runtime.ErrUnavailable
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return runtime.ErrUnavailable
	}
	if err := temp.Close(); err != nil {
		return runtime.ErrUnavailable
	}
	if err := os.Rename(tempPath, target); err != nil {
		return runtime.ErrUnavailable
	}
	return nil
}

// RemoveStaleSessionExports removes only regular rollout snapshots older than 24 hours.
func RemoveStaleSessionExports(root string, now time.Time) (int, int64) {
	var files []string
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if errors.Is(walkErr, os.ErrNotExist) {
			return fs.SkipAll
		}
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != root {
				directories = append(directories, path)
			}
			return nil
		}
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
			info, err := entry.Info()
			if err == nil && now.Sub(info.ModTime()) > sessionExportRetention {
				files = append(files, path)
			}
		}
		return nil
	}); err != nil {
		return 0, 0
	}
	removed := 0
	var freed int64
	for _, path := range files {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if os.Remove(path) == nil {
			removed++
			freed += info.Size()
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		_ = os.Remove(directories[index])
	}
	return removed, freed
}

func validCodexSessionID(id string) bool {
	if len(id) < 1 || len(id) > 128 || strings.TrimSpace(id) != id || strings.Contains(id, "..") {
		return false
	}
	for _, char := range id {
		if char < 0x21 || char == 0x7f || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}
