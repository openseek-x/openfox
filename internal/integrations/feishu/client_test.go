package feishu_test

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"openfox/internal/integrations/feishu"
)

func TestTenantTokenIsCached(t *testing.T) {
	fake, client := newFake(t)
	for i := 0; i < 3; i++ {
		if _, err := client.TenantToken(context.Background(), appID, secret); err != nil {
			t.Fatal(err)
		}
	}
	if fake.TokenCalls() != 1 {
		t.Fatalf("token calls = %d, want 1 (cached)", fake.TokenCalls())
	}
	client.ClearToken(appID, secret)
	if _, err := client.TenantToken(context.Background(), appID, secret); err != nil || fake.TokenCalls() != 2 {
		t.Fatalf("after clear: calls = %d, err = %v", fake.TokenCalls(), err)
	}
}

func TestTenantTokenErrors(t *testing.T) {
	fake, client := newFake(t)
	if _, err := client.TenantToken(context.Background(), appID, "wrong-secret-0000000"); !errors.Is(err, feishu.ErrInvalidCredentials) {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := client.TenantToken(context.Background(), "cli_unknown000000", secret); !errors.Is(err, feishu.ErrInvalidCredentials) {
		t.Fatalf("unknown app: %v", err)
	}
	fake.SetServerError(true)
	if _, err := client.TenantToken(context.Background(), appID, secret); !errors.Is(err, feishu.ErrUnavailable) {
		t.Fatalf("5xx: %v", err)
	}
}

func TestBotInfo(t *testing.T) {
	fake, client := newFake(t)
	token, _ := client.TenantToken(context.Background(), appID, secret)
	bot, err := client.BotInfo(context.Background(), token)
	if err != nil || bot.AppName != "测试机器人" || bot.OpenID == "" {
		t.Fatalf("bot = %+v, %v", bot, err)
	}
	fake.SetBotAvailable(appID, false)
	if _, err := client.BotInfo(context.Background(), token); !errors.Is(err, feishu.ErrBotUnavailable) {
		t.Fatalf("disabled bot: %v", err)
	}
	fake.ExpireTokens()
	if _, err := client.BotInfo(context.Background(), token); !errors.Is(err, feishu.ErrTokenExpired) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestSendCardClassification(t *testing.T) {
	fake, client := newFake(t)
	token, _ := client.TenantToken(context.Background(), appID, secret)
	id, err := client.SendCard(context.Background(), token, "ou_1", `{"a":1}`, "uuid-1")
	if err != nil || id == "" {
		t.Fatalf("send = %q, %v", id, err)
	}
	again, err := client.SendCard(context.Background(), token, "ou_1", `{"a":1}`, "uuid-1")
	if err != nil || again != id || len(fake.Messages(appID)) != 1 {
		t.Fatalf("duplicate uuid = %q, %v, messages = %d", again, err, len(fake.Messages(appID)))
	}
	fake.FailNextSends(1, 99991400)
	_, err = client.SendCard(context.Background(), token, "ou_1", `{}`, "uuid-2")
	var limited *feishu.RateLimitedError
	if !errors.Is(err, feishu.ErrRateLimited) || !errors.As(err, &limited) || limited.RetryAfter != 2*time.Second {
		t.Fatalf("rate limit: %v", err)
	}
	fake.FailNextSends(1, 230013)
	if _, err := client.SendCard(context.Background(), token, "ou_1", `{}`, "uuid-3"); !errors.Is(err, feishu.ErrUserUnreachable) {
		t.Fatalf("unreachable: %v", err)
	}
	fake.FailNextSends(1, 500)
	if _, err := client.SendCard(context.Background(), token, "ou_1", `{}`, "uuid-4"); !errors.Is(err, feishu.ErrUnavailable) {
		t.Fatalf("500: %v", err)
	}
}

func TestOAuthCodeExchange(t *testing.T) {
	fake, client := newFake(t)
	fake.AddUser(appID, "ou_zhang", "张三")
	fake.SetAuthorizeUser("ou_zhang")
	authorize := client.AuthorizeURL(appID, "http://example.test/cb", "st", "challenge")
	parsed, _ := url.Parse(authorize)
	if parsed.Path != "/open-apis/authen/v1/authorize" || parsed.Query().Get("code_challenge_method") != "S256" ||
		parsed.Query().Get("client_id") != appID {
		t.Fatalf("authorize url = %s", authorize)
	}
	if _, err := client.ExchangeCode(context.Background(), appID, secret, "bad-code", "http://example.test/cb", "verifier"); !errors.Is(err, feishu.ErrAuthorizationFailed) {
		t.Fatalf("bad code: %v", err)
	}
	if _, err := client.UserInfo(context.Background(), "bad-token"); !errors.Is(err, feishu.ErrAuthorizationFailed) {
		t.Fatalf("bad token: %v", err)
	}
	if strings.Contains(authorize, secret) {
		t.Fatal("authorize url must not contain the secret")
	}
}
