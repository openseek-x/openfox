package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	EnvironmentDevelopment = "development"
	EnvironmentTest        = "test"
	EnvironmentProduction  = "production"
)

type Config struct {
	Environment  string
	Server       ServerConfig
	Database     DatabaseConfig
	Auth         AuthConfig
	Email        EmailConfig
	Scheduler    SchedulerConfig
	Storage      StorageConfig
	Secrets      SecretsConfig
	Integrations IntegrationsConfig
}

// SecretsConfig 是落库敏感值（Agent 环境变量等）的加密配置。
type SecretsConfig struct {
	// KeyFile 指向 32 字节主密钥文件（原始字节或 64 位十六进制），权限不得对组和其他用户开放。
	// 生产环境必填；开发与测试环境可留空，此时依赖加密的功能返回 SECRETS_UNAVAILABLE。
	KeyFile string
}

// IntegrationsConfig 是外部应用集成的服务端应用配置，默认全部为空（未配置）。
type IntegrationsConfig struct {
	// GitHubClientID / GitHubClientSecretFile 是 GitHub OAuth App 的 client id 与 client secret 文件（绝对路径，权限不得对组和其他用户开放）。
	// 两项都设置才算「已配置」；本阶段只做配置判断，不读取文件内容。
	GitHubClientID         string
	GitHubClientSecretFile string
	// GitHubClientSecret 是 client secret 文件的内容（Load 时读取并去掉首尾空白），不得出现在日志里。
	GitHubClientSecret string
	// GitHubBaseURL / GitHubAPIURL 是授权页与换取令牌、REST API 的地址，默认 github.com；
	// production 必须是 https，development / test 允许 http（本机模拟服务）。
	GitHubBaseURL string
	GitHubAPIURL  string
	// GiteeClientID / GiteeClientSecretFile 是 Gitee 第三方应用的 Client ID 与 Client Secret 文件（规则同 GitHub）；
	// GiteeBaseURL / GiteeAPIURL 是授权页 / 换取令牌与 REST API v5 的地址，默认 gitee.com，校验规则同 GitHub 地址。
	GiteeClientID         string
	GiteeClientSecretFile string
	// GiteeClientSecret 是 Client Secret 文件的内容（Load 时读取），不得出现在日志里。
	GiteeClientSecret string
	GiteeBaseURL      string
	GiteeAPIURL       string
	// FeishuBaseURL / FeishuAccountsURL 是飞书开放平台 API 与账号 / 授权页地址，默认 open.feishu.cn / accounts.feishu.cn；
	// 校验规则与 GitHub 地址相同（production 必须 https）。飞书应用凭证由组织管理员在页面里绑定，不在这里配置。
	FeishuBaseURL     string
	FeishuAccountsURL string
	// LarkBaseURL / LarkAccountsURL 是 Lark（国际版）的开放平台 API 与账号页地址，默认 open.larksuite.com / accounts.larksuite.com；校验规则同飞书。
	LarkBaseURL     string
	LarkAccountsURL string
	// DingTalkAPIURL / DingTalkOAPIURL / DingTalkLoginURL 是钉钉新版接口、旧版接口与网页登录授权页地址，
	// 默认 api.dingtalk.com / oapi.dingtalk.com / login.dingtalk.com；校验规则同飞书。钉钉应用凭证由组织管理员在页面里绑定。
	DingTalkAPIURL   string
	DingTalkOAPIURL  string
	DingTalkLoginURL string
	// WeChatILinkBaseURL 是腾讯 ClawBot（iLink）接口基址，默认 https://ilinkai.weixin.qq.com；
	// production 必须是 https，development / test 允许 http（本机模拟服务）。微信绑定不需要 AppID / AppSecret。
	WeChatILinkBaseURL string
}

const (
	defaultFeishuBaseURL     = "https://open.feishu.cn"
	defaultFeishuAccountsURL = "https://accounts.feishu.cn"
	defaultLarkBaseURL       = "https://open.larksuite.com"
	defaultLarkAccountsURL   = "https://accounts.larksuite.com"

	defaultDingTalkAPIURL   = "https://api.dingtalk.com"
	defaultDingTalkOAPIURL  = "https://oapi.dingtalk.com"
	defaultDingTalkLoginURL = "https://login.dingtalk.com"

	defaultWeChatILinkBaseURL = "https://ilinkai.weixin.qq.com"
)

// WeChatILinkURL 返回 iLink 基址；为空时使用默认值。
func (c IntegrationsConfig) WeChatILinkURL() string {
	if base := strings.TrimRight(c.WeChatILinkBaseURL, "/"); base != "" {
		return base
	}
	return defaultWeChatILinkBaseURL
}

// FeishuURLs 返回飞书开放平台 API 与账号页地址；为空时使用默认值。
func (c IntegrationsConfig) FeishuURLs() (baseURL, accountsURL string) {
	baseURL, accountsURL = strings.TrimRight(c.FeishuBaseURL, "/"), strings.TrimRight(c.FeishuAccountsURL, "/")
	if baseURL == "" {
		baseURL = defaultFeishuBaseURL
	}
	if accountsURL == "" {
		accountsURL = defaultFeishuAccountsURL
	}
	return baseURL, accountsURL
}

// LarkURLs 返回 Lark 开放平台 API 与账号页地址；为空时使用默认值。
func (c IntegrationsConfig) LarkURLs() (baseURL, accountsURL string) {
	baseURL, accountsURL = strings.TrimRight(c.LarkBaseURL, "/"), strings.TrimRight(c.LarkAccountsURL, "/")
	if baseURL == "" {
		baseURL = defaultLarkBaseURL
	}
	if accountsURL == "" {
		accountsURL = defaultLarkAccountsURL
	}
	return baseURL, accountsURL
}

// DingTalkURLs 返回钉钉新版接口、旧版接口与网页登录授权页地址；为空时使用默认值。
func (c IntegrationsConfig) DingTalkURLs() (apiURL, oapiURL, loginURL string) {
	apiURL, oapiURL, loginURL = strings.TrimRight(c.DingTalkAPIURL, "/"), strings.TrimRight(c.DingTalkOAPIURL, "/"), strings.TrimRight(c.DingTalkLoginURL, "/")
	if apiURL == "" {
		apiURL = defaultDingTalkAPIURL
	}
	if oapiURL == "" {
		oapiURL = defaultDingTalkOAPIURL
	}
	if loginURL == "" {
		loginURL = defaultDingTalkLoginURL
	}
	return apiURL, oapiURL, loginURL
}

// GitHubSettings 是 GitHub 集成运行时需要的完整配置。
type GitHubSettings struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	APIURL       string
	Configured   bool
}

const (
	defaultGitHubBaseURL = "https://github.com"
	defaultGitHubAPIURL  = "https://api.github.com"
)

// GitHub 返回 GitHub 集成配置；地址为空时使用默认值。
func (c IntegrationsConfig) GitHub() GitHubSettings {
	base, api := strings.TrimRight(c.GitHubBaseURL, "/"), strings.TrimRight(c.GitHubAPIURL, "/")
	if base == "" {
		base = defaultGitHubBaseURL
	}
	if api == "" {
		api = defaultGitHubAPIURL
	}
	return GitHubSettings{ClientID: c.GitHubClientID, ClientSecret: c.GitHubClientSecret, BaseURL: base, APIURL: api,
		Configured: c.GitHubConfigured()}
}

// GiteeSettings 是 Gitee 集成运行时需要的完整配置。
type GiteeSettings struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	APIURL       string
	Configured   bool
}

const (
	defaultGiteeBaseURL = "https://gitee.com"
	defaultGiteeAPIURL  = "https://gitee.com/api/v5"
)

// Gitee 返回 Gitee 集成配置；地址为空时使用默认值。
func (c IntegrationsConfig) Gitee() GiteeSettings {
	base, api := strings.TrimRight(c.GiteeBaseURL, "/"), strings.TrimRight(c.GiteeAPIURL, "/")
	if base == "" {
		base = defaultGiteeBaseURL
	}
	if api == "" {
		api = defaultGiteeAPIURL
	}
	return GiteeSettings{ClientID: c.GiteeClientID, ClientSecret: c.GiteeClientSecret, BaseURL: base, APIURL: api, Configured: c.GiteeConfigured()}
}

// GiteeConfigured 表示 Gitee 第三方应用已配置。
func (c IntegrationsConfig) GiteeConfigured() bool {
	return c.GiteeClientID != "" && c.GiteeClientSecretFile != ""
}

// GitHubConfigured 表示 GitHub OAuth App 已配置。
func (c IntegrationsConfig) GitHubConfigured() bool {
	return c.GitHubClientID != "" && c.GitHubClientSecretFile != ""
}

type ServerConfig struct {
	Listen    string
	PublicURL string
	// TrustedProxies 是允许提供 X-Forwarded-For 的直连代理地址（CIDR 或 IP，逗号分隔）。
	TrustedProxies string
}

type DatabaseConfig struct {
	URLFile          string
	PoolMax          int
	ContextRole      string
	AppRole          string
	StatementTimeout time.Duration
	LockTimeout      time.Duration
}

type AuthConfig struct {
	Argon2MemoryKiB   uint32
	Argon2Iterations  uint32
	Argon2Parallelism uint8
	Argon2KeyLength   uint32
	SessionIdle       time.Duration
	SessionAbsolute   time.Duration
	// 每个客户端 IP 在 RateLimitWindow 内允许的邮箱验证码请求 / 校验次数；0 表示关闭。
	EmailRequestPerIP int
	EmailVerifyPerIP  int
	RateLimitWindow   time.Duration
	// 同一邮箱 24 小时内允许签发的验证码数与累计校验失败次数；用于阻断长期暴力破解与发信轰炸。
	EmailDailySends    int
	EmailDailyFailures int
}

type EmailConfig struct {
	SMTP SMTPConfig
	OTP  EmailOTPConfig
}

type SMTPConfig struct {
	Host     string
	Port     int
	Security string
	User     string
	From     string
	Password string
	Timeout  time.Duration
}

type EmailOTPConfig struct {
	ChallengeTTL   time.Duration
	ResendCooldown time.Duration
	MaxAttempts    int
	UniversalCode  string
}

type SchedulerConfig struct {
	OfferTTL time.Duration
	// 配置了数据库的中心进程总会运行派发 worker；多实例可同时运行（阶段事务按组织串行化）。
	PollInterval time.Duration
	BatchSize    int
	// LeaseTTL / LeaseRenew 是启动许可租约与设备续租周期，要求 LeaseTTL >= 3 * LeaseRenew。
	LeaseTTL   time.Duration
	LeaseRenew time.Duration
}

type StorageConfig struct {
	Root string
}

func Load() (Config, error) {
	cfg := Config{
		Environment: envOrDefault("OPENFOX_ENV", EnvironmentDevelopment),
		Server: ServerConfig{
			Listen:         envOrDefault("OPENFOX_SERVER_LISTEN", "127.0.0.1:8080"),
			PublicURL:      envOrDefault("OPENFOX_SERVER_PUBLIC_URL", "http://127.0.0.1:8080"),
			TrustedProxies: os.Getenv("OPENFOX_SERVER_TRUSTED_PROXIES"),
		},
		Database: DatabaseConfig{
			URLFile:          os.Getenv("OPENFOX_DATABASE_URL_FILE"),
			PoolMax:          intOrDefault("OPENFOX_DATABASE_POOL_MAX", 20),
			ContextRole:      envOrDefault("OPENFOX_DATABASE_CONTEXT_ROLE", "openfox_context"),
			AppRole:          envOrDefault("OPENFOX_DATABASE_APP_ROLE", "openfox_app"),
			StatementTimeout: durationOrDefault("OPENFOX_DATABASE_STATEMENT_TIMEOUT", 3*time.Second),
			LockTimeout:      durationOrDefault("OPENFOX_DATABASE_LOCK_TIMEOUT", time.Second),
		},
		Auth: AuthConfig{
			Argon2MemoryKiB:    uint32(intOrDefault("OPENFOX_AUTH_ARGON2_MEMORY_KIB", 65536)),
			Argon2Iterations:   uint32(intOrDefault("OPENFOX_AUTH_ARGON2_ITERATIONS", 3)),
			Argon2Parallelism:  uint8(intOrDefault("OPENFOX_AUTH_ARGON2_PARALLELISM", 1)),
			Argon2KeyLength:    uint32(intOrDefault("OPENFOX_AUTH_ARGON2_KEY_LENGTH", 32)),
			SessionIdle:        durationOrDefault("OPENFOX_AUTH_SESSION_IDLE", 12*time.Hour),
			SessionAbsolute:    durationOrDefault("OPENFOX_AUTH_SESSION_ABSOLUTE", 7*24*time.Hour),
			EmailRequestPerIP:  intOrDefault("OPENFOX_AUTH_EMAIL_REQUEST_PER_IP", 10),
			EmailDailySends:    intOrDefault("OPENFOX_AUTH_EMAIL_DAILY_SENDS", 10),
			EmailDailyFailures: intOrDefault("OPENFOX_AUTH_EMAIL_DAILY_FAILURES", 20),
			EmailVerifyPerIP:   intOrDefault("OPENFOX_AUTH_EMAIL_VERIFY_PER_IP", 30),
			RateLimitWindow:    durationOrDefault("OPENFOX_AUTH_RATE_LIMIT_WINDOW", 10*time.Minute),
		},
		Email: EmailConfig{
			SMTP: SMTPConfig{
				Host:     os.Getenv("SMTP_HOST"),
				Port:     intOrDefault("SMTP_PORT", 0),
				Security: normalizeSMTPSecurity(os.Getenv("SMTP_SECURITY")),
				User:     os.Getenv("SMTP_USER"),
				From:     os.Getenv("SMTP_FROM"),
				Password: os.Getenv("SMTP_PASSWORD"),
				Timeout:  durationOrDefault("SMTP_TIMEOUT", 10*time.Second),
			},
			OTP: EmailOTPConfig{
				ChallengeTTL:   durationOrDefault("OPENFOX_AUTH_EMAIL_OTP_TTL", 10*time.Minute),
				ResendCooldown: durationOrDefault("OPENFOX_AUTH_EMAIL_OTP_RESEND_COOLDOWN", time.Minute),
				MaxAttempts:    intOrDefault("OPENFOX_AUTH_EMAIL_OTP_MAX_ATTEMPTS", 5),
				UniversalCode:  strings.TrimSpace(os.Getenv("OPENFOX_AUTH_EMAIL_OTP_UNIVERSAL_CODE")),
			},
		},
		Scheduler: SchedulerConfig{
			OfferTTL:     durationOrDefault("OPENFOX_SCHEDULER_OFFER_TTL", 15*time.Second),
			PollInterval: durationOrDefault("OPENFOX_SCHEDULER_POLL_INTERVAL", time.Second),
			BatchSize:    intOrDefault("OPENFOX_SCHEDULER_BATCH", 50),
			LeaseTTL:     durationOrDefault("OPENFOX_RUN_LEASE_TTL", 90*time.Second),
			LeaseRenew:   durationOrDefault("OPENFOX_RUN_LEASE_RENEW", 20*time.Second),
		},
		Storage: StorageConfig{
			Root: os.Getenv("OPENFOX_STORAGE_ROOT"),
		},
		Secrets: SecretsConfig{
			KeyFile: strings.TrimSpace(os.Getenv("OPENFOX_SECRETS_KEY_FILE")),
		},
		Integrations: IntegrationsConfig{
			GitHubClientID:         strings.TrimSpace(os.Getenv("OPENFOX_GITHUB_CLIENT_ID")),
			GitHubClientSecretFile: strings.TrimSpace(os.Getenv("OPENFOX_GITHUB_CLIENT_SECRET_FILE")),
			GitHubBaseURL:          strings.TrimSpace(os.Getenv("OPENFOX_GITHUB_BASE_URL")),
			GitHubAPIURL:           strings.TrimSpace(os.Getenv("OPENFOX_GITHUB_API_URL")),
			GiteeClientID:          strings.TrimSpace(os.Getenv("OPENFOX_GITEE_CLIENT_ID")),
			GiteeClientSecretFile:  strings.TrimSpace(os.Getenv("OPENFOX_GITEE_CLIENT_SECRET_FILE")),
			GiteeBaseURL:           strings.TrimSpace(os.Getenv("OPENFOX_GITEE_BASE_URL")),
			GiteeAPIURL:            strings.TrimSpace(os.Getenv("OPENFOX_GITEE_API_URL")),
			FeishuBaseURL:          strings.TrimSpace(os.Getenv("OPENFOX_FEISHU_BASE_URL")),
			FeishuAccountsURL:      strings.TrimSpace(os.Getenv("OPENFOX_FEISHU_ACCOUNTS_URL")),
			LarkBaseURL:            strings.TrimSpace(os.Getenv("OPENFOX_LARK_BASE_URL")),
			LarkAccountsURL:        strings.TrimSpace(os.Getenv("OPENFOX_LARK_ACCOUNTS_URL")),
			DingTalkAPIURL:         strings.TrimSpace(os.Getenv("OPENFOX_DINGTALK_API_URL")),
			DingTalkOAPIURL:        strings.TrimSpace(os.Getenv("OPENFOX_DINGTALK_OAPI_URL")),
			DingTalkLoginURL:       strings.TrimSpace(os.Getenv("OPENFOX_DINGTALK_LOGIN_URL")),
			WeChatILinkBaseURL:     strings.TrimSpace(os.Getenv("OPENFOX_WECHAT_ILINK_BASE_URL")),
		},
	}
	if invalid := invalidIntegerEnv(integerEnvKeys...); len(invalid) > 0 {
		return Config{}, fmt.Errorf("invalid integer value for %s", strings.Join(invalid, ", "))
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	if cfg.Integrations.GitHubClientSecretFile != "" {
		secret, err := readSecretFile(cfg.Integrations.GitHubClientSecretFile)
		if err != nil {
			return Config{}, fmt.Errorf("integrations.github_client_secret_file: %w", err)
		}
		cfg.Integrations.GitHubClientSecret = secret
	}
	if cfg.Integrations.GiteeClientSecretFile != "" {
		secret, err := readSecretFile(cfg.Integrations.GiteeClientSecretFile)
		if err != nil {
			return Config{}, fmt.Errorf("integrations.gitee_client_secret_file: %w", err)
		}
		cfg.Integrations.GiteeClientSecret = secret
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if c.Environment != EnvironmentDevelopment && c.Environment != EnvironmentTest && c.Environment != EnvironmentProduction {
		return fmt.Errorf("OPENFOX_ENV must be development, test, or production")
	}
	if err := validateListen(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	if err := validatePublicURL(c.Environment, c.Server.PublicURL); err != nil {
		return fmt.Errorf("server.public_url: %w", err)
	}
	if c.Environment == EnvironmentProduction && strings.TrimSpace(c.Database.URLFile) == "" {
		return errors.New("database.url_file is required in production")
	}
	if c.Environment == EnvironmentProduction {
		if c.Secrets.KeyFile == "" {
			return errors.New("secrets.key_file (OPENFOX_SECRETS_KEY_FILE) is required in production")
		}
		if !filepath.IsAbs(c.Secrets.KeyFile) {
			return errors.New("secrets.key_file must be an absolute path in production")
		}
		storageRoot := strings.TrimSpace(c.Storage.Root)
		if storageRoot == "" {
			return errors.New("storage.root is required in production")
		}
		if !filepath.IsAbs(storageRoot) {
			return errors.New("storage.root must be an absolute path in production")
		}
	}
	if c.Database.PoolMax < 2 || c.Database.PoolMax > 100 {
		return errors.New("database.pool_max must be between 2 and 100")
	}
	if err := validateIdentifier(c.Database.ContextRole); err != nil {
		return fmt.Errorf("database.context_role: %w", err)
	}
	if err := validateIdentifier(c.Database.AppRole); err != nil {
		return fmt.Errorf("database.app_role: %w", err)
	}
	if c.Database.StatementTimeout <= 0 {
		return errors.New("database.statement_timeout must be positive")
	}
	if c.Database.LockTimeout <= 0 || c.Database.LockTimeout > c.Database.StatementTimeout {
		return errors.New("database.lock_timeout must be positive and no greater than statement_timeout")
	}
	if c.Auth.Argon2MemoryKiB < 16*1024 || c.Auth.Argon2MemoryKiB > 1024*1024 {
		return errors.New("auth.argon2_memory_kib must be between 16384 and 1048576")
	}
	if c.Auth.Argon2Iterations < 1 || c.Auth.Argon2Iterations > 10 {
		return errors.New("auth.argon2_iterations must be between 1 and 10")
	}
	if c.Auth.Argon2Parallelism < 1 || c.Auth.Argon2Parallelism > 16 {
		return errors.New("auth.argon2_parallelism must be between 1 and 16")
	}
	if c.Auth.Argon2KeyLength < 16 || c.Auth.Argon2KeyLength > 64 {
		return errors.New("auth.argon2_key_length must be between 16 and 64")
	}
	if c.Auth.SessionIdle <= 0 || c.Auth.SessionAbsolute <= 0 || c.Auth.SessionIdle > c.Auth.SessionAbsolute {
		return errors.New("auth session durations must be positive and idle must not exceed absolute")
	}
	if c.Auth.EmailRequestPerIP < 0 || c.Auth.EmailRequestPerIP > 10000 || c.Auth.EmailVerifyPerIP < 0 || c.Auth.EmailVerifyPerIP > 10000 {
		return errors.New("auth email per-ip limits must be between 0 and 10000")
	}
	// 零值视为未设置（使用默认值），按邮箱的日限额不允许关闭。
	if c.Auth.EmailDailySends < 0 || c.Auth.EmailDailySends > 1000 || c.Auth.EmailDailyFailures < 0 || c.Auth.EmailDailyFailures > 1000 {
		return errors.New("auth email daily limits must be between 1 and 1000")
	}
	// 未设置（零值）时由调用方使用默认窗口，与 OTP TTL 的零值语义一致。
	if c.Auth.RateLimitWindow != 0 && (c.Auth.RateLimitWindow < time.Second || c.Auth.RateLimitWindow > 24*time.Hour) {
		return errors.New("auth.rate_limit_window must be between 1s and 24h")
	}
	if err := validateTrustedProxies(c.Server.TrustedProxies); err != nil {
		return fmt.Errorf("server.trusted_proxies: %w", err)
	}
	if err := validateEmail(c); err != nil {
		return err
	}
	if err := validateIntegrations(c.Integrations); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.github_base_url", c.Integrations.GitHubBaseURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.github_api_url", c.Integrations.GitHubAPIURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.gitee_base_url", c.Integrations.GiteeBaseURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.gitee_api_url", c.Integrations.GiteeAPIURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.feishu_base_url", c.Integrations.FeishuBaseURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.feishu_accounts_url", c.Integrations.FeishuAccountsURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.lark_base_url", c.Integrations.LarkBaseURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.lark_accounts_url", c.Integrations.LarkAccountsURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.dingtalk_api_url", c.Integrations.DingTalkAPIURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.dingtalk_oapi_url", c.Integrations.DingTalkOAPIURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.dingtalk_login_url", c.Integrations.DingTalkLoginURL); err != nil {
		return err
	}
	if err := validateGitHubURL(c.Environment, "integrations.wechat_ilink_base_url", c.Integrations.WeChatILinkBaseURL); err != nil {
		return err
	}
	if c.Scheduler.OfferTTL < 5*time.Second || c.Scheduler.OfferTTL > 60*time.Second {
		return errors.New("scheduler.offer_ttl must be between 5s and 60s")
	}
	if err := validateScheduler(c.Scheduler); err != nil {
		return err
	}
	return nil
}

// validateIntegrations 只校验已设置的项；两项必须同时设置或同时留空，避免半配置被误认为可用。
func validateIntegrations(c IntegrationsConfig) error {
	if err := validateOAuthApp("github", "GITHUB", c.GitHubClientID, c.GitHubClientSecretFile); err != nil {
		return err
	}
	return validateOAuthApp("gitee", "GITEE", c.GiteeClientID, c.GiteeClientSecretFile)
}

func validateOAuthApp(name, envName, clientID, secretFile string) error {
	if (clientID == "") != (secretFile == "") {
		return fmt.Errorf("integrations.%s: OPENFOX_%s_CLIENT_ID and OPENFOX_%s_CLIENT_SECRET_FILE must be set together", name, envName, envName)
	}
	if clientID == "" {
		return nil
	}
	if len(clientID) > 255 || strings.ContainsAny(clientID, " \t\r\n") {
		return fmt.Errorf("integrations.%s_client_id is invalid", name)
	}
	if !filepath.IsAbs(secretFile) {
		return fmt.Errorf("integrations.%s_client_secret_file must be an absolute path", name)
	}
	return nil
}

// validateScheduler 只在字段被设置（非零）时校验，兼容只设置 OfferTTL 的旧调用方；
// Load 总会填入默认值，因此运行时配置一定经过完整校验。
func validateScheduler(c SchedulerConfig) error {
	if c.PollInterval == 0 && c.BatchSize == 0 && c.LeaseTTL == 0 && c.LeaseRenew == 0 {
		return nil
	}
	if c.PollInterval < 100*time.Millisecond || c.PollInterval > time.Minute {
		return errors.New("scheduler.poll_interval must be between 100ms and 1m")
	}
	if c.BatchSize < 1 || c.BatchSize > 500 {
		return errors.New("scheduler.batch must be between 1 and 500")
	}
	if c.LeaseRenew < 5*time.Second || c.LeaseRenew > 5*time.Minute {
		return errors.New("run.lease_renew must be between 5s and 5m")
	}
	if c.LeaseTTL < 3*c.LeaseRenew || c.LeaseTTL > time.Hour {
		return errors.New("run.lease_ttl must be at least 3x run.lease_renew and at most 1h")
	}
	return nil
}

func validateTrustedProxies(value string) error {
	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if strings.Contains(item, "/") {
			if _, _, err := net.ParseCIDR(item); err != nil {
				return fmt.Errorf("invalid CIDR %q", item)
			}
			continue
		}
		if net.ParseIP(item) == nil {
			return fmt.Errorf("invalid IP %q", item)
		}
	}
	return nil
}

func validateEmail(c Config) error {
	smtp := c.Email.SMTP
	smtpConfigured := strings.TrimSpace(smtp.Host) != "" || smtp.Port != 0 || strings.TrimSpace(smtp.Security) != "" || strings.TrimSpace(smtp.User) != "" || strings.TrimSpace(smtp.From) != "" || smtp.Password != ""
	if smtpConfigured {
		if strings.TrimSpace(smtp.Host) == "" || smtp.Port < 1 || smtp.Port > 65535 || strings.TrimSpace(smtp.User) == "" || strings.TrimSpace(smtp.From) == "" || smtp.Password == "" {
			return errors.New("smtp configuration is incomplete")
		}
		if smtp.Security != "ssl_tls" && smtp.Security != "starttls" {
			return errors.New("smtp security must be SSL/TLS or STARTTLS")
		}
		if strings.ContainsAny(smtp.Host+smtp.User+smtp.From, "\r\n") {
			return errors.New("smtp configuration contains a newline")
		}
	}
	if smtpConfigured && (smtp.Timeout <= 0 || smtp.Timeout > 2*time.Minute) {
		return errors.New("smtp timeout must be between 1ns and 2m")
	}
	challengeTTL := c.Email.OTP.ChallengeTTL
	if challengeTTL == 0 {
		challengeTTL = 10 * time.Minute
	}
	if challengeTTL < time.Minute || challengeTTL > time.Hour {
		return errors.New("email otp challenge ttl must be between 1m and 1h")
	}
	resendCooldown := c.Email.OTP.ResendCooldown
	if resendCooldown < 0 || resendCooldown > time.Hour {
		return errors.New("email otp resend cooldown must be between 0 and 1h")
	}
	maxAttempts := c.Email.OTP.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 5
	}
	if maxAttempts < 1 || maxAttempts > 20 {
		return errors.New("email otp max attempts must be between 1 and 20")
	}
	if c.Email.OTP.UniversalCode != "" {
		if c.Environment == EnvironmentProduction {
			return errors.New("email otp universal code is forbidden in production")
		}
		if len(c.Email.OTP.UniversalCode) != 6 {
			return errors.New("email otp universal code must contain exactly 6 digits")
		}
		for _, character := range c.Email.OTP.UniversalCode {
			if character < '0' || character > '9' {
				return errors.New("email otp universal code must contain exactly 6 digits")
			}
		}
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func intOrDefault(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// invalidIntegerEnv 列出取值无法解析为整数的配置键。非法值不能静默变成 0
// （对限流类配置，0 表示关闭），Load 据此拒绝启动。
func invalidIntegerEnv(keys ...string) []string {
	var invalid []string
	for _, key := range keys {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		if _, err := strconv.Atoi(value); err != nil {
			invalid = append(invalid, key)
		}
	}
	return invalid
}

var integerEnvKeys = []string{
	"OPENFOX_DATABASE_POOL_MAX", "OPENFOX_AUTH_ARGON2_MEMORY_KIB", "OPENFOX_AUTH_ARGON2_ITERATIONS",
	"OPENFOX_AUTH_ARGON2_PARALLELISM", "OPENFOX_AUTH_ARGON2_KEY_LENGTH", "OPENFOX_AUTH_EMAIL_REQUEST_PER_IP",
	"OPENFOX_AUTH_EMAIL_VERIFY_PER_IP", "OPENFOX_AUTH_EMAIL_DAILY_SENDS", "OPENFOX_AUTH_EMAIL_DAILY_FAILURES",
	"SMTP_PORT", "OPENFOX_AUTH_EMAIL_OTP_MAX_ATTEMPTS", "OPENFOX_SCHEDULER_BATCH",
}

func durationOrDefault(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}
	return parsed
}

func normalizeSMTPSecurity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ssl/tls", "ssl_tls", "ssl-tls":
		return "ssl_tls"
	case "starttls", "start_tls", "start-tls":
		return "starttls"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func validateListen(listen string) error {
	if strings.TrimSpace(listen) == "" {
		return errors.New("must not be empty")
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return errors.New("must be host:port")
	}
	return nil
}

func validatePublicURL(environment, value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("must be an absolute URL")
	}
	if environment == EnvironmentDevelopment || environment == EnvironmentTest {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("must use http or https")
		}
		return nil
	}
	if parsed.Scheme != "https" {
		return errors.New("must use https outside development and test")
	}
	return nil
}

func validateIdentifier(value string) error {
	if value == "" {
		return errors.New("must not be empty")
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			if index == 0 && character >= '0' && character <= '9' {
				return errors.New("must be a lowercase SQL identifier")
			}
			continue
		}
		return errors.New("must be a lowercase SQL identifier")
	}
	return nil
}

// validateGitHubURL 校验 GitHub 地址：为空表示使用默认值；production 必须是 https，其他环境允许 http（本机模拟服务）。
func validateGitHubURL(environment, name, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s is invalid", name)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if environment == EnvironmentProduction {
			return fmt.Errorf("%s must use https in production", name)
		}
	default:
		return fmt.Errorf("%s must be an http(s) URL", name)
	}
	return nil
}

// readSecretFile 读取单值密钥文件：必须是普通文件且不对组和其他用户开放，内容去掉首尾空白，不能为空。
func readSecretFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("cannot be read")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("must not be readable by group or others")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot be read")
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || len(value) > 512 {
		return "", errors.New("content is empty or too long")
	}
	return value, nil
}
