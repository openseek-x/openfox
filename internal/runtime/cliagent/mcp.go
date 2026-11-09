package cliagent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"openfox/internal/runtime/mcpconfig"
)

func readProviderFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > mcpconfig.MaxConfigBytes {
		return nil, mcpconfig.ErrUnavailable
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, mcpconfig.ErrUnavailable
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, mcpconfig.MaxConfigBytes+1))
	if err != nil || len(b) > mcpconfig.MaxConfigBytes {
		return nil, mcpconfig.ErrUnavailable
	}
	return b, nil
}

func copyProviderFile(src, dst string) error {
	b, err := readProviderFile(src)
	if err != nil || b == nil {
		return err
	}
	return os.WriteFile(dst, b, 0600)
}

// OpenCode "wellknown" auth providers can fetch arbitrary remote config. Only
// copy model provider credentials into the isolated data directory.
func isolateOpenCodeAuth(base map[string]string, root string, env map[string]string) error {
	source := base["XDG_DATA_HOME"]
	if source == "" {
		source = filepath.Join(base["HOME"], ".local", "share")
	}
	data := filepath.Join(root, "data")
	env["XDG_DATA_HOME"] = data
	env["OPENCODE_AUTH_CONTENT"] = ""
	content := []byte(base["OPENCODE_AUTH_CONTENT"])
	if len(content) == 0 {
		b, err := readProviderFile(filepath.Join(source, "opencode", "auth.json"))
		if err != nil {
			return err
		}
		content = b
	}
	if len(content) == 0 {
		return nil
	}
	var providers map[string]json.RawMessage
	if len(content) > mcpconfig.MaxConfigBytes || json.Unmarshal(content, &providers) != nil {
		return mcpconfig.ErrUnavailable
	}
	for name, raw := range providers {
		var entry struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &entry) != nil {
			return mcpconfig.ErrUnavailable
		}
		if entry.Type != "api" && entry.Type != "oauth" {
			delete(providers, name)
		}
	}
	b, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	dir := filepath.Join(data, "opencode")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "auth.json"), b, 0600)
}

func (a *Adapter) sessionStoreDir() (string, error) {
	root := a.StateRoot
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(cache, "OpenFox")
	}
	dir := filepath.Join(root, a.Name()+"-sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
