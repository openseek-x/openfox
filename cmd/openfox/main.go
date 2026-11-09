package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/devices"
)

const version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var coded *exitError
		if errors.As(err, &coded) {
			os.Exit(coded.code)
		}
		os.Exit(1)
	}
}

// 退出码（桌面端据此区分失败类型）：0 成功，1 其他失败，以及下面这些。
const (
	exitInvalidToken      = 2 // 安装令牌格式不对
	exitAlreadyRegistered = 3 // 配置目录已有 config.json 且未加 --force
	exitTokenRejected     = 4 // 中心拒绝令牌（过期、已使用、签发人已无权限）
	exitCenterUnavailable = 5 // 中心不可达或暂时不可用
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func withExitCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return &exitError{code: code, err: err}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: openfox <version|computer|daemon|runtime|mcp>")
	}
	switch args[0] {
	case "version":
		fmt.Println(version)
		return nil
	case "mcp":
		return runMCP(args[1:])
	case "computer":
		return runComputer(args[1:])
	case "daemon":
		return runDaemon(args[1:])
	case "runtime":
		return runRuntime(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runDaemon(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: openfox daemon <install|start|stop|restart|status|run>")
	}
	manager, err := daemon.DefaultManager()
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		flags := flag.NewFlagSet("daemon install", flag.ContinueOnError)
		binary := flags.String("binary", "", "path to the openfox binary")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		path := *binary
		if path == "" {
			path, err = os.Executable()
			if err != nil {
				return err
			}
		}
		return manager.Install(path)
	case "start":
		return manager.Start(context.Background())
	case "stop":
		return manager.Stop(context.Background())
	case "restart":
		return manager.Restart(context.Background())
	case "status":
		status, err := manager.QueryStatus(context.Background())
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	case "run":
		return runDaemonRun(args[1:], manager)
	default:
		return fmt.Errorf("unknown daemon command %q", args[0])
	}
}

// runDaemonRun 实现 `openfox daemon run`。--config 从 config.json 进入远程模式（等价于
// --wss-url/--device-id/--token-file/--center-epoch/--workspace-root，并启用会话令牌续期）；旧参数继续可用。
func runDaemonRun(args []string, manager daemon.Manager) error {
	flags := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	socket := flags.String("socket", manager.SocketPath, "Unix socket path")
	configPath := flags.String("config", "", "device config.json written by `openfox computer register`")
	wssURL := flags.String("wss-url", "", "center WebSocket URL")
	deviceID := flags.String("device-id", "", "registered device ID")
	tokenFile := flags.String("token-file", "", "user-private device session token file")
	centerEpoch := flags.Int64("center-epoch", 0, "last accepted center epoch")
	heartbeatInterval := flags.Duration("heartbeat-interval", daemon.DefaultHeartbeatInterval, "remote heartbeat interval")
	daemonVersion := flags.String("daemon-version", version, "daemon version reported to the center")
	workspaceRoot := flags.String("workspace-root", "", "managed workspace root; enables Run execution in remote mode")
	runtimeExecutable := flags.String("runtime-executable", "", "Codex executable or PATH name (empty: auto-discover)")
	offerPollInterval := flags.Duration("offer-poll-interval", supervisor.DefaultPollInterval, "offer polling fallback interval")
	maxRuns := flags.Int("max-runs", supervisor.DefaultMaxConcurrent, "maximum concurrent local Runs")
	journalPath := flags.String("journal", "", "local Run journal (SQLite); defaults next to the socket")
	tokenRenewBefore := flags.Duration("token-renew-before", daemon.DefaultTokenRenewBefore, "with --config: renew the device session this long before it expires")
	var organizations, supportedVersions, directoryRoots stringListFlag
	flags.Var(&organizations, "organization", "organization ID to poll for offers; may be repeated")
	flags.Var(&directoryRoots, "directory-root", "EXPLICIT directory root mapping name=/absolute/git/repo; may be repeated")
	flags.Var(&supportedVersions, "supported-version", "supported Codex version; may be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	explicit := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	var tokenProvider func(context.Context) (string, error)
	var invalidate func(string)
	if path := strings.TrimSpace(*configPath); path != "" {
		if explicit["wss-url"] || explicit["device-id"] || explicit["token-file"] || explicit["center-epoch"] {
			return errors.New("daemon --config cannot be combined with --wss-url, --device-id, --token-file or --center-epoch")
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		config, err := daemon.LoadDeviceConfig(path)
		if err != nil {
			return fmt.Errorf("daemon: load %s: %w", path, err)
		}
		privateKey, err := daemon.ReadDeviceKey(config.PrivateKeyFile)
		if err != nil {
			return fmt.Errorf("daemon: load device key: %w", err)
		}
		*wssURL, *deviceID, *tokenFile, *centerEpoch = config.WSSURL, config.DeviceID, config.TokenFile, config.CenterEpoch
		if !explicit["workspace-root"] {
			*workspaceRoot = config.WorkspaceRoot
		}
		if !explicit["socket"] {
			// 与 config.json 同目录：journal 与独立 CODEX_HOME 默认也放在这里。
			*socket = filepath.Join(filepath.Dir(path), "daemon.sock")
		}
		if !explicit["runtime-executable"] {
			*runtimeExecutable = config.RuntimeExecutable
		}
		if len(supportedVersions) == 0 {
			supportedVersions = append(supportedVersions, config.SupportedVersions...)
		}
		directoryRoots = append(directoryRoots, directoryRootFlags(config.DirectoryRoots)...)
		organizations = append(organizations, config.OrganizationID)
		provider, err := daemon.NewRenewingTokenProvider(daemon.RenewingTokenProviderConfig{
			TokenFile: config.TokenFile, DeviceID: config.DeviceID, KeyID: config.KeyID, PrivateKey: privateKey,
			Issuer: daemon.AuthClient{BaseURL: daemon.APIBaseURL(config.CenterURL)}, RenewBefore: *tokenRenewBefore,
		})
		if err != nil {
			return err
		}
		tokenProvider, invalidate = provider.Token, provider.Invalidate
	} else if strings.TrimSpace(*tokenFile) != "" {
		tokenProvider = daemon.FileTokenProvider(*tokenFile)
	}

	server := daemon.NewControlServer(*socket)
	remoteRequested := strings.TrimSpace(*wssURL) != "" || strings.TrimSpace(*deviceID) != "" || strings.TrimSpace(*tokenFile) != "" || *centerEpoch != 0
	if remoteRequested && (strings.TrimSpace(*wssURL) == "" || strings.TrimSpace(*deviceID) == "" || strings.TrimSpace(*tokenFile) == "" || *centerEpoch <= 0) {
		return errors.New("daemon remote mode requires --wss-url, --device-id, --token-file, and positive --center-epoch")
	}
	if !remoteRequested && strings.TrimSpace(*workspaceRoot) != "" {
		return errors.New("daemon --workspace-root requires remote mode")
	}
	if len(directoryRoots) > 0 && strings.TrimSpace(*workspaceRoot) == "" {
		return errors.New("daemon --directory-root requires --workspace-root")
	}
	var remote *daemon.RemoteClient
	var source *daemon.StaticHeartbeatSource
	var executor *executorRuntime
	var onRunMessage, onCommandMessage func(daemon.Message)
	var onHeartbeat func()
	if remoteRequested {
		var err error
		source = daemon.NewStaticHeartbeatSource(devices.Heartbeat{
			CenterEpoch: *centerEpoch, ProtocolMajor: daemon.ProtocolMajor,
			DaemonVersion: strings.TrimSpace(*daemonVersion), OperatingSystem: goruntime.GOOS,
			Architecture: goruntime.GOARCH, Capabilities: []byte(`{"runtime":"unknown"}`),
		})
		remote, err = daemon.NewRemoteClient(daemon.RemoteClientConfig{
			URL: *wssURL, DeviceID: *deviceID, TokenProvider: tokenProvider,
			HeartbeatSource: source, HeartbeatInterval: *heartbeatInterval,
			OnState: func(state daemon.RemoteState) { server.SetRemoteState(state) },
			OnRunMessage: func(message daemon.Message) {
				if onRunMessage != nil {
					onRunMessage(message)
				}
			},
			OnCommandMessage: func(message daemon.Message) {
				if onCommandMessage != nil {
					onCommandMessage(message)
				}
			},
			OnHeartbeat: func() {
				if onHeartbeat != nil {
					onHeartbeat()
				}
			},
			OnUnauthorized: invalidate,
			OnReconcile: func(info daemon.ReconcileInfo) {
				server.SetRemoteState(daemon.RemoteReconcile)
				server.SetReconcileAt(info.CurrentEpoch, fmt.Sprintf("center epoch %d requires reconciliation", info.CurrentEpoch))
			},
		})
		if err != nil {
			return err
		}
		if strings.TrimSpace(*workspaceRoot) != "" {
			executor, err = newExecutor(executorOptions{
				deviceID: *deviceID, wssURL: *wssURL, tokenProvider: tokenProvider, invalidate: invalidate, workspaceRoot: *workspaceRoot,
				runtimeExecutable: *runtimeExecutable, supportedVersions: supportedVersions,
				journalPath: *journalPath, socketPath: *socket, organizations: organizations,
				pollInterval: *offerPollInterval, maxRuns: *maxRuns, directoryRoots: directoryRoots,
			})
			if err != nil {
				return err
			}
			defer executor.close()
			onRunMessage = executor.supervisor.HandleMessage
			server.ActiveRuns = executor.supervisor.ActiveRuns
		}
		server.SetRunning(*centerEpoch)
	} else {
		server.SetRunning(0)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	controlErrors := make(chan error, 1)
	go func() { controlErrors <- server.Serve(ctx) }()
	if remote == nil {
		return <-controlErrors
	}
	remoteErrors := make(chan error, 1)
	if executor != nil {
		// 心跳上报 Codex 版本、登录状态与可用模型（供 Agent 配置选择模型），以及本机存储占用。
		reporter := &capabilityReporter{prober: executor.adapter, source: source, directoryRoots: executor.directoryRoots, gitRoots: probeGitRoots(ctx, executor.directoryRoots)}
		reporter.inventoryWake = make(chan struct{}, 1)
		reporter.storeInventory = executor.client.StoreMCPInventory
		executor.client.OnMCPInventoryPrivacy = reporter.setInventoryPrivacy
		reporter.publish()
		for _, other := range executor.others {
			reporter.extras = append(reporter.extras, &namedProber{name: other.Name(), prober: other})
		}
		storageStats := &storageReporter{pathsFn: executor.storagePaths, reporter: reporter}
		commandExecutor, err := newCommandExecutor(executor, reporter, storageStats, remote, organizations)
		if err != nil {
			return err
		}
		proxySlots := make(chan struct{}, 4)
		onHeartbeat = commandExecutor.Wake
		onCommandMessage = func(m daemon.Message) {
			if m.Type != "gitlab.proxy" {
				commandExecutor.HandleMessage(m)
				return
			}
			select {
			case proxySlots <- struct{}{}:
				go func() { defer func() { <-proxySlots }(); daemon.ForwardGitLab(ctx, *wssURL, tokenProvider, m) }()
			default:
			}
		}
		var background sync.WaitGroup
		// 退出前等待在途的探测、统计与远程命令结束，避免遗留 app-server 进程。
		defer func() {
			stop()
			background.Wait()
		}()
		for _, task := range []func(context.Context){reporter.run, storageStats.run, commandExecutor.Run} {
			background.Add(1)
			go func(task func(context.Context)) {
				defer background.Done()
				task(ctx)
			}(task)
		}
		executorDone := make(chan struct{})
		go func() {
			defer close(executorDone)
			if err := executor.supervisor.Run(ctx); err != nil {
				fmt.Fprintln(os.Stderr, "run supervisor stopped:", err)
			}
		}()
		// 返回前等待在途 Run 停止并上报，避免遗留进程。
		defer func() {
			stop()
			<-executorDone
		}()
	}
	go func() { remoteErrors <- remote.Run(ctx) }()
	select {
	case err := <-controlErrors:
		return err
	case err := <-remoteErrors:
		if errors.Is(err, daemon.ErrReconcileRequired) {
			<-ctx.Done()
			return nil
		}
		return err
	}
}
