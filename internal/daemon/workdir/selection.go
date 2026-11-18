package workdir

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// RegisteredDirectory is resolved locally. Path and Root must never leave the execution device.
type RegisteredDirectory struct {
	Name string
	Path string
	Root string
}

func validLogicalDirectory(name string) bool {
	return name != "" && len(name) <= 1024 && path.Clean(name) == name && name != "." && name != ".." &&
		!strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.HasPrefix(name, "~") &&
		!strings.ContainsAny(name, "\\:") && strings.IndexFunc(name, unicode.IsControl) < 0
}

// ResolveDirectory requires both the device registration and the current Agent's allowlist.
// Match names at component boundaries. A nested registration shadows its parent.
func ResolveDirectory(name string, registered map[string]string, allowed []string) (RegisteredDirectory, error) {
	if !validLogicalDirectory(name) {
		return RegisteredDirectory{}, ErrPathOutsideAllowed
	}
	var key string
	for candidate := range registered {
		if len(candidate) > len(key) && (name == candidate || strings.HasPrefix(name, candidate+"/")) {
			key = candidate
		}
	}
	for _, permitted := range allowed {
		if key != "" && permitted == key {
			root := registered[key]
			real, err := filepath.EvalSymlinks(root)
			if err != nil || real != root || !filepath.IsAbs(root) {
				return RegisteredDirectory{}, ErrBindingUnavailable
			}
			suffix := strings.TrimPrefix(strings.TrimPrefix(name, key), "/")
			selected, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(suffix)))
			if err != nil {
				return RegisteredDirectory{}, ErrBindingUnavailable
			}
			if !pathContains(root, selected) {
				return RegisteredDirectory{}, ErrPathOutsideAllowed
			}
			if info, err := os.Stat(selected); err != nil || !info.IsDir() {
				return RegisteredDirectory{}, ErrBindingUnavailable
			}
			return RegisteredDirectory{Name: name, Path: selected, Root: root}, nil
		}
	}
	return RegisteredDirectory{}, ErrPathOutsideAllowed
}

// LocalDirectory maps a path from the native picker to the logical name accepted by the daemon.
func LocalDirectory(selected string, registered map[string]string, allowed []string) (RegisteredDirectory, error) {
	if !filepath.IsAbs(selected) {
		return ResolveDirectory(selected, registered, allowed)
	}
	resolved, err := filepath.EvalSymlinks(selected)
	if err != nil {
		return RegisteredDirectory{}, ErrBindingUnavailable
	}
	var names []string
	for _, key := range allowed {
		if root := registered[key]; root != "" && pathContains(root, resolved) {
			names = append(names, key)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if len(registered[names[i]]) != len(registered[names[j]]) {
			return len(registered[names[i]]) > len(registered[names[j]])
		}
		return names[i] < names[j]
	})
	for _, key := range names {
		suffix, err := filepath.Rel(registered[key], resolved)
		if err == nil {
			directory, err := ResolveDirectory(path.Join(key, filepath.ToSlash(suffix)), registered, allowed)
			if err == nil && directory.Path == resolved {
				return directory, nil
			}
		}
	}
	return RegisteredDirectory{}, ErrPathOutsideAllowed
}

// A worktree exposes the whole repository. Never discover a Git root above a selected
// subdirectory or follow a linked .git directory outside the registered root.
func (d RegisteredDirectory) IsGitRoot(ctx context.Context) bool {
	git := execGitRunner{}
	root, err := resolveGitRoot(ctx, git, d.Path)
	if err != nil || root != d.Path {
		return false
	}
	result, err := git.Run(ctx, d.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return false
	}
	common, err := filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	return err == nil && pathContains(d.Root, common)
}

// ReopenSelection acquires the same lease as ordinary runs, including across source and
// target conversations. Managed worktrees must still belong to the original repository.
func (m *Manager) ReopenSelection(ctx context.Context, key BindingKey, b Binding, directory RegisteredDirectory) (*PreparedWorkspace, error) {
	m.prepareMu.Lock()
	defer m.prepareMu.Unlock()
	real, err := filepath.EvalSymlinks(b.ActualPath)
	if err != nil || real != b.ActualPath {
		return nil, ErrBindingUnavailable
	}
	if b.ConfiguredPath != directory.Path {
		return nil, ErrPathOutsideAllowed
	}
	if b.Owned {
		root := m.ManagedRoot()
		if root == "" || filepath.Dir(b.ActualPath) != root || b.RepositoryRoot != directory.Path || !directory.IsGitRoot(ctx) {
			return nil, ErrBindingUnavailable
		}
		if !strings.HasPrefix(filepath.Base(b.ActualPath), ManagedWorktreePrefix) && !strings.HasPrefix(filepath.Base(b.ActualPath), "shared-") {
			return nil, ErrBindingUnavailable
		}
		origin, err := m.git.Run(ctx, directory.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
		checkout, checkErr := m.git.Run(ctx, b.ActualPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || checkErr != nil || strings.TrimSpace(origin.Stdout) != strings.TrimSpace(checkout.Stdout) {
			return nil, ErrBindingUnavailable
		}
	} else if b.ActualPath != directory.Path || b.Mode != ModeDirect {
		return nil, ErrPathOutsideAllowed
	}
	b.Key = key
	return m.reopen(ctx, b)
}

func localBase(ctx context.Context, git GitRunner, repo, branch string) (string, error) {
	ref := "HEAD"
	if branch != "" {
		if len(branch) > 255 || !gitRefValid(branch) || strings.HasPrefix(branch, "+") || branch == "@" {
			return "", ErrInvalidRequest
		}
		if _, err := hex.DecodeString(branch); (len(branch) == 40 || len(branch) == 64) && err == nil {
			ref = branch
		} else {
			if strings.HasPrefix(branch, "refs/") && !strings.HasPrefix(branch, "refs/heads/") {
				return "", ErrInvalidRequest
			}
			ref = "refs/heads/" + strings.TrimPrefix(branch, "refs/heads/")
			if _, err := git.Run(ctx, repo, "check-ref-format", ref); err != nil {
				return "", ErrInvalidRequest
			}
		}
	}

	result, err := git.Run(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", errors.Join(ErrRepositoryUnavailable, err)
	}
	return strings.TrimSpace(result.Stdout), nil
}
