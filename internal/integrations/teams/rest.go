package teams

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Credentials struct{ TenantID, AppID, Secret string }

func (v Credentials) Valid() bool {
	return GUID(v.TenantID) && GUID(v.AppID) && len(v.Secret) > 0 && len(v.Secret) <= 4096 && !strings.ContainsAny(v.Secret, "\r\n")
}

// AccessToken is deliberately not JSON serializable. Callers keep it in memory
// and fence any credential generation around the network operation.
type AccessToken struct {
	Value     string    `json:"-"`
	ExpiresAt time.Time `json:"-"`
	Scope     string    `json:"-"`
	TenantID  string    `json:"-"`
	AppID     string    `json:"-"`
}

func (c *Client) Token(ctx context.Context, credentials Credentials, scope string) (AccessToken, error) {
	if !credentials.Valid() || scope != ConnectorScope && scope != GraphScope {
		return AccessToken{}, ErrInvalid
	}
	values := url.Values{"grant_type": {"client_credentials"}, "client_id": {credentials.AppID}, "client_secret": {credentials.Secret}, "scope": {scope}}
	data, err := c.request(ctx, http.MethodPost, "https://login.microsoftonline.com/"+credentials.TenantID+"/oauth2/v2.0/token", "", "application/x-www-form-urlencoded", []byte(values.Encode()), 64<<10, false)
	if err != nil {
		return AccessToken{}, err
	}
	var result struct {
		Type    string `json:"token_type"`
		Token   string `json:"access_token"`
		Expires int64  `json:"expires_in"`
	}
	if decodeJSON(data, &result) != nil || !strings.EqualFold(result.Type, "Bearer") || !opaque(result.Token, MaxTokenBytes) || result.Expires <= 0 || result.Expires > int64((365*24*time.Hour)/time.Second) {
		return AccessToken{}, ErrIncomplete
	}
	return AccessToken{Value: result.Token, ExpiresAt: c.now().Add(time.Duration(result.Expires) * time.Second), Scope: scope, TenantID: strings.ToLower(credentials.TenantID), AppID: credentials.AppID}, nil
}

func (c *Client) tokenValid(token AccessToken, scope string, route Route) bool {
	return token.Scope == scope && token.TenantID == route.TenantID && token.AppID == route.AppID && opaque(token.Value, MaxTokenBytes) && c.now().Before(token.ExpiresAt)
}

type Member struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AADObjectID string `json:"aadObjectId"`
	TenantID    string `json:"tenantId"`
	Role        string `json:"userRole"`
}

func (c *Client) Member(ctx context.Context, token AccessToken, route Route) (Member, error) {
	var member Member
	if !c.tokenValid(token, ConnectorScope, route) || route.Validate() != nil {
		return member, ErrInvalid
	}
	base, _ := ServiceURL(route.ServiceURL)
	data, err := c.request(ctx, http.MethodGet, base+"/v3/conversations/"+url.PathEscape(route.ConversationID)+"/members/"+url.PathEscape(route.FromID), token.Value, "", nil, maxResponse, false)
	if err != nil {
		return member, err
	}
	if decodeJSON(data, &member) != nil || member.ID != route.FromID || !GUID(member.AADObjectID) || !strings.EqualFold(member.TenantID, route.TenantID) || route.FromAADObjectID != "" && !strings.EqualFold(route.FromAADObjectID, member.AADObjectID) || len(member.Name) > 1024 {
		return Member{}, ErrIncomplete
	}
	member.AADObjectID = strings.ToLower(member.AADObjectID)
	member.TenantID = strings.ToLower(member.TenantID)
	return member, nil
}

type Team struct {
	ID         string `json:"id"`
	AADGroupID string `json:"aadGroupId"`
	TenantID   string `json:"tenantId"`
}

func (c *Client) Team(ctx context.Context, token AccessToken, route Route) (Team, error) {
	var team Team
	if !c.tokenValid(token, ConnectorScope, route) || route.Validate() != nil || route.ScopeKind != "channel" {
		return team, ErrInvalid
	}
	base, _ := ServiceURL(route.ServiceURL)
	data, err := c.request(ctx, http.MethodGet, base+"/v3/teams/"+url.PathEscape(route.TeamID), token.Value, "", nil, maxResponse, false)
	if err != nil {
		return team, err
	}
	if decodeJSON(data, &team) != nil || team.ID != route.TeamID || !GUID(team.AADGroupID) || team.TenantID != "" && !strings.EqualFold(team.TenantID, route.TenantID) || route.AADGroupID != "" && !strings.EqualFold(team.AADGroupID, route.AADGroupID) {
		return Team{}, ErrIncomplete
	}
	team.AADGroupID = strings.ToLower(team.AADGroupID)
	return team, nil
}

type Channel struct {
	ID             string `json:"id"`
	MembershipType string `json:"membershipType"`
	LayoutType     string `json:"layoutType"`
	IsArchived     *bool  `json:"isArchived"`
}

// Channel proves the limited standard/post capability. Missing metadata is not
// treated as a default. A Graph token is never sent to the Connector or reverse.
func (c *Client) Channel(ctx context.Context, token AccessToken, route Route) (Channel, error) {
	var channel Channel
	if !c.tokenValid(token, GraphScope, route) || route.Validate() != nil || route.ScopeKind != "channel" || !GUID(route.AADGroupID) {
		return channel, ErrInvalid
	}
	query := url.Values{"$select": {"id,membershipType,layoutType,isArchived"}}
	data, err := c.request(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/teams/"+route.AADGroupID+"/channels/"+url.PathEscape(route.ChannelID)+"?"+query.Encode(), token.Value, "", nil, maxResponse, false)
	if err != nil {
		return channel, err
	}
	if decodeJSON(data, &channel) != nil || channel.ID != route.ChannelID {
		return Channel{}, ErrIncomplete
	}
	if channel.MembershipType != "standard" || channel.LayoutType != "post" || channel.IsArchived == nil || *channel.IsArchived {
		return Channel{}, ErrChannelScope
	}
	return channel, nil
}

// Reply performs exactly one POST, with no implicit retry or fallback to a new
// conversation. The worker persists its immutable body/route before calling it.
func (c *Client) Reply(ctx context.Context, token AccessToken, route Route, text string) (string, error) {
	if !c.tokenValid(token, ConnectorScope, route) || route.Validate() != nil || !utf8.ValidString(text) || len([]rune(text)) > 8000 || strings.TrimSpace(text) == "" {
		return "", ErrInvalid
	}
	base, _ := ServiceURL(route.ServiceURL)
	body, err := json.Marshal(struct {
		Type         string  `json:"type"`
		Text         string  `json:"text"`
		TextFormat   string  `json:"textFormat"`
		From         Account `json:"from"`
		Recipient    Account `json:"recipient"`
		Conversation struct {
			ID string `json:"id"`
		} `json:"conversation"`
		ReplyToID string `json:"replyToId"`
	}{Type: "message", Text: text, TextFormat: "plain", From: Account{ID: route.RecipientID}, Recipient: Account{ID: route.FromID}, Conversation: struct {
		ID string `json:"id"`
	}{route.ConversationID}, ReplyToID: route.ActivityID})
	if err != nil {
		return "", ErrInvalid
	}
	data, err := c.request(ctx, http.MethodPost, base+"/v3/conversations/"+url.PathEscape(route.ConversationID)+"/activities/"+url.PathEscape(route.ActivityID), token.Value, "application/json", body, 64<<10, true)
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"id"`
	}
	if decodeJSON(data, &result) != nil || !opaque(result.ID, 2048) {
		return "", &Error{Code: "incomplete_response", Unknown: true}
	}
	return result.ID, nil
}
