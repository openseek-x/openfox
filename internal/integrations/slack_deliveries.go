package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/slack"
)

type slackDeliveryRow struct {
	ID, AppID, Kind, SourceID, EpochID, IdentityID, ChannelID, ThreadTS, Marker, State, Reason, RemoteTS, Lease string
	Part, Attempts                                                                                              int
	Version, Generation, TokenVersion, IdentityVersion                                                          uint64
	Hash                                                                                                        []byte
	Body                                                                                                        slackDeliveryBody
	CreatedAt                                                                                                   time.Time
}

const slackDeliveryColumns = `d.id::text,d.app_id::text,d.source_kind,d.source_id::text,d.part,d.version,d.generation,d.token_version,
 COALESCE(d.channel_epoch_id::text,''),COALESCE(d.identity_id::text,''),COALESCE(d.identity_version,0),d.channel_id,d.thread_ts,d.body,d.request_hash,d.marker,d.state,d.status_reason,d.remote_ts,d.attempts,d.lease_owner,d.created_at`

func scanSlackDelivery(row pgx.Row) (slackDeliveryRow, error) {
	var d slackDeliveryRow
	var raw []byte
	err := row.Scan(&d.ID, &d.AppID, &d.Kind, &d.SourceID, &d.Part, &d.Version, &d.Generation, &d.TokenVersion, &d.EpochID, &d.IdentityID, &d.IdentityVersion, &d.ChannelID, &d.ThreadTS, &raw, &d.Hash, &d.Marker, &d.State, &d.Reason, &d.RemoteTS, &d.Attempts, &d.Lease, &d.CreatedAt)
	if err == nil {
		err = json.Unmarshal(raw, &d.Body)
	}
	return d, err
}
func loadSlackDelivery(ctx context.Context, tx pgx.Tx, id string, lock bool) (slackDeliveryRow, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF d"
	}
	return scanSlackDelivery(tx.QueryRow(ctx, `SELECT `+slackDeliveryColumns+` FROM openfox.slack_deliveries d WHERE d.organization_id=openfox_private.current_organization_id() AND d.id=$1::uuid`+suffix, id))
}
func slackParentTable(kind string) string {
	if kind == "REPLY" {
		return "openfox.im_reply_outbox"
	}
	if kind == "NOTIFICATION" {
		return "openfox.im_notification_outbox"
	}
	return ""
}
func slackFinishParent(ctx context.Context, tx pgx.Tx, kind, id, state, reason, remote string) error {
	table := slackParentTable(kind)
	if table == "" {
		return ErrInvalidRequest
	}
	remoteSQL := ""
	args := []any{id, state, reason}
	if kind == "REPLY" {
		remoteSQL = ",remote_message_id=$4"
		args = append(args, remote)
	}
	_, err := tx.Exec(ctx, `UPDATE `+table+` SET status=$2,last_error=$3,lease_owner='',lease_expires_at=NULL,
 finished_at=CASE WHEN $2 IN ('DONE','FAILED','SKIPPED') THEN clock_timestamp() ELSE NULL END,updated_at=clock_timestamp()`+remoteSQL+`
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, args...)
	return err
}
func slackAggregateParent(ctx context.Context, tx pgx.Tx, d slackDeliveryRow) error {
	if slackParentTable(d.Kind) == "" {
		return nil
	}
	var unknown, failed, skipped, done bool
	var remote string
	if err := tx.QueryRow(ctx, `SELECT bool_or(state='UNKNOWN'),bool_or(state='FAILED'),bool_or(state='SKIPPED'),bool_and(state='DONE'),COALESCE(max(remote_ts) FILTER(WHERE state='DONE'),'')
 FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND source_kind=$1 AND source_id=$2::uuid`, d.Kind, d.SourceID).Scan(&unknown, &failed, &skipped, &done, &remote); err != nil {
		return err
	}
	state, reason := "SENDING", ""
	switch {
	case unknown:
		state, reason = "FAILED", "delivery_unknown"
	case failed:
		state, reason = "FAILED", "delivery_failed"
	case skipped:
		state, reason = "SKIPPED", "delivery_skipped"
	case done:
		state = "DONE"
	}
	return slackFinishParent(ctx, tx, d.Kind, d.SourceID, state, reason, remote)
}
func slackSetPart(ctx context.Context, tx pgx.Tx, d slackDeliveryRow, state, reason, remote string, next *time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET state=$2,status_reason=$3,remote_ts=$4,next_attempt_at=$5,
 lease_owner='',lease_expires_at=NULL,version=version+1,finished_at=CASE WHEN $2 IN ('PENDING','SENDING') THEN NULL ELSE clock_timestamp() END
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, d.ID, state, reason, remote, next)
	if err != nil {
		return err
	}
	return slackAggregateParent(ctx, tx, d)
}

type slackSendWork struct {
	Delivery              slackDeliveryRow
	App                   SlackApp
	Grant                 slack.TokenGrant
	UserID, BotExternalID string
}

func (slackSendWork) String() string   { return "slackSendWork([redacted])" }
func (slackSendWork) GoString() string { return "slackSendWork([redacted])" }

// Caller holds the installation lock before taking epoch/identity locks. Every
// source is re-read: neither the outbox nor the UI is an authorization grant.
func authorizeSlackDelivery(ctx context.Context, tx pgx.Tx, org string, a SlackApp, d slackDeliveryRow) (string, error) {
	if a.State != "ACTIVE" || a.Generation != d.Generation {
		return "", ErrForbidden
	}
	if err := slackParentCurrent(ctx, tx, a.ID); err != nil {
		return "", err
	}
	var owner, user, parent, team string
	var version uint64
	var allowed bool
	if d.Kind == "REPLY" {
		r, err := loadSlackReplySource(ctx, tx, d.SourceID)
		if err != nil {
			return "", err
		}
		if r.AppID != a.ID || r.EpochID != d.EpochID || r.IdentityID != d.IdentityID || r.IdentityVersion != d.IdentityVersion || r.ChannelID != d.ChannelID || r.ThreadTS != d.ThreadTS {
			return "", ErrForbidden
		}
		if err = authorizeSlackReply(ctx, tx, org, r); err != nil {
			return "", err
		}
		owner = r.ActorID
		parent = a.ParentAppID
	} else if d.Kind == "NOTIFICATION" {
		if a.Kind != "CENTRAL" || d.EpochID != "" || d.ThreadTS != "" {
			return "", ErrForbidden
		}
		n, err := loadSlackNotice(ctx, tx, d.SourceID)
		if err != nil {
			return "", err
		}
		if n.AppConnection != a.ConnectionID {
			return "", ErrForbidden
		}
		if err = authorizeSlackNotice(ctx, tx, org, n); err != nil {
			return "", err
		}
		owner = n.RecipientUserID
		parent = a.ID
	} else {
		return "", ErrInvalidRequest
	}
	err := tx.QueryRow(ctx, `SELECT i.slack_user_id,i.team_id,i.version,i.active AND c.status='ACTIVE' AND m.status='ACTIVE' AND m.role<>'GUEST'
 FROM openfox.slack_identities i JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(i.organization_id,i.connection_id)
 JOIN openfox.organization_members m ON (m.organization_id,m.user_id)=(i.organization_id,i.owner_user_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.id=$1::uuid AND i.owner_user_id=$2::uuid AND i.parent_app_id=$3::uuid FOR SHARE OF i`, d.IdentityID, owner, parent).Scan(&user, &team, &version, &allowed)
	if err != nil {
		return "", err
	}
	if !allowed || version != d.IdentityVersion || team != a.TeamID {
		return "", ErrForbidden
	}
	return user, nil
}

func (w *IMWorker) claimSlackDelivery(ctx context.Context, tx pgx.Tx, org, id string) (*slackSendWork, error) {
	d, err := loadSlackDelivery(ctx, tx, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a, err := lockSlackApp(ctx, tx, d.AppID)
	if err != nil {
		return nil, err
	}
	d, err = loadSlackDelivery(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if d.State == "SENDING" {
		var expired bool
		if err = tx.QueryRow(ctx, `SELECT lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp() FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id).Scan(&expired); err != nil {
			return nil, err
		}
		if expired {
			return nil, slackSetPart(ctx, tx, d, "UNKNOWN", "send_lease_expired", "", nil)
		}
		return nil, nil
	}
	if d.State != "PENDING" {
		return nil, nil
	}
	var ready bool
	if err = tx.QueryRow(ctx, `SELECT d.next_attempt_at<=clock_timestamp() AND (d.lease_expires_at IS NULL OR d.lease_expires_at<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM openfox.slack_deliveries x WHERE x.organization_id=d.organization_id AND x.app_id=d.app_id AND x.id<>d.id AND x.lease_expires_at>clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM openfox.slack_deliveries x WHERE x.organization_id=d.organization_id AND x.source_kind=d.source_kind AND x.source_id=d.source_id AND x.part<d.part AND x.state<>'DONE')
 AND NOT EXISTS(SELECT 1 FROM openfox.slack_rate_limits r WHERE r.organization_id=d.organization_id AND r.app_id=d.app_id AND r.not_before>clock_timestamp() AND (r.resource='' OR r.resource=d.channel_id))
 FROM openfox.slack_deliveries d WHERE d.organization_id=openfox_private.current_organization_id() AND d.id=$1::uuid`, id).Scan(&ready); err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	user, err := authorizeSlackDelivery(ctx, tx, org, a, d)
	if slackDenied(err) {
		return nil, slackSetPart(ctx, tx, d, "SKIPPED", "authorization_unavailable", "", nil)
	}
	if err != nil {
		return nil, err
	}
	if d.Attempts >= imOutboxMax {
		return nil, slackSetPart(ctx, tx, d, "FAILED", "attempt_limit", "", nil)
	}
	if !bytes.Equal(d.Hash, slackPartHash(slackPartRequest(d.ChannelID, d.ThreadTS, d.Marker, d.Part, d.Body))) {
		return nil, slackSetPart(ctx, tx, d, "FAILED", "payload_mismatch", "", nil)
	}
	grant, err := w.Service.slackStoredToken(ctx, tx, org, a)
	if err != nil {
		return nil, err
	}
	// Refresh is installation-owned. Do not start a POST with an expiring grant.
	if !grant.ExpiresAt.IsZero() && time.Until(grant.ExpiresAt) < time.Minute {
		next := time.Now().Add(30 * time.Second)
		return nil, slackSetPart(ctx, tx, d, "PENDING", "awaiting_token_refresh", "", &next)
	}
	var botID string
	if err = tx.QueryRow(ctx, `SELECT token_version,bot_external_id FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID).Scan(&d.TokenVersion, &botID); err != nil {
		return nil, err
	}
	lease, err := randomBase64URL(32)
	if err != nil {
		return nil, err
	}
	state := "SENDING"
	if d.ChannelID == "" {
		state = "PENDING"
	}
	err = tx.QueryRow(ctx, `UPDATE openfox.slack_deliveries SET state=$2,lease_owner=$3,lease_expires_at=clock_timestamp()+interval '60 seconds',attempts=attempts+1,token_version=$4,version=version+1,status_reason=''
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid RETURNING version,attempts`, id, state, lease, d.TokenVersion).Scan(&d.Version, &d.Attempts)
	if err != nil {
		return nil, err
	}
	d.Lease, d.State = lease, state
	return &slackSendWork{Delivery: d, App: a, Grant: grant, UserID: user, BotExternalID: botID}, nil
}
func (w *IMWorker) processSlackDeliveries(ctx context.Context, org string) error {
	var ids []string
	if err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM openfox.slack_deliveries WHERE organization_id=openfox_private.current_organization_id()
 AND source_kind IN ('REPLY','NOTIFICATION') AND ((state='PENDING' AND next_attempt_at<=clock_timestamp() AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp())
 AND NOT EXISTS(SELECT 1 FROM openfox.slack_deliveries x WHERE x.organization_id=openfox.slack_deliveries.organization_id AND x.source_kind=openfox.slack_deliveries.source_kind AND x.source_id=openfox.slack_deliveries.source_id AND x.part<openfox.slack_deliveries.part AND x.state<>'DONE')
 AND NOT EXISTS(SELECT 1 FROM openfox.slack_rate_limits r WHERE r.organization_id=openfox.slack_deliveries.organization_id AND r.app_id=openfox.slack_deliveries.app_id AND r.not_before>clock_timestamp() AND (r.resource='' OR r.resource=openfox.slack_deliveries.channel_id))) OR (state='SENDING' AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp()))) ORDER BY created_at,part,id LIMIT 20`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	}); err != nil {
		return err
	}
	for _, id := range ids {
		var work *slackSendWork
		if err := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			work, err = w.claimSlackDelivery(ctx, tx, org, id)
			return err
		}); err != nil {
			return err
		}
		if work == nil {
			continue
		}
		if err := w.sendSlackDelivery(ctx, org, *work); err != nil {
			return err
		}
	}
	return nil
}

type slackSendResult struct {
	Channel, TS, Method string
	Posted              bool
	Err                 error
}

func (w *IMWorker) sendSlackDelivery(ctx context.Context, org string, work slackSendWork) error {
	callCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	client := w.Service.slackClient()
	d := work.Delivery
	result := slackSendResult{Method: "users"}
	user, err := client.UserInfo(callCtx, work.Grant.AccessToken, work.UserID)
	if err == nil && !slackOrdinaryUser(user, work.App.TeamID, work.UserID) {
		err = slack.ErrForbidden
	}
	if err == nil && d.ChannelID == "" {
		result.Method = "open_dm"
		var c slack.Conversation
		c, err = client.OpenDM(callCtx, work.Grant.AccessToken, work.UserID)
		if err == nil && !slackOrdinaryConversation(c, work.App.TeamID, work.UserID, true) {
			err = slack.ErrForbidden
		}
		if err == nil {
			result.Channel = c.ID
		}
	} else if err == nil {
		result.Method = "conversation"
		var c slack.Conversation
		c, err = client.ConversationInfo(callCtx, work.Grant.AccessToken, d.ChannelID)
		if err == nil && !slackOrdinaryConversation(c, work.App.TeamID, work.UserID, d.EpochID == "") {
			err = slack.ErrForbidden
		}
		if err == nil && d.EpochID != "" {
			result.Method = "members"
			found := false
			cursor := ""
			for page := 0; page < 5; page++ {
				var p slack.MembersPage
				p, err = client.ConversationMembers(callCtx, work.Grant.AccessToken, d.ChannelID, cursor)
				if err != nil {
					break
				}
				for _, id := range p.Members {
					if id == work.UserID {
						found = true
					}
				}
				if found || p.Next == "" {
					break
				}
				cursor = p.Next
			}
			if err == nil && !found {
				err = slack.ErrForbidden
			}
		}
		if err == nil {
			err = w.inOrganization(callCtx, org, func(ctx context.Context, tx pgx.Tx) error {
				a, e := lockSlackApp(ctx, tx, d.AppID)
				if e != nil {
					return e
				}
				current, e := loadSlackDelivery(ctx, tx, d.ID, true)
				if e != nil {
					return e
				}
				if current.State != "SENDING" || current.Lease != d.Lease {
					return ErrVersionConflict
				}
				var valid bool
				if e = tx.QueryRow(ctx, `SELECT d.lease_expires_at>clock_timestamp() AND a.token_version=$2 FROM openfox.slack_deliveries d JOIN openfox.slack_apps a ON (a.organization_id,a.id)=(d.organization_id,d.app_id) WHERE d.organization_id=openfox_private.current_organization_id() AND d.id=$1::uuid`, d.ID, d.TokenVersion).Scan(&valid); e != nil {
					return e
				}
				if !valid {
					return ErrVersionConflict
				}
				_, e = authorizeSlackDelivery(ctx, tx, org, a, current)
				return e
			})
		}
		if err == nil {
			result.Method = "post"
			result.Posted = true
			var receipt slack.MessageReceipt
			receipt, err = client.PostMessage(callCtx, work.Grant.AccessToken, slackPartRequest(d.ChannelID, d.ThreadTS, d.Marker, d.Part, d.Body))
			result.Channel, result.TS = receipt.Channel, receipt.TS
		}
	}
	result.Err = err
	// Persist an uncertain send even if the caller's request was cancelled.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finishCancel()
	return w.inOrganization(finishCtx, org, func(ctx context.Context, tx pgx.Tx) error { return w.finishSlackSend(ctx, tx, org, work, result) })
}

func slackRetryDelay(err error, attempt int) (time.Duration, bool) {
	var api *slack.Error
	if errors.As(err, &api) && errors.Is(err, slack.ErrRateLimited) {
		if api.RetryAfter <= 0 || api.RetryAfter > 24*time.Hour {
			return 0, false
		}
		return api.RetryAfter, true
	}
	if errors.Is(err, slack.ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return time.Duration(1<<min(attempt, 8)) * time.Second, true
	}
	return 0, false
}
func slackRateLimit(ctx context.Context, tx pgx.Tx, app, method, resource string, until time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO openfox.slack_rate_limits(organization_id,app_id,method,resource,not_before) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4)
 ON CONFLICT(organization_id,app_id,method,resource) DO UPDATE SET not_before=GREATEST(openfox.slack_rate_limits.not_before,EXCLUDED.not_before)`, app, method, resource, until)
	return err
}
func (w *IMWorker) finishSlackSend(ctx context.Context, tx pgx.Tx, org string, work slackSendWork, r slackSendResult) error {
	original := work.Delivery
	a, err := lockSlackApp(ctx, tx, original.AppID)
	if err != nil {
		return err
	}
	d, err := loadSlackDelivery(ctx, tx, original.ID, true)
	if err != nil {
		return err
	}
	if d.Marker != original.Marker || d.Generation != original.Generation || !bytes.Equal(d.Hash, original.Hash) {
		return ErrVersionConflict
	}
	if r.Posted && r.Err == nil && r.Channel == d.ChannelID && r.TS != "" {
		// Revocation cannot erase positive evidence of a POST already in flight.
		// It does not reactivate this installation or any skipped remaining part.
		if d.State == "DONE" {
			return nil
		}
		if d.Lease == original.Lease || d.State == "UNKNOWN" || d.State == "SKIPPED" {
			if err = slackRateLimit(ctx, tx, a.ID, "post", d.ChannelID, time.Now().Add(time.Second)); err != nil {
				return err
			}
			return slackSetPart(ctx, tx, d, "DONE", "", r.TS, nil)
		}
	}
	if d.Lease != original.Lease || d.State == "DONE" || d.State == "UNKNOWN" || d.State == "SKIPPED" {
		return nil
	}
	if r.Err == nil && original.ChannelID == "" && r.Channel != "" {
		if _, err = authorizeSlackDelivery(ctx, tx, org, a, d); slackDenied(err) {
			return slackSetPart(ctx, tx, d, "SKIPPED", "authorization_unavailable", "", nil)
		} else if err != nil {
			return err
		}
		hash := slackPartHash(slackPartRequest(r.Channel, d.ThreadTS, d.Marker, d.Part, d.Body))
		if _, err = tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET channel_id=$2,request_hash=$3 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, d.ID, r.Channel, hash); err != nil {
			return err
		}
		next := time.Now()
		return slackSetPart(ctx, tx, d, "PENDING", "", "", &next)
	}
	if r.Posted && errors.Is(r.Err, slack.ErrUnknown) {
		return slackSetPart(ctx, tx, d, "UNKNOWN", "external_result_unknown", "", nil)
	}
	if slackDenied(r.Err) || errors.Is(r.Err, slack.ErrForbidden) || errors.Is(r.Err, slack.ErrAuth) {
		return slackSetPart(ctx, tx, d, "SKIPPED", "authorization_unavailable", "", nil)
	}
	if errors.Is(r.Err, ErrVersionConflict) {
		next := time.Now().Add(time.Second)
		return slackSetPart(ctx, tx, d, "PENDING", "state_changed", "", &next)
	}
	delay, retry := slackRetryDelay(r.Err, d.Attempts)
	if retry && d.Attempts < imOutboxMax {
		next := time.Now().Add(delay)
		if errors.Is(r.Err, slack.ErrRateLimited) {
			resource := ""
			if r.Method == "post" {
				resource = d.ChannelID
			}
			if err = slackRateLimit(ctx, tx, a.ID, r.Method, resource, next); err != nil {
				return err
			}
		}
		return slackSetPart(ctx, tx, d, "PENDING", "upstream_retry", "", &next)
	}
	return slackSetPart(ctx, tx, d, "FAILED", "upstream_rejected", "", nil)
}
