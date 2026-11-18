package cliagent

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

const geminiVersion = "0.67.0"
const geminiNodeVersion = "22.23.1"
const geminiNodeSHA = "2e3f1286a7eb3736346ed1803e458a0ff909e2b2d5bc746144dcb76970e9b99d"

// Official npm 0.67.0, archive sha256 3f42374e88171031459ad16e5fac32b161adba330292a1822fb7aae7fb476fc2.
// Registry sha512 and sha1 and all 449 distribution files were compared.
// npm optional node_modules are unverified and explicitly unreadable in sandbox.
//
//go:embed gemini-artifacts.json
var geminiArtifacts []byte

type geminiInstall struct{ Root, Entry, Node string }

func (a *Adapter) geminiInstallation(ctx context.Context) (geminiInstall, error) {
	var out geminiInstall
	if goruntime.GOOS != "darwin" || goruntime.GOARCH != "arm64" {
		return out, fmt.Errorf("Gemini requires macOS arm64")
	}
	entry := a.Executable
	if entry == "" {
		entry = os.Getenv("OPENFOX_GEMINI_PATH")
	}
	if entry == "" {
		entry, _ = exec.LookPath("gemini")
	}
	node := os.Getenv("OPENFOX_GEMINI_NODE_PATH")
	if node == "" {
		node, _ = exec.LookPath("node")
	}
	entry, e := filepath.EvalSymlinks(entry)
	if e != nil || !filepath.IsAbs(entry) || filepath.Base(entry) != "gemini.js" || filepath.Base(filepath.Dir(entry)) != "bundle" {
		return out, fmt.Errorf("Gemini requires the installed official bundle/gemini.js path")
	}
	node, e = filepath.EvalSymlinks(node)
	if e != nil || !filepath.IsAbs(node) {
		return out, fmt.Errorf("Gemini requires the installed official Node 22.23.1 path")
	}
	out = geminiInstall{Root: filepath.Dir(filepath.Dir(entry)), Entry: entry, Node: node}
	for _, name := range []string{"/Library/Application Support/GeminiCli/settings.json", "/Library/Application Support/GeminiCli/system-defaults.json", "/Library/Application Support/GeminiCli/policies"} {
		_, e := os.Lstat(name)
		if e == nil || !os.IsNotExist(e) {
			return out, fmt.Errorf("Gemini system configuration is not supported by the isolated adapter")
		}
	}
	if h, e := geminiHashFile(ctx, node, 120<<20); e != nil || h != geminiNodeSHA {
		return out, fmt.Errorf("Gemini requires the verified official Node 22.23.1 macOS arm64 build")
	}
	var rows []struct {
		Path, SHA256 string
		Bytes        int64
	}
	if json.Unmarshal(geminiArtifacts, &rows) != nil {
		return out, fmt.Errorf("Gemini artifact manifest is unavailable")
	}
	expected := map[string]bool{}
	for _, row := range rows {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		path := filepath.Join(out.Root, row.Path)
		resolved, e := filepath.EvalSymlinks(path)
		if e != nil || resolved != path {
			return out, fmt.Errorf("Gemini package contains a missing or linked artifact")
		}
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Size() != row.Bytes {
			return out, fmt.Errorf("Gemini package artifact differs from official 0.67.0")
		}
		hash, e := geminiHashFile(ctx, path, row.Bytes)
		if e != nil || hash != row.SHA256 {
			return out, fmt.Errorf("Gemini package artifact differs from official 0.67.0")
		}
		expected[path] = true
	}
	e = filepath.WalkDir(out.Root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if path == filepath.Join(out.Root, "node_modules") {
			if !d.IsDir() || d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("Gemini optional dependency directory must not be a link")
			}
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if !expected[path] || d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("Gemini package includes an unverified artifact")
		}
		return nil
	})
	if e != nil {
		return out, fmt.Errorf("Gemini package tree is not the fixed official distribution")
	}
	return out, nil
}
func geminiHashFile(ctx context.Context, path string, max int64) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() || before.Size() > max {
		return "", fmt.Errorf("invalid artifact")
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if e = ctx.Err(); e != nil {
			return "", e
		}
		n, readErr := f.Read(buf)
		total += int64(n)
		if total > max {
			return "", fmt.Errorf("artifact grew")
		}
		if n > 0 {
			h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, e := f.Stat()
	if e != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("artifact changed")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Only the already-authorized Agent/R06 Run environment supplies model inputs.
// Neither daemon environment nor Adapter.Environment may donate cross-org keys.
func geminiModelEnvironment(_ *Adapter, request map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(request))
	for name, value := range request {
		out[name] = value
	}
	return out, nil
}

func geminiPathDisplay(root, source, value string) string {
	if source == "" {
		source = "[empty workspace]"
	}
	return strings.ReplaceAll(value, root, source)
}
