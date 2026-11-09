// Package fakefeishu 是本机模拟的飞书开放平台（tenant_access_token、机器人信息、发消息、OAuth、长连接端点与 WebSocket），
// 供集成测试与浏览器联调使用，绝不访问真实的 open.feishu.cn / accounts.feishu.cn。
package fakefeishu

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"openfox/internal/integrations/feishu"
)

type fakeApp struct {
	secret       string
	botName      string
	botAvailable bool
}

type codeGrant struct {
	appID, openID, challenge, redirect string
}

// Message 是收到的一条消息。
type Message struct {
	AppID     string
	ReceiveID string
	MsgType   string
	Content   string
	UUID      string
	MessageID string
	// receive_id_type（open_id / chat_id）、被回复的消息（reply 接口）。
	ReceiveIDType string
	ReplyTo       string
}

// Request 是收到的一条 HTTP 请求（用于断言没有泄露凭据、请求体正确）。
type Request struct {
	Method string
	Path   string
	Body   string
}

type wsConn struct {
	appID   string
	conn    *websocket.Conn
	wmu     sync.Mutex
	service int32
	closed  chan struct{}
	pending sync.Map // message_id → chan feishu.Frame
}

// Server 是模拟服务。
type Server struct {
	URL string

	httpServer *httptest.Server
	mu         sync.Mutex
	apps       map[string]*fakeApp
	users      map[string]map[string]string // appID → openID → 姓名
	tokens     map[string]string            // tenant_access_token → appID
	userTokens map[string]codeGrant
	codes      map[string]codeGrant
	messages   []Message
	uuids      map[string]string
	requests   []Request
	conns      map[string][]*wsConn
	connects   map[string]int
	pings      map[string]int
	failSends  []int
	serverErr  bool
	endpoint   int
	hsStatus   int
	hsAuthErr  int
	pingSecs   int
	authorize  string
	deny       bool
	seq        int
	tokenCalls int

	// 扫码建应用、机器人收发消息。
	regs           map[string]*registration // device_code → 会话
	regInterval    int
	regBrand       string
	slowDowns      int
	appCount       int
	unions         map[string]string // open_id → union_id
	authorizeUnion string
	chats          map[string][]*chatMessage
	msgChat        map[string]string // message_id → chat_id
	goneMessages   map[string]bool
	historyDenied  bool
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
	return &Server{apps: map[string]*fakeApp{}, users: map[string]map[string]string{}, tokens: map[string]string{},
		userTokens: map[string]codeGrant{}, codes: map[string]codeGrant{}, uuids: map[string]string{},
		conns: map[string][]*wsConn{}, connects: map[string]int{}, pings: map[string]int{}, pingSecs: 120,
		regs: map[string]*registration{}, regInterval: 1, unions: map[string]string{}, chats: map[string][]*chatMessage{},
		msgChat: map[string]string{}, goneMessages: map[string]bool{}}
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

func (s *Server) AddApp(appID, secret, botName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[appID] = &fakeApp{secret: secret, botName: botName, botAvailable: true}
}

func (s *Server) SetAppSecret(appID, secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app := s.apps[appID]; app != nil {
		app.secret = secret
	}
}

func (s *Server) SetBotAvailable(appID string, available bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if app := s.apps[appID]; app != nil {
		app.botAvailable = available
	}
}

func (s *Server) AddUser(appID, openID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[appID] == nil {
		s.users[appID] = map[string]string{}
	}
	s.users[appID][openID] = name
}

// SetAuthorizeUser 设置授权页自动同意时使用的飞书用户。
func (s *Server) SetAuthorizeUser(openID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorize = openID
}

func (s *Server) SetDenyAuthorize(deny bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deny = deny
}

// FailNextSends 让接下来 n 次发消息失败：99991400 → HTTP 429 限流；500 → HTTP 500；其他按业务码返回（例如 230013）。
func (s *Server) FailNextSends(n, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		s.failSends = append(s.failSends, code)
	}
}

// SetServerError 让所有 REST 接口返回 HTTP 500。
func (s *Server) SetServerError(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serverErr = on
}

// SetEndpointCode 设置 /callback/ws/endpoint 返回的业务码（0 为成功）。
func (s *Server) SetEndpointCode(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endpoint = code
}

// SetHandshakeStatus 让 WebSocket 握手失败并返回 Handshake-Status / Handshake-Autherrcode；status 为 0 恢复正常。
func (s *Server) SetHandshakeStatus(status, authErr int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hsStatus, s.hsAuthErr = status, authErr
}

// SetPingInterval 设置下发的 PingInterval（秒）；对新连接和后续 pong 生效。
func (s *Server) SetPingInterval(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingSecs = seconds
}

// ExpireTokens 让全部 tenant_access_token 失效（下一次调用返回 99991663）。
func (s *Server) ExpireTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = map[string]string{}
}

// ---------- 观察 ----------

func (s *Server) Messages(appID string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Message
	for _, m := range s.messages {
		if appID == "" || m.AppID == appID {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// TokenCalls 是 tenant_access_token 接口被调用的次数。
func (s *Server) TokenCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenCalls
}

// ConnCount 是某应用当前打开的长连接数。
func (s *Server) ConnCount(appID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns[appID])
}

// TotalConnects 是某应用累计成功握手的次数。
func (s *Server) TotalConnects(appID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connects[appID]
}

// PingCount 是某应用累计收到的 ping 数。
func (s *Server) PingCount(appID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pings[appID]
}

// DropConnections 主动断开某应用的全部长连接。
func (s *Server) DropConnections(appID string) {
	s.mu.Lock()
	list := append([]*wsConn(nil), s.conns[appID]...)
	s.mu.Unlock()
	for _, c := range list {
		c.conn.CloseNow()
	}
}

// ---------- 推送事件 ----------

// PushEvent 推送一个事件（自动生成 event_id），等待客户端 ACK，返回 ACK 里的 data。
func (s *Server) PushEvent(appID, eventType, eventJSON string) ([]byte, error) {
	s.mu.Lock()
	id := s.nextID("evt_")
	s.mu.Unlock()
	return s.PushEventWithID(appID, id, eventType, eventJSON)
}

// PushEventWithID 同 PushEvent，但指定 event_id（测试重复投递）。
func (s *Server) PushEventWithID(appID, eventID, eventType, eventJSON string) ([]byte, error) {
	return s.PushEventFor(appID, appID, eventID, eventType, eventJSON)
}

// PushEventFor 推送给 appID 的连接，但事件头里的 app_id 写成 headerAppID（测试 app_id 不符）。
func (s *Server) PushEventFor(appID, headerAppID, eventID, eventType, eventJSON string) ([]byte, error) {
	if eventJSON == "" {
		eventJSON = "{}"
	}
	payload := fmt.Sprintf(`{"schema":"2.0","header":{"event_id":%q,"event_type":%q,"app_id":%q,"tenant_key":"tk","create_time":"%d"},"event":%s}`,
		eventID, eventType, headerAppID, time.Now().UnixMilli(), eventJSON)
	return s.PushRaw(appID, []byte(payload), 1)
}

// PushRaw 把 payload 拆成 parts 个分片（sum/seq/message_id）推送，等待 ACK，返回 ACK 里的 data。
func (s *Server) PushRaw(appID string, payload []byte, parts int) ([]byte, error) {
	s.mu.Lock()
	list := s.conns[appID]
	var target *wsConn
	if len(list) > 0 {
		target = list[len(list)-1]
	}
	messageID := s.nextID("msg_")
	s.mu.Unlock()
	if target == nil {
		return nil, fmt.Errorf("no connection for %s", appID)
	}
	if parts < 1 {
		parts = 1
	}
	waiter := make(chan feishu.Frame, 1)
	target.pending.Store(messageID, waiter)
	defer target.pending.Delete(messageID)
	size := (len(payload) + parts - 1) / parts
	for i := 0; i < parts; i++ {
		lo, hi := i*size, (i+1)*size
		if lo > len(payload) {
			lo = len(payload)
		}
		if hi > len(payload) {
			hi = len(payload)
		}
		frame := feishu.Frame{SeqID: uint64(i), Service: target.service, Method: feishu.FrameTypeData,
			Headers: []feishu.Header{{Key: feishu.HeaderType, Value: feishu.TypeEvent}, {Key: feishu.HeaderMessageID, Value: messageID},
				{Key: feishu.HeaderSum, Value: strconv.Itoa(parts)}, {Key: feishu.HeaderSeq, Value: strconv.Itoa(i)},
				{Key: feishu.HeaderTraceID, Value: "trace"}},
			Payload: payload[lo:hi]}
		if err := target.send(&frame); err != nil {
			return nil, err
		}
	}
	select {
	case ack := <-waiter:
		var body struct {
			Code int    `json:"code"`
			Data []byte `json:"data"`
		}
		if err := json.Unmarshal(ack.Payload, &body); err != nil {
			return nil, err
		}
		if body.Code != 200 {
			return body.Data, fmt.Errorf("ack code %d", body.Code)
		}
		return body.Data, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("ack timeout")
	case <-target.closed:
		return nil, fmt.Errorf("connection closed")
	}
}

func (c *wsConn) send(frame *feishu.Frame) error {
	b, err := frame.Marshal()
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageBinary, b)
}

// ---------- HTTP ----------

// Handler 返回全部路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /open-apis/auth/v3/tenant_access_token/internal", s.rest(s.handleToken))
	mux.HandleFunc("GET /open-apis/bot/v3/info", s.rest(s.handleBot))
	mux.HandleFunc("POST /open-apis/im/v1/messages", s.rest(s.handleSend))
	mux.HandleFunc("GET /open-apis/im/v1/messages", s.rest(s.handleListMessages))
	mux.HandleFunc("POST /open-apis/im/v1/messages/{messageID}/reply", s.rest(s.handleReply))
	mux.HandleFunc("POST /oauth/v1/app/registration", s.handleRegistration)
	mux.HandleFunc("GET /page/launcher", s.handleLauncher)
	mux.HandleFunc("GET /_fake/registrations", s.handleAdminRegistrations)
	mux.HandleFunc("POST /_fake/push-message", s.handleAdminPushMessage)
	mux.HandleFunc("POST /callback/ws/endpoint", s.rest(s.handleEndpoint))
	mux.HandleFunc("GET /ws", s.handleWS)
	mux.HandleFunc("GET /open-apis/authen/v1/authorize", s.handleAuthorize)
	mux.HandleFunc("POST /open-apis/authen/v2/oauth/token", s.rest(s.handleOAuthToken))
	mux.HandleFunc("GET /open-apis/authen/v1/user_info", s.rest(s.handleUserInfo))
	mux.HandleFunc("POST /_fake/push", s.handleAdminPush)
	mux.HandleFunc("GET /_fake/messages", s.handleAdminMessages)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// rest 记录请求并按 SetServerError 注入 500。
func (s *Server) rest(next func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		fail := s.serverErr
		s.mu.Unlock()
		if fail {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": 500, "msg": "injected"})
			return
		}
		next(w, r, body)
	}
}

func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		AppID     string `json:"app_id"`
		AppSecret string `json:"app_secret"`
	}
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenCalls++
	app := s.apps[in.AppID]
	if app == nil || app.secret != in.AppSecret {
		writeJSON(w, http.StatusOK, map[string]any{"code": 10003, "msg": "invalid param"})
		return
	}
	token := s.nextID("t-")
	s.tokens[token] = in.AppID
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "tenant_access_token": token, "expire": 7200})
}

func (s *Server) appForToken(r *http.Request) (string, *fakeApp) {
	appID, ok := s.tokens[bearer(r)]
	if !ok {
		return "", nil
	}
	return appID, s.apps[appID]
}

func (s *Server) handleBot(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	appID, app := s.appForToken(r)
	if app == nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 99991663, "msg": "invalid access token"})
		return
	}
	if !app.botAvailable {
		writeJSON(w, http.StatusOK, map[string]any{"code": 10001, "msg": "bot not enabled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "bot": map[string]any{
		"app_name": app.botName, "avatar_url": "https://example.invalid/bot.png", "open_id": "ou_bot_" + appID, "activate_status": 2}})
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		ReceiveID string `json:"receive_id"`
		MsgType   string `json:"msg_type"`
		Content   string `json:"content"`
		UUID      string `json:"uuid"`
	}
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	defer s.mu.Unlock()
	appID, app := s.appForToken(r)
	if app == nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 99991663, "msg": "invalid access token"})
		return
	}
	if len(s.failSends) > 0 {
		code := s.failSends[0]
		s.failSends = s.failSends[1:]
		switch code {
		case 99991400:
			w.Header().Set("x-ogw-ratelimit-reset", "2")
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"code": code, "msg": "request trigger frequency limit"})
		case 500:
			writeJSON(w, http.StatusInternalServerError, map[string]any{"code": code, "msg": "injected"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"code": code, "msg": "injected"})
		}
		return
	}
	if in.UUID != "" {
		if id, ok := s.uuids[appID+"/"+in.UUID]; ok {
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": id}})
			return
		}
	}
	id := s.nextID("om_")
	if in.UUID != "" {
		s.uuids[appID+"/"+in.UUID] = id
	}
	s.messages = append(s.messages, Message{AppID: appID, ReceiveID: in.ReceiveID, MsgType: in.MsgType, Content: in.Content, UUID: in.UUID, MessageID: id,
		ReceiveIDType: r.URL.Query().Get("receive_id_type")})
	if r.URL.Query().Get("receive_id_type") == "chat_id" {
		s.recordBotMessageLocked(appID, in.ReceiveID, id, in.MsgType, in.Content)
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": id}})
}

func (s *Server) handleEndpoint(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		AppID     string `json:"AppID"`
		AppSecret string `json:"AppSecret"`
	}
	_ = json.Unmarshal(body, &in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.endpoint != 0 {
		writeJSON(w, http.StatusOK, map[string]any{"code": s.endpoint, "msg": "injected"})
		return
	}
	app := s.apps[in.AppID]
	if app == nil || app.secret != in.AppSecret {
		writeJSON(w, http.StatusOK, map[string]any{"code": 1000040345, "msg": "invalid app credentials"})
		return
	}
	wsURL := "ws://" + r.Host + "/ws?device_id=dev1&service_id=7&ticket=" + url.QueryEscape(in.AppID)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "ok", "data": map[string]any{
		"URL": wsURL, "ClientConfig": map[string]any{"ReconnectCount": -1, "ReconnectInterval": 1, "ReconnectNonce": 0, "PingInterval": s.pingSecs}}})
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	appID := r.URL.Query().Get("ticket")
	s.mu.Lock()
	status, authErr, pingSecs := s.hsStatus, s.hsAuthErr, s.pingSecs
	s.mu.Unlock()
	if status != 0 {
		w.Header().Set(feishu.HeaderHandshakeStatus, strconv.Itoa(status))
		w.Header().Set(feishu.HeaderHandshakeMsg, "injected")
		w.Header().Set(feishu.HeaderHandshakeAuthErrCode, strconv.Itoa(authErr))
		http.Error(w, "handshake rejected", http.StatusBadRequest)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(2 << 20)
	service, _ := strconv.ParseInt(r.URL.Query().Get("service_id"), 10, 32)
	c := &wsConn{appID: appID, conn: conn, service: int32(service), closed: make(chan struct{})}
	s.mu.Lock()
	s.conns[appID] = append(s.conns[appID], c)
	s.connects[appID]++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		list := s.conns[appID]
		for i, item := range list {
			if item == c {
				s.conns[appID] = append(list[:i:i], list[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		close(c.closed)
		conn.CloseNow()
	}()
	_ = pingSecs
	for {
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var frame feishu.Frame
		if frame.Unmarshal(data) != nil {
			return
		}
		switch frame.Method {
		case feishu.FrameTypeControl:
			if frame.Header(feishu.HeaderType) != feishu.TypePing {
				continue
			}
			s.mu.Lock()
			s.pings[appID]++
			secs := s.pingSecs
			s.mu.Unlock()
			payload, _ := json.Marshal(map[string]any{"PingInterval": secs, "ReconnectCount": -1, "ReconnectInterval": 1, "ReconnectNonce": 0})
			pong := feishu.Frame{Service: frame.Service, Method: feishu.FrameTypeControl,
				Headers: []feishu.Header{{Key: feishu.HeaderType, Value: feishu.TypePong}}, Payload: payload}
			_ = c.send(&pong)
		case feishu.FrameTypeData:
			if ch, ok := c.pending.Load(frame.Header(feishu.HeaderMessageID)); ok {
				select {
				case ch.(chan feishu.Frame) <- frame:
				default:
				}
			}
		}
	}
}

// ---------- OAuth ----------

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	appID, redirect, state := q.Get("client_id"), q.Get("redirect_uri"), q.Get("state")
	s.mu.Lock()
	app := s.apps[appID]
	deny, openID := s.deny, s.authorize
	if s.authorizeUnion != "" {
		openID = s.openIDForLocked(appID, s.authorizeUnion)
	}
	s.mu.Unlock()
	target, err := url.Parse(redirect)
	if app == nil || err != nil || target.Host == "" || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "invalid authorize request", http.StatusBadRequest)
		return
	}
	query := target.Query()
	query.Set("state", state)
	if deny {
		query.Set("error", "access_denied")
	} else {
		s.mu.Lock()
		code := s.nextID("code_")
		s.codes[code] = codeGrant{appID: appID, openID: openID, challenge: q.Get("code_challenge"), redirect: redirect}
		s.mu.Unlock()
		query.Set("code", code)
	}
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusFound)
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		GrantType    string `json:"grant_type"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Code         string `json:"code"`
		RedirectURI  string `json:"redirect_uri"`
		CodeVerifier string `json:"code_verifier"`
	}
	_ = json.Unmarshal(body, &in)
	fail := func(desc string) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"code": 20001, "error": "invalid_grant", "error_description": desc})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	app := s.apps[in.ClientID]
	grant, ok := s.codes[in.Code]
	delete(s.codes, in.Code)
	sum := sha256.Sum256([]byte(in.CodeVerifier))
	switch {
	case in.GrantType != "authorization_code" || app == nil || app.secret != in.ClientSecret:
		fail("invalid client")
	case !ok || grant.appID != in.ClientID || grant.redirect != in.RedirectURI:
		fail("invalid code")
	case base64.RawURLEncoding.EncodeToString(sum[:]) != grant.challenge:
		fail("pkce verification failed")
	default:
		token := s.nextID("u-")
		s.userTokens[token] = grant
		writeJSON(w, http.StatusOK, map[string]any{"code": 0, "access_token": token, "token_type": "Bearer", "expires_in": 7200})
	}
}

func (s *Server) handleUserInfo(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	grant, ok := s.userTokens[bearer(r)]
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": 99991677, "msg": "invalid user token"})
		return
	}
	name := s.users[grant.appID][grant.openID]
	if name == "" {
		name = grant.openID
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{
		"name": name, "avatar_url": "https://example.invalid/" + grant.openID + ".png", "open_id": grant.openID,
		"union_id": s.unionForLocked(grant.openID), "tenant_key": "tk"}})
}

// ---------- 联调管理接口 ----------

func (s *Server) handleAdminPush(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AppID     string          `json:"app_id"`
		EventType string          `json:"event_type"`
		EventID   string          `json:"event_id"`
		Event     json.RawMessage `json:"event"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var data []byte
	var err error
	if in.EventID != "" {
		data, err = s.PushEventWithID(in.AppID, in.EventID, in.EventType, string(in.Event))
	} else {
		data, err = s.PushEvent(in.AppID, in.EventType, string(in.Event))
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "data": string(data)})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len(data) == 0 {
		data = []byte("{}")
	}
	_, _ = w.Write(data)
}

func (s *Server) handleAdminMessages(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"messages": s.Messages(r.URL.Query().Get("app_id"))})
}
