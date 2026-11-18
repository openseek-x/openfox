package integrations

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"openfox/internal/collaboration"
	"openfox/internal/conversations"
	"openfox/internal/membership"
)

type WeComGroup struct {
	ChatID         string     `json:"chat_id"`
	Version        int64      `json:"version"`
	Enabled        bool       `json:"enabled"`
	ConversationID string     `json:"conversation_id"`
	LastSeenAt     *time.Time `json:"last_seen_at"`
}
type WeComGroups struct {
	Groups     []WeComGroup `json:"groups"`
	NextCursor string       `json:"next_cursor"`
}
type WeComGroupPatch struct {
	ChatID          string `json:"chat_id"`
	ExpectedVersion *int64 `json:"expected_version"`
	Enabled         *bool  `json:"enabled"`
}

func validWeComChatID(id string) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id && strings.IndexFunc(id, unicode.IsControl) < 0
}
func (s Service) manageableWeComAssistant(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, lock bool) (botRow, error) {
	if err := requireFeishuMember(ctx, tx, org, actor); err != nil {
		return botRow{}, err
	}
	r, err := loadBotRow(ctx, tx, ProviderWeCom, botID, lock)
	if err != nil {
		return r, err
	}
	if r.bot.Kind != BotKindTaskAssistant {
		return r, ErrNotFound
	}
	can, err := canManageBot(ctx, tx, org, actor, r, map[string]bool{})
	if err != nil {
		return r, err
	}
	if !can {
		return r, ErrForbidden
	}
	return r, nil
}

// The same keyset page includes authorized groups and recent discovery metadata.
// Group names/user identities/bodies are never guessed or exposed here.
func (s Service) WeComGroups(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID, after string) (WeComGroups, error) {
	if after != "" && !validWeComChatID(after) {
		return WeComGroups{}, ErrInvalidRequest
	}
	if _, err := s.manageableWeComAssistant(ctx, tx, org, actor, botID, false); err != nil {
		return WeComGroups{}, err
	}
	rows, err := tx.Query(ctx, `WITH ids AS (
 (SELECT DISTINCT chat_id FROM openfox.wecom_group_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id>$2 ORDER BY chat_id LIMIT 51)
 UNION (SELECT DISTINCT chat_id FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_type='group' AND received_at>clock_timestamp()-interval '7 days' AND chat_id>$2 ORDER BY chat_id LIMIT 51)
 ) SELECT i.chat_id,coalesce(a.version,0),coalesce(a.enabled,false),coalesce(a.conversation_id::text,''),m.last_seen
 FROM (SELECT chat_id FROM ids ORDER BY chat_id LIMIT 51) i
 LEFT JOIN LATERAL(SELECT version,enabled,conversation_id FROM openfox.wecom_group_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=i.chat_id ORDER BY version DESC LIMIT 1) a ON true
 LEFT JOIN LATERAL(SELECT max(received_at) last_seen FROM openfox.im_inbound_messages WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=i.chat_id AND chat_type='group') m ON true
 ORDER BY i.chat_id`, botID, after)
	if err != nil {
		return WeComGroups{}, err
	}
	defer rows.Close()
	out := WeComGroups{Groups: []WeComGroup{}}
	for rows.Next() {
		var g WeComGroup
		if err := rows.Scan(&g.ChatID, &g.Version, &g.Enabled, &g.ConversationID, &g.LastSeenAt); err != nil {
			return out, err
		}
		out.Groups = append(out.Groups, g)
	}
	if len(out.Groups) > 50 {
		out.NextCursor = out.Groups[49].ChatID
		out.Groups = out.Groups[:50]
	}
	return out, rows.Err()
}

func (s Service) PatchWeComGroup(ctx context.Context, tx pgx.Tx, org string, actor collaboration.Actor, botID string, in WeComGroupPatch) (WeComGroup, error) {
	if !validWeComChatID(in.ChatID) || in.ExpectedVersion == nil || *in.ExpectedVersion < 0 || in.Enabled == nil {
		return WeComGroup{}, ErrInvalidRequest
	}
	// Bot -> authorization: serializes first creation and the persistent version counter.
	bot, err := s.manageableWeComAssistant(ctx, tx, org, actor, botID, true)
	if err != nil {
		return WeComGroup{}, err
	}
	current := WeComGroup{ChatID: in.ChatID}
	err = tx.QueryRow(ctx, `SELECT version,enabled,conversation_id::text FROM openfox.wecom_group_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 ORDER BY version DESC LIMIT 1 FOR UPDATE`, botID, in.ChatID).Scan(&current.Version, &current.Enabled, &current.ConversationID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return current, err
	}
	if current.Version != *in.ExpectedVersion {
		return current, ErrVersionConflict
	}
	if current.Enabled == *in.Enabled {
		return current, nil
	}
	if *in.Enabled {
		var active, seen bool
		err = tx.QueryRow(ctx, `SELECT c.status='ACTIVE',EXISTS(SELECT 1 FROM openfox.im_inbound_messages i WHERE i.organization_id=c.organization_id AND i.bot_id=$2::uuid AND i.chat_id=$3 AND i.chat_type='group' AND i.received_at>clock_timestamp()-interval '7 days') FROM openfox.integration_connections c WHERE c.organization_id=openfox_private.current_organization_id() AND c.id=$1::uuid`, bot.bot.ConnectionID, botID, in.ChatID).Scan(&active, &seen)
		if err != nil {
			return current, err
		}
		if !active {
			return current, ErrNeedsReauth
		}
		if current.Version == 0 && !seen {
			return current, ErrNotFound
		}
		id, err := membership.NewID()
		if err != nil {
			return current, err
		}
		_, err = (conversations.Service{}).Create(ctx, tx, org, conversations.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion}, conversations.CreateInput{ID: id, SpaceID: bot.spaceID, AgentID: bot.bot.Agent.ID, Visibility: conversations.VisibilityShared, Origin: conversations.OriginIM})
		if errors.Is(err, conversations.ErrForbidden) {
			return current, ErrForbidden
		}
		if errors.Is(err, conversations.ErrNotFound) {
			return current, ErrNotFound
		}
		if err != nil {
			return current, err
		}
		if _, err = tx.Exec(ctx, `UPDATE openfox.conversations SET title='企业微信群聊' WHERE organization_id=openfox_private.current_organization_id() AND id=$1::uuid`, id); err != nil {
			return current, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO openfox.wecom_group_authorizations(organization_id,bot_id,chat_id,conversation_id,version,enabled,updated_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,$3::uuid,$4,true,$5::uuid)`, botID, in.ChatID, id, current.Version+1, actor.UserID)
		if err != nil {
			return current, err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO openfox.im_chat_bindings(organization_id,bot_id,chat_id,chat_type,conversation_id,created_by) VALUES(openfox_private.current_organization_id(),$1::uuid,$2,'group',$3::uuid,$4::uuid)
 ON CONFLICT(organization_id,bot_id,chat_id) DO UPDATE SET conversation_id=EXCLUDED.conversation_id,created_by=EXCLUDED.created_by,created_at=clock_timestamp() WHERE im_chat_bindings.chat_type='group'`, botID, in.ChatID, id, actor.UserID)
		if err != nil {
			return current, err
		}
		if tag.RowsAffected() != 1 {
			return current, ErrVersionConflict
		}
		current.ConversationID = id
	} else {
		tag, err := tx.Exec(ctx, `UPDATE openfox.wecom_group_authorizations SET enabled=false,version=version+1,updated_by=$3::uuid,updated_at=clock_timestamp() WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND version=$2 AND enabled`, current.ConversationID, current.Version, actor.UserID)
		if err != nil {
			return current, err
		}
		if tag.RowsAffected() != 1 {
			return current, ErrVersionConflict
		}
	}
	current.Version++
	current.Enabled = *in.Enabled
	return current, nil
}

func wecomGroupVersionActive(ctx context.Context, tx pgx.Tx, botID, chatID string, version *int64) (bool, error) {
	if version == nil {
		return false, nil
	}
	var found int64
	err := tx.QueryRow(ctx, `SELECT version FROM openfox.wecom_group_authorizations WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND version=$3 AND enabled FOR SHARE`, botID, chatID, *version).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

var errWeComGroupDisabled = errors.New("integrations: wecom group authorization expired")

func wecomReplyGroupAllowed(ctx context.Context, tx pgx.Tx, item imOutboxItem) (bool, error) {
	if item.GroupVersion == nil {
		var group bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM openfox.im_chat_bindings WHERE organization_id=openfox_private.current_organization_id() AND bot_id=$1::uuid AND chat_id=$2 AND chat_type='group')`, item.BotID, item.ChatID).Scan(&group)
		return !group, err
	}
	var conversationID string
	err := tx.QueryRow(ctx, `SELECT ga.conversation_id::text FROM openfox.wecom_group_authorizations ga
 JOIN openfox.im_bots b ON b.organization_id=ga.organization_id AND b.id=ga.bot_id AND b.removed_at IS NULL AND b.kind='TASK_ASSISTANT'
 JOIN openfox.integration_connections c ON c.organization_id=b.organization_id AND c.id=b.connection_id AND c.status='ACTIVE' AND c.provider='wecom'
 JOIN openfox.im_chat_bindings cb ON cb.organization_id=ga.organization_id AND cb.bot_id=ga.bot_id AND cb.chat_id=ga.chat_id AND cb.conversation_id=ga.conversation_id AND cb.chat_type='group'
 WHERE ga.organization_id=openfox_private.current_organization_id() AND ga.bot_id=$1::uuid AND ga.chat_id=$2 AND ga.enabled AND ga.version=$3
 AND ($4='' OR EXISTS(SELECT 1 FROM openfox.messages m WHERE m.organization_id=ga.organization_id AND m.id=NULLIF($4,'')::uuid AND m.conversation_id=ga.conversation_id))
 FOR SHARE OF ga`, item.BotID, item.ChatID, *item.GroupVersion, item.MessageID).Scan(&conversationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}
