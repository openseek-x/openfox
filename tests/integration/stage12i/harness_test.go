package stage12i

// 阶段 12i「真实进程演练」夹具：与其他集成包不同，这里的中心与 daemon 都是**独立子进程**
// （编译出的 openfox-server 与 openfox 二进制），因此可以对它们发送 SIGKILL / SIGTERM，
// 验证 daemon 崩溃恢复（12a 整链）、优雅停止（K-05）与备份恢复（K-06、NF-09）。
//
// 隔离约束（本机可能有用户正在使用的真实 OpenFox daemon 与数据）：
//   - 所有子进程的 HOME、TMPDIR、CODEX_HOME 都指向本次测试的临时目录（/tmp/of12i-*），不读写真实 HOME；
//   - daemon 只以前台 `openfox daemon run --config <临时目录>/d/config.json` 运行，socket、journal、
//     工作区都落在该配置目录下；从不调用 `daemon install/start/stop/restart` 与 `computer register`
//     （后者在 macOS 上会安装 launchd 服务），电脑注册改走 HTTP 接口并用库函数写本机配置；
//   - 中心只监听 127.0.0.1 的随机端口；需要自建数据库的用例用 initdb 建在临时目录、只开 Unix socket；
//   - 只对本夹具启动并记录的进程发信号，对 Runtime 进程发信号前核对其命令行属于本次临时目录。

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "modernc.org/sqlite"

	"openfox/internal/daemon"
	"openfox/internal/deviceauth"
	"openfox/internal/identity"
)

const (
	drillOwnerID  = "00000000-0000-0000-0000-00000012a901"
	drillMemberID = "00000000-0000-0000-0000-00000012a902"
	// drillOutsiderID 属于另一个组织，用于租户隔离断言。
	drillOutsiderID = "00000000-0000-0000-0000-00000012a903"
	drillPassword   = "stage12i drills password"
	// 中心租约参数取配置允许的最小值：续租 5 秒、租约 15 秒，核对周期约 15～20 秒。
	leaseRenew = "5s"
	leaseTTL   = "15s"
)

var (
	buildOnce sync.Once
	buildDir  string
	buildErr  error
	hashOnce  sync.Once
	hashValue string
	hashErr   error
)

// binaries 编译一次 openfox-server 与 openfox，供本包全部用例共用。
func binaries(t *testing.T) (server, cli string) {
	t.Helper()
	buildOnce.Do(func() {
		buildDir, buildErr = os.MkdirTemp("/tmp", "of12i-bin-")
		if buildErr != nil {
			return
		}
		for name, pkg := range map[string]string{"openfox-server": "openfox/cmd/openfox-server", "openfox": "openfox/cmd/openfox"} {
			if output, err := exec.Command("go", "build", "-o", filepath.Join(buildDir, name), pkg).CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("go build %s: %v\n%s", pkg, err, output)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return filepath.Join(buildDir, "openfox-server"), filepath.Join(buildDir, "openfox")
}

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		_ = os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

func passwordHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		hasher, err := identity.NewArgon2idHasher(identity.Argon2idParams{MemoryKiB: 65536, Iterations: 3, Parallelism: 1, KeyLength: 32, SaltLength: 16})
		if err != nil {
			hashErr = err
			return
		}
		hashValue, hashErr = hasher.Hash(drillPassword)
	})
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	return hashValue
}

func newID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	text := hex.EncodeToString(raw[:])
	return text[0:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

func emailOf(userID string) string { return "s12i-" + userID[len(userID)-4:] + "@example.test" }

// ---- 子进程 ----

// proc 是本夹具启动的一个子进程；只对它（或经核对属于本次临时目录的 Runtime 进程）发信号。
type proc struct {
	name string
	cmd  *exec.Cmd
	done chan struct{}
	err  error
	log  string
}

func startProc(t *testing.T, name, logPath string, env []string, binary string, args ...string) *proc {
	t.Helper()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, args...)
	cmd.Env, cmd.Stdout, cmd.Stderr = env, logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	p := &proc{name: name, cmd: cmd, done: make(chan struct{}), log: logPath}
	go func() {
		p.err = cmd.Wait()
		_ = logFile.Close()
		close(p.done)
	}()
	t.Logf("启动 %s pid=%d", name, cmd.Process.Pid)
	t.Cleanup(func() { p.stop(10 * time.Second) })
	return p
}

func (p *proc) pid() int { return p.cmd.Process.Pid }

func (p *proc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// signalAndWait 发送信号并等待退出，返回耗时；超时返回 false（不升级，由调用方决定）。
func (p *proc) signalAndWait(sig syscall.Signal, limit time.Duration) (time.Duration, bool) {
	begin := time.Now()
	if !p.exited() {
		_ = p.cmd.Process.Signal(sig)
	}
	select {
	case <-p.done:
		return time.Since(begin), true
	case <-time.After(limit):
		return time.Since(begin), false
	}
}

// stop 用于清理：SIGTERM，超时后 SIGKILL。
func (p *proc) stop(limit time.Duration) {
	if p == nil || p.exited() {
		return
	}
	if _, ok := p.signalAndWait(syscall.SIGTERM, limit); !ok {
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// ---- PostgreSQL 临时集群（需要停库或全新目标库的用例使用）----

type cluster struct {
	t      *testing.T
	data   string
	socket string
	port   int
	up     bool
}

func pgTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"initdb", "pg_ctl", "createdb", "pg_dump", "pg_restore", "psql"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("PostgreSQL tool %s is not available", name)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("TCP listeners unavailable: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// newCluster 在 dir 下 initdb 一个只开 Unix socket 的临时集群（不监听 TCP）。
func newCluster(t *testing.T, dir string) *cluster {
	t.Helper()
	pgTools(t)
	c := &cluster{t: t, data: filepath.Join(dir, "data"), socket: filepath.Join(dir, "s"), port: freePort(t)}
	if err := os.MkdirAll(c.socket, 0o700); err != nil {
		t.Fatal(err)
	}
	c.run("initdb", "-D", c.data, "-U", "postgres", "--auth=trust", "--no-locale", "--encoding=UTF8",
		"-c", "shared_memory_type=mmap", "-c", "dynamic_shared_memory_type=mmap")
	c.start()
	t.Cleanup(c.stop)
	return c
}

func (c *cluster) env() []string {
	return append(os.Environ(), "PGHOST="+c.socket, "PGPORT="+strconv.Itoa(c.port), "PGUSER=postgres")
}

func (c *cluster) run(name string, args ...string) string {
	c.t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = c.env()
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}

func (c *cluster) start() {
	c.t.Helper()
	c.run("pg_ctl", "-D", c.data, "-w", "start", "-l", filepath.Join(filepath.Dir(c.data), "postgres.log"),
		"-o", fmt.Sprintf("-k %s -p %d -c listen_addresses='' -c fsync=off -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap", c.socket, c.port))
	c.up = true
}

func (c *cluster) stop() {
	if !c.up {
		return
	}
	cmd := exec.Command("pg_ctl", "-D", c.data, "-w", "stop", "-m", "fast")
	cmd.Env = c.env()
	_, _ = cmd.CombinedOutput()
	c.up = false
}

func (c *cluster) dsn(database string) string {
	return fmt.Sprintf("user=postgres host=%s port=%d dbname=%s sslmode=disable", c.socket, c.port, database)
}

// ---- 演练环境 ----

type stack struct {
	t         *testing.T
	ctx       context.Context
	dir       string // /tmp/of12i-XXXX：本次演练的全部文件
	dsn       string // 超级用户连接串（只用于造数据与取证）
	superuser *pgx.Conn
	serverBin string
	cliBin    string
	listen    string
	center    string
	storage   string
	urlFile   string
	server    *proc
	org       string
	clients   map[string]*client
}

type client struct {
	s    *stack
	http *http.Client
	csrf string
}

// newStack 建立演练目录并连接数据库（dsn 为空时取 OPENFOX_TEST_DATABASE_URL），不启动任何进程。
func newStack(t *testing.T, dsn string, limit time.Duration) *stack {
	t.Helper()
	if os.Getenv("OPENFOX_TEST_DATABASE_URL") == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	if dsn == "" {
		dsn = os.Getenv("OPENFOX_TEST_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	t.Cleanup(cancel)
	// Unix socket 路径上限约 104 字节：演练目录放在 /tmp 下的短路径。
	dir, err := os.MkdirTemp("/tmp", "of12i-")
	if err != nil {
		t.Fatal(err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() && os.Getenv("OPENFOX_STAGE12I_KEEP") != "" {
			t.Logf("保留演练目录 %s", dir)
			return
		}
		_ = os.RemoveAll(dir)
	})
	s := &stack{t: t, ctx: ctx, dir: dir, dsn: dsn, storage: filepath.Join(dir, "storage"), urlFile: filepath.Join(dir, "database.url"),
		org: newID(t), clients: map[string]*client{}}
	s.serverBin, s.cliBin = binaries(t)
	for _, sub := range []string{"home", "tmp", "storage", "user-codex", "state"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "user-codex", "auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.urlFile, []byte(dsn+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 兜底清理（在停止中心与 daemon 之后执行）：用例中途失败时，被强杀的 daemon 留下的假 Runtime 不归任何进程管，
	// 这里按命令行（含本次演练目录下的脚本路径）找出并结束其进程组。
	t.Cleanup(s.reapRuntimes)
	s.listen = fmt.Sprintf("127.0.0.1:%d", freePort(t))
	s.center = "http://" + s.listen
	s.connect()
	return s
}

func (s *stack) connect() {
	s.t.Helper()
	conn, err := pgx.Connect(s.ctx, s.dsn)
	if err != nil {
		s.t.Fatal(err)
	}
	s.superuser = conn
	s.t.Cleanup(func() { _ = conn.Close(context.Background()) })
}

// childEnv 是全部子进程共用的环境：临时 HOME / TMPDIR / CODEX_HOME；其他 Runtime 的可执行文件
// 显式指向不存在的路径，避免探测到本机真实安装的 CLI；不传 SHELL（不启动登录 shell 探测）。
func (s *stack) childEnv(extra ...string) []string {
	missing := filepath.Join(s.dir, "missing-runtime")
	env := []string{
		"HOME=" + filepath.Join(s.dir, "home"), "PATH=" + os.Getenv("PATH"), "TMPDIR=" + filepath.Join(s.dir, "tmp"),
		"CODEX_HOME=" + filepath.Join(s.dir, "user-codex"), "LANG=C",
		"OPENFOX_CLAUDE_PATH=" + missing, "OPENFOX_OPENCODE_PATH=" + missing, "OPENFOX_PI_PATH=" + missing,
	}
	return append(env, extra...)
}

func (s *stack) serverEnv() []string {
	return s.childEnv(
		"OPENFOX_SERVER_PUBLIC_URL="+s.center, "OPENFOX_ENV=test", "OPENFOX_SERVER_LISTEN="+s.listen, "OPENFOX_DATABASE_URL_FILE="+s.urlFile,
		"OPENFOX_STORAGE_ROOT="+s.storage, "OPENFOX_SCHEDULER_POLL_INTERVAL=200ms",
		"OPENFOX_RUN_LEASE_RENEW="+leaseRenew, "OPENFOX_RUN_LEASE_TTL="+leaseTTL,
		"OPENFOX_DATABASE_STATEMENT_TIMEOUT=10s", "OPENFOX_DATABASE_LOCK_TIMEOUT=5s",
		"OPENFOX_AUTH_ARGON2_MEMORY_KIB=65536", "OPENFOX_AUTH_ARGON2_ITERATIONS=3", "OPENFOX_AUTH_ARGON2_PARALLELISM=1",
	)
}

// startServer 启动中心子进程并等待 /ready。
func (s *stack) startServer() *proc {
	s.t.Helper()
	s.server = startProc(s.t, "openfox-server", filepath.Join(s.dir, "server.log"), s.serverEnv(), s.serverBin, "serve")
	s.waitFor("center ready", 30*time.Second, func() bool {
		if s.server.exited() {
			s.t.Fatalf("center exited early: %v\n%s", s.server.err, tail(s.server.log))
		}
		return s.status("/ready") == http.StatusOK
	})
	return s.server
}

// status 返回 GET path 的状态码；连接失败返回 0。
func (s *stack) status(path string) int {
	request, err := http.NewRequestWithContext(s.ctx, http.MethodGet, s.center+path, nil)
	if err != nil {
		return 0
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

func (s *stack) waitFor(what string, limit time.Duration, condition func() bool) time.Duration {
	s.t.Helper()
	begin := time.Now()
	for !condition() {
		if time.Since(begin) > limit {
			s.t.Fatalf("timed out waiting for %s (%s)\n%s", what, limit, s.diagnostics())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return time.Since(begin)
}

// diagnostics 汇总超时时的现场：组织内最近的 Run、中心日志与各 daemon 日志的末尾。
func (s *stack) diagnostics() string {
	var out strings.Builder
	rows, err := s.superuser.Query(context.Background(), `SELECT id::text, state, COALESCE(failure_code, ''), COALESCE(wait_reason, ''), reconciliation_required::text
		FROM openfox.runs WHERE organization_id = $1::uuid ORDER BY created_at DESC LIMIT 6`, s.org)
	if err == nil {
		out.WriteString("--- runs ---\n")
		for rows.Next() {
			var id, state, code, wait, reconcile string
			if rows.Scan(&id, &state, &code, &wait, &reconcile) == nil {
				fmt.Fprintf(&out, "%s %s code=%s wait=%s reconcile=%s\n", id, state, code, wait, reconcile)
			}
		}
		rows.Close()
	}
	out.WriteString("--- server ---\n" + tail(filepath.Join(s.dir, "server.log")))
	logs, _ := filepath.Glob(filepath.Join(s.dir, "*", "daemon.log"))
	for _, path := range logs {
		out.WriteString("\n--- " + path + " ---\n" + tail(path))
	}
	return out.String()
}

func tail(path string) string {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	if len(contents) > 6000 {
		contents = contents[len(contents)-6000:]
	}
	return string(contents)
}

func (s *stack) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.superuser.Exec(s.ctx, query, args...); err != nil {
		s.t.Fatalf("exec %s: %v", query, err)
	}
}

// text 返回单值查询结果的文本；没有行时返回空串。
func (s *stack) text(query string, args ...any) string {
	s.t.Helper()
	var value *string
	err := s.superuser.QueryRow(s.ctx, query, args...).Scan(&value)
	if err == pgx.ErrNoRows || (err == nil && value == nil) {
		return ""
	}
	if err != nil {
		s.t.Fatalf("query %s: %v", query, err)
	}
	return *value
}

func (s *stack) expect(want, query string, args ...any) {
	s.t.Helper()
	if got := s.text(query, args...); got != want {
		s.t.Fatalf("%s = %q, want %q", query, got, want)
	}
}

// seed 写入用户与两个组织：s.org（owner=OWNER、member=MEMBER）与 otherOrg（outsider=OWNER）。
func (s *stack) seed() (otherOrg string) {
	s.t.Helper()
	hash := passwordHash(s.t)
	for _, id := range []string{drillOwnerID, drillMemberID, drillOutsiderID} {
		s.exec(`INSERT INTO openfox.users (id, email) VALUES ($1::uuid, $2) ON CONFLICT DO NOTHING`, id, emailOf(id))
		s.exec(`INSERT INTO openfox.password_credentials (user_id, password_hash) VALUES ($1::uuid, $2)
			ON CONFLICT (user_id) DO UPDATE SET password_hash = EXCLUDED.password_hash, credential_version = 1, status = 'ACTIVE'`, id, hash)
	}
	otherOrg = newID(s.t)
	for _, org := range []string{s.org, otherOrg} {
		s.exec(`INSERT INTO openfox.organizations (id, slug, name) VALUES ($1::uuid, $2, $3)`, org, "s12i-"+org[:8], "Stage12i "+org[:8])
		s.exec(`INSERT INTO openfox.organization_directory (organization_id, slug, name, created_at)
			SELECT id, slug, name, created_at FROM openfox.organizations WHERE id = $1::uuid`, org)
		s.exec(`INSERT INTO openfox.task_statuses (organization_id, name, description, category, position) VALUES
			($1::uuid, 'Todo', '', 'TODO', 0), ($1::uuid, 'In Progress', '', 'IN_PROGRESS', 1), ($1::uuid, 'Done', '', 'DONE', 2)`, org)
		s.exec(`INSERT INTO openfox.organization_capacity (organization_id, max_slots) VALUES ($1::uuid, 8) ON CONFLICT DO NOTHING`, org)
	}
	s.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`, s.org, drillOwnerID)
	s.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'MEMBER')`, s.org, drillMemberID)
	s.exec(`INSERT INTO openfox.organization_members (organization_id, user_id, role) VALUES ($1::uuid, $2::uuid, 'OWNER')`, otherOrg, drillOutsiderID)
	return otherOrg
}

// ---- HTTP ----

func (s *stack) login(userID string) *client {
	s.t.Helper()
	if existing, ok := s.clients[userID]; ok {
		return existing
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatal(err)
	}
	c := &client{s: s, http: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	payload := fmt.Sprintf(`{"email":%q,"password":%q,"client_kind":"web"}`, emailOf(userID), drillPassword)
	response, err := c.http.Post(s.center+"/api/v1/auth/login", "application/json", strings.NewReader(payload))
	if err != nil {
		s.t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		s.t.Fatalf("login %s = %d %s", userID, response.StatusCode, body)
	}
	parsed, _ := neturl.Parse(s.center)
	for _, cookie := range jar.Cookies(parsed) {
		if cookie.Name == "openfox_csrf" {
			c.csrf = cookie.Value
		}
	}
	s.clients[userID] = c
	return c
}

func (s *stack) owner() *client { return s.login(drillOwnerID) }

// do 发送请求并返回状态码与响应体；path 不以 "/" 开头时拼到 s.org 前缀之后。传输错误返回状态 0。
func (c *client) do(method, path string, body any) (int, []byte) {
	c.s.t.Helper()
	return c.doWith(method, path, body)
}

// doWith 与 do 相同，另外附加请求头（键值对）。
func (c *client) doWith(method, path string, body any, headers ...string) (int, []byte) {
	c.s.t.Helper()
	if !strings.HasPrefix(path, "/") {
		path = "/api/v1/organizations/" + c.s.org + "/" + path
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.s.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(c.s.ctx, method, c.s.center+path, reader)
	if err != nil {
		c.s.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		request.Header.Set("X-CSRF-Token", c.csrf)
	}
	for index := 0; index+1 < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(response.Body)
	return response.StatusCode, payload
}

func (c *client) must(want int, method, path string, body, out any) {
	c.s.t.Helper()
	status, payload := c.do(method, path, body)
	if status != want {
		c.s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, payload, want)
	}
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			c.s.t.Fatalf("decode %s: %v", payload, err)
		}
	}
}

// ---- 电脑、Agent、对话 ----

type device struct {
	ID         string
	ConfigPath string
	StateDir   string
	epoch      int64
}

// registerDevice 经 HTTP 注册一台电脑并写本机配置（等价于 `openfox computer register --no-service`，
// 但不经过 CLI，因此绝不会安装 launchd 服务）。配置、密钥、令牌、socket、journal、工作区都在 <dir>/<name>/ 下。
func (s *stack) registerDevice(name string) device {
	s.t.Helper()
	var token struct {
		InstallToken string `json:"install_token"`
	}
	s.owner().must(http.StatusCreated, http.MethodPost, "devices/registration-tokens", map[string]any{"name": "s12i-" + name, "scope": "ORGANIZATION"}, &token)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	keyID := daemon.DeviceKeyID(public)
	var registered struct {
		DeviceID    string `json:"device_id"`
		CenterEpoch int64  `json:"center_epoch"`
	}
	anonymous := &client{s: s, http: &http.Client{Timeout: 20 * time.Second}}
	anonymous.must(http.StatusCreated, http.MethodPost, "/api/v1/device/register", map[string]string{
		"signature": deviceauth.EncodeBase64URL(ed25519.Sign(private, deviceauth.RegistrationPayload(token.InstallToken, keyID))),
		"token":     token.InstallToken, "key_id": keyID, "public_key": deviceauth.EncodeBase64URL(public),
	}, &registered)
	session, err := (daemon.AuthClient{BaseURL: daemon.APIBaseURL(s.center), HTTP: &http.Client{Timeout: 20 * time.Second}}).Session(s.ctx, registered.DeviceID, keyID, private)
	if err != nil {
		s.t.Fatalf("device session: %v", err)
	}
	stateDir := filepath.Join(s.dir, name)
	wss, err := daemon.CenterWSSURL(s.center)
	if err != nil {
		s.t.Fatal(err)
	}
	config := daemon.DeviceConfig{
		Version: daemon.DeviceConfigVersion, CenterURL: s.center, WSSURL: wss, DeviceID: registered.DeviceID, OrganizationID: s.org, KeyID: keyID,
		PrivateKeyFile: filepath.Join(stateDir, daemon.DeviceKeyFileName), TokenFile: filepath.Join(stateDir, daemon.DeviceTokenFileName),
		WorkspaceRoot: filepath.Join(stateDir, "workspaces"), CenterEpoch: registered.CenterEpoch,
		SupportedVersions: []string{"0.0.1-e2e"}, RuntimeExecutable: s.fakeCodex(),
	}
	if err := daemon.WriteDeviceKey(config.PrivateKeyFile, private); err != nil {
		s.t.Fatal(err)
	}
	if err := daemon.WritePrivateFile(config.TokenFile, []byte(session.Token+"\n")); err != nil {
		s.t.Fatal(err)
	}
	path := filepath.Join(stateDir, daemon.DeviceConfigFileName)
	if err := daemon.WriteDeviceConfig(path, config); err != nil {
		s.t.Fatal(err)
	}
	return device{ID: registered.DeviceID, ConfigPath: path, StateDir: stateDir, epoch: registered.CenterEpoch}
}

// startDaemon 以前台方式启动 daemon 子进程：`openfox daemon run --config <临时配置>`。
// socket、journal（runs.db）、独立 CODEX_HOME、附件与产物目录都默认落在配置文件所在目录。
func (s *stack) startDaemon(d device) *proc {
	s.t.Helper()
	if !strings.HasPrefix(d.ConfigPath, s.dir+string(filepath.Separator)) {
		s.t.Fatalf("daemon config %s is outside the drill directory", d.ConfigPath)
	}
	return startProc(s.t, "openfox daemon", filepath.Join(d.StateDir, "daemon.log"), s.childEnv(), s.cliBin,
		"daemon", "run", "--config", d.ConfigPath, "--heartbeat-interval", "500ms", "--offer-poll-interval", "1s")
}

func (s *stack) waitOnline(d device) time.Duration {
	s.t.Helper()
	return s.waitFor("device online", 60*time.Second, func() bool {
		var view struct {
			Online            bool       `json:"online"`
			RuntimesCheckedAt *time.Time `json:"runtimes_checked_at"`
		}
		status, payload := s.owner().do(http.MethodGet, "devices/"+d.ID, nil)
		return status == http.StatusOK && json.Unmarshal(payload, &view) == nil && view.Online && view.RuntimesCheckedAt != nil
	})
}

// createAgent 经接口建空间与绑定该电脑的共享 Agent。
func (s *stack) createAgent(d device) (space, agent string) {
	s.t.Helper()
	grant := s.text(`SELECT id::text FROM openfox.device_grants WHERE organization_id = $1::uuid AND device_id = $2::uuid`, s.org, d.ID)
	var created struct {
		ID string `json:"id"`
	}
	s.owner().must(http.StatusCreated, http.MethodPost, "spaces", map[string]any{"name": "Drill " + newID(s.t)[:6], "visibility": "SHARED"}, &created)
	space = created.ID
	s.owner().must(http.StatusCreated, http.MethodPost, "spaces/"+space+"/agents", map[string]any{
		"name": "Drill agent", "kind": "CODE", "visibility": "SHARED",
		"config": map[string]any{"computer_id": d.ID, "grant_id": grant, "runtime": "codex", "model": "default"},
	}, &created)
	return space, created.ID
}

func (s *stack) createConversation(space, agent string) string {
	s.t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	s.owner().must(http.StatusCreated, http.MethodPost, "conversations", map[string]any{"space_id": space, "agent_id": agent}, &created)
	return created.ID
}

// post 在对话里 @Agent 发一条消息，返回接口状态码（不断言，供「对话是否可输入」类检查使用）。
func (s *stack) post(conversation, agent, body string) int {
	s.t.Helper()
	id := newID(s.t)
	status, _ := s.owner().do(http.MethodPost, "conversations/"+conversation+"/messages", map[string]any{
		"id": id, "client_message_id": "client-" + id, "body": body, "mentions": []map[string]string{{"agent_id": agent}},
	})
	return status
}

func (s *stack) mustPost(conversation, agent, body string) {
	s.t.Helper()
	if status := s.post(conversation, agent, body); status != http.StatusCreated {
		s.t.Fatalf("post message = %d", status)
	}
}

type runRow struct {
	ID, State, FailureCode string
	Generation             int64
	Reconcile              bool
}

func (s *stack) latestRun(conversation string) runRow {
	s.t.Helper()
	var row runRow
	err := s.superuser.QueryRow(s.ctx, `SELECT id::text, state, COALESCE(failure_code, ''), generation, reconciliation_required
		FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid ORDER BY created_at DESC LIMIT 1`,
		s.org, conversation).Scan(&row.ID, &row.State, &row.FailureCode, &row.Generation, &row.Reconcile)
	if err != nil && err != pgx.ErrNoRows {
		s.t.Fatal(err)
	}
	return row
}

func (s *stack) run(id string) runRow {
	s.t.Helper()
	var row runRow
	if err := s.superuser.QueryRow(s.ctx, `SELECT id::text, state, COALESCE(failure_code, ''), generation, reconciliation_required
		FROM openfox.runs WHERE id = $1::uuid`, id).Scan(&row.ID, &row.State, &row.FailureCode, &row.Generation, &row.Reconcile); err != nil {
		s.t.Fatal(err)
	}
	return row
}

func (s *stack) runCount(conversation string) int {
	s.t.Helper()
	count, _ := strconv.Atoi(s.text(`SELECT count(*)::text FROM openfox.runs WHERE organization_id = $1::uuid AND conversation_id = $2::uuid`, s.org, conversation))
	return count
}

// waitRunState 等待对话最新一次 Run 进入指定状态之一，返回该 Run 与耗时。
func (s *stack) waitRunState(conversation string, limit time.Duration, states ...string) (runRow, time.Duration) {
	s.t.Helper()
	var row runRow
	elapsed := s.waitFor("run "+strings.Join(states, "/"), limit, func() bool {
		row = s.latestRun(conversation)
		for _, state := range states {
			if row.State == state {
				return true
			}
		}
		return false
	})
	return row, elapsed
}

// waitNewRunDone 等待对话出现一条不同于 previous 的新 Run 并进入终态。
func (s *stack) waitNewRunDone(conversation, previous string) runRow {
	s.t.Helper()
	var row runRow
	s.waitFor("a new run to finish", 60*time.Second, func() bool {
		row = s.latestRun(conversation)
		return row.ID != "" && row.ID != previous && (row.State == "SUCCEEDED" || row.State == "FAILED" || row.State == "CANCELED")
	})
	return row
}

// expectReleasedOnce 断言该 Run 的容量预留恰好释放一次、电脑与组织计数回到 0。
func (s *stack) expectReleasedOnce(runID, deviceID string) {
	s.t.Helper()
	s.expect("1", `SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status = 'RELEASED'`, runID)
	s.expect("0", `SELECT count(*)::text FROM openfox.capacity_reservations WHERE run_id = $1::uuid AND status <> 'RELEASED'`, runID)
	s.expect("0", `SELECT reserved_slots::text FROM openfox.physical_devices WHERE id = $1::uuid`, deviceID)
	s.expect("0", `SELECT reserved_slots::text FROM openfox.organization_capacity WHERE organization_id = $1::uuid`, s.org)
}

// ---- 假 Runtime ----

// fakeCodex 写一个遵循 codex app-server 协议的假 CLI。行为由 state/mode 文件决定（turn 开始时读取并删除，
// 因此只影响下一次 Run；不用提示词标记，是因为同一对话的后续提示词会带上历史消息）：
//
//	hang：turn 开始后不再读 stdin、永不结束（daemon 被强杀后仍然存活，模拟卡住的 Runtime）；
//	gate：先输出一段，等 state/release1 出现后再输出一段，等 state/release2 出现后正常完成；
//	slow：不结束 turn，直到收到 turn/interrupt 才以 interrupted 收尾；stdin 关闭则退出；
//	无文件：立即回复 OK。
//
// 每次真实 Run（有 thread）把自己的进程号追加到 state/invocations。
func (s *stack) fakeCodex() string {
	s.t.Helper()
	path := filepath.Join(s.dir, "codex-drill.sh")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	script := strings.ReplaceAll(fakeCodexScript, "@STATE@", filepath.Join(s.dir, "state"))
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		s.t.Fatal(err)
	}
	return path
}

const fakeCodexScript = `#!/bin/sh
state='@STATE@'
while [ "$1" = "-c" ]; do shift 2; done
case "$1" in
  --version) echo 'codex-cli 0.0.1-e2e'; exit 0 ;;
  login) echo 'Logged in using e2e fixture'; exit 0 ;;
  app-server) if [ "$2" = "--help" ]; then echo 'codex app-server'; exit 0; fi ;;
  *) exit 2 ;;
esac
IFS= read -r line || exit 0
echo '{"id":1,"result":{"userAgent":"e2e"}}'
IFS= read -r line || exit 0
IFS= read -r line || exit 0
case "$line" in
  *model/list*)
    echo '{"id":2,"result":{"data":[{"id":"gpt-e2e","model":"gpt-e2e","displayName":"GPT E2E","hidden":false,"isDefault":true}],"nextCursor":null}}'
    while IFS= read -r line; do :; done; exit 0 ;;
esac
echo "$$" >> "$state/invocations"
echo '{"id":2,"result":{"thread":{"id":"th-e2e"}}}'
IFS= read -r turn || exit 0
echo '{"id":3,"result":{"turn":{"id":"tu-e2e","status":"inProgress"}}}'
say() { printf '{"method":"item/completed","params":{"threadId":"th-e2e","item":{"type":"agentMessage","text":"%s","phase":"%s"}}}\n' "$1" "$2"; }
done_turn() { printf '{"method":"turn/completed","params":{"threadId":"th-e2e","turn":{"id":"tu-e2e","status":"%s"}}}\n' "$1"; }
mode=$(cat "$state/mode" 2>/dev/null)
rm -f "$state/mode"
case "$mode" in
  hang)
    echo "$$" > "$state/hang-pid"
    while :; do sleep 1; done ;;
  gate)
    say 'gate step one' commentary
    touch "$state/gate-started"
    while [ ! -e "$state/release1" ]; do sleep 0.2; done
    say 'gate step two' commentary
    while [ ! -e "$state/release2" ]; do sleep 0.2; done
    say OK final_answer
    done_turn completed ;;
  slow)
    echo "$$" > "$state/slow-pid"
    IFS= read -r line || exit 0
    case "$line" in
      *turn/interrupt*)
        echo '{"id":4,"result":{}}'
        done_turn interrupted ;;
    esac ;;
  *)
    say OK final_answer
    done_turn completed ;;
esac
while IFS= read -r line; do :; done
exit 0
`

func (s *stack) statePath(name string) string { return filepath.Join(s.dir, "state", name) }

// nextMode 设定下一次 Run 的假 Runtime 行为（hang / gate / slow）。
func (s *stack) nextMode(mode string) {
	s.t.Helper()
	if err := os.WriteFile(s.statePath("mode"), []byte(mode+"\n"), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stack) touch(name string) {
	s.t.Helper()
	if err := os.WriteFile(s.statePath(name), nil, 0o600); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stack) invocations() int {
	contents, err := os.ReadFile(s.statePath("invocations"))
	if err != nil {
		return 0
	}
	return strings.Count(string(contents), "\n")
}

// statePID 读取假 Runtime 写下的进程号文件；不存在时返回 0。
func (s *stack) statePID(name string) int {
	contents, err := os.ReadFile(s.statePath(name))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(contents)))
	return pid
}

// commandLine 返回进程的命令行；进程不存在时返回空串。
func commandLine(pid int) string {
	if pid <= 0 {
		return ""
	}
	output, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// ownedRuntimeAlive 报告 pid 是否仍是本次演练的假 Runtime（命令行含本次演练目录下的脚本路径）。
func (s *stack) ownedRuntimeAlive(pid int) bool {
	return strings.Contains(commandLine(pid), filepath.Join(s.dir, "codex-drill.sh"))
}

// groupAlive 报告进程组 pgid 是否还有成员。
func groupAlive(pgid int) bool {
	output, _ := exec.Command("ps", "-axo", "pgid=").Output()
	for _, field := range strings.Fields(string(output)) {
		if field == strconv.Itoa(pgid) {
			return true
		}
	}
	return false
}

// killOwnedRuntime 结束本次演练的假 Runtime：只在核对命令行属于本次演练目录、且它是自己进程组的组长
// （daemon 用 Setpgid 为每个 Runtime 建独立进程组）之后，才对该进程组发 SIGKILL，并等到组内没有成员。
// 只杀组长会留下它的 sleep 子进程，daemon 重启核对时会得到「组长已退出但进程组仍有成员」的不确定结论。
func (s *stack) killOwnedRuntime(pid int) {
	s.t.Helper()
	if !s.ownedRuntimeAlive(pid) {
		return
	}
	output, _ := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
	if strings.TrimSpace(string(output)) != strconv.Itoa(pid) {
		s.t.Fatalf("runtime %d is not a process group leader (pgid %q); refusing to signal", pid, strings.TrimSpace(string(output)))
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	s.waitFor("runtime process group gone", 10*time.Second, func() bool { return !s.ownedRuntimeAlive(pid) && !groupAlive(pid) })
}

// reapRuntimes 结束仍存活的本次演练假 Runtime（只处理命令行属于本次演练目录、且为进程组组长的进程）。
func (s *stack) reapRuntimes() {
	for _, line := range s.orphanRuntimes() {
		fields := strings.Fields(line)
		pid, err := strconv.Atoi(fields[0])
		if err != nil || !s.ownedRuntimeAlive(pid) {
			continue
		}
		output, _ := exec.Command("ps", "-o", "pgid=", "-p", strconv.Itoa(pid)).Output()
		if strings.TrimSpace(string(output)) == strconv.Itoa(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	}
}

// orphanRuntimes 列出命令行含本次演练目录下假 Runtime 脚本的存活进程。
func (s *stack) orphanRuntimes() []string {
	output, _ := exec.Command("ps", "-axo", "pid=,command=").Output()
	var found []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, filepath.Join(s.dir, "codex-drill.sh")) {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}

// ---- daemon 本地 journal（SQLite，只在 daemon 停止时读写）----

type journalRow struct {
	State        string
	PID          int
	StartToken   int64
	PGID         int
	ReportKind   string
	ReportStatus string
	Certain      bool
}

func (s *stack) journal(d device, runID string, fn func(*sql.DB) error) {
	s.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(d.StateDir, "runs.db"))
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(s.ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		s.t.Fatal(err)
	}
	if err := fn(db); err != nil {
		s.t.Fatalf("journal: %v", err)
	}
}

func (s *stack) journalRow(d device, runID string) journalRow {
	s.t.Helper()
	var row journalRow
	s.journal(d, runID, func(db *sql.DB) error {
		var certain sql.NullInt64
		err := db.QueryRowContext(s.ctx, `SELECT state, COALESCE(pid, 0), COALESCE(start_token, 0), COALESCE(pgid, 0),
			COALESCE(report_kind, ''), COALESCE(report_status, ''), report_certain FROM run_journal WHERE run_id = ?`, runID).
			Scan(&row.State, &row.PID, &row.StartToken, &row.PGID, &row.ReportKind, &row.ReportStatus, &certain)
		row.Certain = certain.Valid && certain.Int64 != 0
		return err
	})
	return row
}

// newMigratedCluster 新建临时集群与数据库，并用 `openfox-server migrate --apply` 应用全部迁移。
// 返回集群与数据库的超级用户连接串。目录在测试结束时删除。
func newMigratedCluster(t *testing.T, database string) (*cluster, string) {
	t.Helper()
	if os.Getenv("OPENFOX_TEST_DATABASE_URL") == "" {
		t.Skip("OPENFOX_TEST_DATABASE_URL is not configured")
	}
	dir, err := os.MkdirTemp("/tmp", "of12i-pg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	c := newCluster(t, dir)
	c.run("createdb", database)
	c.migrate(database)
	return c, c.dsn(database)
}

// migrate 对指定数据库应用全部迁移（幂等：已应用的迁移由台账跳过）。
func (c *cluster) migrate(database string) {
	c.t.Helper()
	server, _ := binaries(c.t)
	urlFile := filepath.Join(filepath.Dir(c.data), database+".url")
	if err := os.WriteFile(urlFile, []byte(c.dsn(database)+"\n"), 0o600); err != nil {
		c.t.Fatal(err)
	}
	migrations, err := filepath.Abs(filepath.Join("..", "..", "..", "db", "migrations"))
	if err != nil {
		c.t.Fatal(err)
	}
	c.run(server, "migrate", "--apply", "--database-url-file", urlFile, "--dir", migrations)
}
