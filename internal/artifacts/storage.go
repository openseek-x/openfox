// Package artifacts owns the bounded file-storage boundary for WP-12.
package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const MaxArtifactBytes int64 = 20 * 1024 * 1024

var (
	ErrInvalidKey = errors.New("artifacts: invalid storage key")
	ErrTooLarge   = errors.New("artifacts: file exceeds size limit")
	ErrNoRoot     = errors.New("artifacts: storage root is not configured")
)

type Stored struct {
	Size   int64
	SHA256 string
}

type Storage interface {
	Put(context.Context, string, io.Reader, int64) (Stored, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

// FreeSpaceProvider is optional so callers can enforce organization disk
// watermarks without widening the storage interface used by existing stores.
type FreeSpaceProvider interface {
	FreeBytes(context.Context) (uint64, error)
}

type Local struct {
	Root string
}

func (l Local) FreeBytes(ctx context.Context) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	root, err := filepath.Abs(strings.TrimSpace(l.Root))
	if err != nil || strings.TrimSpace(l.Root) == "" {
		return 0, ErrNoRoot
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		root = filepath.Dir(root)
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(root, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}

func (l Local) Put(ctx context.Context, key string, source io.Reader, maxBytes int64) (Stored, error) {
	if err := validateKey(key); err != nil {
		return Stored{}, err
	}
	if source == nil || maxBytes <= 0 || maxBytes > MaxArtifactBytes {
		return Stored{}, ErrTooLarge
	}
	path, err := l.path(key)
	if err != nil {
		return Stored{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Stored{}, fmt.Errorf("create artifact directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return Stored{}, fmt.Errorf("create artifact temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	hash := sha256.New()
	writer := io.MultiWriter(temporary, hash)
	var size int64
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return Stored{}, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			size += int64(count)
			if size > maxBytes {
				return Stored{}, ErrTooLarge
			}
			if _, err := writer.Write(buffer[:count]); err != nil {
				return Stored{}, fmt.Errorf("write artifact: %w", err)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return Stored{}, fmt.Errorf("read artifact: %w", readErr)
		}
	}
	if err := temporary.Sync(); err != nil {
		return Stored{}, fmt.Errorf("sync artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Stored{}, fmt.Errorf("close artifact: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		return Stored{}, fmt.Errorf("protect artifact: %w", err)
	}
	// Link-then-remove refuses to replace an existing object. Artifact keys are
	// immutable, so a retry must not silently corrupt an already published file.
	if err := os.Link(temporaryPath, path); err != nil {
		return Stored{}, fmt.Errorf("publish artifact: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(path)
		return Stored{}, fmt.Errorf("finalize artifact: %w", err)
	}
	removeTemporary = false
	return Stored{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (l Local) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := l.path(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (l Local) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := l.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (l Local) path(key string) (string, error) {
	if strings.TrimSpace(l.Root) == "" {
		return "", ErrNoRoot
	}
	if err := validateKey(key); err != nil {
		return "", err
	}
	root, err := filepath.Abs(l.Root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, filepath.FromSlash(key))
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", ErrInvalidKey
	}
	return path, nil
}

func validateKey(key string) error {
	if key == "" || strings.ContainsRune(key, '\x00') || filepath.IsAbs(key) {
		return ErrInvalidKey
	}
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return ErrInvalidKey
	}
	return nil
}
