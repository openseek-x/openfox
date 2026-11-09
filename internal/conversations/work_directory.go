package conversations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"openfox/internal/workdirpath"
)

type WorkDirectoryTarget struct {
	Target    *workdirpath.Scope `json:"target"`
	Supported bool               `json:"supported"`
	Online    bool               `json:"online"`
}

func (s Service) WorkDirectory(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) (WorkDirectoryTarget, error) {
	conversation, err := s.Get(ctx, tx, organizationID, conversationID, actor)
	if err != nil {
		return WorkDirectoryTarget{}, err
	}
	scope := workdirpath.Scope{OrganizationID: organizationID, ConversationID: conversation.ID, UserID: actor.UserID}
	result := WorkDirectoryTarget{}
	err = tx.QueryRow(ctx, `SELECT r.id::text, r.generation, r.computer_id::text, r.space_id::text, r.agent_id::text
		FROM openfox.runs r WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1::uuid
		AND (r.agent_id=$2::uuid OR NOT EXISTS(SELECT 1 FROM openfox.agents a
		 WHERE a.organization_id=r.organization_id AND a.id=$2::uuid AND a.builtin_kind='ASSISTANT'))
		ORDER BY (r.state IN ('QUEUED','PREPARING','RUNNING','CANCEL_REQUESTED')) DESC, r.created_at DESC, r.id DESC LIMIT 1`, conversationID, conversation.AgentID).
		Scan(&scope.RunID, &scope.Generation, &scope.DeviceID, &scope.SpaceID, &scope.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, `SELECT COALESCE(d.capabilities->'workspace_path' = 'true'::jsonb, false),
		COALESCE(d.last_seen_at >= clock_timestamp() - interval '90 seconds', false)
		FROM openfox.physical_devices d JOIN openfox.device_grants g ON g.device_id=d.id
		WHERE g.organization_id=openfox_private.current_organization_id() AND d.id=$1::uuid
		AND d.status='ACTIVE' AND g.status='ACTIVE' AND d.owner_user_id=$2::uuid`, scope.DeviceID, actor.UserID).
		Scan(&result.Supported, &result.Online)
	if errors.Is(err, pgx.ErrNoRows) {
		return WorkDirectoryTarget{}, ErrNotFound
	}
	if err != nil {
		return WorkDirectoryTarget{}, err
	}
	result.Target = &scope
	return result, nil
}
