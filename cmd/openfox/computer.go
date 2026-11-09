package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/devices"
)

// registerTimeout 是注册全流程（注册 + 换会话令牌）的上限。
const registerTimeout = time.Minute

// computerDeps 是 `openfox computer` 的可替换依赖（测试注入假中心与假的 launchctl）。
type computerDeps struct {
	stdin      io.Reader
	stdout     io.Writer
	httpClient *http.Client
	goos       string
	executable func() (string, error)
	manager    func() (daemon.Manager, error)
}

func defaultComputerDeps() computerDeps {
	return computerDeps{
		stdin: os.Stdin, stdout: os.Stdout, httpClient: &http.Client{Timeout: 30 * time.Second},
		goos: runtime.GOOS, executable: os.Executable, manager: daemon.DefaultManager,
	}
}

func runComputer(args []string) error {
	return runComputerWith(args, defaultComputerDeps())
}

func runComputerWith(args []string, deps computerDeps) error {
	if len(args) == 0 {
		return errors.New("usage: openfox computer <register|status|resolve-directory|workspace-path>")
	}
	switch args[0] {
	case "register":
		return runComputerRegister(args[1:], deps)
	case "status":
		return runComputerStatus(args[1:], deps)
	case "resolve-directory":
		return runResolveDirectory(args[1:], deps)
	case "workspace-path":
		return runWorkDirectoryPath(args[1:], deps)
	default:
		return fmt.Errorf("unknown computer command %q", args[0])
	}
}

// computerStatus 是 `openfox computer status --json` 的输出：只含注册是否存在与公开标识，
// 不含私钥、会话令牌与文件内容，供桌面端判断是否需要自动注册。
type computerStatus struct {
	Registered     bool   `json:"registered"`
	OrganizationID string `json:"organization_id,omitempty"`
	DeviceID       string `json:"device_id,omitempty"`
	CenterURL      string `json:"center_url,omitempty"`
	// Reason 只在未注册时出现：not_registered = 没有 config.json；invalid_config = 文件存在但无法读取或格式不对
	Reason string `json:"reason,omitempty"`
}

// runComputerStatus 读取配置目录下的 config.json，输出本机注册状态。
func runComputerStatus(args []string, deps computerDeps) error {
	flags := flag.NewFlagSet("computer status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configDir := flags.String("config-dir", "", "configuration directory (default: platform config directory)")
	asJSON := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("usage: openfox computer status [--json] [--config-dir <dir>]")
	}
	dir := strings.TrimSpace(*configDir)
	if dir == "" {
		var err error
		if dir, err = daemon.DefaultConfigDir(); err != nil {
			return err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	status := computerStatus{}
	config, err := daemon.LoadDeviceConfig(filepath.Join(dir, daemon.DeviceConfigFileName))
	switch {
	case err == nil:
		status = computerStatus{Registered: true, OrganizationID: config.OrganizationID, DeviceID: config.DeviceID, CenterURL: config.CenterURL}
	case errors.Is(err, os.ErrNotExist):
		status.Reason = "not_registered"
	default:
		status.Reason = "invalid_config"
	}
	if *asJSON {
		return json.NewEncoder(deps.stdout).Encode(status)
	}
	if !status.Registered {
		fmt.Fprintf(deps.stdout, "registered=false reason=%s\n", status.Reason)
		return nil
	}
	fmt.Fprintf(deps.stdout, "registered=true organization_id=%s device_id=%s center_url=%s\n", status.OrganizationID, status.DeviceID, status.CenterURL)
	return nil
}

// runComputerRegister 实现 `openfox computer register`：解析安装令牌 → 生成 ed25519 密钥 → 注册 →
// 换会话令牌 → 写 config.json → （macOS 且未加 --no-service）安装并启动 launchd 服务。
// 输出与错误信息中不出现安装令牌、私钥与会话令牌。
func runComputerRegister(args []string, deps computerDeps) error {
	flags := flag.NewFlagSet("computer register", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	installToken := flags.String("install-token", "", "install token from OpenFox (ofx_v2_...); use - to read it from stdin")
	name := flags.String("name", "", "computer name; currently the name is chosen when the token is issued (rename it in OpenFox)")
	workspaceRoot := flags.String("workspace-root", "", "managed workspace root (default: <config-dir>/workspaces)")
	configDir := flags.String("config-dir", "", "configuration directory (default: platform config directory)")
	noService := flags.Bool("no-service", false, "do not install or start the background service")
	force := flags.Bool("force", false, "replace an existing registration in the configuration directory")
	runtimeExecutable := flags.String("runtime-executable", "", "Codex executable or PATH name (empty: auto-discover)")
	var supportedVersions, directoryRoots stringListFlag
	flags.Var(&supportedVersions, "supported-version", "supported Codex version; may be repeated")
	flags.Var(&directoryRoots, "directory-root", "EXPLICIT directory root mapping name=/absolute/git/repo; may be repeated")
	if err := flags.Parse(args); err != nil {
		// flag 的错误信息会回显参数值；令牌可能在其中，因此只给出通用提示。
		return errors.New("computer register: invalid arguments; usage: openfox computer register --install-token <token> [--name <name>] [--workspace-root <dir>] [--config-dir <dir>] [--no-service] [--force]")
	}
	if flags.NArg() != 0 {
		return errors.New("computer register: unexpected positional arguments")
	}
	token := strings.TrimSpace(*installToken)
	if token == "-" {
		line, err := bufio.NewReader(io.LimitReader(deps.stdin, devices.MaxInstallTokenLength+2)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return errors.New("computer register: could not read the install token from stdin")
		}
		token = strings.TrimSpace(line)
	}
	if token == "" {
		return withExitCode(exitInvalidToken, errors.New("computer register: --install-token is required (copy the command shown in OpenFox)"))
	}
	decoded, err := devices.DecodeInstallToken(token)
	if err != nil {
		return withExitCode(exitInvalidToken, errors.New("computer register: the install token is not valid; copy the full command from OpenFox again or generate a new one"))
	}
	if len(supportedVersions) == 0 {
		if value := getenv("OPENFOX_CODEX_SUPPORTED_VERSIONS"); value != "" {
			for _, version := range strings.Split(value, ",") {
				if version = strings.TrimSpace(version); version != "" {
					supportedVersions = append(supportedVersions, version)
				}
			}
		}
	}

	dir := strings.TrimSpace(*configDir)
	if dir == "" {
		if dir, err = daemon.DefaultConfigDir(); err != nil {
			return err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	configPath := filepath.Join(dir, daemon.DeviceConfigFileName)
	if _, err := os.Lstat(configPath); err == nil && !*force {
		return withExitCode(exitAlreadyRegistered, fmt.Errorf("computer register: %s already exists; this computer is already registered (use --force to replace it)", configPath))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	root := strings.TrimSpace(*workspaceRoot)
	if root == "" {
		root = filepath.Join(dir, "workspaces")
	}
	if root, err = filepath.Abs(root); err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("computer register: create workspace root: %w", err)
	}
	roots, err := directoryRootMap(directoryRoots, root)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), registerTimeout)
	defer cancel()
	privateKey, keyID, err := daemon.GenerateDeviceKey()
	if err != nil {
		return fmt.Errorf("computer register: generate device key: %w", err)
	}
	keyPath := filepath.Join(dir, daemon.DeviceKeyFileName)
	tokenPath := filepath.Join(dir, daemon.DeviceTokenFileName)
	// 新私钥先写到 .pending：注册失败时不破坏 --force 之前仍在使用的 device-key。
	pendingKeyPath := keyPath + ".pending"
	if err := daemon.WriteDeviceKey(pendingKeyPath, privateKey); err != nil {
		return fmt.Errorf("computer register: write device key: %w", err)
	}
	defer func() { _ = os.Remove(pendingKeyPath) }()
	client := daemon.AuthClient{BaseURL: daemon.APIBaseURL(decoded.CenterURL), HTTP: deps.httpClient}
	registration, err := client.Register(ctx, token, keyID, privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		return registrationError(decoded.CenterURL, err)
	}
	session, err := client.Session(ctx, registration.DeviceID, keyID, privateKey)
	if err != nil {
		return fmt.Errorf("computer register: the computer was registered but signing in failed (%s); run the command again with --force using a new token", safeCenterError(err))
	}
	if info, err := os.Lstat(keyPath); err == nil && !info.Mode().IsRegular() {
		return errors.New("computer register: device-key exists and is not a regular file")
	}
	if err := os.Rename(pendingKeyPath, keyPath); err != nil {
		return fmt.Errorf("computer register: install device key: %w", err)
	}
	if err := daemon.WritePrivateFile(tokenPath, []byte(session.Token+"\n")); err != nil {
		return fmt.Errorf("computer register: write session token: %w", err)
	}
	wssURL, err := daemon.CenterWSSURL(decoded.CenterURL)
	if err != nil {
		return errors.New("computer register: center URL in the install token is not usable")
	}
	config := daemon.DeviceConfig{
		Version: daemon.DeviceConfigVersion, CenterURL: decoded.CenterURL, WSSURL: wssURL,
		DeviceID: registration.DeviceID, OrganizationID: registration.OrganizationID, KeyID: keyID,
		PrivateKeyFile: keyPath, TokenFile: tokenPath, WorkspaceRoot: root, CenterEpoch: registration.CenterEpoch,
		SupportedVersions: supportedVersions, RuntimeExecutable: strings.TrimSpace(*runtimeExecutable), DirectoryRoots: roots,
	}
	if err := daemon.WriteDeviceConfig(configPath, config); err != nil {
		return fmt.Errorf("computer register: write config: %w", err)
	}

	out := deps.stdout
	fmt.Fprintln(out, "This computer is now connected to OpenFox.")
	if strings.TrimSpace(*name) != "" {
		fmt.Fprintln(out, "Note: --name is not applied by the center yet; rename the computer in OpenFox settings.")
	}
	fmt.Fprintf(out, "  device_id:       %s\n", registration.DeviceID)
	fmt.Fprintf(out, "  organization_id: %s\n", registration.OrganizationID)
	fmt.Fprintf(out, "  config:          %s\n", configPath)
	fmt.Fprintf(out, "  workspace_root:  %s\n", root)
	foreground := "openfox daemon run --config " + shellQuote(configPath)
	if *noService {
		fmt.Fprintln(out, "Background service was not installed (--no-service). Start the daemon with:")
		fmt.Fprintln(out, "  "+foreground)
		return nil
	}
	if deps.goos != "darwin" {
		fmt.Fprintln(out, "Background service installation is only available on macOS. Start the daemon in the foreground with:")
		fmt.Fprintln(out, "  "+foreground)
		return nil
	}
	return installLaunchAgent(ctx, deps, dir, configPath, foreground)
}

// installLaunchAgent 写 launchd plist（daemon run --config）并载入；失败时给出前台运行的命令。
func installLaunchAgent(ctx context.Context, deps computerDeps, dir, configPath, foreground string) error {
	out := deps.stdout
	manager, err := deps.manager()
	if err != nil {
		return err
	}
	manager.GOOS = deps.goos
	// socket 与 journal、CODEX_HOME 放在配置目录，和 config.json 在一起。
	manager.SocketPath = filepath.Join(dir, "daemon.sock")
	binary, err := deps.executable()
	if err != nil {
		return fmt.Errorf("computer register: resolve openfox binary: %w", err)
	}
	if err := manager.InstallService(binary, configPath); err != nil {
		return fmt.Errorf("computer register: install background service: %w (start it manually with: %s)", err, foreground)
	}
	if err := manager.Load(ctx); err != nil {
		return fmt.Errorf("computer register: start background service: %w (start it manually with: %s)", err, foreground)
	}
	fmt.Fprintf(out, "Background service installed and started (launchd %s).\n", daemon.LaunchAgentLabel)
	fmt.Fprintf(out, "  plist: %s\n", manager.LaunchAgentPath)
	if manager.LogPath != "" {
		fmt.Fprintf(out, "  log:   %s\n", manager.LogPath)
	}
	return nil
}

// registrationError 把注册失败转为不含令牌的提示。
func registrationError(centerURL string, err error) error {
	var centerErr *daemon.CenterError
	if errors.As(err, &centerErr) {
		switch {
		case centerErr.Status == http.StatusConflict || centerErr.Status == http.StatusUnauthorized ||
			centerErr.Status == http.StatusForbidden || centerErr.Status == http.StatusNotFound:
			return withExitCode(exitTokenRejected, fmt.Errorf("computer register: the install token was rejected (%s); it may be expired, already used, or issued by someone who no longer has permission; generate a new one in OpenFox", centerErr.Error()))
		case centerErr.Status == http.StatusBadRequest:
			return fmt.Errorf("computer register: the center rejected the registration request (%s)", centerErr.Error())
		}
		return withExitCode(exitCenterUnavailable, fmt.Errorf("computer register: the center at %s is not available (%s); try again later", centerURL, centerErr.Error()))
	}
	return withExitCode(exitCenterUnavailable, fmt.Errorf("computer register: could not reach the center at %s (%s)", centerURL, safeCenterError(err)))
}

// safeCenterError 返回不含请求体的错误描述（net/http 错误只含方法与 URL）。
func safeCenterError(err error) string {
	return boundedText(strings.Join(strings.Fields(err.Error()), " "))
}

// directoryRootMap 校验 --directory-root 并转为写入 config.json 的映射。
func directoryRootMap(values []string, managedRoot string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	roots, err := parseDirectoryRoots(values, managedRoot)
	if err != nil {
		return nil, err
	}
	return roots, nil
}

// directoryRootFlags 把 config.json 的映射转回 name=path 形式（按名字排序，结果稳定）。
func directoryRootFlags(roots map[string]string) []string {
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	values := make([]string, 0, len(names))
	for _, name := range names {
		values = append(values, name+"="+roots[name])
	}
	return values
}

// shellQuote 用单引号包住路径，便于用户直接复制命令。
func shellQuote(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && !strings.ContainsRune("/._-+=:@", r)
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
