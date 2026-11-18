//go:build darwin

package cliagent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const geminiSnapshotBytes int64 = 64 << 20
const geminiSnapshotFileBytes int64 = 8 << 20
const geminiSnapshotFiles = 10000
const geminiSnapshotEntries = 50000

type geminiSnapshotStats struct {
	Bytes                              int64
	Files, Entries, Excluded, Symlinks int
}

func geminiSnapshot(ctx context.Context, source, dest string) (stats geminiSnapshotStats, err error) {
	if err = os.Mkdir(dest, 0700); err != nil {
		return stats, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dest)
		}
	}()
	if source == "" {
		return stats, nil
	}
	fd, e := unix.Open(source, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if e != nil {
		return stats, fmt.Errorf("Gemini source directory cannot be pinned")
	}
	root := os.NewFile(uintptr(fd), source)
	defer root.Close()
	deadline := time.Now().Add(60 * time.Second)
	check := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Gemini snapshot preparation exceeds 60 seconds")
		}
		return nil
	}
	var walk func(*os.File, string, int) error
	walk = func(dir *os.File, target string, depth int) error {
		if depth > 64 {
			return fmt.Errorf("Gemini snapshot depth exceeds 64")
		}
		for {
			if e := check(); e != nil {
				return e
			}
			entries, readErr := dir.ReadDir(128)
			if readErr != nil && readErr != io.EOF {
				return fmt.Errorf("Gemini source directory changed or is unreadable")
			}
			for _, entry := range entries {
				if e := check(); e != nil {
					return e
				}
				stats.Entries++
				if stats.Entries > geminiSnapshotEntries {
					return fmt.Errorf("Gemini snapshot exceeds 50000 entries")
				}
				name := entry.Name()
				if geminiExcludedName(name) {
					stats.Excluded++
					continue
				}
				var before unix.Stat_t
				if e := unix.Fstatat(int(dir.Fd()), name, &before, unix.AT_SYMLINK_NOFOLLOW); e != nil {
					return fmt.Errorf("Gemini source changed during enumeration")
				}
				if before.Mode&unix.S_IFMT == unix.S_IFLNK {
					stats.Symlinks++
					continue
				}
				directory := before.Mode&unix.S_IFMT == unix.S_IFDIR
				if !directory && before.Mode&unix.S_IFMT != unix.S_IFREG {
					return fmt.Errorf("Gemini snapshot contains a special file")
				}
				flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
				if directory {
					flags |= unix.O_DIRECTORY
				}
				childFD, e := unix.Openat(int(dir.Fd()), name, flags, 0)
				if e != nil {
					return fmt.Errorf("Gemini source changed while opening")
				}
				child := os.NewFile(uintptr(childFD), name)
				var pinned unix.Stat_t
				e = unix.Fstat(childFD, &pinned)
				if e != nil || !geminiSameVersion(before, pinned) {
					child.Close()
					return fmt.Errorf("Gemini source changed before copying")
				}
				dst := filepath.Join(target, name)
				if directory {
					if e = os.Mkdir(dst, 0700); e == nil {
						e = walk(child, dst, depth+1)
					}
				} else {
					stats.Files++
					if stats.Files > geminiSnapshotFiles {
						child.Close()
						return fmt.Errorf("Gemini snapshot exceeds 10000 files")
					}
					if pinned.Size > geminiSnapshotFileBytes || pinned.Size < 0 || stats.Bytes+pinned.Size > geminiSnapshotBytes {
						child.Close()
						return fmt.Errorf("Gemini snapshot exceeds 8 MiB per file or 64 MiB total")
					}
					var out *os.File
					out, e = os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if e == nil {
						var copied int64
						buf := make([]byte, 64<<10)
						for {
							if e = check(); e != nil {
								break
							}
							var n int
							var readError error
							n, readError = child.Read(buf)
							if n > 0 {
								copied += int64(n)
								stats.Bytes += int64(n)
								if copied > geminiSnapshotFileBytes || stats.Bytes > geminiSnapshotBytes {
									e = fmt.Errorf("Gemini source grew beyond snapshot budget")
									break
								}
								if _, e = out.Write(buf[:n]); e != nil {
									break
								}
							}
							if readError == io.EOF {
								break
							}
							if readError != nil {
								e = readError
								break
							}
						}
						if closeErr := out.Close(); e == nil {
							e = closeErr
						}
					}
				}
				var after unix.Stat_t
				statErr := unix.Fstat(childFD, &after)
				_ = child.Close()
				if e != nil {
					return e
				}
				if statErr != nil || !geminiSameVersion(pinned, after) {
					return fmt.Errorf("Gemini source changed during copying")
				}
			}
			if readErr == io.EOF {
				return nil
			}
		}
	}
	err = walk(root, dest, 0)
	return stats, err
}
func geminiSameVersion(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
func geminiExcludedName(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case ".git", ".gemini", ".agents", ".claude", ".codex", ".hermes", ".cursor", "node_modules", "__pycache__", ".ssh", ".aws", ".config", "gemini.md":
		return true
	}
	if strings.HasPrefix(lower, ".env") {
		return true
	}
	for _, ext := range []string{".pem", ".key", ".p8", ".pk8", ".p12", ".pfx", ".jks", ".keystore", ".kdbx"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return geminiPrivateName(lower)
}
func geminiPrivateName(name string) bool {
	if !strings.HasPrefix(name, "openfox-") {
		return false
	}
	i := strings.LastIndexByte(name, '-')
	if i < 8 || i == len(name)-1 {
		return false
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func geminiWorkspaceAllowed(work, runRoot string) error {
	for _, part := range strings.Split(filepath.Clean(work), string(os.PathSeparator)) {
		if geminiPrivateName(strings.ToLower(part)) {
			return fmt.Errorf("Gemini workspace selects private Run state")
		}
	}
	w, e := os.Stat(work)
	if e != nil || !w.IsDir() {
		return fmt.Errorf("Gemini workspace is unavailable")
	}
	for p := runRoot; ; p = filepath.Dir(p) {
		info, e := os.Stat(p)
		if e != nil {
			return e
		}
		if os.SameFile(w, info) {
			return fmt.Errorf("Gemini workspace contains private Run state")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
