package stage3

// 个人访问令牌、通知渠道、内测功能的集成测试。
// 令牌与内测的用例使用本文件新建的测试账号（不依赖固定账号的令牌数量）；令牌请求只带 Authorization 头，不带 cookie 与 CSRF。

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"strings"
	"testing"
	"time"

	"openfox/internal/identity"
)

type tokenView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

type tokenCreated struct {
	Token  tokenView `json:"token"`
	Secret string    `json:"secret"`
}

type notificationChannelView struct {
	Channel        string   `json:"channel"`
	Enabled        bool     `json:"enabled"`
	Available      bool     `json:"available"`
	DisabledEvents []string `json:"disabled_events"`
	DirectMessages *bool    `json:"direct_messages"`
	Customized     bool     `json:"customized"`
}

// newTokenUser 新建一个测试账号（随机 ID 与邮箱），返回 ID 与邮箱；账号默认不属于任何组织。
func newTokenUser(t *testing.T, env *s3Env) (string, string) {
	t.Helper()
	id := s3NewID(t)
	email := "tok-" + id + "@example.test"
	env.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2)`, id, email)
	env.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)`, id, s3Hash)
	return id, email
}

// loginClient 用邮箱密码登录，返回带 cookie 与 CSRF 的客户端（与 env.client 相同，但不按用户缓存）。
func (e *s3Env) loginClient(email string) *s3Client {
	e.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	client := &s3Client{env: e, http: &http.Client{Jar: jar}, server: e.server}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, email, s3Password)
	response, err := client.http.Post(e.server.URL+"/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		e.t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		e.t.Fatalf("login status = %d", response.StatusCode)
	}
	parsed, _ := neturl.Parse(e.server.URL)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			client.csrf = cookie.Value
		}
	}
	return client
}

// sendRaw 发送请求并返回状态码与响应体。不调用 t.Fatal，可在 goroutine 中使用。
func (e *s3Env) sendRaw(httpClient *http.Client, method, path string, body any, headers map[string]string) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(e.ctx, method, e.server.URL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	return response.StatusCode, payload, err
}

// bearer 只带 Authorization 头发送请求（无 cookie、无 CSRF）。
func (e *s3Env) bearer(secret, method, path string, body any) s3Response {
	e.t.Helper()
	status, payload, err := e.sendRaw(&http.Client{}, method, path, body, map[string]string{"Authorization": "Bearer " + secret})
	if err != nil {
		e.t.Fatal(err)
	}
	return s3Response{Status: status, Body: payload}
}

// createToken 以会话身份为 userClient 创建令牌，返回明文与记录。
func createToken(t *testing.T, client *s3Client, name string, days any) tokenCreated {
	t.Helper()
	var created tokenCreated
	client.must(http.StatusCreated, http.MethodPost, "/v1/me/tokens", map[string]any{"name": name, "expires_in_days": days}, &created)
	if !strings.HasPrefix(created.Secret, identity.PersonalAccessTokenPrefix) || len(created.Secret) != 51 {
		t.Fatal("created token secret has an unexpected shape")
	}
	return created
}

// addOrgMember 以超级用户把测试账号加入测试组织。
func (e *s3Env) addOrgMember(userID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT DO NOTHING`, e.org, userID, role)
}

func TestTokenCreateListAndSecretShownOnce(t *testing.T) {
	env := newS3Env(t)
	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)

	created := createToken(t, client, "  CI bot  ", 30)
	if created.Token.Name != "CI bot" {
		t.Fatalf("name = %q, want trimmed CI bot", created.Token.Name)
	}
	if created.Token.Prefix != created.Secret[:12] {
		t.Fatal("prefix must be ofx_pat_ plus the first four random characters")
	}
	if created.Token.ExpiresAt == nil || created.Token.LastUsedAt != nil {
		t.Fatal("new token must have an expiry and no last_used_at")
	}

	var list struct {
		Tokens []tokenView `json:"tokens"`
	}
	response := client.must(http.StatusOK, http.MethodGet, "/v1/me/tokens", nil, &list)
	if strings.Contains(string(response.Body), created.Secret) {
		t.Fatal("token list must not contain the plaintext secret")
	}
	if len(list.Tokens) != 1 || list.Tokens[0].ID != created.Token.ID {
		t.Fatalf("list = %+v, want the created token only", list.Tokens)
	}

	// 只存哈希：哈希与明文的 SHA-256 一致，且整行的文本表示里没有明文。
	hash := sha256.Sum256([]byte(created.Secret))
	if env.count(`SELECT count(*) FROM openfox.personal_access_tokens WHERE id = $1::uuid AND token_hash = $2`,
		created.Token.ID, hash[:]) != 1 {
		t.Fatal("token_hash must be the SHA-256 of the secret")
	}
	rows, err := env.superuser.Query(env.ctx, `SELECT t::text FROM openfox.personal_access_tokens t WHERE t.user_id = $1::uuid`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var rowText string
		if err := rows.Scan(&rowText); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rowText, created.Secret) {
			t.Fatal("a stored token row contains the plaintext secret")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestTokenBearerAuthenticatesLikeSession(t *testing.T) {
	env := newS3Env(t)
	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)
	env.addOrgMember(userID, "MEMBER")
	created := createToken(t, client, "bearer", nil)

	var me struct {
		ID         string `json:"id"`
		AccessTier string `json:"access_tier"`
	}
	response := env.bearer(created.Secret, http.MethodGet, "/v1/me", nil)
	if response.Status != http.StatusOK {
		t.Fatalf("bearer GET /v1/me = %d %s", response.Status, response.Body)
	}
	response.decode(t, &me)
	if me.ID != userID || me.AccessTier != "standard" {
		t.Fatalf("bearer /v1/me = %+v, want the token owner in standard tier", me)
	}
	if got := env.bearer(created.Secret, http.MethodGet, "/v1/organizations/"+env.org+"/get-started", nil); got.Status != http.StatusOK {
		t.Fatalf("bearer get-started in own org = %d %s", got.Status, got.Body)
	}
	if got := env.bearer(created.Secret, http.MethodGet, "/v1/organizations/"+env.otherOrg+"/get-started", nil); got.Status != http.StatusNotFound {
		t.Fatalf("bearer get-started in another org = %d, want 404", got.Status)
	}
	// 令牌请求不需要 CSRF：PUT 通知渠道直接成功。
	if got := env.bearer(created.Secret, http.MethodPut, "/v1/me/notification-channels/desktop",
		map[string]any{"enabled": true, "disabled_events": []string{}}); got.Status != http.StatusOK {
		t.Fatalf("bearer PUT without CSRF = %d %s", got.Status, got.Body)
	}
	// 格式错误、格式正确但不存在的令牌都是 401。
	if got := env.bearer("ofx_pat_short", http.MethodGet, "/v1/me", nil); got.Status != http.StatusUnauthorized {
		t.Fatalf("malformed token = %d, want 401", got.Status)
	}
	unknown := identity.PersonalAccessTokenPrefix + strings.Repeat("A", 43)
	if got := env.bearer(unknown, http.MethodGet, "/v1/me", nil); got.Status != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d, want 401", got.Status)
	}
}

func TestTokenExpiredRevokedAndOwnership(t *testing.T) {
	env := newS3Env(t)
	_, email := newTokenUser(t, env)
	client := env.loginClient(email)
	kept := createToken(t, client, "kept", nil)
	expiring := createToken(t, client, "expiring", 30)

	// expires_at 必须晚于 created_at：两者一起回拨，令牌进入过期状态。
	env.exec(`UPDATE openfox.personal_access_tokens
		SET created_at = clock_timestamp() - interval '31 days', expires_at = clock_timestamp() - interval '1 day'
		WHERE id = $1::uuid`, expiring.Token.ID)
	if got := env.bearer(expiring.Secret, http.MethodGet, "/v1/me", nil); got.Status != http.StatusUnauthorized {
		t.Fatalf("expired token = %d, want 401", got.Status)
	}
	if got := env.bearer(kept.Secret, http.MethodGet, "/v1/me", nil); got.Status != http.StatusOK {
		t.Fatalf("unexpired token = %d, want 200", got.Status)
	}

	// 已过期但未吊销的令牌仍出现在列表里。
	var list struct {
		Tokens []tokenView `json:"tokens"`
	}
	client.must(http.StatusOK, http.MethodGet, "/v1/me/tokens", nil, &list)
	if len(list.Tokens) != 2 {
		t.Fatalf("list length = %d, want 2 (expired but not revoked is listed)", len(list.Tokens))
	}

	// 吊销后立即失效；重复吊销与格式错误的 ID 都是 404。
	client.must(http.StatusNoContent, http.MethodDelete, "/v1/me/tokens/"+kept.Token.ID, nil, nil)
	if got := env.bearer(kept.Secret, http.MethodGet, "/v1/me", nil); got.Status != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d, want 401", got.Status)
	}
	client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "/v1/me/tokens/"+kept.Token.ID, nil)
	client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "/v1/me/tokens/not-a-uuid", nil)
	client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "/v1/me/tokens/"+s3NewID(t), nil)
	if env.count(`SELECT count(*) FROM openfox.personal_access_tokens WHERE id = $1::uuid AND revoked_at IS NOT NULL`,
		kept.Token.ID) != 1 {
		t.Fatal("revoked token must have revoked_at set")
	}

	// 别人的令牌：404，且对方的令牌仍然有效。
	otherID, otherEmail := newTokenUser(t, env)
	other := env.loginClient(otherEmail)
	foreign := createToken(t, other, "foreign", nil)
	client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodDelete, "/v1/me/tokens/"+foreign.Token.ID, nil)
	if got := env.bearer(foreign.Secret, http.MethodGet, "/v1/me", nil); got.Status != http.StatusOK {
		t.Fatalf("foreign token must stay valid after a failed revoke = %d", got.Status)
	}
	if env.count(`SELECT count(*) FROM openfox.personal_access_tokens WHERE id = $1::uuid AND user_id = $2::uuid`,
		foreign.Token.ID, otherID) != 1 {
		t.Fatal("foreign token must still belong to its owner")
	}
}

func TestTokenCannotManageTokensOrChangeEmail(t *testing.T) {
	env := newS3Env(t)
	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)
	created := createToken(t, client, "limited", nil)

	denied := func(method, path string, body any) {
		t.Helper()
		got := env.bearer(created.Secret, method, path, body)
		if got.Status != http.StatusForbidden || got.code() != "TOKEN_FORBIDDEN" {
			t.Fatalf("%s %s by token = %d %s, want 403 TOKEN_FORBIDDEN", method, path, got.Status, got.Body)
		}
	}
	denied(http.MethodGet, "/v1/me/tokens", nil)
	denied(http.MethodPost, "/v1/me/tokens", map[string]any{"name": "x"})
	denied(http.MethodDelete, "/v1/me/tokens/"+created.Token.ID, nil)
	denied(http.MethodPost, "/v1/me/email-change/start", nil)
	denied(http.MethodPost, "/v1/me/email-change/verify-current", map[string]any{"challenge_id": s3NewID(t), "code": "000000"})
	denied(http.MethodPost, "/v1/me/email-change/request-new", map[string]any{"challenge_id": s3NewID(t), "email": "x@example.test"})
	denied(http.MethodPost, "/v1/me/email-change/confirm", map[string]any{"challenge_id": s3NewID(t), "code": "000000"})

	// 其余接口与会话权限相同。
	if got := env.bearer(created.Secret, http.MethodGet, "/v1/me/preferences", nil); got.Status != http.StatusOK {
		t.Fatalf("token preferences read = %d, want 200", got.Status)
	}
	// 令牌注销：返回 204，不吊销会话，也不吊销令牌。
	if got := env.bearer(created.Secret, http.MethodPost, "/v1/auth/logout", nil); got.Status != http.StatusNoContent {
		t.Fatalf("token logout = %d, want 204", got.Status)
	}
	client.must(http.StatusOK, http.MethodGet, "/v1/me", nil, nil)
	if got := env.bearer(created.Secret, http.MethodGet, "/v1/me", nil); got.Status != http.StatusOK {
		t.Fatalf("token must survive a token logout = %d", got.Status)
	}
	if env.count(`SELECT count(*) FROM openfox.personal_access_tokens WHERE user_id = $1::uuid AND revoked_at IS NULL`,
		userID) != 1 {
		t.Fatal("token logout must not revoke tokens")
	}
}

func TestTokenValidationAndLimit(t *testing.T) {
	env := newS3Env(t)
	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)

	invalid := []map[string]any{
		{"name": ""},
		{"name": "   "},
		{"name": strings.Repeat("a", 81)},
		{"name": "ok", "expires_in_days": 45},
		{"name": "ok", "expires_in_days": 0},
		{"name": "ok", "expires_in_days": "30"},
		{"name": "ok", "unknown": true},
	}
	for _, body := range invalid {
		client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPost, "/v1/me/tokens", body)
	}

	// 名称边界：80 个字符（多字节字符按字符计）可以，expires_in_days 为 null 表示永不过期。
	boundary := createToken(t, client, strings.Repeat("长", 80), nil)
	if boundary.Token.ExpiresAt != nil {
		t.Fatal("null expires_in_days must mean no expiry")
	}
	for i := 1; i < 50; i++ {
		createToken(t, client, fmt.Sprintf("token %d", i), 90)
	}
	client.expectError(http.StatusConflict, "TOKEN_LIMIT", http.MethodPost, "/v1/me/tokens", map[string]any{"name": "over"})

	// 吊销一个后可以再创建。
	client.must(http.StatusNoContent, http.MethodDelete, "/v1/me/tokens/"+boundary.Token.ID, nil, nil)
	createToken(t, client, "after revoke", 365)
	if env.count(`SELECT count(*) FROM openfox.personal_access_tokens WHERE user_id = $1::uuid AND revoked_at IS NULL`,
		userID) != 50 {
		t.Fatal("active token count must be exactly 50")
	}
}

func TestTokenConcurrentCreateRespectsLimit(t *testing.T) {
	env := newS3Env(t)
	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)
	headers := map[string]string{"X-CSRF-Token": client.csrf}

	statuses := parallel(60, func(index int) int {
		status, _, err := env.sendRaw(client.http, http.MethodPost, "/v1/me/tokens",
			map[string]any{"name": fmt.Sprintf("parallel %d", index)}, headers)
		if err != nil {
			return -1
		}
		return status
	})
	if got := countStatus(statuses, http.StatusCreated); got != 50 {
		t.Fatalf("created = %d, want exactly 50 (statuses %v)", got, statuses)
	}
	if got := countStatus(statuses, http.StatusConflict); got != 10 {
		t.Fatalf("conflicts = %d, want 10", got)
	}
	if env.count(`SELECT count(*) FROM openfox.personal_access_tokens WHERE user_id = $1::uuid AND revoked_at IS NULL`,
		userID) != 50 {
		t.Fatal("stored active tokens must not exceed 50")
	}
}

func TestTokenLastUsedAtIsThrottled(t *testing.T) {
	env := newS3Env(t)
	_, email := newTokenUser(t, env)
	client := env.loginClient(email)
	created := createToken(t, client, "usage", nil)

	var before *time.Time
	env.scalar(&before, `SELECT last_used_at FROM openfox.personal_access_tokens WHERE id = $1::uuid`, created.Token.ID)
	if before != nil {
		t.Fatal("last_used_at must be empty before the first use")
	}
	env.bearer(created.Secret, http.MethodGet, "/v1/me", nil)
	var first time.Time
	env.scalar(&first, `SELECT last_used_at FROM openfox.personal_access_tokens WHERE id = $1::uuid`, created.Token.ID)

	// 一分钟内重复使用不写库。
	env.bearer(created.Secret, http.MethodGet, "/v1/me", nil)
	var second time.Time
	env.scalar(&second, `SELECT last_used_at FROM openfox.personal_access_tokens WHERE id = $1::uuid`, created.Token.ID)
	if !second.Equal(first) {
		t.Fatalf("last_used_at changed within a minute: %v -> %v", first, second)
	}

	// 距上次记录超过一分钟后再次写入。
	stale := time.Now().Add(-2 * time.Minute)
	env.exec(`UPDATE openfox.personal_access_tokens SET last_used_at = $2 WHERE id = $1::uuid`, created.Token.ID, stale)
	env.bearer(created.Secret, http.MethodGet, "/v1/me", nil)
	var refreshed time.Time
	env.scalar(&refreshed, `SELECT last_used_at FROM openfox.personal_access_tokens WHERE id = $1::uuid`, created.Token.ID)
	if !refreshed.After(stale.Add(time.Minute)) {
		t.Fatalf("last_used_at was not refreshed after a minute: %v", refreshed)
	}
}

func TestNotificationChannelDefaultsSaveAndReset(t *testing.T) {
	env := newS3Env(t)
	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)

	var list struct {
		Channels []notificationChannelView `json:"channels"`
	}
	response := client.must(http.StatusOK, http.MethodGet, "/v1/me/notification-channels", nil, &list)
	if !strings.Contains(string(response.Body), `"disabled_events":[]`) {
		t.Fatal("default disabled_events must be an empty array, not null")
	}
	if len(list.Channels) != 5 || list.Channels[0].Channel != "desktop" || list.Channels[1].Channel != "browser" || list.Channels[2].Channel != "feishu" || list.Channels[3].Channel != "email" || list.Channels[4].Channel != "wecom" {
		t.Fatalf("channels = %+v, want desktop, browser, feishu, email and wecom", list.Channels)
	}
	for _, channel := range list.Channels {
		if channel.Enabled != (channel.Channel != "email" && channel.Channel != "wecom") || channel.Available != (channel.Channel != "email") || channel.Customized || len(channel.DisabledEvents) != 0 {
			t.Fatalf("default channel = %+v", channel)
		}
	}
	var saved notificationChannelView
	client.expectError(http.StatusServiceUnavailable, "EMAIL_UNAVAILABLE", http.MethodPut,
		"/v1/me/notification-channels/email", map[string]any{"enabled": true, "disabled_events": []string{}})
	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/email",
		map[string]any{"enabled": false, "disabled_events": []string{}}, &saved)
	if saved.Enabled || saved.Available || !saved.Customized {
		t.Fatalf("disabled unavailable email channel = %+v", saved)
	}
	client.must(http.StatusOK, http.MethodDelete, "/v1/me/notification-channels/email", nil, &saved)
	if saved.Enabled || saved.Available || saved.Customized {
		t.Fatalf("reset unavailable email channel = %+v", saved)
	}

	client.must(http.StatusOK, http.MethodPut, "/v1/me/notification-channels/desktop",
		map[string]any{"enabled": false, "disabled_events": []string{"mention", "assign", "mention"}}, &saved)
	if saved.Enabled || !saved.Customized || strings.Join(saved.DisabledEvents, ",") != "assign,mention" {
		t.Fatalf("saved desktop = %+v, want disabled, customized, events sorted and deduplicated", saved)
	}
	client.must(http.StatusOK, http.MethodGet, "/v1/me/notification-channels", nil, &list)
	if list.Channels[0].Enabled || !list.Channels[0].Customized || list.Channels[1].Customized {
		t.Fatalf("after save channels = %+v", list.Channels)
	}

	invalid := []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/v1/me/notification-channels/desktop", map[string]any{"enabled": true, "disabled_events": []string{"digest"}}},
		{http.MethodPut, "/v1/me/notification-channels/Email", map[string]any{"enabled": true, "disabled_events": []string{}}},
		{http.MethodPut, "/v1/me/notification-channels/Desktop", map[string]any{"enabled": true, "disabled_events": []string{}}},
		{http.MethodPut, "/v1/me/notification-channels/desktop", map[string]any{"enabled": true}},
		{http.MethodPut, "/v1/me/notification-channels/desktop", map[string]any{"disabled_events": []string{}}},
		{http.MethodDelete, "/v1/me/notification-channels/Email", nil},
	}
	for _, tc := range invalid {
		client.expectError(http.StatusBadRequest, "INVALID_REQUEST", tc.method, tc.path, tc.body)
	}

	client.must(http.StatusOK, http.MethodDelete, "/v1/me/notification-channels/desktop", nil, &saved)
	if !saved.Enabled || saved.Customized || len(saved.DisabledEvents) != 0 {
		t.Fatalf("reset desktop = %+v, want defaults", saved)
	}
	if env.count(`SELECT count(*) FROM openfox.user_notification_channels WHERE user_id = $1::uuid AND channel = 'desktop'`,
		userID) != 0 {
		t.Fatal("reset must delete the stored row")
	}
}

func TestBetaFeatureAccessTiersAndToggles(t *testing.T) {
	env := newS3Env(t)
	owner := env.owner()

	var me struct {
		AccessTier string `json:"access_tier"`
	}
	owner.must(http.StatusOK, http.MethodGet, "/v1/me", nil, &me)
	if me.AccessTier != "standard" {
		t.Fatalf("owner access_tier = %q, want standard", me.AccessTier)
	}
	owner.expectError(http.StatusForbidden, "BETA_ONLY", http.MethodGet, "/v1/me/beta-features", nil)
	owner.expectError(http.StatusForbidden, "BETA_ONLY", http.MethodPut, "/v1/me/beta-features/anything", map[string]any{"enabled": true})

	userID, email := newTokenUser(t, env)
	client := env.loginClient(email)
	env.exec(`UPDATE openfox.users SET access_tier = 'beta' WHERE id = $1::uuid`, userID)
	client.must(http.StatusOK, http.MethodGet, "/v1/me", nil, &me)
	if me.AccessTier != "beta" {
		t.Fatalf("access_tier = %q, want beta", me.AccessTier)
	}

	// 没有可用功能时返回空数组。
	var list struct {
		Features []struct {
			Key     string `json:"key"`
			Enabled bool   `json:"enabled"`
		} `json:"features"`
	}
	response := client.must(http.StatusOK, http.MethodGet, "/v1/me/beta-features", nil, &list)
	if !strings.Contains(string(response.Body), `"features":[]`) {
		t.Fatalf("empty catalog body = %s, want an empty features array", response.Body)
	}
	client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, "/v1/me/beta-features/demo_feature", map[string]any{"enabled": true})

	previous := identity.BetaFeatureKeys
	identity.BetaFeatureKeys = []string{"demo_feature", "preview_board"}
	t.Cleanup(func() { identity.BetaFeatureKeys = previous })

	client.must(http.StatusOK, http.MethodGet, "/v1/me/beta-features", nil, &list)
	if len(list.Features) != 2 || list.Features[0].Key != "demo_feature" || list.Features[0].Enabled || list.Features[1].Enabled {
		t.Fatalf("features = %+v, want two disabled features in catalog order", list.Features)
	}
	var toggled struct {
		Key     string `json:"key"`
		Enabled bool   `json:"enabled"`
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/beta-features/demo_feature", map[string]any{"enabled": true}, &toggled)
	if toggled.Key != "demo_feature" || !toggled.Enabled {
		t.Fatalf("toggled = %+v", toggled)
	}
	client.must(http.StatusOK, http.MethodGet, "/v1/me/beta-features", nil, &list)
	if !list.Features[0].Enabled || list.Features[1].Enabled {
		t.Fatalf("features after toggle = %+v", list.Features)
	}
	client.must(http.StatusOK, http.MethodPut, "/v1/me/beta-features/demo_feature", map[string]any{"enabled": false}, &toggled)
	if toggled.Enabled {
		t.Fatal("feature must be switched off")
	}
	client.expectError(http.StatusNotFound, "NOT_FOUND", http.MethodPut, "/v1/me/beta-features/unknown_feature", map[string]any{"enabled": true})
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPut, "/v1/me/beta-features/demo_feature", map[string]any{})

	// internal 与 beta 同样可用；standard 即使清单非空也 403。
	env.exec(`UPDATE openfox.users SET access_tier = 'internal' WHERE id = $1::uuid`, userID)
	client.must(http.StatusOK, http.MethodGet, "/v1/me/beta-features", nil, &list)
	env.exec(`UPDATE openfox.users SET access_tier = 'standard' WHERE id = $1::uuid`, userID)
	owner.expectError(http.StatusForbidden, "BETA_ONLY", http.MethodGet, "/v1/me/beta-features", nil)
}
