// Package qqconnector invokes only a separately provisioned fixed official SDK.
// It never implements the SDK's internal QR protocol or inherits a user login.
package qqconnector

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"openfox/internal/integrations/qq"
)

//go:embed provenance.json
var provenance []byte

//go:embed entry.mjs
var entry []byte

var ErrUnverified = errors.New("qq connector: fixed deployment not verified")
var ErrUnknown = errors.New("qq connector: outcome unknown; do not automatically restart")

type Config struct{ NodePath, Root string }
type Event struct {
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	AppID     string `json:"app_id,omitempty"`
	AppSecret string `json:"app_secret,omitempty"`
}

func (Event) String() string   { return "qqconnector.Event([redacted])" }
func (Event) GoString() string { return "qqconnector.Event([redacted])" }

func hashFile(name string) (string, error) {
	f, e := os.Open(name)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (c Config) Validate() error {
	if !filepath.IsAbs(c.NodePath) || !filepath.IsAbs(c.Root) {
		return ErrUnverified
	}
	var expected struct {
		Node  map[string]string `json:"node_sha256"`
		Files map[string]string `json:"files"`
	}
	if json.Unmarshal(provenance, &expected) != nil {
		return ErrUnverified
	}
	want, ok := expected.Node[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return ErrUnverified
	}
	node, e := filepath.EvalSymlinks(c.NodePath)
	if e != nil {
		return ErrUnverified
	}
	info, e := os.Stat(node)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Mode().Perm()&0022 != 0 {
		return ErrUnverified
	}
	got, e := hashFile(node)
	if e != nil || got != want {
		return ErrUnverified
	}
	root, e := filepath.EvalSymlinks(c.Root)
	if e != nil {
		return ErrUnverified
	}
	seen := map[string]bool{}
	e = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return ErrUnverified
		}
		if d.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return ErrUnverified
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		rel = filepath.ToSlash(rel)
		want, ok := expected.Files[rel]
		if !ok {
			return ErrUnverified
		}
		actual, e := hashFile(path)
		if e != nil || actual != want {
			return ErrUnverified
		}
		seen[rel] = true
		return nil
	})
	if e != nil || len(seen) != len(expected.Files) {
		return ErrUnverified
	}
	return nil
}

// Run owns exactly one Node process. Parent pipe EOF terminates its HTTP work;
// cancellation has an eight-second hard stop. Events carry secrets only in the
// final success callback, which the caller must seal durably before accepting.
func (c Config) Run(ctx context.Context, onEvent func(context.Context, Event) error) error {
	return c.run(ctx, onEvent, nil)
}

type testSetup func(home string, script []byte) ([]byte, func(*exec.Cmd), error)

func (c Config) run(ctx context.Context, onEvent func(context.Context, Event) error, setup testSetup) error {
	if e := c.Validate(); e != nil {
		return e
	}
	if onEvent == nil {
		return ErrUnverified
	}
	home, e := os.MkdirTemp("", "openfox-qq-helper-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(home)
	// Node canonicalizes import.meta.url. Keep HOME and the owned script path
	// identical even on macOS /var -> /private/var temporary-directory aliases.
	home, e = filepath.EvalSymlinks(home)
	if e != nil {
		return e
	}
	script := append([]byte(nil), entry...)
	var commandHook func(*exec.Cmd)
	if setup != nil {
		script, commandHook, e = setup(home, script)
		if e != nil {
			return e
		}
	}
	file := filepath.Join(home, "entry.mjs")
	if e = os.WriteFile(file, script, 0600); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.NodePath, "--no-addons", "--disable-proto=throw", "--max-old-space-size=64", file, c.Root)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "TMPDIR=" + home, "LANG=C.UTF-8"}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 8 * time.Second
	input, e := cmd.StdinPipe()
	if e != nil {
		return e
	}
	defer input.Close()
	output, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	cmd.Cancel = func() error { return input.Close() }
	if commandHook != nil {
		commandHook(cmd)
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	waited := false
	defer func() {
		if !waited {
			_ = input.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(io.LimitReader(output, (1<<20)+1))
	scanner.Buffer(make([]byte, 4096), 32<<10)
	success := false
	var terminal bool
	for scanner.Scan() {
		var event Event
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil {
			return ErrUnknown
		}
		if terminal {
			return ErrUnknown
		}
		switch event.Kind {
		case "qr":
			u, e := url.Parse(event.URL)
			if e != nil || u.Scheme != "https" || u.Host != "q.qq.com" || u.User != nil || u.Fragment != "" || u.Path != "/qqbot/openclaw/connect.html" || len(event.URL) > 8192 || event.AppID != "" || event.AppSecret != "" {
				return ErrUnknown
			}
		case "expired":
			if event.URL != "" || event.AppID != "" || event.AppSecret != "" {
				return ErrUnknown
			}
		case "success":
			if !(qq.Credentials{AppID: event.AppID, AppSecret: event.AppSecret}).Valid() || event.URL != "" {
				return ErrUnknown
			}
			terminal, success = true, true
		case "failure", "cancelled":
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrUnknown
		default:
			return ErrUnknown
		}
		if e = onEvent(ctx, event); e != nil {
			return e
		}
	}
	e = cmd.Wait()
	waited = true
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanner.Err() != nil || e != nil || !success {
		return ErrUnknown
	}
	return nil
}

// A configured directory is not proof of availability; validate actual fixed
// contents before execution. This helper does not install or fetch anything.
func (c Config) Configured() bool {
	return strings.TrimSpace(c.NodePath) != "" && strings.TrimSpace(c.Root) != ""
}
