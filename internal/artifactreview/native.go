package artifactreview

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/docs"
	"openfox/internal/policy"
)

// Native file context stays in the requesting user's private, standalone chat.
// These checks also run after opening, so sharing a chat cannot expose snapshots.
func nativeAccess(ctx context.Context, tx pgx.Tx, org string, d Input, user string, write bool) (docs.Ref, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT visibility='PRIVATE' AND owner_user_id=$2::uuid AND task_id IS NULL AND crew_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM openfox.conversation_members m WHERE m.organization_id=c.organization_id AND m.conversation_id=c.id AND m.user_id<>$2::uuid)
 FROM openfox.conversations c WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, d.ConversationID, user).Scan(&allowed)
	if err != nil {
		return docs.Ref{}, missing(err)
	}
	if !allowed {
		return docs.Ref{}, conversations.ErrForbidden
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT role FROM openfox.organization_members WHERE organization_id=openfox_private.current_organization_id() AND user_id=$1::uuid AND status='ACTIVE'`, user).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return docs.Ref{}, conversations.ErrForbidden
	}
	if err != nil {
		return docs.Ref{}, err
	}
	access, err := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, org, d.SpaceID, user)
	if err != nil {
		return docs.Ref{}, err
	}
	visibility := policy.VisibilityPrivate
	if access.Space.Visibility == "SHARED" {
		visibility = policy.VisibilityPublic
	}
	p := policy.Request{Action: policy.ActionReadSpace, Actor: policy.Actor{OrganizationID: org, ActorID: user, Role: policy.OrganizationRole(strings.ToLower(role)), Membership: policy.MembershipActive}, Space: &policy.Space{OrganizationID: org, ID: d.SpaceID, Visibility: visibility}}
	if m := access.Membership; m != nil {
		status := policy.MembershipActive
		if m.Status != "ACTIVE" {
			status = policy.MembershipRevoked
		}
		var memberRole policy.SpaceRole
		switch m.Role {
		case "OWNER", "EDITOR":
			memberRole = policy.SpaceRoleAdministrator
		case "MEMBER":
			memberRole = policy.SpaceRoleMember
		case "VIEWER":
			memberRole = policy.SpaceRoleVisitor
		}
		p.SpaceMembership = &policy.SpaceMembership{OrganizationID: org, SpaceID: d.SpaceID, ActorID: user, Role: memberRole, Membership: status}
	}
	if !policy.Authorize(p).Allowed() {
		return docs.Ref{}, conversations.ErrNotFound
	}
	ref, err := docs.LoadRef(ctx, tx, d.SpaceID, d.NodeID)
	if err != nil {
		return ref, err
	}
	if ref.Kind != docs.KindDoc {
		return ref, ErrInvalid
	}
	if write {
		if err = access.RequireWritable(); err != nil {
			return ref, err
		}
		if !ref.Container.IsPersonal() {
			p.Action = policy.ActionUpdateObject
			p.Object = &policy.Object{OrganizationID: org, SpaceID: d.SpaceID, ID: d.NodeID}
			if !policy.Authorize(p).Allowed() {
				return ref, conversations.ErrForbidden
			}
		}
	}
	return ref, nil
}

func (s Service) nativeFile(ctx context.Context, tx pgx.Tx, org string, input Input, user string, write bool) (File, error) {
	ref, err := nativeAccess(ctx, tx, org, input, user, write)
	if err != nil {
		return File{}, err
	}
	detail, err := docs.GetNodeDetail(ctx, tx, input.SpaceID, input.NodeID)
	if err != nil {
		return File{}, err
	}
	if int64(ref.Version) != input.NodeVersion || detail.Body == nil || Digest(*detail.Body) != input.SHA256 {
		return File{}, ErrConflict
	}
	return File{ID: input.NodeID, Version: int64(ref.Version), Native: true, NativeBody: *detail.Body, Name: detail.Title + ".md", MIMEType: "text/markdown", SizeBytes: int64(len(*detail.Body)), SHA256: input.SHA256, State: "AVAILABLE"}, nil
}
func (s Service) ApplyNative(ctx context.Context, tx pgx.Tx, org string, q Context, body string) error {
	if len(body) > docs.MaxBodyBytes {
		return ErrInvalid
	}
	var node string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.doc_nodes WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid AND space_id=$2::uuid FOR NO KEY UPDATE`, q.NodeID, q.SpaceID).Scan(&node); err != nil {
		return missing(err)
	}
	if _, err := s.nativeFile(ctx, tx, org, q.Input, q.UserID, true); err != nil {
		return err
	}
	ref, err := nativeAccess(ctx, tx, org, q.Input, q.UserID, true)
	if err != nil {
		return err
	}
	return docs.UpdateNode(ctx, tx, ref, q.UserID, ref.Version, nil, &body)
}
