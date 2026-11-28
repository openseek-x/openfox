package cliagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

type geminiHome struct {
	Root, Work, Home, Tmp, Policy, Sandbox string
	Env                                    map[string]string
}

func geminiPrepareHome(root string, install geminiInstall, port string) (geminiHome, error) {
	h := geminiHome{Root: root, Work: filepath.Join(root, "work"), Home: filepath.Join(root, "home"), Tmp: filepath.Join(root, "tmp"), Policy: filepath.Join(root, "policy.toml"), Sandbox: filepath.Join(root, "native.sb")}
	for _, p := range []string{h.Home, filepath.Join(h.Home, ".gemini"), h.Tmp} {
		if e := os.MkdirAll(p, 0700); e != nil {
			return h, e
		}
	}
	settings := map[string]any{
		"security":    map[string]any{"auth": map[string]any{"selectedType": "gemini-api-key"}, "folderTrust": map[string]any{"enabled": false}},
		"tools":       map[string]any{"core": []string{"read_file", "list_directory", "glob", "grep_search"}, "useRipgrep": false},
		"hooksConfig": map[string]any{"enabled": false}, "skills": map[string]any{"enabled": false},
		"admin":        map[string]any{"extensions": map[string]any{"enabled": false}, "mcp": map[string]any{"enabled": false}},
		"experimental": map[string]any{"enableAgents": false, "autoMemory": false},
		"general":      map[string]any{"disableAutoUpdate": true, "enableAutoUpdateNotification": false, "checkpointing": map[string]any{"enabled": false}},
		"advanced":     map[string]any{"ignoreLocalEnv": true, "autoConfigureMemory": false},
		"context":      map[string]any{"includeDirectories": []string{}, "includeDirectoryTree": false, "memoryBoundaryMarkers": []string{}, "loadMemoryFromIncludeDirectories": false},
		"telemetry":    map[string]any{"enabled": false}, "privacy": map[string]any{"usageStatisticsEnabled": false}, "mcpServers": map[string]any{},
	}
	data, _ := json.Marshal(settings)
	if e := os.WriteFile(filepath.Join(h.Home, ".gemini", "settings.json"), data, 0600); e != nil {
		return h, e
	}
	if e := os.WriteFile(h.Policy, []byte("[[rule]]\ntoolName=\"*\"\ndecision=\"deny\"\npriority=998\n\n[[rule]]\ntoolName=[\"read_file\",\"list_directory\",\"glob\",\"grep_search\"]\ndecision=\"allow\"\npriority=999\n"), 0600); e != nil {
		return h, e
	}
	h.Env = map[string]string{"HOME": h.Home, "GEMINI_CLI_HOME": h.Home, "TMPDIR": h.Tmp, "PATH": "/usr/bin:/bin", "GEMINI_CLI_NO_RELAUNCH": "1", "GEMINI_CLI_TRUST_WORKSPACE": "true", "GEMINI_FORCE_FILE_STORAGE": "true", "GEMINI_TELEMETRY_ENABLED": "false", "CI": "1", "NO_COLOR": "1", "TERM": "xterm-256color"}
	var sb strings.Builder
	sb.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow sysctl-read)\n(allow signal (target children))\n")
	fmt.Fprintf(&sb, "(allow process-exec (literal %s))\n", strconv.Quote(install.Node))
	sb.WriteString("(allow file-read* (subpath \"/usr\") (subpath \"/System\") (subpath \"/Library/Apple\") (literal \"/dev/null\") (literal \"/dev/urandom\") (literal \"/dev/random\"))\n")
	sb.WriteString("(allow file-read* (literal \"/\") (literal \"/private\") (literal \"/private/tmp\") (literal \"/dev\"))\n")
	fmt.Fprintf(&sb, "(allow file-read* (literal %s) (subpath %s) (subpath %s))\n", strconv.Quote(install.Node), strconv.Quote(install.Root), strconv.Quote(root))
	fmt.Fprintf(&sb, "(deny file-read* (subpath %s))\n", strconv.Quote(filepath.Join(install.Root, "node_modules")))
	fmt.Fprintf(&sb, "(allow file-write* (subpath %s) (subpath %s) (literal \"/dev/null\"))\n", strconv.Quote(h.Home), strconv.Quote(h.Tmp))
	for _, p := range []string{install.Node, install.Root, root} {
		for p = filepath.Dir(p); ; p = filepath.Dir(p) {
			fmt.Fprintf(&sb, "(allow file-read-metadata (literal %s))\n", strconv.Quote(p))
			if p == "/" {
				break
			}
		}
	}
	sb.WriteString("(allow file-read-metadata (literal \"/tmp\") (literal \"/var\"))\n")
	if port != "" {
		if _, e := strconv.ParseUint(port, 10, 16); e != nil {
			return h, e
		}
		fmt.Fprintf(&sb, "(allow network-outbound (remote ip \"localhost:%s\"))\n", port)
	}
	if e := os.WriteFile(h.Sandbox, []byte(sb.String()), 0600); e != nil {
		return h, e
	}
	return h, nil
}
func (a *Adapter) detectGemini(ctx context.Context) (runtime.Capabilities, error) {
	ctx, cancel := context.WithTimeout(ctxOrBackground(ctx), a.probeTimeout())
	defer cancel()
	c := runtime.Capabilities{Runtime: NameGemini, Login: runtime.LoginUnknown, SupportsJSONEvents: true, SupportsCancel: true, SupportsResume: false, DirectoryModes: []string{"NONE", "WORKSPACE", "EXPLICIT"}}
	install, e := a.geminiInstallation(ctx)
	if e != nil {
		c.Executable = install.Candidate.Path
		c.Reason = e.Error()
		return c, fmt.Errorf("%w: %s", runtime.ErrUnavailable, e)
	}
	c.Executable = install.Candidate.Path
	c.DiscoverySource = install.Candidate.Source
	root, e := a.makeStateDir()
	if e != nil {
		return c, e
	}
	defer os.RemoveAll(root)
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return c, e
	}
	h, e := geminiPrepareHome(root, install, "")
	if e != nil {
		return c, e
	}
	if e = os.Mkdir(h.Work, 0700); e != nil {
		return c, e
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-f", h.Sandbox, install.Node, install.Entry, "--version")
	cmd.Dir = h.Work
	cmd.Env = environmentSlice(h.Env)
	var out boundedGeminiBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if e = cmd.Run(); e != nil || strings.TrimSpace(out.String()) != geminiVersion {
		c.Reason = "Gemini isolated version probe failed"
		return c, fmt.Errorf("%w: %s", runtime.ErrUnavailable, c.Reason)
	}
	c.Version = geminiVersion
	c.ProtocolReady = true
	c.RequiresRunConfiguration = true
	c.Reason = "Gemini protocol ready; each Run requires an explicit Google Gemini API key and model, without attachments or native extensions"
	return c, nil
}

type boundedGeminiBuffer struct{ strings.Builder }

func (b *boundedGeminiBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 8192 {
		return 0, fmt.Errorf("Gemini probe output exceeds limit")
	}
	return b.Builder.Write(p)
}

// The trusted Node preload only observes a host-owned lifetime pipe and a total
// deadline. It neither loads workspace code nor grants any native tool authority.
func geminiLifetimeImport(timeout time.Duration) string {
	ms := timeout.Milliseconds() + 2000
	if ms < 2000 {
		ms = 2000
	}
	code := `import{Socket}from'node:net';const s=new Socket({fd:3,readable:true,writable:false});s.on('end',()=>process.exit(143));s.on('error',()=>process.exit(143));s.unref();s.resume();setTimeout(()=>process.exit(124),` + strconv.FormatInt(ms, 10) + `).unref();`
	return "data:text/javascript," + url.PathEscape(code)
}
func validateGeminiRequest(a *Adapter, req runtime.RunRequest) error {
	if e := runtimepolicy.ValidateGeminiModes(req.ApprovalMode, req.CollaborationMode, req.ReasoningEffort, req.Args); e != nil {
		return fmt.Errorf("%w: %s", runtime.ErrUnsupported, e)
	}
	if len(req.MCPServers) > 0 || len(req.Skills) > 0 || len(req.Tools) > 0 || req.ToolCall != nil || req.ResumeSession != "" {
		return fmt.Errorf("%w: Gemini supports no MCP, skills, task bridge, extensions or native resume", runtime.ErrUnsupported)
	}
	env, e := geminiModelEnvironment(a, req.Environment)
	if e != nil {
		return e
	}
	_, e = runtimepolicy.ResolveGeminiModel(req.Model, env)
	if e != nil {
		return fmt.Errorf("%w: %s", runtime.ErrInvalidRequest, e)
	}
	return nil
}
