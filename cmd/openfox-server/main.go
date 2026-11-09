package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/dispatch"
	"openfox/internal/email"
	"openfox/internal/exports"
	"openfox/internal/gitlabproxy"
	"openfox/internal/identity"
	"openfox/internal/imports"
	"openfox/internal/integrations"
	"openfox/internal/integrations/dingtalk"
	"openfox/internal/integrations/feishu"
	"openfox/internal/integrations/gitee"
	"openfox/internal/integrations/github"
	"openfox/internal/integrations/gitlab"
	"openfox/internal/integrations/wechat"
	"openfox/internal/integrations/wecom"
	"openfox/internal/notifications"
	"openfox/internal/platform/config"
	"openfox/internal/platform/httpserver"
	"openfox/internal/platform/migrations"
	"openfox/internal/platform/postgres"
	"openfox/internal/platform/ratelimit"
	"openfox/internal/runtimeexports"
	"openfox/internal/secrets"
)

const version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: openfox-server <serve|migrate|market|version>")
	}

	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "migrate":
		return checkMigrations(args[1:])
	case "market":
		return marketCommand(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var pool *postgres.Pool
	readiness := httpserver.ReadinessNotConfigured
	if strings.TrimSpace(cfg.Database.URLFile) != "" {
		openContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pool, err = postgres.NewFromURLFile(openContext, postgres.Config{
			MaxConns:         int32(cfg.Database.PoolMax),
			ContextRole:      cfg.Database.ContextRole,
			AppRole:          cfg.Database.AppRole,
			StatementTimeout: cfg.Database.StatementTimeout,
			LockTimeout:      cfg.Database.LockTimeout,
		}, cfg.Database.URLFile)
		cancel()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer pool.Close()
		readiness = pool.Ping
		hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{
			MemoryKiB:   cfg.Auth.Argon2MemoryKiB,
			Iterations:  cfg.Auth.Argon2Iterations,
			Parallelism: cfg.Auth.Argon2Parallelism,
			KeyLength:   cfg.Auth.Argon2KeyLength,
			SaltLength:  16,
		})
		if err != nil {
			return fmt.Errorf("configure password hasher: %w", err)
		}
		var storage artifacts.Storage
		if strings.TrimSpace(cfg.Storage.Root) != "" {
			storage = artifacts.Local{Root: cfg.Storage.Root}
		}
		secretBox, err := secrets.LoadFile(cfg.Secrets.KeyFile)
		if err != nil {
			return fmt.Errorf("load secrets key: %w", err)
		}
		if secretBox == nil {
			logger.Warn("secrets_key_not_configured", "effect", "agent environment variables are unavailable")
		}
		var githubClient *github.Client
		if githubCfg := cfg.Integrations.GitHub(); githubCfg.Configured {
			githubClient = github.New(githubCfg.ClientID, githubCfg.ClientSecret, githubCfg.BaseURL, githubCfg.APIURL)
		}
		// GitLab（自部署 / 极狐）：实例地址由组织管理员填写，所以客户端按连接构造；非生产环境允许 http 与本机地址（本机模拟服务）。
		gitlabOptions := gitlab.Options{AllowLocal: cfg.Environment != config.EnvironmentProduction}
		// Gitee：单一 SaaS，第三方应用由部署方在服务端配置（OPENFOX_GITEE_*），未配置时不构造客户端。
		var giteeClient *gitee.Client
		if giteeCfg := cfg.Integrations.Gitee(); giteeCfg.Configured {
			giteeClient = gitee.New(giteeCfg.ClientID, giteeCfg.ClientSecret, giteeCfg.BaseURL, giteeCfg.APIURL)
		}
		dispatchHub := dispatch.NewHub()
		gitFactory := integrations.WithGitee(integrations.NewGitClientFactory(gitlabOptions, gitlabproxy.Broker{Pool: pool, Hub: dispatchHub}), giteeClient)
		gitValidateInstance := integrations.NewInstanceValidator(gitlabOptions)
		// 飞书客户端：地址可配置，应用凭证由组织管理员在页面里绑定，所以始终构造。
		feishuBaseURL, feishuAccountsURL := cfg.Integrations.FeishuURLs()
		feishuClient := feishu.New(feishuBaseURL, feishuAccountsURL)
		// Lark：使用同一套开放平台协议，只是域名不同。
		larkBaseURL, larkAccountsURL := cfg.Integrations.LarkURLs()
		larkClient := feishu.New(larkBaseURL, larkAccountsURL)
		// 钉钉：新版接口、旧版接口、网页登录授权页三个地址可配置。
		dingAPIURL, dingOAPIURL, dingLoginURL := cfg.Integrations.DingTalkURLs()
		dingTalkClient := dingtalk.New(dingAPIURL, dingOAPIURL, dingLoginURL)
		// 微信 ClawBot / iLink：基址可配置；开发 / 测试环境允许服务端返回与配置基址同源的地址（本机模拟服务）。
		wechatClient := wechat.New(cfg.Integrations.WeChatILinkURL(), version, cfg.Environment != config.EnvironmentProduction)
		wecomClient := &wecom.Client{Logger: logger}
		var emailSender identity.EmailSender
		if strings.TrimSpace(cfg.Email.SMTP.Host) != "" {
			emailSender = email.SMTPSender{Config: email.SMTPConfig{
				Host: cfg.Email.SMTP.Host, Port: cfg.Email.SMTP.Port, Security: cfg.Email.SMTP.Security,
				User: cfg.Email.SMTP.User, From: cfg.Email.SMTP.From, Password: cfg.Email.SMTP.Password,
				Timeout: cfg.Email.SMTP.Timeout,
			}}
		}
		trustedProxies, err := ratelimit.ParseTrustedProxies(cfg.Server.TrustedProxies)
		if err != nil {
			return fmt.Errorf("parse trusted proxies: %w", err)
		}
		dispatchStore, err := dispatch.NewStore(dispatch.Config{
			OfferTTL: cfg.Scheduler.OfferTTL, LeaseTTL: cfg.Scheduler.LeaseTTL, LeaseRenew: cfg.Scheduler.LeaseRenew,
			PollInterval: cfg.Scheduler.PollInterval, BatchSize: cfg.Scheduler.BatchSize,
		})
		if err != nil {
			return fmt.Errorf("configure dispatch: %w", err)
		}
		// 设备唤醒经 PostgreSQL LISTEN/NOTIFY 在实例之间转发；占用一条池外连接。
		relay, err := dispatch.NewPostgresRelay(pool, dispatchHub, logger)
		if err != nil {
			return fmt.Errorf("configure dispatch relay: %w", err)
		}
		// 派发 worker 总是运行；随进程退出而停止，未提交的阶段事务会回滚，下一轮由其他实例或重启后继续。
		workerContext, stopWorker := context.WithCancel(context.Background())
		workerDone, relayDone, importDone, syncDone, exportDone, runtimeExportDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
		imConnDone, feishuNotifyDone, imWorkerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
		emailNotifyDone := make(chan struct{})
		tokenRefreshDone := make(chan struct{})
		worker := &dispatch.Worker{Pool: pool, Store: dispatchStore, Hub: dispatchHub, Logger: logger}
		go func() {
			defer close(relayDone)
			relay.Run(workerContext)
		}()
		go func() {
			defer close(workerDone)
			logger.Info("dispatch_worker_started", "poll_interval", cfg.Scheduler.PollInterval.String(),
				"batch", cfg.Scheduler.BatchSize, "relay_instance", relay.Instance())
			_ = worker.Run(workerContext)
		}()
		// 导入 worker：执行 Jira CSV 导入、关联外部账号、清理过期草稿。
		importWorker := &imports.Worker{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(), Secrets: secretBox, Storage: storage}
		go func() {
			defer close(importDone)
			logger.Info("import_worker_started", "instance", importWorker.Instance)
			_ = importWorker.Run(workerContext)
		}()
		// 导出 worker：生成任务数据导出文件、清理过期文件；没有文件存储时导出任务会失败（STORAGE_UNAVAILABLE）。
		exportWorker := &exports.Worker{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(), Storage: storage}
		go func() {
			defer close(exportDone)
			logger.Info("export_worker_started", "instance", exportWorker.Instance)
			_ = exportWorker.Run(workerContext)
		}()
		runtimeExportWorker := &runtimeexports.Worker{Pool: pool, Storage: storage, Logger: logger}
		go func() {
			defer close(runtimeExportDone)
			logger.Info("runtime_session_export_cleanup_started")
			_ = runtimeExportWorker.Run(workerContext)
		}()
		// Issue 同步 worker：把双向同步空间里的任务 / 评论变更发到 GitHub，并导入已有 Issue。
		syncWorker := &integrations.SyncWorker{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(),
			Service: integrations.Service{GitHubConfigured: cfg.Integrations.GitHubConfigured(), GiteeConfigured: cfg.Integrations.GiteeConfigured(), GitHub: githubClient, Box: secretBox,
				GitFactory: gitFactory, PublicURL: strings.TrimRight(cfg.Server.PublicURL, "/")}}
		go func() {
			defer close(syncDone)
			logger.Info("issue_sync_worker_started", "instance", syncWorker.Instance)
			_ = syncWorker.Run(workerContext)
		}()
		// Git 令牌续期：GitLab / Gitee 的 OAuth 访问令牌会过期，到期前用刷新令牌换新。
		tokenRefresher := &integrations.TokenRefresher{Pool: pool, Logger: logger,
			Service: integrations.Service{Box: secretBox, GitFactory: gitFactory, PublicURL: strings.TrimRight(cfg.Server.PublicURL, "/")}}
		go func() {
			defer close(tokenRefreshDone)
			_ = tokenRefresher.Run(workerContext)
		}()
		// 飞书长连接管理与通知 worker：没有绑定飞书应用时空转。
		feishuService := integrations.Service{Feishu: feishuClient, DingTalk: dingTalkClient, WeCom: wecomClient, IMClients: map[string]*feishu.Client{integrations.ProviderFeishu: feishuClient, integrations.ProviderLark: larkClient}, Box: secretBox, PublicURL: strings.TrimRight(cfg.Server.PublicURL, "/")}
		imConnManager := &integrations.LongConnManager{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(),
			Service: feishuService, Client: feishuClient}
		go func() {
			defer close(imConnDone)
			logger.Info("feishu_longconn_manager_started", "instance", imConnManager.Instance)
			_ = imConnManager.Run(workerContext)
		}()
		feishuNotifyWorker := &integrations.FeishuNotifyWorker{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(), Service: feishuService, Connections: imConnManager}
		go func() {
			defer close(feishuNotifyDone)
			logger.Info("feishu_notify_worker_started", "instance", feishuNotifyWorker.Instance)
			_ = feishuNotifyWorker.Run(workerContext)
		}()
		emailWorker := &notifications.EmailWorker{Pool: pool, Sender: emailSender, Logger: logger,
			Instance: imports.NewInstanceID(), PublicURL: strings.TrimRight(cfg.Server.PublicURL, "/")}
		go func() {
			defer close(emailNotifyDone)
			logger.Info("email_notification_worker_started", "instance", emailWorker.Instance, "delivery_available", emailSender != nil)
			_ = emailWorker.Run(workerContext)
		}()
		// 飞书扫码会话、入站消息与回发：没有任务助手 / Agent 机器人时空转。
		imWorker := &integrations.IMWorker{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(), Service: feishuService, Connections: imConnManager}
		go func() {
			defer close(imWorkerDone)
			logger.Info("feishu_im_worker_started", "instance", imWorker.Instance)
			_ = imWorker.Run(workerContext)
		}()
		// 微信长轮询与入站 / 回发：没有微信绑定时空转；需要密钥文件才能解密 bot_token。
		wechatService := integrations.Service{WeChat: wechatClient, Box: secretBox, PublicURL: strings.TrimRight(cfg.Server.PublicURL, "/")}
		wechatPoller := &integrations.WeChatPoller{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(), Service: wechatService}
		wechatWorker := &integrations.WeChatWorker{Pool: pool, Logger: logger, Instance: imports.NewInstanceID(), Service: wechatService}
		wechatPollerDone, wechatWorkerDone := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(wechatPollerDone)
			logger.Info("wechat_poller_started", "instance", wechatPoller.Instance)
			_ = wechatPoller.Run(workerContext)
		}()
		go func() {
			defer close(wechatWorkerDone)
			logger.Info("wechat_worker_started", "instance", wechatWorker.Instance)
			_ = wechatWorker.Run(workerContext)
		}()
		defer func() {
			stopWorker()
			deadline := time.After(10 * time.Second)
			for _, done := range []chan struct{}{workerDone, relayDone, importDone, syncDone, exportDone, runtimeExportDone, imConnDone, feishuNotifyDone, emailNotifyDone, imWorkerDone, wechatPollerDone, wechatWorkerDone, tokenRefreshDone} {
				select {
				case <-done:
				case <-deadline:
					logger.Warn("dispatch_worker_stop_timeout")
					return
				}
			}
		}()
		serverHandler := api.NewWithDispatch(identity.Service{
			Store:       identity.PostgresStore{Pool: pool},
			EmailStore:  identity.PostgresStore{Pool: pool},
			EmailSender: emailSender,
			EmailOTP: identity.EmailOTPConfig{
				ChallengeTTL: cfg.Email.OTP.ChallengeTTL, ResendCooldown: cfg.Email.OTP.ResendCooldown,
				MaxAttempts: cfg.Email.OTP.MaxAttempts, UniversalCode: cfg.Email.OTP.UniversalCode,
				DailySends: cfg.Auth.EmailDailySends, DailyFailures: cfg.Auth.EmailDailyFailures,
			},
			PasswordHasher:  hasher,
			SessionIdle:     cfg.Auth.SessionIdle,
			SessionAbsolute: cfg.Auth.SessionAbsolute,
			Now:             time.Now,
		}, pool, cfg.Environment == config.EnvironmentProduction, storage, api.AuthRateLimits{
			EmailRequestPerWindow: cfg.Auth.EmailRequestPerIP,
			EmailVerifyPerWindow:  cfg.Auth.EmailVerifyPerIP,
			Window:                cfg.Auth.RateLimitWindow,
			TrustedProxies:        trustedProxies,
		}, api.DispatchOptions{
			Store: dispatchStore, Hub: dispatchHub, Secrets: secretBox, PublicURL: cfg.Server.PublicURL,
			GitHubConfigured: cfg.Integrations.GitHubConfigured(), GiteeConfigured: cfg.Integrations.GiteeConfigured(), GitHubClient: githubClient, GitFactory: gitFactory, GitValidateInstance: gitValidateInstance, FeishuClient: feishuClient, LarkClient: larkClient, DingTalkClient: dingTalkClient, WeChatClient: wechatClient, WeComClient: wecomClient,
		})
		server := httpserver.New(cfg, version, logger, readiness)
		server.Mount("/api/", http.StripPrefix("/api", serverHandler))
		return serveHTTP(server, logger, cfg)
	}
	server := httpserver.New(cfg, version, logger, readiness)
	return serveHTTP(server, logger, cfg)
}

func serveHTTP(server *httpserver.Server, logger *slog.Logger, cfg config.Config) error {

	shutdownContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("server_started", "listen", cfg.Server.Listen, "version", version)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	case <-shutdownContext.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}

func checkMigrations(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dir := flags.String("dir", "db/migrations", "migration directory")
	check := flags.Bool("check", false, "validate the migration manifest without executing SQL")
	apply := flags.Bool("apply", false, "apply pending migrations using the supplied database URL file")
	databaseURLFile := flags.String("database-url-file", os.Getenv("OPENFOX_DATABASE_URL_FILE"), "restricted file containing the PostgreSQL URL")
	timeout := flags.Duration("timeout", 10*time.Minute, "overall migration timeout")
	lockTimeout := flags.Duration("lock-timeout", 5*time.Second, "PostgreSQL lock timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *check && *apply {
		return errors.New("migrate: --check and --apply are mutually exclusive")
	}

	files, err := migrations.Load(*dir)
	if err != nil {
		return fmt.Errorf("check migrations: %w", err)
	}
	if *check {
		fmt.Printf("migration manifest valid: %d files\n", len(files))
		return nil
	}
	if !*apply {
		return errors.New("migrate requires --check or explicit --apply")
	}
	if strings.TrimSpace(*databaseURLFile) == "" {
		return errors.New("migrate --apply requires --database-url-file or OPENFOX_DATABASE_URL_FILE")
	}
	contents, err := os.ReadFile(*databaseURLFile)
	if err != nil {
		return fmt.Errorf("read database URL file: %w", err)
	}
	result, err := migrations.Apply(context.Background(), migrations.ApplyConfig{
		URL: strings.TrimSpace(string(contents)), Dir: *dir,
		Timeout: *timeout, LockTimeout: *lockTimeout,
	})
	if err != nil {
		return err
	}
	for _, entry := range result.Applied {
		fmt.Printf("applied %s\n", entry.Filename)
	}
	fmt.Printf("migration apply complete: applied=%d total=%d\n", len(result.Applied), len(result.Current))
	return nil
}
