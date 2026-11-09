package cliagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"openfox/internal/artifacts"
	"openfox/internal/runtime"
)

const maxSessionSearchEntries = 100_000

var errSessionNotFound = errors.New("runtime session file not found")

// ExportSession returns exactly one provider-native session file. Credentials,
// user settings, and unrelated sessions are never included.
func (a *Adapter) ExportSession(ctx context.Context, sessionID string) (runtime.SessionFile, error) {
	if a == nil || !validNativeSessionID(sessionID) {
		return runtime.SessionFile{}, runtime.ErrInvalidRequest
	}
	switch a.Name() {
	case NameOpenCode:
		return a.exportOpenCodeSession(ctx, sessionID)
	case NameClaudeCode:
		base := a.baseEnv("")
		root := strings.TrimSpace(base["CLAUDE_CONFIG_DIR"])
		if root == "" {
			root = filepath.Join(base["HOME"], ".claude")
		}
		file, err := findNativeSessionFile(ctx, filepath.Join(root, "projects"), func(name string) bool {
			return name == sessionID+".jsonl"
		})
		if err != nil {
			return runtime.SessionFile{}, err
		}
		file.Name = "projects/" + file.Name
		return file, nil
	case NamePi:
		base := a.baseEnv("")
		agentDir := strings.TrimSpace(base["PI_CODING_AGENT_DIR"])
		if agentDir == "" {
			agentDir = filepath.Join(base["HOME"], ".pi", "agent")
		}
		file, err := findNativeSessionFile(ctx, filepath.Join(agentDir, "sessions"), func(name string) bool {
			return strings.HasSuffix(name, "_"+sessionID+".jsonl") || name == sessionID+".jsonl"
		})
		if err != nil {
			return runtime.SessionFile{}, err
		}
		file.Name = "sessions/" + file.Name
		return file, nil
	default:
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
}

func (a *Adapter) exportOpenCodeSession(ctx context.Context, sessionID string) (runtime.SessionFile, error) {
	path, _, err := a.locate(ctxOrBackground(ctx))
	if err != nil {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	storeDir, err := a.sessionStoreDir()
	if err != nil {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	env := a.baseEnv(path)
	env["OPENCODE_DB"] = filepath.Join(storeDir, "opencode.db")
	command := exec.CommandContext(ctxOrBackground(ctx), path, "session", "export", sessionID)
	command.Env = environmentSlice(env)
	output := &boundedBuffer{limit: int(artifacts.MaxArtifactBytes)}
	command.Stdout = output
	command.Stderr = ioDiscard{}
	if err := command.Run(); err != nil || output.exceeded || !json.Valid(output.Bytes()) {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	return runtime.SessionFile{Name: "opencode-session.json", Data: append([]byte(nil), output.Bytes()...)}, nil
}

func validNativeSessionID(id string) bool {
	if len(id) == 0 || len(id) > 256 || strings.TrimSpace(id) != id || strings.HasPrefix(id, "-") || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		if r < 0x21 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func findNativeSessionFile(ctx context.Context, root string, match func(string) bool) (runtime.SessionFile, error) {
	if strings.TrimSpace(root) == "" {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	var found string
	entries := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if entries > maxSessionSearchEntries {
			return errors.New("runtime session search limit exceeded")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && entry.Type().IsRegular() && match(entry.Name()) {
			if found != "" {
				return errors.New("multiple matching runtime session files")
			}
			found = path
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return runtime.SessionFile{}, err
		}
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	if found == "" {
		return runtime.SessionFile{}, errSessionNotFound
	}
	info, err := os.Stat(found)
	if err != nil || !info.Mode().IsRegular() {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	if info.Size() < 1 || info.Size() > artifacts.MaxArtifactBytes {
		return runtime.SessionFile{}, artifacts.ErrTooLarge
	}
	data, err := os.ReadFile(found)
	if err != nil {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	relative, err := filepath.Rel(root, found)
	if err != nil || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return runtime.SessionFile{}, runtime.ErrUnavailable
	}
	return runtime.SessionFile{Name: filepath.ToSlash(relative), Data: data}, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	if len(value) > b.limit-b.Len() {
		b.exceeded = true
		return 0, fmt.Errorf("runtime session exceeds %d bytes", b.limit)
	}
	return b.Buffer.Write(value)
}

type ioDiscard struct{}

func (ioDiscard) Write(value []byte) (int, error) { return len(value), nil }
