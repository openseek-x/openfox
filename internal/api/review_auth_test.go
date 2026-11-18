package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"openfox/internal/identity"
	"strings"
	"testing"
	"time"
)

func reviewAuthRequest(h http.Handler, path, ip, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = ip + ":1234"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func reviewAuthHandler() http.Handler {
	return New(identity.Service{Store: &emailAuthStore{}, EmailStore: &emailAuthStore{}, Now: time.Now, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour}, nil, false)
}
func TestReviewPasswordFailureLimits(t *testing.T) {
	h := reviewAuthHandler()
	login := func(email, ip string) *httptest.ResponseRecorder {
		return reviewAuthRequest(h, "/v1/auth/login", ip, fmt.Sprintf(`{"email":%q,"password":"wrong"}`, email))
	}
	for range 5 {
		if w := login("alice@example.test", "192.0.2.1"); w.Code != 401 {
			t.Fatalf("失败计数前响应=%d", w.Code)
		}
	}
	if w := login(" Alice@Example.Test ", "192.0.2.1"); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("账号/IP 没有限流：%d", w.Code)
	}
	if w := login("alice@example.test", "192.0.2.2"); w.Code != 401 {
		t.Fatalf("攻击者锁住其他 IP：%d", w.Code)
	}
	for i := 0; i < 25; i++ {
		login(fmt.Sprintf("user%d@example.test", i), "192.0.2.1")
	}
	if w := login("other@example.test", "192.0.2.1"); w.Code != 429 {
		t.Fatalf("IP 没有限流：%d", w.Code)
	}
}
func TestReviewInvitationAcceptanceLimits(t *testing.T) {
	h := reviewAuthHandler()
	for range 10 {
		reviewAuthRequest(h, "/v1/invites/test/accept", "192.0.2.3", `{}`)
	}
	w := reviewAuthRequest(h, "/v1/invites/test/accept", "192.0.2.3", `{}`)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("邀请接受无限流：%d %s", w.Code, w.Body.String())
	}
}
func TestReviewEmailFailureLimitsSeparateClients(t *testing.T) {
	h := reviewAuthHandler()
	for range 20 {
		reviewAuthRequest(h, "/v1/auth/email/verify", "192.0.2.4", `{"email":"alice@example.test","code":"123456"}`)
	}
	w := reviewAuthRequest(h, "/v1/auth/email/verify", "192.0.2.4", `{"email":"alice@example.test","code":"123456"}`)
	if w.Code != 429 {
		t.Fatalf("邮箱/IP 没有限流：%d", w.Code)
	}
	w = reviewAuthRequest(h, "/v1/auth/email/verify", "192.0.2.5", `{"email":"alice@example.test","code":"123456"}`)
	if w.Code != 401 {
		t.Fatalf("攻击者锁住其他 IP：%d", w.Code)
	}
}

func TestReviewSuccessfulPasswordDoesNotConsumeFailureQuota(t *testing.T) {
	hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 16384, Iterations: 1, Parallelism: 1, KeyLength: 16, SaltLength: 16})
	if err != nil {
		t.Fatal(err)
	}
	hash, err := hasher.Hash("有效密码")
	if err != nil {
		t.Fatal(err)
	}
	store := &emailAuthStore{credentials: identity.Credentials{UserID: "00000000-0000-0000-0000-000000000001", PasswordHash: hash, Status: "ACTIVE", CredentialVersion: 1}}
	h := New(identity.Service{Store: store, PasswordHasher: hasher, Now: time.Now, SessionIdle: time.Hour, SessionAbsolute: 24 * time.Hour}, nil, false)
	for range 7 {
		w := reviewAuthRequest(h, "/v1/auth/login", "192.0.2.6", `{"email":"alice@example.test","password":"有效密码"}`)
		if w.Code != 201 {
			t.Fatalf("成功登录消耗失败配额：%d", w.Code)
		}
	}
}

func TestReviewPasswordWorkIsBounded(t *testing.T) {
	h := &Handler{passwordSlots: make(chan struct{}, 4)}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	releases := []func(){}
	for range 4 {
		release, ok := h.reservePasswordSlot(httptest.NewRecorder(), req)
		if !ok {
			t.Fatal("提前拒绝")
		}
		releases = append(releases, release)
	}
	w := httptest.NewRecorder()
	if _, ok := h.reservePasswordSlot(w, req); ok || w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal("密码计算没有并发上限")
	}
	for _, release := range releases {
		release()
	}
	release, ok := h.reservePasswordSlot(httptest.NewRecorder(), req)
	if !ok {
		t.Fatal("名额未释放")
	}
	release()
}
