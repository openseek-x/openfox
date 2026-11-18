package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/slack"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type slackInboundJob struct {
	ID, AppID, Lease string
	Generation       uint64
	Attempts         int
	Envelope         SlackEnvelope
}
type slackInboundTarget struct {
	App                                                 SlackApp
	Actor                                               conversations.Actor
	IdentityID, EpochID, RouteID, ConversationID, Token string
	IdentityVersion                                     uint64
	DM                                                  bool
	Thread                                              string
}

var errSlackUnbound = errors.New("slack: personal identity not bound")

func (w *IMWorker) processSlackMessages(ctx context.Context, org string) error {
	nonce, e := randomBase64URL(12)
	if e != nil {
		return e
	}
	lease := w.instance() + ":" + nonce
	var jobs []slackInboundJob
	e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `WITH due AS(SELECT id FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND EXISTS(SELECT 1 FROM openfox.slack_apps a WHERE a.organization_id=openfox.slack_events.organization_id AND a.id=openfox.slack_events.app_id AND a.kind<>'CENTRAL') AND event_type IN ('message','app_mention') AND COALESCE(payload->'event'->>'bot_id','')='' AND COALESCE(payload->'event'->>'subtype','')='' AND ((state='PENDING' AND next_attempt_at<=clock_timestamp()) OR (state='PROCESSING' AND lease_expires_at<=clock_timestamp())) ORDER BY received_at,id LIMIT 10 FOR UPDATE SKIP LOCKED) UPDATE openfox.slack_events e SET state='PROCESSING',lease_owner=$1,lease_expires_at=clock_timestamp()+interval '60 seconds',attempts=attempts+1 FROM due WHERE e.organization_id=openfox_private.current_organization_id() AND e.id=due.id RETURNING e.id::text,e.app_id::text,e.generation,e.attempts,e.payload`, lease)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			j := slackInboundJob{Lease: lease}
			var raw []byte
			if e = rows.Scan(&j.ID, &j.AppID, &j.Generation, &j.Attempts, &raw); e != nil {
				return e
			}
			if json.Unmarshal(raw, &j.Envelope) != nil {
				return ErrInvalidRequest
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	var all []error
	for _, j := range jobs {
		jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		e = w.processSlackMessage(jobCtx, org, j)
		if errors.Is(e, errSlackUnbound) {
			if j.Envelope.Event.ChannelType == "im" {
				e = w.inOrganization(jobCtx, org, func(ctx context.Context, tx pgx.Tx) error { return w.materializeSlackSpecial(ctx, tx, org, j) })
			} else {
				e = ErrForbidden
			}
		}
		cancel()
		if e != nil {
			if save := w.failSlackEvent(ctx, org, j, e); save != nil {
				all = append(all, save)
			}
		}
	}
	return errors.Join(all...)
}
func slackInboundActor(a collaboration.Actor) conversations.Actor {
	return conversations.Actor{UserID: a.UserID, Role: a.Role, AuthorizationVersion: a.AuthorizationVersion}
}
func (w *IMWorker) prepareSlackMessage(ctx context.Context, tx pgx.Tx, org string, j slackInboundJob) (slackInboundTarget, error) {
	out := slackInboundTarget{}
	a, e := lockSlackApp(ctx, tx, j.AppID)
	if e != nil {
		return out, e
	}
	out.App = a
	if a.Kind == "CENTRAL" {
		return out, slack.ErrUnsupported
	}
	if a.State != "ACTIVE" || a.Generation != j.Generation {
		return out, ErrNeedsReauth
	}
	if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
		return out, e
	}
	ev := j.Envelope.Event
	if ev.Subtype != "" || ev.Hidden || ev.BotID != "" || ev.User == a.BotUserID || !slackID(ev.User) || !utf8.ValidString(ev.Text) || utf8.RuneCountInString(ev.Text) > maxInboundText {
		return out, ErrInvalidRequest
	}
	var owner string
	e = tx.QueryRow(ctx, `SELECT i.id::text,i.version,i.owner_user_id::text FROM openfox.slack_identities i JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid AND i.team_id=$2 AND i.slack_user_id=$3 AND i.active AND c.status='ACTIVE'`, a.ParentAppID, a.TeamID, ev.User).Scan(&out.IdentityID, &out.IdentityVersion, &owner)
	if errors.Is(e, pgx.ErrNoRows) {
		e = errSlackUnbound
	}
	if e != nil {
		return out, e
	}
	actor, e := slackActor(ctx, tx, org, collaboration.Actor{UserID: owner})
	if e != nil {
		return out, e
	}
	if actor.Role == policy.OrganizationRoleGuest {
		return out, ErrForbidden
	}
	out.Actor = slackInboundActor(actor)
	out.DM = ev.ChannelType == "im"
	out.Thread = ev.ThreadTS
	if out.Thread == "" && !out.DM {
		out.Thread = ev.TS
	}
	if out.Thread != "" && !slackTimestamp(out.Thread) {
		return out, ErrInvalidRequest
	}
	if !out.DM {
		e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_channels WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND channel_id=$2 AND generation=$3 AND enabled AND public_reply_consent AND disabled_at IS NULL`, a.ID, ev.Channel, a.Generation).Scan(&out.EpochID)
		if errors.Is(e, pgx.ErrNoRows) {
			e = ErrForbidden
		}
		if e != nil {
			return out, e
		}
	}
	e = tx.QueryRow(ctx, `SELECT id::text,conversation_id::text FROM openfox.slack_routes WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND generation=$2 AND channel_id=$3 AND thread_ts=$4 AND channel_epoch_id IS NOT DISTINCT FROM NULLIF($5,'')::uuid`, a.ID, a.Generation, ev.Channel, out.Thread, out.EpochID).Scan(&out.RouteID, &out.ConversationID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return out, e
	}
	mention := j.Envelope.Event.Type == "app_mention" || strings.Contains(ev.Text, "<@"+a.BotUserID+">")
	if !out.DM && !mention && out.RouteID == "" {
		return out, ErrForbidden
	}
	grant, e := w.Service.slackStoredToken(ctx, tx, org, a)
	if e != nil {
		return out, e
	}
	if !grant.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		return out, ErrNeedsReauth
	}
	out.Token = grant.AccessToken
	return out, nil
}
func (w *IMWorker) processSlackMessage(ctx context.Context, org string, j slackInboundJob) error {
	var t slackInboundTarget
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		t, e = w.prepareSlackMessage(ctx, tx, org, j)
		return e
	})
	if e != nil {
		return e
	}
	ev := j.Envelope.Event
	var user slack.User
	e = w.slackRead(ctx, org, t.App.ID, "users", "", func() error { var e error; user, e = w.Service.slackClient().UserInfo(ctx, t.Token, ev.User); return e })
	if e != nil {
		return e
	}
	if !slackOrdinaryUser(user, t.App.TeamID, ev.User) {
		return ErrForbidden
	}
	var channel slack.Conversation
	e = w.slackRead(ctx, org, t.App.ID, "conversation", ev.Channel, func() error {
		var e error
		channel, e = w.Service.slackClient().ConversationInfo(ctx, t.Token, ev.Channel)
		return e
	})
	if e != nil {
		return e
	}
	if channel.ID != ev.Channel || !slackOrdinaryConversation(channel, t.App.TeamID, ev.User, t.DM) {
		return ErrForbidden
	}
	if !t.DM {
		// Prove the invoking human is currently in this channel; bot membership is
		// separate and does not stand in for human authorization.
		cursor := ""
		seen := map[string]bool{}
		found := false
		for page := 0; page < 5; page++ {
			var members slack.MembersPage
			e = w.slackRead(ctx, org, t.App.ID, "members", ev.Channel, func() error {
				var e error
				members, e = w.Service.slackClient().ConversationMembers(ctx, t.Token, ev.Channel, cursor)
				return e
			})
			if e != nil {
				return e
			}
			for _, id := range members.Members {
				if id == ev.User {
					found = true
					break
				}
			}
			if found {
				break
			}
			if members.Next == "" {
				return ErrForbidden
			}
			if seen[members.Next] {
				return slack.ErrIncomplete
			}
			seen[members.Next] = true
			cursor = members.Next
		}
		if !found {
			return slack.ErrIncomplete
		}
	}
	history, limited := w.slackThreadContext(ctx, org, t, j)
	text := strings.TrimSpace(strings.ReplaceAll(ev.Text, "<@"+t.App.BotUserID+">", ""))
	if text == "" {
		return ErrInvalidRequest
	}
	body := composeInboundBody(text, history, limited)
	return w.inOrganizationAs(ctx, org, t.Actor.UserID, t.Actor.AuthorizationVersion, func(ctx context.Context, tx pgx.Tx) error { return w.postSlackMessage(ctx, tx, org, j, t, body) })
}
func (w *IMWorker) slackThreadContext(ctx context.Context, org string, t slackInboundTarget, j slackInboundJob) (string, bool) {
	// DM has the existing central conversation history. Only the explicitly
	// selected channel thread is fetched; never enumerate unrelated channels.
	if t.DM {
		return "", false
	}
	var messages []slack.Message
	cursor := ""
	seen := map[string]bool{}
	limited := false
	for page := 0; page < 2; page++ {
		var p slack.MessagesPage
		e := w.slackRead(ctx, org, t.App.ID, "replies", "", func() error {
			var e error
			p, e = w.Service.slackClient().Replies(ctx, t.Token, slack.HistoryInput{Channel: j.Envelope.Event.Channel, ThreadTS: t.Thread, Cursor: cursor, Limit: 15, Latest: j.Envelope.Event.TS, Inclusive: false})
			return e
		})
		if e != nil {
			limited = true
			break
		}
		messages = append(messages, p.Messages...)
		limited = p.Limited || p.HasMore || p.Next != ""
		if p.Next == "" {
			break
		}
		if seen[p.Next] {
			limited = true
			break
		}
		seen[p.Next] = true
		cursor = p.Next
	}
	sort.SliceStable(messages, func(i, j int) bool {
		a, _ := slackTimestampTime(messages[i].TS)
		b, _ := slackTimestampTime(messages[j].TS)
		return a.Before(b)
	})
	triggerTime, _ := slackTimestampTime(j.Envelope.Event.TS)
	lines := []string{}
	dedup := map[string]bool{}
	for _, m := range messages {
		at, e := slackTimestampTime(m.TS)
		if e != nil || !at.Before(triggerTime) {
			continue
		}
		if m.TS == j.Envelope.Event.TS || dedup[m.TS] || m.Subtype != "" || m.Text == "" || m.BotID != "" || m.User == t.App.BotUserID || !utf8.ValidString(m.Text) {
			continue
		}
		dedup[m.TS] = true
		lines = append(lines, "["+m.User+"] "+limitRunes(m.Text, maxContextLineRunes))
	}
	if len(lines) > maxContextLines {
		lines = lines[len(lines)-maxContextLines:]
		limited = true
	}
	for len(lines) > 0 && utf8.RuneCountInString(strings.Join(lines, "\n")) > maxContextRunes {
		lines = lines[1:]
		limited = true
	}
	if limited && len(lines) > 0 {
		note := "（仅包含可读取范围内的部分线程记录）"
		for len(lines) > 0 && utf8.RuneCountInString(strings.Join(lines, "\n"))+1+utf8.RuneCountInString(note) > maxContextRunes {
			lines = lines[1:]
		}
		lines = append(lines, note)
	}
	return strings.Join(lines, "\n"), limited
}
func (w *IMWorker) postSlackMessage(ctx context.Context, tx pgx.Tx, org string, j slackInboundJob, t slackInboundTarget, body string) error {
	current, e := w.prepareSlackMessage(ctx, tx, org, j)
	if e != nil {
		return e
	}
	if current.Actor.UserID != t.Actor.UserID || current.Actor.AuthorizationVersion != t.Actor.AuthorizationVersion || current.IdentityID != t.IdentityID || current.IdentityVersion != t.IdentityVersion || current.EpochID != t.EpochID {
		return ErrForbidden
	}
	var lease bool
	e = tx.QueryRow(ctx, `SELECT state='PROCESSING' AND lease_owner=$2 AND lease_expires_at>clock_timestamp() FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, j.ID, j.Lease).Scan(&lease)
	if e != nil {
		return e
	}
	if !lease {
		return ErrVersionConflict
	}
	route, conversation := current.RouteID, current.ConversationID
	if route == "" {
		route, e = membership.NewID()
		if e != nil {
			return e
		}
		kind := "group"
		if t.DM {
			kind = "p2p"
		}
		decision := inboundDecision{member: t.Actor, agentID: current.App.AgentID, spaceID: current.App.SpaceID, provider: ProviderSlack}
		var reject *inboundReject
		conversation, reject, e = w.createBinding(ctx, tx, org, inboundItem{BotID: current.App.BotID, ChatID: route, ChatType: kind}, decision)
		if e != nil {
			return e
		}
		if reject != nil {
			return ErrForbidden
		}
		_, e = tx.Exec(ctx, `INSERT INTO openfox.slack_routes(organization_id,id,app_id,bot_id,generation,channel_id,thread_ts,channel_epoch_id,identity_id,identity_version,conversation_id) VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4,$5,$6,NULLIF($7,'')::uuid,$8::uuid,$9,$10::uuid)`, route, current.App.ID, current.App.BotID, current.App.Generation, j.Envelope.Event.Channel, current.Thread, current.EpochID, current.IdentityID, current.IdentityVersion, conversation)
		if e != nil {
			return e
		}
	}
	if e = conversations.LockSlackInvocation(ctx, tx, conversation, t.Actor.UserID); e != nil {
		return e
	}
	service := conversations.Service{}
	if !t.DM {
		if e = service.AddIMParticipant(ctx, tx, org, conversation, t.Actor); e != nil {
			return e
		}
	}
	clientID := "slack:" + j.ID
	var messageID string
	e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.messages WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND client_message_id=$2`, conversation, clientID).Scan(&messageID)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	if messageID == "" {
		id, e := membership.NewID()
		if e != nil {
			return e
		}
		m, e := service.PostMessage(ctx, tx, org, conversation, t.Actor, conversations.MessageInput{ID: id, Body: body, ClientMessageID: clientID, Mentions: []conversations.Mention{{AgentID: current.App.AgentID}}})
		if e != nil {
			return e
		}
		messageID = m.ID
	}
	if e = recordIMAudit(ctx, tx, org, t.Actor.UserID, t.Actor.AuthorizationVersion, "integration.slack_message_received", "conversation_message", messageID, current.App.SpaceID, map[string]string{"source": "slack"}); e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.slack_events SET state='DONE',status_reason='',payload='{}',lease_owner='',lease_expires_at=NULL,next_attempt_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_owner=$2`, j.ID, j.Lease)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return nil
}
func (w *IMWorker) failSlackEvent(ctx context.Context, org string, j slackInboundJob, cause error) error {
	state, reason := "PENDING", slackFailure(cause)
	var next *time.Time
	if errors.Is(cause, ErrForbidden) || errors.Is(cause, ErrNeedsReauth) || errors.Is(cause, slack.ErrUnsupported) || errors.Is(cause, ErrInvalidRequest) || errors.Is(cause, conversations.ErrForbidden) || errors.Is(cause, conversations.ErrNotFound) {
		state = "IGNORED"
	} else if j.Attempts >= 5 {
		state = "FAILED"
	} else {
		v := time.Now().Add(time.Duration(j.Attempts) * 30 * time.Second)
		next = &v
	}
	var upstream *slack.Error
	if errors.As(cause, &upstream) {
		if errors.Is(cause, slack.ErrRateLimited) && upstream.RetryAfter > 0 {
			v := time.Now().Add(upstream.RetryAfter)
			next = &v
			state = "PENDING"
		} else if errors.Is(cause, slack.ErrInvalid) {
			state = "FAILED"
			next = nil
		}
	}
	if errors.Is(cause, ErrVersionConflict) {
		return nil
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_events SET state=$3,status_reason=$4,next_attempt_at=$5,payload=CASE WHEN $3 IN ('FAILED','IGNORED') THEN '{}'::jsonb ELSE payload END,lease_owner='',lease_expires_at=NULL,finished_at=CASE WHEN $3 IN ('FAILED','IGNORED') THEN clock_timestamp() ELSE NULL END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_owner=$2`, j.ID, j.Lease, state, reason, next)
		return e
	})
}
