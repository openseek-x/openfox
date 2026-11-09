package integrations

// GitLab personal access tokens use the same encrypted connections and workers
// as OAuth. Authorization is checked before network I/O and again before saving.
import (
	"context"
	"errors"
	"openfox/internal/gitlabproxy"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type GitLabTokenInput struct {
	ProxyDeviceID string `json:"proxy_device_id,omitempty"`
	Purpose       string `json:"purpose"`
	ConnectionID  string `json:"connection_id,omitempty"`
	InstanceURL   string `json:"instance_url,omitempty"`
	AccessToken   string `json:"access_token"`
	Version       uint64 `json:"version,omitempty"`
}

type GitLabTokenPlan struct {
	Proxy      gitlabproxy.Binding
	Input      GitLabTokenInput
	Connection *Connection
}

func (s Service) PrepareGitLabToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, in GitLabTokenInput) (GitLabTokenPlan, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, org, actor); err != nil {
		return GitLabTokenPlan{}, err
	}
	if s.git() != ProviderGitLab || !s.GitProviderConfigured(s.git()) {
		return GitLabTokenPlan{}, ErrNotConfigured
	}
	if s.Box == nil {
		return GitLabTokenPlan{}, ErrSecretsUnavailable
	}
	in.AccessToken = strings.TrimSpace(in.AccessToken)
	if in.AccessToken == "" || len(in.AccessToken) > 1024 || strings.ContainsAny(in.AccessToken, " \t\r\n") || len(in.InstanceURL) > 2048 {
		return GitLabTokenPlan{}, ErrInvalidRequest
	}
	plan := GitLabTokenPlan{Input: in}
	if in.ProxyDeviceID != "" {
		if in.Purpose != PurposeOrganization || !membership.ValidateUUID(in.ProxyDeviceID) {
			return plan, ErrInvalidRequest
		}
		plan.Proxy = gitlabproxy.Binding{Organization: org, Device: in.ProxyDeviceID, Owner: actor.UserID}
	}
	switch in.Purpose {
	case PurposeOrganization:
		if !CanManage(actor.Role) {
			return plan, ErrForbidden
		}
		if strings.TrimSpace(in.InstanceURL) == "" {
			return plan, ErrInvalidInstance
		}
	case PurposePersonal, PurposeReauthorize:
		if !membership.ValidateUUID(in.ConnectionID) {
			return plan, ErrInvalidRequest
		}
		c, err := loadConnection(ctx, tx, in.ConnectionID)
		if err != nil {
			return plan, err
		}
		if c.Provider != ProviderGitLab || c.Status == StatusDisconnected {
			return plan, ErrNotFound
		}
		if in.Purpose == PurposePersonal {
			if actor.Role == policy.OrganizationRoleGuest || c.Scope != ScopeOrganization {
				return plan, ErrForbidden
			}
		} else {
			if c.Scope == ScopeOrganization && !CanManage(actor.Role) || c.Scope == ScopePersonal && (c.OwnerUserID == nil || *c.OwnerUserID != actor.UserID) {
				return plan, ErrForbidden
			}
			if c.Version != in.Version {
				return plan, ErrVersionConflict
			}
		}
		parent := c.ID
		if c.ParentConnectionID != nil {
			parent = *c.ParentConnectionID
		}
		inst, found, e := s.connectionInstance(ctx, tx, org, parent, ProviderGitLab)
		if e != nil {
			return plan, e
		}
		if !found {
			return plan, ErrNotFound
		}
		plan.Proxy = inst.Proxy
		plan.Connection = &c
		plan.Input.InstanceURL = c.InstanceURL
	default:
		return plan, ErrInvalidRequest
	}
	if plan.Proxy.Device != "" {
		if err := gitlabproxy.Authorized(ctx, tx, plan.Proxy); err != nil {
			return plan, ErrForbidden
		}
	}
	return plan, nil
}

// ValidateGitLabToken runs outside the database transaction. The existing
// factory preserves HTTPS, DNS pinning, redirect and timeout protections.
func (s Service) ValidateGitLabToken(ctx context.Context, plan GitLabTokenPlan) (CallbackOutcome, string, error) {
	if s.ValidateInstance == nil {
		return CallbackOutcome{}, "", ErrInvalidInstance
	}
	var base string
	var err error
	if plan.Proxy.Device != "" {
		base, _, err = gitlabproxy.Instance(plan.Input.InstanceURL)
	} else {
		base, err = s.ValidateInstance(ctx, plan.Input.InstanceURL)
	}
	if err != nil {
		return CallbackOutcome{}, "", ErrInvalidInstance
	}
	client, err := s.gitClient(ProviderGitLab, GitInstance{BaseURL: base, PersonalToken: true, Proxy: plan.Proxy})
	if err != nil {
		return CallbackOutcome{}, "", err
	}
	validator, ok := client.(interface {
		PersonalToken(context.Context, string) (gitapi.Token, error)
	})
	if !ok {
		return CallbackOutcome{}, "", ErrNotConfigured
	}
	token, err := validator.PersonalToken(ctx, plan.Input.AccessToken)
	if errors.Is(err, gitapi.ErrTokenRevoked) {
		return CallbackOutcome{}, "", ErrNeedsReauth
	}
	if err != nil {
		return CallbackOutcome{}, "", ErrUpstreamUnavailable
	}
	user, err := client.User(ctx, plan.Input.AccessToken)
	if errors.Is(err, gitapi.ErrTokenRevoked) {
		return CallbackOutcome{}, "", ErrNeedsReauth
	}
	if err != nil {
		return CallbackOutcome{}, "", ErrUpstreamUnavailable
	}
	if user.ID == 0 || user.Login == "" {
		return CallbackOutcome{}, "", ErrNeedsReauth
	}
	out := CallbackOutcome{Token: token.Access, Scope: token.Scope, ExpiresAt: token.ExpiresAt, User: user}
	if plan.Connection != nil && plan.Connection.Scope == ScopeOrganization && plan.Input.Purpose == PurposeReauthorize && !strings.EqualFold(plan.Connection.ExternalID, user.Login) {
		out.Orgs, err = client.Orgs(ctx, token.Access)
		if err != nil {
			return CallbackOutcome{}, "", ErrUpstreamUnavailable
		}
	}
	return out, base, nil
}

func (s Service) SaveGitLabToken(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, plan GitLabTokenPlan, out CallbackOutcome, base string) (Connection, []AuditEntry, error) {
	// Serialize token bindings per organization; uniqueness constraints still
	// protect connections created concurrently through the OAuth endpoint.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, org+":gitlab-token"); err != nil {
		return Connection{}, nil, err
	}
	if plan.Connection != nil {
		// Hold both the target and its parent through the final checks and write,
		// so a concurrent disconnect cannot be undone by a slow validation.
		if _, err := tx.Exec(ctx, `SELECT id FROM openfox.integration_connections WHERE organization_id = openfox_private.current_organization_id() AND (id = $1::uuid OR id = (SELECT parent_connection_id FROM openfox.integration_connections WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid)) ORDER BY id FOR UPDATE`, plan.Connection.ID); err != nil {
			return Connection{}, nil, err
		}
	}
	current, err := s.PrepareGitLabToken(ctx, tx, org, actor, plan.Input)
	if err != nil {
		return Connection{}, nil, err
	}
	if plan.Proxy != current.Proxy {
		return Connection{}, nil, ErrVersionConflict
	}
	if plan.Connection != nil && (current.Connection == nil || current.Connection.Version != plan.Connection.Version || current.Connection.InstanceURL != plan.Connection.InstanceURL) {
		return Connection{}, nil, ErrVersionConflict
	}
	var id string
	var created bool
	switch plan.Input.Purpose {
	case PurposeOrganization:
		// The existing uniqueness contract identifies an account by login. Never
		// overwrite a same-name account belonging to a different GitLab server.
		var existingBase string
		err = tx.QueryRow(ctx, `SELECT COALESCE(settings->>'instance_url','') FROM openfox.integration_connections WHERE organization_id = openfox_private.current_organization_id() AND provider = 'gitlab' AND scope = 'ORGANIZATION' AND lower(external_id) = lower($1) AND status <> 'DISCONNECTED' FOR UPDATE`, out.User.Login).Scan(&existingBase)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Connection{}, nil, err
		}
		if err == nil && existingBase != base {
			return Connection{}, nil, ErrVersionConflict
		}
		id, created, err = s.bindOrganization(ctx, tx, org, actor, OAuthAccount{Login: out.User.Login, Kind: "user", AvatarURL: out.User.AvatarURL}, pendingSecret{AccessToken: out.Token, Scope: out.Scope, InstanceURL: base, ExpiresAt: out.ExpiresAt})
	case PurposePersonal:
		id, created, err = s.upsertPersonal(ctx, tx, org, actor, out, plan.Input.ConnectionID)
	case PurposeReauthorize:
		c := current.Connection
		if c.Scope == ScopePersonal {
			if c.ExternalID != formatID(out.User.ID) {
				return Connection{}, nil, ErrNeedsReauth
			}
			if c.ParentConnectionID == nil {
				return Connection{}, nil, ErrNotFound
			}
			parent, parentErr := loadConnection(ctx, tx, *c.ParentConnectionID)
			if parentErr != nil || parent.Status == StatusDisconnected {
				return Connection{}, nil, ErrNotFound
			}
			id, created, err = s.upsertPersonal(ctx, tx, org, actor, out, *c.ParentConnectionID)
		} else {
			// OAuth may have originally bound a group. Check group membership too.
			if !canAccessAccount(c.ExternalID, out) {
				return Connection{}, nil, ErrNeedsReauth
			}
			var reason string
			reason, err = s.reauthorize(ctx, tx, org, c.ID, out)
			if reason != "" {
				return Connection{}, nil, ErrNeedsReauth
			}
			id = c.ID
		}
	}
	if isUniqueViolation(err) {
		err = ErrVersionConflict
	}
	if err != nil {
		return Connection{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE openfox.integration_connections SET settings = (settings - 'proxy_device_id' - 'proxy_owner_id') || '{"auth_method":"personal_token"}'::jsonb || jsonb_strip_nulls(jsonb_build_object('proxy_device_id',nullif($2::text,''),'proxy_owner_id',nullif($3::text,''))) WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id, plan.Proxy.Device, plan.Proxy.Owner); err != nil {
		return Connection{}, nil, err
	}
	c, err := loadConnection(ctx, tx, id)
	if err != nil {
		return Connection{}, nil, err
	}
	action := "integration.connection_reauthorized"
	if created {
		action = "integration.connection_created"
	}
	return c, []AuditEntry{{Action: action, ResourceID: id, Metadata: map[string]string{"source": ProviderGitLab, "result": c.Scope}}}, nil
}
