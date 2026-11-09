package workdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func canonicalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%w: path is empty", ErrInvalidRequest)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	absolute = filepath.Clean(absolute)

	current := absolute
	var suffix []string
	for {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", fmt.Errorf("resolve symlinks %q: %w", current, evalErr)
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", fmt.Errorf("inspect path %q: %w", current, statErr)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("resolve existing parent for %q: %w", path, statErr)
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathContains(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

func (p GuardPolicy) maxPathLength() int {
	if p.MaxPathLength <= 0 {
		return 4096
	}
	return p.MaxPathLength
}

func normalizeRoots(roots []string) ([]string, error) {
	result := make([]string, 0, len(roots))
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		resolved, err := canonicalPath(root)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		result = append(result, resolved)
	}
	return result, nil
}

func (p GuardPolicy) normalize() (GuardPolicy, error) {
	var err error
	if strings.TrimSpace(p.ManagedRoot) != "" {
		p.ManagedRoot, err = canonicalPath(p.ManagedRoot)
		if err != nil {
			return GuardPolicy{}, fmt.Errorf("normalize managed root: %w", err)
		}
	}
	p.AllowedRoots, err = normalizeRoots(p.AllowedRoots)
	if err != nil {
		return GuardPolicy{}, fmt.Errorf("normalize allowed roots: %w", err)
	}
	p.ProtectedRoots, err = normalizeRoots(p.ProtectedRoots)
	if err != nil {
		return GuardPolicy{}, fmt.Errorf("normalize protected roots: %w", err)
	}
	return p, nil
}

func (p GuardPolicy) check(path string) (string, error) {
	resolved, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	if len(resolved) > p.maxPathLength() {
		return "", fmt.Errorf("%w: path exceeds %d bytes", ErrInvalidRequest, p.maxPathLength())
	}
	for _, protected := range p.ProtectedRoots {
		if pathContains(protected, resolved) {
			return "", fmt.Errorf("%w: %s", ErrPathProtected, resolved)
		}
	}
	if len(p.AllowedRoots) > 0 {
		allowed := false
		for _, root := range p.AllowedRoots {
			if pathContains(root, resolved) {
				allowed = true
				break
			}
		}
		if !allowed {
			return "", fmt.Errorf("%w: %s", ErrPathOutsideAllowed, resolved)
		}
	}
	return resolved, nil
}

func ensureDirectory(path string, create bool) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", ErrInvalidRequest, path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) || !create {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create directory %q: %w", path, err)
	}
	info, err = os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat created directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrInvalidRequest, path)
	}
	return nil
}
