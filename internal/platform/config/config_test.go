package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesSafeDevelopmentDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Fatalf("listen = %q", cfg.Server.Listen)
	}
	if cfg.Database.PoolMax != 20 {
		t.Fatalf("pool max = %d", cfg.Database.PoolMax)
	}
	if cfg.Scheduler.OfferTTL != 15*time.Second {
		t.Fatalf("offer ttl = %s", cfg.Scheduler.OfferTTL)
	}
	if cfg.Database.ContextRole != "openfox_context" || cfg.Database.AppRole != "openfox_app" {
		t.Fatalf("database roles = %q/%q", cfg.Database.ContextRole, cfg.Database.AppRole)
	}
	if cfg.Auth.SessionIdle != 12*time.Hour || cfg.Auth.SessionAbsolute != 7*24*time.Hour {
		t.Fatalf("auth session durations = %s/%s", cfg.Auth.SessionIdle, cfg.Auth.SessionAbsolute)
	}
}

func TestValidateRejectsInsecureProductionURL(t *testing.T) {
	cfg := Config{
		Environment: EnvironmentProduction,
		Server:      ServerConfig{Listen: "127.0.0.1:8080", PublicURL: "http://127.0.0.1:8080"},
		Database:    DatabaseConfig{URLFile: "/run/openfox/database.url", PoolMax: 20, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 3 * time.Second, LockTimeout: time.Second},
		Scheduler:   SchedulerConfig{OfferTTL: 15 * time.Second},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() unexpectedly accepted an insecure production URL")
	}
}

func TestValidateRejectsInvalidDurationValue(t *testing.T) {
	t.Setenv("OPENFOX_DATABASE_STATEMENT_TIMEOUT", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("Load() unexpectedly accepted an invalid duration")
	}
}

func TestValidateRequiresPersistentAbsoluteStorageInProduction(t *testing.T) {
	cfg := Config{
		Environment: EnvironmentProduction,
		Server:      ServerConfig{Listen: "127.0.0.1:8080", PublicURL: "https://openfox.example"},
		Database:    DatabaseConfig{URLFile: "/run/openfox/database.url", PoolMax: 20, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 3 * time.Second, LockTimeout: time.Second},
		Auth:        AuthConfig{Argon2MemoryKiB: 65536, Argon2Iterations: 3, Argon2Parallelism: 1, Argon2KeyLength: 32, SessionIdle: 12 * time.Hour, SessionAbsolute: 168 * time.Hour},
		Scheduler:   SchedulerConfig{OfferTTL: 15 * time.Second},
		Secrets:     SecretsConfig{KeyFile: "/run/openfox/secrets.key"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted production config without persistent storage")
	}
	cfg.Storage.Root = "./var/openfox"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted relative production storage path")
	}
	cfg.Storage.Root = "/var/lib/openfox"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected absolute production storage path: %v", err)
	}
	// 生产环境必须配置绝对路径的加密主密钥文件。
	cfg.Secrets.KeyFile = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted production config without secrets key file")
	}
	cfg.Secrets.KeyFile = "secrets.key"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted relative secrets key file")
	}
}

func TestLoadNormalizesSMTPAndAcceptsUniversalCodeOutsideProduction(t *testing.T) {
	t.Setenv("OPENFOX_ENV", EnvironmentTest)
	t.Setenv("SMTP_HOST", "smtp.example.test")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_SECURITY", "SSL/TLS")
	t.Setenv("SMTP_USER", "sender@example.test")
	t.Setenv("SMTP_FROM", "sender@example.test")
	t.Setenv("SMTP_PASSWORD", "test-only-password")
	t.Setenv("OPENFOX_AUTH_EMAIL_OTP_UNIVERSAL_CODE", "666666")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Email.SMTP.Security != "ssl_tls" || cfg.Email.SMTP.Port != 465 || cfg.Email.OTP.UniversalCode != "666666" {
		t.Fatalf("email config = %#v", cfg.Email)
	}
}

func TestValidateRejectsUniversalCodeInProduction(t *testing.T) {
	cfg := Config{
		Environment: EnvironmentProduction,
		Server:      ServerConfig{Listen: "127.0.0.1:8080", PublicURL: "https://openfox.example"},
		Database:    DatabaseConfig{URLFile: "/run/openfox/database.url", PoolMax: 20, ContextRole: "openfox_context", AppRole: "openfox_app", StatementTimeout: 3 * time.Second, LockTimeout: time.Second},
		Auth:        AuthConfig{Argon2MemoryKiB: 65536, Argon2Iterations: 3, Argon2Parallelism: 1, Argon2KeyLength: 32, SessionIdle: 12 * time.Hour, SessionAbsolute: 168 * time.Hour},
		Email:       EmailConfig{OTP: EmailOTPConfig{UniversalCode: "666666"}},
		Scheduler:   SchedulerConfig{OfferTTL: 15 * time.Second},
		Storage:     StorageConfig{Root: "/var/lib/openfox"},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() accepted a universal email code in production")
	}
}

func TestLoadAuthRateLimitDefaultsAndValidation(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.EmailRequestPerIP != 10 || cfg.Auth.EmailVerifyPerIP != 30 || cfg.Auth.RateLimitWindow != 10*time.Minute {
		t.Fatalf("rate limit defaults = %d/%d/%s", cfg.Auth.EmailRequestPerIP, cfg.Auth.EmailVerifyPerIP, cfg.Auth.RateLimitWindow)
	}
	t.Setenv("OPENFOX_SERVER_TRUSTED_PROXIES", "10.0.0.0/8, 127.0.0.1")
	if _, err := Load(); err != nil {
		t.Fatalf("valid trusted proxies rejected: %v", err)
	}
	t.Setenv("OPENFOX_SERVER_TRUSTED_PROXIES", "not-an-ip")
	if _, err := Load(); err == nil {
		t.Fatal("invalid trusted proxy accepted")
	}
	t.Setenv("OPENFOX_SERVER_TRUSTED_PROXIES", "")
	t.Setenv("OPENFOX_AUTH_EMAIL_REQUEST_PER_IP", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("negative per-ip limit accepted")
	}
	t.Setenv("OPENFOX_AUTH_EMAIL_REQUEST_PER_IP", "0")
	t.Setenv("OPENFOX_AUTH_RATE_LIMIT_WINDOW", "48h")
	if _, err := Load(); err == nil {
		t.Fatal("oversized rate limit window accepted")
	}
}

func TestLoadRejectsMalformedIntegerLimitsAndHasDailyDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.EmailDailySends != 10 || cfg.Auth.EmailDailyFailures != 20 {
		t.Fatalf("daily defaults = %d/%d", cfg.Auth.EmailDailySends, cfg.Auth.EmailDailyFailures)
	}
	// 非整数不能静默变成 0（0 会关闭 IP 限流）。
	t.Setenv("OPENFOX_AUTH_EMAIL_REQUEST_PER_IP", "10/10m")
	if _, err := Load(); err == nil {
		t.Fatal("malformed per-ip limit accepted")
	}
	t.Setenv("OPENFOX_AUTH_EMAIL_REQUEST_PER_IP", "")
	t.Setenv("OPENFOX_AUTH_EMAIL_DAILY_FAILURES", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("negative daily failure limit accepted")
	}
}

func TestLoadSchedulerDefaultsAndValidation(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Scheduler.PollInterval != time.Second || cfg.Scheduler.BatchSize != 50 ||
		cfg.Scheduler.LeaseTTL != 90*time.Second || cfg.Scheduler.LeaseRenew != 20*time.Second {
		t.Fatalf("scheduler defaults = %+v", cfg.Scheduler)
	}

	t.Setenv("OPENFOX_RUN_LEASE_TTL", "60s")
	t.Setenv("OPENFOX_RUN_LEASE_RENEW", "15s")
	cfg, err = Load()
	if err != nil || cfg.Scheduler.LeaseTTL != time.Minute {
		t.Fatalf("scheduler = %+v, err = %v", cfg.Scheduler, err)
	}

	// 租约必须至少覆盖 3 个续租周期。
	t.Setenv("OPENFOX_RUN_LEASE_TTL", "40s")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted lease ttl shorter than 3x renew")
	}
	t.Setenv("OPENFOX_RUN_LEASE_TTL", "90s")
	t.Setenv("OPENFOX_SCHEDULER_BATCH", "0")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted scheduler batch 0")
	}
	t.Setenv("OPENFOX_SCHEDULER_BATCH", "many")
	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-integer scheduler batch")
	}
}

func TestValidateIntegrationsGitHubConfig(t *testing.T) {
	cfg := Config{Environment: EnvironmentDevelopment}
	if cfg.Integrations.GitHubConfigured() {
		t.Fatal("empty config must not be configured")
	}
	cases := []struct {
		name string
		in   IntegrationsConfig
		ok   bool
		conf bool
	}{
		{"empty", IntegrationsConfig{}, true, false},
		{"both", IntegrationsConfig{GitHubClientID: "Iv1.abc", GitHubClientSecretFile: "/etc/openfox/github-secret"}, true, true},
		{"only id", IntegrationsConfig{GitHubClientID: "Iv1.abc"}, false, false},
		{"only file", IntegrationsConfig{GitHubClientSecretFile: "/etc/x"}, false, false},
		{"relative file", IntegrationsConfig{GitHubClientID: "Iv1.abc", GitHubClientSecretFile: "secret"}, false, true},
		{"id with space", IntegrationsConfig{GitHubClientID: "a b", GitHubClientSecretFile: "/etc/x"}, false, true},
	}
	for _, c := range cases {
		err := validateIntegrations(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
		if c.in.GitHubConfigured() != c.conf {
			t.Errorf("%s: configured = %v, want %v", c.name, c.in.GitHubConfigured(), c.conf)
		}
	}
}

func TestValidateGitHubURLs(t *testing.T) {
	cases := []struct {
		env, value string
		ok         bool
	}{
		{EnvironmentProduction, "", true},
		{EnvironmentProduction, "https://github.example.com", true},
		{EnvironmentProduction, "http://github.example.com", false},
		{EnvironmentDevelopment, "http://127.0.0.1:18096", true},
		{EnvironmentTest, "http://127.0.0.1:18096", true},
		{EnvironmentDevelopment, "ftp://x", false},
		{EnvironmentDevelopment, "https://user:pw@x", false},
		{EnvironmentDevelopment, "not a url", false},
	}
	for _, c := range cases {
		if err := validateGitHubURL(c.env, "x", c.value); (err == nil) != c.ok {
			t.Errorf("%s %q: err = %v, want ok=%v", c.env, c.value, err, c.ok)
		}
	}
}

func TestGitHubSettingsDefaults(t *testing.T) {
	got := IntegrationsConfig{GitHubClientID: "id", GitHubClientSecretFile: "/x", GitHubClientSecret: "s",
		GitHubBaseURL: "http://127.0.0.1:1/"}.GitHub()
	if got.BaseURL != "http://127.0.0.1:1" || got.APIURL != "https://api.github.com" || !got.Configured || got.ClientSecret != "s" {
		t.Fatalf("settings = %+v", got)
	}
	if empty := (IntegrationsConfig{}).GitHub(); empty.BaseURL != "https://github.com" || empty.Configured {
		t.Fatalf("empty settings = %+v", empty)
	}
}

func TestValidateIntegrationsGiteeConfig(t *testing.T) {
	cases := []struct {
		name string
		in   IntegrationsConfig
		ok   bool
		conf bool
	}{
		{"empty", IntegrationsConfig{}, true, false},
		{"both", IntegrationsConfig{GiteeClientID: "abc", GiteeClientSecretFile: "/etc/openfox/gitee-secret"}, true, true},
		{"only id", IntegrationsConfig{GiteeClientID: "abc"}, false, false},
		{"only file", IntegrationsConfig{GiteeClientSecretFile: "/etc/x"}, false, false},
		{"relative file", IntegrationsConfig{GiteeClientID: "abc", GiteeClientSecretFile: "secret"}, false, true},
		{"id with space", IntegrationsConfig{GiteeClientID: "a b", GiteeClientSecretFile: "/etc/x"}, false, true},
	}
	for _, c := range cases {
		if err := validateIntegrations(c.in); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
		if c.in.GiteeConfigured() != c.conf {
			t.Errorf("%s: configured = %v, want %v", c.name, c.in.GiteeConfigured(), c.conf)
		}
	}
}

func TestGiteeSettingsDefaultsAndURLs(t *testing.T) {
	got := IntegrationsConfig{GiteeClientID: "id", GiteeClientSecretFile: "/x", GiteeClientSecret: "s", GiteeBaseURL: "http://127.0.0.1:1/"}.Gitee()
	if got.BaseURL != "http://127.0.0.1:1" || got.APIURL != "https://gitee.com/api/v5" || !got.Configured || got.ClientSecret != "s" {
		t.Fatalf("settings = %+v", got)
	}
	if empty := (IntegrationsConfig{}).Gitee(); empty.BaseURL != "https://gitee.com" || empty.APIURL != "https://gitee.com/api/v5" || empty.Configured {
		t.Fatalf("empty settings = %+v", empty)
	}
	if err := validateGitHubURL(EnvironmentProduction, "integrations.gitee_api_url", "http://gitee.example.com/api/v5"); err == nil {
		t.Fatal("production must reject an http gitee api url")
	}
	if err := validateGitHubURL(EnvironmentDevelopment, "integrations.gitee_api_url", "http://127.0.0.1:18103/api/v5"); err != nil {
		t.Fatalf("development must accept local gitee urls: %v", err)
	}
}

func TestReadSecretFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	if got, err := readSecretFile(write("ok", "  abc123\n", 0o600)); err != nil || got != "abc123" {
		t.Fatalf("ok file = %q, %v", got, err)
	}
	if _, err := readSecretFile(write("open", "abc", 0o640)); err == nil {
		t.Fatal("group-readable file accepted")
	}
	if _, err := readSecretFile(write("empty", " \n", 0o600)); err == nil {
		t.Fatal("empty file accepted")
	}
	if _, err := readSecretFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := readSecretFile(dir); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestFeishuURLsDefaultsAndValidation(t *testing.T) {
	base, accounts := IntegrationsConfig{}.FeishuURLs()
	if base != "https://open.feishu.cn" || accounts != "https://accounts.feishu.cn" {
		t.Fatalf("defaults = %s, %s", base, accounts)
	}
	base, accounts = IntegrationsConfig{FeishuBaseURL: "http://127.0.0.1:18097/", FeishuAccountsURL: "http://127.0.0.1:18097"}.FeishuURLs()
	if base != "http://127.0.0.1:18097" || accounts != "http://127.0.0.1:18097" {
		t.Fatalf("custom = %s, %s", base, accounts)
	}
	for _, c := range []struct {
		env, value string
		ok         bool
	}{
		{EnvironmentDevelopment, "http://127.0.0.1:18097", true},
		{EnvironmentProduction, "http://127.0.0.1:18097", false},
		{EnvironmentProduction, "https://open.feishu.cn", true},
		{EnvironmentDevelopment, "ftp://open.feishu.cn", false},
		{EnvironmentDevelopment, "https://u:p@open.feishu.cn", false},
		{EnvironmentDevelopment, "", true},
	} {
		if err := validateGitHubURL(c.env, "integrations.feishu_base_url", c.value); (err == nil) != c.ok {
			t.Errorf("validate(%s, %q) = %v, want ok=%v", c.env, c.value, err, c.ok)
		}
	}
}

// Lark 与钉钉的地址默认值、可配置、末尾斜杠处理。
func TestLarkAndDingTalkURLsDefaultsAndOverrides(t *testing.T) {
	base, accounts := IntegrationsConfig{}.LarkURLs()
	if base != "https://open.larksuite.com" || accounts != "https://accounts.larksuite.com" {
		t.Fatalf("lark defaults = %s %s", base, accounts)
	}
	base, accounts = IntegrationsConfig{LarkBaseURL: "http://127.0.0.1:18098/", LarkAccountsURL: "http://127.0.0.1:18098"}.LarkURLs()
	if base != "http://127.0.0.1:18098" || accounts != "http://127.0.0.1:18098" {
		t.Fatalf("lark overrides = %s %s", base, accounts)
	}
	api, oapi, login := IntegrationsConfig{}.DingTalkURLs()
	if api != "https://api.dingtalk.com" || oapi != "https://oapi.dingtalk.com" || login != "https://login.dingtalk.com" {
		t.Fatalf("dingtalk defaults = %s %s %s", api, oapi, login)
	}
	api, oapi, login = IntegrationsConfig{DingTalkAPIURL: "http://127.0.0.1:18099/", DingTalkOAPIURL: "http://127.0.0.1:18099", DingTalkLoginURL: "http://127.0.0.1:18099/"}.DingTalkURLs()
	if api != "http://127.0.0.1:18099" || oapi != "http://127.0.0.1:18099" || login != "http://127.0.0.1:18099" {
		t.Fatalf("dingtalk overrides = %s %s %s", api, oapi, login)
	}
}
