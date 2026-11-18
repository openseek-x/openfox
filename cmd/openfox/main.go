package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/devices"
)

// version 由构建时的 -ldflags "-X main.version=..." 注入。
var version = "dev"

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

// runtimeConfigSyncInterval 是 daemon 向中心同步组织运行配置版本的间隔（阶段 12e，K-02）。
const runtimeConfigSyncInterval = 5 * time.Minute

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
		return errors.New("usage: openfox <version|computer|daemon|runtime|mcp|auth|profile|org|space|task|agent|conversation|run|search|me>")
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
	case "auth", "profile", "org", "space", "task", "agent", "conversation", "run", "search", "me":
		return runCLI(args[0], args[1:], defaultCLIDeps())
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runDaemon(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: openfox daemon <install|start|stop|restart|status|run|service-status|startup|logs|service-action>")
	}
	manager, err := daemon.DefaultManager()
	if err != nil {
		return err
	}
	switch args[0] {
	case "service-status", "startup", "logs", "service-action", "proxy-status", "proxy-save":
		return runLocalServiceInput(args, manager, os.Stdin, os.Stdout)
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
	var machineProxy *daemon.MachineProxyNetwork
	var centerHTTP *http.Client
	var proxyEnvironment func() (map[string]string, error)
	// 阶段 12e：centerAPI 是 --config 模式下中心 API 根地址（运行配置确认等设备侧接口用）；显式参数模式为空，不启用。
	var centerAPI string
	// renewing / deviceConfigPath 仅 --config 模式有值：密钥轮换要改写本机配置并切换令牌提供者。
	var renewing *daemon.RenewingTokenProvider
	var deviceConfigPath string
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
		if goruntime.GOOS == "darwin" {
			store := daemon.MachineProxyStore{ConfigPath: path, Scope: daemon.ProxyScope(config)}
			machineProxy = daemon.NewMachineProxyNetwork(store)
			defer machineProxy.Close()
			centerHTTP, proxyEnvironment = machineProxy.Client(), store.Environment
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
		// 注册组织在前，其余来自旁路文件 organizations.json（阶段 12e，D-09；旧版本 daemon 只认 config.json 的组织）。
		organizations = append(organizations, daemon.LoadOrganizations(path, config)...)
		centerAPI, deviceConfigPath = daemon.APIBaseURL(config.CenterURL), path
		provider, err := daemon.NewRenewingTokenProvider(daemon.RenewingTokenProviderConfig{
			TokenFile: config.TokenFile, DeviceID: config.DeviceID, KeyID: config.KeyID, PrivateKey: privateKey,
			Issuer: daemon.AuthClient{BaseURL: daemon.APIBaseURL(config.CenterURL), HTTP: centerHTTP}, RenewBefore: *tokenRenewBefore,
		})
		if err != nil {
			return err
		}
		tokenProvider, invalidate, renewing = provider.Token, provider.Invalidate, provider
	} else if strings.TrimSpace(*tokenFile) != "" {
		tokenProvider = daemon.FileTokenProvider(*tokenFile)
	}

	server := daemon.NewControlServer(*socket)
	var centerDialer daemon.Dialer
	if machineProxy != nil {
		centerDialer, server.MachineProxy = machineProxy, machineProxy.Status
	}
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
			Dialer: centerDialer,
			URL:    *wssURL, DeviceID: *deviceID, TokenProvider: tokenProvider,
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
				httpClient: centerHTTP, machineProxy: proxyEnvironment,
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
	reporter := &capabilityReporter{source: source, developmentToolsProbe: probeDevelopmentTools, timezoneProbe: probeTimezone}
	var background sync.WaitGroup
	// Wait for probes, storage collection and remote commands before shutdown.
	defer func() { stop(); background.Wait() }()
	startBackground := func(task func(context.Context)) {
		background.Add(1)
		go func() { defer background.Done(); task(ctx) }()
	}
	if executor != nil {
		if executor.browser != nil {
			startBackground(func(ctx context.Context) {
				if err := executor.browser.Serve(ctx, executor.browserSocket); err != nil {
					fmt.Fprintln(os.Stderr, "local browser broker stopped")
				}
			})
		}
		// 心跳上报 Codex 版本、登录状态与可用模型（供 Agent 配置选择模型），以及本机存储占用。
		reporter.prober = executor.adapter
		reporter.directoryRoots = executor.directoryRoots
		reporter.gitRoots = probeGitRoots(ctx, executor.directoryRoots)
		reporter.inventoryWake = make(chan struct{}, 1)
		// 阶段 12e：本机并发上限（中心取与中心上限的较小值，不反向覆盖本机配置）、命中受保护名单的已注册根（只告警）。
		// 旧中心忽略未知键。
		reporter.additional = map[string]any{
			"local_max_runs":            executor.supervisor.MaxConcurrent(),
			"protected_directory_roots": executor.protectedRoots,
		}
		// 阶段 12e（D-01）：两步式密钥轮换。详情页的 key_rotate 命令与 `openfox computer rotate`（经控制 socket）
		// 共用同一个轮换器，互斥执行。
		var rotator *daemon.KeyRotator
		if renewing != nil {
			rotator = &daemon.KeyRotator{
				ConfigPath: deviceConfigPath, Client: daemon.AuthClient{BaseURL: centerAPI, HTTP: centerHTTP}, CurrentToken: renewing.Token,
				Switch: renewing.SwitchKey, Refresh: remote.RefreshAuth,
			}
			server.SetRotateKey(rotator.Rotate)
			// 能力位：中心据此决定是否允许从详情页下发 key_rotate；旧 daemon 没有该键。
			reporter.additional["key_rotation"] = true
			// 多组织：本 daemon 会向中心刷新 ACTIVE 授权的组织并分别领取。
			reporter.additional["multi_organization"] = true
		}
		reporter.storeInventory = executor.client.StoreMCPInventory
		executor.client.OnMCPInventoryPrivacy = reporter.setInventoryPrivacy
		reporter.publish()
		for _, other := range executor.others {
			reporter.extras = append(reporter.extras, &namedProber{name: other.Name(), prober: other})
		}
		if executor.hermes != nil {
			reporter.extras = append(reporter.extras, &namedProber{name: executor.hermes.Name(), prober: executor.hermes})
		}
		storageStats := &storageReporter{pathsFn: executor.storagePaths, reporter: reporter}
		commandExecutor, err := newCommandExecutor(executor, reporter, storageStats, remote, organizations, rotator)
		if err != nil {
			return err
		}
		var backgroundTasks []func(context.Context)
		// knownOrganizations 返回当前已知的组织列表（启动时来自本机配置，之后由中心刷新）。
		var knownMu sync.Mutex
		known := append([]string(nil), organizations...)
		knownOrganizations := func() []string {
			knownMu.Lock()
			defer knownMu.Unlock()
			return append([]string(nil), known...)
		}
		if rotator != nil {
			// 阶段 12e（D-09）：定期向中心刷新这台电脑 ACTIVE 授权的组织，写入旁路文件并更新领取与命令轮询列表；
			// 被移出的组织不再轮询。旧版中心不支持时停止刷新，保持本机配置的组织。
			refresher := &daemon.OrganizationRefresher{
				ConfigPath: deviceConfigPath, Client: daemon.AuthClient{BaseURL: centerAPI, HTTP: centerHTTP}, Token: tokenProvider,
				OnChange: func(next []string) {
					knownMu.Lock()
					known = append([]string(nil), next...)
					knownMu.Unlock()
					executor.supervisor.SetOrganizations(next)
					commandExecutor.SetOrganizations(next)
				},
			}
			backgroundTasks = append(backgroundTasks, refresher.Run)
		}
		if centerAPI != "" && tokenProvider != nil {
			// 阶段 12e（K-02）：按组织拉取运行配置、确认已应用版本，并把本机状态写入心跳 capabilities.runtime_config。
			runtimeSync := &daemon.RuntimeConfigSync{Client: daemon.AuthClient{BaseURL: centerAPI, HTTP: centerHTTP}, Token: tokenProvider,
				Publish: func(status map[string]daemon.RuntimeConfigStatus) {
					if len(status) == 0 {
						// 旧版中心或尚未取到任何组织的配置：不上报该键，中心按旧 daemon 处理。
						reporter.setExtra("runtime_config", nil)
						return
					}
					reporter.setExtra("runtime_config", status)
					remote.RequestHeartbeat()
				}}
			backgroundTasks = append(backgroundTasks, func(ctx context.Context) {
				// 启动后同步一次，之后每 5 分钟一次。
				ticker := time.NewTicker(runtimeConfigSyncInterval)
				defer ticker.Stop()
				for {
					runtimeSync.Sync(ctx, knownOrganizations())
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			})
		}
		if rotator != nil && daemon.PendingKeyRotation(deviceConfigPath) {
			// 上次轮换中断：启动后续做（期间中心两把密钥都认；失败只记录，下次启动或下次轮换再试）。
			backgroundTasks = append(backgroundTasks, func(ctx context.Context) {
				if _, err := rotator.Rotate(ctx); err != nil {
					fmt.Fprintln(os.Stderr, "resume key rotation:", err)
				}
			})
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
				go func() {
					defer func() { <-proxySlots }()
					daemon.ForwardGitLab(ctx, *wssURL, tokenProvider, m, centerHTTP)
				}()
			default:
			}
		}
		startBackground(storageStats.run)
		startBackground(commandExecutor.Run)
		for _, task := range backgroundTasks {
			startBackground(task)
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
	// Remote-only daemons still report development tools without a Runtime.
	startBackground(reporter.run)
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
