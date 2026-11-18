package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/slack"
)

type SlackEnvelope struct {
	Type               string     `json:"type"`
	Challenge          string     `json:"challenge"`
	AppID              string     `json:"api_app_id"`
	TeamID             string     `json:"team_id"`
	EventID            string     `json:"event_id"`
	EventTime          int64      `json:"event_time"`
	IsExtSharedChannel bool       `json:"is_ext_shared_channel"`
	Event              SlackEvent `json:"event"`
}
type SlackEvent struct {
	Type        string                 `json:"type"`
	Subtype     string                 `json:"subtype"`
	User        string                 `json:"user"`
	Channel     string                 `json:"channel"`
	ChannelType string                 `json:"channel_type"`
	Text        string                 `json:"text"`
	TS          string                 `json:"ts"`
	EventTS     string                 `json:"event_ts"`
	ThreadTS    string                 `json:"thread_ts"`
	BotID       string                 `json:"bot_id"`
	AppID       string                 `json:"app_id"`
	Team        string                 `json:"team"`
	Hidden      bool                   `json:"hidden"`
	Metadata    *slack.MessageMetadata `json:"metadata"`
	Links       []struct {
		URL    string `json:"url"`
		Domain string `json:"domain"`
	} `json:"links"`
	Tokens struct {
		Bot   []string `json:"bot"`
		OAuth []string `json:"oauth"`
	} `json:"tokens"`
}

func (s Service) VerifySlackCentral(timestamp, signature string, raw []byte) (SlackEnvelope, error) {
	var event SlackEnvelope
	if !s.SlackConfigured() {
		return event, ErrNotConfigured
	}
	if e := slack.VerifySignature(s.SlackSigningSecret, timestamp, signature, raw, time.Now()); e != nil {
		return event, e
	}
	if json.Unmarshal(raw, &event) != nil {
		return event, ErrInvalidRequest
	}
	if event.Type == "url_verification" && (event.Challenge == "" || len(event.Challenge) > 4096) {
		return event, ErrInvalidRequest
	}
	return event, nil
}

// ReceiveSlackEvent acknowledges only after this transaction commits. App
// generation and current signing credentials are checked together under lock.
func (s Service) ReceiveSlackEvent(ctx context.Context, tx pgx.Tx, org, id, timestamp, signature string, raw []byte) (string, error) {
	a, e := lockSlackApp(ctx, tx, id)
	if e != nil {
		return "", e
	}
	secret, e := s.slackAppSecrets(ctx, tx, org, a)
	if e != nil {
		return "", e
	}
	if e = slack.VerifySignature(secret.SigningSecret, timestamp, signature, raw, time.Now()); e != nil {
		return "", e
	}
	var event SlackEnvelope
	if json.Unmarshal(raw, &event) != nil {
		return "", ErrInvalidRequest
	}
	if a.State == "CANCELLED" || a.State == "DISCONNECTED" || a.State == "ORPHANED_APP" {
		return "", nil
	}
	if a.Kind != "CENTRAL" {
		if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
			if errors.Is(e, ErrNeedsReauth) {
				return "", nil
			}
			return "", e
		}
	}
	if event.Type == "url_verification" {
		if a.Kind == "CENTRAL" || event.Challenge == "" || len(event.Challenge) > 4096 {
			return "", ErrInvalidRequest
		}
		state := a.State
		if state == "WAITING_URL_VERIFICATION" || state == "WAITING_INSTALL" {
			state = "WAITING_INSTALL"
			if a.Installed {
				state = "ACTIVE"
			}
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET url_verified_at=clock_timestamp(),state=$2,activated_at=CASE WHEN $2='ACTIVE' THEN COALESCE(activated_at,clock_timestamp()) ELSE activated_at END,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, state)
		if e == nil && state == "ACTIVE" {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'DISCONNECTED'`, a.ConnectionID)
		}
		return event.Challenge, e
	}
	if event.Type == "app_rate_limited" {
		if event.AppID != a.ExternalAppID || event.TeamID != a.TeamID {
			return "", ErrInvalidRequest
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET status_reason='events_rate_limited',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID)
		return "", e
	}
	if event.Type != "event_callback" || event.AppID != a.ExternalAppID || event.TeamID != a.TeamID || !slack.ID(event.EventID) || len(event.EventID) > 128 || event.EventTime <= 0 {
		return "", ErrInvalidRequest
	}
	if a.State != "ACTIVE" || event.IsExtSharedChannel {
		return "", nil
	}
	var activated *time.Time
	if e = tx.QueryRow(ctx, `SELECT activated_at FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&activated); e != nil {
		return "", e
	}
	// Slack event_time is seconds; same-second first events must remain eligible.
	if activated == nil || event.EventTime < activated.Unix() {
		return "", nil
	}
	switch event.Event.Type {
	case "app_mention", "message", "link_shared", "app_uninstalled", "tokens_revoked":
	default:
		return "", nil
	}
	if (event.Event.Type == "app_mention" || event.Event.Type == "message") && (!slackID(event.Event.Channel) || !slackTimestamp(event.Event.TS)) {
		return "", ErrInvalidRequest
	}
	// Delivery time can be later than the original message. Do not admit a
	// replayed pre-installation message merely because its envelope is new.
	if event.Event.Type == "app_mention" || event.Event.Type == "message" || event.Event.Type == "link_shared" {
		messageTime, err := slackTimestampTime(event.Event.TS)
		if err != nil {
			return "", err
		}
		if messageTime.Unix() < activated.Unix() {
			return "", nil
		}
	}
	sum := sha256.Sum256(raw)
	var key any
	if (event.Event.Type == "app_mention" || event.Event.Type == "message") && event.Event.Subtype == "" {
		// Slack may deliver the same mention as two distinct event IDs. It is one
		// input; modified/deleted message events are ignored by the business worker.
		h := sha256.Sum256([]byte(event.Event.Channel + "\x00" + event.Event.TS))
		key = hex.EncodeToString(h[:])
	}
	tag, e := tx.Exec(ctx, `INSERT INTO openfox.slack_events(organization_id,app_id,team_id,event_id,event_type,generation,payload_hash,payload,message_key) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4,$5,$6,$7::jsonb,$8) ON CONFLICT DO NOTHING`, a.ID, event.TeamID, event.EventID, event.Event.Type, a.Generation, sum[:], raw, key)
	if e != nil {
		return "", e
	}
	if tag.RowsAffected() == 0 {
		var hash []byte
		e = tx.QueryRow(ctx, `SELECT payload_hash FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND team_id=$2 AND event_id=$3`, a.ID, event.TeamID, event.EventID).Scan(&hash)
		if e == nil && string(hash) != string(sum[:]) {
			return "", ErrVersionConflict
		}
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return "", e
		}
	}
	return "", nil
}

func slackTimestamp(v string) bool { _, e := slackTimestampTime(v); return e == nil }
func slackTimestampTime(v string) (time.Time, error) {
	p := strings.Split(v, ".")
	if len(p) != 2 || len(p[0]) < 1 || len(p[0]) > 16 || len(p[1]) < 1 || len(p[1]) > 9 {
		return time.Time{}, ErrInvalidRequest
	}
	for _, x := range p {
		for _, r := range x {
			if r < '0' || r > '9' {
				return time.Time{}, ErrInvalidRequest
			}
		}
	}
	seconds, e := strconv.ParseInt(p[0], 10, 64)
	if e != nil {
		return time.Time{}, ErrInvalidRequest
	}
	nanos, e := strconv.ParseInt(p[1]+strings.Repeat("0", 9-len(p[1])), 10, 64)
	if e != nil {
		return time.Time{}, ErrInvalidRequest
	}
	return time.Unix(seconds, nanos), nil
}
