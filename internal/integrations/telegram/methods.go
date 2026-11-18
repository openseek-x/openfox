package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

func parseUser(raw []byte) (User, error) {
	var out User
	m, e := object(raw)
	if e != nil || !required(m, "id", "is_bot", "first_name") || json.Unmarshal(raw, &out) != nil || !positiveID(out.ID) || !bounded(out.FirstName, 1024) || out.FirstName == "" || !bounded(out.LastName, 1024) || (out.Username != "" && !username(out.Username)) {
		return User{}, ErrInvalid
	}
	return out, nil
}
func parseChat(raw []byte) (Chat, error) {
	var out Chat
	m, e := object(raw)
	if e != nil || !required(m, "id", "type") || json.Unmarshal(raw, &out) != nil || !validID(out.ID) || !bounded(out.Title, 1024) || !bounded(out.FirstName, 1024) || !bounded(out.LastName, 1024) || (out.Username != "" && !username(out.Username)) {
		return Chat{}, ErrInvalid
	}
	switch out.Type {
	case "private", "group", "supergroup", "channel":
	default:
		return Chat{}, ErrInvalid
	}
	if out.Type == "private" && out.ID < 0 || out.Type != "private" && out.ID > 0 {
		return Chat{}, ErrInvalid
	}
	return out, nil
}
func parseMember(raw []byte) (ChatMember, error) {
	var out ChatMember
	m, e := object(raw)
	if e != nil || !required(m, "status", "user") || json.Unmarshal(raw, &out) != nil {
		return out, ErrInvalid
	}
	out.User, e = parseUser(m["user"])
	if e != nil {
		return ChatMember{}, e
	}
	switch out.Status {
	case "creator", "administrator":
		if out.IsAnonymous == nil {
			return ChatMember{}, ErrInvalid
		}
	case "member", "left", "kicked":
	case "restricted":
		if out.IsMember == nil || out.CanSendMessages == nil {
			return ChatMember{}, ErrInvalid
		}
	default:
		return ChatMember{}, ErrInvalid
	}
	return out, nil
}
func (c *Client) GetMe(ctx context.Context, token string) (Bot, error) {
	raw, e := c.call(ctx, token, "getMe", struct{}{}, false)
	if e != nil {
		return Bot{}, e
	}
	u, e := parseUser(raw)
	if e != nil || !u.IsBot || !username(u.Username) {
		return Bot{}, incomplete(false)
	}
	var out Bot
	if json.Unmarshal(raw, &out) != nil {
		return Bot{}, incomplete(false)
	}
	out.User = u
	return out, nil
}
func (c *Client) GetWebhookInfo(ctx context.Context, token string) (WebhookInfo, error) {
	raw, e := c.call(ctx, token, "getWebhookInfo", struct{}{}, false)
	if e != nil {
		return WebhookInfo{}, e
	}
	m, e := object(raw)
	var w struct {
		URL                          string   `json:"url"`
		HasCustomCertificate         bool     `json:"has_custom_certificate"`
		PendingUpdateCount           int64    `json:"pending_update_count"`
		LastErrorDate                int64    `json:"last_error_date"`
		LastErrorMessage             string   `json:"last_error_message"`
		LastSynchronizationErrorDate int64    `json:"last_synchronization_error_date"`
		MaxConnections               int      `json:"max_connections"`
		AllowedUpdates               []string `json:"allowed_updates"`
	}
	if e != nil || !required(m, "url", "has_custom_certificate", "pending_update_count") || json.Unmarshal(raw, &w) != nil || !bounded(w.URL, 4096) || w.PendingUpdateCount < 0 || w.LastErrorDate < 0 || w.LastSynchronizationErrorDate < 0 || w.MaxConnections < 0 || w.MaxConnections > 100 || len(w.AllowedUpdates) > 64 {
		return WebhookInfo{}, incomplete(false)
	}
	// A malicious/reflected provider field must not return this request's token.
	decoded := w.URL
	for i := 0; i < 3; i++ {
		if strings.Contains(decoded, token) {
			return WebhookInfo{}, incomplete(false)
		}
		next, e := url.QueryUnescape(decoded)
		if e != nil || next == decoded {
			break
		}
		decoded = next
	}
	for _, v := range w.AllowedUpdates {
		if !username(v) {
			return WebhookInfo{}, incomplete(false)
		}
	}
	return WebhookInfo{URL: w.URL, HasCustomCertificate: w.HasCustomCertificate, PendingUpdateCount: w.PendingUpdateCount, LastErrorDate: w.LastErrorDate, LastErrorPresent: w.LastErrorMessage != "", LastSynchronizationErrorDate: w.LastSynchronizationErrorDate, MaxConnections: w.MaxConnections, AllowedUpdates: w.AllowedUpdates}, nil
}
func validWebhookSecret(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func webhookURL(s string) bool {
	u, e := url.Parse(s)
	if e != nil || len(s) > 4096 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s, "\r\n\x00") {
		return false
	}
	switch u.Port() {
	case "", "443", "80", "88", "8443":
		return true
	}
	return false
}
func (c *Client) SetWebhook(ctx context.Context, token string, input WebhookInput) error {
	if !webhookURL(input.URL) || !validWebhookSecret(input.Secret) || input.MaxConnections < 1 || input.MaxConnections > 100 {
		return ErrInvalid
	}
	payload := struct {
		URL                string   `json:"url"`
		Secret             string   `json:"secret_token"`
		MaxConnections     int      `json:"max_connections"`
		AllowedUpdates     []string `json:"allowed_updates"`
		DropPendingUpdates bool     `json:"drop_pending_updates"`
	}{input.URL, input.Secret, input.MaxConnections, []string{"message", "my_chat_member"}, false}
	raw, e := c.call(ctx, token, "setWebhook", payload, true)
	if e != nil {
		return e
	}
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return incomplete(true)
	}
	return nil
}
func (c *Client) DeleteWebhook(ctx context.Context, token string) error {
	raw, e := c.call(ctx, token, "deleteWebhook", struct {
		DropPendingUpdates bool `json:"drop_pending_updates"`
	}{false}, true)
	if e != nil {
		return e
	}
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("true")) {
		return incomplete(true)
	}
	return nil
}
func (c *Client) GetChat(ctx context.Context, token string, id ID) (Chat, error) {
	if !validID(id) {
		return Chat{}, ErrInvalid
	}
	raw, e := c.call(ctx, token, "getChat", struct {
		ID int64 `json:"chat_id"`
	}{int64(id)}, false)
	if e != nil {
		return Chat{}, e
	}
	out, e := parseChat(raw)
	if e != nil || out.ID != id {
		return Chat{}, incomplete(false)
	}
	return out, nil
}
func (c *Client) GetChatMember(ctx context.Context, token string, chat, user ID) (ChatMember, error) {
	if !validID(chat) || !positiveID(user) {
		return ChatMember{}, ErrInvalid
	}
	raw, e := c.call(ctx, token, "getChatMember", struct {
		Chat int64 `json:"chat_id"`
		User int64 `json:"user_id"`
	}{int64(chat), int64(user)}, false)
	if e != nil {
		return ChatMember{}, e
	}
	out, e := parseMember(raw)
	if e != nil || out.User.ID != user {
		return ChatMember{}, incomplete(false)
	}
	return out, nil
}
func (c *Client) SendMessage(ctx context.Context, token string, input SendMessageInput) (MessageReceipt, error) {
	if !validID(input.ChatID) || !positiveID(input.ReplyToMessageID) || !plainText(input.Text) || UTF16Len(input.Text) > MaxTextUTF16 {
		return MessageReceipt{}, ErrInvalid
	}
	payload := struct {
		ChatID      int64  `json:"chat_id"`
		Text        string `json:"text"`
		LinkPreview struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
		Reply struct {
			MessageID         int64 `json:"message_id"`
			AllowWithoutReply bool  `json:"allow_sending_without_reply"`
		} `json:"reply_parameters"`
	}{ChatID: int64(input.ChatID), Text: input.Text}
	payload.LinkPreview.Disabled = true
	payload.Reply.MessageID = int64(input.ReplyToMessageID)
	raw, e := c.call(ctx, token, "sendMessage", payload, true)
	if e != nil {
		return MessageReceipt{}, e
	}
	out, e := parseMessage(raw, 0)
	if e != nil || out.UnsupportedReason != "" || !positiveID(out.ID) || out.Chat.ID != input.ChatID || !ordinaryChat(out.Chat) || out.From == nil || !out.From.IsBot || out.Text != input.Text || out.ReplyToMessage == nil || out.ReplyToMessage.ID != input.ReplyToMessageID || out.ReplyToMessage.Chat.ID != input.ChatID {
		return MessageReceipt{}, incomplete(true)
	}
	return MessageReceipt{ChatID: out.Chat.ID, MessageID: out.ID, ReplyToMessageID: out.ReplyToMessage.ID}, nil
}
