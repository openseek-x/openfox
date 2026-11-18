package slack

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func (c *Client) UserInfo(ctx context.Context, token, id string) (User, error) {
	if !ID(id) {
		return User{}, ErrInvalid
	}
	var out struct {
		User User `json:"user"`
	}
	e := c.api(ctx, "GET", "users.info", token, nil, url.Values{"user": {id}}, nil, false, &out)
	if e != nil {
		return User{}, e
	}
	if out.User.ID != id || !ID(out.User.TeamID) || !bounded(out.User.Name, 255) {
		return User{}, ErrIncomplete
	}
	return out.User, nil
}
func validConversation(v Conversation) bool {
	if !ID(v.ID) || !bounded(v.Name, 255) || len(v.SharedTeamIDs) > 100 || len(v.PendingShared) > 100 {
		return false
	}
	for _, id := range append(append([]string{}, v.SharedTeamIDs...), v.PendingShared...) {
		if !ID(id) {
			return false
		}
	}
	return (v.User == "" || ID(v.User)) && (v.ContextTeamID == "" || ID(v.ContextTeamID))
}
func (c *Client) ConversationInfo(ctx context.Context, token, id string) (Conversation, error) {
	if !ID(id) {
		return Conversation{}, ErrInvalid
	}
	var out struct {
		Channel Conversation `json:"channel"`
	}
	e := c.api(ctx, "GET", "conversations.info", token, nil, url.Values{"channel": {id}}, nil, false, &out)
	if e != nil {
		return Conversation{}, e
	}
	if !validConversation(out.Channel) || out.Channel.ID != id {
		return Conversation{}, ErrIncomplete
	}
	return out.Channel, nil
}
func (c *Client) ConversationMembers(ctx context.Context, token, channel, next string) (MembersPage, error) {
	if !ID(channel) || !cursor(next) {
		return MembersPage{}, ErrInvalid
	}
	var out struct {
		Members json.RawMessage `json:"members"`
		Meta    struct {
			Next string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	e := c.api(ctx, "GET", "conversations.members", token, nil, url.Values{"channel": {channel}, "cursor": {next}, "limit": {"200"}}, nil, false, &out)
	if e != nil {
		return MembersPage{}, e
	}
	page := MembersPage{Next: out.Meta.Next}
	if !requiredArray(out.Members) || json.Unmarshal(out.Members, &page.Members) != nil || len(page.Members) > 200 || !cursor(page.Next) || page.Next != "" && page.Next == next {
		return MembersPage{}, ErrIncomplete
	}
	seen := map[string]bool{}
	for _, id := range page.Members {
		if !ID(id) || seen[id] {
			return MembersPage{}, ErrIncomplete
		}
		seen[id] = true
	}
	return page, nil
}

// Third-party metadata may use nested/number payloads. Such a payload is not our
// string receipt schema; retain its event type but never coerce it into proof.
func (m *MessageMetadata) UnmarshalJSON(data []byte) error {
	var in struct {
		Type    string          `json:"event_type"`
		Payload json.RawMessage `json:"event_payload"`
	}
	if e := json.Unmarshal(data, &in); e != nil {
		return e
	}
	m.EventType = in.Type
	m.EventPayload = nil
	var p map[string]string
	if json.Unmarshal(in.Payload, &p) == nil {
		m.EventPayload = p
	}
	return nil
}
func (c *Client) messages(ctx context.Context, token string, in HistoryInput, thread bool) (MessagesPage, error) {
	if !ID(in.Channel) || !cursor(in.Cursor) || in.Limit < 1 || in.Limit > MaxHistoryPage || in.Oldest != "" && !timestamp(in.Oldest) || in.Latest != "" && !timestamp(in.Latest) || thread && !timestamp(in.ThreadTS) || !thread && in.ThreadTS != "" {
		return MessagesPage{}, ErrInvalid
	}
	form := url.Values{"channel": {in.Channel}, "cursor": {in.Cursor}, "limit": {strconv.Itoa(in.Limit)}, "include_all_metadata": {"true"}, "inclusive": {strconv.FormatBool(in.Inclusive)}}
	if in.Oldest != "" {
		form.Set("oldest", in.Oldest)
	}
	if in.Latest != "" {
		form.Set("latest", in.Latest)
	}
	method := "conversations.history"
	if thread {
		method = "conversations.replies"
		form.Set("ts", in.ThreadTS)
	}
	var out struct {
		Messages json.RawMessage `json:"messages"`
		More     *bool           `json:"has_more"`
		Limited  bool            `json:"is_limited"`
		Meta     struct {
			Next string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if e := c.api(ctx, "GET", method, token, nil, form, nil, false, &out); e != nil {
		return MessagesPage{}, e
	}
	page := MessagesPage{Next: out.Meta.Next, Limited: out.Limited || out.More == nil}
	if out.More != nil {
		page.HasMore = *out.More
	}
	if page.Next != "" {
		page.HasMore = true
	}
	if !requiredArray(out.Messages) || json.Unmarshal(out.Messages, &page.Messages) != nil || len(page.Messages) > in.Limit || !cursor(page.Next) || page.Next != "" && page.Next == in.Cursor {
		return MessagesPage{}, ErrIncomplete
	}
	seen := map[string]bool{}
	for _, m := range page.Messages {
		if !timestamp(m.TS) || seen[m.TS] || m.ThreadTS != "" && !timestamp(m.ThreadTS) || !bounded(m.Text, 40000) || !bounded(m.Type, 100) || !bounded(m.Subtype, 100) {
			return MessagesPage{}, ErrIncomplete
		}
		seen[m.TS] = true
		if thread && m.TS != in.ThreadTS && m.ThreadTS != in.ThreadTS {
			return MessagesPage{}, ErrIncomplete
		}
		for _, id := range []string{m.User, m.BotID, m.AppID, m.Team} {
			if id != "" && !ID(id) {
				return MessagesPage{}, ErrIncomplete
			}
		}
	}
	return page, nil
}
func (c *Client) History(ctx context.Context, token string, in HistoryInput) (MessagesPage, error) {
	return c.messages(ctx, token, in, false)
}
func (c *Client) Replies(ctx context.Context, token string, in HistoryInput) (MessagesPage, error) {
	return c.messages(ctx, token, in, true)
}
func (c *Client) OpenDM(ctx context.Context, token, user string) (Conversation, error) {
	if !ID(user) || strings.Contains(user, ",") {
		return Conversation{}, ErrInvalid
	}
	var out struct {
		Channel Conversation `json:"channel"`
	}
	e := c.api(ctx, "POST", "conversations.open", token, nil, nil, map[string]any{"users": user, "return_im": true}, true, &out)
	if e != nil {
		return Conversation{}, e
	}
	if !validConversation(out.Channel) || out.Channel.IsIM == nil || !*out.Channel.IsIM || out.Channel.User != user {
		return Conversation{}, incomplete(true)
	}
	return out.Channel, nil
}

// EscapeText encodes Slack control characters without trimming or rewriting
// ordinary text. Callers use its rune length to split durable message parts.
func EscapeText(v string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(v)
}
func plainText(v string, max int) bool {
	return bounded(v, max) && strings.IndexFunc(v, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}
func (c *Client) PostMessage(ctx context.Context, token string, in PostMessageInput) (MessageReceipt, error) {
	wire := EscapeText(in.Text)
	if !ID(in.Channel) || in.ThreadTS != "" && !timestamp(in.ThreadTS) || in.Text == "" || !plainText(in.Text, MaxMessageRunes) || utf8.RuneCountInString(wire) > MaxMessageRunes {
		return MessageReceipt{}, ErrInvalid
	}
	payload := map[string]any{"channel": in.Channel, "text": wire, "mrkdwn": false, "parse": "none", "link_names": false, "reply_broadcast": false, "unfurl_links": false, "unfurl_media": false}
	if in.ThreadTS != "" {
		payload["thread_ts"] = in.ThreadTS
	}
	if m := in.Metadata; m != nil {
		if !ID(m.EventType) || len(m.EventType) > 100 || len(m.EventPayload) > 4 || len(m.EventPayload) == 0 {
			return MessageReceipt{}, ErrInvalid
		}
		for k, v := range m.EventPayload {
			if !ID(k) || len(k) > 64 || !ID(v) || len(v) > 128 {
				return MessageReceipt{}, ErrInvalid
			}
		}
		payload["metadata"] = m
	}
	var out MessageReceipt
	if e := c.api(ctx, "POST", "chat.postMessage", token, nil, nil, payload, true, &out); e != nil {
		return MessageReceipt{}, e
	}
	if out.Channel != in.Channel || !timestamp(out.TS) {
		return MessageReceipt{}, incomplete(true)
	}
	return out, nil
}
func (c *Client) Unfurl(ctx context.Context, token, channel, ts string, previews map[string]UnfurlPreview) error {
	if !ID(channel) || !timestamp(ts) || len(previews) == 0 || len(previews) > 5 {
		return ErrInvalid
	}
	unfurls := map[string]any{}
	for target, p := range previews {
		u, e := url.Parse(target)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(target) > 2048 || !plainText(p.Title, 300) || !plainText(p.Text, 1000) {
			return ErrInvalid
		}
		unfurls[target] = map[string]any{"title": EscapeText(p.Title), "text": EscapeText(p.Text), "mrkdwn_in": []string{}}
	}
	var out struct{}
	return c.api(ctx, "POST", "chat.unfurl", token, nil, nil, map[string]any{"channel": channel, "ts": ts, "unfurls": unfurls}, true, &out)
}
