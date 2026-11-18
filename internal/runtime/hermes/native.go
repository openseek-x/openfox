package hermes

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"openfox/internal/runtime"
	"openfox/internal/runtimepolicy"
)

// bootstrap is OpenFox-owned code, passed in argv rather than loaded from the
// native-writable Run profile. Upstream source files remain hash-identical.
//
//go:embed bootstrap.py
var bootstrapSource string

//go:embed snapshot.py
var snapshotSource string

var bootstrap = snapshotSource + "\n" + bootstrapSource

// Go's MkdirTemp allocation uses this private numeric Run namespace. Keep it
// unavailable as model input even when a registered directory grant is broad.
// Installation names such as openfox-hermes-install are not Run directories.
func privateNativePath(path string) bool {
	for _, part := range strings.Split(filepath.Clean(path), string(os.PathSeparator)) {
		separator := strings.LastIndex(part, "-")
		if separator < 0 || !strings.EqualFold(part[:separator], "openfox-hermes") {
			continue
		}
		suffix := part[separator+1:]
		if suffix == "" {
			continue
		}
		digits := true
		for _, c := range suffix {
			if c < '0' || c > '9' {
				digits = false
				break
			}
		}
		if digits {
			return true
		}
	}
	return false
}

func workspaceContainsRunRoot(workspace, root string) (bool, error) {
	work, err := os.Stat(workspace)
	if err != nil {
		return false, err
	}
	// SameFile recognizes case aliases on supported case-insensitive macOS
	// volumes without treating every /tmp project as a protected directory.
	for parent := root; ; parent = filepath.Dir(parent) {
		info, err := os.Stat(parent)
		if err != nil {
			return false, err
		}
		if os.SameFile(work, info) {
			return true, nil
		}
		if parent == filepath.Dir(parent) {
			return false, nil
		}
	}
}

type nativeHome struct {
	install                     installation
	root, profile, cwd, sandbox string
	env                         []string
	gateway                     *exec.Cmd
	gatewayDone                 chan struct{}
	gatewayLog                  boundedLog
	proxyPort                   string
	workspace                   *os.File
	parentRead, parentWrite     *os.File
	closeOnce                   sync.Once
}

func (a *Adapter) prepare(install installation, cwd string, proxy *modelProxy) (*nativeHome, error) {
	if a.StateRoot != "" {
		if err := os.MkdirAll(a.StateRoot, 0700); err != nil {
			return nil, err
		}
	}
	parent := a.StateRoot
	// The pinned gateway uses HOME/state/gateway.control.sock and otherwise
	// falls back to a shared /tmp name. Keep its actual owned HOME short enough;
	// do not authorize that fallback. All files still belong to this Run only.
	if parent == "" || len(parent)+len("/openfox-hermes-1234567890/home/profile/state/gateway.control.sock") > 100 {
		parent = "/private/tmp"
	}
	root, err := os.MkdirTemp(parent, "openfox-hermes-")
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if !privateNativePath(filepath.Base(root)) {
		_ = os.Remove(root)
		return nil, fmt.Errorf("Hermes temporary Run namespace is not recognized")
	}
	if cwd != "" {
		contains, checkErr := workspaceContainsRunRoot(cwd, root)
		if checkErr != nil || contains || privateNativePath(cwd) {
			_ = os.Remove(root)
			return nil, fmt.Errorf("%w: Hermes workspace overlaps private Run state or cannot be verified", runtime.ErrInvalidRequest)
		}
	}
	home := &nativeHome{install: install, root: root, profile: filepath.Join(root, "home", "profile"), cwd: cwd, sandbox: filepath.Join(root, "native.sb")}
	for _, dir := range []string{"home/profile", "runtime", "cache", "tmp", "pycache"} {
		if err = os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			_ = os.RemoveAll(root)
			return nil, err
		}
	}
	if cwd == "" {
		home.cwd = filepath.Join(root, "work")
		_ = os.Mkdir(home.cwd, 0700)
	}
	home.workspace, err = openWorkspace(home.cwd)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("Hermes authorized workspace could not be pinned: %w", err)
	}
	home.parentRead, home.parentWrite, err = os.Pipe()
	if err != nil {
		_ = home.workspace.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	env := map[string]string{"HOME": filepath.Join(root, "home"), "HERMES_HOME": home.profile, "HERMES_RUNTIME_DIR": filepath.Join(root, "runtime"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "UV_CACHE_DIR": filepath.Join(root, "cache"), "TMPDIR": filepath.Join(root, "tmp"), "PATH": filepath.Join(install.Venv, "bin") + ":/usr/bin:/bin", "LANG": "en_US.UTF-8", "LC_ALL": "en_US.UTF-8", "HERMES_DISABLE_LAZY_INSTALLS": "1", "PYTHONNOUSERSITE": "1", "PYTHONDONTWRITEBYTECODE": "1", "PYTHONPYCACHEPREFIX": filepath.Join(root, "pycache"), "UV_PYTHON_DOWNLOADS": "never"}
	for key, value := range env {
		home.env = append(home.env, key+"="+value)
	}
	if proxy != nil {
		u, _ := url.Parse(proxy.url)
		home.proxyPort = u.Port()
	}
	if err = home.writeSandbox(""); err != nil {
		home.close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	return home, nil
}

func (h *nativeHome) writeConfig(cfg runtimepolicy.HermesModelConfig, proxy *modelProxy) error {
	config := map[string]any{
		"model":             map[string]any{"default": cfg.Model, "provider": "custom", "base_url": proxy.url, "api_key": proxy.token, "stream": true, "context_length": cfg.ContextLength},
		"platform_toolsets": map[string]any{"acp": []string{"openfox_read_search"}}, "plugins": map[string]any{"enabled": []string{}}, "mcp_servers": map[string]any{},
		"gateway":  map[string]bool{"managed_workers": false},
		"security": map[string]any{"allow_lazy_installs": false}, "hooks": map[string]any{}, "hooks_auto_accept": false,
		"auth": map[string]any{"adopt_external_logins": false}, "telemetry": map[string]any{"shared_metrics": map[string]bool{"enabled": false, "send": false}},
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(h.profile, "config.yaml"), data, 0600)
}

func (h *nativeHome) writeSandbox(gatewayPort string) error {
	quote := strconv.Quote
	var b strings.Builder
	b.WriteString("(version 1)\n(deny default)\n(allow process-fork)\n(allow sysctl-read)\n(allow signal (target children))\n")
	binary, err := filepath.EvalSymlinks(h.install.Python)
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "(allow process-exec (literal %s))\n", quote(binary))
	fmt.Fprintf(&b, "(allow process-exec (literal %s))\n", quote(h.install.RG))
	b.WriteString("(allow file-read* (subpath \"/usr\") (subpath \"/System\") (subpath \"/Library/Apple\") (literal \"/dev/null\") (literal \"/dev/urandom\") (literal \"/dev/random\"))\n")
	// LocalEnvironment checks that the OS shell exists even when the native
	// read/search path uses Python only. Metadata is not an execution grant.
	b.WriteString("(allow file-read* (literal \"/bin\") (literal \"/bin/bash\") (literal \"/bin/sh\"))\n")
	for _, path := range []string{h.install.Source, h.install.Base, h.install.Venv, h.install.RGRoot, h.root, h.cwd} {
		fmt.Fprintf(&b, "(allow file-read* (subpath %s))\n", quote(path))
	}
	fmt.Fprintf(&b, "(allow file-write* (subpath %s) (literal \"/dev/null\"))\n", quote(h.root))
	// Metadata for ancestors permits path traversal, not listing/reading HOME.
	for _, path := range []string{h.install.Source, h.install.Base, h.install.Venv, h.install.RGRoot, h.root, h.cwd} {
		for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
			fmt.Fprintf(&b, "(allow file-read-metadata (literal %s))\n", quote(parent))
			if parent == "/" {
				break
			}
		}
	}
	b.WriteString("(allow file-read* (literal \"/\") (literal \"/private\") (literal \"/private/tmp\") (literal \"/dev\"))\n")
	// Resolve the standard macOS aliases without granting either alias tree.
	// Actual file reads still have to match the canonical workspace/install root.
	b.WriteString("(allow file-read-metadata (literal \"/tmp\") (literal \"/var\"))\n")
	indexes, err := filepath.Glob(filepath.Join(h.install.Site, "*.dist-info", "RECORD"))
	if err != nil {
		return err
	}
	for _, index := range indexes {
		fmt.Fprintf(&b, "(deny file-read* (literal %s))\n", quote(index))
	}
	b.WriteString("(allow network-inbound (local ip \"localhost:*\"))\n")
	for _, port := range []string{h.proxyPort, gatewayPort} {
		if port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("invalid owned Hermes port")
			}
			fmt.Fprintf(&b, "(allow network-outbound (remote ip \"localhost:%d\"))\n", n)
		}
	}
	fmt.Fprintf(&b, "(allow network-outbound (remote unix-socket (path-prefix %s)))\n(allow network-inbound (local unix-socket (path-prefix %s)))\n", quote(h.root+"/"), quote(h.root+"/"))
	// Exact policy exclusions override the workspace/root grants. Disallow all
	// pre-existing bytecode so modified caches cannot bypass source hashes.
	b.WriteString("(deny file-read* (regex #\"(^|/)(__pycache__|[.]hermes|[.]ssh|[.]aws)(/|$)\") (regex #\"(^|/)[.]env([^/]*|/.*)$\"))\n")
	for _, path := range []string{filepath.Join(h.install.Source, ".git"), filepath.Join(h.profile, "plugins"), filepath.Join(h.profile, "hooks"), filepath.Join(h.profile, "skills")} {
		fmt.Fprintf(&b, "(deny file-read* (subpath %s))\n", quote(path))
	}
	return os.WriteFile(h.sandbox, []byte(b.String()), 0600)
}

func (h *nativeHome) args(args ...string) []string {
	if len(args) > 0 && (args[0] == "acp" || len(args) > 1 && args[0] == "gateway" && args[1] == "run") {
		options, _ := json.Marshal(map[string]any{"cwd": h.cwd, "rg": h.install.RG, "workspace_fd": 3, "parent_fd": 4, "run_root": h.root})
		return append([]string{"-f", h.sandbox, h.install.Python, "-c", bootstrap, string(options), "--"}, args...)
	}
	return append([]string{"-f", h.sandbox, h.install.Python, "-m", "hermes_cli.main"}, args...)
}
func (h *nativeHome) commandOutput(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", h.args(args...)...)
	cmd.Dir = h.install.Source
	cmd.Env = h.env
	cmd.ExtraFiles = h.childFiles()
	var output boundedLog
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return output.Bytes(), err
}

func (h *nativeHome) startGateway(ctx context.Context) error {
	cmd := exec.Command("/usr/bin/sandbox-exec", h.args("gateway", "run", "--external-supervisor", "-v")...)
	cmd.Dir, cmd.Env = h.install.Source, h.env
	cmd.ExtraFiles = h.childFiles()
	cmd.Stdout, cmd.Stderr = &h.gatewayLog, &h.gatewayLog
	configureGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Hermes foreground gateway failed to start")
	}
	h.gateway, h.gatewayDone = cmd, make(chan struct{})
	go func() { _ = cmd.Wait(); close(h.gatewayDone) }()
	readyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("Hermes foreground gateway readiness timed out")
		case <-h.gatewayDone:
			return fmt.Errorf("Hermes foreground gateway exited before readiness")
		case <-ticker.C:
		}
		if bytes.Contains(h.gatewayLog.Bytes(), []byte("INFO gateway.run: Press Ctrl+C to stop")) {
			break
		}
	}
	// The official attach command only attaches; it neither installs a service nor
	// starts an external daemon. Bind its receipt to our live child and private HOME.
	data, err := os.ReadFile(filepath.Join(h.profile, "gateway_state.json"))
	if err != nil {
		return fmt.Errorf("Hermes gateway ownership state is missing")
	}
	var state struct {
		PID   int    `json:"pid"`
		Home  string `json:"hermes_home"`
		State string `json:"gateway_state"`
	}
	if len(data) > 64<<10 || json.Unmarshal(data, &state) != nil || state.PID != cmd.Process.Pid || state.Home != h.profile || state.State != "running" {
		return fmt.Errorf("Hermes gateway ownership state is invalid")
	}
	attachCtx, stop := context.WithTimeout(readyCtx, 8*time.Second)
	defer stop()
	data, err = h.commandOutput(attachCtx, "gateway", "attach", "--json", "--timeout", "5")
	if err != nil {
		return fmt.Errorf("Hermes gateway attach failed")
	}
	var receipt struct {
		State, Protocol, URL string
		Endpoint             struct {
			Profile    string `json:"profile_id"`
			Supervisor string
			Instance   string `json:"instance_id"`
			Protocol   int    `json:"runtime_protocol"`
		}
	}
	if json.Unmarshal(data, &receipt) != nil || receipt.State != "ready" || receipt.Protocol != "hermes-gateway-v1" || receipt.Endpoint.Profile != h.profile || receipt.Endpoint.Supervisor != "external" || receipt.Endpoint.Instance == "" || receipt.Endpoint.Protocol != 1 {
		return fmt.Errorf("Hermes gateway attachment does not match this Run")
	}
	u, err := url.Parse(receipt.URL)
	if err != nil || u.Scheme != "ws" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Path != "/api/ws" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("Hermes gateway returned an unexpected local endpoint")
	}
	select {
	case <-h.gatewayDone:
		return fmt.Errorf("Hermes gateway exited during attachment")
	default:
	}
	return h.writeSandbox(u.Port())
}

func (h *nativeHome) close() {
	h.closeOnce.Do(func() {
		if h.parentWrite != nil {
			_ = h.parentWrite.Close()
		}
		if h.gateway != nil {
			stopGroup(h.gateway, h.gatewayDone)
		}
		if h.workspace != nil {
			_ = h.workspace.Close()
		}
		if h.parentRead != nil {
			_ = h.parentRead.Close()
		}
	})
}

func (h *nativeHome) childFiles() []*os.File { return []*os.File{h.workspace, h.parentRead} }

// Native startup diagnostics stay inside memory and are never copied to API
// errors. Keep the tail for the readiness marker without unbounded log growth.
type boundedLog struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 64<<10 {
		b.data = append([]byte(nil), b.data[len(b.data)-(64<<10):]...)
	}
	return n, nil
}
func (b *boundedLog) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}
