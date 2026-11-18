package teams

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"
	"time"
)

type Account struct {
	ID                    string `json:"id"`
	Name                  string `json:"name,omitempty"`
	AADObjectID           string `json:"aadObjectId,omitempty"`
	Role                  string `json:"role,omitempty"`
	Type                  string `json:"type,omitempty"`
	AgenticUserID         string `json:"agenticUserId,omitempty"`
	AgenticAppID          string `json:"agenticAppId,omitempty"`
	AgenticAppBlueprintID string `json:"agenticAppBlueprintId,omitempty"`
}

type Activity struct {
	Type         string  `json:"type"`
	ID           string  `json:"id"`
	Timestamp    string  `json:"timestamp"`
	ServiceURL   string  `json:"serviceUrl"`
	ChannelID    string  `json:"channelId"`
	From         Account `json:"from"`
	Recipient    Account `json:"recipient"`
	Conversation struct {
		ID       string `json:"id"`
		Type     string `json:"conversationType"`
		TenantID string `json:"tenantId"`
	} `json:"conversation"`
	ChannelData struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
		Team struct {
			ID         string `json:"id"`
			AADGroupID string `json:"aadGroupId"`
		} `json:"team"`
		Channel struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"channel"`
		ReplyChain json.RawMessage `json:"replyChain"`
		Chat       json.RawMessage `json:"chat"`
		Source     json.RawMessage `json:"source"`
	} `json:"channelData"`
	Text       string `json:"text"`
	TextFormat string `json:"textFormat"`
	Entities   []struct {
		Type      string  `json:"type"`
		Text      string  `json:"text"`
		Mentioned Account `json:"mentioned"`
	} `json:"entities"`
	Attachments    []json.RawMessage `json:"attachments"`
	MembersAdded   []Account         `json:"membersAdded"`
	MembersRemoved []Account         `json:"membersRemoved"`
	Action         string            `json:"action"`
}

type Route struct {
	TenantID        string `json:"tenant_id"`
	AppID           string `json:"app_id"`
	ServiceURL      string `json:"service_url"`
	ConversationID  string `json:"conversation_id"`
	ActivityID      string `json:"activity_id"`
	FromID          string `json:"from_id"`
	FromAADObjectID string `json:"from_aad_object_id,omitempty"`
	RecipientID     string `json:"recipient_id"`
	ScopeKind       string `json:"scope_kind"`
	TeamID          string `json:"team_id,omitempty"`
	AADGroupID      string `json:"aad_group_id,omitempty"`
	ChannelID       string `json:"channel_id,omitempty"`
}

type VerifiedActivity struct {
	Activity  Activity
	Route     Route
	Text      string
	Mentioned bool
	Supported bool
	BodyHash  string
}

func (c *Client) AuthenticateActivity(ctx context.Context, authorization string, raw []byte, tenantID, appID string) (VerifiedActivity, error) {
	var out VerifiedActivity
	if len(raw) == 0 || len(raw) > MaxActivityBytes || !GUID(tenantID) || !GUID(appID) {
		return out, ErrInvalid
	}
	if decodeJSON(raw, &out.Activity) != nil {
		return out, ErrInvalid
	}
	a := out.Activity
	if a.ChannelID != "msteams" || !strings.EqualFold(a.ChannelData.Tenant.ID, tenantID) || a.Recipient.ID != "28:"+appID {
		return out, ErrUnauthorized
	}
	if a.Conversation.TenantID != "" && !strings.EqualFold(a.Conversation.TenantID, tenantID) {
		return out, ErrUnauthorized
	}
	if !opaque(a.ID, 2048) || !opaque(a.Conversation.ID, 4096) || !opaque(a.From.ID, 1024) || !opaque(a.Recipient.ID, 1024) || len(a.Entities) > 256 {
		return out, ErrInvalid
	}
	if a.From.AADObjectID != "" && !GUID(a.From.AADObjectID) {
		return out, ErrInvalid
	}
	if err := c.ValidateJWT(ctx, authorization, appID, a.ServiceURL); err != nil {
		return out, err
	}
	if a.From.Role == "bot" || a.From.Type == "bot" || a.From.agentic() || a.Recipient.agentic() {
		return out, ErrUnsupported
	}
	serviceURL, err := ServiceURL(a.ServiceURL)
	if err != nil {
		return out, err
	}
	out.Route = Route{TenantID: strings.ToLower(tenantID), AppID: appID, ServiceURL: serviceURL, ConversationID: a.Conversation.ID, ActivityID: a.ID, FromID: a.From.ID, FromAADObjectID: strings.ToLower(a.From.AADObjectID), RecipientID: a.Recipient.ID}
	switch a.Conversation.Type {
	case "personal":
		out.Route.ScopeKind = "personal"
		if a.ChannelData.Team.ID != "" || a.ChannelData.Channel.ID != "" {
			return out, ErrInvalid
		}
	case "groupChat":
		out.Route.ScopeKind = "group"
		if a.ChannelData.Team.ID != "" || a.ChannelData.Channel.ID != "" {
			return out, ErrUnsupported
		}
	case "channel":
		out.Route.ScopeKind = "channel"
		if !opaque(a.ChannelData.Team.ID, 2048) || !opaque(a.ChannelData.Channel.ID, 2048) {
			return out, ErrInvalid
		}
		out.Route.TeamID = a.ChannelData.Team.ID
		out.Route.ChannelID = a.ChannelData.Channel.ID
		if id := a.ChannelData.Team.AADGroupID; id != "" {
			if !GUID(id) {
				return out, ErrInvalid
			}
			out.Route.AADGroupID = strings.ToLower(id)
		}
		if a.ChannelData.Channel.Type != "" && a.ChannelData.Channel.Type != "standard" {
			return out, ErrUnsupported
		}
	default:
		return out, ErrUnsupported
	}
	if present(a.ChannelData.ReplyChain) || present(a.ChannelData.Chat) || present(a.ChannelData.Source) {
		return out, ErrUnsupported
	}
	if a.Timestamp != "" {
		if _, err := time.Parse(time.RFC3339Nano, a.Timestamp); err != nil {
			return out, ErrInvalid
		}
	}
	for _, entity := range a.Entities {
		if entity.Type == "mention" && entity.Mentioned.ID == a.Recipient.ID && entity.Text != "" {
			out.Mentioned = true
			// Match the authenticated entity's literal text; a display name or an
			// arbitrary <at> tag alone never grants a group invocation.
			a.Text = strings.ReplaceAll(a.Text, entity.Text, "")
		}
	}
	if a.TextFormat == "xml" {
		plain, e := activityXMLText(a.Text)
		if e != nil {
			return out, ErrUnsupported
		}
		a.Text = plain
	}
	out.Text = strings.TrimSpace(a.Text)
	out.Supported = a.Type == "message" && out.Text != "" && (a.TextFormat == "" || a.TextFormat == "plain" || a.TextFormat == "xml" || a.TextFormat == "markdown")
	if out.Route.ScopeKind != "personal" && !out.Mentioned {
		out.Supported = false
	}
	sum := sha256.Sum256(raw)
	out.BodyHash = hex.EncodeToString(sum[:])
	return out, nil
}

func present(v json.RawMessage) bool { return len(v) > 0 && string(v) != "null" }

func (a Account) agentic() bool {
	return a.AgenticUserID != "" || a.AgenticAppID != "" || a.AgenticAppBlueprintID != ""
}

// StableKey is for bounded existing IM columns. Raw identifiers remain in the
// route. Unambiguous tuple encoding keeps different opaque tuples distinct.
func StableKey(domain string, values ...string) string {
	data, _ := json.Marshal(append([]string{domain}, values...))
	sum := sha256.Sum256(data)
	return "teams:" + hex.EncodeToString(sum[:])
}

func (r Route) Validate() error {
	if r.FromAADObjectID != "" && !GUID(r.FromAADObjectID) {
		return ErrInvalid
	}
	if !GUID(r.TenantID) || !GUID(r.AppID) || r.RecipientID != "28:"+r.AppID || !opaque(r.ConversationID, 4096) || !opaque(r.ActivityID, 2048) || !opaque(r.FromID, 1024) {
		return ErrInvalid
	}
	if _, err := ServiceURL(r.ServiceURL); err != nil {
		return err
	}
	switch r.ScopeKind {
	case "personal", "group":
		if r.TeamID != "" || r.ChannelID != "" || r.AADGroupID != "" {
			return ErrInvalid
		}
	case "channel":
		if !opaque(r.TeamID, 2048) || !opaque(r.ChannelID, 2048) || r.AADGroupID != "" && !GUID(r.AADGroupID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (r Route) ScopeKey() string {
	if r.ScopeKind == "channel" {
		return StableKey("scope:channel", r.TenantID, r.AppID, r.TeamID, r.ChannelID)
	}
	return StableKey("scope:"+r.ScopeKind, r.TenantID, r.AppID, r.ConversationID)
}

func (r Route) ChatKey() string {
	return StableKey("conversation", r.TenantID, r.AppID, r.ConversationID)
}
func (r Route) MessageKey() string {
	return StableKey("activity", r.TenantID, r.AppID, r.ConversationID, r.ActivityID)
}

// Teams XML text is presented as text, never executable markup. In particular,
// formatting around a binding command must not route its nonce into a Runtime.
func activityXMLText(raw string) (string, error) {
	d := xml.NewDecoder(strings.NewReader("<root>" + raw + "</root>"))
	d.Strict = false
	d.AutoClose = []string{"br", "hr"}
	d.Entity = map[string]string{"nbsp": " "}
	var text strings.Builder
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch v := token.(type) {
		case xml.CharData:
			text.Write(v)
		case xml.StartElement:
			if v.Name.Local == "br" || v.Name.Local == "p" || v.Name.Local == "div" {
				text.WriteByte('\n')
			}
		case xml.EndElement:
			if v.Name.Local == "p" || v.Name.Local == "div" {
				text.WriteByte('\n')
			}
		case xml.Directive:
			return "", ErrUnsupported
		}
	}
	return text.String(), nil
}
