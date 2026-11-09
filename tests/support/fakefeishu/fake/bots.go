package fakefeishu

// 模拟扫码创建应用（设备码流程）、机器人收发群 / 单聊消息、群历史。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type registration struct {
	deviceCode string
	userCode   string
	state      string // pending / approved / denied / expired / consumed
	openID     string
}

type chatMessage struct {
	messageID  string
	msgType    string
	content    string
	senderID   string
	senderType string
	created    int64
	mentions   []Mention
}

// Mention 是消息里的一个 @。
type Mention struct {
	Key, OpenID, Name string
}

// ---------- 用户 / union_id ----------

// OpenIDFor 返回某应用下某 union_id 对应的 open_id（由 appID + union_id 派生：同一用户在不同应用下 open_id 不同、union_id 相同）。
func OpenIDFor(appID, unionID string) string {
	sum := sha256.Sum256([]byte(appID + "|" + unionID))
	return "ou_" + hex.EncodeToString(sum[:])[:16]
}

func (s *Server) openIDForLocked(appID, unionID string) string {
	openID := OpenIDFor(appID, unionID)
	s.unions[openID] = unionID
	return openID
}

func (s *Server) unionForLocked(openID string) string {
	if union, ok := s.unions[openID]; ok {
		return union
	}
	return "on_" + openID
}

// AddUnionUser 登记一个飞书用户：同一 union_id 在不同应用下有不同 open_id。返回该应用下的 open_id。
func (s *Server) AddUnionUser(appID, unionID, name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	openID := s.openIDForLocked(appID, unionID)
	if s.users[appID] == nil {
		s.users[appID] = map[string]string{}
	}
	s.users[appID][openID] = name
	return openID
}

// SetAuthorizeUnion 设置授权页（以及扫码页）自动同意时使用的 union_id；优先于 SetAuthorizeUser。
func (s *Server) SetAuthorizeUnion(unionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorizeUnion = unionID
}

// ---------- 扫码建应用 ----------

// SetRegistrationBrand controls the tenant brand returned by the QR flow.
func (s *Server) SetRegistrationBrand(brand string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regBrand = brand
}

// SetRegistrationInterval 设置 begin 返回的轮询间隔（秒，默认 1）。
func (s *Server) SetRegistrationInterval(seconds int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.regInterval = seconds
}

// SetSlowDown 让接下来 n 次 poll 返回 slow_down。
func (s *Server) SetSlowDown(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slowDowns = n
}

func (s *Server) findRegistrationLocked(userCode string) *registration {
	for _, reg := range s.regs {
		if reg.userCode == userCode {
			return reg
		}
	}
	return nil
}

// ApproveRegistration 在飞书里确认授权（openID 为空时用默认授权用户）。
func (s *Server) ApproveRegistration(userCode, openID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.findRegistrationLocked(userCode)
	if reg == nil || reg.state != "pending" {
		return false
	}
	reg.state, reg.openID = "approved", openID
	return true
}

func (s *Server) DenyRegistration(userCode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.findRegistrationLocked(userCode)
	if reg == nil || reg.state != "pending" {
		return false
	}
	reg.state = "denied"
	return true
}

func (s *Server) ExpireRegistration(userCode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.findRegistrationLocked(userCode)
	if reg == nil {
		return false
	}
	reg.state = "expired"
	return true
}

// PendingUserCodes 返回待确认的 user_code。
func (s *Server) PendingUserCodes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, reg := range s.regs {
		if reg.state == "pending" {
			out = append(out, reg.userCode)
		}
	}
	return out
}

// RegistrationCount 是累计发起的扫码会话数。
func (s *Server) RegistrationCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.regs)
}

func (s *Server) handleRegistration(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	form, _ := url.ParseQuery(string(body))
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Body: redactForm(form)})
	fail := s.serverErr
	s.mu.Unlock()
	if fail {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "server_error"})
		return
	}
	switch form.Get("action") {
	case "init":
		writeJSON(w, http.StatusOK, map[string]any{"nonce": "nonce", "supported_auth_methods": []string{"client_secret"}})
	case "begin":
		if form.Get("archetype") != "PersonalAgent" || form.Get("auth_method") != "client_secret" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
			return
		}
		s.mu.Lock()
		s.seq++
		reg := &registration{deviceCode: s.nextID("dc_"), userCode: fmt.Sprintf("FAKE-%04d", s.seq), state: "pending"}
		s.regs[reg.deviceCode] = reg
		interval := s.regInterval
		s.mu.Unlock()
		base := "http://" + r.Host
		writeJSON(w, http.StatusOK, map[string]any{"device_code": reg.deviceCode, "user_code": reg.userCode,
			"verification_uri": base + "/page/launcher", "verification_uri_complete": base + "/page/launcher?user_code=" + reg.userCode,
			"interval": interval, "expire_in": 600})
	case "poll":
		s.handleRegistrationPoll(w, form.Get("device_code"))
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
	}
}

// redactForm 记录请求时去掉 device_code，避免测试日志里出现敏感值。
func redactForm(form url.Values) string {
	copied := url.Values{}
	for k, v := range form {
		if k == "device_code" {
			v = []string{"<redacted>"}
		}
		copied[k] = v
	}
	return copied.Encode()
}

func (s *Server) handleRegistrationPoll(w http.ResponseWriter, deviceCode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg := s.regs[deviceCode]
	if reg == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant", "error_description": "unknown device code"})
		return
	}
	if s.slowDowns > 0 && (reg.state == "pending" || reg.state == "approved") {
		s.slowDowns--
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "slow_down"})
		return
	}
	switch reg.state {
	case "pending":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "authorization_pending"})
	case "denied":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "access_denied"})
	case "expired", "consumed":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "expired_token"})
	case "approved":
		s.appCount++
		appID := fmt.Sprintf("cli_fake%08d", s.appCount)
		secret := "secret_" + strings.Repeat("x", 8) + strconv.Itoa(s.appCount) + "_abcdefgh"
		s.apps[appID] = &fakeApp{secret: secret, botName: fmt.Sprintf("OpenFox 智能体 %d", s.appCount), botAvailable: true}
		openID := reg.openID
		if openID == "" {
			openID = s.authorize
			if s.authorizeUnion != "" {
				openID = s.openIDForLocked(appID, s.authorizeUnion)
			}
		}
		reg.state = "consumed"
		brand := s.regBrand
		if brand == "" {
			brand = "feishu"
		}
		writeJSON(w, http.StatusOK, map[string]any{"client_id": appID, "client_secret": secret,
			"user_info": map[string]any{"open_id": openID, "tenant_brand": brand}})
	}
}

// handleLauncher 是「打开链接」的落地页：用默认授权用户自动同意。
func (s *Server) handleLauncher(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("user_code")
	if !s.ApproveRegistration(code, "") {
		http.Error(w, "unknown or finished registration", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<html><body>已授权 " + url.QueryEscape(code) + "</body></html>"))
}

func (s *Server) handleAdminRegistrations(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []map[string]string{}
	for _, reg := range s.regs {
		items = append(items, map[string]string{"user_code": reg.userCode, "state": reg.state,
			"verification_url": "http://" + r.Host + "/page/launcher?user_code=" + reg.userCode})
	}
	writeJSON(w, http.StatusOK, map[string]any{"registrations": items})
}

// ---------- 群历史、回复 ----------

// SetHistoryForbidden 让读群历史返回缺少权限（im:message.group_msg）。
func (s *Server) SetHistoryForbidden(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.historyDenied = on
}

// SetMessageGone 让回复这条消息时返回「消息已撤回」（230011）。
func (s *Server) SetMessageGone(messageID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goneMessages[messageID] = true
}

// AddChatMessage 往群历史里放一条消息（senderUnion 为空且 senderType 为 app 表示机器人发的）。返回 message_id。
func (s *Server) AddChatMessage(chatID, senderUnion, senderType, text string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, _ := json.Marshal(map[string]string{"text": text})
	return s.addChatLocked(chatID, &chatMessage{msgType: "text", content: string(content), senderID: "ou_hist_" + senderUnion, senderType: senderType})
}

func (s *Server) addChatLocked(chatID string, m *chatMessage) string {
	if m.messageID == "" {
		m.messageID = s.nextID("om_h_")
	}
	m.created = time.Now().UnixMilli() + int64(len(s.chats[chatID]))
	s.chats[chatID] = append(s.chats[chatID], m)
	s.msgChat[m.messageID] = chatID
	return m.messageID
}

func (s *Server) recordBotMessageLocked(appID, chatID, messageID, msgType, content string) {
	s.addChatLocked(chatID, &chatMessage{messageID: messageID, msgType: msgType, content: content, senderID: "ou_bot_" + appID, senderType: "app"})
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, app := s.appForToken(r); app == nil {
		writeJSON(w, http.StatusOK, map[string]any{"code": 99991663, "msg": "invalid access token"})
		return
	}
	if s.historyDenied {
		writeJSON(w, http.StatusOK, map[string]any{"code": 99991672, "msg": "Access denied. One of the following scopes is required: [im:message.group_msg]"})
		return
	}
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("page_size"))
	if size <= 0 || size > 50 {
		size = 20
	}
	list := s.chats[q.Get("container_id")]
	items := []map[string]any{}
	for i := len(list) - 1; i >= 0 && len(items) < size; i-- {
		m := list[i]
		mentions := []map[string]any{}
		for _, mention := range m.mentions {
			mentions = append(mentions, map[string]any{"key": mention.Key, "id": mention.OpenID, "id_type": "open_id", "name": mention.Name})
		}
		items = append(items, map[string]any{"message_id": m.messageID, "msg_type": m.msgType, "create_time": strconv.FormatInt(m.created, 10),
			"sender": map[string]any{"id": m.senderID, "id_type": "open_id", "sender_type": m.senderType},
			"body":   map[string]any{"content": m.content}, "mentions": mentions})
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"items": items, "has_more": false}})
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		MsgType string `json:"msg_type"`
		Content string `json:"content"`
		UUID    string `json:"uuid"`
	}
	_ = json.Unmarshal(body, &in)
	target := r.PathValue("messageID")
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
	chatID, known := s.msgChat[target]
	if !known || s.goneMessages[target] {
		writeJSON(w, http.StatusOK, map[string]any{"code": 230011, "msg": "The message is recalled or not found"})
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
	s.messages = append(s.messages, Message{AppID: appID, ReceiveID: chatID, MsgType: in.MsgType, Content: in.Content, UUID: in.UUID, MessageID: id,
		ReceiveIDType: "chat_id", ReplyTo: target})
	s.recordBotMessageLocked(appID, chatID, id, in.MsgType, in.Content)
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "success", "data": map[string]any{"message_id": id}})
}

// ---------- 推送消息事件 ----------

// MessageSpec 描述一条要推给机器人的 im.message.receive_v1。
type MessageSpec struct {
	AppID       string
	ChatID      string
	ChatType    string // p2p / group
	SenderUnion string
	SenderType  string // 默认 user
	Text        string
	MsgType     string // 默认 text
	Content     string // 非空时直接作为 content（覆盖 Text）
	MentionBot  bool
	MessageID   string
	ParentID    string
	Mentions    []Mention // 额外的 @
	EventID     string
}

// PushMessage 推送一条文字消息：单聊不需要 @；群聊 mentionBot 为 true 时带 @机器人。返回 message_id。
func (s *Server) PushMessage(appID, chatID, chatType, senderUnion, text string, mentionBot bool) (string, error) {
	return s.PushMessageSpec(MessageSpec{AppID: appID, ChatID: chatID, ChatType: chatType, SenderUnion: senderUnion, Text: text, MentionBot: mentionBot})
}

// PushMessageSpec 推送消息事件并等待客户端 ACK。
func (s *Server) PushMessageSpec(spec MessageSpec) (string, error) {
	s.mu.Lock()
	if spec.MessageID == "" {
		spec.MessageID = s.nextID("om_in_")
	}
	if spec.MsgType == "" {
		spec.MsgType = "text"
	}
	if spec.SenderType == "" {
		spec.SenderType = "user"
	}
	senderOpen := s.openIDForLocked(spec.AppID, spec.SenderUnion)
	botName := ""
	if app := s.apps[spec.AppID]; app != nil {
		botName = app.botName
	}
	mentions := append([]Mention(nil), spec.Mentions...)
	text := spec.Text
	if spec.MentionBot {
		mentions = append([]Mention{{Key: "@_user_1", OpenID: "ou_bot_" + spec.AppID, Name: botName}}, mentions...)
		text = "@_user_1 " + text
	}
	content := spec.Content
	if content == "" {
		encoded, _ := json.Marshal(map[string]string{"text": text})
		content = string(encoded)
	}
	// 群历史里也能看到这条消息。
	history := &chatMessage{messageID: spec.MessageID, msgType: spec.MsgType, content: content, senderID: senderOpen, senderType: spec.SenderType, mentions: mentions}
	s.addChatLocked(spec.ChatID, history)
	eventID := spec.EventID
	if eventID == "" {
		eventID = s.nextID("evt_")
	}
	s.mu.Unlock()
	mentionJSON := []map[string]any{}
	for _, m := range mentions {
		mentionJSON = append(mentionJSON, map[string]any{"key": m.Key, "id": map[string]string{"open_id": m.OpenID, "union_id": "", "user_id": ""}, "name": m.Name, "tenant_key": "tk"})
	}
	event, _ := json.Marshal(map[string]any{
		"sender": map[string]any{"sender_id": map[string]string{"open_id": senderOpen, "union_id": spec.SenderUnion, "user_id": ""},
			"sender_type": spec.SenderType, "tenant_key": "tk"},
		"message": map[string]any{"message_id": spec.MessageID, "parent_id": spec.ParentID, "create_time": strconv.FormatInt(time.Now().UnixMilli(), 10),
			"chat_id": spec.ChatID, "chat_type": spec.ChatType, "message_type": spec.MsgType, "content": content, "mentions": mentionJSON}})
	_, err := s.PushEventWithID(spec.AppID, eventID, "im.message.receive_v1", string(event))
	return spec.MessageID, err
}

func (s *Server) handleAdminPushMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AppID       string `json:"app_id"`
		ChatID      string `json:"chat_id"`
		ChatType    string `json:"chat_type"`
		SenderUnion string `json:"sender_union_id"`
		Text        string `json:"text"`
		MentionBot  bool   `json:"mention_bot"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&in); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	id, err := s.PushMessage(in.AppID, in.ChatID, in.ChatType, in.SenderUnion, in.Text, in.MentionBot)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message_id": id})
}
