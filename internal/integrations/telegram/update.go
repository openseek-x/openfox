package telegram

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

// VerifyWebhookSecret requires exactly one header and compares equal-length
// secret values in constant time. Caller must enforce body limits before decode.
func VerifyWebhookSecret(secret string, header http.Header) error {
	if !validWebhookSecret(secret) {
		return ErrSignature
	}
	var values []string
	for k, v := range header {
		if strings.EqualFold(k, "X-Telegram-Bot-Api-Secret-Token") {
			values = append(values, v...)
		}
	}
	if len(values) != 1 || len(values[0]) != len(secret) || subtle.ConstantTimeCompare([]byte(values[0]), []byte(secret)) != 1 {
		return ErrSignature
	}
	return nil
}
func ordinaryChat(chat Chat) bool {
	return !chat.IsForum && !chat.IsDirectMessages && (chat.Type == "private" || chat.Type == "group" || chat.Type == "supergroup")
}
func plainText(text string) bool {
	if text == "" || !bounded(text, 4096) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return false
		}
	}
	return true
}
func messageReason(name string) string {
	switch name {
	case "message_thread_id", "direct_messages_topic", "is_topic_message":
		return "unsupported_topic"
	case "business_connection_id", "sender_business_bot", "is_from_offline":
		return "unsupported_business"
	case "guest_query_id", "guest_bot_caller_user", "guest_bot_caller_chat":
		return "unsupported_guest"
	case "receiver_user", "ephemeral_message_id":
		return "unsupported_ephemeral"
	case "sender_chat", "author_signature", "is_automatic_forward":
		return "unsupported_anonymous"
	case "forward_origin":
		return "unsupported_forward"
	case "via_bot":
		return "unsupported_bot"
	case "edit_date":
		return "unsupported_edit"
	default:
		return "unsupported_message"
	}
}
func parseMessage(raw []byte, depth int) (Message, error) {
	if depth > 1 {
		return Message{}, ErrInvalid
	}
	m, e := object(raw)
	if e != nil || !required(m, "message_id", "date", "chat") {
		return Message{}, ErrInvalid
	}
	var out Message
	// Parse only fields we consume. Never decode/store opaque attachment payloads.
	for name, target := range map[string]any{"message_id": &out.ID, "date": &out.Date, "text": &out.Text, "migrate_to_chat_id": &out.MigrateToChatID, "migrate_from_chat_id": &out.MigrateFromChatID} {
		if v, ok := m[name]; ok {
			if !required(m, name) || json.Unmarshal(v, target) != nil {
				return Message{}, ErrInvalid
			}
		}
	}
	if out.ID < 0 || out.Date <= 0 {
		return Message{}, ErrInvalid
	}
	out.Chat, e = parseChat(m["chat"])
	if e != nil {
		return Message{}, e
	}
	if v, ok := m["from"]; ok {
		u, e := parseUser(v)
		if e != nil {
			return Message{}, e
		}
		out.From = &u
	}
	if v, ok := m["reply_to_message"]; ok {
		reply, e := parseMessage(v, depth+1)
		if e != nil {
			return Message{}, e
		}
		out.ReplyToMessage = &reply
	}
	if v, ok := m["entities"]; ok {
		if len(v) == 0 || v[0] != '[' || json.Unmarshal(v, &out.Entities) != nil || len(out.Entities) > MaxEntities {
			return Message{}, ErrInvalid
		}
		var items []json.RawMessage
		if json.Unmarshal(v, &items) != nil {
			return Message{}, ErrInvalid
		}
		for i, entity := range items {
			fields, e := object(entity)
			if e != nil || !required(fields, "type", "offset", "length") || !bounded(out.Entities[i].Type, 64) || out.Entities[i].Type == "" {
				return Message{}, ErrInvalid
			}
			if _, e = UTF16Slice(out.Text, out.Entities[i].Offset, out.Entities[i].Length); e != nil {
				return Message{}, e
			}
			if v, ok := fields["user"]; ok {
				u, e := parseUser(v)
				if e != nil {
					return Message{}, e
				}
				out.Entities[i].User = &u
			}
			if out.Entities[i].Type == "text_mention" && out.Entities[i].User == nil {
				return Message{}, ErrInvalid
			}
		}
	}
	// First choose a deterministic, fixed reason. This allow-list intentionally
	// fails closed on future message shapes; it never promotes captions/media.
	for name := range m {
		switch name {
		case "message_id", "date", "chat", "from", "text", "entities", "reply_to_message", "migrate_to_chat_id", "migrate_from_chat_id", "has_protected_content", "link_preview_options", "sender_boost_count", "sender_tag":
			continue
		}
		reason := messageReason(name)
		if out.UnsupportedReason == "" || reason < out.UnsupportedReason {
			out.UnsupportedReason = reason
		}
	}
	if !ordinaryChat(out.Chat) {
		out.UnsupportedReason = "unsupported_chat"
		if out.Chat.IsForum || out.Chat.IsDirectMessages {
			out.UnsupportedReason = "unsupported_topic"
		}
	}
	if out.ID == 0 {
		out.UnsupportedReason = "unsupported_ephemeral"
	}
	return out, nil
}
func parseMemberUpdate(raw []byte) (ChatMemberUpdated, error) {
	m, e := object(raw)
	var out ChatMemberUpdated
	if e != nil || !required(m, "chat", "from", "date", "old_chat_member", "new_chat_member") {
		return out, ErrInvalid
	}
	out.Chat, e = parseChat(m["chat"])
	if e != nil {
		return out, e
	}
	out.From, e = parseUser(m["from"])
	if e != nil {
		return out, e
	}
	if json.Unmarshal(m["date"], &out.Date) != nil || out.Date <= 0 {
		return out, ErrInvalid
	}
	out.OldChatMember, e = parseMember(m["old_chat_member"])
	if e != nil {
		return out, e
	}
	out.NewChatMember, e = parseMember(m["new_chat_member"])
	if e != nil || out.NewChatMember.User.ID != out.OldChatMember.User.ID || !out.NewChatMember.User.IsBot || !out.OldChatMember.User.IsBot {
		return out, ErrInvalid
	}
	return out, nil
}

// DecodeUpdate rejects malformed input; well-formed unsupported kinds retain
// their ID with ErrUnsupported so the service can ACK/diagnose without running.
// Authentication, deduplication and durable ACK must precede any dispatch.
func DecodeUpdate(raw []byte) (Update, error) {
	if len(raw) > MaxBodyBytes {
		return Update{}, ErrInvalid
	}
	m, e := object(raw)
	var out Update
	if e != nil || !required(m, "update_id") || json.Unmarshal(m["update_id"], &out.ID) != nil || out.ID < 0 || len(m) != 2 {
		return Update{}, ErrInvalid
	}
	if v, ok := m["message"]; ok {
		out.Kind = "message"
		message, e := parseMessage(v, 0)
		if e != nil {
			return out, e
		}
		out.Message = &message
		if message.UnsupportedReason != "" {
			return out, fixed(ErrUnsupported, message.UnsupportedReason)
		}
		if message.MigrateToChatID != 0 || message.MigrateFromChatID != 0 {
			out.Kind = "migration"
			if message.Text != "" || message.MigrateToChatID > 0 || message.MigrateFromChatID > 0 || message.MigrateToChatID != 0 && message.MigrateFromChatID != 0 || message.MigrateToChatID != 0 && message.Chat.Type != "group" || message.MigrateFromChatID != 0 && message.Chat.Type != "supergroup" {
				return out, ErrInvalid
			}
			return out, nil
		}
		if e := ordinaryMessage(message); e != nil {
			return out, e
		}
		return out, nil
	}
	if v, ok := m["my_chat_member"]; ok {
		out.Kind = "my_chat_member"
		member, e := parseMemberUpdate(v)
		if e != nil {
			return out, e
		}
		out.MyChatMember = &member
		if !ordinaryChat(member.Chat) {
			return out, fixed(ErrUnsupported, "unsupported_chat")
		}
		return out, nil
	}
	out.Kind = "unsupported"
	return out, fixed(ErrUnsupported, "unsupported_update")
}
func ordinaryMessage(m Message) error {
	if m.UnsupportedReason != "" {
		return fixed(ErrUnsupported, m.UnsupportedReason)
	}
	if !positiveID(m.ID) || m.Date <= 0 || !validID(m.Chat.ID) || !ordinaryChat(m.Chat) || m.MigrateToChatID != 0 || m.MigrateFromChatID != 0 {
		return fixed(ErrUnsupported, "unsupported_message")
	}
	if m.From == nil || !positiveID(m.From.ID) || m.From.IsBot {
		return fixed(ErrUnsupported, "unsupported_sender")
	}
	if !plainText(m.Text) {
		return fixed(ErrUnsupported, "unsupported_text")
	}
	return nil
}

func UTF16Len(text string) int {
	n := 0
	for _, r := range text {
		n++
		if r > 0xFFFF {
			n++
		}
	}
	return n
}
func UTF16Slice(text string, offset, length int) (string, error) {
	if !utf8.ValidString(text) || offset < 0 || length <= 0 || offset > 8192 || length > 8192 || offset > 8192-length {
		return "", ErrInvalid
	}
	start, end := -1, -1
	units := 0
	for pos, r := range text {
		if units == offset {
			start = pos
		}
		if units == offset+length {
			end = pos
			break
		}
		units++
		if r > 0xFFFF {
			units++
		}
	}
	if units == offset {
		start = len(text)
	}
	if end < 0 && units == offset+length {
		end = len(text)
	}
	if start < 0 || end < 0 {
		return "", ErrInvalid
	}
	return text[start:end], nil
}

// ParseText uses entity offsets, never string containment. Group commands must
// explicitly name the current getMe username; private commands may omit it.
func ParseText(message Message, bot Bot) (TextFacts, error) {
	if e := ordinaryMessage(message); e != nil {
		return TextFacts{}, e
	}
	if !positiveID(bot.ID) || !bot.IsBot || !username(bot.Username) || len(message.Entities) > MaxEntities {
		return TextFacts{}, ErrInvalid
	}
	out := TextFacts{Text: message.Text}
	for _, entity := range message.Entities {
		text, e := UTF16Slice(message.Text, entity.Offset, entity.Length)
		if e != nil {
			return TextFacts{}, e
		}
		switch entity.Type {
		case "mention":
			if strings.EqualFold(text, "@"+bot.Username) {
				out.Mentioned = true
			}
		case "text_mention":
			if entity.User == nil {
				return TextFacts{}, ErrInvalid
			}
			if entity.User.ID == bot.ID && entity.User.IsBot {
				out.Mentioned = true
			}
		case "bot_command":
			if entity.Offset != 0 || !strings.HasPrefix(text, "/") {
				continue
			}
			parts := strings.Split(text[1:], "@")
			if len(parts) > 2 || len(parts[0]) < 1 || len(parts[0]) > 32 || !username(parts[0]) {
				continue
			}
			targeted := len(parts) == 2 && strings.EqualFold(parts[1], bot.Username) || len(parts) == 1 && message.Chat.Type == "private"
			if !targeted {
				continue
			}
			// An entity cannot turn a prefix of /ask@OtherBot into a
			// command for us. A following argument needs a separator.
			if len(text) < len(message.Text) {
				next, _ := utf8.DecodeRuneInString(message.Text[len(text):])
				if !unicode.IsSpace(next) {
					return TextFacts{}, ErrInvalid
				}
			}
			if out.Command != "" {
				return TextFacts{}, ErrInvalid
			}
			out.Command = parts[0]
			out.CommandArgument = strings.TrimSpace(message.Text[len(text):])
			out.Mentioned = true
		}
	}
	if reply := message.ReplyToMessage; reply != nil && reply.Chat.ID == message.Chat.ID && positiveID(reply.ID) && reply.UnsupportedReason == "" && reply.From != nil && reply.From.ID == bot.ID && reply.From.IsBot {
		out.ReplyToBot = true
		out.ReplyToMessageID = reply.ID
	}
	return out, nil
}
