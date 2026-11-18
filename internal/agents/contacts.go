package agents

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/tenancy"
)

// 成员页签 GET /contacts：Agent 与小队按可读空间分组，真人为组织 ACTIVE 成员。

// MaxContacts 是成员页签的总条目上限；超出时 Truncated 为 true。
const MaxContacts = 1000

const (
	ContactGroupSpace      = "SPACE"
	ContactGroupUnassigned = "UNASSIGNED"
	ContactGroupHumans     = "HUMANS"
)

type ContactOwner struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

type ContactItem struct {
	Kind            string          `json:"kind"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Title           *string         `json:"title"`
	Description     string          `json:"description"`
	Email           *string         `json:"email"`
	Avatar          json.RawMessage `json:"avatar"`
	Online          *bool           `json:"online"`
	SpaceID         *string         `json:"space_id"`
	Owner           *ContactOwner   `json:"owner"`
	Runtime         *string         `json:"runtime"`
	AllowAssign     *bool           `json:"allow_assign"`
	AllowDirectChat *bool           `json:"allow_direct_chat"`
	CreatedByMe     bool            `json:"created_by_me"`
	// lastConversationAt 只用于组内排序。
	lastConversationAt *time.Time
}

type ContactGroup struct {
	Kind      string        `json:"kind"`
	SpaceID   *string       `json:"space_id"`
	SpaceName *string       `json:"space_name"`
	Items     []ContactItem `json:"items"`
}

type Contacts struct {
	Groups    []ContactGroup `json:"groups"`
	Truncated bool           `json:"truncated"`
}

func (s PostgresStore) Contacts(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor) (Contacts, error) {
	if err := s.validate(selection, actor); err != nil {
		return Contacts{}, err
	}
	role := strings.ToUpper(string(actor.Role))
	type spaceKey struct{ id, name string }
	var agentsBySpace, crewsBySpace map[spaceKey][]ContactItem
	var humans []ContactItem
	truncated := false
	err := s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		agentsBySpace, crewsBySpace = map[spaceKey][]ContactItem{}, map[spaceKey][]ContactItem{}
		rows, err := tx.Query(ctx, `
			SELECT a.id::text, a.name, a.description, a.title, a.avatar, `+agentOnlineSQL+`, a.space_id::text, s.name,
			       a.owner_user_id::text, COALESCE(u.email, ''), c.runtime, a.allow_assign, a.allow_direct_chat,
			       (SELECT max(cv.updated_at) FROM openfox.conversations cv
			         WHERE cv.organization_id = a.organization_id AND cv.agent_id = a.id
			           AND cv.owner_user_id = openfox_private.current_actor_id())
			  FROM openfox.agents a
			  JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
			  LEFT JOIN openfox.agent_config_versions c
			    ON c.organization_id = a.organization_id AND c.agent_id = a.id
			   AND c.config_version = a.current_config_version
			  LEFT JOIN openfox.users u ON u.id = a.owner_user_id
			 WHERE a.organization_id = openfox_private.current_organization_id()
			   AND a.archived_at IS NULL AND `+directorySpaceSQL+` AND `+directoryAgentVisibleSQL+`
			   AND `+agentNotExclusiveSQL+`
			 ORDER BY a.created_at, a.id
			 LIMIT $2`, role, MaxContacts+1)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			var item ContactItem
			var avatar []byte
			var online, allowAssign, allowChat bool
			var spaceID, spaceName string
			var owner ContactOwner
			var title string
			if err := rows.Scan(&item.ID, &item.Name, &item.Description, &title, &avatar, &online, &spaceID, &spaceName,
				&owner.ID, &owner.Email, &item.Runtime, &allowAssign, &allowChat, &item.lastConversationAt); err != nil {
				rows.Close()
				return err
			}
			item.Kind, item.Title, item.Online, item.SpaceID, item.Owner = "agent", &title, &online, &spaceID, &owner
			item.AllowAssign, item.AllowDirectChat = &allowAssign, &allowChat
			item.CreatedByMe = owner.ID == actor.UserID
			if len(avatar) > 0 {
				item.Avatar = avatar
			}
			key := spaceKey{spaceID, spaceName}
			agentsBySpace[key] = append(agentsBySpace[key], item)
			count++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			SELECT cr.id::text, cr.name, cr.description, cr.space_id::text, s.name, cr.owner_user_id::text, COALESCE(u.email, ''),
			       EXISTS (SELECT 1 FROM openfox.agent_crew_members m
			                 JOIN openfox.agents a ON a.organization_id = m.organization_id AND a.id = m.agent_id
			                 LEFT JOIN openfox.agent_config_versions c
			                   ON c.organization_id = a.organization_id AND c.agent_id = a.id
			                  AND c.config_version = a.current_config_version
			                WHERE m.organization_id = cr.organization_id AND m.crew_id = cr.id
			                  AND a.archived_at IS NULL AND `+directoryAgentVisibleSQL+` AND `+agentOnlineSQL+`)
			  FROM openfox.agent_crews cr
			  JOIN openfox.spaces s ON s.organization_id = cr.organization_id AND s.id = cr.space_id
			  LEFT JOIN openfox.users u ON u.id = cr.owner_user_id
			 WHERE cr.organization_id = openfox_private.current_organization_id()
			   AND cr.archived_at IS NULL AND `+crewVisibleSQL+`
			 ORDER BY cr.created_at, cr.id
			 LIMIT $2`, role, MaxContacts+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item ContactItem
			var online bool
			var spaceID, spaceName string
			var owner ContactOwner
			if err := rows.Scan(&item.ID, &item.Name, &item.Description, &spaceID, &spaceName, &owner.ID, &owner.Email, &online); err != nil {
				rows.Close()
				return err
			}
			item.Kind, item.Online, item.SpaceID, item.Owner = "crew", &online, &spaceID, &owner
			item.CreatedByMe = owner.ID == actor.UserID
			key := spaceKey{spaceID, spaceName}
			crewsBySpace[key] = append(crewsBySpace[key], item)
			count++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `
			SELECT om.user_id::text, u.email
			  FROM openfox.organization_members om
			  JOIN openfox.users u ON u.id = om.user_id
			 WHERE om.organization_id = openfox_private.current_organization_id() AND om.status = 'ACTIVE'
			 ORDER BY u.email, om.user_id
			 LIMIT $1`, MaxContacts+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, email string
			if err := rows.Scan(&id, &email); err != nil {
				return err
			}
			emailValue := email
			humans = append(humans, ContactItem{Kind: "user", ID: id, Name: email, Email: &emailValue, CreatedByMe: id == actor.UserID})
			count++
		}
		truncated = count > MaxContacts
		return rows.Err()
	})
	if err != nil {
		return Contacts{}, err
	}
	keys := make([]spaceKey, 0, len(agentsBySpace)+len(crewsBySpace))
	seen := map[spaceKey]bool{}
	for _, group := range []map[spaceKey][]ContactItem{crewsBySpace, agentsBySpace} {
		for key := range group {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].name != keys[j].name {
			return keys[i].name < keys[j].name
		}
		return keys[i].id < keys[j].id
	})
	result := Contacts{Groups: make([]ContactGroup, 0, len(keys)+1), Truncated: truncated}
	remaining := MaxContacts
	take := func(items []ContactItem) []ContactItem {
		if len(items) > remaining {
			items = items[:remaining]
		}
		remaining -= len(items)
		return items
	}
	for _, key := range keys {
		crews, agentItems := crewsBySpace[key], agentsBySpace[key]
		sortContacts(crews)
		sortContacts(agentItems)
		items := take(append(append(make([]ContactItem, 0, len(crews)+len(agentItems)), crews...), agentItems...))
		if len(items) == 0 {
			continue
		}
		id, name := key.id, key.name
		result.Groups = append(result.Groups, ContactGroup{Kind: ContactGroupSpace, SpaceID: &id, SpaceName: &name, Items: items})
	}
	// 目前所有受管 Agent 都属于空间，UNASSIGNED 恒为空，不输出空组。
	if items := take(humans); len(items) > 0 {
		result.Groups = append(result.Groups, ContactGroup{Kind: ContactGroupHumans, Items: items})
	}
	return result, nil
}

// sortContacts：我创建的优先 → 我与它最近的会话时间（新的在前，没有会话的在后）→ 名称 → ID。
func sortContacts(items []ContactItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.CreatedByMe != b.CreatedByMe {
			return a.CreatedByMe
		}
		switch {
		case a.lastConversationAt != nil && b.lastConversationAt == nil:
			return true
		case a.lastConversationAt == nil && b.lastConversationAt != nil:
			return false
		case a.lastConversationAt != nil && !a.lastConversationAt.Equal(*b.lastConversationAt):
			return a.lastConversationAt.After(*b.lastConversationAt)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
}
