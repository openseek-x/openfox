package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// ProfileInput 是 PATCH …/agents/{a}/profile：资料、触发开关与访问权限（空间 / 可见性 / 所有者）。
// 乐观锁用 ProfileVersion，与配置版本链使用的 Version 互不影响。
type ProfileInput struct {
	Name            *string
	Title           *string
	Description     *string
	Avatar          collaboration.OptionalRaw
	AllowAssign     *bool
	AllowDirectChat *bool
	TriggerCrew     *bool
	TriggerMention  *bool
	TriggerWorkflow *bool
	SpaceID         *string
	Visibility      *Visibility
	OwnerUserID     *string
	ProfileVersion  uint64
}

// maxAvatarBytes 与 agents_avatar_shape 约束一致（pg_column_size ≤ 1024）。
const maxAvatarBytes = 1024

func (s PostgresStore) UpdateProfile(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string, input ProfileInput) (Agent, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return Agent{}, err
	}
	if input.ProfileVersion == 0 {
		return Agent{}, ErrInvalidRequest
	}
	avatar, err := normalizeAvatar(input.Avatar)
	if err != nil {
		return Agent{}, err
	}
	var result Agent
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockEditableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor, false)
		if err != nil {
			return err
		}
		if current.ProfileVersion != input.ProfileVersion {
			return ErrVersionConflict
		}
		next := current
		if input.Name != nil {
			next.Name = strings.TrimSpace(*input.Name)
		}
		if input.Description != nil {
			next.Description = strings.TrimSpace(*input.Description)
		}
		if input.Visibility != nil {
			next.Visibility = *input.Visibility
		}
		if err := validateAgentFields(next.Name, next.Description, next.Kind, next.Visibility); err != nil {
			return err
		}
		if input.Title != nil {
			if next.Title, err = validateTitle(*input.Title); err != nil {
				return err
			}
		}
		if input.Avatar.Set {
			if err := validateAvatarReference(ctx, tx, selection.OrganizationID, avatar); err != nil {
				return err
			}
			next.Avatar = avatar
		}
		for target, value := range map[*bool]*bool{&next.AllowAssign: input.AllowAssign, &next.AllowDirectChat: input.AllowDirectChat,
			&next.TriggerCrew: input.TriggerCrew, &next.TriggerMention: input.TriggerMention, &next.TriggerWorkflow: input.TriggerWorkflow} {
			if value != nil {
				*target = *value
			}
		}
		if input.SpaceID != nil {
			target, err := normalizeUUID(*input.SpaceID)
			if err != nil {
				return ErrInvalidRequest
			}
			next.SpaceID = target
		}
		if input.OwnerUserID != nil {
			owner, err := normalizeUUID(*input.OwnerUserID)
			if err != nil {
				return ErrInvalidOwner
			}
			next.OwnerUserID = owner
		}
		spaceChanged, ownerChanged := next.SpaceID != current.SpaceID, next.OwnerUserID != current.OwnerUserID
		if spaceChanged {
			// 移到新空间：操作人在新空间也须有管理权限（不可读的空间按不存在处理）。
			if err := authorizeSpace(ctx, tx, selection.OrganizationID, next.SpaceID, actor, policy.ActionReadSpace, true); err != nil {
				return err
			}
			if err := authorizeSpace(ctx, tx, selection.OrganizationID, next.SpaceID, actor, policy.ActionManageSpace, false); err != nil {
				return err
			}
		}
		if spaceChanged || ownerChanged {
			if ok, err := membersCanReadSpace(ctx, tx, next.SpaceID, []string{next.OwnerUserID}); err != nil {
				return err
			} else if !ok {
				return ErrInvalidOwner
			}
			if err := checkGrantScope(ctx, tx, selection.OrganizationID, agentID, next.SpaceID, next.OwnerUserID); err != nil {
				return err
			}
		}
		if spaceChanged {
			if err := leaveCrewsOutsideSpace(ctx, tx, agentID, next.SpaceID); err != nil {
				return err
			}
		}
		var avatarArg any
		if len(next.Avatar) > 0 {
			avatarArg = string(next.Avatar)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.agents
			   SET name = $3, title = $4, description = $5, avatar = $6::jsonb,
			       allow_assign = $7, allow_direct_chat = $8, trigger_crew = $9, trigger_mention = $10,
			       trigger_workflow = $11, space_id = $12::uuid, owner_user_id = $13::uuid, visibility = $14,
			       profile_version = profile_version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = $1::uuid AND id = $2::uuid AND profile_version = $15`,
			selection.OrganizationID, agentID, next.Name, next.Title, next.Description, avatarArg,
			next.AllowAssign, next.AllowDirectChat, next.TriggerCrew, next.TriggerMention, next.TriggerWorkflow,
			next.SpaceID, next.OwnerUserID, next.Visibility, int64(input.ProfileVersion))
		if err != nil {
			return mapDatabaseError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		if err := recordAudit(ctx, tx, selection, actor, "agent.profile_updated", agentID, next.SpaceID, input.ProfileVersion+1); err != nil {
			return err
		}
		result, err = loadAgent(ctx, tx, selection.OrganizationID, next.SpaceID, agentID, actor.UserID, false)
		return err
	})
	return result, err
}

// normalizeAvatar：null 清除头像；否则必须是不超过 1 KiB 的 JSON 对象（紧凑化后存储）。
func normalizeAvatar(input collaboration.OptionalRaw) (json.RawMessage, error) {
	if !input.Set || input.Value == nil {
		return nil, nil
	}
	var object map[string]any
	if err := json.Unmarshal(input.Value, &object); err != nil || object == nil {
		return nil, ErrInvalidRequest
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, input.Value); err != nil || compact.Len() > maxAvatarBytes {
		return nil, ErrInvalidRequest
	}
	return compact.Bytes(), nil
}

func validateAvatarReference(ctx context.Context, tx pgx.Tx, organizationID string, avatar json.RawMessage) error {
	var object map[string]any
	if len(avatar) == 0 {
		return nil
	}
	if err := json.Unmarshal(avatar, &object); err != nil {
		return ErrInvalidRequest
	}
	if object["kind"] != "upload" {
		return nil
	}
	value, ok := object["value"].(string)
	prefix := collaboration.IconURL(organizationID, "")
	if !ok || !strings.HasPrefix(value, prefix) {
		return ErrInvalidRequest
	}
	return collaboration.RequireUploadedIcon(ctx, tx, strings.TrimPrefix(value, prefix))
}

// leaveCrewsOutsideSpace：Agent 换空间时退出其他空间的小队；若它是那些小队的指定队长则拒绝。
func leaveCrewsOutsideSpace(ctx context.Context, tx pgx.Tx, agentID, spaceID string) error {
	var leads bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.agent_crews
		                WHERE organization_id = openfox_private.current_organization_id()
		                  AND leader_agent_id = $1::uuid AND space_id <> $2::uuid)`, agentID, spaceID).Scan(&leads); err != nil {
		return err
	}
	if leads {
		return ErrCrewLeaderRequired
	}
	rows, err := tx.Query(ctx, `
		DELETE FROM openfox.agent_crew_members m
		 USING openfox.agent_crews cr
		 WHERE m.organization_id = openfox_private.current_organization_id()
		   AND cr.organization_id = m.organization_id AND cr.id = m.crew_id
		   AND m.agent_id = $1::uuid AND cr.space_id <> $2::uuid
		RETURNING m.crew_id::text`, agentID, spaceID)
	if err != nil {
		return err
	}
	var crews []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		crews = append(crews, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(crews) == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.agent_crews SET version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[])`, crews)
	return err
}
