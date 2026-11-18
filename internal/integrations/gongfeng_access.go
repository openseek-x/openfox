package integrations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/integrations/gitapi"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

// loadGongfengScopedClient is the narrow shared outbox load hook. Caller has
// already loaded this sync link; arbitrary owner/path never selects a project.
func (s Service) loadGongfengScopedClient(ctx context.Context, tx pgx.Tx, org, connection, link string) (gitapi.Client, error) {
	a, e := s.loadGongfeng(ctx, tx, org, connection, false)
	if e != nil {
		return nil, e
	}
	var project int64
	var path string
	e = tx.QueryRow(ctx, `SELECT remote_project_id,current_path FROM openfox.integration_gongfeng_repositories WHERE organization_id=openfox_private.current_organization_id() AND connection_id=$1::uuid AND sync_link_id=$2::uuid AND removed_at IS NULL`, connection, link).Scan(&project, &path)
	if e != nil {
		return nil, e
	}
	return a.Client.Scope(project, path)
}

// Do not fall back to an organization owner after the original manager loses
// membership. Locks pair the authority snapshot with the following DB writes.
func gongfengWorkActor(ctx context.Context, tx pgx.Tx, org, user, space string) (collaboration.Actor, error) {
	var id string
	if e := tx.QueryRow(ctx, `SELECT user_id::text FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid FOR SHARE`, user).Scan(&id); e != nil {
		return collaboration.Actor{}, ErrForbidden
	}
	m, e := (membership.Service{}).LoadActiveActor(ctx, tx, org, user)
	if e != nil {
		return collaboration.Actor{}, e
	}
	a := collaboration.Actor{UserID: m.UserID, Role: m.Role, AuthorizationVersion: m.AuthorizationVersion}
	if !CanManage(a.Role) {
		return a, ErrForbidden
	}
	if space == "" {
		return a, nil
	}
	e = tx.QueryRow(ctx, `SELECT id::text FROM openfox.spaces WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND deleted_at IS NULL FOR SHARE`, space).Scan(&id)
	if errors.Is(e, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	if e != nil {
		return a, e
	}
	if _, e = tx.Exec(ctx, `SELECT user_id FROM openfox.space_members WHERE organization_id=openfox_private.current_organization_id() AND space_id=$1::uuid AND user_id=$2::uuid FOR SHARE`, space, user); e != nil {
		return a, e
	}
	access, e := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, org, space, user)
	if e != nil {
		return a, e
	}
	if e = access.RequireWritable(); e != nil {
		return a, e
	}
	visibility := policy.VisibilityPrivate
	if access.Space.Visibility == "SHARED" {
		visibility = policy.VisibilityPublic
	}
	req := policy.Request{Object: &policy.Object{OrganizationID: org, SpaceID: space, ID: "gongfeng-sync"}, Action: policy.ActionCreateObject, Actor: policy.Actor{OrganizationID: org, ActorID: user, Role: a.Role, Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: org, ID: space, Visibility: visibility}}
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
		req.SpaceMembership = &policy.SpaceMembership{OrganizationID: org, SpaceID: space, ActorID: user, Role: role, Membership: state}
	}
	if !policy.Authorize(req).Allowed() {
		return a, ErrForbidden
	}
	return a, nil
}

func gongfengLinkAuthority(ctx context.Context, tx pgx.Tx, org string, a GongfengAccess, r GongfengRepository) (syncLinkRow, collaboration.Actor, error) {
	link := syncLinkRow{ID: r.SyncLinkID, ConnectionID: a.ID}
	var creator string
	e := tx.QueryRow(ctx, `SELECT space_id::text,direction,status,created_by::text FROM openfox.integration_sync_links WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND connection_id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, r.SyncLinkID, a.ID).Scan(&link.SpaceID, &link.Direction, &link.Status, &creator)
	if e != nil {
		return link, collaboration.Actor{}, e
	}
	if _, e = gongfengWorkActor(ctx, tx, org, a.CreatedBy, ""); e != nil {
		return link, collaboration.Actor{}, e
	}
	actor, e := gongfengWorkActor(ctx, tx, org, creator, link.SpaceID)
	return link, actor, e
}
