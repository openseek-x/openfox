package fakegithub

// 模拟 GitHub 的仓库与 Issue 接口、故障注入与 Webhook 负载构造。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Repo 是模拟的仓库。
type Repo struct {
	FullName string
	Private  bool
}

// IssueRecord 是模拟服务里的一个 Issue（或 PR 条目）。
type IssueRecord struct {
	Number      int
	ID          int64
	Title       string
	Body        string
	State       string // open / closed
	StateReason string
	Login       string // 创建者
	UserID      int64
	Assignees   []string
	IsPR        bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	// Token 是创建这个 Issue 的令牌（测试里区分个人令牌与组织令牌）。
	Token string
}

// Patch 是收到的一次 PATCH Issue 请求。
type Patch struct {
	Repo   string
	Number int
	Body   map[string]any
	Token  string
}

func owner(fullName string) string { o, _, _ := strings.Cut(fullName, "/"); return o }

// AddRepo 增加一个仓库（owner 取全名的前半）。
func (s *Server) AddRepo(fullName string, private bool) {
	s.mu.Lock()
	s.repos = append(s.repos, Repo{FullName: fullName, Private: private})
	s.mu.Unlock()
}

func (s *Server) repo(fullName string) (Repo, bool) {
	for _, repo := range s.repos {
		if strings.EqualFold(repo.FullName, fullName) {
			return repo, true
		}
	}
	return Repo{}, false
}

// SeedIssue 在仓库里预置一个 Issue 并返回编号（isPR 为真时是混入的 PR 条目）。
func (s *Server) SeedIssue(repo, title, body string, isPR bool, assignees ...string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(repo)
	number := len(s.issues[key]) + 1
	now := time.Now().UTC().Truncate(time.Second)
	s.issues[key] = append(s.issues[key], &IssueRecord{Number: number, ID: int64(500000 + s.nextID()), Title: title, Body: body, State: "open",
		Login: "someone", UserID: 777, Assignees: assignees, IsPR: isPR, CreatedAt: now, UpdatedAt: now})
	return number
}

// Issues 返回仓库里的 Issue 副本（含 PR 条目）。
func (s *Server) Issues(repo string) []IssueRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]IssueRecord, 0)
	for _, item := range s.issues[strings.ToLower(repo)] {
		items = append(items, *item)
	}
	return items
}

// Patches 返回收到的 PATCH 请求副本。
func (s *Server) Patches() []Patch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Patch(nil), s.patches...)
}

// IssueCalls 返回 POST / PATCH issues 的总次数（含失败的）。
func (s *Server) IssueCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.issueCalls }

// SetRateLimit 让所有仓库 API 返回 403 限流（X-RateLimit-Reset 为 resetAt）；传零值解除。
func (s *Server) SetRateLimit(resetAt time.Time) { s.mu.Lock(); s.rateLimit = resetAt; s.mu.Unlock() }

// DropNextIssueCreate 让下一次 POST /issues 实际建好 Issue 后断开连接，客户端拿不到响应（创建结果未知）。
func (s *Server) DropNextIssueCreate() { s.mu.Lock(); s.dropCreate = true; s.mu.Unlock() }

// SetIssueWriteStatus 让 POST / PATCH issues 返回指定状态码（不创建、不修改）；传 0 恢复。
func (s *Server) SetIssueWriteStatus(status int) { s.mu.Lock(); s.writeCode = status; s.mu.Unlock() }

// LoginOfToken 返回令牌换取时的授权者 login。
func (s *Server) LoginOfToken(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenLogin[token]
}

// gate 校验令牌（401）与限流（403）；调用方已持有锁。返回 false 表示已写出响应。
func (s *Server) gate(w http.ResponseWriter, r *http.Request) bool {
	if !s.valid(r) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return false
	}
	if !s.rateLimit.IsZero() {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(s.rateLimit.Unix(), 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) routeIssues(mux *http.ServeMux) {
	mux.HandleFunc("GET /orgs/{org}/repos", s.listOrgRepos)
	mux.HandleFunc("GET /user/repos", s.listUserRepos)
	mux.HandleFunc("GET /repos/{owner}/{repo}", s.getRepo)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", s.listIssues)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues", s.createIssue)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", s.patchIssue)
}

func repoJSON(repo Repo, host string) map[string]any {
	_, name, _ := strings.Cut(repo.FullName, "/")
	return map[string]any{"full_name": repo.FullName, "name": name, "private": repo.Private,
		"html_url": "https://github.example.test/" + repo.FullName, "owner": map[string]any{"login": owner(repo.FullName)}}
}

func pageParams(r *http.Request, defaultSize int) (page, size int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	size, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	if size < 1 || size > 100 {
		size = defaultSize
	}
	return page, size
}

func nextLinkHeader(w http.ResponseWriter, r *http.Request, page int) {
	query := r.URL.Query()
	query.Set("page", strconv.Itoa(page+1))
	w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?%s>; rel="next"`, r.Host, r.URL.Path, query.Encode()))
}

func (s *Server) writeRepos(w http.ResponseWriter, r *http.Request, repos []Repo) {
	sort.Slice(repos, func(i, j int) bool { return repos[i].FullName < repos[j].FullName })
	page, size := pageParams(r, 50)
	start, end := (page-1)*size, page*size
	if start > len(repos) {
		start = len(repos)
	}
	if end > len(repos) {
		end = len(repos)
	} else if end < len(repos) {
		nextLinkHeader(w, r, page)
	}
	items := make([]map[string]any, 0)
	for _, repo := range repos[start:end] {
		items = append(items, repoJSON(repo, r.Host))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (s *Server) listOrgRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gate(w, r) {
		return
	}
	var repos []Repo
	for _, repo := range s.repos {
		if strings.EqualFold(owner(repo.FullName), r.PathValue("org")) {
			repos = append(repos, repo)
		}
	}
	s.writeRepos(w, r, repos)
}

func (s *Server) listUserRepos(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gate(w, r) {
		return
	}
	login := s.tokenLogin[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	var repos []Repo
	for _, repo := range s.repos {
		if strings.EqualFold(owner(repo.FullName), login) {
			repos = append(repos, repo)
		}
	}
	s.writeRepos(w, r, repos)
}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gate(w, r) {
		return
	}
	repo, ok := s.repo(r.PathValue("owner") + "/" + r.PathValue("repo"))
	if !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(repoJSON(repo, r.Host))
}

func issueJSON(repo string, item *IssueRecord) map[string]any {
	assignees := make([]map[string]any, 0, len(item.Assignees))
	for i, login := range item.Assignees {
		assignees = append(assignees, map[string]any{"id": assigneeID(login, i), "login": login})
	}
	out := map[string]any{"id": item.ID, "number": item.Number, "title": item.Title, "body": item.Body, "state": item.State,
		"html_url": fmt.Sprintf("https://github.example.test/%s/issues/%d", repo, item.Number),
		"user":     map[string]any{"id": item.UserID, "login": item.Login}, "assignees": assignees,
		"created_at": item.CreatedAt.Format(time.RFC3339), "updated_at": item.UpdatedAt.Format(time.RFC3339)}
	if item.StateReason != "" {
		out["state_reason"] = item.StateReason
	}
	if item.IsPR {
		out["pull_request"] = map[string]any{"url": "https://github.example.test/" + repo + "/pull/" + strconv.Itoa(item.Number)}
	}
	return out
}

// assigneeID 给没有显式编号的处理人生成稳定的数字 ID。
func assigneeID(login string, _ int) int64 {
	var sum int64 = 1000
	for _, c := range login {
		sum = sum*31 + int64(c)
	}
	return sum % 1000000
}

func (s *Server) listIssues(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gate(w, r) {
		return
	}
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	if _, ok := s.repo(full); !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	query := r.URL.Query()
	state := query.Get("state")
	if state == "" {
		state = "open"
	}
	var since time.Time
	if raw := query.Get("since"); raw != "" {
		since, _ = time.Parse(time.RFC3339, raw)
	}
	var matched []*IssueRecord
	for _, item := range s.issues[strings.ToLower(full)] {
		if state != "all" && item.State != state {
			continue
		}
		if creator := query.Get("creator"); creator != "" && !strings.EqualFold(item.Login, creator) {
			continue
		}
		if !since.IsZero() && item.UpdatedAt.Before(since) {
			continue
		}
		matched = append(matched, item)
	}
	page, size := pageParams(r, 30)
	start, end := (page-1)*size, page*size
	if start > len(matched) {
		start = len(matched)
	}
	if end > len(matched) {
		end = len(matched)
	} else if end < len(matched) {
		nextLinkHeader(w, r, page)
	}
	items := make([]map[string]any, 0)
	for _, item := range matched[start:end] {
		items = append(items, issueJSON(full, item))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gate(w, r) {
		return
	}
	s.issueCalls++
	if s.writeCode != 0 {
		http.Error(w, `{"message":"error"}`, s.writeCode)
		return
	}
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	if _, ok := s.repo(full); !ok {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	var body struct {
		Title     string   `json:"title"`
		Body      string   `json:"body"`
		Assignees []string `json:"assignees"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
		http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	key := strings.ToLower(full)
	now := time.Now().UTC().Truncate(time.Second)
	item := &IssueRecord{Number: len(s.issues[key]) + 1, ID: int64(500000 + s.nextID()), Title: body.Title, Body: body.Body, State: "open",
		Login: s.tokenLogin[token], UserID: 4242, Assignees: body.Assignees, CreatedAt: now, UpdatedAt: now, Token: token}
	s.issues[key] = append(s.issues[key], item)
	if s.dropCreate {
		s.dropCreate = false
		if hijacker, ok := w.(http.Hijacker); ok {
			if conn, _, err := hijacker.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(issueJSON(full, item))
}

func (s *Server) patchIssue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gate(w, r) {
		return
	}
	s.issueCalls++
	if s.writeCode != 0 {
		http.Error(w, `{"message":"error"}`, s.writeCode)
		return
	}
	full := r.PathValue("owner") + "/" + r.PathValue("repo")
	number, _ := strconv.Atoi(r.PathValue("number"))
	var target *IssueRecord
	for _, item := range s.issues[strings.ToLower(full)] {
		if item.Number == number {
			target = item
		}
	}
	if target == nil {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.patches = append(s.patches, Patch{Repo: full, Number: number, Body: body, Token: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
	if value, ok := body["title"].(string); ok {
		target.Title = value
	}
	if value, ok := body["body"].(string); ok {
		target.Body = value
	}
	if value, ok := body["state"].(string); ok {
		target.State = value
		target.StateReason = ""
		if reason, ok := body["state_reason"].(string); ok && value == "closed" {
			target.StateReason = reason
		}
	}
	if list, ok := body["assignees"].([]any); ok {
		target.Assignees = nil
		for _, entry := range list {
			if login, ok := entry.(string); ok {
				target.Assignees = append(target.Assignees, login)
			}
		}
	}
	target.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(issueJSON(full, target))
}

// ---- Webhook 负载 ----

// IssueEvent 描述 Webhook 里的 Issue。
type IssueEvent struct {
	Number      int
	ID          int64
	Title       string
	Body        string
	State       string // open / closed
	StateReason string
	Login       string
	UserID      int64
	Assignees   []EventUser
	CreatedAt   time.Time
}

// EventUser 是 Webhook 里的用户。
type EventUser struct {
	ID    int64
	Login string
}

func (e IssueEvent) json(repo string) map[string]any {
	assignees := make([]map[string]any, 0, len(e.Assignees))
	for _, a := range e.Assignees {
		assignees = append(assignees, map[string]any{"id": a.ID, "login": a.Login})
	}
	created := e.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC().Truncate(time.Second)
	}
	state := e.State
	if state == "" {
		state = "open"
	}
	out := map[string]any{"id": e.ID, "number": e.Number, "title": e.Title, "body": e.Body, "state": state,
		"html_url":   fmt.Sprintf("https://github.example.test/%s/issues/%d", repo, e.Number),
		"user":       map[string]any{"id": e.UserID, "login": e.Login},
		"assignees":  assignees,
		"created_at": created.Format(time.RFC3339), "updated_at": time.Now().UTC().Format(time.RFC3339)}
	if e.StateReason != "" {
		out["state_reason"] = e.StateReason
	}
	return out
}

func repoPayload(repo string, private bool) map[string]any {
	return map[string]any{"full_name": repo, "private": private, "owner": map[string]any{"login": owner(repo)}}
}

// IssuesPayload 构造 issues 事件的负载。
func IssuesPayload(action, repo string, private bool, issue IssueEvent) []byte {
	data, _ := json.Marshal(map[string]any{"action": action, "repository": repoPayload(repo, private), "issue": issue.json(repo)})
	return data
}

// IssueCommentPayload 构造 issue_comment 事件的负载。
func IssueCommentPayload(action, repo string, private bool, issue IssueEvent, commentID int64, body string, author EventUser) []byte {
	data, _ := json.Marshal(map[string]any{"action": action, "repository": repoPayload(repo, private), "issue": issue.json(repo),
		"comment": map[string]any{"id": commentID, "body": body, "created_at": time.Now().UTC().Format(time.RFC3339),
			"user": map[string]any{"id": author.ID, "login": author.Login}}})
	return data
}

// EventOf 把模拟服务里已有的 Issue 转成事件描述（用来把 OpenFox 创建的 Issue 再投回 Webhook）。
func (s *Server) EventOf(repo string, number int) (IssueEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.issues[strings.ToLower(repo)] {
		if item.Number != number {
			continue
		}
		event := IssueEvent{Number: item.Number, ID: item.ID, Title: item.Title, Body: item.Body, State: item.State, StateReason: item.StateReason,
			Login: item.Login, UserID: item.UserID, CreatedAt: item.CreatedAt}
		for i, login := range item.Assignees {
			event.Assignees = append(event.Assignees, EventUser{ID: assigneeID(login, i), Login: login})
		}
		return event, true
	}
	return IssueEvent{}, false
}

// AssigneeID 返回模拟服务给某个 login 生成的 ID（Webhook 与 REST 保持一致）。
func AssigneeID(login string) int64 { return assigneeID(login, 0) }
