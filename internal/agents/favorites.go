package agents

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/tenancy"
)

// 侧栏收藏（☆）：个人偏好，与目录置顶 agent_directory_pins 是两个概念（迁移 000028 agent_favorites）。

// AddFavorite 幂等收藏；目标须对当前成员可见（否则 ErrNotFound），已归档返回 ErrArchived。
func (s PostgresStore) AddFavorite(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, target PinTarget) error {
	agentID, crewID, err := s.favoriteTarget(selection, actor, target)
	if err != nil {
		return err
	}
	role := strings.ToUpper(string(actor.Role))
	return s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var archived *time.Time
		var lookup, id string
		if agentID != "" {
			id = agentID
			lookup = `SELECT a.archived_at FROM openfox.agents a
			  JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
			 WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = $2::uuid
			   AND ` + directorySpaceSQL + ` AND ` + directoryAgentVisibleSQL
		} else {
			id = crewID
			lookup = `SELECT cr.archived_at FROM openfox.agent_crews cr
			  JOIN openfox.spaces s ON s.organization_id = cr.organization_id AND s.id = cr.space_id
			 WHERE cr.organization_id = openfox_private.current_organization_id() AND cr.id = $2::uuid
			   AND ` + crewVisibleSQL
		}
		if err := tx.QueryRow(ctx, lookup, role, id).Scan(&archived); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if archived != nil {
			return ErrArchived
		}
		if agentID != "" {
			_, err = tx.Exec(ctx, `
				INSERT INTO openfox.agent_favorites (organization_id, user_id, agent_id)
				VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid)
				ON CONFLICT (organization_id, user_id, agent_id) WHERE agent_id IS NOT NULL DO NOTHING`, id)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO openfox.agent_favorites (organization_id, user_id, crew_id)
				VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid)
				ON CONFLICT (organization_id, user_id, crew_id) WHERE crew_id IS NOT NULL DO NOTHING`, id)
		}
		return err
	})
}

// RemoveFavorite 幂等取消收藏；只删除本人的行（RLS 兜底），不要求目标仍可见。
func (s PostgresStore) RemoveFavorite(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, target PinTarget) error {
	agentID, crewID, err := s.favoriteTarget(selection, actor, target)
	if err != nil {
		return err
	}
	return s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM openfox.agent_favorites
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND user_id = openfox_private.current_actor_id()
			   AND (agent_id = $1::uuid OR crew_id = $2::uuid)`, nullableUUID(agentID), nullableUUID(crewID))
		return err
	})
}

func (s PostgresStore) favoriteTarget(selection tenancy.OrganizationSelection, actor Actor, target PinTarget) (string, string, error) {
	if err := s.validate(selection, actor); err != nil {
		return "", "", err
	}
	agentID, crewID := strings.TrimSpace(target.AgentID), strings.TrimSpace(target.CrewID)
	if (agentID == "") == (crewID == "") {
		return "", "", ErrInvalidRequest
	}
	var err error
	if agentID != "" {
		agentID, err = normalizeUUID(agentID)
	} else {
		crewID, err = normalizeUUID(crewID)
	}
	if err != nil {
		return "", "", ErrInvalidRequest
	}
	return agentID, crewID, nil
}
