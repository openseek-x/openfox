package conversations

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations/mentiontext"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"strings"
)

// 阶段 12g · 分享快照的来源读取。读权限判定完全复用现有的会话路径（getVisible / authorizeSpace），
// 因此“谁能创建快照”与“谁能读原对象”始终一致。这里只取用户可见的正文、发言者显示名与时间。

var (
	// ErrShareSourceEmpty 表示来源里没有可分享的文本。
	ErrShareSourceEmpty = errors.New("conversations: share source is empty")
	// ErrShareSourceTooLarge 表示来源条目数超过单个快照的上限。
	ErrShareSourceTooLarge = errors.New("conversations: share source is too large")
)

// MaxShareItems 是单个快照的条目数上限（与 share_snapshots.item_count 的约束保持一致且更严格）。
const MaxShareItems = 500

// ShareItem 是快照里的一条消息。ReplyToID 是消息引用的源ID，不包含引用快照正文。
type ShareItem struct {
	ID         string
	AuthorKind string // USER 或 AGENT
	AuthorName string
	Body       string
	CreatedAt  time.Time
	ReplyToID  string
}

type ShareSource struct {
	// Title 是来源自带的标题或首条正文摘要，创建人可以在创建时覆盖。
	Title string
	Items []ShareItem
}

// anonymousAuthorName 在发言者没有显示名时使用，绝不回退到邮箱。
const anonymousAuthorName = "Member"

// ConversationShareSource 读取对话里可见的文本消息（USER / AGENT 的 TEXT 消息，不含系统消息、草稿卡与工具输出）。
func (s Service) ConversationShareSource(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, messageID string) (ShareSource, error) {
	if messageID != "" && !membership.ValidateUUID(messageID) {
		return ShareSource{}, ErrNotFound
	}
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, false)
	if err != nil {
		return ShareSource{}, err
	}
	var title *string
	if messageID == "" {
		if err := tx.QueryRow(ctx, `
		SELECT title FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conversation.ID).Scan(&title); err != nil {
			return ShareSource{}, err
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT m.id::text, m.author_kind, COALESCE(NULLIF(btrim(u.full_name), ''), ''), COALESCE(a.name, ''),
		       m.body, m.created_at, COALESCE(m.reply_to_id::text, '')
		  FROM openfox.messages m
		  LEFT JOIN openfox.users u ON u.id = m.author_user_id
		  LEFT JOIN openfox.agents a ON a.organization_id = m.organization_id AND a.id = m.author_agent_id
		 WHERE m.organization_id = openfox_private.current_organization_id()
		   AND m.conversation_id = $1::uuid AND m.kind = 'TEXT' AND m.author_kind IN ('USER', 'AGENT')
           AND ($3::text = '' OR m.id = NULLIF($3::text, '')::uuid)
		 ORDER BY m.seq
		 LIMIT $2`, conversation.ID, MaxShareItems+1, strings.ToLower(messageID))
	if err != nil {
		return ShareSource{}, err
	}
	source, err := collectShareItems(rows)
	if err != nil {
		return ShareSource{}, err
	}
	if messageID != "" && len(source.Items) == 0 {
		return ShareSource{}, ErrNotFound
	}
	if title != nil && *title != "" {
		source.Title = *title
	}
	return finishShareSource(source)
}

// CommentThreadShareSource 读取评论所在线程（根评论及全部未删除的回复）。目标评论被删除或不在该任务下一律 ErrNotFound。
func (s Service) CommentThreadShareSource(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID, commentID string, actor Actor) (ShareSource, error) {
	if !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) || !membership.ValidateUUID(commentID) {
		return ShareSource{}, ErrNotFound
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, taskID, policy.ActionReadObject); err != nil {
		return ShareSource{}, err
	}
	var taskLive bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL)`, spaceID, taskID).Scan(&taskLive); err != nil {
		return ShareSource{}, err
	}
	if !taskLive {
		return ShareSource{}, ErrNotFound
	}
	// 先沿 reply_to_id 向上找到根评论（RLS 会隐藏已删除的评论，链断了就是不存在）。
	var rootID string
	err := tx.QueryRow(ctx, `
		WITH RECURSIVE chain(id, parent) AS (
		    SELECT id, reply_to_id FROM openfox.task_comments
		     WHERE organization_id = openfox_private.current_organization_id()
		       AND space_id = $1::uuid AND task_id = $2::uuid AND id = $3::uuid
		    UNION
		    SELECT c.id, c.reply_to_id FROM openfox.task_comments c JOIN chain ON c.id = chain.parent
		     WHERE c.organization_id = openfox_private.current_organization_id()
		       AND c.space_id = $1::uuid AND c.task_id = $2::uuid
		)
		SELECT id::text FROM chain WHERE parent IS NULL`, spaceID, taskID, commentID).Scan(&rootID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareSource{}, ErrNotFound
	}
	if err != nil {
		return ShareSource{}, err
	}
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE subtree(id) AS (
		    SELECT id FROM openfox.task_comments
		     WHERE organization_id = openfox_private.current_organization_id()
		       AND space_id = $1::uuid AND task_id = $2::uuid AND id = $3::uuid
		    UNION
		    SELECT c.id FROM openfox.task_comments c JOIN subtree p ON c.reply_to_id = p.id
		     WHERE c.organization_id = openfox_private.current_organization_id()
		       AND c.space_id = $1::uuid AND c.task_id = $2::uuid
		)
		SELECT c.id::text, CASE WHEN c.author_agent_id IS NOT NULL THEN 'AGENT' ELSE 'USER' END,
		       COALESCE(NULLIF(btrim(u.full_name), ''), ''), COALESCE(a.name, ''),
		       c.body, c.created_at, COALESCE(c.reply_to_id::text, '')
		  FROM openfox.task_comments c
		  JOIN subtree ON subtree.id = c.id
		  LEFT JOIN openfox.users u ON u.id = c.author_user_id
		  LEFT JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.author_agent_id
		 WHERE c.organization_id = openfox_private.current_organization_id()
		 ORDER BY c.created_at, c.id
		 LIMIT $4`, spaceID, taskID, rootID, MaxShareItems+1)
	if err != nil {
		return ShareSource{}, err
	}
	source, err := collectShareItems(rows)
	if err != nil {
		return ShareSource{}, err
	}
	return finishShareSource(source)
}

func collectShareItems(rows pgx.Rows) (ShareSource, error) {
	defer rows.Close()
	var source ShareSource
	for rows.Next() {
		var item ShareItem
		var userName, agentName string
		if err := rows.Scan(&item.ID, &item.AuthorKind, &userName, &agentName, &item.Body, &item.CreatedAt, &item.ReplyToID); err != nil {
			return ShareSource{}, err
		}
		item.AuthorName = userName
		if item.AuthorKind == "AGENT" {
			item.AuthorName = agentName
		}
		if item.AuthorName == "" {
			item.AuthorName = anonymousAuthorName
		}
		// 提及令牌渲染为纯文本 @名字：快照里不保留任何 openfox://mention 标识符。
		item.Body = mentiontext.Plain(item.Body)
		source.Items = append(source.Items, item)
	}
	return source, rows.Err()
}

func finishShareSource(source ShareSource) (ShareSource, error) {
	if len(source.Items) == 0 {
		return ShareSource{}, ErrShareSourceEmpty
	}
	if len(source.Items) > MaxShareItems {
		return ShareSource{}, ErrShareSourceTooLarge
	}
	return source, nil
}
