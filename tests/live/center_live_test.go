// Package live 针对已部署的中心做冒烟验证：只走公开 HTTP/WSS 接口，不直连数据库。
package live

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openfox/internal/daemon"
	"openfox/internal/daemon/supervisor"
	"openfox/internal/daemon/workdir"
	"openfox/internal/deviceauth"
	"openfox/internal/devices"
	"openfox/internal/runtime/codex"
)

// TestDeployedCenterDispatchesToLocalCodex 验证已部署中心的完整派发链路：
// 邮箱验证码登录 → 签发注册令牌 → 本机生成密钥注册临时电脑 → 挑战签名换设备会话 →
// 进程内 Daemon（WSS 心跳 + supervisor + 真实 Codex）→ 创建 Agent → @Agent 发消息 →
// 中心派发 → 本机执行 → 回复写回；再验证用户取消运行中的 Run。结束时归档 Agent、撤销电脑。
//
// 会写入目标中心的数据库并调用真实模型，只在显式配置以下变量时运行：
//
//	OFD_LIVE_CENTER        中心地址，含 /api 前缀，例如 http://127.0.0.1:28080/api（经 SSH 隧道）
//	OFD_LIVE_EMAIL         测试账号邮箱
//	OFD_LIVE_CODE          测试环境验证码
//	OFD_REAL_CODEX_VERSION 本机 Codex 版本（受支持版本矩阵）
//	OFD_LIVE_CONCURRENCY   可选，≥2 时追加并发阶段：电脑与 Agent 上限设为 N，组织 active_max 不足 N 时
//	                       临时调到 N（结束时恢复），同时发 N 个对话，验证 N 个 Run 同时 RUNNING 且各自成功
func TestDeployedCenterDispatchesToLocalCodex(t *testing.T) {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("OFD_LIVE_CENTER")), "/")
	email, code := strings.TrimSpace(os.Getenv("OFD_LIVE_EMAIL")), strings.TrimSpace(os.Getenv("OFD_LIVE_CODE"))
	version := strings.TrimSpace(os.Getenv("OFD_REAL_CODEX_VERSION"))
	if base == "" || email == "" || code == "" || version == "" {
		t.Skip("set OFD_LIVE_CENTER, OFD_LIVE_EMAIL, OFD_LIVE_CODE and OFD_REAL_CODEX_VERSION to run")
	}
	concurrency := 1
	if value := strings.TrimSpace(os.Getenv("OFD_LIVE_CONCURRENCY")); value != "" {
		if _, err := fmt.Sscanf(value, "%d", &concurrency); err != nil || concurrency < 1 || concurrency > 4 {
			t.Fatal("OFD_LIVE_CONCURRENCY must be 1..4")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	jar, _ := cookiejar.New(nil)
	user := &client{t: t, base: base, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}

	// 1. 用户登录（Cookie 会话 + CSRF）。
	user.call("POST", "/v1/auth/email/request", map[string]string{"email": email}, nil, 202)
	user.call("POST", "/v1/auth/email/verify", map[string]string{"email": email, "code": code, "client_kind": "web"}, nil, 201)
	baseURL, _ := url.Parse(base)
	for _, cookie := range jar.Cookies(baseURL) {
		if cookie.Name == "openfox_csrf" {
			user.csrf = cookie.Value
		}
	}
	var orgs struct {
		Organizations []struct{ ID, Name, Role string } `json:"organizations"`
	}
	user.call("GET", "/v1/me/organizations", nil, &orgs, 200)
	if len(orgs.Organizations) == 0 {
		t.Fatal("test account has no organization")
	}
	org := orgs.Organizations[0].ID
	var spaces struct {
		Spaces []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"spaces"`
	}
	user.call("GET", "/v1/organizations/"+org+"/spaces", nil, &spaces, 200)
	if len(spaces.Spaces) == 0 {
		t.Fatal("organization has no space")
	}
	space := spaces.Spaces[0].ID
	t.Logf("organization=%s (%s) space=%s (%s)", org, orgs.Organizations[0].Name, space, spaces.Spaces[0].Name)

	// 2. 注册临时电脑：注册令牌 → 公钥登记 → 挑战签名换设备会话。
	suffix := time.Now().Format("20060102-150405")
	var registration struct {
		Token string `json:"token"`
	}
	user.call("POST", "/v1/organizations/"+org+"/devices/registration-tokens",
		map[string]any{"name": "live-smoke-" + suffix, "install_id": newUUID(t), "max_slots": concurrency}, &registration, 201)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	anonymous := &client{t: t, base: base, http: &http.Client{Timeout: 30 * time.Second}}
	var registered struct {
		DeviceID    string `json:"device_id"`
		CenterEpoch int64  `json:"center_epoch"`
	}
	anonymous.call("POST", "/v1/device/register", map[string]string{
		"token": registration.Token, "key_id": "live", "public_key": deviceauth.EncodeBase64URL(publicKey),
	}, &registered, 201)
	device := registered.DeviceID
	// 无论成败都撤销临时电脑的授权，避免在测试中心留下可调度的电脑。
	defer user.call("POST", "/v1/organizations/"+org+"/devices/"+device+"/revoke", nil, nil, 204)
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
		Nonce       string `json:"nonce"`
		Challenge   string `json:"challenge"`
	}
	anonymous.call("POST", "/v1/device/auth/challenge", map[string]string{"device_id": device, "key_id": "live"}, &challenge, 0)
	nonce, err := deviceauth.DecodeBase64URL(challenge.Nonce, 0)
	if err != nil {
		t.Fatal("challenge nonce is not base64url")
	}
	challengeBytes, err := deviceauth.DecodeBase64URL(challenge.Challenge, 0)
	if err != nil {
		t.Fatal("challenge is not base64url")
	}
	signature := ed25519.Sign(privateKey, deviceauth.CanonicalPayload(challenge.ChallengeID, device, "live", nonce, challengeBytes))
	var session struct {
		Token string `json:"token"`
	}
	anonymous.call("POST", "/v1/device/auth/token", map[string]string{
		"challenge_id": challenge.ChallengeID, "device_id": device, "key_id": "live", "signature": deviceauth.EncodeBase64URL(signature),
	}, &session, 0)
	t.Logf("device=%s center_epoch=%d", device, registered.CenterEpoch)

	// 3. 进程内 Daemon：真实 Codex 适配器 + WSS 心跳（上报可用模型）+ supervisor。
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	adapter := codex.New("")
	adapter.SupportedVersions = []string{version}
	adapter.HomeRoot = filepath.Join(root, "codex-homes")
	models, err := adapter.ListModels(ctx)
	if err != nil || len(models) == 0 {
		t.Fatalf("ListModels = %v, %v", models, err)
	}
	selected := models[0].ID
	for _, model := range models {
		if model.IsDefault {
			selected = model.ID
		}
	}
	capabilities, _ := json.Marshal(map[string]any{"runtimes": map[string]any{"codex": map[string]any{
		"version": version, "login_status": "LOGGED_IN", "runnable": true, "models": models,
	}}})
	manager, err := workdir.NewManager(workdir.GuardPolicy{ManagedRoot: filepath.Join(root, "workspaces"), AllowNonGit: true})
	if err != nil {
		t.Fatal(err)
	}
	journal, err := supervisor.OpenJournal(ctx, filepath.Join(root, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	token := daemon.StaticTokenProvider(session.Token)
	runner, err := supervisor.New(supervisor.Config{
		Client: &supervisor.Client{BaseURL: base, Token: token}, Adapter: adapter, Workdir: manager,
		Journal: journal, Organizations: []string{org}, PollInterval: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := daemon.NewStaticHeartbeatSource(devices.Heartbeat{
		CenterEpoch: registered.CenterEpoch, ProtocolMajor: daemon.ProtocolMajor, DaemonVersion: "live-smoke",
		OperatingSystem: "darwin", Architecture: "arm64", Capabilities: capabilities,
	})
	remote, err := daemon.NewRemoteClient(daemon.RemoteClientConfig{
		URL:      strings.Replace(strings.Replace(base, "https://", "wss://", 1), "http://", "ws://", 1) + "/v1/device/connect",
		DeviceID: device, TokenProvider: token, HeartbeatInterval: 2 * time.Second, HeartbeatSource: source,
		OnRunMessage: runner.HandleMessage,
	})
	if err != nil {
		t.Fatal(err)
	}
	daemonContext, stopDaemon := context.WithCancel(ctx)
	remoteDone, runnerDone := make(chan error, 1), make(chan error, 1)
	go func() { remoteDone <- remote.Run(daemonContext) }()
	go func() { runnerDone <- runner.Run(daemonContext) }()
	defer func() {
		stopDaemon()
		<-remoteDone
		<-runnerDone
	}()

	// 4. 中心收到心跳：电脑在线，模型列表已保存，取得授权 ID。
	var grantID string
	waitFor(t, "device heartbeat stored with models", time.Minute, func() bool {
		var list struct {
			Devices []struct {
				ID           string          `json:"id"`
				GrantID      string          `json:"grant_id"`
				LastSeenAt   *time.Time      `json:"last_seen_at"`
				Capabilities json.RawMessage `json:"capabilities"`
			} `json:"devices"`
		}
		user.call("GET", "/v1/organizations/"+org+"/devices", nil, &list, 200)
		for _, item := range list.Devices {
			if item.ID == device && item.LastSeenAt != nil && bytes.Contains(item.Capabilities, []byte(selected)) {
				grantID = item.GrantID
				return true
			}
		}
		return false
	})
	t.Logf("grant=%s models=%d selected=%s", grantID, len(models), selected)

	// 5. 创建绑定这台电脑的 Agent；结束时归档。
	var agent struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	user.call("POST", "/v1/organizations/"+org+"/spaces/"+space+"/agents", map[string]any{
		"name": "Live smoke " + suffix, "kind": "CODE", "visibility": "PRIVATE",
		"config": map[string]any{
			"computer_id": device, "grant_id": grantID, "runtime": "codex", "model": selected,
			"max_concurrency": concurrency, "queue_timeout_seconds": 600, "run_timeout_seconds": 600, "directory_mode": "WORKSPACE",
		},
	}, &agent, 201)
	defer func() {
		var current struct {
			Version uint64 `json:"version"`
		}
		user.call("GET", "/v1/organizations/"+org+"/spaces/"+space+"/agents/"+agent.ID, nil, &current, 200)
		user.call("PATCH", "/v1/organizations/"+org+"/spaces/"+space+"/agents/"+agent.ID,
			map[string]any{"archived": true, "version": current.Version}, nil, 200)
	}()

	// 6. 正常执行：@Agent → 派发 → 本机 Codex → 回复写回。
	conversation := user.mention(org, space, agent.ID, "Reply with exactly: OK")
	run := user.waitRun(org, conversation, 10*time.Minute, "SUCCEEDED", "FAILED", "CANCELED")
	reply := user.agentReply(org, conversation)
	t.Logf("run=%s state=%s reply=%q", run.ID, run.State, reply)
	if run.State != "SUCCEEDED" || strings.TrimSpace(reply) != "OK" {
		t.Fatalf("run state = %s, reply = %q", run.State, reply)
	}

	// 7. 取消：长命令运行中被用户取消 → CANCELED。
	conversation2 := user.mention(org, space, agent.ID,
		"Run the shell command `sleep 97` in the current directory and wait for it to finish, then reply with exactly: DONE")
	run2 := user.waitRun(org, conversation2, 5*time.Minute, "RUNNING", "SUCCEEDED", "FAILED", "CANCELED")
	if run2.State != "RUNNING" {
		t.Fatalf("second run state = %s, want RUNNING", run2.State)
	}
	time.Sleep(15 * time.Second) // 让 Codex 真正进入命令执行
	started := time.Now()
	user.callWithHeaders("POST", "/v1/organizations/"+org+"/runs/"+run2.ID+"/cancel", nil, nil, 0,
		map[string]string{"Idempotency-Key": "live-cancel-" + suffix})
	run2 = user.waitRun(org, conversation2, 2*time.Minute, "CANCELED", "SUCCEEDED", "FAILED")
	t.Logf("cancel run=%s state=%s after=%s", run2.ID, run2.State, time.Since(started).Round(100*time.Millisecond))
	if run2.State != "CANCELED" {
		t.Fatalf("cancelled run state = %s", run2.State)
	}
	if concurrency >= 2 {
		user.concurrentRuns(org, space, agent.ID, concurrency)
	}
}

// concurrentRuns 同时向 n 个对话 @Agent，验证 n 个 Run 曾同时处于 RUNNING，且全部成功、回复各自正确。
// 组织 active_max 小于 n 时临时调高，结束时恢复原值。
func (c *client) concurrentRuns(org, space, agent string, n int) {
	c.t.Helper()
	type runtimeConfig struct {
		ConfigVersion int64          `json:"config_version"`
		Policy        map[string]any `json:"policy"`
	}
	var current runtimeConfig
	c.call("GET", "/v1/organizations/"+org+"/runtime-config", nil, &current, 200)
	original, _ := current.Policy["active_max"].(float64)
	if int(original) < n {
		policy := clonePolicy(current.Policy)
		policy["active_max"] = n
		var updated runtimeConfig
		c.call("PATCH", "/v1/organizations/"+org+"/runtime-config",
			map[string]any{"expected_version": current.ConfigVersion, "policy": policy}, &updated, 200)
		c.t.Logf("organization active_max %d -> %d (config_version %d)", int(original), n, updated.ConfigVersion)
		defer func() {
			var latest runtimeConfig
			c.call("GET", "/v1/organizations/"+org+"/runtime-config", nil, &latest, 200)
			restore := clonePolicy(latest.Policy)
			restore["active_max"] = int(original)
			c.call("PATCH", "/v1/organizations/"+org+"/runtime-config",
				map[string]any{"expected_version": latest.ConfigVersion, "policy": restore}, nil, 200)
			c.t.Logf("organization active_max restored to %d", int(original))
		}()
	}

	conversations := make([]string, n)
	for i := range conversations {
		conversations[i] = c.mention(org, space, agent, fmt.Sprintf(
			"Run the shell command `sleep 20` in the current directory and wait for it to finish, then reply with exactly: C%d", i+1))
	}
	// 每秒采样一次各对话最新 Run 的状态；记录同时 RUNNING 的最大数量。
	started := time.Now()
	peak := 0
	runs := make([]runView, n)
	waitFor(c.t, "concurrent runs finished", 10*time.Minute, func() bool {
		running, finished := 0, 0
		for i, conversation := range conversations {
			var page struct {
				Runs []runView `json:"runs"`
			}
			c.call("GET", "/v1/organizations/"+org+"/conversations/"+conversation+"/runs", nil, &page, 200)
			if len(page.Runs) == 0 {
				continue
			}
			runs[i] = page.Runs[len(page.Runs)-1]
			switch runs[i].State {
			case "RUNNING":
				running++
			case "SUCCEEDED", "FAILED", "CANCELED":
				finished++
			}
		}
		if running > peak {
			peak = running
		}
		return finished == n
	})
	c.t.Logf("concurrent runs=%d peak_running=%d elapsed=%s", n, peak, time.Since(started).Round(time.Second))
	for i, conversation := range conversations {
		reply := c.agentReply(org, conversation)
		c.t.Logf("concurrent run=%s state=%s reply=%q", runs[i].ID, runs[i].State, reply)
		if runs[i].State != "SUCCEEDED" || strings.TrimSpace(reply) != fmt.Sprintf("C%d", i+1) {
			c.t.Fatalf("concurrent run %d: state = %s, reply = %q", i+1, runs[i].State, reply)
		}
	}
	if peak < n {
		c.t.Fatalf("peak running = %d, want %d (runs did not overlap)", peak, n)
	}
}

func clonePolicy(policy map[string]any) map[string]any {
	clone := make(map[string]any, len(policy))
	for key, value := range policy {
		clone[key] = value
	}
	return clone
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

type runView struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

func (c *client) call(method, path string, body, out any, want int) {
	c.t.Helper()
	c.callWithHeaders(method, path, body, out, want, nil)
}

// callWithHeaders 发送请求并检查状态码（want=0 表示接受任意 2xx）。错误信息只含响应体，不含凭据。
func (c *client) callWithHeaders(method, path string, body, out any, want int, headers map[string]string) {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" && method != "GET" {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.http.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if (want != 0 && response.StatusCode != want) || (want == 0 && response.StatusCode/100 != 2) {
		c.t.Fatalf("%s %s = %d: %s", method, path, response.StatusCode, payload)
	}
	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			c.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
}

func (c *client) mention(org, space, agent, body string) string {
	c.t.Helper()
	conversation := newUUID(c.t)
	c.call("POST", "/v1/organizations/"+org+"/conversations",
		map[string]any{"id": conversation, "space_id": space, "agent_id": agent, "visibility": "PRIVATE"}, nil, 201)
	message := newUUID(c.t)
	c.call("POST", "/v1/organizations/"+org+"/conversations/"+conversation+"/messages", map[string]any{
		"id": message, "client_message_id": "live-" + message, "body": body,
		"mentions": []map[string]string{{"agent_id": agent}},
	}, nil, 201)
	return conversation
}

func (c *client) waitRun(org, conversation string, limit time.Duration, states ...string) runView {
	c.t.Helper()
	var run runView
	waitFor(c.t, "run state in "+strings.Join(states, "/"), limit, func() bool {
		var page struct {
			Runs []runView `json:"runs"`
		}
		c.call("GET", "/v1/organizations/"+org+"/conversations/"+conversation+"/runs", nil, &page, 200)
		if len(page.Runs) == 0 {
			return false
		}
		run = page.Runs[len(page.Runs)-1]
		for _, state := range states {
			if run.State == state {
				return true
			}
		}
		return false
	})
	return run
}

func (c *client) agentReply(org, conversation string) string {
	c.t.Helper()
	var page struct {
		Messages []struct {
			AuthorKind string `json:"author_kind"`
			Body       string `json:"body"`
		} `json:"messages"`
	}
	c.call("GET", "/v1/organizations/"+org+"/conversations/"+conversation+"/messages", nil, &page, 200)
	reply := ""
	for _, message := range page.Messages {
		if message.AuthorKind == "AGENT" {
			reply = message.Body
		}
	}
	return reply
}

func waitFor(t *testing.T, what string, limit time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
