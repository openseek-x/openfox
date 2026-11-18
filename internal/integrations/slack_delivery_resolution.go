package integrations

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/slack"
	"openfox/internal/membership"
)

var slackRemoteTimestamp = regexp.MustCompile(`^[0-9]{1,16}\.[0-9]{1,9}$`)

type SlackDeliveryResolution struct {
	delivery         slackDeliveryRow
	app              SlackApp
	target           slackReceiptTarget
	grant            slack.TokenGrant
	actor            collaboration.Actor
	action, remoteTS string
}

func (SlackDeliveryResolution) String() string   { return "SlackDeliveryResolution([redacted])" }
func (SlackDeliveryResolution) GoString() string { return "SlackDeliveryResolution([redacted])" }

type SlackDeliveryResolutionResult struct {
	matched   bool
	failure   string
	rateAfter time.Duration
}

func (s Service) PrepareSlackDeliveryResolution(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, appID, deliveryID string, in SlackResolveDeliveryInput) (SlackDeliveryResolution, error) {
	var work SlackDeliveryResolution
	if !membership.ValidateUUID(appID) || !membership.ValidateUUID(deliveryID) || in.Version == 0 || (in.Action != "skip" && in.Action != "verify") || (in.Action == "verify" && !slackRemoteTimestamp.MatchString(in.RemoteTS)) || (in.Action == "skip" && in.RemoteTS != "") {
		return work, ErrInvalidRequest
	}
	a, err := lockSlackApp(ctx, tx, appID)
	if err != nil {
		return work, err
	}
	current, err := slackActor(ctx, tx, org, actor)
	if err != nil {
		return work, err
	}
	if err = slackAppManager(ctx, tx, org, current, a); err != nil {
		return work, err
	}
	d, err := loadSlackDelivery(ctx, tx, deliveryID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return work, ErrNotFound
	}
	if err != nil {
		return work, err
	}
	if d.AppID != a.ID {
		return work, ErrNotFound
	}
	if d.Version != in.Version || d.State != "UNKNOWN" || slackParentTable(d.Kind) == "" {
		return work, ErrVersionConflict
	}
	work = SlackDeliveryResolution{delivery: d, app: a, actor: current, action: in.Action, remoteTS: in.RemoteTS}
	if in.Action == "skip" {
		return work, nil
	}
	if !bytes.Equal(d.Hash, slackPartHash(slackPartRequest(d.ChannelID, d.ThreadTS, d.Marker, d.Part, d.Body))) {
		return work, ErrVersionConflict
	}
	if a.State != "ACTIVE" || a.Generation != d.Generation || d.ChannelID == "" {
		return work, ErrVersionConflict
	}
	if err = slackParentCurrent(ctx, tx, a.ID); err != nil {
		return work, err
	}
	method := "history"
	if d.ThreadTS != "" {
		method = "replies"
	}
	var blocked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.slack_rate_limits WHERE organization_id=openfox_private.current_organization_id() AND app_id=$1::uuid AND method=$2 AND not_before>clock_timestamp())`, a.ID, method).Scan(&blocked); err != nil {
		return work, err
	}
	if blocked {
		return work, slack.ErrRateLimited
	}
	if err = slackRateLimit(ctx, tx, a.ID, method, "", time.Now().Add(time.Minute)); err != nil {
		return work, err
	}
	work.grant, err = s.slackStoredToken(ctx, tx, org, a)
	if err != nil {
		return work, err
	}
	if !work.grant.ExpiresAt.IsZero() && time.Until(work.grant.ExpiresAt) < 30*time.Second {
		return work, ErrNeedsReauth
	}
	var botID string
	if err = tx.QueryRow(ctx, `SELECT bot_external_id FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID).Scan(&botID); err != nil {
		return work, err
	}
	work.target = slackReceiptTarget{AppID: a.ExternalAppID, BotID: botID, BotUserID: a.BotUserID, TeamID: a.TeamID, ChannelID: d.ChannelID, ThreadTS: d.ThreadTS, Marker: d.Marker, Part: d.Part, Body: d.Body}
	return work, nil
}
func (s Service) ExecuteSlackDeliveryResolution(ctx context.Context, work SlackDeliveryResolution) SlackDeliveryResolutionResult {
	if work.action == "skip" {
		return SlackDeliveryResolutionResult{}
	}
	if work.action != "verify" {
		return SlackDeliveryResolutionResult{failure: "invalid_resolution"}
	}
	in := slack.HistoryInput{Channel: work.delivery.ChannelID, ThreadTS: work.delivery.ThreadTS, Oldest: work.remoteTS, Latest: work.remoteTS, Inclusive: true, Limit: 15}
	var page slack.MessagesPage
	var err error
	if in.ThreadTS == "" {
		page, err = s.slackClient().History(ctx, work.grant.AccessToken, in)
	} else {
		page, err = s.slackClient().Replies(ctx, work.grant.AccessToken, in)
	}
	if err != nil {
		out := SlackDeliveryResolutionResult{failure: "verification_unavailable"}
		var api *slack.Error
		if errors.As(err, &api) && errors.Is(err, slack.ErrRateLimited) {
			out.failure = "verification_rate_limited"
			out.rateAfter = api.RetryAfter
		}
		return out
	}
	for _, m := range page.Messages {
		if m.TS == work.remoteTS && slackReceiptMatches(work.target, work.app.TeamID, in.Channel, m) {
			return SlackDeliveryResolutionResult{matched: true}
		}
	}
	return SlackDeliveryResolutionResult{failure: "verification_not_matched"}
}
func slackDeliveryView(d slackDeliveryRow) SlackDelivery {
	return SlackDelivery{ID: d.ID, AppID: d.AppID, SourceKind: d.Kind, Part: d.Part, Version: d.Version, State: d.State, StatusReason: d.Reason, ChannelID: d.ChannelID, ThreadTS: d.ThreadTS, RemoteTS: d.RemoteTS, CreatedAt: d.CreatedAt}
}
func (s Service) FinishSlackDeliveryResolution(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, work SlackDeliveryResolution, result SlackDeliveryResolutionResult) (SlackDelivery, error) {
	a, err := lockSlackApp(ctx, tx, work.app.ID)
	if err != nil {
		return SlackDelivery{}, err
	}
	current, err := slackActor(ctx, tx, org, actor)
	if err != nil {
		return SlackDelivery{}, err
	}
	if current.UserID != work.actor.UserID || current.AuthorizationVersion != work.actor.AuthorizationVersion {
		return SlackDelivery{}, ErrForbidden
	}
	if err = slackAppManager(ctx, tx, org, current, a); err != nil {
		return SlackDelivery{}, err
	}
	d, err := loadSlackDelivery(ctx, tx, work.delivery.ID, true)
	if err != nil {
		return SlackDelivery{}, err
	}
	if d.AppID != a.ID || d.Version != work.delivery.Version || d.State != "UNKNOWN" || !bytes.Equal(d.Hash, work.delivery.Hash) {
		return SlackDelivery{}, ErrVersionConflict
	}
	if work.action == "skip" {
		// The explicit action abandons all not-yet-confirmed parts, preserving DONE.
		_, err = tx.Exec(ctx, `UPDATE openfox.slack_deliveries SET state='SKIPPED',status_reason='manually_skipped',version=version+1,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,finished_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND source_kind=$1 AND source_id=$2::uuid AND state<>'DONE'`, d.Kind, d.SourceID)
		if err == nil {
			err = slackAggregateParent(ctx, tx, d)
		}
	} else if work.action == "verify" {
		if a.Generation != work.app.Generation || a.ExternalAppID != work.app.ExternalAppID || a.TeamID != work.app.TeamID {
			return SlackDelivery{}, ErrVersionConflict
		}
		if result.matched {
			err = slackSetPart(ctx, tx, d, "DONE", "verified_receipt", work.remoteTS, nil)
		} else {
			if result.rateAfter > 0 {
				method := "history"
				if d.ThreadTS != "" {
					method = "replies"
				}
				if err = slackRateLimit(ctx, tx, a.ID, method, "", time.Now().Add(result.rateAfter)); err != nil {
					return SlackDelivery{}, err
				}
			}
			err = slackSetPart(ctx, tx, d, "UNKNOWN", result.failure, "", nil)
		}
	} else {
		return SlackDelivery{}, ErrInvalidRequest
	}
	if err != nil {
		return SlackDelivery{}, err
	}
	d, err = loadSlackDelivery(ctx, tx, d.ID, false)
	return slackDeliveryView(d), err
}

// Called only for authenticated, durably accepted bot-message events. Positive
// evidence may settle an unknown send; missing events never authorize a retry.
func (w *IMWorker) processSlackReceipt(ctx context.Context, org, appID string, generation uint64, event SlackEvent) error {
	if event.Hidden || event.Metadata == nil || event.Metadata.EventType != slackDeliveryEvent {
		return nil
	}
	marker := event.Metadata.EventPayload["delivery_id"]
	if len(marker) < 16 || len(marker) > 128 {
		return nil
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, err := lockSlackApp(ctx, tx, appID)
		if err != nil {
			return err
		}
		if a.Generation != generation {
			return nil
		}
		var id, botID string
		err = tx.QueryRow(ctx, `SELECT d.id::text,a.bot_external_id FROM openfox.slack_deliveries d JOIN openfox.slack_apps a ON (a.organization_id,a.id)=(d.organization_id,d.app_id)
 WHERE d.organization_id=openfox_private.current_organization_id() AND d.app_id=$1::uuid AND d.marker=$2 AND d.generation=$3 AND d.source_kind IN ('REPLY','NOTIFICATION')`, appID, marker, generation).Scan(&id, &botID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		d, err := loadSlackDelivery(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if d.State == "DONE" || d.Attempts == 0 {
			return nil
		}
		target := slackReceiptTarget{AppID: a.ExternalAppID, BotID: botID, BotUserID: a.BotUserID, TeamID: a.TeamID, ChannelID: d.ChannelID, ThreadTS: d.ThreadTS, Marker: d.Marker, Part: d.Part, Body: d.Body}
		message := slack.Message{Type: event.Type, Subtype: event.Subtype, User: event.User, Text: event.Text, TS: event.TS, ThreadTS: event.ThreadTS, BotID: event.BotID, AppID: event.AppID, Team: event.Team, Metadata: event.Metadata}
		if !slackRemoteTimestamp.MatchString(event.TS) || !slackReceiptMatches(target, a.TeamID, event.Channel, message) || !bytes.Equal(d.Hash, slackPartHash(slackPartRequest(d.ChannelID, d.ThreadTS, d.Marker, d.Part, d.Body))) {
			return nil
		}
		if err := slackRateLimit(ctx, tx, a.ID, "post", d.ChannelID, time.Now().Add(time.Second)); err != nil {
			return err
		}
		return slackSetPart(ctx, tx, d, "DONE", "event_receipt", event.TS, nil)
	})
}
