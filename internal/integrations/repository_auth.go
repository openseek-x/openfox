package integrations

import (
	"context"

	"github.com/jackc/pgx/v5"
	"openfox/internal/gitlabproxy"
	"openfox/internal/reposource"
)

// The dispatch caller has already checked the live run and reads the binding
// directly from the Agent/space repository, never from a device-supplied URL.
func (s Service) RepositoryAuthorization(ctx context.Context, tx pgx.Tx, org, device string, repo reposource.Repository) (*reposource.Authorization, error) {
	var provider, instance, proxyDevice, proxyOwner string
	var sealed []byte
	e := tx.QueryRow(ctx, `SELECT provider,COALESCE(settings->>'instance_url',''),COALESCE(settings->>'proxy_device_id',''),COALESCE(settings->>'proxy_owner_id',''),credentials_sealed
 FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND scope='ORGANIZATION' AND status='ACTIVE' FOR SHARE`, repo.ConnectionID).Scan(&provider, &instance, &proxyDevice, &proxyOwner, &sealed)
	if e != nil || !reposource.MatchesInstance(provider, instance, repo.URL) {
		return nil, ErrForbidden
	}
	if proxyDevice != "" {
		if proxyDevice != device {
			return nil, ErrForbidden
		}
		if e = gitlabproxy.Authorized(ctx, tx, gitlabproxy.Binding{Organization: org, Device: device, Owner: proxyOwner}); e != nil {
			return nil, ErrForbidden
		}
	}
	creds, e := s.openCredentials(org, repo.ConnectionID, sealed)
	if e != nil {
		return nil, e
	}
	if creds.AccessToken == "" {
		return nil, ErrNeedsReauth
	}
	username := "oauth2"
	if provider == "github" {
		username = "x-access-token"
	}
	return &reposource.Authorization{Scope: repo.ConnectionID, Username: username, Token: creds.AccessToken, PrivateNetwork: proxyDevice != ""}, nil
}
