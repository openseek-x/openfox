package collaboration

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 任务子资源：关注者、外部链接、任务关联。写接口都先确认任务存在、未删除且属于该空间。

var (
	errSubNotFound     = NewRuleError(404, "NOT_FOUND", "resource is not available")
	errInvalidFollower = NewRuleError(400, "INVALID_REQUEST", "follower kind or id is invalid")
	errInvalidLink     = NewRuleError(400, "INVALID_LINK", "link must be an http(s) url within 2048 characters")
	errLinkExists      = NewRuleError(409, "LINK_EXISTS", "the link already exists on this task")
	errLimitReached    = NewRuleError(409, "LIMIT_REACHED", "the limit for this resource is reached")
	errRelationExists  = NewRuleError(409, "RELATION_EXISTS", "the relation already exists")
	errInvalidRelation = NewRuleError(400, "INVALID_RELATION", "relation is invalid")
)

const maxTaskLinks = 100

// ---- 关注者 ----

type TaskFollower struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s Service) ListFollowers(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor) ([]TaskFollower, bool, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, false, err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return nil, false, err
	}
	rows, err := tx.Query(ctx, `
		SELECT f.kind, COALESCE(f.user_id, f.agent_id)::text, COALESCE(u.email, ag.name, '')
		  FROM openfox.task_followers f
		  LEFT JOIN openfox.users u ON u.id = f.user_id
		  LEFT JOIN openfox.agents ag ON ag.organization_id = f.organization_id AND ag.id = f.agent_id
		 WHERE f.organization_id = openfox_private.current_organization_id() AND f.task_id = $1::uuid
		 ORDER BY f.created_at, f.id`, taskID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := make([]TaskFollower, 0)
	following := false
	for rows.Next() {
		var item TaskFollower
		if err := rows.Scan(&item.Kind, &item.ID, &item.Name); err != nil {
			return nil, false, err
		}
		if item.Kind == "HUMAN" && item.ID == actor.UserID {
			following = true
		}
		items = append(items, item)
	}
	return items, following, rows.Err()
}

func parseFollowerKind(kind, id string) (string, string, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	id = strings.ToLower(strings.TrimSpace(id))
	if (kind != "HUMAN" && kind != "AGENT") || !membership.ValidateUUID(id) {
		return "", "", errInvalidFollower
	}
	return kind, id, nil
}

// AddFollower 幂等地添加关注者；成员须能读该空间，Agent 须属于该空间且对操作人可用。
func (s Service) AddFollower(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, kind, id string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	kind, id, err := parseFollowerKind(kind, id)
	if err != nil {
		return err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return err
	}
	if err := s.validateAssigneeRefs(ctx, tx, spaceID, actor, []TaskAssigneeRef{{Kind: kind, ID: id}}); err != nil {
		return errInvalidFollower
	}
	if kind == "HUMAN" {
		return addHumanFollower(ctx, tx, spaceID, taskID, id, actor.UserID)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, agent_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'AGENT', $3::uuid, $4::uuid)
		ON CONFLICT DO NOTHING`, spaceID, taskID, id, actor.UserID)
	return err
}

func (s Service) RemoveFollower(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, kind, id string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	kind, id, err := parseFollowerKind(kind, id)
	if err != nil {
		return err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM openfox.task_followers
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND kind = $2 AND COALESCE(user_id, agent_id) = $3::uuid`, taskID, kind, id)
	return err
}

// ---- 链接 ----

type TaskLink struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Version   uint64    `json:"version"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func normalizeLinkURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errInvalidLink
	}
	return raw, nil
}

func normalizeLinkTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if len([]rune(title)) > 200 {
		return "", errInvalidLink
	}
	return title, nil
}

const linkColumns = `l.id::text, l.url, l.title, l.version, l.created_by::text, l.created_at, l.updated_at`

func scanLink(row interface{ Scan(...any) error }) (TaskLink, error) {
	var link TaskLink
	var version int64
	if err := row.Scan(&link.ID, &link.URL, &link.Title, &version, &link.CreatedBy, &link.CreatedAt, &link.UpdatedAt); err != nil {
		return TaskLink{}, err
	}
	var err error
	link.Version, err = positiveVersion(version)
	return link, err
}

func (s Service) ListLinks(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor) ([]TaskLink, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+linkColumns+` FROM openfox.task_links l
		 WHERE l.organization_id = openfox_private.current_organization_id() AND l.task_id = $1::uuid
		 ORDER BY l.created_at, l.id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TaskLink, 0)
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, link)
	}
	return items, rows.Err()
}

func (s Service) GetLink(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, linkID string, actor Actor) (TaskLink, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskLink{}, err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return TaskLink{}, err
	}
	if !membership.ValidateUUID(linkID) {
		return TaskLink{}, errSubNotFound
	}
	link, err := scanLink(tx.QueryRow(ctx, `SELECT `+linkColumns+` FROM openfox.task_links l
		 WHERE l.organization_id = openfox_private.current_organization_id()
		   AND l.task_id = $1::uuid AND l.id = $2::uuid`, taskID, linkID))
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskLink{}, errSubNotFound
	}
	return link, err
}

func (s Service) CreateLink(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, rawURL, title, requestID string) (TaskLink, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskLink{}, err
	}
	linkURL, err := normalizeLinkURL(rawURL)
	if err != nil {
		return TaskLink{}, err
	}
	if title, err = normalizeLinkTitle(title); err != nil {
		return TaskLink{}, err
	}
	// 锁住任务行，让「每任务 ≤100」的计数检查与插入互斥。
	if err := lockLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return TaskLink{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM openfox.task_links
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid`, taskID).Scan(&count); err != nil {
		return TaskLink{}, err
	}
	if count >= maxTaskLinks {
		return TaskLink{}, errLimitReached
	}
	linkID, err := membership.NewID()
	if err != nil {
		return TaskLink{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_links (organization_id, space_id, task_id, id, url, title, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4, $5, $6::uuid)`,
		spaceID, taskID, linkID, linkURL, title, actor.UserID); err != nil {
		if isUniqueViolation(err) {
			return TaskLink{}, errLinkExists
		}
		return TaskLink{}, mapDatabaseError(err)
	}
	if err := newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, "task.link_added", map[string]any{"link_id": linkID}); err != nil {
		return TaskLink{}, err
	}
	return s.GetLink(ctx, tx, organizationID, spaceID, taskID, linkID, actor)
}

func (s Service) UpdateLink(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, linkID string, actor Actor, rawURL, title *string, version uint64) (TaskLink, error) {
	current, err := s.GetLink(ctx, tx, organizationID, spaceID, taskID, linkID, actor)
	if err != nil {
		return TaskLink{}, err
	}
	if version == 0 {
		return TaskLink{}, ErrInvalidVersion
	}
	newURL, newTitle := current.URL, current.Title
	if rawURL != nil {
		if newURL, err = normalizeLinkURL(*rawURL); err != nil {
			return TaskLink{}, err
		}
	}
	if title != nil {
		if newTitle, err = normalizeLinkTitle(*title); err != nil {
			return TaskLink{}, err
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.task_links SET url = $1, title = $2, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $3::uuid AND id = $4::uuid AND version = $5`, newURL, newTitle, taskID, linkID, int64(version))
	if err != nil {
		if isUniqueViolation(err) {
			return TaskLink{}, errLinkExists
		}
		return TaskLink{}, mapDatabaseError(err)
	}
	if result.RowsAffected() != 1 {
		return TaskLink{}, ErrVersionConflict
	}
	return s.GetLink(ctx, tx, organizationID, spaceID, taskID, linkID, actor)
}

func (s Service) DeleteLink(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, linkID string, actor Actor, requestID string) error {
	if _, err := s.GetLink(ctx, tx, organizationID, spaceID, taskID, linkID, actor); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM openfox.task_links
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND id = $2::uuid`, taskID, linkID); err != nil {
		return err
	}
	return newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, "task.link_removed", map[string]any{"link_id": linkID})
}

func lockLiveTask(ctx context.Context, tx pgx.Tx, spaceID, taskID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL FOR UPDATE`, spaceID, taskID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) {
		return ErrTaskNotFound
	}
	return err
}

// ---- 关联 ----

var builtinRelationKeys = map[string]bool{"BLOCKS": true, "CLONES": true, "DUPLICATES": true, "RELATES": true}

type RelationTask struct {
	ID             string `json:"id"`
	SpaceID        string `json:"space_id"`
	DisplayID      string `json:"display_id"`
	Title          string `json:"title"`
	StatusCategory string `json:"status_category"`
	StatusName     string `json:"status_name"`
}

type TaskRelation struct {
	ID        string       `json:"id"`
	TypeID    string       `json:"type_id"`
	Key       *string      `json:"key"`
	Direction string       `json:"direction"`
	Task      RelationTask `json:"task"`
	CreatedAt time.Time    `json:"created_at"`
}

// relationRowsSQL 以当前任务（$2）为视角列出关联，对端任务必须未删除且对操作人可读（$1 为组织角色）。
const relationRowsSQL = `
	SELECT r.id::text,
	       CASE WHEN r.builtin_type IS NOT NULL THEN 'builtin:' || r.builtin_type ELSE r.relation_type_id::text END,
	       r.builtin_type,
	       CASE WHEN r.source_task_id = $2::uuid THEN 'OUTWARD' ELSE 'INWARD' END,
	       o.id::text, o.space_id::text, s.prefix, o.sequence, o.title, ts.category, ts.name, r.created_at
	  FROM openfox.task_relations r
	  JOIN openfox.tasks o
	    ON o.organization_id = r.organization_id
	   AND o.id = CASE WHEN r.source_task_id = $2::uuid THEN r.target_task_id ELSE r.source_task_id END
	   AND o.deleted_at IS NULL
	  JOIN openfox.spaces s ON s.organization_id = o.organization_id AND s.id = o.space_id
	  JOIN openfox.task_statuses ts ON ts.organization_id = o.organization_id AND ts.id = o.status_id
	 WHERE r.organization_id = openfox_private.current_organization_id()
	   AND (r.source_task_id = $2::uuid OR r.target_task_id = $2::uuid)
	   AND ` + SpaceReadableSQL

func (s Service) queryRelations(ctx context.Context, tx pgx.Tx, actor Actor, taskID, extra string, args ...any) ([]TaskRelation, error) {
	queryArgs := append([]any{strings.ToUpper(string(actor.Role)), taskID}, args...)
	rows, err := tx.Query(ctx, relationRowsSQL+extra+` ORDER BY r.created_at, r.id`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]TaskRelation, 0)
	for rows.Next() {
		var item TaskRelation
		var prefix string
		var sequence int64
		if err := rows.Scan(&item.ID, &item.TypeID, &item.Key, &item.Direction, &item.Task.ID, &item.Task.SpaceID,
			&prefix, &sequence, &item.Task.Title, &item.Task.StatusCategory, &item.Task.StatusName, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Task.DisplayID = displayID(prefix, sequence)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) ListRelations(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor) ([]TaskRelation, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return nil, err
	}
	return s.queryRelations(ctx, tx, actor, taskID, "")
}

func (s Service) GetRelation(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, relationID string, actor Actor) (TaskRelation, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskRelation{}, err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return TaskRelation{}, err
	}
	if !membership.ValidateUUID(relationID) {
		return TaskRelation{}, errSubNotFound
	}
	items, err := s.queryRelations(ctx, tx, actor, taskID, ` AND r.id = $3::uuid`, relationID)
	if err != nil {
		return TaskRelation{}, err
	}
	if len(items) == 0 {
		return TaskRelation{}, errSubNotFound
	}
	return items[0], nil
}

func (s Service) CreateRelation(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, typeID, targetTaskID, direction, requestID string) (TaskRelation, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TaskRelation{}, err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return TaskRelation{}, err
	}
	targetTaskID = strings.ToLower(strings.TrimSpace(targetTaskID))
	if !membership.ValidateUUID(targetTaskID) || targetTaskID == taskID {
		return TaskRelation{}, errInvalidRelation
	}
	direction = strings.ToUpper(strings.TrimSpace(direction))
	if direction == "" {
		direction = "OUTWARD"
	}
	if direction != "OUTWARD" && direction != "INWARD" {
		return TaskRelation{}, errInvalidRelation
	}
	var builtin, custom string
	typeID = strings.TrimSpace(typeID)
	if key, found := strings.CutPrefix(typeID, "builtin:"); found {
		if !builtinRelationKeys[key] {
			return TaskRelation{}, errInvalidRelation
		}
		builtin = key
	} else {
		typeID = strings.ToLower(typeID)
		var exists bool
		if !membership.ValidateUUID(typeID) {
			return TaskRelation{}, errInvalidRelation
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.task_relation_types
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid)`, typeID).Scan(&exists); err != nil {
			return TaskRelation{}, err
		}
		if !exists {
			return TaskRelation{}, errInvalidRelation
		}
		custom = typeID
	}
	// 目标任务必须未删除且对操作人可读；不可读与不存在一样返回 404，不泄露存在性。
	var targetSpaceID string
	err := tx.QueryRow(ctx, `
		SELECT t.space_id::text
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.id = $2::uuid AND t.deleted_at IS NULL AND `+SpaceReadableSQL,
		strings.ToUpper(string(actor.Role)), targetTaskID).Scan(&targetSpaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskRelation{}, errSubNotFound
	}
	if err != nil {
		return TaskRelation{}, err
	}
	sourceSpace, sourceTask, targetSpace, targetTask := spaceID, taskID, targetSpaceID, targetTaskID
	if direction == "INWARD" {
		sourceSpace, sourceTask, targetSpace, targetTask = targetSpaceID, targetTaskID, spaceID, taskID
	}
	relationID, err := membership.NewID()
	if err != nil {
		return TaskRelation{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_relations (
			organization_id, id, source_space_id, source_task_id, target_space_id, target_task_id,
			builtin_type, relation_type_id, created_by
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
		          NULLIF($6, ''), NULLIF($7, '')::uuid, $8::uuid)`,
		relationID, sourceSpace, sourceTask, targetSpace, targetTask, builtin, custom, actor.UserID); err != nil {
		if isUniqueViolation(err) {
			return TaskRelation{}, errRelationExists
		}
		return TaskRelation{}, mapDatabaseError(err)
	}
	if err := newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, "task.relation_added",
		map[string]any{"relation_id": relationID, "type_id": typeID, "target_task_id": targetTaskID}); err != nil {
		return TaskRelation{}, err
	}
	return s.GetRelation(ctx, tx, organizationID, spaceID, taskID, relationID, actor)
}

// RelationOtherEnd 返回关联另一端任务的空间与任务 ID，用于「源或目标任务的 UpdateObject 均可」的权限判断。
func (s Service) RelationOtherEnd(ctx context.Context, tx pgx.Tx, taskID, relationID string) (otherSpaceID, otherTaskID string, err error) {
	if !membership.ValidateUUID(taskID) || !membership.ValidateUUID(relationID) {
		return "", "", errSubNotFound
	}
	err = tx.QueryRow(ctx, `
		SELECT CASE WHEN source_task_id = $1::uuid THEN target_space_id ELSE source_space_id END::text,
		       CASE WHEN source_task_id = $1::uuid THEN target_task_id ELSE source_task_id END::text
		  FROM openfox.task_relations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $2::uuid
		   AND (source_task_id = $1::uuid OR target_task_id = $1::uuid)`, taskID, relationID).Scan(&otherSpaceID, &otherTaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errSubNotFound
	}
	return otherSpaceID, otherTaskID, err
}

func (s Service) DeleteRelation(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, relationID string, actor Actor, requestID string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return err
	}
	if !membership.ValidateUUID(relationID) {
		return errSubNotFound
	}
	var typeID, other string
	err := tx.QueryRow(ctx, `
		DELETE FROM openfox.task_relations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $2::uuid
		   AND (source_task_id = $1::uuid OR target_task_id = $1::uuid)
		RETURNING CASE WHEN builtin_type IS NOT NULL THEN 'builtin:' || builtin_type ELSE relation_type_id::text END,
		          CASE WHEN source_task_id = $1::uuid THEN target_task_id ELSE source_task_id END::text`,
		taskID, relationID).Scan(&typeID, &other)
	if errors.Is(err, pgx.ErrNoRows) {
		return errSubNotFound
	}
	if err != nil {
		return err
	}
	return newActivityWriter(tx, spaceID, taskID, actor.UserID, requestID).add(ctx, "task.relation_removed",
		map[string]any{"relation_id": relationID, "type_id": typeID, "target_task_id": other})
}
