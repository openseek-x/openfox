// Package fakegithub 是本机模拟的 GitHub（OAuth 授权、令牌换取、/user、/user/orgs、PR 评论），
// 供集成测试与浏览器联调使用，绝不访问真实 github.com。
package fakegithub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options 是模拟服务的配置。
type Options struct {
	ClientID     string
	ClientSecret string
	// Login / UserID / AvatarURL 是授权者本人。
	Login     string
	UserID    int64
	AvatarURL string
	// Orgs 是授权者所属组织的 login 列表。
	Orgs []string
	// OrgsPageSize 大于 0 时 /user/orgs 按此大小分页并返回 Link 头（测试翻页）。
	OrgsPageSize int
}

// Comment 是收到的一条 PR 评论。
type Comment struct {
	Owner  string
	Repo   string
	Number int
	Body   string
	ID     int64
	// Token 是发评论用的令牌，用来区分个人令牌与组织令牌。
	Token string
}

type pendingCode struct {
	challenge string
	token     string
}

// Server 是模拟服务；通过 Handler 挂到任意监听器，或用 New 直接起一个 httptest 服务。
type Server struct {
	URL string

	mu             sync.Mutex
	opts           Options
	httpServer     *httptest.Server
	codes          map[string]pendingCode
	tokens         map[string]bool // 令牌 → 是否有效
	deny           bool
	commentStatus  int
	comments       []Comment
	challenges     []string
	counter        int
	exchangeFailed bool

	// 仓库与 Issue。
	tokenLogin map[string]string // 令牌 → 换取令牌时的授权者 login
	repos      []Repo
	issues     map[string][]*IssueRecord // 小写 owner/name → Issue（含混入的 PR 条目）
	patches    []Patch
	rateLimit  time.Time // 非零表示限流中（403 + X-RateLimit-Reset）
	dropCreate bool      // 下一次 POST /issues 建好后断开连接（客户端拿不到响应）
	writeCode  int       // 非零时 POST / PATCH issues 返回该状态码
	issueCalls int
}

// New 起一个 httptest 服务。调用方负责 Close。
func New(opts Options) *Server {
	s := NewHandler(opts)
	s.httpServer = httptest.NewServer(s.Handler())
	s.URL = s.httpServer.URL
	return s
}

// NewHandler 只构造状态，不监听；用于 main 程序。
func NewHandler(opts Options) *Server {
	if opts.ClientID == "" {
		opts.ClientID = "fake-client-id"
	}
	if opts.ClientSecret == "" {
		opts.ClientSecret = "fake-client-secret"
	}
	if opts.Login == "" {
		opts.Login = "lin"
	}
	if opts.UserID == 0 {
		opts.UserID = 4242
	}
	return &Server{opts: opts, codes: map[string]pendingCode{}, tokens: map[string]bool{}, commentStatus: http.StatusCreated,
		tokenLogin: map[string]string{}, issues: map[string][]*IssueRecord{}}
}

// Close 关闭 httptest 服务。
func (s *Server) Close() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

// ---- 测试控制 ----

// DenyAuthorize 让授权页返回 error=access_denied。
func (s *Server) DenyAuthorize(deny bool) { s.mu.Lock(); s.deny = deny; s.mu.Unlock() }

// FailExchange 让令牌换取返回 error。
func (s *Server) FailExchange(fail bool) { s.mu.Lock(); s.exchangeFailed = fail; s.mu.Unlock() }

// SetCommentStatus 设置评论接口的状态码（例如 401）；默认 201。
func (s *Server) SetCommentStatus(status int) { s.mu.Lock(); s.commentStatus = status; s.mu.Unlock() }

// SetIdentity 修改授权者与所属组织（下一次授权生效）。
func (s *Server) SetIdentity(login string, userID int64, orgs ...string) {
	s.mu.Lock()
	s.opts.Login, s.opts.UserID, s.opts.Orgs = login, userID, orgs
	s.mu.Unlock()
}

// RevokeTokens 让所有已发出的令牌失效（之后的 API 调用返回 401）。
func (s *Server) RevokeTokens() {
	s.mu.Lock()
	for token := range s.tokens {
		s.tokens[token] = false
	}
	s.mu.Unlock()
}

// Comments 返回收到的评论副本。
func (s *Server) Comments() []Comment {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Comment(nil), s.comments...)
}

// Challenges 返回授权页收到的 code_challenge。
func (s *Server) Challenges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.challenges...)
}

// SignedWebhook 构造带 X-Hub-Signature-256 的 GitHub Webhook 请求。
func SignedWebhook(targetURL, secret, event, deliveryID string, payload []byte) *http.Request {
	req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(payload)))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", deliveryID)
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return req
}

// ---- HTTP ----

// Handler 返回模拟服务的路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.accessToken)
	mux.HandleFunc("GET /user", s.user)
	mux.HandleFunc("GET /user/orgs", s.orgs)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", s.comment)
	s.routeIssues(mux)
	return mux
}

func (s *Server) nextID() int {
	s.counter++
	return s.counter
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if query.Get("client_id") != s.opts.ClientID || err != nil || redirect.Scheme == "" || redirect.Host == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges = append(s.challenges, query.Get("code_challenge"))
	out := redirect.Query()
	out.Set("state", query.Get("state"))
	if s.deny {
		out.Set("error", "access_denied")
	} else {
		code := fmt.Sprintf("code-%d", s.nextID())
		s.codes[code] = pendingCode{challenge: query.Get("code_challenge")}
		out.Set("code", code)
	}
	redirect.RawQuery = out.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code string) {
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	if s.exchangeFailed {
		fail("bad_verification_code")
		return
	}
	if r.PostForm.Get("client_id") != s.opts.ClientID || r.PostForm.Get("client_secret") != s.opts.ClientSecret {
		fail("incorrect_client_credentials")
		return
	}
	pending, ok := s.codes[r.PostForm.Get("code")]
	if !ok {
		fail("bad_verification_code")
		return
	}
	delete(s.codes, r.PostForm.Get("code"))
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != pending.challenge {
		fail("bad_verification_code")
		return
	}
	token := fmt.Sprintf("gho_fake_%d", s.nextID())
	s.tokens[token] = true
	s.tokenLogin[token] = s.opts.Login
	_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "scope": "repo,read:org", "token_type": "bearer"})
}

// valid 校验 Authorization 头里的令牌；调用方已持有锁。
func (s *Server) valid(r *http.Request) bool {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return s.tokens[token]
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.valid(r) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	avatar := s.opts.AvatarURL
	if avatar == "" {
		avatar = "https://avatars.example.test/u/" + strconv.FormatInt(s.opts.UserID, 10)
	}
	w.Header().Set("X-OAuth-Scopes", "read:org, repo")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": s.opts.UserID, "login": s.opts.Login, "avatar_url": avatar})
}

func (s *Server) orgs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.valid(r) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size := s.opts.OrgsPageSize
	if size <= 0 {
		size = 100
	}
	start, end := (page-1)*size, page*size
	if start > len(s.opts.Orgs) {
		start = len(s.opts.Orgs)
	}
	if end > len(s.opts.Orgs) {
		end = len(s.opts.Orgs)
	} else if end < len(s.opts.Orgs) {
		next := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page + 1)}}
		w.Header().Set("Link", fmt.Sprintf(`<http://%s/user/orgs?%s>; rel="next"`, r.Host, next.Encode()))
	}
	items := make([]map[string]any, 0)
	for i, login := range s.opts.Orgs[start:end] {
		items = append(items, map[string]any{"id": 1000 + start + i, "login": login,
			"avatar_url": "https://avatars.example.test/o/" + login})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (s *Server) comment(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commentStatus == http.StatusUnauthorized || !s.gate(w, r) {
		if s.commentStatus == http.StatusUnauthorized {
			http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		}
		return
	}
	if s.commentStatus != http.StatusCreated {
		http.Error(w, `{"message":"error"}`, s.commentStatus)
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	number, _ := strconv.Atoi(r.PathValue("number"))
	id := int64(9000 + s.nextID())
	s.comments = append(s.comments, Comment{Owner: r.PathValue("owner"), Repo: r.PathValue("repo"), Number: number, Body: body.Body, ID: id,
		Token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
}
