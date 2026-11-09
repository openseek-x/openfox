package stage3

// 个人资料、偏好与快捷键集成测试：资料、头像、偏好、修改邮箱四步。
// 复用本目录的夹具（真实 HTTP 处理器、登录会话、租户事务）。
// 需要发信的用例用 withMail 换成发件替身，从邮件正文里取验证码（万能验证码未使用）。
// 头像用例用 withAvatarStorage 把存储指向 t.TempDir()。

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"openfox/internal/api"
	"openfox/internal/artifacts"
	"openfox/internal/identity"
)

type profileMe struct {
	ID        string  `json:"id"`
	Email     string  `json:"email"`
	FullName  string  `json:"full_name"`
	Bio       string  `json:"bio"`
	AvatarURL *string `json:"avatar_url"`
}

type profileChallenge struct {
	ChallengeID string    `json:"challenge_id"`
	ExpiresAt   time.Time `json:"expires_at"`
	ResendAfter time.Time `json:"resend_after"`
}

type profilePrefs struct {
	Theme                     string            `json:"theme"`
	Density                   string            `json:"density"`
	Locale                    *string           `json:"locale"`
	OutlineColor              int               `json:"outline_color"`
	OpenSessionOnAssign       bool              `json:"open_session_on_assign"`
	OpenSessionOnQuickCommand bool              `json:"open_session_on_quick_command"`
	SendShortcut              string            `json:"send_shortcut"`
	CommandShortcuts          map[string]string `json:"command_shortcuts"`
	Version                   int64             `json:"version"`
}

type profileMember struct {
	UserID    string  `json:"user_id"`
	FullName  string  `json:"full_name"`
	AvatarURL *string `json:"avatar_url"`
}

var mailCodePattern = regexp.MustCompile(`验证码是 (\d{6})`)

// withAvatarStorage 换用带本地存储的服务器，并清空已登录的客户端（会话在库里，重新登录即可）。
func withAvatarStorage(env *s3Env, root string) {
	env.t.Helper()
	env.server = httptestServer(env, api.NewWithDispatch(env.auth, env.database, false, artifacts.Local{Root: root},
		api.DefaultAuthRateLimits, api.DispatchOptions{}))
	env.clients = map[string]*s3Client{}
}

// withEmailLogin 换用带发件替身与邮箱验证码持久层的服务器（登录验证码需要 EmailStore，夹具默认没有）。
func withEmailLogin(env *s3Env) *recordingMail {
	env.t.Helper()
	mail := &recordingMail{bodies: map[string]string{}}
	auth := env.auth
	auth.EmailSender = mail
	auth.EmailStore = identity.PostgresStore{Pool: env.database}
	env.server = httptestServer(env, api.NewWithDispatch(auth, env.database, false, nil, api.DefaultAuthRateLimits, api.DispatchOptions{}))
	env.clients = map[string]*s3Client{}
	return mail
}

// secondClient 为同一用户再登录一次得到独立会话；缓存中的第一个客户端保持不变。
func secondClient(env *s3Env, userID string) *s3Client {
	first := env.client(userID)
	delete(env.clients, userID)
	second := env.client(userID)
	env.clients[userID] = first
	return second
}

// mailCode 取最近一封发给 to 的邮件里的 6 位验证码。
func mailCode(t *testing.T, mail *recordingMail, to string) string {
	t.Helper()
	mail.mu.Lock()
	defer mail.mu.Unlock()
	match := mailCodePattern.FindStringSubmatch(mail.bodies[to])
	if match == nil {
		t.Fatalf("no verification code mailed to %s: %q", to, mail.bodies[to])
	}
	return match[1]
}

// imageBytes 返回以文件头开始、长度为 size 的字节串（只用于按文件头判断格式）。
func imageBytes(header string, size int) []byte {
	data := make([]byte, size)
	copy(data, header)
	return data
}

const (
	pngHeader  = "\x89PNG\r\n\x1a\n"
	jpegHeader = "\xff\xd8\xff\xe0"
	webpHeader = "RIFF\x00\x00\x00\x00WEBPVP8 "
)

// uploadAvatar 以 multipart 字段 file 上传头像；响应体原样返回，由调用方断言。
func uploadAvatar(t *testing.T, client *s3Client, data []byte) s3Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "avatar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(client.env.ctx, http.MethodPut, client.server.URL+"/v1/me/avatar", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-CSRF-Token", client.csrf)
	response, err := client.http.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return s3Response{Status: response.StatusCode, Header: response.Header, Body: payload}
}

// completeEmailChange 以 client 身份走完四步，把邮箱改为 newEmail，返回 Me。
func completeEmailChange(t *testing.T, mail *recordingMail, client *s3Client, currentEmail, newEmail string) profileMe {
	t.Helper()
	var started profileChallenge
	client.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/start", nil, &started)
	client.must(http.StatusNoContent, http.MethodPost, "/v1/me/email-change/verify-current",
		map[string]any{"challenge_id": started.ChallengeID, "code": mailCode(t, mail, currentEmail)}, nil)
	var requested profileChallenge
	client.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/request-new",
		map[string]any{"challenge_id": started.ChallengeID, "email": newEmail}, &requested)
	var me profileMe
	client.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/confirm",
		map[string]any{"challenge_id": requested.ChallengeID, "code": mailCode(t, mail, newEmail)}, &me)
	return me
}

func TestProfileSaveAndValidate(t *testing.T) {
	env := newS3Env(t)
	user := env.newUser()
	client := env.client(user)
	var me profileMe
	client.must(http.StatusOK, http.MethodPatch, "/v1/me",
		map[string]any{"full_name": "  Alice Liu  ", "bio": "hello"}, &me)
	if me.FullName != "Alice Liu" || me.Bio != "hello" || me.AvatarURL != nil {
		t.Fatalf("saved profile = %+v", me)
	}
	client.must(http.StatusOK, http.MethodPatch, "/v1/me", map[string]any{"full_name": strings.Repeat("名", 80)}, &me)
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me", map[string]any{"full_name": "   "})
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me", map[string]any{"full_name": strings.Repeat("名", 81)})
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me", map[string]any{"bio": strings.Repeat("x", 281)})
	client.must(http.StatusOK, http.MethodGet, "/v1/me", nil, &me)
	if me.FullName != strings.Repeat("名", 80) || me.Email != s3Email(user) {
		t.Fatalf("profile after rejected updates = %+v", me)
	}
}

func TestProfileShowsInMembersList(t *testing.T) {
	env := newS3Env(t)
	user := env.newUser()
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`, env.org, user)
	env.client(user).must(http.StatusOK, http.MethodPatch, "/v1/me", map[string]any{"full_name": "Bob Chen"}, nil)
	var body struct {
		Members []profileMember `json:"members"`
	}
	env.owner().must(http.StatusOK, http.MethodGet, "members", nil, &body)
	for _, member := range body.Members {
		if member.UserID == user {
			if member.FullName != "Bob Chen" || member.AvatarURL != nil {
				t.Fatalf("member = %+v", member)
			}
			return
		}
	}
	t.Fatalf("member %s missing from list", user)
}

func TestAvatarUploadAndAccess(t *testing.T) {
	env := newS3Env(t)
	root := t.TempDir()
	withAvatarStorage(env, root)
	// 夹具直接插入的组织没有 organization_directory 行；头像的同组织判断依赖它（生产环境由建组织的函数写入）。
	env.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
		SELECT id, slug, name, created_at FROM openfox.organizations WHERE id = $1::uuid ON CONFLICT DO NOTHING`, env.org)
	uploader := env.newUser()
	env.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`, env.org, uploader)
	client := env.client(uploader)

	for _, header := range []string{pngHeader, jpegHeader, webpHeader} {
		response := uploadAvatar(t, client, imageBytes(header, 2048))
		if response.Status != http.StatusOK {
			t.Fatalf("upload %q = %d %s", header[:4], response.Status, response.Body)
		}
	}
	png := imageBytes(pngHeader, 4096)
	response := uploadAvatar(t, client, png)
	if response.Status != http.StatusOK {
		t.Fatalf("upload png = %d %s", response.Status, response.Body)
	}
	var me profileMe
	response.decode(t, &me)
	if me.AvatarURL == nil || !strings.HasPrefix(*me.AvatarURL, "/api/v1/users/"+uploader+"/avatar?v=") {
		t.Fatalf("avatar_url = %v", me.AvatarURL)
	}

	path := "/v1/users/" + uploader + "/avatar"
	self := client.must(http.StatusOK, http.MethodGet, path, nil, nil)
	if !bytes.Equal(self.Body, png) || self.Header.Get("Content-Type") != "image/png" ||
		self.Header.Get("Cache-Control") != "private, max-age=86400" || self.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("self avatar headers = %v", self.Header)
	}
	env.member().must(http.StatusOK, http.MethodGet, path, nil, nil)
	env.outsider().expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, path, nil)

	noAvatar := env.newUser()
	env.client(noAvatar).expectError(http.StatusNotFound, "NOT_FOUND", http.MethodGet, "/v1/users/"+noAvatar+"/avatar", nil)

	env.client(uploader).expectError(http.StatusBadRequest, "INVALID_AVATAR", http.MethodPut, "/v1/me/avatar", nil)
	if rejected := uploadAvatar(t, client, []byte("hello world, not an image")); rejected.Status != http.StatusBadRequest || rejected.code() != "INVALID_AVATAR" {
		t.Fatalf("non-image upload = %d %s", rejected.Status, rejected.Body)
	}
	if tooLarge := uploadAvatar(t, client, imageBytes(pngHeader, 5<<20+1)); tooLarge.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload = %d %s", tooLarge.Status, tooLarge.Body)
	}

	var oldKey string
	env.scalar(&oldKey, `SELECT avatar_key FROM openfox.users WHERE id = $1::uuid`, uploader)
	if replaced := uploadAvatar(t, client, imageBytes(webpHeader, 3000)); replaced.Status != http.StatusOK {
		t.Fatalf("replace avatar = %d %s", replaced.Status, replaced.Body)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(oldKey))); !os.IsNotExist(err) {
		t.Fatalf("previous avatar file still exists after replacement: %v", err)
	}
	// 同内容重传命中已存在的键（存储返回 ErrExist）：仍然成功，且当前头像文件不能被删掉。
	var currentKey string
	env.scalar(&currentKey, `SELECT avatar_key FROM openfox.users WHERE id = $1::uuid`, uploader)
	if again := uploadAvatar(t, client, imageBytes(webpHeader, 3000)); again.Status != http.StatusOK {
		t.Fatalf("re-upload identical avatar = %d %s", again.Status, again.Body)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(currentKey))); err != nil {
		t.Fatalf("current avatar file missing after identical re-upload: %v", err)
	}
	client.must(http.StatusOK, http.MethodGet, path, nil, nil)
}

func TestAvatarStorageUnavailable(t *testing.T) {
	env := newS3Env(t)
	user := env.newUser()
	client := env.client(user)
	response := uploadAvatar(t, client, imageBytes(pngHeader, 1024))
	if response.Status != http.StatusServiceUnavailable || response.code() != "STORAGE_UNAVAILABLE" {
		t.Fatalf("upload without storage = %d %s", response.Status, response.Body)
	}
}

func TestPreferencesDefaultsAndSave(t *testing.T) {
	env := newS3Env(t)
	user := env.newUser()
	client := env.client(user)
	var prefs profilePrefs
	client.must(http.StatusOK, http.MethodGet, "/v1/me/preferences", nil, &prefs)
	if prefs.Version != 0 || prefs.Theme != "system" || prefs.Density != "comfortable" || prefs.Locale != nil || prefs.OutlineColor != 0 ||
		!prefs.OpenSessionOnAssign || !prefs.OpenSessionOnQuickCommand || prefs.SendShortcut != "enter" || len(prefs.CommandShortcuts) != 0 {
		t.Fatalf("defaults = %+v", prefs)
	}

	client.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences",
		map[string]any{"version": 0, "theme": "dark", "locale": "zh-CN", "outline_color": 3, "send_shortcut": "shift_enter"}, &prefs)
	if prefs.Version != 1 || prefs.Theme != "dark" || prefs.Locale == nil || *prefs.Locale != "zh-CN" || prefs.OutlineColor != 3 || prefs.SendShortcut != "shift_enter" {
		t.Fatalf("first save = %+v", prefs)
	}
	client.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "/v1/me/preferences",
		map[string]any{"version": 0, "theme": "light"})
	client.expectError(http.StatusConflict, "VERSION_CONFLICT", http.MethodPatch, "/v1/me/preferences",
		map[string]any{"version": 5, "theme": "light"})

	client.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 1, "theme": "light"}, &prefs)
	if prefs.Version != 2 || prefs.Theme != "light" || prefs.OutlineColor != 3 || prefs.Locale == nil || *prefs.Locale != "zh-CN" {
		t.Fatalf("partial patch = %+v", prefs)
	}
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 2, "theme": "neon"})
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me/preferences", map[string]any{"version": 2, "unknown": 1})
	client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me/preferences", map[string]any{"theme": "dark"})
	env.scalar(&prefs.Version, `SELECT version FROM openfox.user_preferences WHERE user_id = $1::uuid`, user)
	if prefs.Version != 2 {
		t.Fatalf("stored version = %d, want 2", prefs.Version)
	}
}

func TestPreferencesShortcuts(t *testing.T) {
	env := newS3Env(t)
	client := env.client(env.newUser())
	// 每次请求前清空解码目标：encoding/json 会把新对象合并进已有的 map。
	var prefs profilePrefs
	fresh := func() { prefs = profilePrefs{} }
	client.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences",
		map[string]any{"version": 0, "command_shortcuts": map[string]string{"command_menu": "Shift+Mod+J"}}, &prefs)
	if len(prefs.CommandShortcuts) != 1 || prefs.CommandShortcuts["command_menu"] != "mod+shift+j" {
		t.Fatalf("normalized shortcuts = %v", prefs.CommandShortcuts)
	}

	// 与默认相同的值不入库；未出现的命令回到默认。
	fresh()
	client.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences",
		map[string]any{"version": 1, "command_shortcuts": map[string]string{"command_menu": "mod+k", "move_to_space": "mod+j"}}, &prefs)
	if len(prefs.CommandShortcuts) != 1 || prefs.CommandShortcuts["move_to_space"] != "mod+j" {
		t.Fatalf("replaced shortcuts = %v", prefs.CommandShortcuts)
	}

	conflicts := []map[string]string{
		{"find_in_view": "mod+shift+m"},
		{"command_menu": "mod+j", "move_to_space": "mod+j"},
	}
	for _, shortcuts := range conflicts {
		client.expectError(http.StatusBadRequest, "SHORTCUT_CONFLICT", http.MethodPatch, "/v1/me/preferences",
			map[string]any{"version": 2, "command_shortcuts": shortcuts})
	}
	invalid := []map[string]string{
		{"command_menu": "ctrl+k"},
		{"command_menu": "mod+ab"},
		{"unknown_command": "mod+j"},
		{"command_menu": "mod"},
	}
	for _, shortcuts := range invalid {
		client.expectError(http.StatusBadRequest, "INVALID_REQUEST", http.MethodPatch, "/v1/me/preferences",
			map[string]any{"version": 2, "command_shortcuts": shortcuts})
	}

	// command_shortcuts 整体替换：只带 find_in_view 时 move_to_space 回到默认。
	fresh()
	client.must(http.StatusOK, http.MethodPatch, "/v1/me/preferences",
		map[string]any{"version": 2, "command_shortcuts": map[string]string{"find_in_view": "mod+j"}}, &prefs)
	if len(prefs.CommandShortcuts) != 1 || prefs.CommandShortcuts["find_in_view"] != "mod+j" || prefs.Version != 3 {
		t.Fatalf("whole-object replace = %+v", prefs)
	}
}

func TestEmailChangeFullFlow(t *testing.T) {
	env := newS3Env(t)
	mail := withEmailLogin(env)
	user := env.newUser()
	client := env.client(user)
	current := s3Email(user)
	newEmail := "s3-moved-" + s3Suffix(t) + "@example.test"

	me := completeEmailChange(t, mail, client, current, newEmail)
	if me.Email != newEmail || me.ID != user {
		t.Fatalf("me after change = %+v", me)
	}
	var stored string
	env.scalar(&stored, `SELECT email FROM openfox.users WHERE id = $1::uuid`, user)
	if stored != newEmail {
		t.Fatalf("stored email = %s, want %s", stored, newEmail)
	}

	// 之后用新邮箱能登录：邮箱验证码登录取到的是新邮件里的码。
	anonymous := &s3Client{env: env, http: &http.Client{}, server: env.server}
	if status := anonymous.doIn("", http.MethodPost, "/v1/auth/email/request", map[string]any{"email": newEmail}).Status; status != http.StatusAccepted {
		t.Fatalf("email login request = %d", status)
	}
	var login struct {
		UserID string `json:"user_id"`
	}
	anonymous.must(http.StatusCreated, http.MethodPost, "/v1/auth/email/verify",
		map[string]any{"email": newEmail, "code": mailCode(t, mail, newEmail), "client_kind": "web"}, &login)
	if login.UserID != user {
		t.Fatalf("login after change = %+v", login)
	}
}

func TestEmailChangeRevokesOtherSessions(t *testing.T) {
	env := newS3Env(t)
	mail := withMail(env)
	user := env.newUser()
	current := env.client(user)
	other := secondClient(env, user)
	newEmail := "s3-revoke-" + s3Suffix(t) + "@example.test"

	completeEmailChange(t, mail, current, s3Email(user), newEmail)
	current.must(http.StatusOK, http.MethodGet, "/v1/me", nil, nil)
	other.expectError(http.StatusUnauthorized, "UNAUTHENTICATED", http.MethodGet, "/v1/me", nil)
}

func TestEmailChangeRejectsMisuse(t *testing.T) {
	env := newS3Env(t)
	mail := withMail(env)

	// 错误验证码：返回 400 并计数；用尽后即使码正确也作废。
	wrongUser := env.newUser()
	client := env.client(wrongUser)
	var started profileChallenge
	client.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/start", nil, &started)
	correct := mailCode(t, mail, s3Email(wrongUser))
	wrong := "000000"
	if correct == wrong {
		wrong = "111111"
	}
	client.expectError(http.StatusBadRequest, "CODE_INVALID", http.MethodPost, "/v1/me/email-change/verify-current",
		map[string]any{"challenge_id": started.ChallengeID, "code": wrong})
	var attempts int
	env.scalar(&attempts, `SELECT attempt_count FROM openfox.email_change_challenges WHERE id = $1::uuid`, started.ChallengeID)
	if attempts != 1 {
		t.Fatalf("attempt_count after one wrong code = %d", attempts)
	}
	for range 4 {
		client.expectError(http.StatusBadRequest, "CODE_INVALID", http.MethodPost, "/v1/me/email-change/verify-current",
			map[string]any{"challenge_id": started.ChallengeID, "code": wrong})
	}
	client.expectError(http.StatusBadRequest, "CODE_INVALID", http.MethodPost, "/v1/me/email-change/verify-current",
		map[string]any{"challenge_id": started.ChallengeID, "code": correct})

	// 跨用户的 challenge_id 一律 CODE_INVALID。
	owner := env.newUser()
	ownerClient := env.client(owner)
	var ownerStart profileChallenge
	ownerClient.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/start", nil, &ownerStart)
	env.client(env.newUser()).expectError(http.StatusBadRequest, "CODE_INVALID", http.MethodPost, "/v1/me/email-change/verify-current",
		map[string]any{"challenge_id": ownerStart.ChallengeID, "code": mailCode(t, mail, s3Email(owner))})

	// 没有完成第 2 步就走第 3 步。
	skipped := env.newUser()
	skippedClient := env.client(skipped)
	var skippedStart profileChallenge
	skippedClient.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/start", nil, &skippedStart)
	skippedClient.expectError(http.StatusBadRequest, "CODE_INVALID", http.MethodPost, "/v1/me/email-change/request-new",
		map[string]any{"challenge_id": skippedStart.ChallengeID, "email": "s3-skip@example.test"})

	// 新邮箱与当前相同 400；被其他用户占用 409。
	taken := env.newUser()
	takenClient := env.client(taken)
	var takenStart profileChallenge
	takenClient.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/start", nil, &takenStart)
	takenClient.must(http.StatusNoContent, http.MethodPost, "/v1/me/email-change/verify-current",
		map[string]any{"challenge_id": takenStart.ChallengeID, "code": mailCode(t, mail, s3Email(taken))}, nil)
	takenClient.expectError(http.StatusBadRequest, "INVALID_EMAIL", http.MethodPost, "/v1/me/email-change/request-new",
		map[string]any{"challenge_id": takenStart.ChallengeID, "email": strings.ToUpper(s3Email(taken))})
	takenClient.expectError(http.StatusConflict, "EMAIL_TAKEN", http.MethodPost, "/v1/me/email-change/request-new",
		map[string]any{"challenge_id": takenStart.ChallengeID, "email": s3Email(s3OtherID)})

	// 重新发送冷却：同一用户 1 分钟内第二次发送返回 429 并带 Retry-After。
	cooldown := env.newUser()
	cooldownClient := env.client(cooldown)
	cooldownClient.must(http.StatusOK, http.MethodPost, "/v1/me/email-change/start", nil, nil)
	limited := cooldownClient.expectError(http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", http.MethodPost, "/v1/me/email-change/start", nil)
	if limited.Header.Get("Retry-After") == "" {
		t.Fatal("rate-limited response has no Retry-After header")
	}
}

func TestEmailChangeUnavailableWithoutDelivery(t *testing.T) {
	env := newS3Env(t)
	client := env.client(env.newUser())
	client.expectError(http.StatusServiceUnavailable, "EMAIL_LOGIN_UNAVAILABLE", http.MethodPost, "/v1/me/email-change/start", nil)
}
