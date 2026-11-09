package agents

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/tenancy"
)

// 小队：队长、指令、任务运行设置、真人成员与专属 Agent（迁移 000028）。
// 队长：leader_agent_id 为空的旧小队取 position 最小的 Agent；设置队长后它排第一（position 0）。
// 专属 Agent（exclusive）：不出现在目录与成员页签、不能单聊、不能被单独指派为任务处理人。

type AgentLeader struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
}

type CrewMember struct {
	Kind        string   `json:"kind"`
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Title       *string  `json:"title"`
	Exclusive   bool     `json:"exclusive"`
	ActiveTasks []string `json:"active_tasks"`
	Online      *bool    `json:"online"`
}

// Crew 是小队详情；Type / Pinned / Mine 与目录条目同名字段语义一致，旧调用方可继续读取。
type Crew struct {
	Type             DirectoryType `json:"type"`
	ID               string        `json:"id"`
	SpaceID          string        `json:"space_id"`
	SpaceName        string        `json:"space_name"`
	Name             string        `json:"name"`
	Description      string        `json:"description"`
	Visibility       Visibility    `json:"visibility"`
	OwnerUserID      string        `json:"owner_user_id"`
	OwnerEmail       string        `json:"owner_email"`
	Leader           *AgentLeader  `json:"leader"`
	Instructions     string        `json:"instructions"`
	SharedTaskDir    bool          `json:"shared_task_dir"`
	Members          []CrewMember  `json:"members"`
	MemberCount      int           `json:"member_count"`
	RunningTaskCount int           `json:"running_task_count"`
	Pinned           bool          `json:"pinned"`
	Favorite         bool          `json:"favorite"`
	Mine             bool          `json:"mine"`
	ArchivedAt       *time.Time    `json:"archived_at"`
	Version          uint64        `json:"version"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

// crewAgentMember / crewHumanMember 是批量读取的成员行（只含当前成员可见、未归档的 Agent）。
type crewAgentMember struct {
	ID, Name, Title string
	Kind            Kind
	Exclusive       bool
	Online          bool
	Position        int
}

type crewHumanMember struct {
	ID, Email string
}

type crewMemberSet struct {
	agents []crewAgentMember
	humans []crewHumanMember
}

// leader 按规则选队长：指定队长可见时用它，否则取 position 最小的可见 Agent。
func (set *crewMemberSet) leader(explicitID string) *AgentLeader {
	if set == nil || len(set.agents) == 0 {
		return nil
	}
	chosen := set.agents[0]
	for _, agent := range set.agents {
		if agent.ID == explicitID {
			chosen = agent
			break
		}
	}
	return &AgentLeader{ID: chosen.ID, Name: chosen.Name, Title: chosen.Title}
}

func (set *crewMemberSet) anyOnline() bool {
	if set == nil {
		return false
	}
	for _, agent := range set.agents {
		if agent.Online {
			return true
		}
	}
	return false
}

func (set *crewMemberSet) count() int {
	if set == nil {
		return 0
	}
	return len(set.agents) + len(set.humans)
}

const activeRunStatesSQL = `('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED')`

// loadCrewMembers 一次读取多个小队的 Agent 成员与真人成员（各一条查询）。
func loadCrewMembers(ctx context.Context, tx pgx.Tx, crewIDs []string) (map[string]*crewMemberSet, error) {
	result := make(map[string]*crewMemberSet, len(crewIDs))
	if len(crewIDs) == 0 {
		return result, nil
	}
	for _, id := range crewIDs {
		result[id] = &crewMemberSet{}
	}
	rows, err := tx.Query(ctx, `
		SELECT m.crew_id::text, a.id::text, a.name, a.title, a.kind, m.exclusive, `+agentOnlineSQL+`, m.position
		  FROM openfox.agent_crew_members m
		  JOIN openfox.agents a ON a.organization_id = m.organization_id AND a.id = m.agent_id
		  LEFT JOIN openfox.agent_config_versions c
		    ON c.organization_id = a.organization_id AND c.agent_id = a.id
		   AND c.config_version = a.current_config_version
		 WHERE m.organization_id = openfox_private.current_organization_id()
		   AND m.crew_id = ANY($1::uuid[]) AND a.archived_at IS NULL AND `+directoryAgentVisibleSQL+`
		 ORDER BY m.crew_id, m.position, m.created_at, a.id`, crewIDs)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var crewID, kind string
		var item crewAgentMember
		if err := rows.Scan(&crewID, &item.ID, &item.Name, &item.Title, &kind, &item.Exclusive, &item.Online, &item.Position); err != nil {
			rows.Close()
			return nil, err
		}
		item.Kind = Kind(kind)
		if set := result[crewID]; set != nil {
			set.agents = append(set.agents, item)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx, `
		SELECT h.crew_id::text, h.user_id::text, u.email
		  FROM openfox.agent_crew_human_members h
		  JOIN openfox.organization_members om
		    ON om.organization_id = h.organization_id AND om.user_id = h.user_id AND om.status = 'ACTIVE'
		  JOIN openfox.users u ON u.id = h.user_id
		 WHERE h.organization_id = openfox_private.current_organization_id() AND h.crew_id = ANY($1::uuid[])
		 ORDER BY h.crew_id, h.created_at, h.user_id`, crewIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var crewID string
		var item crewHumanMember
		if err := rows.Scan(&crewID, &item.ID, &item.Email); err != nil {
			return nil, err
		}
		if set := result[crewID]; set != nil {
			set.humans = append(set.humans, item)
		}
	}
	return result, rows.Err()
}

// crewRow 是 agent_crews 一行加空间名称、所有者邮箱与个人标记。
type crewRow struct {
	Crew
	LeaderAgentID string
}

const crewSelectSQL = `
	SELECT cr.id::text, cr.space_id::text, s.name, cr.name, cr.description, cr.visibility,
	       cr.owner_user_id::text, COALESCE(u.email, ''), COALESCE(cr.leader_agent_id::text, ''),
	       cr.instructions, cr.shared_task_dir, cr.archived_at, cr.version, cr.created_at, cr.updated_at,
	       EXISTS (SELECT 1 FROM openfox.agent_directory_pins p
	                WHERE p.organization_id = cr.organization_id
	                  AND p.user_id = openfox_private.current_actor_id() AND p.crew_id = cr.id),
	       EXISTS (SELECT 1 FROM openfox.agent_favorites f
	                WHERE f.organization_id = cr.organization_id
	                  AND f.user_id = openfox_private.current_actor_id() AND f.crew_id = cr.id)
	  FROM openfox.agent_crews cr
	  JOIN openfox.spaces s ON s.organization_id = cr.organization_id AND s.id = cr.space_id
	  LEFT JOIN openfox.users u ON u.id = cr.owner_user_id`

// crewVisibleSQL：空间可读（$1 为大写组织角色）且小队共享或归当前成员所有。
const crewVisibleSQL = directorySpaceSQL + ` AND (cr.visibility = 'SHARED' OR cr.owner_user_id = openfox_private.current_actor_id())`

func scanCrewRow(row rowScanner, actorID string) (crewRow, error) {
	var item crewRow
	var visibility string
	var version int64
	if err := row.Scan(&item.ID, &item.SpaceID, &item.SpaceName, &item.Name, &item.Description, &visibility,
		&item.OwnerUserID, &item.OwnerEmail, &item.LeaderAgentID, &item.Instructions, &item.SharedTaskDir,
		&item.ArchivedAt, &version, &item.CreatedAt, &item.UpdatedAt, &item.Pinned, &item.Favorite); err != nil {
		return crewRow{}, err
	}
	if version <= 0 {
		return crewRow{}, ErrInvalidRequest
	}
	item.Type, item.Visibility, item.Version = DirectoryTypeCrew, Visibility(visibility), uint64(version)
	item.Mine = item.OwnerUserID == actorID
	return item, nil
}

// loadCrew 读取一个对当前成员可见的小队详情（spaceID 为空时不限定空间）。
func loadCrew(ctx context.Context, tx pgx.Tx, actor Actor, spaceID, crewID string) (Crew, error) {
	query := crewSelectSQL + `
	 WHERE cr.organization_id = openfox_private.current_organization_id() AND cr.id = $2::uuid AND ` + crewVisibleSQL
	args := []any{strings.ToUpper(string(actor.Role)), crewID}
	if spaceID != "" {
		query += ` AND cr.space_id = $3::uuid`
		args = append(args, spaceID)
	}
	row, err := scanCrewRow(tx.QueryRow(ctx, query, args...), actor.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Crew{}, ErrNotFound
	}
	if err != nil {
		return Crew{}, err
	}
	sets, err := loadCrewMembers(ctx, tx, []string{crewID})
	if err != nil {
		return Crew{}, err
	}
	set := sets[crewID]
	crew := row.Crew
	crew.Leader = set.leader(row.LeaderAgentID)
	crew.MemberCount = set.count()
	agentIDs := make([]string, 0, len(set.agents))
	for _, agent := range set.agents {
		agentIDs = append(agentIDs, agent.ID)
	}
	humanIDs := make([]string, 0, len(set.humans))
	for _, human := range set.humans {
		humanIDs = append(humanIDs, human.ID)
	}
	agentTasks, running, err := activeAgentTasks(ctx, tx, agentIDs)
	if err != nil {
		return Crew{}, err
	}
	humanTasks, err := activeHumanTasks(ctx, tx, crew.SpaceID, humanIDs)
	if err != nil {
		return Crew{}, err
	}
	crew.RunningTaskCount = running
	// 队长排第一，其余按 position。
	ordered := append([]crewAgentMember(nil), set.agents...)
	if crew.Leader != nil {
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID == crew.Leader.ID && ordered[j].ID != crew.Leader.ID })
	}
	crew.Members = make([]CrewMember, 0, crew.MemberCount)
	for _, agent := range ordered {
		title, online := agent.Title, agent.Online
		crew.Members = append(crew.Members, CrewMember{Kind: "AGENT", ID: agent.ID, Name: agent.Name, Title: &title,
			Exclusive: agent.Exclusive, ActiveTasks: nonNilStrings(agentTasks[agent.ID]), Online: &online})
	}
	for _, human := range set.humans {
		crew.Members = append(crew.Members, CrewMember{Kind: "HUMAN", ID: human.ID, Name: human.Email,
			ActiveTasks: nonNilStrings(humanTasks[human.ID])})
	}
	return crew, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// activeAgentTasks：每个 Agent 有活跃运行的任务 display_id（最近的 3 个），以及这些任务去重后的总数。
func activeAgentTasks(ctx context.Context, tx pgx.Tx, agentIDs []string) (map[string][]string, int, error) {
	result := map[string][]string{}
	if len(agentIDs) == 0 {
		return result, 0, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT agent_id, task_id, display_id, rank FROM (
		    SELECT r.agent_id::text AS agent_id, r.task_id::text AS task_id, ts.prefix || '-' || t.sequence AS display_id,
		           row_number() OVER (PARTITION BY r.agent_id ORDER BY max(r.created_at) DESC, r.task_id) AS rank
		      FROM openfox.runs r
		      JOIN openfox.tasks t ON t.organization_id = r.organization_id AND t.id = r.task_id AND t.deleted_at IS NULL
		      JOIN openfox.spaces ts ON ts.organization_id = t.organization_id AND ts.id = t.space_id
		     WHERE r.organization_id = openfox_private.current_organization_id()
		       AND r.agent_id = ANY($1::uuid[]) AND r.state IN `+activeRunStatesSQL+`
		     GROUP BY r.agent_id, r.task_id, ts.prefix, t.sequence
		) ranked
		 ORDER BY agent_id, rank`, agentIDs)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	tasks := map[string]bool{}
	for rows.Next() {
		var agentID, taskID, displayID string
		var rank int64
		if err := rows.Scan(&agentID, &taskID, &displayID, &rank); err != nil {
			return nil, 0, err
		}
		tasks[taskID] = true
		if rank <= 3 {
			result[agentID] = append(result[agentID], displayID)
		}
	}
	return result, len(tasks), rows.Err()
}

// activeHumanTasks：真人成员在小队空间里被分配的进行中任务（IN_PROGRESS 类别，最近更新的 3 个）。
func activeHumanTasks(ctx context.Context, tx pgx.Tx, spaceID string, userIDs []string) (map[string][]string, error) {
	result := map[string][]string{}
	if len(userIDs) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT user_id, display_id FROM (
		    SELECT x.user_id::text AS user_id, ts.prefix || '-' || t.sequence AS display_id,
		           row_number() OVER (PARTITION BY x.user_id ORDER BY t.updated_at DESC, t.id) AS rank
		      FROM openfox.task_assignees x
		      JOIN openfox.tasks t ON t.organization_id = x.organization_id AND t.id = x.task_id AND t.deleted_at IS NULL
		      JOIN openfox.task_statuses st ON st.organization_id = t.organization_id AND st.id = t.status_id
		      JOIN openfox.spaces ts ON ts.organization_id = t.organization_id AND ts.id = t.space_id
		     WHERE x.organization_id = openfox_private.current_organization_id()
		       AND x.user_id = ANY($1::uuid[]) AND t.space_id = $2::uuid AND st.category = 'IN_PROGRESS'
		) ranked
		 WHERE rank <= 3
		 ORDER BY user_id, rank`, userIDs, spaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID, displayID string
		if err := rows.Scan(&userID, &displayID); err != nil {
			return nil, err
		}
		result[userID] = append(result[userID], displayID)
	}
	return result, rows.Err()
}

func (s PostgresStore) GetCrew(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, crewID string) (Crew, error) {
	spaceID, crewID, err := s.agentRef(selection, actor, spaceID, crewID)
	if err != nil {
		return Crew{}, err
	}
	var result Crew
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionReadSpace, true); err != nil {
			return err
		}
		result, err = loadCrew(ctx, tx, actor, spaceID, crewID)
		return err
	})
	return result, err
}

// normalizeIDList 规范化 UUID 列表：非法或重复返回 ErrInvalidRequest。
func normalizeIDList(values []string, seen map[string]bool) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, raw := range values {
		id, err := normalizeUUID(raw)
		if err != nil || seen[id] {
			return nil, ErrInvalidRequest
		}
		seen[id] = true
		result = append(result, id)
	}
	return result, nil
}

// requireCrewAgents 校验 Agent 都在小队空间、对当前成员可见、未归档且不是内置小助手。
func requireCrewAgents(ctx context.Context, tx pgx.Tx, spaceID string, agentIDs []string) error {
	if len(agentIDs) == 0 {
		return nil
	}
	var usable int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.agents a
		 WHERE a.organization_id = openfox_private.current_organization_id() AND a.space_id = $1::uuid
		   AND a.id = ANY($2::uuid[]) AND a.archived_at IS NULL AND `+directoryAgentVisibleSQL,
		spaceID, agentIDs).Scan(&usable); err != nil {
		return err
	}
	if usable != len(agentIDs) {
		return ErrInvalidRequest
	}
	return nil
}

func validateInstructions(value string) error {
	if len([]rune(value)) > 100000 || strings.IndexFunc(value, isInstructionControl) >= 0 {
		return ErrInvalidRequest
	}
	return nil
}

// CreateCrew 在空间内创建小队（需空间管理权限）。队长排第一；总人数（含队长与真人成员）≤ MaxCrewMembers。
// 兼容旧请求：不传 LeaderAgentID 时取 AgentIDs[0]。
func (s PostgresStore) CreateCrew(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID string, input CrewInput) (Crew, error) {
	if err := s.validate(selection, actor); err != nil {
		return Crew{}, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil {
		return Crew{}, ErrInvalidRequest
	}
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	if input.Visibility == "" {
		input.Visibility = VisibilityShared
	}
	if err := validateAgentFields(input.Name, input.Description, KindCode, input.Visibility); err != nil {
		return Crew{}, err
	}
	seen := map[string]bool{}
	var agentIDs []string
	leader := strings.TrimSpace(input.LeaderAgentID)
	if leader != "" {
		if agentIDs, err = normalizeIDList([]string{leader}, seen); err != nil {
			return Crew{}, err
		}
		// 队长同时出现在 agent_ids 里时去重。
		rest := make([]string, 0, len(input.AgentIDs))
		for _, raw := range input.AgentIDs {
			if id, _ := normalizeUUID(raw); id != agentIDs[0] {
				rest = append(rest, raw)
			}
		}
		more, err := normalizeIDList(rest, seen)
		if err != nil {
			return Crew{}, err
		}
		agentIDs = append(agentIDs, more...)
	} else if agentIDs, err = normalizeIDList(input.AgentIDs, seen); err != nil {
		return Crew{}, err
	}
	userIDs, err := normalizeIDList(input.UserIDs, map[string]bool{})
	if err != nil {
		return Crew{}, err
	}
	if len(agentIDs) == 0 || len(agentIDs)+len(userIDs) > MaxCrewMembers {
		return Crew{}, ErrInvalidRequest
	}
	var result Crew
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if err := authorizeSpace(ctx, tx, selection.OrganizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
			return err
		}
		if err := requireWritableSpace(ctx, tx, spaceID); err != nil {
			return err
		}
		if err := requireCrewAgents(ctx, tx, spaceID, agentIDs); err != nil {
			return err
		}
		if ok, err := membersCanReadSpace(ctx, tx, spaceID, userIDs); err != nil {
			return err
		} else if !ok {
			return ErrInvalidRequest
		}
		id, err := membership.NewID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.agent_crews (organization_id, id, space_id, owner_user_id, name, description, visibility, leader_agent_id)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8::uuid)`,
			selection.OrganizationID, id, spaceID, actor.UserID, input.Name, input.Description, input.Visibility, agentIDs[0]); err != nil {
			return mapDatabaseError(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.agent_crew_members (organization_id, crew_id, agent_id, position)
			SELECT $1::uuid, $2::uuid, member.id, member.position - 1
			  FROM unnest($3::uuid[]) WITH ORDINALITY AS member(id, position)`,
			selection.OrganizationID, id, agentIDs); err != nil {
			return mapDatabaseError(err)
		}
		if err := insertCrewHumans(ctx, tx, id, userIDs, actor.UserID); err != nil {
			return err
		}
		if err := recordResourceAudit(ctx, tx, selection, actor, "agent_crew", "agent_crew.created", id, spaceID, 1); err != nil {
			return err
		}
		result, err = loadCrew(ctx, tx, actor, "", id)
		return err
	})
	return result, err
}

func insertCrewHumans(ctx context.Context, tx pgx.Tx, crewID string, userIDs []string, actorID string) error {
	if len(userIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.agent_crew_human_members (organization_id, crew_id, user_id, created_by)
		SELECT openfox_private.current_organization_id(), $1::uuid, member.id, $3::uuid
		  FROM unnest($2::uuid[]) AS member(id)
		ON CONFLICT DO NOTHING`, crewID, userIDs, actorID)
	return mapDatabaseError(err)
}

// lockedCrew 是加锁读取的小队行。
type lockedCrew struct {
	ID, SpaceID, OwnerUserID, Name, Description, Instructions, LeaderAgentID string
	Visibility                                                               Visibility
	SharedTaskDir                                                            bool
	ArchivedAt                                                               *time.Time
	Version                                                                  uint64
}

// lockManageableCrew：需要空间管理权限；他人的私有小队按不存在处理。
func lockManageableCrew(ctx context.Context, tx pgx.Tx, organizationID, spaceID, crewID string, actor Actor) (lockedCrew, error) {
	if err := authorizeSpace(ctx, tx, organizationID, spaceID, actor, policy.ActionManageSpace, false); err != nil {
		return lockedCrew{}, err
	}
	var item lockedCrew
	var visibility string
	var version int64
	err := tx.QueryRow(ctx, `
		SELECT id::text, space_id::text, owner_user_id::text, name, description, instructions,
		       COALESCE(leader_agent_id::text, ''), visibility, shared_task_dir, archived_at, version
		  FROM openfox.agent_crews
		 WHERE organization_id = $1::uuid AND space_id = $2::uuid AND id = $3::uuid
		 FOR UPDATE`, organizationID, spaceID, crewID).Scan(&item.ID, &item.SpaceID, &item.OwnerUserID, &item.Name,
		&item.Description, &item.Instructions, &item.LeaderAgentID, &visibility, &item.SharedTaskDir, &item.ArchivedAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedCrew{}, ErrNotFound
	}
	if err != nil {
		return lockedCrew{}, err
	}
	item.Visibility, item.Version = Visibility(visibility), uint64(version)
	if item.Visibility == VisibilityPrivate && item.OwnerUserID != actor.UserID {
		return lockedCrew{}, ErrNotFound
	}
	return item, nil
}

// effectiveLeader 返回小队当前的队长 Agent ID（指定队长，或 position 最小的成员；不考虑可见性）。
func effectiveLeader(ctx context.Context, tx pgx.Tx, crew lockedCrew) (string, error) {
	if crew.LeaderAgentID != "" {
		return crew.LeaderAgentID, nil
	}
	var leader string
	err := tx.QueryRow(ctx, `
		SELECT agent_id::text FROM openfox.agent_crew_members
		 WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid
		 ORDER BY position, created_at, agent_id
		 LIMIT 1`, crew.ID).Scan(&leader)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return leader, err
}

// compactCrewPositions 把成员 position 重新编号为 0..n-1，队长排第一，其余保持原有顺序。
func compactCrewPositions(ctx context.Context, tx pgx.Tx, crewID, leaderID string) error {
	_, err := tx.Exec(ctx, `
		UPDATE openfox.agent_crew_members m
		   SET position = ordered.next_position
		  FROM (SELECT agent_id,
		               row_number() OVER (ORDER BY (agent_id = $2::uuid) DESC, position, created_at, agent_id) - 1 AS next_position
		          FROM openfox.agent_crew_members
		         WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid) ordered
		 WHERE m.organization_id = openfox_private.current_organization_id() AND m.crew_id = $1::uuid
		   AND m.agent_id = ordered.agent_id AND m.position <> ordered.next_position`, crewID, nullableUUID(leaderID))
	return err
}

// UpdateCrew 修改小队（需空间管理权限，乐观锁 Version）。
func (s PostgresStore) UpdateCrew(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, crewID string, patch CrewPatch) (Crew, error) {
	if err := s.validate(selection, actor); err != nil {
		return Crew{}, err
	}
	spaceID, err := normalizeUUID(spaceID)
	if err != nil || patch.Version == 0 || patch.empty() {
		return Crew{}, ErrInvalidRequest
	}
	crewID, err = normalizeUUID(crewID)
	if err != nil {
		return Crew{}, ErrNotFound
	}
	var result Crew
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		current, err := lockManageableCrew(ctx, tx, selection.OrganizationID, spaceID, crewID, actor)
		if err != nil {
			return err
		}
		if current.Version != patch.Version {
			return ErrVersionConflict
		}
		next := current
		if patch.Name != nil {
			next.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Description != nil {
			next.Description = strings.TrimSpace(*patch.Description)
		}
		if patch.Visibility != nil {
			next.Visibility = *patch.Visibility
		}
		if err := validateAgentFields(next.Name, next.Description, KindCode, next.Visibility); err != nil {
			return err
		}
		if patch.Instructions != nil {
			next.Instructions = strings.TrimSpace(*patch.Instructions)
			if err := validateInstructions(next.Instructions); err != nil {
				return err
			}
		}
		if patch.SharedTaskDir != nil {
			next.SharedTaskDir = *patch.SharedTaskDir
		}
		if patch.Archived != nil {
			if *patch.Archived && current.ArchivedAt == nil {
				now := time.Now().UTC()
				next.ArchivedAt = &now
			} else if !*patch.Archived {
				next.ArchivedAt = nil
			}
		}
		if patch.SpaceID != nil {
			if next.SpaceID, err = normalizeUUID(*patch.SpaceID); err != nil {
				return ErrInvalidRequest
			}
		}
		if patch.OwnerUserID != nil {
			if next.OwnerUserID, err = normalizeUUID(*patch.OwnerUserID); err != nil {
				return ErrInvalidOwner
			}
		}
		if patch.LeaderAgentID != nil {
			if next.LeaderAgentID, err = normalizeUUID(*patch.LeaderAgentID); err != nil {
				return ErrInvalidRequest
			}
			var member bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM openfox.agent_crew_members m
				                 JOIN openfox.agents a ON a.organization_id = m.organization_id AND a.id = m.agent_id
				                WHERE m.organization_id = openfox_private.current_organization_id()
				                  AND m.crew_id = $1::uuid AND m.agent_id = $2::uuid AND a.archived_at IS NULL)`,
				crewID, next.LeaderAgentID).Scan(&member); err != nil {
				return err
			}
			if !member {
				return ErrInvalidRequest
			}
		}
		spaceChanged := next.SpaceID != current.SpaceID
		if spaceChanged {
			if err := authorizeSpace(ctx, tx, selection.OrganizationID, next.SpaceID, actor, policy.ActionReadSpace, true); err != nil {
				return err
			}
			if err := authorizeSpace(ctx, tx, selection.OrganizationID, next.SpaceID, actor, policy.ActionManageSpace, false); err != nil {
				return err
			}
			leader := next.LeaderAgentID
			if leader == "" {
				if leader, err = effectiveLeader(ctx, tx, current); err != nil {
					return err
				}
			}
			var leaderSpace string
			if leader != "" {
				if err := tx.QueryRow(ctx, `
					SELECT space_id::text FROM openfox.agents
					 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
					leader).Scan(&leaderSpace); err != nil {
					return err
				}
			}
			if leader == "" || leaderSpace != next.SpaceID {
				return ErrCrewLeaderSpaceMismatch
			}
			next.LeaderAgentID = leader
			// 不在新空间的 Agent 成员随换空间移除。
			if _, err := tx.Exec(ctx, `
				DELETE FROM openfox.agent_crew_members m
				 USING openfox.agents a
				 WHERE m.organization_id = openfox_private.current_organization_id() AND m.crew_id = $1::uuid
				   AND a.organization_id = m.organization_id AND a.id = m.agent_id AND a.space_id <> $2::uuid`,
				crewID, next.SpaceID); err != nil {
				return err
			}
		}
		if spaceChanged || next.OwnerUserID != current.OwnerUserID {
			if ok, err := membersCanReadSpace(ctx, tx, next.SpaceID, []string{next.OwnerUserID}); err != nil {
				return err
			} else if !ok {
				return ErrInvalidOwner
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.agent_crews
			   SET name = $3, description = $4, visibility = $5, instructions = $6, shared_task_dir = $7,
			       archived_at = $8, space_id = $9::uuid, owner_user_id = $10::uuid, leader_agent_id = $11::uuid,
			       version = version + 1, updated_at = clock_timestamp()
			 WHERE organization_id = $1::uuid AND id = $2::uuid AND version = $12`,
			selection.OrganizationID, crewID, next.Name, next.Description, next.Visibility, next.Instructions,
			next.SharedTaskDir, next.ArchivedAt, next.SpaceID, next.OwnerUserID, nullableUUID(next.LeaderAgentID),
			int64(patch.Version))
		if err != nil {
			return mapDatabaseError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrVersionConflict
		}
		if next.LeaderAgentID != current.LeaderAgentID || spaceChanged {
			if err := compactCrewPositions(ctx, tx, crewID, next.LeaderAgentID); err != nil {
				return err
			}
		}
		action := "agent_crew.updated"
		if patch.Archived != nil && (current.ArchivedAt == nil) != (next.ArchivedAt == nil) {
			action = "agent_crew.restored"
			if next.ArchivedAt != nil {
				action = "agent_crew.archived"
			}
		}
		if err := recordResourceAudit(ctx, tx, selection, actor, "agent_crew", action, crewID, next.SpaceID, patch.Version+1); err != nil {
			return err
		}
		result, err = loadCrew(ctx, tx, actor, "", crewID)
		return err
	})
	return result, err
}

func (p CrewPatch) empty() bool {
	return p.Name == nil && p.Description == nil && p.Visibility == nil && p.SpaceID == nil && p.OwnerUserID == nil &&
		p.Instructions == nil && p.SharedTaskDir == nil && p.LeaderAgentID == nil && p.Archived == nil
}

// AddCrewMembers 添加 Agent 与真人成员（已是成员的忽略）；总人数超过 MaxCrewMembers 返回 ErrLimitReached。
func (s PostgresStore) AddCrewMembers(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, crewID string, agentIDs, userIDs []string) (Crew, error) {
	spaceID, crewID, err := s.agentRef(selection, actor, spaceID, crewID)
	if err != nil {
		return Crew{}, err
	}
	agentIDs, err = normalizeIDList(agentIDs, map[string]bool{})
	if err != nil {
		return Crew{}, err
	}
	userIDs, err = normalizeIDList(userIDs, map[string]bool{})
	if err != nil {
		return Crew{}, err
	}
	if len(agentIDs) == 0 && len(userIDs) == 0 {
		return Crew{}, ErrInvalidRequest
	}
	var result Crew
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		crew, err := lockManageableCrew(ctx, tx, selection.OrganizationID, spaceID, crewID, actor)
		if err != nil {
			return err
		}
		if crew.ArchivedAt != nil {
			return ErrArchived
		}
		if err := requireCrewAgents(ctx, tx, spaceID, agentIDs); err != nil {
			return err
		}
		if ok, err := membersCanReadSpace(ctx, tx, spaceID, userIDs); err != nil {
			return err
		} else if !ok {
			return ErrInvalidRequest
		}
		leader, err := effectiveLeader(ctx, tx, crew)
		if err != nil {
			return err
		}
		if err := compactCrewPositions(ctx, tx, crewID, leader); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.agent_crew_members (organization_id, crew_id, agent_id, position)
			SELECT openfox_private.current_organization_id(), $1::uuid, member.id,
			       (SELECT count(*) FROM openfox.agent_crew_members
			         WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid)
			       + member.position - 1
			  FROM unnest($2::uuid[]) WITH ORDINALITY AS member(id, position)
			 WHERE NOT EXISTS (SELECT 1 FROM openfox.agent_crew_members existing
			                    WHERE existing.organization_id = openfox_private.current_organization_id()
			                      AND existing.crew_id = $1::uuid AND existing.agent_id = member.id)`,
			crewID, agentIDs); err != nil {
			return mapDatabaseError(err)
		}
		if err := insertCrewHumans(ctx, tx, crewID, userIDs, actor.UserID); err != nil {
			return err
		}
		var total int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM openfox.agent_crew_members
			         WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid)
			     + (SELECT count(*) FROM openfox.agent_crew_human_members
			         WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid)`,
			crewID).Scan(&total); err != nil {
			return err
		}
		if total > MaxCrewMembers {
			return ErrLimitReached
		}
		if err := touchCrew(ctx, tx, crewID); err != nil {
			return err
		}
		if err := recordResourceAudit(ctx, tx, selection, actor, "agent_crew", "agent_crew.members_added", crewID, spaceID, crew.Version+1); err != nil {
			return err
		}
		result, err = loadCrew(ctx, tx, actor, "", crewID)
		return err
	})
	return result, err
}

// SetCrewMemberExclusive 设置或取消专属成员；一个 Agent 至多是一个小队的专属成员。
func (s PostgresStore) SetCrewMemberExclusive(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, crewID, agentID string, exclusive bool) (Crew, error) {
	spaceID, crewID, err := s.agentRef(selection, actor, spaceID, crewID)
	if err != nil {
		return Crew{}, err
	}
	agentID, err = normalizeUUID(agentID)
	if err != nil {
		return Crew{}, ErrResourceNotFound
	}
	var result Crew
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		crew, err := lockManageableCrew(ctx, tx, selection.OrganizationID, spaceID, crewID, actor)
		if err != nil {
			return err
		}
		if exclusive {
			var elsewhere bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM openfox.agent_crew_members
				                WHERE organization_id = openfox_private.current_organization_id()
				                  AND agent_id = $1::uuid AND exclusive AND crew_id <> $2::uuid)`,
				agentID, crewID).Scan(&elsewhere); err != nil {
				return err
			}
			if elsewhere {
				return ErrCrewMembershipConflict
			}
		}
		tag, err := tx.Exec(ctx, `
			UPDATE openfox.agent_crew_members SET exclusive = $3
			 WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid AND agent_id = $2::uuid`,
			crewID, agentID, exclusive)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrCrewMembershipConflict
			}
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrResourceNotFound
		}
		if err := touchCrew(ctx, tx, crewID); err != nil {
			return err
		}
		if err := recordResourceAudit(ctx, tx, selection, actor, "agent_crew", "agent_crew.member_updated", crewID, spaceID, crew.Version+1); err != nil {
			return err
		}
		result, err = loadCrew(ctx, tx, actor, "", crewID)
		return err
	})
	return result, err
}

// RemoveCrewMember 移除成员（幂等）。kind 为 agent / human（不区分大小写）；队长返回 ErrCrewLeaderRequired。
func (s PostgresStore) RemoveCrewMember(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, crewID, kind, memberID string) error {
	spaceID, crewID, err := s.agentRef(selection, actor, spaceID, crewID)
	if err != nil {
		return err
	}
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if kind != "AGENT" && kind != "HUMAN" {
		return ErrInvalidRequest
	}
	memberID, err = normalizeUUID(memberID)
	if err != nil {
		return ErrInvalidRequest
	}
	return s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		crew, err := lockManageableCrew(ctx, tx, selection.OrganizationID, spaceID, crewID, actor)
		if err != nil {
			return err
		}
		var tag pgconn.CommandTag
		if kind == "AGENT" {
			leader, err := effectiveLeader(ctx, tx, crew)
			if err != nil {
				return err
			}
			if leader == memberID {
				return ErrCrewLeaderRequired
			}
			tag, err = tx.Exec(ctx, `
				DELETE FROM openfox.agent_crew_members
				 WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid AND agent_id = $2::uuid`,
				crewID, memberID)
			if err != nil {
				return err
			}
		} else {
			tag, err = tx.Exec(ctx, `
				DELETE FROM openfox.agent_crew_human_members
				 WHERE organization_id = openfox_private.current_organization_id() AND crew_id = $1::uuid AND user_id = $2::uuid`,
				crewID, memberID)
			if err != nil {
				return err
			}
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if err := touchCrew(ctx, tx, crewID); err != nil {
			return err
		}
		return recordResourceAudit(ctx, tx, selection, actor, "agent_crew", "agent_crew.member_removed", crewID, spaceID, crew.Version+1)
	})
}
