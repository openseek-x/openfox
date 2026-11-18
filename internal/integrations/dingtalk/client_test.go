package dingtalk

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	fake "openfox/tests/support/fakedingtalk/fake"
)

func newClient(t *testing.T) (*Client, *fake.Server) {
	t.Helper()
	server := fake.New()
	t.Cleanup(server.Close)
	server.AddApp("dingapp0000001", "secret-0123456789ab")
	server.AddUser(fake.User{OpenID: "ou_1", UnionID: "un_1", StaffID: "st_1", Name: "Lin"})
	return New(server.URL, server.URL, server.URL), server
}

func TestAccessTokenCachesAndClassifiesErrors(t *testing.T) {
	client, server := newClient(t)
	ctx := context.Background()
	if _, err := client.AccessToken(ctx, "dingapp0000001", "wrong-secret-0123456"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong secret = %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := client.AccessToken(ctx, "dingapp0000001", "secret-0123456789ab"); err != nil {
			t.Fatal(err)
		}
	}
	// 一次失败（不缓存）+ 一次成功（缓存），后两次命中缓存。
	if calls := server.TokenCalls(); calls != 2 {
		t.Fatalf("token calls = %d, want 2", calls)
	}
	client.ClearToken("dingapp0000001", "secret-0123456789ab")
	if _, err := client.AccessToken(ctx, "dingapp0000001", "secret-0123456789ab"); err != nil || server.TokenCalls() != 3 {
		t.Fatalf("after ClearToken: err=%v calls=%d", err, server.TokenCalls())
	}
	server.SetServerError(true)
	client.ClearToken("dingapp0000001", "secret-0123456789ab")
	if _, err := client.AccessToken(ctx, "dingapp0000001", "secret-0123456789ab"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("server error = %v", err)
	}
}

func TestAuthorizeURLAndIdentity(t *testing.T) {
	client, server := newClient(t)
	ctx := context.Background()
	authorize := client.AuthorizeURL("dingapp0000001", "https://x.example/cb", "st-1")
	for _, want := range []string{"client_id=dingapp0000001", "scope=openid", "prompt=consent", "response_type=code", "state=st-1", "redirect_uri=https%3A%2F%2Fx.example%2Fcb"} {
		if !strings.Contains(authorize, want) {
			t.Fatalf("authorize url %s lacks %s", authorize, want)
		}
	}
	if !strings.HasPrefix(authorize, server.URL+"/oauth2/auth?") {
		t.Fatalf("authorize url = %s", authorize)
	}
	if _, err := client.ExchangeCode(ctx, "dingapp0000001", "secret-0123456789ab", "bogus"); !errors.Is(err, ErrAuthorizationFailed) {
		t.Fatalf("bogus code = %v", err)
	}
	if _, err := client.UserInfo(ctx, "bogus"); !errors.Is(err, ErrAuthorizationFailed) {
		t.Fatalf("bogus user token = %v", err)
	}
	token, err := client.AccessToken(ctx, "dingapp0000001", "secret-0123456789ab")
	if err != nil {
		t.Fatal(err)
	}
	staff, err := client.StaffIDByUnionID(ctx, token, "un_1")
	if err != nil || staff != "st_1" {
		t.Fatalf("staff = %q err=%v", staff, err)
	}
	if _, err := client.StaffIDByUnionID(ctx, token, "un_other"); !errors.Is(err, ErrUserUnreachable) {
		t.Fatalf("unknown union = %v", err)
	}
	if _, err := client.StaffIDByUnionID(ctx, "bad-token", "un_1"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("bad token = %v", err)
	}
}

func TestUnionIDByStaffID(t *testing.T) {
	client, server := newClient(t)
	server.AddCorpUser("corp_other", fake.User{OpenID: "ou_9", UnionID: "un_9", StaffID: "st_1", Name: "Other"})
	ctx := context.Background()
	token, _ := client.AccessToken(ctx, "dingapp0000001", "secret-0123456789ab")
	profile, err := client.UnionIDByStaffID(ctx, token, "st_1")
	if err != nil || profile.UnionID != "un_1" || profile.CorpID != "corp_default" {
		t.Fatalf("profile = %+v err=%v", profile, err)
	}
	// 同一个 userid 在另一个企业里是另一个人：应用只能查自己企业里的员工。
	server.AddApp("dingapp0000002", "secret-other-0123456")
	server.SetAppCorp("dingapp0000002", "corp_other")
	other, _ := client.AccessToken(ctx, "dingapp0000002", "secret-other-0123456")
	profile, err = client.UnionIDByStaffID(ctx, other, "st_1")
	if err != nil || profile.UnionID != "un_9" || profile.CorpID != "corp_other" {
		t.Fatalf("other corp profile = %+v err=%v", profile, err)
	}
	if _, err := client.UnionIDByStaffID(ctx, token, "st_missing"); !errors.Is(err, ErrUserUnreachable) {
		t.Fatalf("missing staff = %v", err)
	}
	if _, err := client.UnionIDByStaffID(ctx, "bad-token", "st_1"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("bad token = %v", err)
	}
}

func TestSendClassification(t *testing.T) {
	client, server := newClient(t)
	ctx := context.Background()
	token, _ := client.AccessToken(ctx, "dingapp0000001", "secret-0123456789ab")
	if _, err := client.SendUserMarkdown(ctx, token, "dingapp0000001", "st_1", "t", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SendGroupMarkdown(ctx, token, "dingapp0000001", "cid_1", "t", "hello group"); err != nil {
		t.Fatal(err)
	}
	messages := server.Messages("dingapp0000001")
	if len(messages) != 2 || messages[0].Kind != "user" || messages[1].Kind != "group" || messages[1].Target != "cid_1" {
		t.Fatalf("messages = %+v", messages)
	}
	if _, err := client.SendUserMarkdown(ctx, token, "dingapp0000001", "st_missing", "t", "x"); !errors.Is(err, ErrUserUnreachable) {
		t.Fatalf("invalid staff = %v", err)
	}
	server.SetFlowControlled("st_1", true)
	var limited *RateLimitedError
	if _, err := client.SendUserMarkdown(ctx, token, "dingapp0000001", "st_1", "t", "x"); !errors.As(err, &limited) {
		t.Fatalf("flow controlled = %v", err)
	}
	server.SetFlowControlled("st_1", false)
	server.FailNextSends(1, 429)
	if _, err := client.SendGroupMarkdown(ctx, token, "dingapp0000001", "cid_1", "t", "x"); !errors.As(err, &limited) || limited.RetryAfter != time.Second {
		t.Fatalf("429 = %v", err)
	}
	server.FailNextSends(1, 401)
	if _, err := client.SendGroupMarkdown(ctx, token, "dingapp0000001", "cid_1", "t", "x"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("401 = %v", err)
	}
	server.FailNextSends(1, 500)
	if _, err := client.SendGroupMarkdown(ctx, token, "dingapp0000001", "cid_1", "t", "x"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("500 = %v", err)
	}
	server.FailNextSends(1, 400)
	if _, err := client.SendGroupMarkdown(ctx, token, "dingapp0000001", "cid_1", "t", "x"); !errors.Is(err, ErrUserUnreachable) {
		t.Fatalf("400 = %v", err)
	}
}

func TestStreamHandshakeAckAndSystemFrames(t *testing.T) {
	client, server := newClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var got []Message
	connected := make(chan struct{}, 4)
	done := make(chan error, 1)
	go func() {
		done <- client.Run(ctx, Config{AppKey: "dingapp0000001", Secret: "secret-0123456789ab",
			OnConnected: func() { connected <- struct{}{} },
			Handler: func(_ context.Context, msg Message) error {
				mu.Lock()
				got = append(got, msg)
				mu.Unlock()
				if strings.Contains(string(msg.Data), "fail") {
					return errors.New("later")
				}
				return nil
			}})
	}()
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("not connected")
	}
	// 回调：ACK 带空 response；事件：成功 SUCCESS，失败 LATER。
	ack, err := server.PushCallback("dingapp0000001", TopicBotMessage, map[string]string{"msgId": "m1"})
	if err != nil || ack != `{"response":{}}` {
		t.Fatalf("callback ack = %q err=%v", ack, err)
	}
	ack, err = server.PushEvent("dingapp0000001", "user_add_org", "ev-1", map[string]string{"x": "ok"})
	if err != nil || !strings.Contains(ack, `"SUCCESS"`) {
		t.Fatalf("event ack = %q err=%v", ack, err)
	}
	ack, err = server.PushEvent("dingapp0000001", "user_add_org", "ev-2", map[string]string{"x": "fail"})
	if err != nil || !strings.Contains(ack, `"LATER"`) {
		t.Fatalf("failed event ack = %q err=%v", ack, err)
	}
	mu.Lock()
	if len(got) != 3 || got[0].Topic != TopicBotMessage || got[1].EventID != "ev-1" || got[1].EventType != "user_add_org" {
		t.Fatalf("messages = %+v", got)
	}
	mu.Unlock()
	// ping 原样带回 opaque（fake 内部校验）。
	if _, err := server.PushSystem("dingapp0000001", "ping"); err != nil {
		t.Fatal(err)
	}
	// disconnect：Run 返回可重连的错误。
	if _, err := server.PushSystem("dingapp0000001", "disconnect"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var retry *RetryError
		if !errors.As(err, &retry) || !errors.Is(err, ErrDisconnected) || IsPermanent(err) {
			t.Fatalf("Run after disconnect = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after disconnect")
	}
}

func TestStreamErrorsAndEndpointValidation(t *testing.T) {
	client, server := newClient(t)
	ctx := context.Background()
	if err := client.Run(ctx, Config{AppKey: "dingapp0000001", Secret: "wrong-secret-0123456"}); !IsPermanent(err) || !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("bad credentials = %v", err)
	}
	server.SetEndpointOK(false)
	err := client.Run(ctx, Config{AppKey: "dingapp0000001", Secret: "secret-0123456789ab"})
	var retry *RetryError
	if !errors.As(err, &retry) || IsPermanent(err) {
		t.Fatalf("gateway down = %v", err)
	}
	// 端点安全校验：只接受与 API 同域或共享上级域名的 wss 地址。
	for _, bad := range []string{"wss://evil.example/connect", "ws://127.0.0.1:1/connect", "https://x/connect", "wss://user@x/connect"} {
		if _, err := client.validateEndpoint(bad); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("endpoint %q = %v, want invalid", bad, err)
		}
	}
	real := New("https://api.dingtalk.com", "https://oapi.dingtalk.com", "https://login.dingtalk.com")
	for _, good := range []string{"wss://wss-open-connection.dingtalk.com:443/connect", "wss://api.dingtalk.com/connect"} {
		if _, err := real.validateEndpoint(good); err != nil {
			t.Fatalf("endpoint %q = %v, want valid", good, err)
		}
	}
	for _, bad := range []string{"ws://wss-open-connection.dingtalk.com/connect", "wss://dingtalk.com.evil.example/connect", "wss://10.0.0.1/connect", "wss://evil-dingtalk.com/connect"} {
		if _, err := real.validateEndpoint(bad); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("endpoint %q = %v, want invalid", bad, err)
		}
	}
}
