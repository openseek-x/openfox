package feishu_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"openfox/internal/integrations/feishu"
)

func TestRegistrationFlow(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	if err := client.RegistrationInit(ctx); err != nil {
		t.Fatal(err)
	}
	begin, err := client.RegistrationBeginFlow(ctx)
	if err != nil || begin.DeviceCode == "" || !strings.Contains(begin.VerificationURIComplete, "user_code=") || begin.Interval < 1 || begin.ExpireIn != 600 {
		t.Fatalf("begin = %+v, %v", begin, err)
	}
	if _, err := client.RegistrationPoll(ctx, begin.DeviceCode); !errors.Is(err, feishu.ErrRegistrationPending) {
		t.Fatalf("pending: %v", err)
	}
	fake.SetSlowDown(1)
	var slow *feishu.SlowDownError
	if _, err := client.RegistrationPoll(ctx, begin.DeviceCode); !errors.As(err, &slow) || slow.Extra != 5 {
		t.Fatalf("slow_down: %v", err)
	}
	fake.SetAuthorizeUnion("on_scanner")
	codes := fake.PendingUserCodes()
	if len(codes) != 1 || !fake.ApproveRegistration(codes[0], "") {
		t.Fatalf("approve: %v", codes)
	}
	result, err := client.RegistrationPoll(ctx, begin.DeviceCode)
	if err != nil || !strings.HasPrefix(result.ClientID, "cli_") || result.ClientSecret == "" || result.OpenID == "" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	// 凭证只返回一次。
	if _, err := client.RegistrationPoll(ctx, begin.DeviceCode); !errors.Is(err, feishu.ErrRegistrationExpired) {
		t.Fatalf("second poll: %v", err)
	}
	// 新应用可以直接换 token 和读机器人。
	token, err := client.TenantToken(ctx, result.ClientID, result.ClientSecret)
	if err != nil {
		t.Fatal(err)
	}
	if bot, err := client.BotInfo(ctx, token); err != nil || bot.AppName == "" {
		t.Fatalf("bot = %+v, %v", bot, err)
	}
}

func TestRegistrationTerminalStates(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		apply func(code string)
		want  error
	}{
		{"denied", func(c string) { fake.DenyRegistration(c) }, feishu.ErrRegistrationDenied},
		{"expired", func(c string) { fake.ExpireRegistration(c) }, feishu.ErrRegistrationExpired},
	} {
		begin, err := client.RegistrationBeginFlow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		codes := fake.PendingUserCodes()
		tc.apply(codes[len(codes)-1])
		if _, err := client.RegistrationPoll(ctx, begin.DeviceCode); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	// 未知设备码按被拒绝处理；飞书 5xx 是可重试的不可用。
	if _, err := client.RegistrationPoll(ctx, "unknown"); !errors.Is(err, feishu.ErrRegistrationRejected) {
		t.Fatalf("unknown: %v", err)
	}
	fake.SetServerError(true)
	if _, err := client.RegistrationPoll(ctx, "x"); !errors.Is(err, feishu.ErrUnavailable) {
		t.Fatalf("5xx: %v", err)
	}
	if err := client.RegistrationInit(ctx); !errors.Is(err, feishu.ErrUnavailable) {
		t.Fatalf("init 5xx: %v", err)
	}
}

func TestChatMessagesAndReply(t *testing.T) {
	fake, client := newFake(t)
	ctx := context.Background()
	token, _ := client.TenantToken(ctx, appID, secret)
	first := fake.AddChatMessage("oc_1", "on_a", "user", "第一条")
	fake.AddChatMessage("oc_1", "on_b", "user", "第二条")
	items, err := client.ListChatMessages(ctx, token, "oc_1", 20)
	if err != nil || len(items) != 2 || !strings.Contains(items[0].Body.Content, "第二条") || items[0].Sender.SenderType != "user" {
		t.Fatalf("items = %+v, %v", items, err)
	}
	id, err := client.ReplyCard(ctx, token, first, `{"elements":[]}`, "uuid-1")
	if err != nil || id == "" {
		t.Fatalf("reply: %q %v", id, err)
	}
	// 同一个 uuid 重放返回同一条。
	if again, err := client.ReplyCard(ctx, token, first, `{"elements":[]}`, "uuid-1"); err != nil || again != id {
		t.Fatalf("replay: %q %v", again, err)
	}
	fake.SetMessageGone(first)
	if _, err := client.ReplyCard(ctx, token, first, `{"elements":[]}`, "uuid-2"); !errors.Is(err, feishu.ErrMessageGone) {
		t.Fatalf("gone: %v", err)
	}
	if _, err := client.SendChatCard(ctx, token, "oc_1", `{"elements":[]}`, "uuid-3"); err != nil {
		t.Fatal(err)
	}
	fake.SetHistoryForbidden(true)
	if _, err := client.ListChatMessages(ctx, token, "oc_1", 20); !errors.Is(err, feishu.ErrNoPermission) {
		t.Fatalf("forbidden: %v", err)
	}
}
