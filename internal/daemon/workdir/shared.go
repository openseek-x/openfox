package workdir

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// prepareShared runs under prepareMu. Its manifest is outside the task directory;
// reopening never resets, cleans or checks out files written by a previous member.
func (m *Manager) prepareShared(ctx context.Context, req Request) (*PreparedWorkspace, error) {
	if req.Mode != ModeNoRepo && req.Mode != ModeSpecifiedGit && req.Mode != ModeTempGit {
		return nil, ErrInvalidRequest
	}
	root, err := m.managedRoot()
	if err != nil {
		return nil, err
	}
	encodedKey, err := json.Marshal(req.Key)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encodedKey)
	name := "shared-" + hex.EncodeToString(digest[:])
	path := filepath.Join(root, name)
	manifest := filepath.Join(root, "."+name+".json")
	source := ""
	if req.SourcePath != "" {
		source, err = canonicalPath(req.SourcePath)
		if err != nil {
			return nil, err
		}
	}
	var binding Binding
	data, err := os.ReadFile(manifest)
	if err == nil {
		if json.Unmarshal(data, &binding) != nil || binding.Key != req.Key || binding.ActualPath != path ||
			binding.Mode != req.Mode || binding.ConfiguredPath != source {
			return nil, ErrBindingConflict
		}
		workspace, err := m.reopen(ctx, binding)
		if err != nil {
			return nil, err
		}
		// Registry IDs are local to this daemon instance, not persistent identifiers.
		binding.ID = ""
		bound, _, err := m.bindings.Bind(binding)
		if err != nil {
			_ = workspace.Release()
			return nil, err
		}
		workspace.Binding = bound
		return workspace, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		// A directory without its manifest is not safe to adopt or overwrite.
		return nil, ErrBindingUnavailable
	}
	req.RequestedPath = path
	workspace, err := m.prepareFresh(ctx, req)
	if err != nil {
		return nil, err
	}
	binding = Binding{Key: req.Key, Mode: req.Mode, ConfiguredPath: source, ActualPath: workspace.Path,
		RepositoryRoot: workspace.RepositoryRoot, Branch: workspace.Branch, Owned: workspace.Owned, IsGit: workspace.IsGit}
	bound, _, err := m.bindings.Bind(binding)
	if err != nil {
		_ = workspace.Release()
		return nil, err
	}
	workspace.Binding = bound
	data, err = json.Marshal(bound)
	if err == nil {
		err = os.WriteFile(manifest+".tmp", data, 0o600)
	}
	if err == nil {
		err = os.Rename(manifest+".tmp", manifest)
	}
	if err != nil {
		_ = workspace.Release()
		return nil, err
	}
	return &workspace, nil
}
