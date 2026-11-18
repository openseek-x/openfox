package main

import (
	"bytes"
	"log/slog"
	"openfox/internal/platform/config"
	"strings"
	"testing"
)

func TestReviewDevelopmentAllowanceWarnings(t *testing.T) {
	for _, env := range []string{config.EnvironmentDevelopment, config.EnvironmentTest, config.EnvironmentProduction} {
		t.Run(env, func(t *testing.T) {
			cfg := config.Config{Environment: env}
			cfg.Email.OTP.UniversalCode = "842619"
			var output bytes.Buffer
			warnDevelopmentAllowances(slog.New(slog.NewJSONHandler(&output, nil)), cfg)
			if env == config.EnvironmentProduction {
				if output.Len() != 0 {
					t.Fatal("生产配置不应启用开发放行")
				}
				return
			}
			for _, event := range []string{"gitlab_local_access_enabled", "email_universal_code_enabled"} {
				if !strings.Contains(output.String(), event) {
					t.Errorf("启动未警告 %s", event)
				}
			}
			if strings.Contains(output.String(), cfg.Email.OTP.UniversalCode) {
				t.Fatal("日志泄露万能验证码")
			}
		})
	}
}
