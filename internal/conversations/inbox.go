package conversations

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

// 收件箱通知按任务（无任务时按对话）聚合成组，组落在三个分区之一。
// 分区由 cleared_at / snoozed_until 推导，不另存状态：
//   - inbox：未清理，且没有稍后处理或稍后时间已到
//   - later：未清理，稍后时间晚于当前时间
//   - cleared：已清理
// 同一任务在已清理后又来新通知时，新通知单独成组回到收件箱。

type InboxSection string

const (
	InboxSectionInbox   InboxSection = "inbox"
	InboxSectionLater   InboxSection = "later"
	InboxSectionCleared InboxSection = "cleared"
)

// InboxImportance uses the recipient’s delivery level at creation; legacy rows retain kind-based classification.
type InboxImportance string

const (
	InboxImportanceAll     InboxImportance = "all"
	InboxImportancePrimary InboxImportance = "primary"
	InboxImportanceOther   InboxImportance = "other"
)

// InboxSpaceMode 对应空间筛选的「是 / 不是」。
type InboxSpaceMode string

const (
	InboxSpaceIs    InboxSpaceMode = "is"
	InboxSpaceIsNot InboxSpaceMode = "is_not"
)

const (
	// MaxInboxBatch 限制一次按组操作的组数，与列表单页上限一致。
	MaxInboxBatch = MaxPageSize
	// MaxInboxSpaceFilter 限制一次筛选的空间数。
	MaxInboxSpaceFilter = 50
	// MaxSnoozeDuration 限制稍后处理的最远时间。
	MaxSnoozeDuration = 366 * 24 * time.Hour
	maxInboxQuery     = 200
)

// InboxScope 是列表与批量操作共用的筛选范围：分区、重要性、空间（是 / 不是）与搜索词。
type InboxScope struct {
	Section    InboxSection
	Importance InboxImportance
	// SpaceID 是旧版单空间参数，与 SpaceIDs 合并使用。
	SpaceID   string
	SpaceIDs  []string
	SpaceMode InboxSpaceMode
	Query     string
}

type InboxQuery struct {
	DesktopFeed bool
	InboxScope
	Limit  int
	Cursor string
}

type InboxGroup struct {
	DirectChatID       string     `json:"direct_chat_id,omitempty"`
	DirectMessageID    string     `json:"direct_message_id,omitempty"`
	AutomationID       string     `json:"automation_id,omitempty"`
	ID                 string     `json:"id"`
	NotificationID     string     `json:"notification_id"`
	Kind               string     `json:"kind"`
	TaskID             string     `json:"task_id,omitempty"`
	TaskDisplayID      string     `json:"task_display_id,omitempty"`
	TaskTitle          string     `json:"task_title,omitempty"`
	TaskStatusCategory string     `json:"task_status_category,omitempty"`
	TaskTypeIcon       *string    `json:"task_type_icon"`
	ConversationID     string     `json:"conversation_id,omitempty"`
	SpaceID            string     `json:"space_id,omitempty"`
	SpaceName          string     `json:"space_name,omitempty"`
	AgentID            string     `json:"agent_id,omitempty"`
	ActorKind          string     `json:"actor_kind,omitempty"`
	ActorName          string     `json:"actor_name,omitempty"`
	SafeSummary        string     `json:"safe_summary"`
	Count              int        `json:"count"`
	Unread             bool       `json:"unread"`
	SnoozedUntil       *time.Time `json:"snoozed_until,omitempty"`
	ClearedAt          *time.Time `json:"cleared_at,omitempty"`
	LatestAt           time.Time  `json:"latest_at"`
}

type InboxCounts struct {
	Inbox       int `json:"inbox"`
	InboxUnread int `json:"inbox_unread"`
	// InboxImportantUnread 是收件箱里含「重要」未读通知的组数，侧栏角标据此显示红色。
	InboxImportantUnread int `json:"inbox_important_unread"`
	// InboxOtherUnread 是收件箱里含「其他」未读通知的组数（「其他」分段的计数）。
	// 同一组可能同时含两类未读通知，因此 重要 + 其他 可以大于 inbox_unread。
	InboxOtherUnread int `json:"inbox_other_unread"`
	Later            int `json:"later"`
	Cleared          int `json:"cleared"`
}

type InboxPage struct {
	Groups     []InboxGroup `json:"groups"`
	Counts     InboxCounts  `json:"counts"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// InboxAction 是按组操作；每个动作只作用于该组在指定分区内的通知。
type InboxAction string

const (
	InboxActionRead     InboxAction = "read"
	InboxActionUnread   InboxAction = "unread"
	InboxActionClear    InboxAction = "clear"
	InboxActionUnclear  InboxAction = "unclear"
	InboxActionSnooze   InboxAction = "snooze"
	InboxActionUnsnooze InboxAction = "unsnooze"
)

// InboxBulkOperation 是分区级批量操作，范围受分区、重要性与空间筛选约束。
type InboxBulkOperation string

const (
	InboxBulkMarkRead  InboxBulkOperation = "mark_read"
	InboxBulkClearRead InboxBulkOperation = "clear_read"
	InboxBulkClearAll  InboxBulkOperation = "clear_all"
)

// notificationVisibleSQL 是通知对当前成员可见的条件（$1 为大写组织角色）：
// 任务通知要求任务所在空间可见；对话通知还要求对话本身可见。
// 收件人失去访问权限后，通知保留在库中但不再展示。
// 两个分支都按协作策略判断空间可读（collaboration.SpaceReadableSQL）；任务分支还要求任务未删除。
const notificationVisibleSQL = `(
    (n.direct_chat_id IS NOT NULL AND EXISTS (
       SELECT 1 FROM openfox.direct_chats d JOIN openfox.organization_members peer ON peer.organization_id=d.organization_id
         AND peer.user_id=CASE WHEN d.user_low=n.recipient_user_id THEN d.user_high ELSE d.user_low END AND peer.status='ACTIVE'
        WHERE d.organization_id=n.organization_id AND d.id=n.direct_chat_id AND n.recipient_user_id IN (d.user_low,d.user_high)
    )) OR
    (n.conversation_id IS NULL AND n.task_id IS NULL AND n.automation_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM openfox.automations a JOIN openfox.spaces s ON s.organization_id=a.organization_id AND s.id=a.space_id
        WHERE a.organization_id=n.organization_id AND a.id=n.automation_id AND ` + collaboration.SpaceReadableSQL + `
    )) OR
	(n.conversation_id IS NULL AND n.task_id IS NOT NULL AND EXISTS (
		SELECT 1
		  FROM openfox.tasks t
		  JOIN openfox.spaces s
		    ON s.organization_id = t.organization_id AND s.id = t.space_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.id = n.task_id AND t.deleted_at IS NULL
		   AND ` + collaboration.SpaceReadableSQL + `
	))
	OR (n.conversation_id IS NOT NULL AND EXISTS (
		SELECT 1
		  FROM openfox.conversations c
		  JOIN openfox.spaces s
		    ON s.organization_id = c.organization_id AND s.id = c.space_id
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND c.id = n.conversation_id
		   AND (c.owner_user_id = openfox_private.current_actor_id()
		        OR (c.visibility = 'SHARED' AND EXISTS (
		            SELECT 1
		              FROM openfox.conversation_members cm
		              JOIN openfox.organization_members om
		                ON om.organization_id = cm.organization_id AND om.user_id = cm.user_id
		             WHERE cm.organization_id = c.organization_id
		               AND cm.conversation_id = c.id
		               AND cm.user_id = openfox_private.current_actor_id()
		               AND om.status = 'ACTIVE')))
		   AND ` + collaboration.SpaceReadableSQL + `
	))
)`

// 分区条件；now() 在事务内固定，保证同一请求里列表与计数一致。
const (
	inboxSectionInboxSQL   = `(n.cleared_at IS NULL AND (n.snoozed_until IS NULL OR n.snoozed_until <= now()))`
	inboxSectionLaterSQL   = `(n.cleared_at IS NULL AND n.snoozed_until > now())`
	inboxSectionClearedSQL = `(n.cleared_at IS NOT NULL)`
	// Notifications created before inbox preferences have no delivery snapshot.
	inboxOtherKindsSQL = `COALESCE(n.inbox_level='normal', n.kind IN ('HANDOFF_REVIEW', 'REPLY', 'TASK_COMMENT'))`
)

func sectionSQL(section InboxSection) (string, bool) {
	switch section {
	case InboxSectionInbox:
		return inboxSectionInboxSQL, true
	case InboxSectionLater:
		return inboxSectionLaterSQL, true
	case InboxSectionCleared:
		return inboxSectionClearedSQL, true
	default:
		return "", false
	}
}

func importanceSQL(importance InboxImportance) (string, bool) {
	switch importance {
	case "", InboxImportanceAll:
		return "", true
	case InboxImportancePrimary:
		return " AND NOT " + inboxOtherKindsSQL, true
	case InboxImportanceOther:
		return " AND " + inboxOtherKindsSQL, true
	default:
		return "", false
	}
}

// notificationSpaceSQL 取通知所属空间：任务优先，其次对话。
const notificationSpaceSQL = `COALESCE(
	(SELECT t.space_id FROM openfox.tasks t
	  WHERE t.organization_id = n.organization_id AND t.id = n.task_id),
	(SELECT c.space_id FROM openfox.conversations c
	  WHERE c.organization_id = n.organization_id AND c.id = n.conversation_id), (SELECT a.space_id FROM openfox.automations a WHERE a.organization_id=n.organization_id AND a.id=n.automation_id))`

// inboxFilter 是校验后的筛选条件，按占位符名填进 SQL。
type inboxFilter struct {
	section    string
	importance string
	spaceIDs   []string
	negate     bool
	pattern    string
}

func (scope InboxScope) filter() (inboxFilter, error) {
	section, ok := sectionSQL(scope.Section)
	if !ok {
		return inboxFilter{}, ErrInvalidRequest
	}
	importance, ok := importanceSQL(scope.Importance)
	if !ok {
		return inboxFilter{}, ErrInvalidRequest
	}
	values := scope.SpaceIDs
	if scope.SpaceID != "" {
		values = append([]string{scope.SpaceID}, values...)
	}
	if len(values) > MaxInboxSpaceFilter {
		return inboxFilter{}, ErrInvalidRequest
	}
	spaceIDs := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !membership.ValidateUUID(value) {
			return inboxFilter{}, ErrInvalidRequest
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			spaceIDs = append(spaceIDs, value)
		}
	}
	var negate bool
	switch scope.SpaceMode {
	case "", InboxSpaceIs:
	case InboxSpaceIsNot:
		negate = true
	default:
		return inboxFilter{}, ErrInvalidRequest
	}
	search := strings.TrimSpace(scope.Query)
	if len([]rune(search)) > maxInboxQuery {
		return inboxFilter{}, ErrInvalidRequest
	}
	pattern := ""
	if search != "" {
		pattern = "%" + escapeLike(search) + "%"
	}
	return inboxFilter{section: section + " AND n.inbox_level IS DISTINCT FROM 'off'", importance: importance, spaceIDs: spaceIDs, negate: negate, pattern: pattern}, nil
}

// inboxGroupsSQL 先在通知层完成分区、重要性、空间筛选，再按组聚合并关联展示字段，最后按搜索词过滤组。
// 占位符 {role} {spaces} {negate} {pattern} 由调用方替换成各自的参数序号。
// 空间筛选：未选空间时不过滤；「是」保留所选空间，「不是」排除所选空间。
func inboxGroupsSQL(filter inboxFilter) string {
	return `
		WITH visible AS (
			SELECT n.id, n.kind, n.direct_chat_id, n.direct_message_id, n.automation_id, n.actor_agent_id, n.comment_id, n.task_id, n.conversation_id, n.message_id, n.actor_user_id,
			       n.safe_summary, n.read_at, n.snoozed_until, n.cleared_at, n.created_at,
			       COALESCE(n.task_id, n.conversation_id, n.automation_id, n.direct_chat_id) AS group_id
			  FROM openfox.notifications n
			 WHERE n.organization_id = openfox_private.current_organization_id()
			   AND n.recipient_user_id = openfox_private.current_actor_id()
			   AND ` + filter.section + filter.importance + `
			   AND (cardinality({spaces}::uuid[]) = 0
			        OR COALESCE(` + notificationSpaceSQL + ` = ANY({spaces}::uuid[]), false) <> {negate}::boolean)
			   AND ` + strings.ReplaceAll(notificationVisibleSQL, "$1", "{role}") + `
		), grouped AS (
			SELECT DISTINCT ON (v.group_id)
			       v.group_id, v.id, v.kind, v.direct_chat_id, v.direct_message_id, v.automation_id, v.actor_agent_id, v.comment_id, v.task_id, v.conversation_id, v.message_id,
			       v.actor_user_id, v.safe_summary, v.snoozed_until, v.cleared_at,
			       count(*) OVER w AS item_count,
			       bool_or(v.read_at IS NULL) OVER w AS unread,
			       max(v.created_at) OVER w AS latest_at
			  FROM visible v
			WINDOW w AS (PARTITION BY v.group_id)
			 ORDER BY v.group_id, v.created_at DESC, v.id DESC
		), matched AS (
			SELECT g.*, t.title AS task_title, t.sequence AS task_sequence, ts.category AS task_category, tt.icon AS task_type_icon,
			       c.agent_id, sp.id AS space_id, sp.name AS space_name, sp.prefix AS space_prefix,
			       COALESCE(m.author_kind, CASE WHEN aa.id IS NOT NULL THEN 'AGENT' END) AS author_kind, COALESCE(ma.name,aa.name) AS message_agent_name, ca.name AS conversation_agent_name,
			       mu.email AS message_user_email, au.email AS actor_user_email
			  FROM grouped g
			  LEFT JOIN openfox.tasks t
			    ON t.organization_id = openfox_private.current_organization_id() AND t.id = g.task_id
			  LEFT JOIN openfox.task_statuses ts
			    ON ts.organization_id = t.organization_id AND ts.id = t.status_id
			  LEFT JOIN openfox.task_types tt
			    ON tt.organization_id = t.organization_id AND tt.id = t.type_id
			  LEFT JOIN openfox.conversations c
			    ON c.organization_id = openfox_private.current_organization_id() AND c.id = g.conversation_id
			  LEFT JOIN openfox.automations auto ON auto.organization_id=openfox_private.current_organization_id() AND auto.id=g.automation_id
              LEFT JOIN openfox.spaces sp
			    ON sp.organization_id = openfox_private.current_organization_id()
			   AND sp.id = COALESCE(t.space_id, c.space_id, auto.space_id)
			  LEFT JOIN openfox.messages m
			    ON m.organization_id = openfox_private.current_organization_id() AND m.id = g.message_id
			  LEFT JOIN openfox.task_comments tc ON tc.organization_id=openfox_private.current_organization_id() AND tc.id=g.comment_id
              LEFT JOIN openfox.agents aa ON aa.organization_id=openfox_private.current_organization_id() AND aa.id=coalesce(g.actor_agent_id,CASE WHEN g.kind IN ('TASK_COMMENT','MENTION') THEN tc.author_agent_id END)
              LEFT JOIN openfox.agents ma
			    ON ma.organization_id = m.organization_id AND ma.id = m.author_agent_id
			  LEFT JOIN openfox.agents ca
			    ON ca.organization_id = c.organization_id AND ca.id = c.agent_id
			  LEFT JOIN openfox.users mu ON mu.id = m.author_user_id
			  LEFT JOIN openfox.users au ON au.id = g.actor_user_id
			 WHERE ({pattern} = '' OR t.title ILIKE {pattern} ESCAPE '\' OR (sp.prefix || '-' || t.sequence::text) ILIKE {pattern} ESCAPE '\'
			        OR (g.direct_chat_id IS NOT NULL AND au.email ILIKE {pattern} ESCAPE '\') OR ca.name ILIKE {pattern} ESCAPE '\' OR ma.name ILIKE {pattern} ESCAPE '\')
		)`
}

func (s Service) ListInbox(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, query InboxQuery) (InboxPage, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return InboxPage{}, err
	}
	if query.Limit < 1 || query.Limit > MaxPageSize {
		return InboxPage{}, ErrInvalidRequest
	}
	filter, err := query.InboxScope.filter()
	if err != nil {
		return InboxPage{}, err
	}
	if query.DesktopFeed {
		filter.section = inboxSectionInboxSQL
	}
	var cursorAt *time.Time
	cursorID := ""
	if query.Cursor != "" {
		at, id, err := decodeInboxCursor(query.Cursor)
		if err != nil {
			return InboxPage{}, ErrInvalidRequest
		}
		cursorAt, cursorID = &at, id
	}
	role := strings.ToUpper(string(actor.Role))
	sql := strings.NewReplacer("{role}", "$1", "{spaces}", "$3", "{pattern}", "$4", "{negate}", "$7").Replace(inboxGroupsSQL(filter)) + `
		SELECT g.group_id::text, g.id::text, g.kind, COALESCE(g.direct_chat_id::text,''), COALESCE(g.direct_message_id::text,''), COALESCE(g.automation_id::text,''),
		       COALESCE(g.task_id::text, ''), COALESCE(g.space_prefix || '-' || g.task_sequence::text, ''),
		       COALESCE(g.task_title, ''), COALESCE(g.task_category, ''), g.task_type_icon,
		       COALESCE(g.conversation_id::text, ''),
		       COALESCE(g.space_id::text, ''), COALESCE(g.space_name, ''),
		       COALESCE(g.agent_id::text, ''),
		       -- 发起人：消息作者优先，其次记录的发起成员，最后是对话绑定的 Agent。
		       CASE WHEN g.author_kind = 'AGENT' THEN 'AGENT'
		            WHEN g.author_kind = 'USER' OR g.actor_user_id IS NOT NULL THEN 'USER'
		            WHEN g.agent_id IS NOT NULL THEN 'AGENT'
		            ELSE '' END,
		       COALESCE(CASE WHEN g.author_kind = 'AGENT' THEN g.message_agent_name
		                     WHEN g.author_kind = 'USER' THEN split_part(g.message_user_email, '@', 1)
		                     WHEN g.actor_user_id IS NOT NULL THEN split_part(g.actor_user_email, '@', 1)
		                     ELSE g.conversation_agent_name END, ''),
		       g.safe_summary, g.item_count, g.unread, g.snoozed_until, g.cleared_at, g.latest_at
		  FROM matched g
		 WHERE ($5::timestamptz IS NULL OR (g.latest_at, g.group_id) < ($5::timestamptz, NULLIF($6, '')::uuid))
		 ORDER BY g.latest_at DESC, g.group_id DESC
		 LIMIT $2`
	rows, err := tx.Query(ctx, sql, role, query.Limit+1, filter.spaceIDs, filter.pattern, cursorAt, cursorID, filter.negate)
	if err != nil {
		return InboxPage{}, err
	}
	defer rows.Close()
	groups := make([]InboxGroup, 0, query.Limit+1)
	for rows.Next() {
		var group InboxGroup
		if err := rows.Scan(&group.ID, &group.NotificationID, &group.Kind, &group.DirectChatID, &group.DirectMessageID, &group.AutomationID,
			&group.TaskID, &group.TaskDisplayID, &group.TaskTitle, &group.TaskStatusCategory, &group.TaskTypeIcon,
			&group.ConversationID, &group.SpaceID, &group.SpaceName, &group.AgentID,
			&group.ActorKind, &group.ActorName, &group.SafeSummary, &group.Count, &group.Unread,
			&group.SnoozedUntil, &group.ClearedAt, &group.LatestAt); err != nil {
			return InboxPage{}, err
		}
		groups = append(groups, group)
	}
	if err := rows.Err(); err != nil {
		return InboxPage{}, err
	}
	page := InboxPage{Groups: groups}
	if len(groups) > query.Limit {
		page.Groups = groups[:query.Limit]
		last := page.Groups[len(page.Groups)-1]
		page.NextCursor = encodeInboxCursor(last.LatestAt, last.ID)
	}
	counts, err := s.inboxCounts(ctx, tx, role)
	if err != nil {
		return InboxPage{}, err
	}
	page.Counts = counts
	return page, nil
}

// InboxStats 只返回各分区的组数，供侧栏未读角标使用。
func (s Service) InboxStats(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) (InboxCounts, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return InboxCounts{}, err
	}
	return s.inboxCounts(ctx, tx, strings.ToUpper(string(actor.Role)))
}

func (Service) inboxCounts(ctx context.Context, tx pgx.Tx, role string) (InboxCounts, error) {
	var counts InboxCounts
	err := tx.QueryRow(ctx, `
		WITH visible AS (
			SELECT COALESCE(n.task_id, n.conversation_id, n.automation_id, n.direct_chat_id) AS group_id, n.read_at,
			       `+inboxOtherKindsSQL+` AS is_other,
			       `+inboxSectionInboxSQL+` AS in_inbox,
			       `+inboxSectionLaterSQL+` AS in_later,
			       `+inboxSectionClearedSQL+` AS in_cleared
			  FROM openfox.notifications n
			 WHERE n.organization_id = openfox_private.current_organization_id()
			   AND n.recipient_user_id = openfox_private.current_actor_id()
			   AND n.inbox_level IS DISTINCT FROM 'off' AND `+notificationVisibleSQL+`
		)
		SELECT count(DISTINCT group_id) FILTER (WHERE in_inbox),
		       count(DISTINCT group_id) FILTER (WHERE in_inbox AND read_at IS NULL),
		       count(DISTINCT group_id) FILTER (WHERE in_inbox AND read_at IS NULL AND NOT is_other),
		       count(DISTINCT group_id) FILTER (WHERE in_inbox AND read_at IS NULL AND is_other),
		       count(DISTINCT group_id) FILTER (WHERE in_later),
		       count(DISTINCT group_id) FILTER (WHERE in_cleared)
		  FROM visible`, role).Scan(&counts.Inbox, &counts.InboxUnread, &counts.InboxImportantUnread, &counts.InboxOtherUnread, &counts.Later, &counts.Cleared)
	if err == nil {
		var n int
		err = tx.QueryRow(ctx, "SELECT count(*)"+pendingOfferFrom).Scan(&n)
		counts.Inbox += n
		counts.InboxUnread += n
		counts.InboxImportantUnread += n
	}
	return counts, err
}

// ApplyInboxAction 对若干组执行同一动作，返回受影响的通知条数。
// 重复执行同一动作是幂等的（受影响条数可能为 0），不视为错误。
func (s Service) ApplyInboxAction(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor,
	action InboxAction, section InboxSection, groupIDs []string, snoozedUntil *time.Time) (int64, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	sectionCondition, ok := sectionSQL(section)
	if !ok {
		return 0, ErrInvalidRequest
	}
	ids, err := normalizeGroupIDs(groupIDs)
	if err != nil {
		return 0, err
	}
	var set string
	args := []any{ids}
	switch action {
	case InboxActionRead:
		set = `read_at = COALESCE(read_at, clock_timestamp())`
	case InboxActionUnread:
		set = `read_at = NULL`
	case InboxActionClear:
		if section == InboxSectionCleared {
			return 0, ErrInvalidRequest
		}
		set = `cleared_at = clock_timestamp(), snoozed_until = NULL, read_at = COALESCE(read_at, clock_timestamp())`
	case InboxActionUnclear:
		if section != InboxSectionCleared {
			return 0, ErrInvalidRequest
		}
		set = `cleared_at = NULL`
	case InboxActionSnooze:
		if section != InboxSectionInbox || snoozedUntil == nil {
			return 0, ErrInvalidRequest
		}
		now := time.Now()
		if !snoozedUntil.After(now) || snoozedUntil.Sub(now) > MaxSnoozeDuration {
			return 0, ErrInvalidRequest
		}
		set = `snoozed_until = $2::timestamptz, read_at = COALESCE(read_at, clock_timestamp())`
		args = append(args, snoozedUntil.UTC())
	case InboxActionUnsnooze:
		if section != InboxSectionLater {
			return 0, ErrInvalidRequest
		}
		set = `snoozed_until = NULL`
	default:
		return 0, ErrInvalidRequest
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.notifications n
		   SET `+set+`
		 WHERE n.organization_id = openfox_private.current_organization_id()
		   AND n.recipient_user_id = openfox_private.current_actor_id()
		   AND COALESCE(n.task_id, n.conversation_id, n.automation_id, n.direct_chat_id) = ANY($1::uuid[]) AND n.inbox_level IS DISTINCT FROM 'off'
		   AND `+sectionCondition, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// ApplyInboxBulk 执行分区级批量操作：全部标为已读、归档全部已读、全部清理。
// 范围与列表一致（分区、重要性、空间是 / 不是、搜索词），只作用于当前成员看得到的组。
func (s Service) ApplyInboxBulk(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor,
	operation InboxBulkOperation, scope InboxScope) (int64, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return 0, err
	}
	filter, err := scope.filter()
	if err != nil {
		return 0, err
	}
	sectionCondition := filter.section
	where := `n.organization_id = openfox_private.current_organization_id()
		   AND n.recipient_user_id = openfox_private.current_actor_id()
		   AND ` + sectionCondition + filter.importance + `
		   AND COALESCE(n.task_id, n.conversation_id, n.automation_id, n.direct_chat_id) IN (SELECT group_id FROM matched)`
	var set string
	switch operation {
	case InboxBulkMarkRead:
		set = `read_at = clock_timestamp()`
		where += ` AND n.read_at IS NULL`
	case InboxBulkClearRead:
		if scope.Section == InboxSectionCleared {
			return 0, ErrInvalidRequest
		}
		// 只归档整组都已读的组，避免一组里还有未读时被部分清走。
		set = `cleared_at = clock_timestamp(), snoozed_until = NULL`
		where += `
		   AND NOT EXISTS (
		       SELECT 1 FROM openfox.notifications u
		        WHERE u.organization_id = n.organization_id
		          AND u.recipient_user_id = n.recipient_user_id
		          AND COALESCE(u.task_id, u.conversation_id, u.automation_id, u.direct_chat_id) = COALESCE(n.task_id, n.conversation_id, n.automation_id, n.direct_chat_id)
		          AND u.read_at IS NULL
		          AND ` + strings.ReplaceAll(sectionCondition, "n.", "u.") + `)`
	case InboxBulkClearAll:
		if scope.Section == InboxSectionCleared {
			return 0, ErrInvalidRequest
		}
		set = `cleared_at = clock_timestamp(), snoozed_until = NULL,
		       read_at = COALESCE(read_at, clock_timestamp())`
	default:
		return 0, ErrInvalidRequest
	}
	sql := strings.NewReplacer("{role}", "$1", "{spaces}", "$2", "{pattern}", "$3", "{negate}", "$4").Replace(inboxGroupsSQL(filter)) + `
		UPDATE openfox.notifications n SET ` + set + `
		 WHERE ` + where
	result, err := tx.Exec(ctx, sql, strings.ToUpper(string(actor.Role)), filter.spaceIDs, filter.pattern, filter.negate)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

func normalizeGroupIDs(values []string) ([]string, error) {
	if len(values) < 1 || len(values) > MaxInboxBatch {
		return nil, ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !membership.ValidateUUID(value) {
			return nil, ErrInvalidRequest
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		ids = append(ids, value)
	}
	return ids, nil
}

func escapeLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

// 游标是「最新时间(纳秒) + 组 ID」的 base64，客户端只原样回传。
func encodeInboxCursor(at time.Time, groupID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixNano(), 10) + "|" + groupID))
}

func decodeInboxCursor(value string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || !membership.ValidateUUID(id) {
		return time.Time{}, "", fmt.Errorf("invalid inbox cursor")
	}
	nanos, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return time.Time{}, "", err
	}
	return time.Unix(0, nanos).UTC(), id, nil
}
