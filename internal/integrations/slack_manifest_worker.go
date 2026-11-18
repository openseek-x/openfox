package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/slack"
)

func (s Service) slackManifest(a SlackApp, events bool) slack.Manifest {
	scope := slackAgentScopes
	eventTypes := []string{"app_mention", "message.im", "message.channels", "message.groups", "app_uninstalled", "tokens_revoked"}
	m := slack.Manifest{DisplayInformation: slack.DisplayInformation{Name: a.Name}, Features: slack.ManifestFeatures{BotUser: slack.ManifestBot{DisplayName: a.Name}, AppHome: slack.ManifestHome{MessagesTabEnabled: true, MessagesTabReadOnlyEnabled: false}}, OAuthConfig: slack.ManifestOAuth{RedirectURLs: []string{s.SlackCallbackURI(false)}, Scopes: slack.ManifestScopes{Bot: scope}}, Settings: slack.ManifestSettings{TokenRotationEnabled: true, OrgDeployEnabled: false, SocketModeEnabled: false}}
	if events {
		m.Settings.EventSubscriptions = &slack.ManifestEvents{RequestURL: s.SlackEventsURL(a.ID), BotEvents: eventTypes}
	}
	return m
}

type slackManifestJob struct {
	AppID, Stage, Lease string
	Generation          uint64
}

func (w *IMWorker) processSlackOperations(ctx context.Context, org string) error {
	if !w.Service.SlackConfigured() {
		return nil
	}
	leaseSuffix, err := randomBase64URL(12)
	if err != nil {
		return err
	}
	lease := w.instance() + ":" + leaseSuffix
	var jobs []slackManifestJob
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		// A process dying during a non-idempotent remote call cannot reset to ready.
		if _, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET state=CASE state WHEN 'PROVING' THEN 'PROOF_UNKNOWN' WHEN 'CREATING' THEN 'CREATE_UNKNOWN' ELSE 'MANIFEST_UNKNOWN' END,status_reason='external_result_unknown',configuration_sealed=NULL,configuration_key_id='',configuration_expires_at=NULL,next_attempt_at=NULL,lease_owner='',lease_expires_at=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND state IN ('PROVING','CREATING') AND lease_expires_at<=clock_timestamp()`); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `WITH due AS (SELECT id,state FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND state IN ('PREPARED','APP_CREATED','MANIFEST_UNKNOWN') AND next_attempt_at<=clock_timestamp() AND (lease_expires_at IS NULL OR lease_expires_at<=clock_timestamp()) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) UPDATE openfox.slack_apps a SET lease_owner=$1,lease_expires_at=clock_timestamp()+interval '60 seconds',state=CASE WHEN due.state='PREPARED' THEN 'CREATING' WHEN due.state='APP_CREATED' THEN 'MANIFEST_UNKNOWN' ELSE a.state END FROM due WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=due.id RETURNING a.id::text,due.state,a.generation`, lease)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var j slackManifestJob
			j.Lease = lease
			if e = rows.Scan(&j.AppID, &j.Stage, &j.Generation); e != nil {
				return e
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
		if e = w.processSlackManifestJob(ctx, org, j); e != nil {
			all = append(all, e)
		}
	}
	return errors.Join(all...)
}
func (w *IMWorker) processSlackManifestJob(ctx context.Context, org string, j slackManifestJob) error {
	// Claim one job immediately before its bounded network work: no later job
	// spends its lease waiting for an earlier refresh/manifest request.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var app SlackApp
	var token string
	var actor collaboration.Actor
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		app, e = lockSlackApp(ctx, tx, j.AppID)
		if e != nil {
			return e
		}
		if app.Generation != j.Generation {
			return ErrVersionConflict
		}
		var blob []byte
		var expiry *time.Time
		var creatorVersion, parentVersion, identityVersion uint64
		var identity string
		var lease bool
		e = tx.QueryRow(ctx, `SELECT configuration_sealed,configuration_expires_at,actor_version,parent_version,identity_id::text,identity_version,lease_owner=$2 AND lease_expires_at>clock_timestamp() FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, j.AppID, j.Lease).Scan(&blob, &expiry, &creatorVersion, &parentVersion, &identity, &identityVersion, &lease)
		if e != nil {
			return e
		}
		if !lease {
			return ErrVersionConflict
		}
		if expiry == nil || !expiry.After(time.Now().Add(5*time.Second)) {
			return ErrNeedsReauth
		}
		actor, e = slackActor(ctx, tx, org, collaboration.Actor{UserID: app.CreatedBy, AuthorizationVersion: creatorVersion})
		if e != nil {
			return e
		}
		if e = slackAppManager(ctx, tx, org, actor, app); e != nil {
			return e
		}
		if e = slackParentCurrent(ctx, tx, app.ID); e != nil {
			return e
		}
		var identityOK bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.slack_identities WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$2 AND active AND owner_user_id=$3::uuid)`, identity, identityVersion, actor.UserID).Scan(&identityOK)
		if e != nil {
			return e
		}
		if !identityOK {
			return ErrNeedsReauth
		}
		plain, e := w.Service.open(blob, slackAAD(org, j.AppID, "configuration"))
		if e != nil {
			return e
		}
		token = string(plain)
		body, _ := json.Marshal(w.Service.slackManifest(app, j.Stage != "PREPARED"))
		hash := sha256.Sum256(body)
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET manifest_hash=$2 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID, hash[:])
		return e
	})
	if e != nil {
		return w.finishSlackManifestFailure(ctx, org, j, e, false)
	}
	var created slack.CreatedApp
	switch j.Stage {
	case "PREPARED":
		created, e = w.Service.slackClient().CreateManifest(ctx, token, w.Service.slackManifest(app, false))
	case "APP_CREATED":
		e = w.Service.slackClient().UpdateManifest(ctx, token, app.ExternalAppID, w.Service.slackManifest(app, true))
	case "MANIFEST_UNKNOWN":
		var raw json.RawMessage
		raw, e = w.Service.slackClient().ExportManifest(ctx, token, app.ExternalAppID)
		if e == nil {
			var actual slack.Manifest
			if json.Unmarshal(raw, &actual) != nil {
				e = slack.ErrIncomplete
			} else {
				want, _ := json.Marshal(w.Service.slackManifest(app, true))
				got, _ := json.Marshal(actual)
				if string(got) != string(want) {
					e = slack.ErrIncomplete
				}
			}
		}
	}
	token = ""
	if e != nil {
		return w.finishSlackManifestFailure(ctx, org, j, e, true)
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		current, e := lockSlackApp(ctx, tx, j.AppID)
		if e != nil {
			return e
		}
		var lease bool
		if e = tx.QueryRow(ctx, `SELECT lease_owner=$2 AND lease_expires_at>clock_timestamp() FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID, j.Lease).Scan(&lease); e != nil {
			return e
		}
		authorized := current.Generation == j.Generation && lease && current.State != "CANCELLED" && current.State != "DISCONNECTED"
		if authorized {
			if _, e = slackActor(ctx, tx, org, actor); e != nil {
				if !errors.Is(e, ErrForbidden) {
					return e
				}
				authorized = false
			}
		}
		if authorized {
			if e = slackAppManager(ctx, tx, org, actor, current); e != nil {
				if !errors.Is(e, ErrForbidden) && !errors.Is(e, ErrNotFound) {
					return e
				}
				authorized = false
			}
		}
		if authorized {
			if e = slackParentCurrent(ctx, tx, current.ID); e != nil {
				if !errors.Is(e, ErrNeedsReauth) {
					return e
				}
				authorized = false
			}
		}
		if authorized {
			var identityOK bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.slack_apps a JOIN openfox.slack_identities i ON i.organization_id=a.organization_id AND i.id=a.identity_id AND i.version=a.identity_version JOIN openfox.integration_connections c ON c.organization_id=i.organization_id AND c.id=i.connection_id WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND i.active AND c.status='ACTIVE' AND i.owner_user_id=$2::uuid)`, current.ID, actor.UserID).Scan(&identityOK)
			if e != nil {
				return e
			}
			authorized = identityOK
		}
		if !authorized {
			if j.Stage == "PREPARED" && created.AppID != "" {
				_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET app_id=$2,state='ORPHANED_APP',status_reason='created_after_authorization_changed',configuration_sealed=NULL,configuration_key_id='',configuration_expires_at=NULL,lease_owner='',lease_expires_at=NULL,next_attempt_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID, created.AppID)
				return e
			}
			return nil
		}
		if j.Stage == "PREPARED" {
			creds := slackAppSecrets{ClientID: created.Credentials.ClientID, ClientSecret: created.Credentials.ClientSecret, SigningSecret: created.SigningSecret}
			plain, _ := json.Marshal(creds)
			sealed, key, e := w.Service.seal(plain, slackAAD(org, app.ID, "app"))
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET app_id=$2,app_credentials_sealed=$3,app_credentials_key_id=$4,state='APP_CREATED',status_reason='',lease_owner='',lease_expires_at=NULL,next_attempt_at=clock_timestamp(),version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID, created.AppID, sealed, key)
			return e
		}
		state := "WAITING_URL_VERIFICATION"
		if current.URLVerified {
			state = "WAITING_INSTALL"
		}
		if current.URLVerified && current.Installed {
			state = "ACTIVE"
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET state=$2,status_reason='',activated_at=CASE WHEN $2='ACTIVE' THEN COALESCE(activated_at,clock_timestamp()) ELSE activated_at END,configuration_sealed=NULL,configuration_key_id='',configuration_expires_at=NULL,lease_owner='',lease_expires_at=NULL,next_attempt_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID, state)
		if e != nil {
			return e
		}
		if state == "ACTIVE" {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',updated_at=clock_timestamp(),version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'DISCONNECTED'`, app.ConnectionID)
		}
		return e
	})
}
func (w *IMWorker) finishSlackManifestFailure(ctx context.Context, org string, j slackManifestJob, cause error, sent bool) error {
	state, reason := "FAILED", slackFailure(cause)
	if j.Stage == "APP_CREATED" {
		state = "APP_CREATED"
	}
	if j.Stage == "MANIFEST_UNKNOWN" {
		state = "MANIFEST_UNKNOWN"
	}
	var next *time.Time
	if errors.Is(cause, ErrVersionConflict) {
		return nil
	}
	if errors.Is(cause, ErrNeedsReauth) {
		state, reason = "APP_CREATED", "configuration_expired"
		if j.Stage == "PREPARED" {
			state = "FAILED"
		}
	}
	if errors.Is(cause, ErrForbidden) || errors.Is(cause, ErrNotFound) {
		reason = "authorization_changed"
	}
	if sent && errors.Is(cause, slack.ErrUnknown) {
		if j.Stage == "PREPARED" {
			state = "CREATE_UNKNOWN"
		} else {
			state = "MANIFEST_UNKNOWN"
			v := time.Now().Add(5 * time.Second)
			next = &v
		}
	}
	if errors.Is(cause, slack.ErrRateLimited) {
		var upstream *slack.Error
		if errors.As(cause, &upstream) && upstream.RetryAfter > 0 && upstream.RetryAfter <= 15*time.Minute {
			state = j.Stage
			v := time.Now().Add(upstream.RetryAfter)
			next = &v
		}
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET state=$4,status_reason=$5,next_attempt_at=$6,configuration_sealed=CASE WHEN $6::timestamptz IS NULL THEN NULL ELSE configuration_sealed END,configuration_key_id=CASE WHEN $6::timestamptz IS NULL THEN '' ELSE configuration_key_id END,configuration_expires_at=CASE WHEN $6::timestamptz IS NULL THEN NULL ELSE configuration_expires_at END,lease_owner='',lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND generation=$2 AND lease_owner=$3`, j.AppID, j.Generation, j.Lease, state, reason, next)
		return e
	})
}
