// Package fakewechat 是本机模拟的腾讯 ClawBot（iLink）服务：get_bot_qrcode（POST / GET 都接受）、get_qrcode_status（各状态、验证码、重定向）、
// getupdates 长轮询与游标、-14 会话失效、sendmessage 记录与按 client_id 去重，以及管理接口 /_fake/*。
// 供集成测试与浏览器联调使用，绝不访问真实的 ilinkai.weixin.qq.com。协议依据见 internal/integrations/wechat 包注释。
package fakewechat

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QRStep 是扫码状态序列中的一步；每次 get_qrcode_status 调用推进一步，最后一步保持不变。
type QRStep struct {
	Status       string
	BotToken     string
	BotID        string
	BaseURL      string
	UserID       string
	RedirectHost string
	// VerifyCode 非空且 Status = need_verifycode：必须带这个验证码的请求才能推进；错误次数超过 3 次变为 verify_code_blocked。
	VerifyCode string
}

// Message 是用户发给 bot 的消息（getupdates 返回）。
type Message struct {
	MessageID    int64
	FromUserID   string
	MessageType  int
	Text         string
	NoText       bool // true 时 item_list 里放一个非文本项（type = 2）
	ContextToken string
	GroupID      string
}

// Sent 是 bot 发出的一条消息。
type Sent struct {
	Token        string
	BotID        string
	ToUserID     string
	Text         string
	ClientID     string
	ContextToken string
}

// Request 是收到的一条 HTTP 请求（用于断言没有泄露凭据、请求头齐全）。
type Request struct {
	Method, Path, Query, Body string
	AuthType, Authorization   string
	AppID, ClientVersion, UIN string
}

type qrState struct {
	steps   []QRStep
	index   int
	wrong   int
	blocked bool
}

type bot struct {
	id      string
	queue   []Message
	expired bool
	nextID  int64
}

// Server 是模拟服务。
type Server struct {
	// URL 是服务地址（NewTestServer 自动设置）。
	URL string
	// Hold 是 getupdates / get_qrcode_status 没有新内容时的服务端等待时长（默认 300ms；联调可调大）。
	Hold time.Duration
	// TimeoutMs 是 getupdates 返回的 longpolling_timeout_ms。
	TimeoutMs int

	mu       sync.Mutex
	cond     *sync.Cond
	plans    []*qrState
	defPlan  func(code string) []QRStep
	qrs      map[string]*qrState
	qrCount  int
	bots     map[string]*bot // token → bot
	sent     []Sent
	clients  map[string]bool
	requests []Request
	failSend int
	httpSrv  *httptest.Server
}

// NewServer 构造服务；defaultPlan 为空时使用 wait → scaned → confirmed 的默认序列。
func NewServer() *Server {
	s := &Server{Hold: 300 * time.Millisecond, TimeoutMs: 35000, qrs: map[string]*qrState{}, bots: map[string]*bot{}, clients: map[string]bool{}}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Start 启动 httptest 服务并设置 URL。
func (s *Server) Start() *httptest.Server {
	s.httpSrv = httptest.NewServer(s.Handler())
	s.URL = s.httpSrv.URL
	return s.httpSrv
}

// SetDefaultPlan 设置没有排队计划时每个新二维码使用的状态序列（联调用）。
func (s *Server) SetDefaultPlan(fn func(code string) []QRStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defPlan = fn
}

// QueueQR 为下一个被申请的二维码排队一个状态序列。
func (s *Server) QueueQR(steps ...QRStep) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans = append(s.plans, &qrState{steps: steps})
}

// AddBot 预先登记一个有效的 bot_token（测试直接构造绑定时使用）。
func (s *Server) AddBot(token, botID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bots[token] = &bot{id: botID}
}

// Push 给 bot 追加一条用户消息，返回分配的 message_id。
func (s *Server) Push(token string, m Message) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bots[token]
	if b == nil {
		return 0
	}
	if m.MessageID == 0 {
		b.nextID++
		m.MessageID = 1000 + b.nextID
	}
	if m.MessageType == 0 {
		m.MessageType = 1
	}
	if m.ContextToken == "" {
		m.ContextToken = "ctx-" + strconv.FormatInt(m.MessageID, 10)
	}
	b.queue = append(b.queue, m)
	s.cond.Broadcast()
	return m.MessageID
}

// Expire 让该 token 之后的 getupdates / sendmessage 都返回 -14。
func (s *Server) Expire(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := s.bots[token]; b != nil {
		b.expired = true
	}
	s.cond.Broadcast()
}

// FailNextSends 让接下来 n 次 sendmessage 返回 ret = 1。
func (s *Server) FailNextSends(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failSend = n
}

// Sent 返回已发出的消息副本。
func (s *Server) Sent() []Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sent(nil), s.sent...)
}

// Requests 返回收到的请求副本。
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Close 关闭 httptest 服务。
func (s *Server) Close() {
	s.mu.Lock()
	s.cond.Broadcast()
	s.mu.Unlock()
	if s.httpSrv != nil {
		s.httpSrv.Close()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) record(r *http.Request, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body,
		AuthType: r.Header.Get("AuthorizationType"), Authorization: r.Header.Get("Authorization"),
		AppID: r.Header.Get("iLink-App-Id"), ClientVersion: r.Header.Get("iLink-App-ClientVersion"), UIN: r.Header.Get("X-WECHAT-UIN")})
}

// Handler 返回完整的 HTTP 处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	wrap := func(fn func(w http.ResponseWriter, r *http.Request, body []byte)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			s.record(r, string(body))
			fn(w, r, body)
		}
	}
	// 官方仓库是 POST，社区文档写 GET：两种都接受。
	mux.HandleFunc("/ilink/bot/get_bot_qrcode", wrap(s.getBotQRCode))
	mux.HandleFunc("GET /ilink/bot/get_qrcode_status", wrap(s.getQRCodeStatus))
	mux.HandleFunc("POST /ilink/bot/getupdates", wrap(s.getUpdates))
	mux.HandleFunc("POST /ilink/bot/sendmessage", wrap(s.sendMessage))
	mux.HandleFunc("POST /_fake/push", wrap(s.adminPush))
	mux.HandleFunc("GET /_fake/messages", wrap(s.adminMessages))
	mux.HandleFunc("POST /_fake/expire", wrap(s.adminExpire))
	return mux
}

func (s *Server) getBotQRCode(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	s.qrCount++
	code := fmt.Sprintf("qr_%d", s.qrCount)
	var state *qrState
	switch {
	case len(s.plans) > 0:
		state, s.plans = s.plans[0], s.plans[1:]
	case s.defPlan != nil:
		state = &qrState{steps: s.defPlan(code)}
	default:
		state = &qrState{steps: []QRStep{{Status: "wait"}, {Status: "scaned"}, {Status: "confirmed",
			BotToken: "tok_" + code, BotID: "bot_" + code + "@im.bot", UserID: "wxid_" + code + "@im.wechat", BaseURL: s.URL}}}
	}
	s.qrs[code] = state
	s.mu.Unlock()
	writeJSON(w, map[string]any{"ret": 0, "qrcode": code, "qrcode_img_content": "https://liteapp.weixin.qq.com/q/fake?qrcode=" + code})
}

func (s *Server) getQRCodeStatus(w http.ResponseWriter, r *http.Request, _ []byte) {
	code, verify := r.URL.Query().Get("qrcode"), r.URL.Query().Get("verify_code")
	s.mu.Lock()
	state := s.qrs[code]
	if state == nil {
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ret": 0, "status": "expired"})
		return
	}
	if state.blocked {
		s.mu.Unlock()
		writeJSON(w, map[string]any{"ret": 0, "status": "verify_code_blocked"})
		return
	}
	step := state.steps[state.index]
	if step.Status == "need_verifycode" && step.VerifyCode != "" {
		switch {
		case verify == step.VerifyCode && state.index+1 < len(state.steps):
			state.index++
			step = state.steps[state.index]
		case verify != "":
			state.wrong++
			if state.wrong >= 3 {
				state.blocked = true
				s.mu.Unlock()
				writeJSON(w, map[string]any{"ret": 0, "status": "verify_code_blocked"})
				return
			}
		}
	} else if state.index+1 < len(state.steps) {
		state.index++ // 先返回当前步，下一次查询推进到下一步
	}
	if step.Status == "confirmed" && step.BotToken != "" {
		if s.bots[step.BotToken] == nil {
			s.bots[step.BotToken] = &bot{id: step.BotID}
		}
	}
	hold := s.Hold
	s.mu.Unlock()
	if step.Status == "wait" && hold > 0 {
		time.Sleep(hold)
	}
	out := map[string]any{"ret": 0, "status": step.Status}
	if step.Status == "confirmed" {
		out["bot_token"], out["ilink_bot_id"], out["baseurl"], out["ilink_user_id"] = step.BotToken, step.BotID, step.BaseURL, step.UserID
	}
	if step.RedirectHost != "" {
		out["redirect_host"] = step.RedirectHost
	}
	writeJSON(w, out)
}

func bearer(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		Buf string `json:"get_updates_buf"`
	}
	_ = json.Unmarshal(body, &in)
	cursor, _ := strconv.Atoi(in.Buf)
	token := bearer(r)
	deadline := time.Now().Add(s.Hold)
	timer := time.AfterFunc(s.Hold, func() { s.mu.Lock(); s.cond.Broadcast(); s.mu.Unlock() })
	defer timer.Stop()
	ctx := r.Context()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		b := s.bots[token]
		if b == nil || b.expired {
			writeJSON(w, map[string]any{"ret": -14, "errcode": -14, "errmsg": "session expired"})
			return
		}
		if cursor < len(b.queue) || !time.Now().Before(deadline) || ctx.Err() != nil {
			msgs := []map[string]any{}
			for _, m := range b.queue[min(cursor, len(b.queue)):] {
				item := map[string]any{"type": 1, "text_item": map[string]string{"text": m.Text}}
				if m.NoText {
					item = map[string]any{"type": 2}
				}
				msgs = append(msgs, map[string]any{"message_id": m.MessageID, "from_user_id": m.FromUserID, "to_user_id": b.id,
					"message_type": m.MessageType, "message_state": 2, "item_list": []any{item}, "context_token": m.ContextToken,
					"create_time_ms": time.Now().UnixMilli(), "group_id": m.GroupID})
			}
			writeJSON(w, map[string]any{"ret": 0, "msgs": msgs, "get_updates_buf": strconv.Itoa(len(b.queue)), "longpolling_timeout_ms": s.TimeoutMs})
			return
		}
		s.cond.Wait()
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		Msg struct {
			ToUserID     string `json:"to_user_id"`
			ClientID     string `json:"client_id"`
			ContextToken string `json:"context_token"`
			MessageType  int    `json:"message_type"`
			ItemList     []struct {
				Type     int `json:"type"`
				TextItem struct {
					Text string `json:"text"`
				} `json:"text_item"`
			} `json:"item_list"`
		} `json:"msg"`
	}
	_ = json.Unmarshal(body, &in)
	token := bearer(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bots[token]
	if b == nil || b.expired {
		writeJSON(w, map[string]any{"ret": -14, "errcode": -14})
		return
	}
	if s.failSend > 0 {
		s.failSend--
		writeJSON(w, map[string]any{"ret": 1, "errmsg": "send failed"})
		return
	}
	if in.Msg.ClientID != "" && s.clients[token+"|"+in.Msg.ClientID] {
		writeJSON(w, map[string]any{"ret": 0}) // 按 client_id 幂等
		return
	}
	s.clients[token+"|"+in.Msg.ClientID] = true
	text := ""
	if len(in.Msg.ItemList) > 0 {
		text = in.Msg.ItemList[0].TextItem.Text
	}
	s.sent = append(s.sent, Sent{Token: token, BotID: b.id, ToUserID: in.Msg.ToUserID, Text: text, ClientID: in.Msg.ClientID, ContextToken: in.Msg.ContextToken})
	writeJSON(w, map[string]any{"ret": 0})
}

// adminPush：POST /_fake/push {"token","from_user_id","text","group_id","message_type","no_text","context_token"}。
func (s *Server) adminPush(w http.ResponseWriter, _ *http.Request, body []byte) {
	var in struct {
		Token        string `json:"token"`
		FromUserID   string `json:"from_user_id"`
		Text         string `json:"text"`
		GroupID      string `json:"group_id"`
		MessageType  int    `json:"message_type"`
		NoText       bool   `json:"no_text"`
		ContextToken string `json:"context_token"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := s.Push(in.Token, Message{FromUserID: in.FromUserID, Text: in.Text, GroupID: in.GroupID, MessageType: in.MessageType, NoText: in.NoText, ContextToken: in.ContextToken})
	writeJSON(w, map[string]any{"message_id": id})
}

// adminMessages：GET /_fake/messages 返回 bot 发出的消息。
func (s *Server) adminMessages(w http.ResponseWriter, _ *http.Request, _ []byte) {
	sent := s.Sent()
	out := make([]map[string]string, 0, len(sent))
	for _, m := range sent {
		out = append(out, map[string]string{"bot_id": m.BotID, "to_user_id": m.ToUserID, "text": m.Text, "client_id": m.ClientID, "context_token": m.ContextToken})
	}
	writeJSON(w, map[string]any{"messages": out})
}

// adminExpire：POST /_fake/expire {"token"}。
func (s *Server) adminExpire(w http.ResponseWriter, _ *http.Request, body []byte) {
	var in struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(body, &in)
	s.Expire(in.Token)
	writeJSON(w, map[string]any{"ok": true})
}
