package integrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/integrations/slack"
	"openfox/internal/membership"
)

type slackPreviewTask struct{ URL, SpaceID, TaskID, EpochID, Title, Status string }
type slackSpecialBody struct {
	User, ActorID, Text string
	Tasks               []slackPreviewTask
}
type slackSpecialWork struct {
	ID, AppID, Kind, Channel, Thread, IdentityID, Marker, Lease string
	Generation, IdentityVersion, TokenVersion                   uint64
	Attempts                                                    int
	Body                                                        slackSpecialBody
	Hash                                                        []byte
}

func slackTaskURL(base, raw, org string) (string, string, bool) {
	b, e := url.Parse(strings.TrimRight(base, "/"))
	if e != nil {
		return "", "", false
	}
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawPath != "" || !strings.EqualFold(u.Host, b.Host) {
		return "", "", false
	}
	prefix := strings.TrimRight(b.Path, "/") + "/o/" + strings.ToLower(org) + "/spaces/"
	if !strings.HasPrefix(u.Path, prefix) || !strings.HasSuffix(u.Path, "/tasks") {
		return "", "", false
	}
	space := strings.TrimSuffix(strings.TrimPrefix(u.Path, prefix), "/tasks")
	if !membership.ValidateUUID(space) {
		return "", "", false
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q) != 1 || len(q["task"]) != 1 || len(q.Get("task")) > 64 || q.Get("task") == "" {
		return "", "", false
	}
	return space, q.Get("task"), true
}
func (w *IMWorker) processSlackSpecialEvents(ctx context.Context, org string) error {
	nonce, e := randomBase64URL(12)
	if e != nil {
		return e
	}
	lease := w.instance() + ":" + nonce
	var jobs []slackInboundJob
	e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `WITH due AS(SELECT e.id FROM openfox.slack_events e JOIN openfox.slack_apps a ON a.organization_id=e.organization_id AND a.id=e.app_id WHERE e.organization_id=openfox_private.current_organization_id() AND ((e.state='PENDING' AND e.next_attempt_at<=clock_timestamp()) OR (e.state='PROCESSING' AND e.lease_expires_at<=clock_timestamp())) AND (e.event_type IN ('link_shared','app_uninstalled','tokens_revoked') OR (e.event_type='message' AND a.kind='CENTRAL' AND COALESCE(e.payload->'event'->>'bot_id','')='' AND COALESCE(e.payload->'event'->>'subtype','')='') OR (COALESCE(e.payload->'event'->>'subtype','')<>'' AND e.payload->'event'->'metadata' IS NULL)) ORDER BY e.received_at,e.id LIMIT 10 FOR UPDATE OF e SKIP LOCKED) UPDATE openfox.slack_events e SET state='PROCESSING',lease_owner=$1,lease_expires_at=clock_timestamp()+interval '60 seconds',attempts=attempts+1 FROM due WHERE e.organization_id=openfox_private.current_organization_id() AND e.id=due.id RETURNING e.id::text,e.app_id::text,e.generation,e.attempts,e.payload`, lease)
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
		e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error { return w.materializeSlackSpecial(ctx, tx, org, j) })
		if e != nil {
			if e = w.failSlackEvent(ctx, org, j, e); e != nil {
				all = append(all, e)
			}
		}
	}
	return errors.Join(all...)
}
func (w *IMWorker) materializeSlackSpecial(ctx context.Context, tx pgx.Tx, org string, j slackInboundJob) error {
	a, e := lockSlackApp(ctx, tx, j.AppID)
	if e != nil {
		return e
	}
	if a.State != "ACTIVE" || a.Generation != j.Generation {
		return ErrNeedsReauth
	}
	if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
		return e
	}
	var lease bool
	if e = tx.QueryRow(ctx, `SELECT lease_owner=$2 AND lease_expires_at>clock_timestamp() FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, j.ID, j.Lease).Scan(&lease); e != nil {
		return e
	}
	if !lease {
		return ErrVersionConflict
	}
	ev := j.Envelope.Event
	if ev.Type == "app_uninstalled" || ev.Type == "tokens_revoked" {
		// OAuth token revocation does not imply a refresh grant is reusable. Fence
		// business execution and require a fresh interactive authorization.
		affected := ev.Type == "app_uninstalled"
		if ev.Type == "tokens_revoked" {
			for _, id := range ev.Tokens.Bot {
				if id == a.BotUserID {
					affected = true
				}
			}
		}
		if affected {
			if _, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET state='NEEDS_REAUTH',status_reason='platform_authorization_revoked',generation=generation+1,token_version=token_version+1,version=version+1,activated_at=NULL,refresh_lease_owner='',refresh_lease_expires_at=NULL,next_attempt_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='platform_authorization_revoked',credentials_sealed=NULL,credentials_key_id='',version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID); e != nil {
				return e
			}
		}
		return finishSlackSpecialEvent(ctx, tx, j, "DONE", "")
	}
	if ev.Subtype != "" || ev.Hidden || ev.BotID != "" || !slackID(ev.User) {
		return finishSlackSpecialEvent(ctx, tx, j, "IGNORED", "unsupported_event")
	}
	kind := "NOTICE"
	body := slackSpecialBody{User: ev.User, Text: "请在 OpenFox 打开原任务，或前往对应 Agent App 继续对话。 " + w.bindURL(org, ProviderSlack)}
	var identity string
	var identityVersion uint64
	if ev.Type == "link_shared" {
		kind = "UNFURL"
		body.Text = ""
		if a.Kind != "CENTRAL" || !slackID(ev.Channel) || !slackTimestamp(ev.TS) || len(ev.Links) < 1 || len(ev.Links) > 5 {
			return finishSlackSpecialEvent(ctx, tx, j, "IGNORED", "unsupported_preview")
		}
		e = tx.QueryRow(ctx, `SELECT i.id::text,i.version,i.owner_user_id::text FROM openfox.slack_identities i JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid AND i.team_id=$2 AND i.slack_user_id=$3 AND i.active AND c.status='ACTIVE'`, a.ID, a.TeamID, ev.User).Scan(&identity, &identityVersion, &body.ActorID)
		if errors.Is(e, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if e != nil {
			return e
		}
		actor, e := slackActor(ctx, tx, org, collaboration.Actor{UserID: body.ActorID})
		if e != nil {
			return e
		}
		cs := collaboration.Service{}
		seen := map[string]bool{}
		for _, link := range ev.Links {
			space, display, ok := slackTaskURL(w.Service.PublicURL, link.URL, org)
			if !ok || seen[link.URL] {
				continue
			}
			seen[link.URL] = true
			actual, taskID, e := cs.FindTaskByDisplayID(ctx, tx, org, display, actor)
			if errors.Is(e, collaboration.ErrTaskNotFound) || errors.Is(e, collaboration.ErrActionForbidden) {
				continue
			}
			if e != nil {
				return e
			}
			if actual != space {
				continue
			}
			readable, e := (conversations.Service{}).CanReadSpace(ctx, tx, org, slackInboundActor(actor), space)
			if e != nil {
				return e
			}
			if !readable {
				continue
			}
			task, e := cs.GetTask(ctx, tx, org, space, taskID, actor)
			if errors.Is(e, collaboration.ErrTaskNotFound) || errors.Is(e, collaboration.ErrActionForbidden) {
				continue
			}
			if e != nil {
				return e
			}
			var epoch string
			e = tx.QueryRow(ctx, `SELECT c.id::text FROM openfox.slack_channels c JOIN openfox.slack_apps d ON d.organization_id=c.organization_id AND d.id=c.app_id WHERE c.organization_id=openfox_private.current_organization_id() AND d.parent_app_id=$1::uuid AND d.parent_generation=$2 AND d.state='ACTIVE' AND c.generation=d.generation AND c.space_id=$3::uuid AND c.channel_id=$4 AND c.enabled AND c.public_reply_consent AND c.disabled_at IS NULL ORDER BY c.id LIMIT 1`, a.ID, a.Generation, space, ev.Channel).Scan(&epoch)
			if errors.Is(e, pgx.ErrNoRows) {
				continue
			}
			if e != nil {
				return e
			}
			body.Tasks = append(body.Tasks, slackPreviewTask{URL: link.URL, SpaceID: space, TaskID: taskID, EpochID: epoch, Title: limitRunes(task.DisplayID+" · "+task.Title, 300), Status: limitRunes(task.StatusName, 1000)})
		}
		if len(body.Tasks) == 0 {
			return finishSlackSpecialEvent(ctx, tx, j, "IGNORED", "preview_not_authorized")
		}
	} else if ev.Type != "message" || ev.ChannelType != "im" || !slackID(ev.Channel) || !slackTimestamp(ev.TS) {
		return finishSlackSpecialEvent(ctx, tx, j, "IGNORED", "unsupported_event")
	}
	raw, e := json.Marshal(body)
	if e != nil {
		return e
	}
	hash := sha256.Sum256(raw)
	marker, e := randomBase64URL(24)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.slack_deliveries(organization_id,app_id,source_kind,source_id,part,generation,token_version,identity_id,identity_version,channel_id,thread_ts,body,request_hash,marker) SELECT organization_id,id,$2,$3::uuid,1,generation,token_version,NULLIF($4,'')::uuid,NULLIF($5,0),$6,$7,$8::jsonb,$9,$10 FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid ON CONFLICT(organization_id,source_kind,source_id,part) DO NOTHING`, a.ID, kind, j.ID, identity, identityVersion, ev.Channel, ev.TS, raw, hash[:], marker)
	if e != nil {
		return e
	}
	return finishSlackSpecialEvent(ctx, tx, j, "DONE", "")
}
func finishSlackSpecialEvent(ctx context.Context, tx pgx.Tx, j slackInboundJob, state, reason string) error {
	tag, e := tx.Exec(ctx, `UPDATE openfox.slack_events SET state=$3,status_reason=$4,payload='{}',lease_owner='',lease_expires_at=NULL,next_attempt_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_owner=$2`, j.ID, j.Lease, state, reason)
	if e == nil && tag.RowsAffected() != 1 {
		return ErrVersionConflict
	}
	return e
}

func (w *IMWorker) processSlackSpecialDeliveries(ctx context.Context, org string) error {
	nonce, e := randomBase64URL(12)
	if e != nil {
		return e
	}
	lease := w.instance() + ":" + nonce
	var jobs []slackSpecialWork
	e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET state='UNKNOWN',status_reason='send_result_unknown',next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND source_kind IN ('NOTICE','UNFURL') AND state='SENDING' AND lease_expires_at<=clock_timestamp()`); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `WITH due AS(SELECT id FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND source_kind IN ('NOTICE','UNFURL') AND state='PENDING' AND next_attempt_at<=clock_timestamp() ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) UPDATE openfox.slack_deliveries d SET state='SENDING',lease_owner=$1,lease_expires_at=clock_timestamp()+interval '60 seconds',attempts=attempts+1,version=version+1 FROM due WHERE d.organization_id=openfox_private.current_organization_id() AND d.id=due.id RETURNING d.id::text,d.app_id::text,d.source_kind,d.generation,d.token_version,COALESCE(d.identity_id::text,''),COALESCE(d.identity_version,0),d.channel_id,d.thread_ts,d.marker,d.body,d.attempts,d.request_hash`, lease)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			j := slackSpecialWork{Lease: lease}
			var raw []byte
			if e = rows.Scan(&j.ID, &j.AppID, &j.Kind, &j.Generation, &j.TokenVersion, &j.IdentityID, &j.IdentityVersion, &j.Channel, &j.Thread, &j.Marker, &raw, &j.Attempts, &j.Hash); e != nil {
				return e
			}
			if json.Unmarshal(raw, &j.Body) != nil {
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
		if e = w.sendSlackSpecial(ctx, org, j); e != nil {
			all = append(all, e)
		}
	}
	return errors.Join(all...)
}
func (w *IMWorker) slackSpecialGate(ctx context.Context, tx pgx.Tx, org string, j slackSpecialWork) (SlackApp, string, error) {
	a, e := lockSlackApp(ctx, tx, j.AppID)
	if e != nil {
		return a, "", e
	}
	if a.State != "ACTIVE" || a.Generation != j.Generation || (j.Kind == "UNFURL" && a.Kind != "CENTRAL") {
		return a, "", ErrNeedsReauth
	}
	if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
		return a, "", e
	}
	var live bool
	e = tx.QueryRow(ctx, `SELECT state='SENDING' AND lease_owner=$2 AND lease_expires_at>clock_timestamp() AND generation=$3 AND marker=$4 AND request_hash=$5 FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR UPDATE`, j.ID, j.Lease, j.Generation, j.Marker, j.Hash).Scan(&live)
	if e != nil {
		return a, "", e
	}
	if !live {
		return a, "", ErrVersionConflict
	}
	grant, e := w.Service.slackStoredToken(ctx, tx, org, a)
	if e != nil {
		return a, "", e
	}
	if !grant.ExpiresAt.After(time.Now().Add(30 * time.Second)) {
		return a, "", ErrNeedsReauth
	}
	if j.Kind == "UNFURL" {
		actor, e := slackActor(ctx, tx, org, collaboration.Actor{UserID: j.Body.ActorID})
		if e != nil {
			return a, "", e
		}
		tasks := append([]slackPreviewTask(nil), j.Body.Tasks...)
		sort.Slice(tasks, func(i, k int) bool { return tasks[i].EpochID < tasks[k].EpochID })
		for _, task := range tasks {
			var allowed bool
			e = tx.QueryRow(ctx, `SELECT c.enabled AND c.public_reply_consent AND c.disabled_at IS NULL AND c.channel_id=$2 AND c.space_id=$3::uuid AND d.state='ACTIVE' AND c.generation=d.generation AND d.parent_generation=$4 AND d.parent_app_id=$5::uuid FROM openfox.slack_channels c JOIN openfox.slack_apps d ON d.organization_id=c.organization_id AND d.id=c.app_id WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid FOR SHARE OF c`, task.EpochID, j.Channel, task.SpaceID, a.Generation, a.ID).Scan(&allowed)
			if e != nil {
				return a, "", e
			}
			if !allowed {
				return a, "", ErrForbidden
			}
			readable, e := (conversations.Service{}).CanReadSpace(ctx, tx, org, slackInboundActor(actor), task.SpaceID)
			if e != nil {
				return a, "", e
			}
			if !readable {
				return a, "", ErrForbidden
			}
			if _, e = (collaboration.Service{}).GetTask(ctx, tx, org, task.SpaceID, task.TaskID, actor); e != nil {
				return a, "", e
			}
		}
		var valid bool
		e = tx.QueryRow(ctx, `SELECT i.active AND i.version=$2 AND i.owner_user_id=$3::uuid AND i.slack_user_id=$4 AND i.team_id=$5 AND i.parent_app_id=$6::uuid AND c.status='ACTIVE' FROM openfox.slack_identities i JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE i.organization_id=openfox_private.current_organization_id() AND i.id=$1::uuid FOR SHARE OF i`, j.IdentityID, j.IdentityVersion, actor.UserID, j.Body.User, a.TeamID, a.ID).Scan(&valid)
		if e != nil {
			return a, "", e
		}
		if !valid {
			return a, "", ErrForbidden
		}
	}
	return a, grant.AccessToken, nil
}
func (w *IMWorker) sendSlackSpecial(ctx context.Context, org string, j slackSpecialWork) error {
	jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var a SlackApp
	var token string
	e := w.inOrganization(jobCtx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		a, token, e = w.slackSpecialGate(ctx, tx, org, j)
		return e
	})
	if e == nil {
		var u slack.User
		e = w.slackRead(jobCtx, org, a.ID, "users", "", func() error {
			var e error
			u, e = w.Service.slackClient().UserInfo(jobCtx, token, j.Body.User)
			return e
		})
		if e == nil && !slackOrdinaryUser(u, a.TeamID, j.Body.User) {
			e = ErrForbidden
		}
	}
	if e == nil {
		var c slack.Conversation
		e = w.slackRead(jobCtx, org, a.ID, "conversation", j.Channel, func() error {
			var e error
			c, e = w.Service.slackClient().ConversationInfo(jobCtx, token, j.Channel)
			return e
		})
		if e == nil && (c.ID != j.Channel || !slackOrdinaryConversation(c, a.TeamID, j.Body.User, j.Kind == "NOTICE")) {
			e = ErrForbidden
		}
	}
	if e == nil && j.Kind == "UNFURL" {
		found := false
		cursor := ""
		seen := map[string]bool{}
		for page := 0; page < 5; page++ {
			var p slack.MembersPage
			e = w.slackRead(jobCtx, org, a.ID, "members", j.Channel, func() error {
				var e error
				p, e = w.Service.slackClient().ConversationMembers(jobCtx, token, j.Channel, cursor)
				return e
			})
			if e != nil {
				break
			}
			for _, id := range p.Members {
				if id == j.Body.User {
					found = true
				}
			}
			if found || p.Next == "" {
				break
			}
			if seen[p.Next] {
				e = slack.ErrIncomplete
				break
			}
			seen[p.Next] = true
			cursor = p.Next
		}
		if e == nil && !found {
			e = ErrForbidden
		}
	}
	if e == nil {
		e = w.inOrganization(jobCtx, org, func(ctx context.Context, tx pgx.Tx) error {
			var e error
			a, token, e = w.slackSpecialGate(ctx, tx, org, j)
			return e
		})
	}
	sent := false
	remote := ""
	if e == nil {
		method := "post"
		if j.Kind == "UNFURL" {
			method = "unfurl"
		}
		e = w.slackRead(jobCtx, org, a.ID, method, j.Channel, func() error {
			sent = true
			if j.Kind == "UNFURL" {
				previews := map[string]slack.UnfurlPreview{}
				for _, t := range j.Body.Tasks {
					previews[t.URL] = slack.UnfurlPreview{Title: t.Title, Text: t.Status}
				}
				return w.Service.slackClient().Unfurl(jobCtx, token, j.Channel, j.Thread, previews)
			} else {
				var receipt slack.MessageReceipt
				receipt, e = w.Service.slackClient().PostMessage(jobCtx, token, slack.PostMessageInput{Channel: j.Channel, ThreadTS: j.Thread, Text: j.Body.Text, Metadata: slackSpecialMetadata(j.Marker, j.Body.Text)})
				remote = receipt.TS
			}
			return e
		})
	}
	token = ""
	state, reason := "DONE", ""
	var next *time.Time
	if e != nil {
		state, reason = "FAILED", slackFailure(e)
		if errors.Is(e, ErrForbidden) || errors.Is(e, ErrNeedsReauth) || errors.Is(e, collaboration.ErrTaskNotFound) || errors.Is(e, collaboration.ErrActionForbidden) {
			state = "SKIPPED"
		}
		if sent && (errors.Is(e, slack.ErrUnknown) || errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded)) {
			state = "UNKNOWN"
		}
		var rate *slack.Error
		if (j.Attempts < 5 || !sent) && errors.Is(e, slack.ErrRateLimited) && errors.As(e, &rate) && rate.RetryAfter > 0 {
			state = "PENDING"
			v := time.Now().Add(rate.RetryAfter)
			next = &v
		}
	}
	finishCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	return w.inOrganization(finishCtx, org, func(ctx context.Context, tx pgx.Tx) error {
		if sent && e == nil {
			// A disconnect may revoke the lease while the known POST is in flight.
			// Retain its positive receipt without reactivating any other work.
			_, err := tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET state='DONE',status_reason='',remote_ts=$5,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,version=version+1,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND marker=$3 AND request_hash=$4 AND (lease_owner=$6 OR state IN ('UNKNOWN','SKIPPED'))`, j.ID, j.Generation, j.Marker, j.Hash, remote, j.Lease)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET state=$3,status_reason=$4,remote_ts=$5,next_attempt_at=$6,attempts=attempts-CASE WHEN $7 THEN 1 ELSE 0 END,lease_owner='',lease_expires_at=NULL,version=version+1,finished_at=CASE WHEN $3='PENDING' THEN NULL ELSE clock_timestamp() END WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND lease_owner=$2 AND state='SENDING'`, j.ID, j.Lease, state, reason, remote, next, !sent && state == "PENDING")
		return err
	})
}

func slackSpecialMetadata(marker, text string) *slack.MessageMetadata {
	sum := sha256.Sum256([]byte(text))
	return &slack.MessageMetadata{EventType: "openfox_delivery", EventPayload: map[string]string{"delivery_id": marker, "part": "1", "body_hash": hex.EncodeToString(sum[:])}}
}

// A positive signed own-bot receipt can settle a NOTICE. Unfurl has no matching
// message receipt and an unknown outcome therefore remains unknown.
func (w *IMWorker) processSlackSpecialReceipt(ctx context.Context, org, appID string, generation uint64, ev SlackEvent) error {
	if ev.Hidden || ev.Type != "message" || (ev.Subtype != "" && ev.Subtype != "bot_message") || ev.Metadata == nil || ev.Metadata.EventType != "openfox_delivery" || len(ev.Metadata.EventPayload) != 3 || !slackTimestamp(ev.TS) {
		return nil
	}
	marker := ev.Metadata.EventPayload["delivery_id"]
	if len(marker) < 16 || len(marker) > 128 {
		return nil
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, e := lockSlackApp(ctx, tx, appID)
		if e != nil {
			return e
		}
		if a.Generation != generation {
			return nil
		}
		var id, channel, thread, bot string
		var raw, hash []byte
		e = tx.QueryRow(ctx, `SELECT d.id::text,d.channel_id,d.thread_ts,d.body,d.request_hash,a.bot_external_id FROM openfox.slack_deliveries d JOIN openfox.slack_apps a ON a.organization_id=d.organization_id AND a.id=d.app_id WHERE d.organization_id=openfox_private.current_organization_id() AND d.app_id=$1::uuid AND d.generation=$2 AND d.marker=$3 AND d.source_kind='NOTICE' AND d.attempts>0 AND d.state<>'DONE' FOR UPDATE OF d`, appID, generation, marker).Scan(&id, &channel, &thread, &raw, &hash, &bot)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		var body slackSpecialBody
		if json.Unmarshal(raw, &body) != nil {
			return ErrInvalidRequest
		}
		canonical, _ := json.Marshal(body)
		sum := sha256.Sum256(canonical)
		if !bytes.Equal(sum[:], hash) || bot == "" || ev.BotID != bot || ev.AppID != a.ExternalAppID || (ev.Team != "" && ev.Team != a.TeamID) || (ev.User != "" && ev.User != a.BotUserID) || ev.Channel != channel || ev.ThreadTS != thread {
			return nil
		}
		expected := slackSpecialMetadata(marker, body.Text)
		if ev.Metadata.EventPayload["part"] != "1" || ev.Metadata.EventPayload["body_hash"] != expected.EventPayload["body_hash"] {
			return nil
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET state='DONE',status_reason='event_receipt',remote_ts=$2,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,version=version+1,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, ev.TS)
		return e
	})
}
