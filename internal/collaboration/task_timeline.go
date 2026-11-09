package collaboration

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/membership"
)

// 任务表情回应与时间线（动态 + 评论按时间合并）。

const maxReactionEmojis = 50

var errInvalidReaction = NewRuleError(400, "INVALID_REACTION", "emoji must be 1 to 32 characters")

type ReactionSummary struct {
	Emoji   string `json:"emoji"`
	Count   int    `json:"count"`
	Reacted bool   `json:"reacted"`
}

// reactionSummaries 一次查询取任务全部目标（描述与各评论）的汇总，key 为 comment_id，描述为空串。
func (s Service) reactionSummaries(ctx context.Context, tx pgx.Tx, taskID string) (map[string][]ReactionSummary, error) {
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(comment_id::text, ''), emoji, count(*),
		       COALESCE(bool_or(user_id = openfox_private.current_actor_id()), false)
		  FROM openfox.task_reactions
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
		 GROUP BY comment_id, emoji
		 ORDER BY min(created_at), emoji`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]ReactionSummary)
	for rows.Next() {
		var commentID string
		var item ReactionSummary
		if err := rows.Scan(&commentID, &item.Emoji, &item.Count, &item.Reacted); err != nil {
			return nil, err
		}
		result[commentID] = append(result[commentID], item)
	}
	return result, rows.Err()
}

// ReactionSummary 返回任务描述（commentID 为空）或某条评论的表情汇总，没有则为空数组。
func (s Service) ReactionSummary(ctx context.Context, tx pgx.Tx, taskID, commentID string) ([]ReactionSummary, error) {
	all, err := s.reactionSummaries(ctx, tx, taskID)
	if err != nil {
		return nil, err
	}
	if items := all[commentID]; items != nil {
		return items, nil
	}
	return []ReactionSummary{}, nil
}

func normalizeEmoji(emoji string) (string, error) {
	emoji = strings.TrimSpace(emoji)
	if n := len([]rune(emoji)); n < 1 || n > 32 {
		return "", errInvalidReaction
	}
	return emoji, nil
}

func (s Service) checkReactionTarget(ctx context.Context, tx pgx.Tx, spaceID, taskID, commentID string) error {
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return err
	}
	if commentID == "" {
		return nil
	}
	if !membership.ValidateUUID(commentID) {
		return errSubNotFound
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.task_comments
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND id = $2::uuid)`, taskID, commentID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errSubNotFound
	}
	return nil
}

// AddReaction 幂等地给描述或评论加表情；同一目标最多 50 种不同表情。
func (s Service) AddReaction(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, emoji, commentID string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	emoji, err := normalizeEmoji(emoji)
	if err != nil {
		return err
	}
	commentID = strings.ToLower(strings.TrimSpace(commentID))
	if err := s.checkReactionTarget(ctx, tx, spaceID, taskID, commentID); err != nil {
		return err
	}
	// 锁住任务行，让「最多 50 种」的检查与插入互斥。
	if err := lockLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return err
	}
	var distinct int
	var known bool
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT emoji), COALESCE(bool_or(emoji = $3), false)
		  FROM openfox.task_reactions
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
		   AND comment_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid`, taskID, commentID, emoji).Scan(&distinct, &known); err != nil {
		return err
	}
	if !known && distinct >= maxReactionEmojis {
		return errLimitReached
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.task_reactions (organization_id, space_id, task_id, comment_id, user_id, emoji)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, NULLIF($3, '')::uuid, $4::uuid, $5)
		ON CONFLICT DO NOTHING`, spaceID, taskID, commentID, actor.UserID, emoji)
	return mapDatabaseError(err)
}

func (s Service) RemoveReaction(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, emoji, commentID string) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	emoji, err := normalizeEmoji(emoji)
	if err != nil {
		return err
	}
	commentID = strings.ToLower(strings.TrimSpace(commentID))
	if err := s.checkReactionTarget(ctx, tx, spaceID, taskID, commentID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM openfox.task_reactions
		 WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid
		   AND comment_id IS NOT DISTINCT FROM NULLIF($2, '')::uuid
		   AND user_id = $3::uuid AND emoji = $4`, taskID, commentID, actor.UserID, emoji)
	return err
}

// ---- 时间线 ----

type TimelineActivity struct {
	ID        string          `json:"id"`
	TaskID    string          `json:"task_id"`
	Kind      string          `json:"kind"`
	SafeDiff  json.RawMessage `json:"safe_diff"`
	ActorID   string          `json:"actor_id"`
	ActorName string          `json:"actor_name"`
	CreatedAt time.Time       `json:"created_at"`
}

type TimelineMention struct {
	UserID  string `json:"user_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

type ReplyPreview struct {
	ID            string `json:"id"`
	AuthorName    string `json:"author_name"`
	AuthorUserID  string `json:"author_user_id,omitempty"`
	AuthorAgentID string `json:"author_agent_id,omitempty"`
	Body          string `json:"body"`
}

type TimelineComment struct {
	HasReplies        bool              `json:"has_replies"`
	EditVersion       int64             `json:"edit_version"`
	CanDelete         bool              `json:"can_delete"`
	CanEdit           bool              `json:"can_edit"`
	EditedAt          *time.Time        `json:"edited_at,omitempty"`
	ResolvedAt        *time.Time        `json:"resolved_at,omitempty"`
	ResolvedCommentID string            `json:"resolved_comment_id,omitempty"`
	ReplyCount        int               `json:"reply_count,omitempty"`
	RecentReplies     []ReplyPreview    `json:"recent_replies,omitempty"`
	OrganizationID    string            `json:"organization_id"`
	SpaceID           string            `json:"space_id"`
	TaskID            string            `json:"task_id"`
	ID                string            `json:"id"`
	AuthorUserID      string            `json:"author_user_id,omitempty"`
	AuthorAgentID     string            `json:"author_agent_id,omitempty"`
	AuthorKind        string            `json:"author_kind"`
	AuthorName        string            `json:"author_name"`
	Body              string            `json:"body"`
	ClientID          string            `json:"client_comment_id,omitempty"`
	ReplyToID         string            `json:"reply_to_id,omitempty"`
	Mentions          []TimelineMention `json:"mentions"`
	Reactions         []ReactionSummary `json:"reactions"`
	CreatedAt         time.Time         `json:"created_at"`
}

type TimelineItem struct {
	Type     string            `json:"type"`
	Activity *TimelineActivity `json:"activity,omitempty"`
	Comment  *TimelineComment  `json:"comment,omitempty"`
}

type TimelinePage struct {
	Page[TimelineItem]
	Total *int64
}

// Scope both the root and every descendant to the task. Older clients may have
// replied to another reply; those messages belong to the same topic.
const timelineReplies = `WITH RECURSIVE replies(root_id,id,created_at) AS (
	SELECT root.id, c.id, c.created_at FROM openfox.task_comments root
	JOIN openfox.task_comments c ON c.organization_id = root.organization_id AND c.task_id = root.task_id AND c.reply_to_id = root.id
	WHERE root.organization_id = openfox_private.current_organization_id() AND root.task_id = $1::uuid
	  AND root.reply_to_id IS NULL AND root.id = ANY($2::uuid[])
	UNION ALL
	SELECT p.root_id, c.id, c.created_at FROM replies p JOIN openfox.task_comments c ON c.reply_to_id = p.id
	WHERE c.organization_id = openfox_private.current_organization_id() AND c.task_id = $1::uuid
) `

// ListTimeline 按 (created_at, id) 升序合并任务动态与评论，keyset 分页。
func (s Service) ListTimeline(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, canWrite bool, limit int, cursor *Cursor, thread string) (TimelinePage, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return TimelinePage{}, err
	}
	if limit < 1 || limit > MaxPageSize {
		return TimelinePage{}, ErrInvalidPageSize
	}
	if err := s.requireLiveTask(ctx, tx, spaceID, taskID); err != nil {
		return TimelinePage{}, err
	}
	var after, afterID any
	if cursor != nil {
		after, afterID = cursor.CreatedAt, cursor.ID
	}
	var rows pgx.Rows
	var err error
	switch thread {
	case "", "roots":
		filter := ""
		if thread == "roots" {
			filter = " AND c.reply_to_id IS NULL"
		}
		rows, err = tx.Query(ctx, `SELECT u.kind, u.id::text, u.created_at FROM (
			SELECT 'activity' AS kind, a.id, a.created_at FROM openfox.task_activities a
			 WHERE a.organization_id = openfox_private.current_organization_id() AND a.task_id = $1::uuid
			UNION ALL
			SELECT 'comment', c.id, c.created_at FROM openfox.task_comments c
			 WHERE c.organization_id = openfox_private.current_organization_id() AND c.task_id = $1::uuid`+filter+`
		) u WHERE ($3::timestamptz IS NULL OR (u.created_at, u.id) > ($3,$4::uuid))
		ORDER BY u.created_at, u.id LIMIT $2`, taskID, limit+1, after, afterID)
	default:
		if !membership.ValidateUUID(thread) {
			return TimelinePage{}, NewRuleError(400, "INVALID_THREAD", "thread must be roots or a comment UUID")
		}
		rows, err = tx.Query(ctx, timelineReplies+`
			SELECT 'comment', id::text, created_at FROM replies
			WHERE ($4::timestamptz IS NULL OR (created_at, id) < ($4,$5::uuid))
			ORDER BY created_at DESC, replies.id DESC LIMIT $3`, taskID, []string{thread}, limit+1, after, afterID)
	}
	if err != nil {
		return TimelinePage{}, err
	}
	type ref struct {
		kind, id string
		at       time.Time
	}
	refs := make([]ref, 0, limit+1)
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.kind, &r.id, &r.at); err != nil {
			rows.Close()
			return TimelinePage{}, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TimelinePage{}, err
	}
	page := TimelinePage{Page: Page[TimelineItem]{Items: make([]TimelineItem, 0, limit)}}
	if thread == "roots" && cursor == nil {
		var total int64
		err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM openfox.task_activities WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid)
		  + (SELECT count(*) FROM openfox.task_comments WHERE organization_id = openfox_private.current_organization_id() AND task_id = $1::uuid)`, taskID).Scan(&total)
		if err != nil {
			return TimelinePage{}, err
		}
		page.Total = &total
	}
	if len(refs) > limit {
		refs = refs[:limit]
		last := refs[limit-1]
		page.NextCursor, err = EncodeCursor(Cursor{CreatedAt: last.at, ID: last.id})
		if err != nil {
			return TimelinePage{}, err
		}
	}
	var activityIDs, commentIDs []string
	for _, r := range refs {
		if r.kind == "activity" {
			activityIDs = append(activityIDs, r.id)
		} else {
			commentIDs = append(commentIDs, r.id)
		}
	}
	activities := make(map[string]*TimelineActivity, len(activityIDs))
	if len(activityIDs) > 0 {
		arows, err := tx.Query(ctx, `
			SELECT a.id::text, a.task_id::text, a.kind, a.safe_diff, a.actor_id::text, COALESCE(u.email, ''), a.created_at
			  FROM openfox.task_activities a LEFT JOIN openfox.users u ON u.id = a.actor_id
			 WHERE a.organization_id = openfox_private.current_organization_id() AND a.id = ANY($1::uuid[])`, activityIDs)
		if err != nil {
			return TimelinePage{}, err
		}
		for arows.Next() {
			var item TimelineActivity
			var diff []byte
			if err := arows.Scan(&item.ID, &item.TaskID, &item.Kind, &diff, &item.ActorID, &item.ActorName, &item.CreatedAt); err != nil {
				arows.Close()
				return TimelinePage{}, err
			}
			item.SafeDiff = json.RawMessage(diff)
			activities[item.ID] = &item
		}
		arows.Close()
		if err := arows.Err(); err != nil {
			return TimelinePage{}, err
		}
	}
	comments := make(map[string]*TimelineComment, len(commentIDs))
	if len(commentIDs) > 0 {
		reactions, err := s.reactionSummaries(ctx, tx, taskID)
		if err != nil {
			return TimelinePage{}, err
		}
		crows, err := tx.Query(ctx, `
			SELECT c.organization_id::text, c.space_id::text, c.task_id::text, c.id::text,
			       COALESCE(c.author_user_id::text, ''), COALESCE(c.author_agent_id::text, ''),
			       COALESCE(u.email, a.name, ''), c.body, COALESCE(c.client_comment_id, ''), COALESCE(c.reply_to_id::text, ''), c.created_at, c.edit_version, c.edited_at, c.resolved_at, COALESCE(c.resolved_comment_id::text,''),
                   EXISTS(SELECT 1 FROM openfox.task_comments child WHERE child.organization_id=c.organization_id AND child.task_id=c.task_id AND child.reply_to_id=c.id)
			  FROM openfox.task_comments c LEFT JOIN openfox.users u ON u.id = c.author_user_id
			  LEFT JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.author_agent_id
			 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = ANY($1::uuid[])`, commentIDs)
		if err != nil {
			return TimelinePage{}, err
		}
		for crows.Next() {
			var item TimelineComment
			if err := crows.Scan(&item.OrganizationID, &item.SpaceID, &item.TaskID, &item.ID, &item.AuthorUserID,
				&item.AuthorAgentID, &item.AuthorName, &item.Body, &item.ClientID, &item.ReplyToID, &item.CreatedAt, &item.EditVersion, &item.EditedAt, &item.ResolvedAt, &item.ResolvedCommentID, &item.HasReplies); err != nil {
				crows.Close()
				return TimelinePage{}, err
			}
			// Agent 任务工具写的评论：作者是 Agent（见 000044），author_name 为 Agent 名称。
			item.CanEdit = canWrite && canEditComment(actor, item.AuthorUserID)
			item.CanDelete = canWrite && canDeleteComment(actor, item.AuthorUserID)
			item.AuthorKind = "USER"
			if item.AuthorAgentID != "" {
				item.AuthorKind = "AGENT"
			}
			item.Mentions = []TimelineMention{}
			item.Reactions = reactions[item.ID]
			if item.Reactions == nil {
				item.Reactions = []ReactionSummary{}
			}
			comments[item.ID] = &item
		}
		crows.Close()
		if err := crows.Err(); err != nil {
			return TimelinePage{}, err
		}
		mrows, err := tx.Query(ctx, `
			SELECT comment_id::text, COALESCE(mentioned_user_id::text, ''), COALESCE(mentioned_agent_id::text, '')
			  FROM openfox.comment_mentions
			 WHERE organization_id = openfox_private.current_organization_id() AND comment_id = ANY($1::uuid[])
			 ORDER BY comment_id, position`, commentIDs)
		if err != nil {
			return TimelinePage{}, err
		}
		for mrows.Next() {
			var commentID string
			var mention TimelineMention
			if err := mrows.Scan(&commentID, &mention.UserID, &mention.AgentID); err != nil {
				mrows.Close()
				return TimelinePage{}, err
			}
			if comment := comments[commentID]; comment != nil {
				comment.Mentions = append(comment.Mentions, mention)
			}
		}
		mrows.Close()
		if err := mrows.Err(); err != nil {
			return TimelinePage{}, err
		}
	}
	if thread == "roots" && len(commentIDs) != 0 {
		rows, err := tx.Query(ctx, timelineReplies+`
			SELECT ranked.root_id::text, ranked.total, c.id::text, COALESCE(u.email,a.name,''),
			       COALESCE(c.author_user_id::text,''), COALESCE(c.author_agent_id::text,''), left(c.body, 180)
			FROM (SELECT *, count(*) OVER (PARTITION BY root_id) AS total,
				row_number() OVER (PARTITION BY root_id ORDER BY created_at DESC, id DESC) AS position FROM replies) ranked
			JOIN openfox.task_comments c ON c.id = ranked.id AND c.task_id = $1::uuid AND c.organization_id = openfox_private.current_organization_id()
			LEFT JOIN openfox.users u ON u.id = c.author_user_id
			LEFT JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.author_agent_id
			WHERE ranked.position <= 5 ORDER BY ranked.root_id, ranked.position DESC`, taskID, commentIDs)
		if err != nil {
			return TimelinePage{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var root string
			var count int
			var preview ReplyPreview
			if err := rows.Scan(&root, &count, &preview.ID, &preview.AuthorName, &preview.AuthorUserID, &preview.AuthorAgentID, &preview.Body); err != nil {
				return TimelinePage{}, err
			}
			comment := comments[root]
			comment.ReplyCount = count
			comment.RecentReplies = append(comment.RecentReplies, preview)
		}
		if err := rows.Err(); err != nil {
			return TimelinePage{}, err
		}
	}
	for _, r := range refs {
		if r.kind == "activity" {
			if item := activities[r.id]; item != nil {
				page.Items = append(page.Items, TimelineItem{Type: "activity", Activity: item})
			}
		} else if item := comments[r.id]; item != nil {
			page.Items = append(page.Items, TimelineItem{Type: "comment", Comment: item})
		}
	}
	return page, nil
}
