package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/tenancy"
)

// 首页「全部 Agent 和小队」：跨空间列出当前成员可见的 Agent 与小队，带个人置顶标记。

type DirectoryType string

const (
	DirectoryTypeAgent DirectoryType = "agent"
	DirectoryTypeCrew  DirectoryType = "crew"
)

// MaxDirectoryEntries 是 Agent 与小队各自的单次上限；超出时 Truncated 为 true。
const MaxDirectoryEntries = 500

// MaxCrewMembers 与 agent_crew_members_position_valid 约束一致。
const MaxCrewMembers = 32

// DirectoryMember 是小队成员；Kind 为 Agent 的类型，真人成员为 HUMAN（Name 为邮箱）。
type DirectoryMember struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Kind  Kind    `json:"kind"`
	Title *string `json:"title"`
}

// KindHuman 只用于目录与小队成员里的真人成员。
const KindHuman Kind = "HUMAN"

type DirectoryEntry struct {
	Type        DirectoryType   `json:"type"`
	ID          string          `json:"id"`
	SpaceID     string          `json:"space_id"`
	SpaceName   string          `json:"space_name"`
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Avatar      json.RawMessage `json:"avatar"`
	Description string          `json:"description"`
	AgentKind   Kind            `json:"agent_kind,omitempty"`
	Visibility  Visibility      `json:"visibility"`
	OwnerUserID string          `json:"owner_user_id"`
	OwnerEmail  string          `json:"owner_email"`
	// Runtime / Model / ComputerName 只对 Agent 有值；小队为 null。
	Runtime      *string `json:"runtime"`
	Model        *string `json:"model"`
	ComputerName *string `json:"computer_name"`
	// Online：Agent = 电脑在线；小队 = 任一可见 Agent 成员在线。
	Online   bool         `json:"online"`
	Pinned   bool         `json:"pinned"`
	Favorite bool         `json:"favorite"`
	Mine     bool         `json:"mine"`
	Leader   *AgentLeader `json:"leader"`
	// AllowAssign / AllowDirectChat 只对 Agent 有值；小队为 null。
	AllowAssign     *bool             `json:"allow_assign"`
	AllowDirectChat *bool             `json:"allow_direct_chat"`
	MemberCount     int               `json:"member_count"`
	Members         []DirectoryMember `json:"members,omitempty"`
	ArchivedAt      *time.Time        `json:"archived_at,omitempty"`
	Version         uint64            `json:"version"`
	CreatedAt       time.Time         `json:"created_at"`
}

type Directory struct {
	Entries       []DirectoryEntry `json:"entries"`
	ArchivedCount int              `json:"archived_count"`
	Truncated     bool             `json:"truncated"`
}

type DirectoryOptions struct {
	// Archived 为 true 时只列已归档的条目（「显示已归档」对话框）。
	Archived bool
}

// PinTarget 只能填一个：Agent 或小队。
type PinTarget struct {
	AgentID string
	CrewID  string
}

type CrewInput struct {
	Name          string
	Description   string
	Visibility    Visibility
	LeaderAgentID string
	AgentIDs      []string
	UserIDs       []string
}

type CrewPatch struct {
	Name          *string
	Description   *string
	Visibility    *Visibility
	SpaceID       *string
	OwnerUserID   *string
	Instructions  *string
	SharedTaskDir *bool
	LeaderAgentID *string
	Archived      *bool
	Version       uint64
}

// 当前成员可读的空间（s 为空间别名，$1 为大写组织角色）以及 Agent 自身的可见性。
const directorySpaceSQL = `s.status = 'ACTIVE' AND ` + collaboration.SpaceReadableSQL

// 内置小助手不进入目录，也不能当小队成员。
const directoryAgentVisibleSQL = `(a.builtin_kind IS NULL AND (a.visibility = 'SHARED' OR a.owner_user_id = openfox_private.current_actor_id()))`

// agentNotExclusiveSQL：小队的专属 Agent 不单独出现在目录与成员页签。
const agentNotExclusiveSQL = `NOT EXISTS (SELECT 1 FROM openfox.agent_crew_members xm
	WHERE xm.organization_id = a.organization_id AND xm.agent_id = a.id AND xm.exclusive)`

func (s PostgresStore) Directory(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, options DirectoryOptions) (Directory, error) {
	if err := s.validate(selection, actor); err != nil {
		return Directory{}, err
	}
	role := strings.ToUpper(string(actor.Role))
	result := Directory{Entries: make([]DirectoryEntry, 0)}
	err := s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		agentRows, err := tx.Query(ctx, `
			SELECT a.id::text, a.space_id::text, s.name, a.name, a.description, a.kind, a.visibility,
			       a.owner_user_id::text, COALESCE(u.email, ''), c.model,
			       EXISTS (SELECT 1 FROM openfox.agent_directory_pins p
			                WHERE p.organization_id = a.organization_id
			                  AND p.user_id = openfox_private.current_actor_id() AND p.agent_id = a.id),
			       a.archived_at, a.version, a.created_at,
			       a.title, a.avatar, c.runtime, `+agentComputerNameSQL+`, `+agentOnlineSQL+`, `+agentFavoriteSQL+`,
			       a.allow_assign, a.allow_direct_chat
			  FROM openfox.agents a
			  JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
			  LEFT JOIN openfox.agent_config_versions c
			    ON c.organization_id = a.organization_id AND c.agent_id = a.id
			   AND c.config_version = a.current_config_version
			  LEFT JOIN openfox.users u ON u.id = a.owner_user_id
			 WHERE a.organization_id = openfox_private.current_organization_id()
			   AND a.space_id IS NOT NULL
			   AND `+directorySpaceSQL+`
			   AND `+directoryAgentVisibleSQL+` AND `+agentNotExclusiveSQL+`
			   AND (a.archived_at IS NOT NULL) = $2
			 ORDER BY a.created_at, a.id
			 LIMIT $3`, role, options.Archived, MaxDirectoryEntries+1)
		if err != nil {
			return err
		}
		agents, truncated, err := scanDirectoryRows(agentRows, actor.UserID, DirectoryTypeAgent)
		if err != nil {
			return err
		}
		crewRows, err := tx.Query(ctx, `
			SELECT cr.id::text, cr.space_id::text, s.name, cr.name, cr.description, '', cr.visibility,
			       cr.owner_user_id::text, COALESCE(u.email, ''), NULL::text,
			       EXISTS (SELECT 1 FROM openfox.agent_directory_pins p
			                WHERE p.organization_id = cr.organization_id
			                  AND p.user_id = openfox_private.current_actor_id() AND p.crew_id = cr.id),
			       cr.archived_at, cr.version, cr.created_at,
			       '', NULL::jsonb, NULL::text, NULL::text, false,
			       EXISTS (SELECT 1 FROM openfox.agent_favorites f
			                WHERE f.organization_id = cr.organization_id
			                  AND f.user_id = openfox_private.current_actor_id() AND f.crew_id = cr.id),
			       NULL::boolean, NULL::boolean, COALESCE(cr.leader_agent_id::text, '')
			  FROM openfox.agent_crews cr
			  JOIN openfox.spaces s ON s.organization_id = cr.organization_id AND s.id = cr.space_id
			  LEFT JOIN openfox.users u ON u.id = cr.owner_user_id
			 WHERE cr.organization_id = openfox_private.current_organization_id()
			   AND `+directorySpaceSQL+`
			   AND (cr.visibility = 'SHARED' OR cr.owner_user_id = openfox_private.current_actor_id())
			   AND (cr.archived_at IS NOT NULL) = $2
			 ORDER BY cr.created_at, cr.id
			 LIMIT $3`, role, options.Archived, MaxDirectoryEntries+1)
		if err != nil {
			return err
		}
		crews, crewsTruncated, err := scanDirectoryRows(crewRows, actor.UserID, DirectoryTypeCrew)
		if err != nil {
			return err
		}
		if err := fillCrewEntries(ctx, tx, crews); err != nil {
			return err
		}
		result.Entries = append(agents, crews...)
		result.Truncated = truncated || crewsTruncated
		// 归档数量只在活跃视图里需要（用于「显示已归档（n）」）。
		if options.Archived {
			result.ArchivedCount = len(result.Entries)
			return nil
		}
		return tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM openfox.agents a
			          JOIN openfox.spaces s ON s.organization_id = a.organization_id AND s.id = a.space_id
			         WHERE a.organization_id = openfox_private.current_organization_id()
			           AND a.archived_at IS NOT NULL AND `+directorySpaceSQL+` AND `+directoryAgentVisibleSQL+`
			           AND `+agentNotExclusiveSQL+`)
			     + (SELECT count(*) FROM openfox.agent_crews cr
			          JOIN openfox.spaces s ON s.organization_id = cr.organization_id AND s.id = cr.space_id
			         WHERE cr.organization_id = openfox_private.current_organization_id()
			           AND cr.archived_at IS NOT NULL AND `+directorySpaceSQL+`
			           AND (cr.visibility = 'SHARED' OR cr.owner_user_id = openfox_private.current_actor_id()))`,
			role).Scan(&result.ArchivedCount)
	})
	return result, err
}

func scanDirectoryRows(rows pgx.Rows, actorID string, kind DirectoryType) ([]DirectoryEntry, bool, error) {
	defer rows.Close()
	entries := make([]DirectoryEntry, 0)
	for rows.Next() {
		var item DirectoryEntry
		var agentKind, visibility, leaderID string
		var version int64
		var avatar []byte
		targets := []any{&item.ID, &item.SpaceID, &item.SpaceName, &item.Name, &item.Description,
			&agentKind, &visibility, &item.OwnerUserID, &item.OwnerEmail, &item.Model, &item.Pinned,
			&item.ArchivedAt, &version, &item.CreatedAt, &item.Title, &avatar, &item.Runtime,
			&item.ComputerName, &item.Online, &item.Favorite, &item.AllowAssign, &item.AllowDirectChat}
		if kind == DirectoryTypeCrew {
			targets = append(targets, &leaderID)
		}
		if err := rows.Scan(targets...); err != nil {
			return nil, false, err
		}
		if version <= 0 {
			return nil, false, ErrInvalidRequest
		}
		item.Type, item.Version = kind, uint64(version)
		item.AgentKind, item.Visibility = Kind(agentKind), Visibility(visibility)
		item.Mine = item.OwnerUserID == actorID
		if len(avatar) > 0 {
			item.Avatar = avatar
		}
		if kind == DirectoryTypeCrew {
			// 暂存指定队长 ID，fillCrewEntries 批量补成员后再解析队长。
			item.Leader = &AgentLeader{ID: leaderID}
		}
		entries = append(entries, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(entries) > MaxDirectoryEntries {
		return entries[:MaxDirectoryEntries], true, nil
	}
	return entries, false, nil
}

// SetPin 幂等地置顶或取消置顶；目标必须对当前成员可见，否则按不存在处理。
// 已归档的目标只允许取消置顶。
func (s PostgresStore) SetPin(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, target PinTarget, pinned bool) error {
	if err := s.validate(selection, actor); err != nil {
		return err
	}
	agentID, crewID := strings.TrimSpace(target.AgentID), strings.TrimSpace(target.CrewID)
	if (agentID == "") == (crewID == "") {
		return ErrInvalidRequest
	}
	var err error
	if agentID != "" {
		agentID, err = normalizeUUID(agentID)
	} else {
		crewID, err = normalizeUUID(crewID)
	}
	if err != nil {
		return ErrNotFound
	}
	role := strings.ToUpper(string(actor.Role))
	return s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		var archived *time.Time
		var lookup string
		var id string
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
			   AND ` + directorySpaceSQL + `
			   AND (cr.visibility = 'SHARED' OR cr.owner_user_id = openfox_private.current_actor_id())`
		}
		if err := tx.QueryRow(ctx, lookup, role, id).Scan(&archived); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if !pinned {
			_, err := tx.Exec(ctx, `
				DELETE FROM openfox.agent_directory_pins
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND user_id = openfox_private.current_actor_id()
				   AND (agent_id = $1::uuid OR crew_id = $1::uuid)`, id)
			return err
		}
		if archived != nil {
			return ErrArchived
		}
		if agentID != "" {
			_, err = tx.Exec(ctx, `
				INSERT INTO openfox.agent_directory_pins (organization_id, user_id, agent_id)
				VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid)
				ON CONFLICT (organization_id, user_id, agent_id) WHERE agent_id IS NOT NULL DO NOTHING`, id)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO openfox.agent_directory_pins (organization_id, user_id, crew_id)
				VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid)
				ON CONFLICT (organization_id, user_id, crew_id) WHERE crew_id IS NOT NULL DO NOTHING`, id)
		}
		return err
	})
}

// fillCrewEntries 批量补齐小队条目的成员（可见 Agent + 真人）、队长、人数与在线状态。
func fillCrewEntries(ctx context.Context, tx pgx.Tx, entries []DirectoryEntry) error {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	sets, err := loadCrewMembers(ctx, tx, ids)
	if err != nil {
		return err
	}
	for index := range entries {
		entry := &entries[index]
		explicit := ""
		if entry.Leader != nil {
			explicit = entry.Leader.ID
		}
		set := sets[entry.ID]
		entry.Leader = set.leader(explicit)
		entry.MemberCount = set.count()
		entry.Online = set.anyOnline()
		entry.Members = make([]DirectoryMember, 0, entry.MemberCount)
		for _, agent := range set.agents {
			title := agent.Title
			entry.Members = append(entry.Members, DirectoryMember{ID: agent.ID, Name: agent.Name, Kind: agent.Kind, Title: &title})
		}
		for _, human := range set.humans {
			entry.Members = append(entry.Members, DirectoryMember{ID: human.ID, Name: human.Email, Kind: KindHuman})
		}
	}
	return nil
}
