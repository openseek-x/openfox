// Package fakegitlab 是本机模拟的 GitLab / 极狐（OAuth 授权码 + PKCE、令牌刷新、REST API v4 的用户、群组、项目、Issue、评论），
// 供集成测试与浏览器联调使用，绝不访问真实的 GitLab 实例。
package fakegitlab

import (
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
	// ClientID / ClientSecret 是实例上创建的 OAuth 应用（Application ID / Secret）。
	ClientID     string
	ClientSecret string
	// Login / UserID 是授权者本人；Groups 是授权者所属的顶级群组 path。
	Login  string
	UserID int64
	Groups []string
	// TokenTTL 是访问令牌有效期（秒），默认 7200（与 GitLab 一致）。
	TokenTTL int
}

// Repo 是模拟的项目。
type Repo struct {
	Path    string // path_with_namespace，例如 openfox-dev/app 或 openfox-dev/sub/app
	Private bool
}

// User 是实例上的用户（assignee_ids 查询用）。
type User struct {
	ID       int64
	Username string
}

// IssueRecord 是模拟的 Issue。
type IssueRecord struct {
	ID          int64
	IID         int
	Project     string
	Title       string
	Description string
	State       string // opened / closed
	AuthorID    int64
	AuthorLogin string
	Assignees   []User
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Note 是收到的一条评论。
type Note struct {
	Project string
	Kind    string // issues / merge_requests
	IID     int
	Body    string
	ID      int64
	Token   string
}

type pendingCode struct {
	challenge string
	login     string
	userID    int64
}

type tokenRecord struct {
	valid   bool
	login   string
	userID  int64
	expires time.Time
	refresh string
	scopes  []string
}

// Server 是模拟服务；通过 Handler 挂到任意监听器，或用 New 直接起一个 httptest 服务。
type Server struct {
	URL string

	mu         sync.Mutex
	opts       Options
	httpServer *httptest.Server
	codes      map[string]pendingCode
	tokens     map[string]*tokenRecord
	refresh    map[string]string // 刷新令牌 → 访问令牌（刷新后旧的立即失效）
	repos      []Repo
	users      []User
	issues     map[string][]*IssueRecord
	notes      []Note
	challenges []string
	counter    int

	deny           bool
	exchangeFailed bool
	refreshStatus  int       // 非零时 /oauth/token 的刷新请求返回该状态码（例如 500）
	rateLimit      time.Time // 非零表示限流中（429 + Retry-After）
	dropCreate     bool      // 下一次 POST /issues 建好后断开连接
	writeCode      int       // 非零时 POST / PUT issues 返回该状态码
	noteStatus     int       // 非零时评论接口返回该状态码（默认 201）
	refreshCalls   int
	apiCalls       int
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
		opts.ClientID = "fake-app-id"
	}
	if opts.ClientSecret == "" {
		opts.ClientSecret = "fake-app-secret"
	}
	if opts.Login == "" {
		opts.Login = "lin"
	}
	if opts.UserID == 0 {
		opts.UserID = 4242
	}
	if opts.TokenTTL == 0 {
		opts.TokenTTL = 7200
	}
	s := &Server{opts: opts, codes: map[string]pendingCode{}, tokens: map[string]*tokenRecord{}, refresh: map[string]string{},
		issues: map[string][]*IssueRecord{}}
	s.users = append(s.users, User{ID: opts.UserID, Username: opts.Login})
	return s
}

// Close 关闭 httptest 服务。
func (s *Server) Close() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

// ---- 测试控制 ----

func (s *Server) locked(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); fn() }

// DenyAuthorize 让授权页返回 error=access_denied。
func (s *Server) DenyAuthorize(deny bool) { s.locked(func() { s.deny = deny }) }

// FailExchange 让授权码换取令牌失败。
func (s *Server) FailExchange(fail bool) { s.locked(func() { s.exchangeFailed = fail }) }

// SetRefreshStatus 让刷新请求返回指定状态码（例如 500 模拟服务故障；0 恢复正常）。
func (s *Server) SetRefreshStatus(status int) { s.locked(func() { s.refreshStatus = status }) }

// SetNoteStatus 设置评论接口的状态码（例如 401）；0 表示默认 201。
func (s *Server) SetNoteStatus(status int) { s.locked(func() { s.noteStatus = status }) }

// SetWriteStatus 让创建 / 修改 Issue 返回指定状态码；0 恢复正常。
func (s *Server) SetWriteStatus(status int) { s.locked(func() { s.writeCode = status }) }

// DropNextCreate 让下一次创建 Issue 建好后断开连接（客户端拿不到响应）。
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

// SetIdentity 修改授权者与所属群组（下一次授权生效）。
func (s *Server) SetIdentity(login string, userID int64, groups ...string) {
	s.locked(func() {
		s.opts.Login, s.opts.UserID, s.opts.Groups = login, userID, groups
		s.addUser(User{ID: userID, Username: login})
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

// AddUser 注册实例上的用户（创建 Issue 时 assignee 的用户名要能查到 ID）。
func (s *Server) AddUser(id int64, username string) {
	s.locked(func() { s.addUser(User{ID: id, Username: username}) })
}

// AddRepo 增加一个项目。
func (s *Server) AddRepo(path string, private bool) {
	s.locked(func() { s.repos = append(s.repos, Repo{Path: path, Private: private}) })
}

// RevokeTokens 让所有已发出的访问令牌失效（之后的 API 调用返回 401；刷新令牌仍可用于刷新）。
func (s *Server) RevokeTokens() {
	s.locked(func() {
		for _, token := range s.tokens {
			token.valid = false
		}
	})
}

// RevokeAll 同时让刷新令牌失效（模拟用户在实例上撤销了应用授权）。
func (s *Server) RevokeAll() {
	s.locked(func() {
		for _, token := range s.tokens {
			token.valid = false
		}
		s.refresh = map[string]string{}
	})
}

// ExpireTokens 让所有访问令牌立即过期（401），刷新令牌仍然有效。
func (s *Server) ExpireTokens() {
	s.locked(func() {
		for _, token := range s.tokens {
			token.expires = time.Now().Add(-time.Minute)
		}
	})
}

// Notes 返回收到的评论副本。
func (s *Server) Notes() []Note {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Note(nil), s.notes...)
}

// Challenges 返回授权页收到的 code_challenge。
func (s *Server) Challenges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.challenges...)
}

// RefreshCalls 返回收到的刷新请求次数。
func (s *Server) RefreshCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.refreshCalls }

// TokenFor 返回某个访问令牌的当前刷新令牌（测试断言用）。
func (s *Server) TokenCount() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.tokens) }

// AddPersonalToken supplies a test-only PAT to the real HTTP client.
func (s *Server) AddPersonalToken(token, login string, userID int64, scopes ...string) {
	s.locked(func() {
		s.tokens[token] = &tokenRecord{valid: true, login: login, userID: userID, scopes: scopes, expires: time.Now().Add(30 * 24 * time.Hour)}
	})
}

// AddIssue 模拟在实例上直接创建 Issue（用于「导入已有 Issue」）。
func (s *Server) AddIssue(project, title, description string, authorID int64, authorLogin string) *IssueRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newIssue(project, title, description, authorID, authorLogin, nil)
}

// Issues 返回项目 Issue 的快照。
func (s *Server) Issues(project string) []IssueRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.issues[strings.ToLower(project)]
	out := make([]IssueRecord, 0, len(items))
	for _, item := range items {
		out = append(out, *item)
	}
	return out
}

// CloseIssue / EditIssue 模拟在实例上直接修改 Issue。
func (s *Server) EditIssue(project string, iid int, edit func(*IssueRecord)) {
	s.locked(func() {
		for _, item := range s.issues[strings.ToLower(project)] {
			if item.IID == iid {
				edit(item)
				item.UpdatedAt = time.Now().UTC()
			}
		}
	})
}

func (s *Server) newIssue(project, title, description string, authorID int64, authorLogin string, assignees []User) *IssueRecord {
	key := strings.ToLower(project)
	iid := len(s.issues[key]) + 1
	now := time.Now().UTC()
	record := &IssueRecord{ID: int64(70000 + s.nextID()), IID: iid, Project: project, Title: title, Description: description, State: "opened",
		AuthorID: authorID, AuthorLogin: authorLogin, Assignees: assignees, CreatedAt: now, UpdatedAt: now}
	s.issues[key] = append(s.issues[key], record)
	return record
}

func (s *Server) nextID() int {
	s.counter++
	return s.counter
}

// ---- HTTP ----

// Handler 返回模拟服务的路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("GET /api/v4/user", s.api(s.user))
	mux.HandleFunc("GET /api/v4/personal_access_tokens/self", s.api(func(w http.ResponseWriter, r *http.Request, token *tokenRecord) {
		writeJSON(w, http.StatusOK, map[string]any{"active": token.valid, "revoked": !token.valid, "scopes": token.scopes, "expires_at": token.expires.UTC().Format("2006-01-02")})
	}))
	mux.HandleFunc("GET /api/v4/projects", s.api(func(w http.ResponseWriter, r *http.Request, token *tokenRecord) {
		var projects []any
		for _, repo := range s.repos {
			projects = append(projects, projectJSON(repo))
		}
		writeJSON(w, http.StatusOK, projects)
	}))
	mux.HandleFunc("GET /api/v4/users", s.api(s.listUsers))
	mux.HandleFunc("GET /api/v4/groups", s.api(s.groups))
	mux.HandleFunc("GET /api/v4/groups/{id}/projects", s.api(s.groupProjects))
	mux.HandleFunc("GET /api/v4/users/{username}/projects", s.api(s.userProjects))
	mux.HandleFunc("GET /api/v4/projects/{project}", s.api(s.project))
	mux.HandleFunc("GET /api/v4/projects/{project}/issues", s.api(s.listIssues))
	mux.HandleFunc("POST /api/v4/projects/{project}/issues", s.api(s.createIssue))
	mux.HandleFunc("PUT /api/v4/projects/{project}/issues/{iid}", s.api(s.updateIssue))
	mux.HandleFunc("POST /api/v4/projects/{project}/{kind}/{iid}/notes", s.api(s.note))
	return mux
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if query.Get("client_id") != s.opts.ClientID || err != nil || redirect.Scheme == "" || redirect.Host == "" ||
		query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" {
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
		s.codes[code] = pendingCode{challenge: query.Get("code_challenge"), login: s.opts.Login, userID: s.opts.UserID}
		out.Set("code", code)
	}
	redirect.RawQuery = out.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) issueToken(login string, userID int64) map[string]any {
	access := fmt.Sprintf("glpat-fake-%d", s.nextID())
	refresh := fmt.Sprintf("glrt-fake-%d", s.nextID())
	ttl := time.Duration(s.opts.TokenTTL) * time.Second
	s.tokens[access] = &tokenRecord{valid: true, login: login, userID: userID, expires: time.Now().Add(ttl), refresh: refresh}
	s.refresh[refresh] = access
	return map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": s.opts.TokenTTL, "refresh_token": refresh,
		"scope": "api", "created_at": time.Now().Unix()}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
	}
	form := r.PostForm
	if form.Get("client_id") != s.opts.ClientID || form.Get("client_secret") != s.opts.ClientSecret {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		if s.exchangeFailed {
			fail("invalid_grant")
			return
		}
		pending, ok := s.codes[form.Get("code")]
		if !ok {
			fail("invalid_grant")
			return
		}
		delete(s.codes, form.Get("code"))
		sum := sha256.Sum256([]byte(form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != pending.challenge {
			fail("invalid_grant")
			return
		}
		_ = json.NewEncoder(w).Encode(s.issueToken(pending.login, pending.userID))
	case "refresh_token":
		s.refreshCalls++
		if s.refreshStatus != 0 {
			w.WriteHeader(s.refreshStatus)
			return
		}
		access, ok := s.refresh[form.Get("refresh_token")]
		if !ok || form.Get("redirect_uri") == "" {
			fail("invalid_grant")
			return
		}
		old := s.tokens[access]
		delete(s.refresh, form.Get("refresh_token"))
		old.valid = false // 刷新后旧的访问令牌与刷新令牌立即失效
		_ = json.NewEncoder(w).Encode(s.issueToken(old.login, old.userID))
	default:
		fail("unsupported_grant_type")
	}
}

// api 包装 REST 处理器：限流、令牌校验。处理器在持锁状态下执行。
func (s *Server) api(next func(w http.ResponseWriter, r *http.Request, token *tokenRecord)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.apiCalls++
		if !s.rateLimit.IsZero() && time.Now().Before(s.rateLimit) {
			w.Header().Set("Retry-After", strconv.Itoa(int(time.Until(s.rateLimit).Seconds())+1))
			http.Error(w, `{"message":"Retry later"}`, http.StatusTooManyRequests)
			return
		}
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
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

// paginate 取出第 page 页（每页 perPage），并设置 X-Next-Page。
func paginate[T any](w http.ResponseWriter, r *http.Request, items []T, defaultPer int) []T {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if per < 1 || per > 100 {
		per = defaultPer
	}
	start, end := (page-1)*per, page*per
	if start > len(items) {
		start = len(items)
	}
	if end >= len(items) {
		end = len(items)
	} else {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	return items[start:end]
}

func (s *Server) user(w http.ResponseWriter, _ *http.Request, token *tokenRecord) {
	writeJSON(w, http.StatusOK, map[string]any{"id": token.userID, "username": token.login,
		"avatar_url": "https://avatars.example.test/u/" + strconv.FormatInt(token.userID, 10)})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	name := r.URL.Query().Get("username")
	out := []map[string]any{}
	for _, user := range s.users {
		if strings.EqualFold(user.Username, name) {
			out = append(out, map[string]any{"id": user.ID, "username": user.Username})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) groups(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	items := make([]map[string]any, 0, len(s.opts.Groups))
	for i, path := range s.opts.Groups {
		items = append(items, map[string]any{"id": 2000 + i, "path": path, "full_path": path, "avatar_url": "https://avatars.example.test/g/" + path})
	}
	writeJSON(w, http.StatusOK, paginate(w, r, items, 20))
}

func projectJSON(repo Repo) map[string]any {
	owner, _, _ := strings.Cut(repo.Path, "/")
	name := repo.Path[strings.LastIndex(repo.Path, "/")+1:]
	visibility := "public"
	if repo.Private {
		visibility = "private"
	}
	return map[string]any{"permissions": map[string]any{"project_access": map[string]int{"access_level": 40}}, "id": 1, "name": name, "path": name, "path_with_namespace": repo.Path, "visibility": visibility,
		"web_url": "https://gitlab.example.test/" + repo.Path, "namespace": map[string]any{"full_path": owner}}
}

func (s *Server) projectsUnder(namespace, search string) []map[string]any {
	out := []map[string]any{}
	needle := strings.ToLower(search)
	repos := append([]Repo(nil), s.repos...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	for _, repo := range repos {
		owner, _, _ := strings.Cut(repo.Path, "/")
		if !strings.EqualFold(owner, namespace) || (needle != "" && !strings.Contains(strings.ToLower(repo.Path), needle)) {
			continue
		}
		out = append(out, projectJSON(repo))
	}
	return out
}

func (s *Server) groupProjects(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	writeJSON(w, http.StatusOK, paginate(w, r, s.projectsUnder(r.PathValue("id"), r.URL.Query().Get("search")), 20))
}

func (s *Server) userProjects(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	writeJSON(w, http.StatusOK, paginate(w, r, s.projectsUnder(r.PathValue("username"), r.URL.Query().Get("search")), 20))
}

func (s *Server) findRepo(path string) (Repo, bool) {
	for _, repo := range s.repos {
		if strings.EqualFold(repo.Path, path) {
			return repo, true
		}
	}
	return Repo{}, false
}

func (s *Server) project(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	repo, ok := s.findRepo(r.PathValue("project"))
	if !ok {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, projectJSON(repo))
}

func issueJSON(issue *IssueRecord) map[string]any {
	assignees := make([]map[string]any, 0, len(issue.Assignees))
	for _, a := range issue.Assignees {
		assignees = append(assignees, map[string]any{"id": a.ID, "username": a.Username})
	}
	return map[string]any{"id": issue.ID, "iid": issue.IID, "title": issue.Title, "description": issue.Description, "state": issue.State,
		"web_url":   "https://gitlab.example.test/" + issue.Project + "/-/issues/" + strconv.Itoa(issue.IID),
		"author":    map[string]any{"id": issue.AuthorID, "username": issue.AuthorLogin},
		"assignees": assignees, "created_at": issue.CreatedAt.Format(time.RFC3339), "updated_at": issue.UpdatedAt.Format(time.RFC3339)}
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	project := r.PathValue("project")
	if _, ok := s.findRepo(project); !ok {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	state, author := query.Get("state"), query.Get("author_username")
	var since time.Time
	if raw := query.Get("updated_after"); raw != "" {
		since, _ = time.Parse(time.RFC3339, raw)
	}
	items := []map[string]any{}
	for _, issue := range s.issues[strings.ToLower(project)] {
		if state != "all" && state != "" && issue.State != state {
			continue
		}
		if author != "" && !strings.EqualFold(issue.AuthorLogin, author) {
			continue
		}
		if !since.IsZero() && issue.UpdatedAt.Before(since) {
			continue
		}
		items = append(items, issueJSON(issue))
	}
	writeJSON(w, http.StatusOK, paginate(w, r, items, 20))
}

func (s *Server) lookupUsers(ids []int64) []User {
	out := []User{}
	for _, id := range ids {
		for _, user := range s.users {
			if user.ID == id {
				out = append(out, user)
			}
		}
	}
	return out
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, token *tokenRecord) {
	project := r.PathValue("project")
	if _, ok := s.findRepo(project); !ok {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
		return
	}
	if s.writeCode != 0 {
		http.Error(w, `{"message":"error"}`, s.writeCode)
		return
	}
	var body struct {
		Title       string  `json:"title"`
		Description string  `json:"description"`
		AssigneeIDs []int64 `json:"assignee_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		http.Error(w, `{"message":"title is missing"}`, http.StatusBadRequest)
		return
	}
	issue := s.newIssue(project, body.Title, body.Description, token.userID, token.login, s.lookupUsers(body.AssigneeIDs))
	if s.dropCreate {
		s.dropCreate = false
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	writeJSON(w, http.StatusCreated, issueJSON(issue))
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	project := r.PathValue("project")
	iid, _ := strconv.Atoi(r.PathValue("iid"))
	if s.writeCode != 0 {
		http.Error(w, `{"message":"error"}`, s.writeCode)
		return
	}
	var body struct {
		Title       *string  `json:"title"`
		Description *string  `json:"description"`
		AssigneeIDs *[]int64 `json:"assignee_ids"`
		StateEvent  string   `json:"state_event"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, issue := range s.issues[strings.ToLower(project)] {
		if issue.IID != iid {
			continue
		}
		if body.Title != nil {
			issue.Title = *body.Title
		}
		if body.Description != nil {
			issue.Description = *body.Description
		}
		if body.AssigneeIDs != nil {
			issue.Assignees = s.lookupUsers(*body.AssigneeIDs)
		}
		switch body.StateEvent {
		case "close":
			issue.State = "closed"
		case "reopen":
			issue.State = "opened"
		}
		issue.UpdatedAt = time.Now().UTC()
		writeJSON(w, http.StatusOK, issueJSON(issue))
		return
	}
	http.Error(w, `{"message":"404 Not found"}`, http.StatusNotFound)
}

func (s *Server) note(w http.ResponseWriter, r *http.Request, _ *tokenRecord) {
	kind := r.PathValue("kind")
	if kind != "issues" && kind != "merge_requests" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if s.noteStatus == http.StatusUnauthorized {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if s.noteStatus != 0 && s.noteStatus != http.StatusCreated {
		http.Error(w, `{"message":"error"}`, s.noteStatus)
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Body == "" {
		http.Error(w, `{"message":"body is missing"}`, http.StatusBadRequest)
		return
	}
	iid, _ := strconv.Atoi(r.PathValue("iid"))
	id := int64(9000 + s.nextID())
	s.notes = append(s.notes, Note{Project: r.PathValue("project"), Kind: kind, IID: iid, Body: body.Body, ID: id,
		Token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "body": body.Body})
}

// SignedWebhook 构造 GitLab Webhook 请求：X-Gitlab-Token 是明文 Secret token；idempotencyKey 为空时不带 Idempotency-Key 头。
func SignedWebhook(targetURL, secret, event, idempotencyKey string, payload []byte) *http.Request {
	req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(string(payload)))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "GitLab/17.4.0")
	req.Header.Set("X-Gitlab-Event", event)
	if secret != "" {
		req.Header.Set("X-Gitlab-Token", secret)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
		req.Header.Set("X-Gitlab-Webhook-UUID", "hook-uuid")
	}
	return req
}
