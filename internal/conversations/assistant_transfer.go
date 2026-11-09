package conversations

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var (
	ErrAssistantTransferNotReady = errors.New("conversations: recipient assistant is not ready for transfer")
	ErrAssistantTransferBusy     = errors.New("conversations: assistant transfer requires an idle conversation")
)

const assistantTransferTargetFrom = ` FROM openfox.agents a
 JOIN openfox.spaces assistant_space ON assistant_space.organization_id=a.organization_id AND assistant_space.id=a.space_id AND assistant_space.status='ACTIVE'
 JOIN openfox.agent_config_versions cfg ON cfg.organization_id=a.organization_id AND cfg.agent_id=a.id AND cfg.config_version=a.current_config_version
 JOIN openfox.physical_devices d ON d.id=cfg.computer_id AND d.id=a.device_id AND d.status='ACTIVE'
 JOIN openfox.device_grants g ON g.organization_id=a.organization_id AND g.id=cfg.grant_id AND g.device_id=d.id AND g.status='ACTIVE'
 WHERE a.organization_id=openfox_private.current_organization_id() AND a.owner_user_id=$1
 AND a.builtin_kind='ASSISTANT' AND a.visibility='PRIVATE' AND a.status='ACTIVE' AND a.archived_at IS NULL
 AND EXISTS(SELECT 1 FROM openfox.organization_members target_member
 WHERE target_member.organization_id=a.organization_id AND target_member.user_id=a.owner_user_id AND target_member.status='ACTIVE'
 AND (EXISTS(SELECT 1 FROM openfox.space_members sm WHERE sm.organization_id=a.organization_id AND sm.space_id=a.space_id
 AND sm.user_id=a.owner_user_id AND sm.status='ACTIVE')
 OR (assistant_space.visibility='SHARED' AND target_member.role<>'GUEST' AND NOT EXISTS(SELECT 1 FROM openfox.space_members sm
 WHERE sm.organization_id=a.organization_id AND sm.space_id=a.space_id AND sm.user_id=a.owner_user_id AND sm.status<>'ACTIVE'))))
 AND (g.scope='ORGANIZATION' OR (g.scope='PERSONAL' AND g.scope_user_id=$1)
 OR (g.scope='SPACE' AND g.scope_space_id=$2 AND g.scope_space_id=a.space_id))`

func assistantTransferTarget(ctx context.Context, tx pgx.Tx, recipient, space string) (string, error) {
	var agent string
	err := tx.QueryRow(ctx, `SELECT a.id::text`+assistantTransferTargetFrom+` FOR SHARE OF a,d,g,assistant_space`, recipient, space).Scan(&agent)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrAssistantTransferNotReady
	}
	return agent, err
}

func assistantTransferIdle(ctx context.Context, tx pgx.Tx, conversation string) error {
	var busy bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.runs
 WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1
 AND (state IN ('QUEUED','PREPARING','RUNNING','CANCEL_REQUESTED') OR reconciliation_required))
 OR EXISTS(SELECT 1 FROM openfox.conversation_inputs
 WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1 AND status='PENDING')
 OR EXISTS(SELECT 1 FROM openfox.run_reconciliation_cases rc JOIN openfox.runs r ON r.organization_id=rc.organization_id AND r.id=rc.run_id
 WHERE r.organization_id=openfox_private.current_organization_id() AND r.conversation_id=$1 AND rc.status='OPEN')`, conversation).Scan(&busy)
	if err != nil {
		return err
	}
	if busy {
		return ErrAssistantTransferBusy
	}
	return nil
}
