package qq

import (
	"encoding/json"
	"strings"
	"time"
)

type MessageEvent struct {
	EventID, Type                                               string
	Scope                                                       Scope
	MessageID, MessageIndex, ChatID, SenderID, MemberRole, Text string
	SentAt                                                      time.Time
	HasAttachments                                              bool
}

type LifecycleEvent struct {
	EventID, Type, SubjectID, ChatID string
	SentAt                           time.Time
}

// Lifecycle payloads use Unix seconds and top-level openid/group_openid, not
// message.author fields. Optional union_openid is never an identity bridge.
func ParseLifecycle(body []byte) (LifecycleEvent, error) {
	var v struct {
		Op   *int            `json:"op"`
		ID   string          `json:"id"`
		Type string          `json:"t"`
		Data json.RawMessage `json:"d"`
	}
	if !object(body) || json.Unmarshal(body, &v) != nil || v.Op == nil || *v.Op != 0 || !ID(v.ID) {
		return LifecycleEvent{}, ErrInvalid
	}
	out := LifecycleEvent{EventID: v.ID, Type: v.Type}
	if v.Type != "FRIEND_DEL" && v.Type != "GROUP_DEL_ROBOT" {
		return out, ErrUnsupported
	}
	var data struct {
		OpenID    string `json:"openid"`
		GroupID   string `json:"group_openid"`
		Timestamp int64  `json:"timestamp"`
	}
	if json.Unmarshal(v.Data, &data) != nil {
		return out, ErrInvalid
	}
	switch v.Type {
	case "FRIEND_DEL":
		out.SubjectID = data.OpenID
		out.ChatID = out.SubjectID
	case "GROUP_DEL_ROBOT":
		out.ChatID = data.GroupID
	default:
		return out, ErrUnsupported
	}
	if !ID(out.ChatID) || data.Timestamp <= 0 || data.Timestamp > 253402300799 {
		return LifecycleEvent{}, ErrInvalid
	}
	out.SentAt = time.Unix(data.Timestamp, 0).UTC()
	return out, nil
}

// ParseMessage is called after signature verification. Only the documented
// ordinary text shape is accepted; auth_token, nicknames, mentions and nested
// history never become trusted identity or additional prompt content.
func ParseMessage(body []byte) (MessageEvent, error) {
	var v struct {
		Op   *int   `json:"op"`
		ID   string `json:"id"`
		Type string `json:"t"`
		Data struct {
			ID, Content, Timestamp string
			GroupID                string `json:"group_openid"`
			MessageType            *int   `json:"message_type"`
			Author                 struct {
				UserID   string `json:"user_openid"`
				MemberID string `json:"member_openid"`
				Role     string `json:"member_role"`
				Bot      bool   `json:"bot"`
			} `json:"author"`
			Scene struct {
				Source string   `json:"source"`
				Ext    []string `json:"ext"`
			} `json:"message_scene"`
			Attachments []json.RawMessage `json:"attachments"`
		} `json:"d"`
	}
	if !object(body) || json.Unmarshal(body, &v) != nil || v.Op == nil || *v.Op != 0 || !ID(v.ID) {
		return MessageEvent{}, ErrInvalid
	}
	out := MessageEvent{EventID: v.ID, Type: v.Type, MessageID: v.Data.ID, Text: v.Data.Content, HasAttachments: len(v.Data.Attachments) > 0}
	switch v.Type {
	case "C2C_MESSAGE_CREATE":
		out.Scope = C2C
		out.SenderID = v.Data.Author.UserID
		out.ChatID = out.SenderID
	case "GROUP_AT_MESSAGE_CREATE":
		out.Scope = Group
		out.ChatID = v.Data.GroupID
		out.SenderID = v.Data.Author.MemberID
		out.MemberRole = v.Data.Author.Role
	default:
		return MessageEvent{}, ErrUnsupported
	}
	if v.Data.Author.Bot || v.Data.MessageType == nil || *v.Data.MessageType != 0 || v.Data.Scene.Source != "" && v.Data.Scene.Source != "default" {
		return MessageEvent{}, ErrUnsupported
	}
	if !ID(out.MessageID) || !ID(out.ChatID) || !ID(out.SenderID) || !text(out.Text) {
		return MessageEvent{}, ErrInvalid
	}
	var err error
	out.SentAt, err = time.Parse(time.RFC3339Nano, v.Data.Timestamp)
	if err != nil {
		return MessageEvent{}, ErrInvalid
	}
	for _, entry := range v.Data.Scene.Ext {
		if strings.HasPrefix(entry, "msg_idx=") {
			index := strings.TrimPrefix(entry, "msg_idx=")
			if !ID(index) || out.MessageIndex != "" {
				return MessageEvent{}, ErrInvalid
			}
			out.MessageIndex = index
		}
	}
	return out, nil
}
