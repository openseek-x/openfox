package integrations

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/slack"
	"openfox/internal/membership"
)

const slackChannelColumns = `id::text,channel_id,name,COALESCE(space_id::text,''),enabled,version,generation,public_reply_consent,last_seen_at`

func scanSlackChannel(row pgx.Row) (SlackChannel, error) {
	var c SlackChannel
	e := row.Scan(&c.ID, &c.ChannelID, &c.Name, &c.SpaceID, &c.Enabled, &c.Version, &c.Generation, &c.PublicReplyConsent, &c.LastSeenAt)
	return c, e
}
func slackAppForBot(ctx context.Context, tx pgx.Tx, bot string) (SlackApp, error) {
	if !membership.ValidateUUID(bot) {
		return SlackApp{}, ErrNotFound
	}
	var id string
	e := tx.QueryRow(ctx, `SELECT a.id::text FROM openfox.slack_apps a JOIN openfox.im_bots b ON b.organization_id=a.organization_id AND b.id=a.bot_id AND b.removed_at IS NULL WHERE a.organization_id=openfox_private.current_organization_id() AND a.bot_id=$1::uuid`, bot).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	if e != nil {
		return SlackApp{}, e
	}
	return loadSlackApp(ctx, tx, id)
}
func (s Service) SlackChannels(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot, cursor string) (SlackChannelsPage, error) {
	out := SlackChannelsPage{Channels: []SlackChannel{}}
	if cursor != "" && !membership.ValidateUUID(cursor) {
		return out, ErrInvalidRequest
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	a, e := slackAppForBot(ctx, tx, bot)
	if e != nil {
		return out, e
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `SELECT `+slackChannelColumns+` FROM openfox.slack_channels c WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND ($2='' OR id>NULLIF($2,'')::uuid) AND NOT EXISTS(SELECT 1 FROM openfox.slack_channels n WHERE n.organization_id=c.organization_id AND n.app_id=c.app_id AND n.channel_id=c.channel_id AND n.version>c.version) ORDER BY id LIMIT 51`, a.ID, cursor)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		c, e := scanSlackChannel(rows)
		if e != nil {
			return out, e
		}
		out.Channels = append(out.Channels, c)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Channels) > 50 {
		out.Channels = out.Channels[:50]
		out.NextCursor = out.Channels[49].ID
	}
	return out, nil
}

type SlackChannelTarget struct {
	App                                     SlackApp
	Actor                                   collaboration.Actor
	ChannelID, IdentityID, SlackUser, Token string
	IdentityVersion                         uint64
	Input                                   SlackChannelInput
}

func (SlackChannelTarget) String() string   { return "SlackChannelTarget([redacted])" }
func (SlackChannelTarget) GoString() string { return "SlackChannelTarget([redacted])" }
func (s Service) PrepareSlackChannel(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot, channel string, in SlackChannelInput) (SlackChannelTarget, error) {
	out := SlackChannelTarget{ChannelID: channel, Input: in}
	if !slackID(channel) || !in.Enabled || !in.PublicReplyConsent {
		return out, ErrInvalidRequest
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return out, e
	}
	a, e := slackAppForBot(ctx, tx, bot)
	if e != nil {
		return out, e
	}
	a, e = lockSlackApp(ctx, tx, a.ID)
	if e != nil {
		return out, e
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return out, e
	}
	if a.State != "ACTIVE" {
		return out, ErrNeedsReauth
	}
	if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
		return out, e
	}
	e = tx.QueryRow(ctx, `SELECT i.id::text,i.version,i.slack_user_id FROM openfox.slack_identities i JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid AND i.owner_user_id=$2::uuid AND i.team_id=$3 AND i.active AND c.status='ACTIVE' FOR SHARE OF i`, a.ParentAppID, current.UserID, a.TeamID).Scan(&out.IdentityID, &out.IdentityVersion, &out.SlackUser)
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNeedsReauth
	}
	if e != nil {
		return out, e
	}
	var version uint64
	e = tx.QueryRow(ctx, `SELECT version FROM openfox.slack_channels WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND channel_id=$2 ORDER BY version DESC LIMIT 1`, a.ID, channel).Scan(&version)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	if version != in.Version {
		return out, ErrVersionConflict
	}
	token, e := s.slackStoredToken(ctx, tx, org, a)
	if e != nil {
		return out, e
	}
	if !token.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		return out, ErrNeedsReauth
	}
	out.App = a
	out.Actor = current
	out.Token = token.AccessToken
	return out, nil
}
func (s Service) VerifySlackChannel(ctx context.Context, t SlackChannelTarget) (slack.Conversation, error) {
	u, e := s.slackClient().UserInfo(ctx, t.Token, t.SlackUser)
	if e != nil {
		return slack.Conversation{}, e
	}
	if !slackOrdinaryUser(u, t.App.TeamID, t.SlackUser) || u.ID != t.SlackUser {
		return slack.Conversation{}, ErrForbidden
	}
	c, e := s.slackClient().ConversationInfo(ctx, t.Token, t.ChannelID)
	if e != nil {
		return c, e
	}
	if c.ID != t.ChannelID || !slackOrdinaryConversation(c, t.App.TeamID, "", false) || utf8.RuneCountInString(c.Name) > 200 {
		return c, ErrForbidden
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 5; page++ {
		m, e := s.slackClient().ConversationMembers(ctx, t.Token, t.ChannelID, cursor)
		if e != nil {
			return c, e
		}
		for _, id := range m.Members {
			if id == t.SlackUser {
				return c, nil
			}
		}
		if m.Next == "" {
			return c, ErrForbidden
		}
		if seen[m.Next] {
			return c, slack.ErrIncomplete
		}
		seen[m.Next] = true
		cursor = m.Next
	}
	return c, slack.ErrIncomplete
}
func (s Service) FinishSlackChannel(ctx context.Context, tx pgx.Tx, org string, t SlackChannelTarget, c slack.Conversation) (SlackChannel, error) {
	a, e := lockSlackApp(ctx, tx, t.App.ID)
	if e != nil {
		return SlackChannel{}, e
	}
	actor, e := slackActor(ctx, tx, org, t.Actor)
	if e != nil {
		return SlackChannel{}, e
	}
	if a.Generation != t.App.Generation || a.State != "ACTIVE" {
		return SlackChannel{}, ErrVersionConflict
	}
	if e = slackAppManager(ctx, tx, org, actor, a); e != nil {
		return SlackChannel{}, e
	}
	if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
		return SlackChannel{}, e
	}
	var old SlackChannel
	old, e = scanSlackChannel(tx.QueryRow(ctx, `SELECT `+slackChannelColumns+` FROM openfox.slack_channels WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND channel_id=$2 ORDER BY version DESC LIMIT 1 FOR UPDATE`, a.ID, t.ChannelID))
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return old, e
	}
	if old.Version != t.Input.Version {
		return old, ErrVersionConflict
	}
	var valid bool
	e = tx.QueryRow(ctx, `SELECT active AND version=$2 AND owner_user_id=$3::uuid FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, t.IdentityID, t.IdentityVersion, actor.UserID).Scan(&valid)
	if e != nil {
		return old, e
	}
	if !valid {
		return old, ErrNeedsReauth
	}
	if old.Enabled {
		return old, nil
	}
	// Prior disabled epochs remain immutable. A new row prevents old routes and
	// queued Runs from reviving; version remains monotonic for the channel UI CAS.
	return scanSlackChannel(tx.QueryRow(ctx, `INSERT INTO openfox.slack_channels(organization_id,app_id,channel_id,name,space_id,enabled,version,generation,authorized_by,identity_id,identity_version,public_reply_consent) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,NULLIF($4,'')::uuid,true,$5,$6,$7::uuid,$8::uuid,$9,true) RETURNING `+slackChannelColumns, a.ID, t.ChannelID, c.Name, a.SpaceID, old.Version+1, a.Generation, actor.UserID, t.IdentityID, t.IdentityVersion))
}
func (s Service) DisableSlackChannel(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, bot, channel string, version uint64) (SlackChannel, error) {
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return SlackChannel{}, e
	}
	a, e := slackAppForBot(ctx, tx, bot)
	if e != nil {
		return SlackChannel{}, e
	}
	a, e = lockSlackApp(ctx, tx, a.ID)
	if e != nil {
		return SlackChannel{}, e
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return SlackChannel{}, e
	}
	old, e := scanSlackChannel(tx.QueryRow(ctx, `SELECT `+slackChannelColumns+` FROM openfox.slack_channels WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND channel_id=$2 ORDER BY version DESC LIMIT 1 FOR UPDATE`, a.ID, channel))
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	if e != nil {
		return old, e
	}
	if old.Version != version {
		return old, ErrVersionConflict
	}
	if !old.Enabled {
		return old, nil
	}
	return scanSlackChannel(tx.QueryRow(ctx, `UPDATE openfox.slack_channels SET enabled=false,disabled_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid RETURNING `+slackChannelColumns, old.ID))
}
