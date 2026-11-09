// Package fakedingtalk 是本机模拟的钉钉开放平台（accessToken、个人 OAuth、unionId→userid、机器人单聊 / 群聊发送、
// Stream 模式端点与 WebSocket），供集成测试与浏览器联调使用，绝不访问真实的 api.dingtalk.com / oapi.dingtalk.com / login.dingtalk.com。
// 一个服务同时扮演 api、oapi、login 三个域名（路径互不冲突）。协议依据钉钉官方文档，见 internal/integrations/dingtalk 包注释。
package fakedingtalk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// defaultCorp 是没有显式指定企业时应用与员工所属的企业。
const defaultCorp = "corp_default"

type fakeApp struct {
	secret string
	corp   string
}

// User 是模拟的钉钉用户（企业内员工）。
type User struct {
	OpenID, UnionID, StaffID, Name string
}

type grant struct {
	appKey string
	user   User
}

// Message 是机器人发出的一条消息。
type Message struct {
	AppKey    string
	RobotCode string
	Kind      string // user / group
	Target    string // 单聊：员工 userid；群聊：openConversationId
	MsgKey    string
	Title     string
	Text      string
}

// Request 是收到的一条 HTTP 请求（用于断言没有泄露凭据）。
type Request struct {
	Method, Path, Body string
}

type wsConn struct {
	appKey  string
	conn    *websocket.Conn
	wmu     sync.Mutex
	closed  chan struct{}
	pending sync.Map // messageId → chan ackFrame
}

type ackFrame struct {
	Code    int               `json:"code"`
	Headers map[string]string `json:"headers"`
	Message string            `json:"message"`
	Data    string            `json:"data"`
}

// Server 是模拟服务。
type Server struct {
	URL string

	httpServer *httptest.Server
	mu         sync.Mutex
	apps       map[string]*fakeApp
	users      map[string]map[string]User // 企业 → staffID → 员工（员工 userid 只在企业内唯一）
	tokens     map[string]string          // accessToken → appKey
	codes      map[string]grant           // 授权码 → 授权
	userTokens map[string]grant
	tickets    map[string]string // ticket → appKey
	messages   []Message
	requests   []Request
	conns      map[string][]*wsConn
	connects   map[string]int
	authorize  string // 授权页自动同意的用户（staffID）
	deny       bool
	serverErr  bool
	failSends  []int // 依次对发送接口返回的 HTTP 状态
	invalid    map[string]bool
	flow       map[string]bool
	seq        int
	tokenCalls int
	openCalls  int
	endpointOK bool
}

// New 起一个 httptest 服务。
func New() *Server {
	s := NewHandler()
	s.httpServer = httptest.NewServer(s.Handler())
	s.URL = s.httpServer.URL
	return s
}

// NewHandler 只构造状态，不监听（独立进程用）；URL 需要调用方设置。
func NewHandler() *Server {
	return &Server{apps: map[string]*fakeApp{}, users: map[string]map[string]User{}, tokens: map[string]string{},
		codes: map[string]grant{}, userTokens: map[string]grant{}, tickets: map[string]string{}, conns: map[string][]*wsConn{},
		connects: map[string]int{}, invalid: map[string]bool{}, flow: map[string]bool{}, endpointOK: true}
}

// Close 关闭服务与全部长连接。
func (s *Server) Close() {
	if s.httpServer != nil {
		s.httpServer.CloseClientConnections()
		s.httpServer.Close()
	}
}

func (s *Server) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s%d_%d", prefix, time.Now().UnixNano(), s.seq)
}

// ---------- 配置 ----------

// AddApp 登记应用（AppKey / AppSecret）。
func (s *Server) AddApp(appKey, secret string) {
	s.mu.Lock()
	s.apps[appKey] = &fakeApp{secret: secret, corp: defaultCorp}
	s.mu.Unlock()
}

// SetAppCorp 设置应用所属的企业（默认 corp_default）。机器人应用与组织绑定的应用不同企业时用它。
func (s *Server) SetAppCorp(appKey, corp string) {
	s.mu.Lock()
	if app := s.apps[appKey]; app != nil {
		app.corp = corp
	}
	s.mu.Unlock()
}

// SetAppSecret 改掉应用的密钥（模拟管理员重置了 AppSecret）。
func (s *Server) SetAppSecret(appKey, secret string) {
	s.mu.Lock()
	if app := s.apps[appKey]; app != nil {
		app.secret = secret
	}
	s.mu.Unlock()
}

// AddUser 登记默认企业内的员工。
func (s *Server) AddUser(user User) { s.AddCorpUser(defaultCorp, user) }

// AddCorpUser 登记指定企业内的员工：不同企业可以有相同的 staffID，但 unionID 不同。
func (s *Server) AddCorpUser(corp string, user User) {
	s.mu.Lock()
	if s.users[corp] == nil {
		s.users[corp] = map[string]User{}
	}
	s.users[corp][user.StaffID] = user
	s.mu.Unlock()
}

// corpOf 返回应用所属企业；调用方持有 s.mu。
func (s *Server) corpOf(appKey string) string {
	if app := s.apps[appKey]; app != nil && app.corp != "" {
		return app.corp
	}
	return defaultCorp
}

// SetAuthorizeUser 设置授权页自动同意的员工（staffID）。
func (s *Server) SetAuthorizeUser(staffID string) { s.mu.Lock(); s.authorize = staffID; s.mu.Unlock() }

// SetDenyAuthorize 让授权页返回 access_denied。
func (s *Server) SetDenyAuthorize(deny bool) { s.mu.Lock(); s.deny = deny; s.mu.Unlock() }

// SetServerError 让所有 REST 接口返回 500。
func (s *Server) SetServerError(on bool) { s.mu.Lock(); s.serverErr = on; s.mu.Unlock() }

// FailNextSends 让接下来 n 次发送接口返回给定的 HTTP 状态（401 令牌失效、429 限流、500 服务错误、400 参数错误）。
func (s *Server) FailNextSends(n, status int) {
	s.mu.Lock()
	for i := 0; i < n; i++ {
		s.failSends = append(s.failSends, status)
	}
	s.mu.Unlock()
}

// SetInvalidStaff 让发给某员工的单聊消息被判为 invalidStaffId。
func (s *Server) SetInvalidStaff(staffID string, on bool) {
	s.mu.Lock()
	s.invalid[staffID] = on
	s.mu.Unlock()
}

// SetFlowControlled 让发给某员工的单聊消息被判为流控。
func (s *Server) SetFlowControlled(staffID string, on bool) {
	s.mu.Lock()
	s.flow[staffID] = on
	s.mu.Unlock()
}

// SetEndpointOK 控制连接凭证注册接口是否可用（false 返回 500，模拟网关故障）。
func (s *Server) SetEndpointOK(ok bool) { s.mu.Lock(); s.endpointOK = ok; s.mu.Unlock() }

// ExpireTokens 让已发出的 accessToken 全部失效。
func (s *Server) ExpireTokens() { s.mu.Lock(); s.tokens = map[string]string{}; s.mu.Unlock() }

// ---------- 观察 ----------

// Messages 返回应用（AppKey）发出的消息。
func (s *Server) Messages(appKey string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.messages {
		if m.AppKey == appKey {
			out = append(out, m)
		}
	}
	return out
}

// Requests 返回收到的全部 HTTP 请求。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// TokenCalls 返回 accessToken 接口被调用的次数。
func (s *Server) TokenCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.tokenCalls }

// ConnCount 返回应用当前在线的长连接数。
func (s *Server) ConnCount(appKey string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns[appKey])
}

// TotalConnects 返回应用累计建立过的长连接数。
func (s *Server) TotalConnects(appKey string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects[appKey]
}

// DropConnections 立刻断开应用的所有长连接（模拟网络中断）。
func (s *Server) DropConnections(appKey string) {
	s.mu.Lock()
	conns := append([]*wsConn(nil), s.conns[appKey]...)
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.conn.CloseNow()
	}
}

// ---------- 推送 ----------

func (s *Server) pickConn(appKey string) *wsConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if list := s.conns[appKey]; len(list) > 0 {
		return list[len(list)-1]
	}
	return nil
}

func (c *wsConn) send(ctx context.Context, payload any) error {
	data, _ := json.Marshal(payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// PushCallback 向应用的长连接推送一条 CALLBACK，等待客户端 ACK 并返回 ACK 的 data。
func (s *Server) PushCallback(appKey, topic string, payload any) (string, error) {
	return s.push(appKey, "CALLBACK", topic, "", payload)
}

// PushEvent 向应用的长连接推送一条 EVENT。
func (s *Server) PushEvent(appKey, eventType, eventID string, payload any) (string, error) {
	return s.push(appKey, "EVENT", "*", eventType+"|"+eventID, payload)
}

func (s *Server) push(appKey, kind, topic, event string, payload any) (string, error) {
	c := s.pickConn(appKey)
	if c == nil {
		return "", fmt.Errorf("fakedingtalk: no connection for %s", appKey)
	}
	s.mu.Lock()
	id := s.nextID("msg_")
	s.mu.Unlock()
	data, _ := json.Marshal(payload)
	headers := map[string]string{"appId": appKey, "connectionId": "conn", "contentType": "application/json", "messageId": id, "time": fmt.Sprint(time.Now().UnixMilli()), "topic": topic}
	if event != "" {
		parts := strings.SplitN(event, "|", 2)
		headers["eventType"], headers["eventId"] = parts[0], parts[1]
	}
	wait := make(chan ackFrame, 1)
	c.pending.Store(id, wait)
	defer c.pending.Delete(id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.send(ctx, map[string]any{"specVersion": "1.0", "type": kind, "headers": headers, "data": string(data)}); err != nil {
		return "", err
	}
	select {
	case ack := <-wait:
		return ack.Data, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-c.closed:
		return "", fmt.Errorf("fakedingtalk: connection closed")
	}
}

// PushSystem 推送 SYSTEM 帧（ping 会等待回包并返回其 data；disconnect 不等待）。
func (s *Server) PushSystem(appKey, topic string) (string, error) {
	c := s.pickConn(appKey)
	if c == nil {
		return "", fmt.Errorf("fakedingtalk: no connection for %s", appKey)
	}
	s.mu.Lock()
	id := s.nextID("sys_")
	s.mu.Unlock()
	data := `{"opaque":"opaque-` + id + `"}`
	if topic == "disconnect" {
		data = `{"reason":"connection is expired"}`
	}
	wait := make(chan ackFrame, 1)
	c.pending.Store(id, wait)
	defer c.pending.Delete(id)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.send(ctx, map[string]any{"type": "SYSTEM", "headers": map[string]string{"topic": topic, "messageId": id, "contentType": "application/json"}, "data": data}); err != nil {
		return "", err
	}
	if topic == "disconnect" {
		return "", nil
	}
	select {
	case ack := <-wait:
		if ack.Data != data {
			return ack.Data, fmt.Errorf("fakedingtalk: ping ack data %q, want %q", ack.Data, data)
		}
		return ack.Data, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ---------- HTTP ----------

// Handler 返回路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1.0/oauth2/accessToken", s.rest(s.handleToken))
	mux.HandleFunc("POST /v1.0/oauth2/userAccessToken", s.rest(s.handleUserToken))
	mux.HandleFunc("GET /v1.0/contact/users/me", s.rest(s.handleUserMe))
	mux.HandleFunc("GET /oauth2/auth", s.handleAuthorize)
	mux.HandleFunc("POST /topapi/user/getbyunionid", s.rest(s.handleUnion))
	mux.HandleFunc("POST /topapi/v2/user/get", s.rest(s.handleUserGet))
	mux.HandleFunc("POST /v1.0/robot/oToMessages/batchSend", s.rest(s.handleSendUser))
	mux.HandleFunc("POST /v1.0/robot/groupMessages/send", s.rest(s.handleSendGroup))
	mux.HandleFunc("POST /v1.0/gateway/connections/open", s.rest(s.handleOpen))
	mux.HandleFunc("GET /connect", s.handleWS)
	mux.HandleFunc("POST /_fake/push", s.handleAdminPush)
	mux.HandleFunc("GET /_fake/messages", s.handleAdminMessages)
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "fake dingtalk open platform") })
	return mux
}

func (s *Server) rest(next func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		failing := s.serverErr
		s.mu.Unlock()
		if failing {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		next(w, r, body)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func v1Err(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message, "requestid": "fake"})
}

func (s *Server) handleToken(w http.ResponseWriter, _ *http.Request, body []byte) {
	var in struct{ AppKey, AppSecret string }
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	s.tokenCalls++
	app := s.apps[in.AppKey]
	if app == nil || app.secret != in.AppSecret {
		s.mu.Unlock()
		v1Err(w, http.StatusBadRequest, "invalidClientIdOrSecret", "invalid appKey or appSecret")
		return
	}
	token := s.nextID("tk_")
	s.tokens[token] = in.AppKey
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "expireIn": 7200})
}

// appForToken 校验新版接口头里的 accessToken。
func (s *Server) appForToken(r *http.Request) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	appKey, ok := s.tokens[r.Header.Get("x-acs-dingtalk-access-token")]
	return appKey, ok
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.apps[q.Get("client_id")]
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if app == nil || err != nil || redirect.Host == "" || q.Get("response_type") != "code" || q.Get("scope") != "openid" || q.Get("prompt") != "consent" {
		http.Error(w, "invalid authorize request", http.StatusBadRequest)
		return
	}
	target := *redirect
	query := target.Query()
	if s.deny {
		query.Set("error", "access_denied")
	} else {
		user, ok := s.users[s.corpOf(q.Get("client_id"))][s.authorize]
		if !ok {
			http.Error(w, "no authorize user", http.StatusBadRequest)
			return
		}
		code := s.nextID("code_")
		s.codes[code] = grant{appKey: q.Get("client_id"), user: user}
		query.Set("code", code)
		query.Set("authCode", code)
	}
	query.Set("state", q.Get("state"))
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *Server) handleUserToken(w http.ResponseWriter, _ *http.Request, body []byte) {
	var in struct{ ClientID, ClientSecret, Code, GrantType string }
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.apps[in.ClientID]
	g, ok := s.codes[in.Code]
	if app == nil || app.secret != in.ClientSecret || !ok || g.appKey != in.ClientID || in.GrantType != "authorization_code" {
		v1Err(w, http.StatusBadRequest, "invalidAuthCode", "invalid code")
		return
	}
	delete(s.codes, in.Code)
	token := s.nextID("ut_")
	s.userTokens[token] = g
	writeJSON(w, http.StatusOK, map[string]any{"accessToken": token, "refreshToken": "rt", "expireIn": 7200, "corpId": "corp"})
}

func (s *Server) handleUserMe(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	g, ok := s.userTokens[r.Header.Get("x-acs-dingtalk-access-token")]
	s.mu.Unlock()
	if !ok {
		v1Err(w, http.StatusUnauthorized, "InvalidAuthentication", "invalid user token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nick": g.user.Name, "avatarUrl": "https://avatar.example/" + g.user.OpenID,
		"openId": g.user.OpenID, "unionId": g.user.UnionID, "mobile": "155****3240", "stateCode": "86"})
}

func (s *Server) handleUnion(w http.ResponseWriter, r *http.Request, body []byte) {
	s.mu.Lock()
	appKey, ok := s.tokens[r.URL.Query().Get("access_token")]
	var in struct{ Unionid string }
	_ = json.Unmarshal(body, &in)
	var user User
	found := false
	for _, candidate := range s.users[s.corpOf(appKey)] {
		if candidate.UnionID == in.Unionid {
			user, found = candidate, true
		}
	}
	s.mu.Unlock()
	switch {
	case !ok:
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 40014, "errmsg": "不合法的access_token"})
	case !found:
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 60121, "errmsg": "找不到该用户"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 0, "errmsg": "ok", "result": map[string]any{"contact_type": 0, "userid": user.StaffID}})
	}
}

// handleUserGet 是 topapi/v2/user/get：用应用的 accessToken 按员工 userid 查详情（只能查应用所属企业内的员工）。
func (s *Server) handleUserGet(w http.ResponseWriter, r *http.Request, body []byte) {
	s.mu.Lock()
	appKey, ok := s.tokens[r.URL.Query().Get("access_token")]
	var in struct{ Userid string }
	_ = json.Unmarshal(body, &in)
	corp := s.corpOf(appKey)
	user, found := s.users[corp][in.Userid]
	s.mu.Unlock()
	switch {
	case !ok:
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 40014, "errmsg": "不合法的access_token"})
	case !found:
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 60121, "errmsg": "找不到该用户"})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"errcode": 0, "errmsg": "ok", "request_id": "fake",
			"result": map[string]any{"userid": user.StaffID, "unionid": user.UnionID, "name": user.Name, "corp_id": corp}})
	}
}

// popFailure 取下一个注入的发送失败状态。
func (s *Server) popFailure() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failSends) == 0 {
		return 0
	}
	status := s.failSends[0]
	s.failSends = s.failSends[1:]
	return status
}

func (s *Server) failSend(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusUnauthorized:
		v1Err(w, status, "InvalidAuthentication", "token expired")
	case http.StatusTooManyRequests:
		w.Header().Set("Retry-After", "1")
		v1Err(w, status, "Throttling", "too many requests")
	default:
		v1Err(w, status, "fake.failure", "injected failure")
	}
}

func (s *Server) handleSendUser(w http.ResponseWriter, r *http.Request, body []byte) {
	appKey, ok := s.appForToken(r)
	if !ok {
		v1Err(w, http.StatusUnauthorized, "InvalidAuthentication", "invalid token")
		return
	}
	if status := s.popFailure(); status != 0 {
		s.failSend(w, status)
		return
	}
	var in struct {
		RobotCode string
		UserIDs   []string `json:"userIds"`
		MsgKey    string
		MsgParam  string
	}
	_ = json.Unmarshal(body, &in)
	var param struct{ Title, Text string }
	_ = json.Unmarshal([]byte(in.MsgParam), &param)
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := map[string]any{"processQueryKey": s.nextID("pq_"), "invalidStaffIdList": []string{}, "flowControlledStaffIdList": []string{}}
	for _, staff := range in.UserIDs {
		switch {
		case s.invalid[staff] || s.users[s.corpOf(appKey)][staff].StaffID == "":
			resp["invalidStaffIdList"] = []string{staff}
		case s.flow[staff]:
			resp["flowControlledStaffIdList"] = []string{staff}
		default:
			s.messages = append(s.messages, Message{AppKey: appKey, RobotCode: in.RobotCode, Kind: "user", Target: staff, MsgKey: in.MsgKey, Title: param.Title, Text: param.Text})
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSendGroup(w http.ResponseWriter, r *http.Request, body []byte) {
	appKey, ok := s.appForToken(r)
	if !ok {
		v1Err(w, http.StatusUnauthorized, "InvalidAuthentication", "invalid token")
		return
	}
	if status := s.popFailure(); status != 0 {
		s.failSend(w, status)
		return
	}
	var in struct {
		RobotCode          string
		OpenConversationID string `json:"openConversationId"`
		MsgKey             string
		MsgParam           string
	}
	_ = json.Unmarshal(body, &in)
	var param struct{ Title, Text string }
	_ = json.Unmarshal([]byte(in.MsgParam), &param)
	s.mu.Lock()
	s.messages = append(s.messages, Message{AppKey: appKey, RobotCode: in.RobotCode, Kind: "group", Target: in.OpenConversationID, MsgKey: in.MsgKey, Title: param.Title, Text: param.Text})
	id := s.nextID("pq_")
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"processQueryKey": id})
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		ClientID, ClientSecret string
		Subscriptions          []struct{ Type, Topic string }
	}
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openCalls++
	app := s.apps[in.ClientID]
	if app == nil || app.secret != in.ClientSecret {
		v1Err(w, http.StatusUnauthorized, "invalidClientIdOrSecret", "invalid clientId or clientSecret")
		return
	}
	if !s.endpointOK {
		v1Err(w, http.StatusInternalServerError, "fake.gateway", "gateway down")
		return
	}
	ticket := s.nextID("ticket_")
	s.tickets[ticket] = in.ClientID
	host := strings.TrimPrefix(s.URL, "http://")
	writeJSON(w, http.StatusOK, map[string]string{"endpoint": "ws://" + host + "/connect", "ticket": ticket})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	s.mu.Lock()
	appKey, ok := s.tickets[ticket]
	delete(s.tickets, ticket) // ticket 只能建一条连接
	s.mu.Unlock()
	if !ok {
		http.Error(w, "invalid ticket", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c := &wsConn{appKey: appKey, conn: conn, closed: make(chan struct{})}
	s.mu.Lock()
	s.conns[appKey] = append(s.conns[appKey], c)
	s.connects[appKey]++
	s.mu.Unlock()
	defer func() {
		close(c.closed)
		s.mu.Lock()
		list := s.conns[appKey]
		for i, item := range list {
			if item == c {
				s.conns[appKey] = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		_ = conn.CloseNow()
	}()
	for {
		_, payload, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var a ackFrame
		if json.Unmarshal(payload, &a) != nil {
			continue
		}
		if ch, ok := c.pending.Load(a.Headers["messageId"]); ok {
			select {
			case ch.(chan ackFrame) <- a:
			default:
			}
		}
	}
}

// handleAdminPush 让浏览器联调时模拟用户给机器人发消息：{"app_key","topic"?,"payload"}，topic 默认机器人消息回调。
func (s *Server) handleAdminPush(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AppKey  string          `json:"app_key"`
		Topic   string          `json:"topic"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if in.Topic == "" {
		in.Topic = "/v1.0/im/bot/messages/get"
	}
	data, err := s.push(in.AppKey, "CALLBACK", in.Topic, "", in.Payload)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ack": data})
}

func (s *Server) handleAdminMessages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"messages": s.Messages(r.URL.Query().Get("app_key"))})
}
