package integrations

import (
	"context"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
)

// Personal targets expose only visible Bot labels and readiness, not manager
// controls, a token, webhook coordinates or another member's identity.
type TelegramPersonalTarget struct {
	BotID    string       `json:"bot_id"`
	Name     string       `json:"name"`
	Username string       `json:"username"`
	Kind     string       `json:"kind"`
	Agent    FeishuBotRef `json:"agent"`
	Space    FeishuBotRef `json:"space"`
	CanBind  bool         `json:"can_bind"`
}

func (s Service) TelegramPersonalTargets(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor) ([]TelegramPersonalTarget, error) {
	if err := telegramActor(ctx, tx, org, actor, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT b.id::text,c.display_name,t.username,b.kind,a.id::text,a.name,a.space_id::text,s.name,
 t.state='ACTIVE' AND c.status='ACTIVE' FROM openfox.telegram_bot_controls t
 JOIN openfox.im_bots b ON b.organization_id=t.organization_id AND b.id=t.bot_id
 JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id
 JOIN openfox.agents a ON a.organization_id=b.organization_id AND a.id=b.agent_id
 JOIN openfox.spaces s ON s.organization_id=a.organization_id AND s.id=a.space_id
 WHERE t.organization_id=openfox_private.current_organization_id() AND b.removed_at IS NULL AND a.archived_at IS NULL AND a.status='ACTIVE' AND s.retired_at IS NULL
 AND (a.visibility='SHARED' OR a.owner_user_id=$1::uuid) ORDER BY b.created_at,b.id LIMIT 201`, actor.UserID)
	if err != nil {
		return nil, err
	}
	all := []TelegramPersonalTarget{}
	for rows.Next() {
		var v TelegramPersonalTarget
		if err = rows.Scan(&v.BotID, &v.Name, &v.Username, &v.Kind, &v.Agent.ID, &v.Agent.Name, &v.Space.ID, &v.Space.Name, &v.CanBind); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(all) > 200 {
		return nil, ErrInvalidRequest
	}
	out := []TelegramPersonalTarget{}
	permissions := map[string]bool{}
	for _, v := range all {
		can, known := permissions[v.Space.ID]
		if !known {
			can, err = (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), v.Space.ID)
			if err != nil {
				return nil, err
			}
			permissions[v.Space.ID] = can
		}
		if can {
			out = append(out, v)
		}
	}
	return out, nil
}

func telegramCheckBindingTarget(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, r telegramControl) error {
	var visible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.agents a JOIN openfox.spaces s ON s.organization_id=a.organization_id AND s.id=a.space_id
 WHERE a.organization_id=openfox_private.current_organization_id() AND a.id=$1::uuid AND a.archived_at IS NULL AND a.status='ACTIVE' AND s.retired_at IS NULL
 AND(a.visibility='SHARED' OR a.owner_user_id=$2::uuid))`, r.bot.Agent.ID, actor.UserID).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return ErrForbidden
	}
	visible, err = (conversations.Service{}).CanReadSpace(ctx, tx, org, conversations.Actor(actor), r.spaceID)
	if err != nil {
		return err
	}
	if !visible {
		return ErrForbidden
	}
	return nil
}
