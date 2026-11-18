package discord

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

func required(raw []byte, keys ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok || string(v) == "null" {
			return false
		}
	}
	return true
}
func (c *Client) GetMe(ctx context.Context, token string) (User, error) {
	var v User
	b, e := c.do(ctx, token, "GET", "/users/@me", "GET /users/@me", "", nil)
	if e != nil {
		return v, e
	}
	if decodeObject(b, &v) != nil || !v.ID.Valid() || !v.Bot || v.Username == "" {
		return User{}, ErrResponse
	}
	return v, nil
}
func (c *Client) GetApplication(ctx context.Context, token string) (Application, error) {
	var v Application
	b, e := c.do(ctx, token, "GET", "/applications/@me", "GET /applications/@me", "", nil)
	if e != nil {
		return v, e
	}
	if decodeObject(b, &v) != nil || !v.ID.Valid() || v.Name == "" || v.BotPublic == nil || v.RequiresCodeGrant == nil || v.Bot != nil && !v.Bot.ID.Valid() {
		return Application{}, ErrResponse
	}
	return v, nil
}
func (c *Client) GetGatewayBot(ctx context.Context, token string) (GatewayBot, error) {
	var v GatewayBot
	b, e := c.do(ctx, token, "GET", "/gateway/bot", "GET /gateway/bot", "", nil)
	if e != nil {
		return v, e
	}
	if decodeObject(b, &v) != nil || !required(b, "shards", "session_start_limit") || !validGatewayURL(v.URL) || v.Shards < 1 || v.Starts.Total < 1 || v.Starts.Remaining < 0 || v.Starts.Remaining > v.Starts.Total || v.Starts.ResetAfter < 0 || v.Starts.MaxConcurrency < 1 {
		return GatewayBot{}, ErrResponse
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(b, &envelope)
	if !required(envelope["session_start_limit"], "total", "remaining", "reset_after", "max_concurrency") {
		return GatewayBot{}, ErrResponse
	}
	return v, nil
}

// ListGuilds reads one bounded page. More guilds never imply local authorization.
func (c *Client) ListGuilds(ctx context.Context, token string, after ID, limit int) (GuildPage, error) {
	if limit < 1 || limit > 199 || after != "" && !after.Valid() {
		return GuildPage{}, ErrInvalid
	}
	q := url.Values{"limit": {strconv.Itoa(limit + 1)}}
	if after != "" {
		q.Set("after", string(after))
	}
	b, e := c.do(ctx, token, "GET", query("/users/@me/guilds", q), "GET /users/@me/guilds", "", nil)
	if e != nil {
		return GuildPage{}, e
	}
	v, e := decodeArray[Guild](b, limit+1)
	if e != nil {
		return GuildPage{}, e
	}
	seen := map[ID]bool{}
	for _, g := range v {
		if !g.ID.Valid() || g.Name == "" || seen[g.ID] || after != "" && compareID(g.ID, after) <= 0 {
			return GuildPage{}, ErrResponse
		}
		seen[g.ID] = true
	}
	sort.Slice(v, func(i, j int) bool { return compareID(v[i].ID, v[j].ID) < 0 })
	out := GuildPage{Guilds: v}
	if len(v) > limit {
		out.Guilds = v[:limit]
		out.Next = out.Guilds[limit-1].ID
	}
	return out, nil
}
func (c *Client) ListChannels(ctx context.Context, token string, guild ID) ([]Channel, error) {
	if !guild.Valid() {
		return nil, ErrInvalid
	}
	b, e := c.do(ctx, token, "GET", "/guilds/"+string(guild)+"/channels", "GET /guilds/:guild/channels", string(guild), nil)
	if e != nil {
		return nil, e
	}
	rawRows, e := decodeArray[json.RawMessage](b, MaxChannelObjects)
	if e != nil {
		return nil, e
	}
	v := make([]Channel, len(rawRows))
	seen := map[ID]bool{}
	for i, raw := range rawRows {
		ch := &v[i]
		if decodeObject(raw, ch) != nil || !required(raw, "type") || !ch.ID.Valid() || ch.GuildID == nil || *ch.GuildID != guild || seen[ch.ID] {
			return nil, ErrResponse
		}
		seen[ch.ID] = true
	}
	return v, nil
}
func (c *Client) GetChannel(ctx context.Context, token string, id ID) (Channel, error) {
	var v Channel
	if !id.Valid() {
		return v, ErrInvalid
	}
	b, e := c.do(ctx, token, "GET", "/channels/"+string(id), "GET /channels/:channel", string(id), nil)
	if e != nil {
		return v, e
	}
	if decodeObject(b, &v) != nil || !required(b, "type") || v.ID != id {
		return Channel{}, ErrResponse
	}
	return v, nil
}
func (c *Client) GetMemberPermissions(ctx context.Context, token string, channel, user ID) (Permissions, error) {
	if !channel.Valid() || !user.Valid() {
		return "", ErrInvalid
	}
	b, e := c.do(ctx, token, "GET", "/channels/"+string(channel)+"/permissions/member/"+string(user)+"?implicit=true", "GET /channels/:channel/permissions/member/:user", string(channel), nil)
	if e != nil {
		return "", e
	}
	var v struct {
		Permissions Permissions `json:"permissions"`
	}
	if decodeObject(b, &v) != nil || !v.Permissions.Valid() {
		return "", ErrResponse
	}
	return v.Permissions, nil
}
func (c *Client) GetGuildMember(ctx context.Context, token string, guild, user ID) (Member, error) {
	var v Member
	if !guild.Valid() || !user.Valid() {
		return v, ErrInvalid
	}
	b, e := c.do(ctx, token, "GET", "/guilds/"+string(guild)+"/members/"+string(user), "GET /guilds/:guild/members/:user", string(guild), nil)
	if e != nil {
		return v, e
	}
	if decodeObject(b, &v) != nil || v.User.ID != user {
		return Member{}, ErrResponse
	}
	return v, nil
}
func (c *Client) GetMessage(ctx context.Context, token string, channel, id ID) (Message, error) {
	var v Message
	if !channel.Valid() || !id.Valid() {
		return v, ErrInvalid
	}
	b, e := c.do(ctx, token, "GET", "/channels/"+string(channel)+"/messages/"+string(id), "GET /channels/:channel/messages/:message", string(channel), nil)
	if e != nil {
		return v, e
	}
	v, e = DecodeMessage(b)
	if e != nil || v.ChannelID != channel || v.ID != id {
		return Message{}, ErrResponse
	}
	return v, nil
}

// GetHistory is one fixed before-message page, never the latest channel page.
// Empty results are facts only; the caller must separately verify permissions.
func (c *Client) GetHistory(ctx context.Context, token string, channel, before ID) ([]Message, error) {
	if !channel.Valid() || !before.Valid() {
		return nil, ErrInvalid
	}
	b, e := c.do(ctx, token, "GET", "/channels/"+string(channel)+"/messages?before="+string(before)+"&limit=20", "GET /channels/:channel/messages", string(channel), nil)
	if e != nil {
		return nil, e
	}
	rawRows, e := decodeArray[json.RawMessage](b, 20)
	if e != nil {
		return nil, e
	}
	v := make([]Message, len(rawRows))
	previous := before
	for i, raw := range rawRows {
		m, err := DecodeMessage(raw)
		if err != nil || m.ChannelID != channel || compareID(m.ID, previous) >= 0 {
			return nil, ErrResponse
		}
		v[i] = m
		previous = m.ID
	}
	return v, nil
}

// DecodeMessage validates a full REST or MESSAGE_CREATE object, not a partial
// MESSAGE_UPDATE. Missing type/content cannot be mistaken for an ordinary post.
func DecodeMessage(raw []byte) (Message, error) {
	var m Message
	if decodeObject(raw, &m) != nil || !required(raw, "type", "content", "timestamp") || !validMessage(m) || m.Timestamp.IsZero() {
		return Message{}, ErrResponse
	}
	return m, nil
}
func validMessage(m Message) bool {
	return m.ID.Valid() && m.ChannelID.Valid() && m.Author.ID.Valid() && utf8.ValidString(m.Content) && len([]rune(m.Content)) <= 8000
}

// OrdinaryMessage excludes other channel and message kinds. DM destinations are
// independent from user IDs and require an authenticated channel recipient check.
func OrdinaryMessage(m Message, channel Channel) bool {
	return validMessage(m) && channel.Supported() && channel.ID == m.ChannelID && (m.GuildID == nil || channel.GuildID != nil && *m.GuildID == *channel.GuildID) && !m.Author.Bot && m.WebhookID == nil && (m.Type == 0 || m.Type == 19) && m.Flags&(1<<6) == 0 && strings.TrimSpace(m.Content) != ""
}
func DMRecipient(channel Channel, user ID) bool {
	if !user.Valid() || !channel.ID.Valid() || channel.Type != 1 || channel.GuildID != nil || len(channel.Recipients) != 1 {
		return false
	}
	return channel.Recipients[0].ID == user && !channel.Recipients[0].Bot
}
func MentionText(m Message, bot ID) (string, bool) {
	if !bot.Valid() {
		return "", false
	}
	found := false
	for _, u := range m.Mentions {
		if u.ID == bot {
			found = true
			break
		}
	}
	a, b := "<@"+string(bot)+">", "<@!"+string(bot)+">"
	if !found || !strings.Contains(m.Content, a) && !strings.Contains(m.Content, b) {
		return m.Content, false
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(m.Content, a, ""), b, "")), true
}
func InstallationURL(app ID) (string, error) {
	if !app.Valid() {
		return "", ErrInvalid
	}
	q := url.Values{"client_id": {string(app)}, "scope": {"bot"}, "permissions": {"68608"}, "integration_type": {"0"}}
	return APIOrigin + "/oauth2/authorize?" + q.Encode(), nil
}
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r > 0xffff {
			n++
		}
	}
	return n
}
func (c *Client) CreateMessage(ctx context.Context, token string, in MessageIntent) (Message, error) {
	if !in.BotID.Valid() || !in.ChannelID.Valid() || !in.MessageID.Valid() || in.GuildID != nil && !in.GuildID.Valid() || !utf8.ValidString(in.Content) || strings.TrimSpace(in.Content) == "" || UTF16Len(in.Content) > 2000 || len(in.Nonce) < 1 || len(in.Nonce) > 25 {
		return Message{}, ErrInvalid
	}
	for _, r := range in.Nonce {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return Message{}, ErrInvalid
		}
	}
	body := struct {
		Content  string `json:"content"`
		Nonce    string `json:"nonce"`
		Enforce  bool   `json:"enforce_nonce"`
		TTS      bool   `json:"tts"`
		Flags    int    `json:"flags"`
		Mentions struct {
			Parse       []string `json:"parse"`
			RepliedUser bool     `json:"replied_user"`
		} `json:"allowed_mentions"`
		Reference MessageReference `json:"message_reference"`
	}{Content: in.Content, Nonce: in.Nonce, Enforce: true, Flags: 4, Reference: MessageReference{Type: 0, MessageID: in.MessageID, ChannelID: in.ChannelID, GuildID: in.GuildID, FailIfNotExists: true}}
	body.Mentions.Parse = []string{}
	b, e := c.do(ctx, token, "POST", "/channels/"+string(in.ChannelID)+"/messages", "POST /channels/:channel/messages", string(in.ChannelID), body)
	if e != nil {
		return Message{}, e
	}
	v, e := DecodeMessage(b)
	if e != nil || v.ChannelID != in.ChannelID || v.Content != in.Content || v.Author.ID != in.BotID || !v.Author.Bot || v.Reference == nil || v.Reference.Type != 0 || v.Reference.MessageID != in.MessageID || v.Reference.ChannelID != in.ChannelID || v.Reference.GuildID != nil && (in.GuildID == nil || *v.Reference.GuildID != *in.GuildID) || v.Nonce != "" && string(v.Nonce) != in.Nonce {
		return Message{}, failure(ErrUnknown, "delivery_unknown")
	}
	return v, nil
}
