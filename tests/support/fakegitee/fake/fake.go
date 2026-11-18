// Package fakegitee 是本机模拟的 Gitee（OAuth 授权码、令牌刷新、OpenAPI v5 的用户、组织、仓库、Issue、评论），
// 供集成测试与浏览器联调使用，绝不访问真实的 Gitee。协议依据 https://gitee.com/api/v5/oauth_doc 与 OpenAPI 描述：
//   - 没有 PKCE：授权页只记录是否收到了 code_challenge（Challenges），客户端不应发送；
//   - 令牌换取必须带 User-Agent，否则 403（与官方文档一致）；
//   - API 的令牌通过 access_token 传递：GET 走查询串，POST / PATCH 走表单（PR 评论接口例外，查询串里也有）；
//     POST / PATCH 的查询串里出现 access_token（该例外之外）会被拒绝并计数（QueryTokenWrites），用来保证客户端不把令牌放进写请求的 URL；
//   - Issue 编号是字母数字串（如 IT0001），id 是数字。
package fakegitee

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options 是模拟服务的配置。
type Options struct {
	ClientID     string
	ClientSecret string
	// Login / UserID 是授权者本人；Orgs 是授权者所属的组织 path。
	Login  string
	UserID int64
	Orgs   []string
	// TokenTTL 是访问令牌有效期（秒），默认 86400（一天，与 Gitee 一致）。
	TokenTTL int
}

// Repo 是模拟的仓库。
type Repo struct {
	Path    string // 空间 path / 仓库 path
	Private bool
}

// User 是 Gitee 用户。
type User struct {
	ID    int64
	Login string
}

// IssueRecord 是模拟的 Issue。
type IssueRecord struct {
	ID          int64
	Number      string // 字母数字串，例如 IT0001
	Repo        string // owner/repo
	Title       string
	Body        string
	State       string // open / progressing / closed / rejected
	AuthorID    int64
	AuthorLogin string
	Assignee    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Note 是收到的一条评论。
type Note struct {
	Repo   string
	Kind   string // issues / pulls
	Number string
	Body   string
	ID     int64
	Token  string
}

type tokenRecord struct {
	valid   bool
	login   string
	userID  int64
	expires time.Time
	refresh string
}

type pendingCode struct {
	login  string
	userID int64
}

// Server 是模拟服务。
type Server struct {
	URL string

	mu         sync.Mutex
	opts       Options
	httpServer *httptest.Server
	codes      map[string]pendingCode
	tokens     map[string]*tokenRecord
	refresh    map[string]string
	repos      []Repo
	users      []User
	issues     map[string][]*IssueRecord
	notes      []Note
	challenges []string
	counter    int

	deny           bool
	exchangeFailed bool
	refreshStatus  int
	rateLimit      time.Time
	dropCreate     bool
	writeCode      int
	noteStatus     int
	refreshCalls   int
	queryWrites    int
	missingUA      int
}

// New 起一个 httptest 服务。调用方负责 Close。
func New(opts Options) *Server {
	s := NewHandler(opts)
	s.httpServer = httptest.NewServer(s.Handler())
	s.URL = s.httpServer.URL
	return s
}

// NewHandler 只构造状态，不监听。
func NewHandler(opts Options) *Server {
	if opts.ClientID == "" {
		opts.ClientID = "fake-gitee-id"
	}
	if opts.ClientSecret == "" {
		opts.ClientSecret = "fake-gitee-secret"
	}
	if opts.Login == "" {
		opts.Login = "lin"
	}
	if opts.UserID == 0 {
		opts.UserID = 5151
	}
	if opts.TokenTTL == 0 {
		opts.TokenTTL = 86400
	}
	s := &Server{opts: opts, codes: map[string]pendingCode{}, tokens: map[string]*tokenRecord{}, refresh: map[string]string{},
		issues: map[string][]*IssueRecord{}}
	s.users = append(s.users, User{ID: opts.UserID, Login: opts.Login})
	return s
}

// Close 关闭 httptest 服务。
func (s *Server) Close() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

func (s *Server) locked(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); fn() }

// ClientID / ClientSecret 返回应用凭据。
func (s *Server) ClientID() string     { return s.opts.ClientID }
func (s *Server) ClientSecret() string { return s.opts.ClientSecret }

func (s *Server) DenyAuthorize(deny bool)     { s.locked(func() { s.deny = deny }) }
func (s *Server) FailExchange(fail bool)      { s.locked(func() { s.exchangeFailed = fail }) }
func (s *Server) SetRefreshStatus(status int) { s.locked(func() { s.refreshStatus = status }) }
func (s *Server) SetNoteStatus(status int)    { s.locked(func() { s.noteStatus = status }) }
func (s *Server) SetWriteStatus(status int)   { s.locked(func() { s.writeCode = status }) }

// DropNextCreate 让下一次创建 Issue 建好后断开连接。
func (s *Server) DropNextCreate() { s.locked(func() { s.dropCreate = true }) }

// SetRateLimit 让 API 在 d 之内返回 429 + Retry-After；0 解除。
func (s *Server) SetRateLimit(d time.Duration) {
	s.locked(func() {
		if d <= 0 {
			s.rateLimit = time.Time{}
			return
		}
		s.rateLimit = time.Now().Add(d)
	})
}

// SetIdentity 修改授权者与所属组织（下一次授权生效）。
func (s *Server) SetIdentity(login string, userID int64, orgs ...string) {
	s.locked(func() {
		s.opts.Login, s.opts.UserID, s.opts.Orgs = login, userID, orgs
		s.addUser(User{ID: userID, Login: login})
	})
}

func (s *Server) addUser(user User) {
	for i := range s.users {
		if s.users[i].ID == user.ID {
			s.users[i] = user
			return
		}
	}
	s.users = append(s.users, user)
}

func (s *Server) AddUser(id int64, login string) {
	s.locked(func() { s.addUser(User{ID: id, Login: login}) })
}
func (s *Server) AddRepo(path string, private bool) {
	s.locked(func() { s.repos = append(s.repos, Repo{Path: path, Private: private}) })
}

// RevokeTokens 让访问令牌失效（API 返回 401；刷新令牌仍可刷新）。
func (s *Server) RevokeTokens() {
	s.locked(func() {
		for _, token := range s.tokens {
			token.valid = false
		}
	})
}

// RevokeAll 同时让刷新令牌失效。
func (s *Server) RevokeAll() {
	s.locked(func() {
		for _, token := range s.tokens {
			token.valid = false
		}
		s.refresh = map[string]string{}
	})
}

// ExpireTokens 让访问令牌立即过期（401），刷新令牌仍然有效。
func (s *Server) ExpireTokens() {
	s.locked(func() {
		for _, token := range s.tokens {
			token.expires = time.Now().Add(-time.Minute)
		}
	})
}

func (s *Server) Notes() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Note(nil), s.notes...)
}

// Challenges 返回授权页收到的 code_challenge（Gitee 没有 PKCE，客户端不应发送，所以应为空）。
func (s *Server) Challenges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.challenges...)
}

func (s *Server) RefreshCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.refreshCalls }
func (s *Server) TokenCount() int   { s.mu.Lock(); defer s.mu.Unlock(); return len(s.tokens) }

// QueryTokenWrites 返回被拒绝的「POST / PATCH 把令牌放在 URL 查询串」的次数。
func (s *Server) QueryTokenWrites() int { s.mu.Lock(); defer s.mu.Unlock(); return s.queryWrites }

// MissingUserAgent 返回缺少 User-Agent 的令牌请求次数。
func (s *Server) MissingUserAgent() int { s.mu.Lock(); defer s.mu.Unlock(); return s.missingUA }

// AddIssue 模拟在 Gitee 上直接创建 Issue（用于「导入已有 Issue」）。
func (s *Server) AddIssue(repo, title, body string, authorID int64, authorLogin string) *IssueRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newIssue(repo, title, body, authorID, authorLogin, "")
}

func (s *Server) Issues(repo string) []IssueRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.issues[strings.ToLower(repo)]
	out := make([]IssueRecord, 0, len(items))
	for _, item := range items {
		out = append(out, *item)
	}
	return out
}

// EditIssue 模拟在 Gitee 上直接修改 Issue。
func (s *Server) EditIssue(repo, number string, edit func(*IssueRecord)) {
	s.locked(func() {
		for _, item := range s.issues[strings.ToLower(repo)] {
			if item.Number == number {
				edit(item)
				item.UpdatedAt = time.Now().UTC()
			}
		}
	})
}

func (s *Server) nextID() int {
	s.counter++
	return s.counter
}

func (s *Server) newIssue(repo, title, body string, authorID int64, authorLogin, assignee string) *IssueRecord {
	key := strings.ToLower(repo)
	n := len(s.issues[key]) + 1
	now := time.Now().UTC()
	record := &IssueRecord{ID: int64(300000000 + s.nextID()), Number: fmt.Sprintf("IT%s%02d", strings.ToUpper(strconv.FormatInt(int64(len(key)), 36)), n),
		Repo: repo, Title: title, Body: body, State: "open", AuthorID: authorID, AuthorLogin: authorLogin, Assignee: assignee, CreatedAt: now, UpdatedAt: now}
	s.issues[key] = append(s.issues[key], record)
	return record
}

// ---- HTTP ----

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("GET /api/v5/user", s.api(s.user))
	mux.HandleFunc("GET /api/v5/user/orgs", s.api(s.orgs))
	mux.HandleFunc("GET /api/v5/user/repos", s.api(s.userRepos))
	mux.HandleFunc("GET /api/v5/orgs/{org}/repos", s.api(s.orgRepos))
	mux.HandleFunc("GET /api/v5/repos/{owner}/{repo}", s.api(s.repo))
	mux.HandleFunc("GET /api/v5/repos/{owner}/{repo}/issues", s.api(s.listIssues))
	mux.HandleFunc("POST /api/v5/repos/{owner}/issues", s.api(s.createIssue))
	mux.HandleFunc("PATCH /api/v5/repos/{owner}/issues/{number}", s.api(s.updateIssue))
	mux.HandleFunc("POST /api/v5/repos/{owner}/{repo}/issues/{number}/comments", s.api(s.issueComment))
	mux.HandleFunc("POST /api/v5/repos/{owner}/{repo}/pulls/{number}/comments", s.api(s.pullComment))
	return mux
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if query.Get("client_id") != s.opts.ClientID || err != nil || redirect.Scheme == "" || redirect.Host == "" || query.Get("response_type") != "code" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if challenge := query.Get("code_challenge"); challenge != "" {
		s.challenges = append(s.challenges, challenge)
	}
	out := redirect.Query()
	out.Set("state", query.Get("state"))
	if s.deny {
		out.Set("error", "access_denied")
	} else {
		code := fmt.Sprintf("gcode-%d", s.nextID())
		s.codes[code] = pendingCode{login: s.opts.Login, userID: s.opts.UserID}
		out.Set("code", code)
	}
	redirect.RawQuery = out.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) issueToken(login string, userID int64) map[string]any {
	access := fmt.Sprintf("gtok-fake-%d", s.nextID())
	refresh := fmt.Sprintf("grt-fake-%d", s.nextID())
	s.tokens[access] = &tokenRecord{valid: true, login: login, userID: userID, expires: time.Now().Add(time.Duration(s.opts.TokenTTL) * time.Second), refresh: refresh}
	s.refresh[refresh] = access
	return map[string]any{"access_token": access, "token_type": "bearer", "expires_in": s.opts.TokenTTL, "refresh_token": refresh,
		"scope": "user_info projects pull_requests issues notes groups", "created_at": time.Now().Unix()}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Header.Get("User-Agent") == "" {
		s.missingUA++
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fail := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	form := r.PostForm
	switch form.Get("grant_type") {
	case "authorization_code":
		if form.Get("client_id") != s.opts.ClientID || form.Get("client_secret") != s.opts.ClientSecret {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		if s.exchangeFailed || form.Get("redirect_uri") == "" {
			fail("invalid_grant")
			return
		}
		pending, ok := s.codes[form.Get("code")]
		if !ok {
			fail("invalid_grant")
			return
		}
		delete(s.codes, form.Get("code"))
		_ = json.NewEncoder(w).Encode(s.issueToken(pending.login, pending.userID))
	case "refresh_token":
		s.refreshCalls++
		if s.refreshStatus != 0 {
			w.WriteHeader(s.refreshStatus)
			return
		}
		access, ok := s.refresh[form.Get("refresh_token")]
		if !ok {
			fail("invalid_grant")
			return
		}
		old := s.tokens[access]
		delete(s.refresh, form.Get("refresh_token"))
		old.valid = false
		_ = json.NewEncoder(w).Encode(s.issueToken(old.login, old.userID))
	default:
		fail("unsupported_grant_type")
	}
}

// api 包装 REST 处理器：限流、令牌校验（只认 access_token 参数）。处理器在持锁状态下执行。
func (s *Server) api(next func(w http.ResponseWriter, r *http.Request, token *tokenRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.rateLimit.IsZero() && time.Now().Before(s.rateLimit) {
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(s.rateLimit).Seconds())+1))
			http.Error(w, `{"message":"Retry later"}`, http.StatusTooManyRequests)
			return
		}
		raw := ""
		if r.Method == http.MethodGet || r.Method == http.MethodDelete {
			raw = r.URL.Query().Get("access_token")
		} else {
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			raw = r.PostForm.Get("access_token")
			pullComment := strings.Contains(r.URL.Path, "/pulls/") && strings.HasSuffix(r.URL.Path, "/comments")
			if r.URL.Query().Get("access_token") != "" && !pullComment {
				s.queryWrites++
				http.Error(w, `{"message":"token in url not accepted by the fake"}`, http.StatusBadRequest)
				return
			}
			if raw == "" {
				raw = r.URL.Query().Get("access_token")
			}
		}
		token, ok := s.tokens[raw]
		if !ok || !token.valid || time.Now().After(token.expires) {
			http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		next(w, r, token)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// paginate 取出第 page 页，并设置 total_count / total_page 头（Gitee 的分页头）。
func paginate[T any](w http.ResponseWriter, r *http.Request, items []T, defaultPer int) []T {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if per < 1 || per > 100 {
		per = defaultPer
	}
	w.Header().Set("total_count", strconv.Itoa(len(items)))
	w.Header().Set("total_page", strconv.Itoa((len(items)+per-1)/per))
	start, end := (page-1)*per, page*per
	if start > len(items) {
		start = len(items)
	}
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func (s *Server) user(w http.ResponseWriter, _ *http.Request, token *tokenRecord) {
	writeJSON(w, http.StatusOK, map[string]any{"id": token.userID, "login": token.login, "name": token.login,
		"avatar_url": "https://avatars.example.test/u/" + strconv.FormatInt(token.userID, 10)})
}

func (s *Server) orgs(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	items := make([]map[string]any, 0, len(s.opts.Orgs))
	for i, path := range s.opts.Orgs {
		items = append(items, map[string]any{"id": 6000 + i, "login": path, "name": "组织 " + path, "avatar_url": "https://avatars.example.test/g/" + path})
	}
	writeJSON(w, http.StatusOK, paginate(w, r, items, 20))
}

func repoJSON(repo Repo) map[string]any {
	owner, name, _ := strings.Cut(repo.Path, "/")
	return map[string]any{"id": 1, "full_name": repo.Path, "human_name": "显示名/" + name, "name": name, "path": name, "private": repo.Private,
		"public": !repo.Private, "html_url": "https://gitee.example.test/" + repo.Path, "namespace": map[string]any{"path": owner, "name": owner}}
}

func (s *Server) reposUnder(namespace, search string) []map[string]any {
	out := []map[string]any{}
	needle := strings.ToLower(search)
	repos := append([]Repo(nil), s.repos...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	for _, repo := range repos {
		owner, _, _ := strings.Cut(repo.Path, "/")
		if !strings.EqualFold(owner, namespace) || (needle != "" && !strings.Contains(strings.ToLower(repo.Path), needle)) {
			continue
		}
		out = append(out, repoJSON(repo))
	}
	return out
}

func (s *Server) orgRepos(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	writeJSON(w, http.StatusOK, paginate(w, r, s.reposUnder(r.PathValue("org"), ""), 20))
}

func (s *Server) userRepos(w http.ResponseWriter, r *http.Request, token *tokenRecord) {
	writeJSON(w, http.StatusOK, paginate(w, r, s.reposUnder(token.login, r.URL.Query().Get("q")), 20))
}

func (s *Server) findRepo(owner, name string) (Repo, bool) {
	for _, repo := range s.repos {
		if strings.EqualFold(repo.Path, owner+"/"+name) {
			return repo, true
		}
	}
	return Repo{}, false
}

func (s *Server) repo(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	repo, ok := s.findRepo(r.PathValue("owner"), r.PathValue("repo"))
	if !ok {
		http.Error(w, `{"message":"Not Found Project"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, repoJSON(repo))
}

func (s *Server) userByLogin(login string) *User {
	for i := range s.users {
		if strings.EqualFold(s.users[i].Login, login) {
			return &s.users[i]
		}
	}
	return nil
}

func (s *Server) issueJSON(issue *IssueRecord) map[string]any {
	zone := time.FixedZone("CST", 8*3600)
	out := map[string]any{"id": issue.ID, "number": issue.Number, "title": issue.Title, "body": issue.Body, "state": issue.State,
		"html_url": "https://gitee.example.test/" + issue.Repo + "/issues/" + issue.Number,
		"user":     map[string]any{"id": issue.AuthorID, "login": issue.AuthorLogin}, "assignee": nil,
		"created_at": issue.CreatedAt.In(zone).Format(time.RFC3339), "updated_at": issue.UpdatedAt.In(zone).Format(time.RFC3339)}
	if issue.Assignee != "" {
		id := int64(0)
		if user := s.userByLogin(issue.Assignee); user != nil {
			id = user.ID
		}
		out["assignee"] = map[string]any{"id": id, "login": issue.Assignee}
	}
	return out
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	owner, name := r.PathValue("owner"), r.PathValue("repo")
	if _, ok := s.findRepo(owner, name); !ok {
		http.Error(w, `{"message":"Not Found Project"}`, http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	state, creator := query.Get("state"), query.Get("creator")
	if state == "" {
		state = "open"
	}
	var since time.Time
	if raw := query.Get("since"); raw != "" {
		since, _ = time.Parse(time.RFC3339, raw)
	}
	items := []map[string]any{}
	for _, issue := range s.issues[strings.ToLower(owner+"/"+name)] {
		if state != "all" && issue.State != state {
			continue
		}
		if creator != "" && !strings.EqualFold(issue.AuthorLogin, creator) {
			continue
		}
		if !since.IsZero() && issue.UpdatedAt.Before(since) {
			continue
		}
		items = append(items, s.issueJSON(issue))
	}
	writeJSON(w, http.StatusOK, paginate(w, r, items, 20))
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, token *tokenRecord) {
	owner, name := r.PathValue("owner"), r.PostForm.Get("repo")
	if _, ok := s.findRepo(owner, name); !ok {
		http.Error(w, `{"message":"Not Found Project"}`, http.StatusNotFound)
		return
	}
	if s.writeCode != 0 {
		http.Error(w, `{"message":"error"}`, s.writeCode)
		return
	}
	title := r.PostForm.Get("title")
	if title == "" {
		http.Error(w, `{"message":"title is missing"}`, http.StatusBadRequest)
		return
	}
	issue := s.newIssue(owner+"/"+name, title, r.PostForm.Get("body"), token.userID, token.login, r.PostForm.Get("assignee"))
	if s.dropCreate {
		s.dropCreate = false
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	writeJSON(w, http.StatusCreated, s.issueJSON(issue))
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	owner, name, number := r.PathValue("owner"), r.PostForm.Get("repo"), r.PathValue("number")
	if s.writeCode != 0 {
		http.Error(w, `{"message":"error"}`, s.writeCode)
		return
	}
	for _, issue := range s.issues[strings.ToLower(owner+"/"+name)] {
		if issue.Number != number {
			continue
		}
		form := r.PostForm
		if form.Has("title") {
			issue.Title = form.Get("title")
		}
		if form.Has("body") {
			issue.Body = form.Get("body")
		}
		if form.Has("assignee") {
			issue.Assignee = form.Get("assignee")
		}
		if state := form.Get("state"); state != "" {
			issue.State = state
		}
		issue.UpdatedAt = time.Now().UTC()
		writeJSON(w, http.StatusOK, s.issueJSON(issue))
		return
	}
	http.Error(w, `{"message":"Not Found Issue"}`, http.StatusNotFound)
}

func (s *Server) note(w http.ResponseWriter, r *http.Request, kind string) {
	if s.noteStatus == http.StatusUnauthorized {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if s.noteStatus != 0 && s.noteStatus != http.StatusCreated {
		http.Error(w, `{"message":"error"}`, s.noteStatus)
		return
	}
	body := r.PostForm.Get("body")
	if body == "" {
		http.Error(w, `{"message":"body is missing"}`, http.StatusBadRequest)
		return
	}
	id := int64(9000 + s.nextID())
	s.notes = append(s.notes, Note{Repo: r.PathValue("owner") + "/" + r.PathValue("repo"), Kind: kind, Number: r.PathValue("number"), Body: body, ID: id,
		Token: r.PostForm.Get("access_token")})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "body": body})
}

func (s *Server) issueComment(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	s.note(w, r, "issues")
}

func (s *Server) pullComment(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	s.note(w, r, "pulls")
}

// ---- Webhook 请求构造 ----

func newHookRequest(targetURL, event string, payload []byte) *http.Request {
	req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(payload)))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "git-oschina-hook")
	req.Header.Set("X-Gitee-Event", event)
	return req
}

// PasswordWebhook 构造「密码」模式的 Webhook 请求：X-Gitee-Token 是明文密码。
func PasswordWebhook(targetURL, password, event string, timestamp time.Time, payload []byte) *http.Request {
	req := newHookRequest(targetURL, event, payload)
	req.Header.Set("X-Gitee-Token", password)
	req.Header.Set("X-Gitee-Timestamp", strconv.FormatInt(timestamp.UnixMilli(), 10))
	return req
}

// Signature 按官方文档计算签名：Base64(HmacSHA256(secret, timestamp + "\n" + secret))，urlEncode 为真时再 URL 编码。
func Signature(secret string, timestampMillis int64, urlEncode bool) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestampMillis, 10) + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if urlEncode {
		return url.QueryEscape(sign)
	}
	return sign
}

// SignedWebhook 构造「签名密钥」模式的 Webhook 请求。
func SignedWebhook(targetURL, secret, event string, timestamp time.Time, urlEncode bool, payload []byte) *http.Request {
	req := newHookRequest(targetURL, event, payload)
	req.Header.Set("X-Gitee-Token", Signature(secret, timestamp.UnixMilli(), urlEncode))
	req.Header.Set("X-Gitee-Timestamp", strconv.FormatInt(timestamp.UnixMilli(), 10))
	return req
}
