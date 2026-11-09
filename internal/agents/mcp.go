package agents

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"openfox/internal/collaboration"
	"openfox/internal/mcpconnect"
	"openfox/internal/membership"
	"openfox/internal/platform/safehttp"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

type MCPServer = mcpconnect.Stored
type MCPList struct {
	OAuthRedirectURI string      `json:"oauth_redirect_uri,omitempty"`
	Servers          []MCPServer `json:"servers"`
	CanEdit          bool        `json:"can_edit"`
	CanUseLocal      bool        `json:"can_use_local"`
}
type MCPInput struct {
	mcpconnect.Config
	ExpectedVersion int64 `json:"expected_version"`
}

var ErrMCPDiscovery = collaboration.NewRuleError(http.StatusBadGateway, "MCP_DISCOVERY_FAILED", "the MCP server did not return a valid tool and resource list")
var ErrMCPAddressBlocked = collaboration.NewRuleError(http.StatusUnprocessableEntity, "MCP_ADDRESS_BLOCKED", "the MCP address resolves to a private or reserved IP address")
var ErrMCPAuth = collaboration.NewRuleError(http.StatusUnprocessableEntity, "MCP_AUTH_REQUIRED", "the MCP server rejected the connection credentials")

// Refresh membership from the database on each side of discovery, including asynchronous OAuth callbacks.
func (s PostgresStore) mcpTx(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent string, write bool, fn func(pgx.Tx, Agent, Actor) error) error {
	if _, _, err := s.agentRef(sel, actor, space, agent); err != nil {
		return err
	}
	return s.run(ctx, sel, func(ctx context.Context, tx pgx.Tx) error {
		var role string
		err := tx.QueryRow(ctx, `SELECT role FROM openfox.organization_members WHERE organization_id=$1 AND user_id=$2 AND status='ACTIVE' FOR SHARE`, sel.OrganizationID, actor.UserID).Scan(&role)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		actor.Role = policy.OrganizationRole(strings.ToLower(role))
		var a Agent
		if write {
			a, err = lockEditableAgent(ctx, tx, sel.OrganizationID, space, agent, actor, false)
			if err == nil {
				err = requireWritableSpace(ctx, tx, space)
			}
		} else {
			a, err = requireReadableAgent(ctx, tx, sel.OrganizationID, space, agent, actor)
		}
		if err != nil {
			return err
		}
		return fn(tx, a, actor)
	})
}

type mcpComputer struct{ ID, Runtime string }

func ownMCPComputer(ctx context.Context, tx pgx.Tx, org, agent string, actor Actor) (mcpComputer, error) {
	var d mcpComputer
	if !actor.AuthenticatedSession {
		return d, nil
	}
	err := tx.QueryRow(ctx, `SELECT d.id::text,c.runtime FROM openfox.agents a
 JOIN openfox.agent_config_versions c ON c.organization_id=a.organization_id AND c.agent_id=a.id AND c.config_version=a.current_config_version
 JOIN openfox.device_grants g ON g.organization_id=c.organization_id AND g.id=c.grant_id AND g.device_id=c.computer_id AND g.status='ACTIVE'
 JOIN openfox.physical_devices d ON d.id=g.device_id AND d.owner_user_id=$3 AND d.status='ACTIVE'
 WHERE a.organization_id=$1 AND a.id=$2 AND (g.scope='ORGANIZATION' OR (g.scope='SPACE' AND g.scope_space_id=a.space_id) OR (g.scope='PERSONAL' AND g.scope_user_id=a.owner_user_id)) FOR SHARE OF g,d`, org, agent, actor.UserID).Scan(&d.ID, &d.Runtime)
	if errors.Is(err, pgx.ErrNoRows) {
		return mcpComputer{}, nil
	}
	return d, err
}

func (s PostgresStore) ListMCP(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent string) (MCPList, error) {
	result := MCPList{Servers: []MCPServer{}}
	err := s.mcpTx(ctx, sel, actor, space, agent, false, func(tx pgx.Tx, a Agent, actor Actor) error {
		d, err := ownMCPComputer(ctx, tx, sel.OrganizationID, agent, actor)
		if err != nil {
			return err
		}
		allowed := a.OwnerUserID == actor.UserID
		if !allowed && a.Visibility == VisibilityShared {
			err = authorizeSpace(ctx, tx, sel.OrganizationID, space, actor, policy.ActionManageSpace, false)
			if err != nil && !errors.Is(err, ErrForbidden) {
				return err
			}
			allowed = err == nil
		}
		result.CanEdit = allowed && a.ArchivedAt == nil && a.BuiltinKind == ""
		if result.CanEdit {
			if err := requireWritableSpace(ctx, tx, space); err != nil {
				if !errors.Is(err, collaboration.ErrSpaceRetired) {
					return err
				}
				result.CanEdit = false
			}
		}
		result.CanUseLocal = result.CanEdit && d.ID != ""
		rows, err := tx.Query(ctx, `SELECT `+mcpconnect.Columns+` FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2
   AND (scope='PUBLIC' OR (device_id=NULLIF($3,'')::uuid AND owner_user_id=$4 AND runtime=$5))
   AND config->>'transport'<>'inherited' ORDER BY lower(name),id LIMIT 64`, sel.OrganizationID, agent, d.ID, actor.UserID, d.Runtime)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := mcpconnect.Scan(rows)
			if err != nil {
				return err
			}
			result.Servers = append(result.Servers, v)
		}
		return rows.Err()
	})
	return result, err
}

const oneMCP = ` FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2 AND id=$3`

func mcpReadLocked(ctx context.Context, tx pgx.Tx, org, agent, id string, actor Actor) (MCPServer, error) {
	if !membership.ValidateUUID(id) {
		return MCPServer{}, ErrNotFound
	}
	r, err := mcpconnect.Scan(tx.QueryRow(ctx, `SELECT `+mcpconnect.Columns+oneMCP+` FOR UPDATE`, org, agent, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return MCPServer{}, nil
	}
	if err != nil {
		return MCPServer{}, err
	}
	if r.Config.Scope == mcpconnect.DeviceOwner {
		d, e := ownMCPComputer(ctx, tx, org, agent, actor)
		if e != nil {
			return MCPServer{}, e
		}
		if r.OwnerUserID != actor.UserID || r.DeviceID != d.ID || r.Runtime != d.Runtime {
			return MCPServer{}, ErrNotFound
		}
	}
	return r, nil
}

func mcpCheckVersion(r MCPServer, version int64, instance string) error {
	if version < 0 || r.Version != version || (instance != "" && r.InstanceID != instance) {
		return ErrVersionConflict
	}
	return nil
}

func (s PostgresStore) mcpSave(ctx context.Context, tx pgx.Tx, org, agent, id string, actor Actor, previous MCPServer, c mcpconnect.Config, manifest mcpconnect.Manifest, expires *time.Time) (MCPServer, error) {
	var d mcpComputer
	var owner string
	var err error
	status := "connected"
	if c.Scope == mcpconnect.DeviceOwner {
		d, err = ownMCPComputer(ctx, tx, org, agent, actor)
		if err != nil {
			return MCPServer{}, err
		}
		if d.ID == "" {
			return MCPServer{}, ErrForbidden
		}
		owner = actor.UserID
		status = "device"
	}
	if c.AuthMode == "oauth" && c.Credential == "" {
		status = "authorizing"
	}
	sealed, err := mcpconnect.Seal(s.Secrets, org, agent, id, c)
	if err != nil {
		return MCPServer{}, ErrSecretsUnavailable
	}
	var ciphertext, nonce []byte
	var key any
	if sealed != nil {
		ciphertext, nonce, key = sealed.Ciphertext, sealed.Nonce, sealed.KeyID
	}
	args := []any{org, agent, id, c.Name, c.Scope, c.Redacted(), manifest, status, ciphertext, nonce, key, expires, d.ID, owner, d.Runtime}
	var row pgx.Row
	if previous.Version == 0 {
		var count int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2 AND scope=$3 AND owner_user_id IS NOT DISTINCT FROM NULLIF($4,'')::uuid`, org, agent, c.Scope, owner).Scan(&count)
		if err != nil {
			return MCPServer{}, err
		}
		if count >= 32 {
			return MCPServer{}, ErrLimitReached
		}
		row = tx.QueryRow(ctx, `INSERT INTO openfox.agent_mcp_servers (organization_id,agent_id,id,name,scope,config,manifest,status,secret_ciphertext,secret_nonce,secret_key_id,expires_at,device_id,owner_user_id,runtime)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,'')::uuid,NULLIF($14,'')::uuid,NULLIF($15,'')) RETURNING `+mcpconnect.Columns, args...)
	} else {
		row = tx.QueryRow(ctx, `UPDATE openfox.agent_mcp_servers SET name=$4,scope=$5,config=$6,manifest=$7,status=$8,secret_ciphertext=$9,secret_nonce=$10,secret_key_id=$11,expires_at=$12,device_id=NULLIF($13,'')::uuid,owner_user_id=NULLIF($14,'')::uuid,runtime=NULLIF($15,''),version=version+1 WHERE organization_id=$1 AND agent_id=$2 AND id=$3 RETURNING `+mcpconnect.Columns, args...)
	}
	result, err := mcpconnect.Scan(row)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return MCPServer{}, ErrNameConflict
	}
	return result, err
}

func mcpProbe(ctx context.Context, opts safehttp.Options, c mcpconnect.Config) (mcpconnect.Manifest, error) {
	if c.Scope != mcpconnect.Public || (c.AuthMode == "oauth" && c.Credential == "") {
		return mcpconnect.EmptyManifest(), nil
	}
	m, err := mcpconnect.Discover(ctx, opts, c)
	if errors.Is(err, safehttp.ErrBlockedAddress) {
		return m, ErrMCPAddressBlocked
	}
	if errors.Is(err, mcpconnect.ErrUnauthorized) {
		return m, ErrMCPAuth
	}
	if err != nil {
		return m, ErrMCPDiscovery
	}
	return m, nil
}

// PutMCP authorizes and reads masked values under a short transaction, probes without holding
// a database connection and rechecks the row instance, version and real actor before saving.
func (s PostgresStore) PutMCP(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent, id string, input MCPInput, opts safehttp.Options) (MCPServer, error) {
	c, err := input.Config.Normalize()
	if err != nil || (c.AuthMode == "oauth" && c.Credential != "" && c.Credential != mcpconnect.Mask) {
		return MCPServer{}, ErrInvalidRequest
	}
	var previous, result MCPServer
	var local mcpComputer
	err = s.mcpTx(ctx, sel, actor, space, agent, true, func(tx pgx.Tx, _ Agent, _ Actor) error {
		previous, err = mcpReadLocked(ctx, tx, sel.OrganizationID, agent, id, actor)
		if err != nil {
			return err
		}
		if err = mcpCheckVersion(previous, input.ExpectedVersion, ""); err != nil {
			return err
		}
		if previous.Config.Transport == "inherited" {
			return ErrInvalidRequest
		}
		if c.Scope == mcpconnect.DeviceOwner {
			local, err = ownMCPComputer(ctx, tx, sel.OrganizationID, agent, actor)
			if err != nil {
				return err
			}
			if local.ID == "" {
				return ErrForbidden
			}
		}
		old, e := previous.Open(s.Secrets, sel.OrganizationID, agent)
		if e != nil {
			return ErrSecretsUnavailable
		}
		c, e = c.Merge(old)
		if e != nil {
			return ErrInvalidRequest
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	manifest, err := mcpProbe(ctx, opts, c)
	if err != nil {
		return result, err
	}
	err = s.mcpTx(ctx, sel, actor, space, agent, true, func(tx pgx.Tx, _ Agent, _ Actor) error {
		v, e := mcpReadLocked(ctx, tx, sel.OrganizationID, agent, id, actor)
		if e != nil {
			return e
		}
		if e = mcpCheckVersion(v, input.ExpectedVersion, previous.InstanceID); e != nil {
			return e
		}
		if local.ID != "" {
			now, e := ownMCPComputer(ctx, tx, sel.OrganizationID, agent, actor)
			if e != nil {
				return e
			}
			if now != local {
				return ErrVersionConflict
			}
		}
		var expires *time.Time
		if c.AuthMode == "oauth" && c.Credential != "" {
			expires = previous.ExpiresAt
		}
		result, e = s.mcpSave(ctx, tx, sel.OrganizationID, agent, id, actor, v, c, manifest, expires)
		if e != nil {
			return e
		}
		return recordAudit(ctx, tx, sel, actor, "agent.mcp_saved", agent, space, uint64(result.Version))
	})
	return result, err
}

func (s PostgresStore) DeleteMCP(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent, id string, version int64) error {
	return s.mcpTx(ctx, sel, actor, space, agent, true, func(tx pgx.Tx, _ Agent, _ Actor) error {
		r, e := mcpReadLocked(ctx, tx, sel.OrganizationID, agent, id, actor)
		if e != nil {
			return e
		}
		if r.ID == "" {
			return ErrResourceNotFound
		}
		if e = mcpCheckVersion(r, version, ""); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `DELETE FROM openfox.agent_mcp_servers WHERE organization_id=$1 AND agent_id=$2 AND id=$3`, sel.OrganizationID, agent, id)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		return recordAudit(ctx, tx, sel, actor, "agent.mcp_removed", agent, space, uint64(version))
	})
}

func (s PostgresStore) MCPForOAuth(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent, id string) (MCPServer, error) {
	var r MCPServer
	err := s.mcpTx(ctx, sel, actor, space, agent, true, func(tx pgx.Tx, _ Agent, _ Actor) error {
		var e error
		r, e = mcpReadLocked(ctx, tx, sel.OrganizationID, agent, id, actor)
		if e != nil {
			return e
		}
		if r.ID == "" || r.Config.AuthMode != "oauth" || r.Config.Scope != mcpconnect.Public {
			return ErrResourceNotFound
		}
		return nil
	})
	return r, err
}

func (s PostgresStore) MCPDiscoveryConfig(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent, id string, version int64) (mcpconnect.Config, error) {
	r, err := s.MCPForOAuth(ctx, sel, actor, space, agent, id)
	if err != nil {
		return mcpconnect.Config{}, err
	}
	if r.Version != version {
		return mcpconnect.Config{}, ErrVersionConflict
	}
	c, err := r.Open(s.Secrets, sel.OrganizationID, agent)
	if err != nil {
		return c, ErrSecretsUnavailable
	}
	return c, nil
}

func (s PostgresStore) BindMCPOAuth(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent, id string, version int64, instanceID, token string, expires time.Time, manifest mcpconnect.Manifest) (MCPServer, error) {
	var result MCPServer
	err := s.mcpTx(ctx, sel, actor, space, agent, true, func(tx pgx.Tx, _ Agent, _ Actor) error {
		r, e := mcpReadLocked(ctx, tx, sel.OrganizationID, agent, id, actor)
		if e != nil {
			return e
		}
		if r.Config.AuthMode != "oauth" || r.Config.Scope != mcpconnect.Public || instanceID == "" {
			return ErrResourceNotFound
		}
		if e = mcpCheckVersion(r, version, instanceID); e != nil {
			return e
		}
		c, e := r.Open(s.Secrets, sel.OrganizationID, agent)
		if e != nil {
			return ErrSecretsUnavailable
		}
		c.Credential = token
		if _, e = c.Normalize(); e != nil || token == "" || token == mcpconnect.Mask || (!expires.IsZero() && !expires.After(time.Now())) {
			return ErrInvalidRequest
		}
		var end *time.Time
		if !expires.IsZero() {
			end = &expires
		}
		result, e = s.mcpSave(ctx, tx, sel.OrganizationID, agent, id, actor, r, c, manifest, end)
		if e != nil {
			return e
		}
		return recordAudit(ctx, tx, sel, actor, "agent.mcp_authorized", agent, space, uint64(result.Version))
	})
	return result, err
}

func (s PostgresStore) DiscoverMCP(ctx context.Context, sel tenancy.OrganizationSelection, actor Actor, space, agent string, input MCPInput, opts safehttp.Options) (mcpconnect.Manifest, error) {
	c, e := input.Config.Normalize()
	if e != nil || c.Scope != mcpconnect.Public || c.AuthMode == "oauth" {
		return mcpconnect.Manifest{}, ErrInvalidRequest
	}
	c, e = c.Merge(mcpconnect.Config{})
	if e != nil {
		return mcpconnect.Manifest{}, ErrInvalidRequest
	}
	e = s.mcpTx(ctx, sel, actor, space, agent, true, func(pgx.Tx, Agent, Actor) error { return nil })
	if e != nil {
		return mcpconnect.Manifest{}, e
	}
	return mcpProbe(ctx, opts, c)
}

func mcpRuntime(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }
