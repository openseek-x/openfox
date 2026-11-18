package integrations

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type BuglyBinding struct {
	ID              string            `json:"id"`
	ConnectionID    string            `json:"connection_id"`
	ProductID       string            `json:"product_id"`
	ProjectName     string            `json:"project_name"`
	SpaceID         string            `json:"space_id"`
	TaskTypeID      string            `json:"task_type_id"`
	PriorityMapping map[string]string `json:"priority_mapping"`
	AuthorizedBy    string            `json:"authorized_by"`
	Status          string            `json:"status"`
	StatusReason    string            `json:"status_reason"`
	Version         uint64            `json:"version"`
}
type BuglyBindingInput struct {
	ConnectionVersion uint64            `json:"connection_version"`
	ProductID         string            `json:"product_id"`
	ProjectName       string            `json:"project_name"`
	SpaceID           string            `json:"space_id"`
	TaskTypeID        string            `json:"task_type_id"`
	PriorityMapping   map[string]string `json:"priority_mapping"`
}
type BuglyBindingUpdate struct {
	Version         uint64            `json:"version"`
	ProjectName     string            `json:"project_name"`
	TaskTypeID      string            `json:"task_type_id"`
	PriorityMapping map[string]string `json:"priority_mapping"`
}
type BuglyVersionInput struct {
	Version uint64 `json:"version"`
}
type BuglySecret struct {
	URL     string `json:"url"`
	Version uint64 `json:"version"`
}
type BuglyDelivery struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	ReceivedAt time.Time `json:"received_at"`
}
type BuglyConfiguration struct {
	Connection *Connection     `json:"connection"`
	Bindings   []BuglyBinding  `json:"bindings"`
	Deliveries []BuglyDelivery `json:"deliveries"`
}

const buglyBindingColumns = `id::text,connection_id::text,product_id,project_name,space_id::text,task_type_id::text,priority_mapping,authorized_by::text,status,status_reason,version`

func scanBuglyBinding(row pgx.Row) (BuglyBinding, error) {
	var b BuglyBinding
	var raw []byte
	e := row.Scan(&b.ID, &b.ConnectionID, &b.ProductID, &b.ProjectName, &b.SpaceID, &b.TaskTypeID, &raw, &b.AuthorizedBy, &b.Status, &b.StatusReason, &b.Version)
	if errors.Is(e, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if e == nil {
		e = json.Unmarshal(raw, &b.PriorityMapping)
	}
	return b, e
}
func loadBuglyBinding(ctx context.Context, tx pgx.Tx, connection, id string) (BuglyBinding, error) {
	if !membership.ValidateUUID(id) {
		return BuglyBinding{}, ErrNotFound
	}
	return scanBuglyBinding(tx.QueryRow(ctx, `SELECT `+buglyBindingColumns+` FROM openfox.integration_bugly_bindings WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid FOR UPDATE`, connection, id))
}
func (s Service) buglyConnection(ctx context.Context, tx pgx.Tx, id string, lock bool) (Connection, []byte, []byte, error) {
	if !membership.ValidateUUID(id) {
		return Connection{}, nil, nil, ErrNotFound
	}
	q := `SELECT credentials_sealed,webhook_token_hash FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND provider='bugly' AND scope='ORGANIZATION'`
	if lock {
		q += " FOR UPDATE"
	}
	var sealed, hash []byte
	e := tx.QueryRow(ctx, q, id).Scan(&sealed, &hash)
	if errors.Is(e, pgx.ErrNoRows) {
		return Connection{}, nil, nil, ErrNotFound
	}
	if e != nil {
		return Connection{}, nil, nil, e
	}
	c, e := loadConnection(ctx, tx, id)
	return c, sealed, hash, e
}
func (s Service) buglyManager(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) error {
	var id string
	e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, actor.UserID).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrForbidden
	}
	if e != nil {
		return e
	}
	return s.requireManager(ctx, tx, org, actor)
}

// The authorizing actor remains fixed. A manager cannot borrow an owner's
// identity after that actor loses current organization or space authority.
func buglySpaceActor(ctx context.Context, tx pgx.Tx, org, user, space string, writable bool) (collaboration.Actor, error) {
	var id string
	if e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, user).Scan(&id); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return collaboration.Actor{}, ErrForbidden
		}
		return collaboration.Actor{}, e
	}
	m, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, user)
	if e != nil {
		if errors.Is(e, membership.ErrAuthorizationStale) {
			return collaboration.Actor{}, ErrForbidden
		}
		return collaboration.Actor{}, e
	}
	a := collaboration.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}
	if !CanManage(a.Role) {
		return a, ErrForbidden
	}
	if e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL FOR UPDATE`, space).Scan(&id); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return a, ErrForbidden
		}
		return a, e
	}
	if _, e = tx.Exec(ctx, `SELECT user_id FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, space, user); e != nil {
		return a, e
	}
	access, e := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, org, space, user)
	if e != nil {
		return a, e
	}
	if writable {
		if e = access.RequireWritable(); e != nil {
			return a, e
		}
	}
	actions := []policy.Action{policy.ActionReadSpace, policy.ActionManageSpace}
	if writable {
		actions = append(actions, policy.ActionCreateObject)
	}
	if e = buglyAuthorizeAccess(access, org, user, a.Role, actions...); e != nil {
		return a, e
	}
	return a, nil
}
func buglyMapping(mapping map[string]string) error {
	if len(mapping) > 20 {
		return ErrInvalidRequest
	}
	for k, v := range mapping {
		if strings.TrimSpace(k) == "" || !buglyText(k, 64, false) {
			return ErrInvalidRequest
		}
		if p, e := collaboration.ParsePriority(v); e != nil || p != v {
			return ErrInvalidRequest
		}
	}
	// Keep the encoded mapping plus a conservative per-entry allowance within
	// the table's JSONB budget. Unicode keys can satisfy the rune/count limits
	// while exceeding that aggregate budget; reject them as input, not a 500.
	raw, err := json.Marshal(mapping)
	if err != nil || len(raw)+8*len(mapping)+16 > 4096 {
		return ErrInvalidRequest
	}
	return nil
}
func buglyType(ctx context.Context, tx pgx.Tx, space, kind string) error {
	if !membership.ValidateUUID(kind) {
		return ErrInvalidRequest
	}
	var id string
	e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.task_types WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND id=$2::uuid AND archived_at IS NULL AND enabled FOR SHARE`, space, kind).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrInvalidRequest
	}
	return e
}
func (s Service) buglyPublicURL() (string, error) {
	u, e := url.Parse(s.PublicURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", ErrNotConfigured
	}
	return strings.TrimRight(s.PublicURL, "/"), nil
}
func (s Service) CreateBuglyConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (Connection, error) {
	if e := s.buglyManager(ctx, tx, org, actor); e != nil {
		return Connection{}, e
	}
	if _, e := s.buglyPublicURL(); e != nil {
		return Connection{}, e
	}
	id, e := membership.NewID()
	if e != nil {
		return Connection{}, e
	}
	token, e := randomBase64URL(32)
	if e != nil {
		return Connection{}, e
	}
	sealed, key, e := s.sealCredentials(org, id, connectionCredentials{WebhookToken: token})
	if e != nil {
		return Connection{}, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO openfox.integration_connections(organization_id,id,provider,scope,external_id,display_name,account_kind,status,status_reason,credentials_sealed,credentials_key_id,webhook_token_hash,created_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,'bugly','ORGANIZATION','local-inbound','Bugly 专业版','webhook','NEEDS_ATTENTION','inbound_disabled',$2,$3,$4,$5::uuid)`, id, sealed, key, TokenHash(token), actor.UserID)
	if isUniqueViolation(e) {
		return Connection{}, ErrVersionConflict
	}
	if e != nil {
		return Connection{}, e
	}
	return loadConnection(ctx, tx, id)
}
func (s Service) ManageBuglyConnection(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id, action string, version uint64) (Connection, error) {
	c, sealed, _, e := s.buglyConnection(ctx, tx, id, true)
	if e != nil {
		return c, e
	}
	if e = s.buglyManager(ctx, tx, org, actor); e != nil {
		return c, e
	}
	if version < 1 || c.Version != version {
		return c, ErrVersionConflict
	}
	if action != "enable" && action != "disable" && action != "rotate" && action != "disconnect" {
		return c, ErrInvalidRequest
	}
	if action == "enable" || action == "rotate" {
		if _, e = s.buglyPublicURL(); e != nil {
			return c, e
		}
	}
	if c.Status == StatusDisconnected && action != "enable" {
		return c, ErrInvalidRequest
	}
	if action == "rotate" || (action == "enable" && c.Status == StatusDisconnected) {
		token, e := randomBase64URL(32)
		if e != nil {
			return c, e
		}
		blob, key, e := s.sealCredentials(org, id, connectionCredentials{WebhookToken: token})
		if e != nil {
			return c, e
		}
		if _, e = tx.Exec(ctx, `UPDATE openfox.integration_connections SET credentials_sealed=$2,credentials_key_id=$3,webhook_token_hash=$4,webhook_last_delivery_at=NULL WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id, blob, key, TokenHash(token)); e != nil {
			return c, e
		}
	} else if action == "enable" {
		creds, e := s.openCredentials(org, id, sealed)
		if e != nil || creds.WebhookToken == "" {
			return c, ErrSecretsUnavailable
		}
	}
	status, reason := c.Status, c.StatusReason
	var disconnected any
	switch action {
	case "enable":
		status, reason = StatusActive, ""
	case "disable":
		status, reason = StatusNeedsAttention, "inbound_disabled"
	case "disconnect":
		status, reason = StatusDisconnected, ""
		disconnected = time.Now().UTC()
	}
	tag, e := tx.Exec(ctx, `UPDATE openfox.integration_connections SET status=$2,status_reason=$3,disconnected_at=$4,version=version+1,updated_at=clock_timestamp(),
 credentials_sealed=CASE WHEN $2='DISCONNECTED' THEN NULL ELSE credentials_sealed END,credentials_key_id=CASE WHEN $2='DISCONNECTED' THEN '' ELSE credentials_key_id END,
 webhook_token_hash=CASE WHEN $2='DISCONNECTED' THEN NULL ELSE webhook_token_hash END
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND version=$5`, id, status, reason, disconnected, version)
	if e != nil {
		return c, e
	}
	if tag.RowsAffected() != 1 {
		return c, ErrVersionConflict
	}
	return loadConnection(ctx, tx, id)
}
func (s Service) BuglyWebhookSecret(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, id string, version uint64) (BuglySecret, error) {
	c, sealed, _, e := s.buglyConnection(ctx, tx, id, true)
	if e != nil {
		return BuglySecret{}, e
	}
	if e = s.buglyManager(ctx, tx, org, actor); e != nil {
		return BuglySecret{}, e
	}
	if c.Version != version {
		return BuglySecret{}, ErrVersionConflict
	}
	if c.Status == StatusDisconnected {
		return BuglySecret{}, ErrBuglyInactive
	}
	creds, e := s.openCredentials(org, id, sealed)
	if e != nil {
		return BuglySecret{}, e
	}
	base, e := s.buglyPublicURL()
	if e != nil {
		return BuglySecret{}, e
	}
	return BuglySecret{URL: base + "/api/v1/hooks/bugly/" + creds.WebhookToken, Version: c.Version}, nil
}
func (s Service) CreateBuglyBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection string, in BuglyBindingInput) (BuglyBinding, error) {
	c, _, _, e := s.buglyConnection(ctx, tx, connection, true)
	if e != nil {
		return BuglyBinding{}, e
	}
	if e = s.buglyManager(ctx, tx, org, actor); e != nil {
		return BuglyBinding{}, e
	}
	if c.Version != in.ConnectionVersion || in.ConnectionVersion < 1 {
		return BuglyBinding{}, ErrVersionConflict
	}
	if c.Status == StatusDisconnected {
		return BuglyBinding{}, ErrBuglyInactive
	}
	if !buglyText(in.ProductID, 255, false) || strings.TrimSpace(in.ProductID) == "" || !buglyText(in.ProjectName, 200, false) || !membership.ValidateUUID(in.SpaceID) {
		return BuglyBinding{}, ErrInvalidRequest
	}
	if e = buglyMapping(in.PriorityMapping); e != nil {
		return BuglyBinding{}, e
	}
	if _, e = buglySpaceActor(ctx, tx, org, actor.UserID, in.SpaceID, true); e != nil {
		return BuglyBinding{}, e
	}
	if e = buglyType(ctx, tx, in.SpaceID, in.TaskTypeID); e != nil {
		return BuglyBinding{}, e
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.integration_bugly_bindings WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND removed_at IS NULL`, connection).Scan(&count); e != nil {
		return BuglyBinding{}, e
	}
	if count >= 100 {
		return BuglyBinding{}, ErrInvalidRequest
	}
	if in.PriorityMapping == nil {
		in.PriorityMapping = map[string]string{}
	}
	raw, _ := json.Marshal(in.PriorityMapping)
	out, e := scanBuglyBinding(tx.QueryRow(ctx, `INSERT INTO openfox.integration_bugly_bindings(organization_id,connection_id,product_id,project_name,space_id,task_type_id,priority_mapping,authorized_by)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3,$4::uuid,$5::uuid,$6::jsonb,$7::uuid) RETURNING `+buglyBindingColumns, connection, in.ProductID, in.ProjectName, in.SpaceID, in.TaskTypeID, raw, actor.UserID))
	if isUniqueViolation(e) {
		e = ErrVersionConflict
	}
	return out, e
}
func (s Service) ManageBuglyBinding(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, connection, id, action string, in BuglyBindingUpdate) (BuglyBinding, error) {
	c, _, _, e := s.buglyConnection(ctx, tx, connection, true)
	if e != nil {
		return BuglyBinding{}, e
	}
	b, e := loadBuglyBinding(ctx, tx, connection, id)
	if e != nil {
		return b, e
	}
	if e = s.buglyManager(ctx, tx, org, actor); e != nil {
		return b, e
	}
	if b.Version != in.Version || in.Version < 1 {
		return b, ErrVersionConflict
	}
	if b.Status == "REMOVED" {
		return b, ErrNotFound
	}
	if _, e = buglySpaceActor(ctx, tx, org, actor.UserID, b.SpaceID, action != "pause" && action != "remove"); e != nil {
		return b, e
	}
	status, reason := b.Status, b.StatusReason
	var removed any
	switch action {
	case "pause":
		status, reason = "PAUSED", ""
	case "remove":
		status, reason = "REMOVED", ""
		removed = time.Now().UTC()
	case "resume":
		if c.Status == StatusDisconnected {
			return b, ErrBuglyInactive
		}
		if _, e = buglySpaceActor(ctx, tx, org, b.AuthorizedBy, b.SpaceID, true); e != nil {
			return b, e
		}
		if e = buglyType(ctx, tx, b.SpaceID, b.TaskTypeID); e != nil {
			return b, e
		}
		status, reason = "ACTIVE", ""
	case "":
		if !buglyText(in.ProjectName, 200, false) {
			return b, ErrInvalidRequest
		}
		if e = buglyMapping(in.PriorityMapping); e != nil {
			return b, e
		}
		if e = buglyType(ctx, tx, b.SpaceID, in.TaskTypeID); e != nil {
			return b, e
		}
		b.ProjectName, b.TaskTypeID, b.PriorityMapping = in.ProjectName, in.TaskTypeID, in.PriorityMapping
	default:
		return b, ErrInvalidRequest
	}
	if b.PriorityMapping == nil {
		b.PriorityMapping = map[string]string{}
	}
	raw, _ := json.Marshal(b.PriorityMapping)
	return scanBuglyBinding(tx.QueryRow(ctx, `UPDATE openfox.integration_bugly_bindings SET project_name=$3,task_type_id=$4::uuid,priority_mapping=$5::jsonb,status=$6,status_reason=$7,removed_at=$8,version=version+1,updated_at=clock_timestamp()
 WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND id=$2::uuid AND version=$9 RETURNING `+buglyBindingColumns, connection, id, b.ProjectName, b.TaskTypeID, raw, status, reason, removed, in.Version))
}
func (s Service) BuglyConfiguration(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) (BuglyConfiguration, error) {
	out := BuglyConfiguration{Bindings: []BuglyBinding{}, Deliveries: []BuglyDelivery{}}
	if e := s.requireManager(ctx, tx, org, actor); e != nil {
		return out, e
	}
	var id string
	e := tx.QueryRow(ctx, `SELECT id::text FROM openfox.integration_connections WHERE organization_id=openfox_private.current_organization_id() AND provider='bugly' AND scope='ORGANIZATION'`).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	c, _, _, e := s.buglyConnection(ctx, tx, id, false)
	if e != nil {
		return out, e
	}
	out.Connection = &c
	rows, e := tx.Query(ctx, `SELECT `+buglyBindingColumns+` FROM openfox.integration_bugly_bindings WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND removed_at IS NULL ORDER BY created_at,id LIMIT 100`, id)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		b, e := scanBuglyBinding(rows)
		if e != nil {
			rows.Close()
			return out, e
		}
		out.Bindings = append(out.Bindings, b)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT id::text,status,detail,received_at FROM openfox.integration_webhook_deliveries WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND provider='bugly' ORDER BY received_at DESC,id DESC LIMIT 50`, id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var d BuglyDelivery
		if e = rows.Scan(&d.ID, &d.Status, &d.Detail, &d.ReceivedAt); e != nil {
			return out, e
		}
		out.Deliveries = append(out.Deliveries, d)
	}
	return out, rows.Err()
}
func buglyTokenEqual(hash []byte, token string) bool {
	return len(hash) == 32 && subtle.ConstantTimeCompare(hash, TokenHash(token)) == 1
}

func buglyAuthorizeAccess(access collaboration.SpaceAccess, org, user string, role policy.OrganizationRole, actions ...policy.Action) error {
	vis := policy.VisibilityPrivate
	if access.Space.Visibility == "SHARED" {
		vis = policy.VisibilityPublic
	}
	req := policy.Request{Actor: policy.Actor{OrganizationID: org, ActorID: user, Role: role, Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: org, ID: access.Space.ID, Visibility: vis}, Object: &policy.Object{OrganizationID: org, SpaceID: access.Space.ID, ID: "bugly-inbound"}}
	if access.Membership != nil {
		state := policy.MembershipActive
		if access.Membership.Status != "ACTIVE" {
			state = policy.MembershipRevoked
		}
		role := policy.SpaceRoleVisitor
		switch access.Membership.Role {
		case "OWNER", "EDITOR":
			role = policy.SpaceRoleAdministrator
		case "MEMBER":
			role = policy.SpaceRoleMember
		}
		req.SpaceMembership = &policy.SpaceMembership{OrganizationID: org, SpaceID: access.Space.ID, ActorID: user, Role: role, Membership: state}
	}
	for _, action := range actions {
		req.Action = action
		if !policy.Authorize(req).Allowed() {
			return ErrForbidden
		}
	}
	return nil
}
