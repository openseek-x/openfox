package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/integrations/slack"
)

type slackRefreshJob struct {
	ID, Lease                string
	Generation, TokenVersion uint64
}

func (w *IMWorker) processSlackMaintenance(ctx context.Context, org string) error {
	var verified bool
	if w.Service.SlackConfigured() {
		if e := w.Pool.RunControlPlane(ctx, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox_private.slack_deployment_verifications WHERE fingerprint=$1)`, w.Service.SlackDeploymentFingerprint()).Scan(&verified)
		}); e != nil {
			return e
		}
	}
	// Session cleanup never holds a session row while waiting for an App. OAuth
	// callbacks lock App then session; separating these transactions avoids inversion.
	if e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE openfox.slack_oauth_sessions SET status=CASE WHEN status='EXCHANGING' THEN 'UNKNOWN' ELSE 'FAILED' END,status_reason=CASE WHEN status='EXCHANGING' THEN 'exchange_result_unknown' ELSE 'session_expired' END,nonce_sealed=decode('','hex'),lease_expires_at=NULL,finished_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.slack_oauth_sessions WHERE organization_id=openfox_private.current_organization_id() AND ((status='EXCHANGING' AND lease_expires_at<=clock_timestamp()) OR (status='PENDING' AND expires_at<=clock_timestamp())) ORDER BY expires_at,id LIMIT 100)`); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `DELETE FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND id IN(SELECT id FROM openfox.slack_events WHERE organization_id=openfox_private.current_organization_id() AND received_at<clock_timestamp()-interval '7 days' AND state IN ('DONE','IGNORED','FAILED') ORDER BY received_at,id LIMIT 200)`)
		return e
	}); e != nil {
		return e
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND (($1 AND kind='CENTRAL' AND state='WAITING_URL_VERIFICATION' AND installed_at IS NOT NULL AND token_expires_at>clock_timestamp()+interval '30 seconds' AND deployment_fingerprint=$2) OR (state='ACTIVE' AND refresh_lease_owner<>'' AND refresh_lease_expires_at<=clock_timestamp())) ORDER BY id LIMIT 1`, verified, w.Service.SlackDeploymentFingerprint()).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		a, e := lockSlackApp(ctx, tx, id)
		if e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE openfox.slack_apps SET state='ACTIVE',status_reason='',url_verified_at=clock_timestamp(),activated_at=clock_timestamp(),version=version+1,next_attempt_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND $2 AND kind='CENTRAL' AND state='WAITING_URL_VERIFICATION' AND installed_at IS NOT NULL AND token_expires_at>clock_timestamp()+interval '30 seconds' AND deployment_fingerprint=$3`, id, verified, w.Service.SlackDeploymentFingerprint())
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 1 {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='ACTIVE',status_reason='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'DISCONNECTED'`, a.ConnectionID)
			return e
		}
		tag, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET state='NEEDS_REAUTH',status_reason='refresh_result_unknown',refresh_lease_owner='',refresh_lease_expires_at=NULL,next_attempt_at=NULL,version=version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND state='ACTIVE' AND refresh_lease_owner<>'' AND refresh_lease_expires_at<=clock_timestamp()`, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 1 {
			_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='slack_refresh_unknown',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status<>'DISCONNECTED'`, a.ConnectionID)
		}
		return e
	})
}
func (w *IMWorker) processSlackRefresh(ctx context.Context, org string) error {
	if !w.Service.SlackConfigured() {
		return nil
	}
	nonce, e := randomBase64URL(12)
	if e != nil {
		return e
	}
	lease := w.instance() + ":" + nonce
	var jobs []slackRefreshJob
	e = w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `WITH due AS(SELECT a.id FROM openfox.slack_apps a WHERE a.organization_id=openfox_private.current_organization_id() AND a.state='ACTIVE' AND a.token_expires_at<=clock_timestamp()+interval '5 minutes' AND a.refresh_lease_owner='' AND (a.next_attempt_at IS NULL OR a.next_attempt_at<=clock_timestamp()) AND (a.kind='CENTRAL' OR EXISTS(SELECT 1 FROM openfox.slack_apps p WHERE p.organization_id=a.organization_id AND p.id=a.parent_app_id AND p.state='ACTIVE' AND p.generation=a.parent_generation)) ORDER BY a.token_expires_at,a.id LIMIT 1 FOR UPDATE OF a SKIP LOCKED) UPDATE openfox.slack_apps a SET refresh_lease_owner=$1,refresh_lease_expires_at=clock_timestamp()+interval '60 seconds' FROM due WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=due.id RETURNING a.id::text,a.generation,a.token_version`, lease)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			j := slackRefreshJob{Lease: lease}
			if e = rows.Scan(&j.ID, &j.Generation, &j.TokenVersion); e != nil {
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
		if e = w.refreshSlackToken(ctx, org, j); e != nil {
			all = append(all, e)
		}
	}
	return errors.Join(all...)
}
func (w *IMWorker) refreshSlackToken(ctx context.Context, org string, j slackRefreshJob) error {
	// Claim one job immediately before its bounded network work: no later job
	// spends its lease waiting for an earlier refresh/manifest request.
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var app SlackApp
	var grant slack.TokenGrant
	var credentials slackAppSecrets
	var expectedBot string
	e := w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		app, e = lockSlackApp(ctx, tx, j.ID)
		if e != nil {
			return e
		}
		var current bool
		e = tx.QueryRow(ctx, `SELECT refresh_lease_owner=$2 AND refresh_lease_expires_at>clock_timestamp() AND token_version=$3,bot_external_id FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, app.ID, j.Lease, j.TokenVersion).Scan(&current, &expectedBot)
		if e != nil {
			return e
		}
		if !current || app.Generation != j.Generation || app.State != "ACTIVE" {
			return ErrVersionConflict
		}
		if e = slackParentCurrent(ctx, tx, app.ID); e != nil {
			return e
		}
		grant, e = w.Service.slackStoredToken(ctx, tx, org, app)
		if e != nil {
			return e
		}
		credentials, e = w.Service.slackAppSecrets(ctx, tx, org, app)
		return e
	})
	if e != nil {
		return e
	}
	updated, e := w.Service.slackClient().RefreshOAuth(ctx, slack.AppCredentials{ClientID: credentials.ClientID, ClientSecret: credentials.ClientSecret}, grant.RefreshToken)
	refreshSucceeded := e == nil
	credentials = slackAppSecrets{}
	grant = slack.TokenGrant{}
	if e == nil {
		var auth slack.AuthIdentity
		auth, e = w.Service.slackClient().AuthTest(ctx, updated.AccessToken)
		scopes := slackCentralScopes
		if app.Kind != "CENTRAL" {
			scopes = slackAgentScopes
		}
		if e == nil && (auth.TeamID != app.TeamID || auth.UserID != app.BotUserID || auth.BotID != expectedBot || auth.EnterpriseID != "" || (updated.AppID != "" && updated.AppID != app.ExternalAppID) || (updated.TeamID != "" && updated.TeamID != app.TeamID) || (updated.BotUserID != "" && updated.BotUserID != app.BotUserID) || !slackScopesContain(updated.Scope, scopes)) {
			e = ErrNeedsReauth
		}
	}
	return w.inOrganization(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
		a, err := lockSlackApp(ctx, tx, j.ID)
		if err != nil {
			return err
		}
		var current bool
		err = tx.QueryRow(ctx, `SELECT refresh_lease_owner=$2 AND refresh_lease_expires_at>clock_timestamp() AND token_version=$3 FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, j.Lease, j.TokenVersion).Scan(&current)
		if err != nil {
			return err
		}
		if !current || a.Generation != j.Generation || a.State != "ACTIVE" {
			return nil
		}
		if e == nil {
			e = slackParentCurrent(ctx, tx, a.ID)
		}
		state, reason := "ACTIVE", ""
		var next *time.Time
		if e != nil {
			state, reason = "NEEDS_REAUTH", slackFailure(e)
			var upstream *slack.Error
			if !refreshSucceeded && errors.Is(e, slack.ErrRateLimited) && errors.As(e, &upstream) && upstream.RetryAfter > 0 {
				state = "ACTIVE"
				v := time.Now().Add(upstream.RetryAfter)
				next = &v
			}
		}
		if e == nil {
			plain, _ := json.Marshal(updated)
			sealed, key, err := w.Service.seal(plain, connectionAAD(org, a.ConnectionID))
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND status='ACTIVE'`, a.ConnectionID, sealed, key); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE openfox.slack_apps SET token_version=token_version+1,token_expires_at=$2,refresh_lease_owner='',refresh_lease_expires_at=NULL,next_attempt_at=NULL,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, updated.ExpiresAt)
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE openfox.slack_apps SET state=$2,status_reason=$3,next_attempt_at=$4,refresh_lease_owner='',refresh_lease_expires_at=NULL,version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ID, state, reason, next); err != nil {
			return err
		}
		if state != "ACTIVE" {
			_, err = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='slack_refresh_failed',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID)
		}
		return err
	})
}
