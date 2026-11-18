package integrations

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
)

func slackID(v string) bool {
	if v == "" || !utf8.ValidString(v) || utf8.RuneCountInString(v) > 64 {
		return false
	}
	return strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}
func slackSecret(v string) bool {
	return v != "" && len(v) <= 4096 && strings.TrimSpace(v) == v && strings.IndexFunc(v, unicode.IsControl) < 0
}

// PrepareSlackReconfiguration only leases a local operation. It never replaces
// a known external App ID and never retries an unknown App creation.
func (s Service) PrepareSlackReconfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, in SlackRecoverAppInput, recover bool) (SlackApp, error) {
	if !s.SlackConfigured() || !slackSecret(in.ConfigurationRefreshToken) {
		return SlackApp{}, ErrInvalidRequest
	}
	a, e := lockSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	current, e := slackActor(ctx, tx, org, actor)
	if e != nil {
		return a, e
	}
	if a.Version != in.Version {
		return a, ErrVersionConflict
	}
	if a.Kind == "CENTRAL" || a.CreatedBy != actor.UserID || a.State == "ACTIVE" || a.State == "CANCELLED" || a.State == "DISCONNECTED" || a.State == "CREATING" || a.State == "PROVING" {
		return a, ErrInvalidRequest
	}
	if e = slackAppManager(ctx, tx, org, current, a); e != nil {
		return a, e
	}
	if e = slackParentCurrent(ctx, tx, a.ID); e != nil {
		return a, e
	}
	if e = slackDynamicIdentity(ctx, tx, a, current); e != nil {
		return a, e
	}
	if recover {
		if !slackID(in.ExternalAppID) || !slackSecret(in.ClientID) || !slackSecret(in.ClientSecret) || !slackSecret(in.SigningSecret) || (a.ExternalAppID != "" && in.ExternalAppID != a.ExternalAppID) {
			return a, ErrInvalidRequest
		}
	} else if a.ExternalAppID == "" {
		return a, ErrInvalidRequest
	}
	_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps a SET state='PROVING',status_reason='',actor_version=$2,parent_version=p.version,identity_id=i.id,identity_version=i.version,configuration_sealed=NULL,configuration_key_id='',configuration_expires_at=NULL,lease_owner='http-configuration',lease_expires_at=clock_timestamp()+interval '45 seconds',next_attempt_at=NULL,version=a.version+1,updated_at=clock_timestamp() FROM openfox.slack_apps p,openfox.slack_identities i WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND p.organization_id=a.organization_id AND p.id=a.parent_app_id AND i.organization_id=a.organization_id AND i.parent_app_id=p.id AND i.owner_user_id=a.created_by AND i.slack_user_id=a.owner_slack_user_id AND i.team_id=a.team_id AND i.active`, id, current.AuthorizationVersion)
	if e != nil {
		return a, e
	}
	return loadSlackApp(ctx, tx, id)
}

// Export is a read using the just-proven configuration owner credential. The
// caller-provided signing/client secrets still need the independent signed URL
// challenge and OAuth checks before any business execution can be activated.
func (s Service) VerifySlackRecovery(ctx context.Context, in SlackRecoverAppInput) SlackProofResult {
	proof := s.RotateSlackConfiguration(ctx, in.ConfigurationRefreshToken)
	if proof.Failure != "" || proof.Unknown {
		return proof
	}
	if _, e := s.slackClient().ExportManifest(ctx, proof.AccessToken, in.ExternalAppID); e != nil {
		proof.AccessToken = ""
		proof.Failure = slackFailure(e)
	}
	return proof
}
func (s Service) FinishSlackRecovery(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, attemptVersion uint64, in SlackRecoverAppInput, proof SlackProofResult) (SlackApp, error) {
	a, e := lockSlackApp(ctx, tx, id)
	if e != nil {
		return a, e
	}
	if a.State != "PROVING" || a.CreatedBy != actor.UserID || a.Version != attemptVersion {
		return a, ErrVersionConflict
	}
	valid := proof.Failure == "" && !proof.Unknown && proof.AccessToken != "" && proof.TeamID == a.TeamID && proof.UserID == a.OwnerSlackUserID && proof.ExpiresAt.After(time.Now().Add(time.Minute))
	if valid {
		if !slackID(in.ExternalAppID) || (a.ExternalAppID != "" && a.ExternalAppID != in.ExternalAppID) {
			return a, ErrInvalidRequest
		}
		plain, _ := json.Marshal(slackAppSecrets{ClientID: in.ClientID, ClientSecret: in.ClientSecret, SigningSecret: in.SigningSecret})
		sealed, key, e := s.seal(plain, slackAAD(org, a.ID, "app"))
		if e != nil {
			return a, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.slack_apps SET app_id=$2,app_credentials_sealed=$3,app_credentials_key_id=$4,url_verified_at=NULL,installed_at=NULL,activated_at=NULL,generation=generation+1,token_version=token_version+1 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, in.ExternalAppID, sealed, key)
		if e != nil {
			return a, e
		}
		_, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET status='NEEDS_ATTENTION',status_reason='slack_recovery_pending',credentials_sealed=NULL,credentials_key_id='',version=version+1,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, a.ConnectionID)
		if e != nil {
			return a, e
		}
	}
	return s.FinishSlackProof(ctx, tx, org, actor, id, attemptVersion, proof)
}
