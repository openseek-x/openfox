package conversations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type slackInvocation struct {
	routeID, identityID string
	identityVersion     int64
}

// CheckSlackInvocation checks the actual actor, not the creator of a shared
// thread. Only epoch and identity locks are acquired here: dispatch callers may
// already hold conversation, Agent or Run locks. Revocation never locks those
// objects after an epoch/identity lock.
func CheckSlackInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	_, err := checkSlackInvocation(ctx, tx, conversationID, actorID)
	return err
}

func checkSlackInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) (*slackInvocation, error) {
	var gate slackInvocation
	var parentID, appID, teamID, channelID, epochID, routeOwner, routeSlackUser, routeTeam string
	var generation int64
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT r.id::text,a.parent_app_id::text,a.id::text,a.team_id,r.channel_id,
 COALESCE(r.channel_epoch_id::text,''),original.owner_user_id::text,original.slack_user_id,original.team_id,r.generation,
 a.state='ACTIVE' AND p.state='ACTIVE' AND a.generation=r.generation
 AND a.parent_generation=p.generation AND a.team_id=p.team_id AND b.removed_at IS NULL AND b.connection_id=a.connection_id
 AND ac.status='ACTIVE' AND pc.status='ACTIVE'
 FROM openfox.slack_routes r
 JOIN openfox.slack_apps a ON (a.organization_id,a.id)=(r.organization_id,r.app_id)
 JOIN openfox.slack_apps p ON (p.organization_id,p.id)=(a.organization_id,a.parent_app_id)
 JOIN openfox.im_bots b ON (b.organization_id,b.id)=(r.organization_id,r.bot_id)
 JOIN openfox.slack_identities original ON (original.organization_id,original.id)=(r.organization_id,r.identity_id)
 JOIN openfox.integration_connections ac ON (ac.organization_id,ac.id)=(a.organization_id,a.connection_id)
 JOIN openfox.integration_connections pc ON (pc.organization_id,pc.id)=(p.organization_id,p.connection_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid`, conversationID).
		Scan(&gate.routeID, &parentID, &appID, &teamID, &channelID, &epochID, &routeOwner, &routeSlackUser, &routeTeam, &generation, &allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // Ordinary OpenFox and other-provider conversations.
	}
	if err != nil {
		return nil, err
	}
	if !allowed || actorID == "" {
		return nil, ErrForbidden
	}
	if epochID != "" {
		err = tx.QueryRow(ctx, `SELECT enabled AND public_reply_consent AND disabled_at IS NULL
 AND app_id=$2::uuid AND channel_id=$3 AND generation=$4
 FROM openfox.slack_channels WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`,
			epochID, appID, channelID, generation).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
			return nil, ErrForbidden
		}
		if err != nil {
			return nil, err
		}
	}
	var slackUser string
	err = tx.QueryRow(ctx, `SELECT i.id::text,i.version,i.slack_user_id,c.status='ACTIVE' AND m.status='ACTIVE' AND m.role<>'GUEST'
 FROM openfox.slack_identities i
 JOIN openfox.integration_connections c ON (c.organization_id,c.id)=(i.organization_id,i.connection_id)
 JOIN openfox.organization_members m ON (m.organization_id,m.user_id)=(i.organization_id,i.owner_user_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.parent_app_id=$1::uuid
 AND i.owner_user_id=$2::uuid AND i.team_id=$3 AND i.active FOR SHARE OF i`, parentID, actorID, teamID).
		Scan(&gate.identityID, &gate.identityVersion, &slackUser, &allowed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !allowed) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	// A same-account rebind may post new messages to its existing DM, while the
	// immutable per-message snapshot still rejects old inputs and Runs. Another
	// local owner or Slack account must not inherit that private destination.
	if epochID == "" && (actorID != routeOwner || slackUser != routeSlackUser || teamID != routeTeam) {
		return nil, ErrForbidden
	}
	return &gate, nil
}

// LockSlackInvocation precedes Run/tool locks. Keep these locks only through the
// local write transaction, never while performing a platform request.
func LockSlackInvocation(ctx context.Context, tx pgx.Tx, conversationID, actorID string) error {
	var botID, parentID, appID string
	err := tx.QueryRow(ctx, `SELECT r.bot_id::text,a.parent_app_id::text,a.id::text
 FROM openfox.slack_routes r JOIN openfox.slack_apps a ON (a.organization_id,a.id)=(r.organization_id,r.app_id)
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid`, conversationID).
		Scan(&botID, &parentID, &appID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.im_bots WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, botID).Scan(&id); err != nil {
		return err
	}
	// Explicit statements establish the order; a JOIN cannot promise lock order.
	for _, id := range []string{parentID, appID} {
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.slack_apps WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid FOR SHARE`, id).Scan(&locked); err != nil {
			return err
		}
	}
	return CheckSlackInvocation(ctx, tx, conversationID, actorID)
}

func snapshotSlackInput(ctx context.Context, tx pgx.Tx, conversationID, actorID, messageID string) error {
	gate, err := checkSlackInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO openfox.slack_input_authorizations(organization_id,message_id,route_id,actor_id,identity_id,identity_version)
 VALUES(openfox_private.current_organization_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5)`, messageID, gate.routeID, actorID, gate.identityID, gate.identityVersion)
	return err
}

// CheckSlackInputs prevents a newly bound identity from reviving older queued
// messages. Missing snapshots fail closed, including synthetic retry inputs.
func CheckSlackInputs(ctx context.Context, tx pgx.Tx, conversationID, actorID string, inputIDs []string) error {
	gate, err := checkSlackInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	var count int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM openfox.conversation_inputs i
 JOIN openfox.slack_input_authorizations s ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.id=ANY($2::uuid[])
 AND s.route_id=$3::uuid AND s.actor_id=$4::uuid AND s.identity_id=$5::uuid AND s.identity_version=$6`,
		conversationID, inputIDs, gate.routeID, actorID, gate.identityID, gate.identityVersion).Scan(&count)
	if err != nil {
		return err
	}
	if len(inputIDs) == 0 || count != len(inputIDs) {
		return ErrForbidden
	}
	return nil
}

// CheckSlackRun checks every original input, including consumed inputs retained
// after completion. Rebinding must not restore an old Run or its queued reply.
func CheckSlackRun(ctx context.Context, tx pgx.Tx, conversationID, actorID, runID string) error {
	gate, err := checkSlackInvocation(ctx, tx, conversationID, actorID)
	if err != nil || gate == nil {
		return err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT count(*)>0 AND COALESCE(bool_and(COALESCE(
 s.route_id=$3::uuid AND s.actor_id=$4::uuid AND s.identity_id=$5::uuid AND s.identity_version=$6,false)),false)
 FROM openfox.conversation_inputs i
 LEFT JOIN openfox.slack_input_authorizations s ON (s.organization_id,s.message_id)=(i.organization_id,i.message_id)
 WHERE i.organization_id=openfox_private.current_organization_id() AND i.conversation_id=$1::uuid AND i.run_id=$2::uuid`,
		conversationID, runID, gate.routeID, actorID, gate.identityID, gate.identityVersion).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
