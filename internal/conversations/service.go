package conversations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"openfox/internal/attachments"
	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
)

type Service struct{}

func (Service) CheckActor(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor) error {
	return (membership.Service{}).CheckActor(ctx, tx, organizationID, membership.ActorSnapshot{
		UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion,
	})
}

func (s Service) Create(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, input CreateInput) (Conversation, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Conversation{}, err
	}
	var err error
	input.CrewID = strings.TrimSpace(input.CrewID)
	if input.CrewID != "" {
		// 小队对话：Agent 取小队队长；这里先规整必要字段，再按普通会话的规则继续校验。
		input.ID = strings.TrimSpace(input.ID)
		input.SpaceID = strings.TrimSpace(input.SpaceID)
		input.AgentID = strings.TrimSpace(input.AgentID)
		input.TaskID = strings.TrimSpace(input.TaskID)
		if !membership.ValidateUUID(input.ID) {
			return Conversation{}, ErrInvalidRequest
		}
		leader, err := s.resolveCrewForCreate(ctx, tx, organizationID, input, actor)
		if err != nil {
			return Conversation{}, err
		}
		input.AgentID = leader
	}
	input, err = input.normalize()
	if err != nil || !membership.ValidateUUID(input.ID) || !membership.ValidateUUID(input.SpaceID) || !membership.ValidateUUID(input.AgentID) || (input.TaskID != "" && !membership.ValidateUUID(input.TaskID)) {
		return Conversation{}, ErrInvalidRequest
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, input.SpaceID, input.ID, policy.ActionCreateObject); err != nil {
		return Conversation{}, err
	}
	var agentStatus string
	var agentSpace string
	var archivedAt *time.Time
	var builtinKind, agentOwner, agentVisibility string
	var directChat bool
	if err := tx.QueryRow(ctx, `
		SELECT a.space_id::text, a.status, a.archived_at, COALESCE(a.builtin_kind, ''), COALESCE(a.owner_user_id::text, ''),
		       a.visibility,
		       a.allow_direct_chat AND NOT EXISTS (
		           SELECT 1 FROM openfox.agent_crew_members m
		            WHERE m.organization_id = a.organization_id AND m.agent_id = a.id AND m.exclusive)
		  FROM openfox.agents a
		 WHERE a.organization_id = openfox_private.current_organization_id()
		   AND a.id = $1::uuid`, input.AgentID).Scan(&agentSpace, &agentStatus, &archivedAt, &builtinKind, &agentOwner,
		&agentVisibility, &directChat); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Conversation{}, ErrNotFound
		}
		return Conversation{}, err
	}
	// 他人的私有 Agent 不可见。
	if agentVisibility == "PRIVATE" && agentOwner != actor.UserID {
		return Conversation{}, ErrNotFound
	}
	if agentSpace != input.SpaceID || agentStatus != "ACTIVE" || archivedAt != nil {
		return Conversation{}, ErrForbidden
	}
	// 单聊（不挂任务的会话）：Agent 须允许单聊且不是小队的专属成员。任务会话不受影响。
	// 飞书任务助手（TASK_ASSISTANT）是空间共享的内置 Agent，不受单聊开关限制。
	if input.TaskID == "" && builtinKind == "" && !directChat && input.CrewID == "" {
		return Conversation{}, ErrForbidden
	}
	// 内置小助手只属于本人：别人看不到它，会话只能是本人的私有会话、不能挂任务。
	if builtinKind == BuiltinAssistant {
		if agentOwner != actor.UserID {
			return Conversation{}, ErrNotFound
		}
		if input.Visibility != VisibilityPrivate || input.TaskID != "" {
			return Conversation{}, ErrInvalidRequest
		}
	}
	if input.TaskID != "" {
		var taskSpace string
		if err := tx.QueryRow(ctx, `
			SELECT space_id::text FROM openfox.tasks
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
			   AND deleted_at IS NULL`, input.TaskID).Scan(&taskSpace); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Conversation{}, ErrNotFound
			}
			return Conversation{}, err
		}
		if taskSpace != input.SpaceID {
			return Conversation{}, ErrInvalidRequest
		}
	}
	origin := input.Origin
	if origin == "" {
		origin = OriginUser
	}
	if (origin != OriginUser && origin != OriginTaskAssignment && origin != OriginIM) || (origin == OriginTaskAssignment && input.TaskID == "") ||
		(origin == OriginIM && input.TaskID != "") {
		return Conversation{}, ErrInvalidRequest
	}
	binding, err := s.bindWorkspace(ctx, tx, organizationID, actor, input)
	if err != nil {
		return Conversation{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.conversations (
			organization_id, id, space_id, agent_id, task_id, owner_user_id, visibility, origin, crew_id, workspace_selection
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid,
		          NULLIF($4, '')::uuid, $5::uuid, $6, $7, NULLIF($8, '')::uuid,$9::jsonb)`,
		input.ID, input.SpaceID, input.AgentID, input.TaskID, actor.UserID, input.Visibility, origin, input.CrewID, binding)
	if err != nil {
		return Conversation{}, mapError(err)
	}
	if binding != nil && binding.Directory != "" {
		if err := RememberWorkDirectory(ctx, tx, organizationID, actor.UserID, input.AgentID, binding.ComputerID, binding.Directory); err != nil {
			return Conversation{}, err
		}
	}
	return s.getVisible(ctx, tx, organizationID, input.ID, actor, false)
}

func (s Service) Get(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) (Conversation, error) {
	return s.getVisible(ctx, tx, organizationID, conversationID, actor, false)
}

func (s Service) AddMember(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, userID string, input MemberInput) error {
	conversation, err := s.loadVisible(ctx, tx, organizationID, conversationID, actor, true, true)
	if err != nil {
		return err
	}
	if conversation.OwnerUserID != actor.UserID || !membership.ValidateUUID(userID) || (input.AccessLevel != AccessViewer && input.AccessLevel != AccessCommenter) {
		return ErrForbidden
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT status = 'ACTIVE' FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, userID).Scan(&active); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !active || userID == actor.UserID {
		return ErrForbidden
	}
	if conversation.TaskID != "" {
		// Only accepting an offer can add someone to a task conversation. An
		// UPDATE cannot recreate a removed member or consume a pending invite.
		tag, err := tx.Exec(ctx, `UPDATE openfox.conversation_members
            SET access_level=$3, version=version+1, updated_at=clock_timestamp()
            WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND user_id=$2::uuid`, conversationID, userID, input.AccessLevel)
		if err != nil {
			return mapError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrInviteRequired
		}
	} else {
		_, err = tx.Exec(ctx, `
		INSERT INTO openfox.conversation_members (organization_id, conversation_id, user_id, access_level)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3)
		ON CONFLICT (organization_id, conversation_id, user_id)
		DO UPDATE SET access_level = EXCLUDED.access_level,
		              version = openfox.conversation_members.version + 1,
		              updated_at = clock_timestamp()`, conversationID, userID, input.AccessLevel)
	}
	if err != nil {
		return mapError(err)
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET visibility = 'SHARED', version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conversationID)
	return mapError(err)
}

func (s Service) RemoveMember(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, userID string) error {
	conversation, err := s.loadVisible(ctx, tx, organizationID, conversationID, actor, true, true)
	if err != nil {
		return err
	}
	if conversation.OwnerUserID != actor.UserID || !membership.ValidateUUID(userID) {
		return ErrForbidden
	}
	result, err := tx.Exec(ctx, `
		DELETE FROM openfox.conversation_members
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND user_id = $2::uuid`, conversationID, userID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, conversationID)
	return err
}

func (s Service) PostMessage(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, input MessageInput) (Message, error) {
	var err error
	input, err = input.normalize()
	if err != nil || !membership.ValidateUUID(input.ID) || (input.ReplyToID != "" && !membership.ValidateUUID(input.ReplyToID)) {
		return Message{}, ErrInvalidRequest
	}
	conversation, err := s.loadVisible(ctx, tx, organizationID, conversationID, actor, true, true)
	if err != nil {
		return Message{}, err
	}
	if conversation.Lifecycle != LifecycleOpen {
		return Message{}, ErrConversationClosed
	}
	if input.ClientMessageID != "" {
		if existing, found, err := s.findMessageByClientID(ctx, tx, organizationID, conversationID, input.ClientMessageID); err != nil {
			return Message{}, err
		} else if found {
			if existing.AssistantAction != input.AssistantAction || existing.Body != input.Body || existing.ReplyToID != input.ReplyToID || !sameMentions(existing.Mentions, input.Mentions) {
				return Message{}, ErrDuplicateMessage
			}
			return existing, nil
		}
	}
	assistant, err := assistantConversation(ctx, tx, conversation, actor)
	if err != nil {
		return Message{}, err
	}
	if input.AssistantAction != "" && !assistant {
		return Message{}, ErrInvalidRequest
	}
	if err := s.validateMentions(ctx, tx, organizationID, conversation, actor.UserID, input.Mentions); err != nil {
		return Message{}, err
	}
	var seq int64
	if err := tx.QueryRow(ctx, `
		UPDATE openfox.conversations
		   SET next_message_seq = next_message_seq + 1,
		       version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		RETURNING next_message_seq - 1`, conversationID).Scan(&seq); err != nil {
		return Message{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.messages (
			organization_id, id, conversation_id, seq, author_kind, author_user_id,
			body, client_message_id, reply_to_id, assistant_action, authenticated_user_input
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3,
		          'USER', openfox_private.current_actor_id(), $4, NULLIF($6, ''), NULLIF($5, '')::uuid, NULLIF($7, ''),$8)`,
		input.ID, conversationID, seq, input.Body, input.ReplyToID, input.ClientMessageID, input.AssistantAction, input.AuthenticatedUserInput)
	if err != nil {
		return Message{}, mapError(err)
	}
	if err := insertMentions(ctx, tx, organizationID, input.ID, input.Mentions); err != nil {
		return Message{}, err
	}
	if err := attachments.Bind(ctx, tx, conversationID, input.ID, actor.UserID, input.AttachmentIDs); err != nil {
		return Message{}, err
	}
	runMentions := input.Mentions
	if assistant {
		// 小助手的 1:1 会话不需要 @，每条消息都交给助手执行。
		runMentions = []Mention{{AgentID: conversation.AgentID}}
	}
	if conversation.CrewID != "" {
		if err := s.enqueueCrewInputs(ctx, tx, conversation, actor.UserID, input.ID, input.Body, input.Mentions); err != nil {
			return Message{}, err
		}
	} else if err := s.enqueueAgentMention(ctx, tx, organizationID, conversation, input.ID, input.Body, runMentions); err != nil {
		return Message{}, err
	}
	for _, mention := range input.Mentions {
		if mention.UserID == "" || mention.UserID == actor.UserID {
			continue
		}
		if _, err := tx.Exec(ctx, `
			SELECT openfox_private.enqueue_notification(
				$1::uuid, 'MENTION', $2::uuid, $3::uuid, NULL::uuid, NULL::uuid,
				$4, 'You were mentioned in a conversation.')`,
			mention.UserID, conversationID, input.ID, "mention:"+input.ID+":"+mention.UserID); err != nil {
			return Message{}, err
		}
	}
	return s.loadMessage(ctx, tx, organizationID, input.ID)
}

func (s Service) ListMessages(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, limit int, after int64) (Page[Message], error) {
	return s.ListMessagePage(ctx, tx, organizationID, conversationID, actor, MessageQuery{Limit: limit, After: after})
}

// MessageQuery 是消息分页：默认按 seq 升序取 seq > After；Backward 时取 seq < Before（Before 为 0 表示从最新一条起）
// 中最新的 Limit 条，页内仍按 seq 升序返回，NextCursor 为页内最小 seq（还有更早消息时），作为下一次的 Before。
type MessageQuery struct {
	Around   string
	Limit    int
	After    int64
	Before   int64
	Backward bool
}

// messageRunSQL 推导消息对应的 Run（m 为消息行）：
//   - USER 消息：它的执行输入（conversation_inputs.message_id）被分配到的 Run；
//   - AGENT / SYSTEM 消息（ACTION_EXECUTED 除外）：同会话中 finished_at 不晚于消息创建时间的最近一次终态 Run。
//     FinalizeConversation 在 Run 终态 CAS 的同一事务里、写完 finished_at 之后才追加消息，
//     所以终态消息总是落在它自己的 Run 之后、下一次 Run 结束之前；
//   - ACTION_EXECUTED（用户确认草稿后追加）不属于任何 Run，为 null。
const messageRunSQL = `CASE
	WHEN m.author_kind = 'USER' THEN (
	    SELECT ci.run_id::text FROM openfox.conversation_inputs ci
	     WHERE ci.organization_id = m.organization_id AND ci.conversation_id = m.conversation_id
	       AND ci.message_id = m.id AND ci.run_id IS NOT NULL
	     ORDER BY ci.created_at DESC LIMIT 1)
	WHEN m.kind = 'ACTION_EXECUTED' THEN NULL
	ELSE (
	    SELECT r.id::text FROM openfox.runs r
	     WHERE r.organization_id = m.organization_id AND r.conversation_id = m.conversation_id
	       AND r.finished_at IS NOT NULL AND r.finished_at <= m.created_at
	     ORDER BY r.finished_at DESC, r.id DESC LIMIT 1)
	END`

func (s Service) ListMessagePage(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, query MessageQuery) (Page[Message], error) {
	limit := query.Limit
	if limit < 1 || limit > MaxPageSize || query.After < 0 || query.Before < 0 || (query.Backward && query.After != 0) || (!query.Backward && query.Before != 0) {
		return Page[Message]{}, ErrInvalidRequest
	}
	if _, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, false); err != nil {
		return Page[Message]{}, err
	}
	if query.Around != "" {
		if !membership.ValidateUUID(query.Around) || query.Before != 0 || query.After != 0 {
			return Page[Message]{}, ErrInvalidRequest
		}
		// Sequence numbers are unique per conversation. This bound keeps the
		// target in the page even if some nearby numbers have gaps.
		err := tx.QueryRow(ctx, `SELECT seq + $3::bigint FROM openfox.messages
			WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND id=$2::uuid`,
			conversationID, query.Around, (limit+1)/2).Scan(&query.Before)
		if errors.Is(err, pgx.ErrNoRows) {
			return Page[Message]{}, ErrNotFound
		}
		if err != nil {
			return Page[Message]{}, err
		}
		query.Backward = true
	}
	condition, order, bound := "m.seq > $2", "m.seq, m.id", query.After
	if query.Backward {
		condition, order, bound = "($2 = 0 OR m.seq < $2)", "m.seq DESC, m.id DESC", query.Before
	}
	rows, err := tx.Query(ctx, `
		SELECT m.organization_id::text, m.id::text, m.conversation_id::text, m.seq,
		       m.author_kind, COALESCE(m.author_user_id::text, ''), COALESCE(m.author_agent_id::text, ''),
		       m.body, COALESCE(m.client_message_id, ''), COALESCE(m.reply_to_id::text, ''), m.created_at,
		       m.kind, m.payload, COALESCE(m.assistant_action, ''), `+messageRunSQL+`
		  FROM openfox.messages m
		 WHERE m.organization_id = openfox_private.current_organization_id()
		   AND m.conversation_id = $1::uuid AND `+condition+`
		 ORDER BY `+order+`
		 LIMIT $3`, conversationID, bound, limit+1)
	if err != nil {
		return Page[Message]{}, err
	}
	defer rows.Close()
	page := Page[Message]{Items: make([]Message, 0, limit)}
	for rows.Next() {
		var item Message
		var payload []byte
		if err := rows.Scan(&item.OrganizationID, &item.ID, &item.ConversationID, &item.Seq,
			&item.AuthorKind, &item.AuthorUserID, &item.AuthorAgentID, &item.Body,
			&item.ClientMessageID, &item.ReplyToID, &item.CreatedAt, &item.Kind, &payload, &item.AssistantAction, &item.RunID); err != nil {
			return Page[Message]{}, err
		}
		if len(payload) > 0 {
			item.Payload = json.RawMessage(payload)
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return Page[Message]{}, err
	}
	rows.Close()
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].Seq
	}
	if query.Backward {
		// 倒序取数后翻回升序，前端直接拼到已有消息前面。
		for left, right := 0, len(page.Items)-1; left < right; left, right = left+1, right-1 {
			page.Items[left], page.Items[right] = page.Items[right], page.Items[left]
		}
	}
	if err := loadMessageMentions(ctx, tx, page.Items); err != nil {
		return Page[Message]{}, err
	}
	if err := loadMessageAttachments(ctx, tx, page.Items); err != nil {
		return Page[Message]{}, err
	}
	return page, nil
}

func (s Service) PostComment(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, input CommentInput) (Comment, error) {
	var err error
	input, err = input.normalize()
	if err != nil || !membership.ValidateUUID(input.ID) || !membership.ValidateUUID(spaceID) || !membership.ValidateUUID(taskID) || (input.ReplyToID != "" && !membership.ValidateUUID(input.ReplyToID)) {
		return Comment{}, ErrInvalidRequest
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, taskID, policy.ActionCreateObject); err != nil {
		return Comment{}, err
	}
	var taskSpace string
	if err := tx.QueryRow(ctx, `
		SELECT space_id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND deleted_at IS NULL`, taskID).Scan(&taskSpace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, ErrNotFound
		}
		return Comment{}, err
	}
	if taskSpace != spaceID {
		return Comment{}, ErrNotFound
	}
	var root collaboration.CommentThread
	if input.ReplyToID != "" {
		root, err = collaboration.LockCommentThread(ctx, tx, spaceID, taskID, input.ReplyToID)
		if err != nil {
			return Comment{}, err
		}
	}
	if existing, found, err := s.findCommentByClientID(ctx, tx, organizationID, taskID, input.ClientCommentID); err != nil {
		return Comment{}, err
	} else if found {
		matchesBody := existing.Body == input.Body
		if existing.originalBodyHash != nil {
			digest := sha256.Sum256([]byte(input.Body))
			matchesBody = bytes.Equal(existing.originalBodyHash, digest[:])
		}
		if !matchesBody || existing.AuthorUserID != actor.UserID || existing.ReplyToID != input.ReplyToID || !sameMentions(existing.Mentions, input.Mentions) {
			return Comment{}, ErrDuplicateMessage
		}
		return existing, nil
	}
	if root.ResolutionID != "" {
		return Comment{}, collaboration.NewRuleError(409, "THREAD_RESOLVED", "reopen the discussion to reply")
	}
	if err := s.validateCommentMentions(ctx, tx, organizationID, spaceID, input.Mentions); err != nil {
		return Comment{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO openfox.task_comments (
			organization_id, space_id, task_id, id, author_user_id, body,
			client_comment_id, reply_to_id
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3::uuid,
		          openfox_private.current_actor_id(), $4, $5, NULLIF($6, '')::uuid)`,
		spaceID, taskID, input.ID, input.Body, input.ClientCommentID, input.ReplyToID)
	if err != nil {
		return Comment{}, mapError(err)
	}
	if err := insertCommentMentions(ctx, tx, organizationID, input.ID, input.Mentions); err != nil {
		return Comment{}, err
	}
	if err := s.notifyComment(ctx, tx, spaceID, taskID, input, actor); err != nil {
		return Comment{}, err
	}
	return s.loadComment(ctx, tx, organizationID, input.ID)
}

func (s Service) ListComments(ctx context.Context, tx pgx.Tx, organizationID, spaceID, taskID string, actor Actor, limit int, after time.Time) (Page[Comment], error) {
	if limit < 1 || limit > MaxPageSize {
		return Page[Comment]{}, ErrInvalidRequest
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, taskID, policy.ActionReadObject); err != nil {
		return Page[Comment]{}, err
	}
	var taskLive bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL)`, spaceID, taskID).Scan(&taskLive); err != nil {
		return Page[Comment]{}, err
	}
	if !taskLive {
		return Page[Comment]{}, ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT organization_id::text, space_id::text, task_id::text, id::text,
		       COALESCE(author_user_id::text, ''), COALESCE(author_agent_id::text, ''), body, COALESCE(client_comment_id, ''),
		       COALESCE(reply_to_id::text, ''), created_at, edit_version, edited_at, resolved_at, COALESCE(resolved_comment_id::text,''), original_body_hash
		  FROM openfox.task_comments
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND space_id = $1::uuid AND task_id = $2::uuid
		   AND ($3::timestamptz IS NULL OR created_at > $3)
		 ORDER BY created_at, id
		 LIMIT $4`, spaceID, taskID, nullableTime(after), limit+1)
	if err != nil {
		return Page[Comment]{}, err
	}
	defer rows.Close()
	page := Page[Comment]{Items: make([]Comment, 0, limit)}
	for rows.Next() {
		comment, err := scanComment(rows)
		if err != nil {
			return Page[Comment]{}, err
		}
		page.Items = append(page.Items, comment)
	}
	if err := rows.Err(); err != nil {
		return Page[Comment]{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].CreatedAt.UnixNano()
	}
	if err := loadCommentMentions(ctx, tx, page.Items); err != nil {
		return Page[Comment]{}, err
	}
	return page, nil
}

func (s Service) Complete(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, version uint64, completeTask bool) (Conversation, error) {
	if version == 0 {
		return Conversation{}, ErrInvalidRequest
	}
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, true)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.OwnerUserID != actor.UserID {
		return Conversation{}, ErrForbidden
	}
	if conversation.Lifecycle != LifecycleOpen {
		return Conversation{}, ErrConversationClosed
	}
	var active int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED')`, conversationID).Scan(&active); err != nil {
		return Conversation{}, err
	}
	if active > 0 {
		return Conversation{}, ErrRunActive
	}
	if completeTask {
		if conversation.TaskID == "" {
			return Conversation{}, ErrInvalidRequest
		}
		if err := s.authorizeSpace(ctx, tx, organizationID, actor, conversation.SpaceID, conversation.TaskID, policy.ActionUpdateObject); err != nil {
			return Conversation{}, err
		}
		task, err := (collaboration.Service{}).GetTask(ctx, tx, organizationID, conversation.SpaceID, conversation.TaskID, collaboration.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion})
		if err != nil {
			return Conversation{}, mapError(err)
		}
		var doneID string
		if err := tx.QueryRow(ctx, `
			SELECT ws.status_id::text
			  FROM openfox.workflow_statuses ws
			  JOIN openfox.task_statuses ts ON ts.organization_id = ws.organization_id AND ts.id = ws.status_id
			 WHERE ws.organization_id = openfox_private.current_organization_id()
			   AND ws.task_type_id = $1::uuid AND ts.category = 'DONE'
			   AND ts.archived_at IS NULL ORDER BY ws.position, ws.status_id LIMIT 1`, task.TypeID).Scan(&doneID); err != nil {
			return Conversation{}, ErrInvalidRequest
		}
		if _, err := (collaboration.Service{}).UpdateTask(ctx, tx, organizationID, conversation.SpaceID, conversation.TaskID,
			collaboration.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion},
			collaboration.TaskUpdateInput{StatusID: &doneID, Version: task.Version}); err != nil {
			return Conversation{}, mapError(err)
		}
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET lifecycle = 'COMPLETED', handoff = 'NONE', version = version + 1,
		       updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND id = $1::uuid AND version = $2`, conversationID, version)
	if err != nil {
		return Conversation{}, mapError(err)
	}
	if result.RowsAffected() != 1 {
		return Conversation{}, ErrVersionConflict
	}
	return s.getVisible(ctx, tx, organizationID, conversationID, actor, false)
}

func (s Service) Reopen(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, version uint64) (Conversation, error) {
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, true)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.OwnerUserID != actor.UserID || conversation.Lifecycle != LifecycleCompleted || version == 0 {
		return Conversation{}, ErrForbidden
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET lifecycle = 'OPEN', version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $2`, conversationID, version)
	if err != nil {
		return Conversation{}, mapError(err)
	}
	if result.RowsAffected() != 1 {
		return Conversation{}, ErrVersionConflict
	}
	return s.getVisible(ctx, tx, organizationID, conversationID, actor, false)
}

func (s Service) SetHandoff(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, handoff string, version uint64) (Conversation, error) {
	handoff = strings.ToUpper(strings.TrimSpace(handoff))
	if !validHandoff(handoff) || handoff == HandoffNone || version == 0 {
		return Conversation{}, ErrInvalidRequest
	}
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, true)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.OwnerUserID != actor.UserID || conversation.Lifecycle != LifecycleOpen {
		return Conversation{}, ErrForbidden
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET handoff = $1, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $2::uuid AND version = $3`, handoff, conversationID, version)
	if err != nil {
		return Conversation{}, mapError(err)
	}
	if result.RowsAffected() != 1 {
		return Conversation{}, ErrVersionConflict
	}
	// 等你回复 → HANDOFF；等你审阅 → HANDOFF_REVIEW（收件箱归「其他」）。
	notificationKind := "HANDOFF"
	if handoff == HandoffWaitingReview {
		notificationKind = "HANDOFF_REVIEW"
	}
	_, err = tx.Exec(ctx, `
		SELECT openfox_private.enqueue_notification(
			cm.user_id, $3, cm.conversation_id, NULL::uuid, NULL::uuid, NULL::uuid,
			$2 || ':' || cm.user_id::text, 'A conversation is waiting for your reply or review.')
		  FROM openfox.conversation_members cm
		  JOIN openfox.organization_members om
		    ON om.organization_id = cm.organization_id AND om.user_id = cm.user_id
		 WHERE cm.organization_id = openfox_private.current_organization_id()
		   AND cm.conversation_id = $1::uuid AND cm.user_id <> openfox_private.current_actor_id()
		   AND om.status = 'ACTIVE'`, conversationID, handoff, notificationKind)
	if err != nil {
		return Conversation{}, err
	}
	return s.getVisible(ctx, tx, organizationID, conversationID, actor, false)
}

func (s Service) ListNotifications(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, limit int) ([]Notification, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxPageSize {
		return nil, ErrInvalidRequest
	}
	role := strings.ToUpper(string(actor.Role))
	rows, err := tx.Query(ctx, `
		SELECT n.organization_id::text, n.id::text, n.recipient_user_id::text, n.kind, COALESCE(n.direct_chat_id::text,''), COALESCE(n.direct_message_id::text,''),
		       COALESCE(n.conversation_id::text, ''), COALESCE(n.message_id::text, ''),
		       COALESCE(n.task_id::text, ''), COALESCE(n.comment_id::text, ''),
		       n.safe_summary, n.read_at, n.created_at
		  FROM openfox.notifications n
		 WHERE n.organization_id = openfox_private.current_organization_id()
		   AND n.recipient_user_id = openfox_private.current_actor_id()
		   AND n.inbox_level IS DISTINCT FROM 'off' AND `+notificationVisibleSQL+`
		 ORDER BY n.created_at DESC, n.id DESC LIMIT $2`, role, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Notification, 0, limit)
	for rows.Next() {
		var item Notification
		if err := rows.Scan(&item.OrganizationID, &item.ID, &item.RecipientID, &item.Kind, &item.DirectChatID, &item.DirectMessageID,
			&item.ConversationID, &item.MessageID, &item.TaskID, &item.CommentID,
			&item.SafeSummary, &item.ReadAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s Service) MarkNotificationRead(ctx context.Context, tx pgx.Tx, organizationID, notificationID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(notificationID) {
		return ErrNotFound
	}
	result, err := tx.Exec(ctx, `
		UPDATE openfox.notifications
		   SET read_at = COALESCE(read_at, clock_timestamp())
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND inbox_level IS DISTINCT FROM 'off' AND id = $1::uuid AND recipient_user_id = openfox_private.current_actor_id()`, notificationID)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s Service) getVisible(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, write bool) (Conversation, error) {
	return s.loadVisible(ctx, tx, organizationID, conversationID, actor, write, false)
}

// Membership changes lock the conversation before its member rows, just like
// offer acceptance. Binding a task or transferring ownership cannot race this check.
func (s Service) loadVisible(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, write, lockConversation bool) (Conversation, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Conversation{}, err
	}
	if !membership.ValidateUUID(conversationID) {
		return Conversation{}, ErrNotFound
	}
	if lockConversation {
		var locked string
		err := tx.QueryRow(ctx, `SELECT id::text FROM openfox.conversations
 WHERE organization_id=openfox_private.current_organization_id() AND id=$1 FOR UPDATE`, conversationID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return Conversation{}, ErrNotFound
		}
		if err != nil {
			return Conversation{}, err
		}
	}
	var item Conversation
	var memberLevel string
	query := `
		SELECT c.organization_id::text, c.id::text, c.space_id::text, c.agent_id::text,
		       COALESCE(c.task_id::text, ''), c.owner_user_id::text, c.visibility,
		       c.lifecycle, c.handoff, c.origin, c.version, c.created_at, c.updated_at,
		       COALESCE(cm.access_level, ''), COALESCE(c.crew_id::text, ''), c.workspace_selection
		  FROM openfox.conversations c
		  LEFT JOIN openfox.conversation_members cm
		    ON cm.organization_id = c.organization_id AND cm.conversation_id = c.id
		   AND cm.user_id = openfox_private.current_actor_id()
		   AND EXISTS (
		       SELECT 1 FROM openfox.organization_members om
		        WHERE om.organization_id = cm.organization_id AND om.user_id = cm.user_id
		          AND om.status = 'ACTIVE'
		   )
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`
	err := tx.QueryRow(ctx, query, conversationID).Scan(
		&item.OrganizationID, &item.ID, &item.SpaceID, &item.AgentID, &item.TaskID,
		&item.OwnerUserID, &item.Visibility, &item.Lifecycle, &item.Handoff, &item.Origin,
		&item.Version, &item.CreatedAt, &item.UpdatedAt, &memberLevel, &item.CrewID, &item.WorkspaceSelection)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, item.SpaceID, item.ID, policy.ActionReadObject); err != nil {
		return Conversation{}, err
	}
	owner := item.OwnerUserID == actor.UserID
	if !owner && (item.Visibility != VisibilityShared || memberLevel == "") {
		return Conversation{}, ErrNotFound
	}
	if write && !owner && memberLevel != AccessCommenter {
		return Conversation{}, ErrForbidden
	}
	if write && item.TaskID != "" {
		if err := s.authorizeSpace(ctx, tx, organizationID, actor, item.SpaceID, item.ID, policy.ActionUpdateObject); err != nil {
			return Conversation{}, err
		}
	} else if write {
		var status string
		if err := tx.QueryRow(ctx, `
			SELECT status FROM openfox.spaces
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`,
			item.SpaceID).Scan(&status); err != nil {
			return Conversation{}, err
		}
		if status == "ARCHIVED" {
			return Conversation{}, collaboration.ErrSpaceRetired
		}
	}
	return item, nil
}

func (s Service) authorizeSpace(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, objectID string, action policy.Action) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	access, err := (collaboration.Service{}).LoadSpaceAccess(ctx, tx, organizationID, spaceID, actor.UserID)
	if err != nil {
		return ErrNotFound
	}
	decision := policy.Authorize(policy.Request{
		Action:          action,
		Actor:           policy.Actor{OrganizationID: organizationID, ActorID: actor.UserID, Role: actor.Role, Membership: policy.MembershipActive},
		Space:           &policy.Space{OrganizationID: organizationID, ID: spaceID, Visibility: spaceVisibility(access.Space.Visibility)},
		SpaceMembership: spaceMembership(access.Membership, organizationID, spaceID),
		Object:          &policy.Object{OrganizationID: organizationID, SpaceID: spaceID, ID: objectID},
	})
	if !decision.Allowed() {
		if decision.Hidden() {
			return ErrNotFound
		}
		return ErrForbidden
	}
	// 停用空间只读：会话创建、评论等对象写入返回 409 SPACE_RETIRED。
	if collaboration.IsObjectWrite(action) {
		return access.RequireWritable()
	}
	return nil
}

func (s Service) validateMentions(ctx context.Context, tx pgx.Tx, organizationID string, conversation Conversation, actorID string, mentions []Mention) error {
	seen := make(map[string]bool, len(mentions))
	var crewMembers map[string]bool
	if conversation.CrewID != "" {
		ids, _, err := crewAgents(ctx, tx, conversation.CrewID, conversation.SpaceID, actorID)
		if err != nil {
			return err
		}
		crewMembers = make(map[string]bool, len(ids))
		for _, id := range ids {
			crewMembers[id] = true
		}
	}
	for _, mention := range mentions {
		if (mention.UserID == "") == (mention.AgentID == "") {
			return ErrInvalidMention
		}
		key := mention.UserID + ":" + mention.AgentID
		if seen[key] {
			return ErrInvalidMention
		}
		seen[key] = true
		if mention.UserID != "" {
			if !membership.ValidateUUID(mention.UserID) || !s.conversationMember(ctx, tx, organizationID, conversation, mention.UserID) {
				return ErrInvalidMention
			}
			continue
		}
		if crewMembers != nil {
			// 小队对话：只能 @ 对发消息的人可用的小队成员。
			if !crewMembers[mention.AgentID] {
				return ErrInvalidMention
			}
			continue
		}
		if mention.AgentID != conversation.AgentID || !membership.ValidateUUID(mention.AgentID) {
			return ErrInvalidMention
		}
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM openfox.agents
			 WHERE organization_id = openfox_private.current_organization_id()
			   AND id = $1::uuid AND status = 'ACTIVE' AND archived_at IS NULL
			   AND (space_id = $2::uuid OR (builtin_kind='ASSISTANT' AND owner_user_id=$3::uuid))
			   AND (builtin_kind IS DISTINCT FROM 'ASSISTANT' OR owner_user_id=$3::uuid))`, mention.AgentID, conversation.SpaceID, actorID).Scan(&exists); err != nil || !exists {
			return ErrInvalidMention
		}
	}
	return nil
}

func (s Service) validateCommentMentions(ctx context.Context, tx pgx.Tx, organizationID, spaceID string, mentions []Mention) error {
	seen := make(map[string]bool, len(mentions))
	for _, mention := range mentions {
		if (mention.UserID == "") == (mention.AgentID == "") {
			return ErrInvalidMention
		}
		key := mention.UserID + ":" + mention.AgentID
		if seen[key] {
			return ErrInvalidMention
		}
		seen[key] = true
		if mention.UserID != "" && (!membership.ValidateUUID(mention.UserID) || !activeMember(ctx, tx, mention.UserID)) {
			return ErrInvalidMention
		}
		if mention.AgentID != "" {
			if !membership.ValidateUUID(mention.AgentID) {
				return ErrInvalidMention
			}
			var active bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM openfox.agents
				 WHERE organization_id = openfox_private.current_organization_id()
				   AND id = $1::uuid AND space_id = $2::uuid
				   AND status = 'ACTIVE' AND archived_at IS NULL)`, mention.AgentID, spaceID).Scan(&active); err != nil || !active {
				return ErrInvalidMention
			}
		}
	}
	return nil
}

func (s Service) conversationMember(ctx context.Context, tx pgx.Tx, organizationID string, conversation Conversation, userID string) bool {
	if userID == conversation.OwnerUserID {
		return true
	}
	if conversation.Visibility != VisibilityShared {
		return false
	}
	var exists bool
	return tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM openfox.conversation_members cm
			JOIN openfox.organization_members om
			  ON om.organization_id = cm.organization_id AND om.user_id = cm.user_id
			 WHERE cm.organization_id = openfox_private.current_organization_id()
			   AND cm.conversation_id = $1::uuid AND cm.user_id = $2::uuid
			   AND om.status = 'ACTIVE')`, conversation.ID, userID).Scan(&exists) == nil && exists
}

func (s Service) enqueueAgentMention(ctx context.Context, tx pgx.Tx, organizationID string, conversation Conversation, messageID, body string, mentions []Mention) error {
	// 一条消息最多产生一个执行输入：Run 只能由对话绑定的 Agent 执行。
	// validateMentions 已拒绝其他 Agent 与重复提及；这里再按绑定 Agent 过滤并在
	// 首次插入后退出，保证 source_event_id 唯一，不依赖调用方的校验顺序。
	for _, mention := range mentions {
		if mention.AgentID == "" || mention.AgentID != conversation.AgentID {
			continue
		}
		if err := insertConversationInput(ctx, tx, conversation.ID, messageID, body, "message:"+messageID, mention.AgentID, ""); err != nil {
			return err
		}
		break
	}
	return nil
}

// insertConversationInput 写一条执行输入、来源触发和 outbox 事件。targetAgentID 非空时记录小队对话的目标成员。
func insertConversationInput(ctx context.Context, tx pgx.Tx, conversationID, messageID, body, sourceID, agentID, targetAgentID string) error {
	payload, err := json.Marshal(map[string]string{"conversation_id": conversationID, "message_id": messageID, "agent_id": agentID})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.conversation_inputs (
			organization_id, conversation_id, message_id, body, source_event_id, target_agent_id
		) VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3, $4, NULLIF($5, '')::uuid)`,
		conversationID, messageID, body, sourceID, targetAgentID); err != nil {
		return mapError(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.execution_triggers (
			organization_id, source_event_id, source_kind, conversation_id, outcome
		) VALUES (openfox_private.current_organization_id(), $1, 'message.mention', $2::uuid, 'CREATED')`, sourceID, conversationID); err != nil {
		return mapError(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.execution_outbox (organization_id, kind, aggregate_id, payload)
		VALUES (openfox_private.current_organization_id(), 'conversation.input', $1::uuid, $2::jsonb)`, messageID, string(payload)); err != nil {
		return err
	}
	return nil
}

// enqueueCrewInputs 把小队对话的一条消息分派给目标成员：每个目标一条输入（source_event_id 含 Agent，保证幂等），
// 同一会话的输入由派发按创建顺序串行执行。
func (s Service) enqueueCrewInputs(ctx context.Context, tx pgx.Tx, conversation Conversation, actorID, messageID, body string, mentions []Mention) error {
	targets, err := crewTargets(ctx, tx, conversation, actorID, mentions)
	if err != nil {
		return err
	}
	for _, agentID := range targets {
		if err := insertConversationInput(ctx, tx, conversation.ID, messageID, body, "message:"+messageID+":"+agentID, agentID, agentID); err != nil {
			return err
		}
	}
	return nil
}

func insertMentions(ctx context.Context, tx pgx.Tx, organizationID, messageID string, mentions []Mention) error {
	for position, mention := range mentions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.message_mentions (
				organization_id, message_id, position, mentioned_user_id, mentioned_agent_id
			) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid)`, messageID, position, mention.UserID, mention.AgentID); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func insertCommentMentions(ctx context.Context, tx pgx.Tx, organizationID, commentID string, mentions []Mention) error {
	for position, mention := range mentions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO openfox.comment_mentions (
				organization_id, comment_id, position, mentioned_user_id, mentioned_agent_id
			) VALUES (openfox_private.current_organization_id(), $1::uuid, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid)`, commentID, position, mention.UserID, mention.AgentID); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (s Service) findMessageByClientID(ctx context.Context, tx pgx.Tx, organizationID, conversationID, clientID string) (Message, bool, error) {
	if clientID == "" {
		return Message{}, false, nil
	}
	row := tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, conversation_id::text, seq,
		       author_kind, COALESCE(author_user_id::text, ''), COALESCE(author_agent_id::text, ''),
		       body, COALESCE(client_message_id, ''), COALESCE(reply_to_id::text, ''), created_at,
		       kind, payload, COALESCE(assistant_action, '')
		  FROM openfox.messages
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND conversation_id = $1::uuid AND client_message_id = $2`, conversationID, clientID)
	message, err := scanMessage(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, false, nil
	}
	if err != nil {
		return Message{}, false, err
	}
	if err := loadOneMessageMentions(ctx, tx, &message); err != nil {
		return Message{}, false, err
	}
	return message, true, nil
}

func (s Service) findCommentByClientID(ctx context.Context, tx pgx.Tx, organizationID, taskID, clientID string) (Comment, bool, error) {
	row := tx.QueryRow(ctx, `
		SELECT organization_id::text, space_id::text, task_id::text, id::text,
		       COALESCE(author_user_id::text, ''), COALESCE(author_agent_id::text, ''), body, COALESCE(client_comment_id, ''),
		       COALESCE(reply_to_id::text, ''), created_at, edit_version, edited_at, resolved_at, COALESCE(resolved_comment_id::text,''), original_body_hash
		  FROM openfox.task_comments
		 WHERE organization_id = openfox_private.current_organization_id()
		   AND task_id = $1::uuid AND client_comment_id = $2`, taskID, clientID)
	comment, err := scanComment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Comment{}, false, nil
	}
	if err != nil {
		return Comment{}, false, err
	}
	if err := loadOneCommentMentions(ctx, tx, &comment); err != nil {
		return Comment{}, false, err
	}
	return comment, true, nil
}

func (s Service) loadMessage(ctx context.Context, tx pgx.Tx, organizationID, id string) (Message, error) {
	message, err := scanMessage(tx.QueryRow(ctx, `
		SELECT organization_id::text, id::text, conversation_id::text, seq,
		       author_kind, COALESCE(author_user_id::text, ''), COALESCE(author_agent_id::text, ''),
		       body, COALESCE(client_message_id, ''), COALESCE(reply_to_id::text, ''), created_at,
		       kind, payload, COALESCE(assistant_action, '')
		  FROM openfox.messages
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id))
	if err != nil {
		return Message{}, err
	}
	if err := loadOneMessageMentions(ctx, tx, &message); err != nil {
		return Message{}, err
	}
	one := []Message{message}
	if err := loadMessageAttachments(ctx, tx, one); err != nil {
		return Message{}, err
	}
	return one[0], nil
}

func (s Service) loadComment(ctx context.Context, tx pgx.Tx, organizationID, id string) (Comment, error) {
	comment, err := scanComment(tx.QueryRow(ctx, `
		SELECT organization_id::text, space_id::text, task_id::text, id::text,
		       COALESCE(author_user_id::text, ''), COALESCE(author_agent_id::text, ''), body, COALESCE(client_comment_id, ''),
		       COALESCE(reply_to_id::text, ''), created_at, edit_version, edited_at, resolved_at, COALESCE(resolved_comment_id::text,''), original_body_hash
		  FROM openfox.task_comments
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, id))
	if err != nil {
		return Comment{}, err
	}
	if err := loadOneCommentMentions(ctx, tx, &comment); err != nil {
		return Comment{}, err
	}
	return comment, nil
}

func loadOneMessageMentions(ctx context.Context, tx pgx.Tx, message *Message) error {
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(mentioned_user_id::text, ''), COALESCE(mentioned_agent_id::text, '')
		  FROM openfox.message_mentions
		 WHERE organization_id = openfox_private.current_organization_id() AND message_id = $1::uuid
		 ORDER BY position`, message.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mention Mention
		if err := rows.Scan(&mention.UserID, &mention.AgentID); err != nil {
			return err
		}
		message.Mentions = append(message.Mentions, mention)
	}
	return rows.Err()
}

// loadMessageAttachments 为消息批量附带已发送的附件（一次查询）。
func loadMessageAttachments(ctx context.Context, tx pgx.Tx, messages []Message) error {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	byMessage, err := attachments.ListForMessages(ctx, tx, ids)
	if err != nil {
		return err
	}
	for index := range messages {
		messages[index].Attachments = byMessage[messages[index].ID]
		if messages[index].Attachments == nil {
			messages[index].Attachments = []attachments.Attachment{}
		}
	}
	return nil
}

// AuthorizeWrite 校验调用者可以向该会话发送内容（上传对话附件用）：可见、可写、会话未关闭、空间未归档。
func (s Service) AuthorizeWrite(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) (Conversation, error) {
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, true)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.Lifecycle != LifecycleOpen {
		return Conversation{}, ErrConversationClosed
	}
	return conversation, nil
}

func loadMessageMentions(ctx context.Context, tx pgx.Tx, messages []Message) error {
	for index := range messages {
		if err := loadOneMessageMentions(ctx, tx, &messages[index]); err != nil {
			return err
		}
	}
	return nil
}

func loadOneCommentMentions(ctx context.Context, tx pgx.Tx, comment *Comment) error {
	rows, err := tx.Query(ctx, `
		SELECT COALESCE(mentioned_user_id::text, ''), COALESCE(mentioned_agent_id::text, '')
		  FROM openfox.comment_mentions
		 WHERE organization_id = openfox_private.current_organization_id() AND comment_id = $1::uuid
		 ORDER BY position`, comment.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mention Mention
		if err := rows.Scan(&mention.UserID, &mention.AgentID); err != nil {
			return err
		}
		comment.Mentions = append(comment.Mentions, mention)
	}
	return rows.Err()
}

func loadCommentMentions(ctx context.Context, tx pgx.Tx, comments []Comment) error {
	for index := range comments {
		if err := loadOneCommentMentions(ctx, tx, &comments[index]); err != nil {
			return err
		}
	}
	return nil
}

func scanConversation(row interface{ Scan(...any) error }) (Conversation, error) {
	var item Conversation
	var version int64
	err := row.Scan(&item.OrganizationID, &item.ID, &item.SpaceID, &item.AgentID, &item.TaskID,
		&item.OwnerUserID, &item.Visibility, &item.Lifecycle, &item.Handoff, &item.Origin, &version, &item.CreatedAt, &item.UpdatedAt)
	if err == nil {
		item.Version = uint64(version)
	}
	return item, err
}

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var item Message
	var payload []byte
	err := row.Scan(&item.OrganizationID, &item.ID, &item.ConversationID, &item.Seq,
		&item.AuthorKind, &item.AuthorUserID, &item.AuthorAgentID, &item.Body,
		&item.ClientMessageID, &item.ReplyToID, &item.CreatedAt, &item.Kind, &payload, &item.AssistantAction)
	if len(payload) > 0 {
		item.Payload = json.RawMessage(payload)
	}
	return item, err
}

func scanComment(row interface{ Scan(...any) error }) (Comment, error) {
	var item Comment
	err := row.Scan(&item.OrganizationID, &item.SpaceID, &item.TaskID, &item.ID,
		&item.AuthorUserID, &item.AuthorAgentID, &item.Body, &item.ClientID, &item.ReplyToID, &item.CreatedAt, &item.EditVersion, &item.EditedAt, &item.ResolvedAt, &item.ResolvedCommentID, &item.originalBodyHash)
	// Agent 任务工具写的评论没有用户作者（见 000044）。
	item.AuthorKind = "USER"
	if item.AuthorAgentID != "" {
		item.AuthorKind = "AGENT"
	}
	return item, err
}

func sameMentions(left, right []Mention) bool {
	if len(left) != len(right) {
		return false
	}
	key := func(mention Mention) string { return mention.UserID + ":" + mention.AgentID }
	leftKeys := make([]string, len(left))
	rightKeys := make([]string, len(right))
	for index := range left {
		leftKeys[index] = key(left[index])
		rightKeys[index] = key(right[index])
	}
	sort.Strings(leftKeys)
	sort.Strings(rightKeys)
	for index := range leftKeys {
		if leftKeys[index] != rightKeys[index] {
			return false
		}
	}
	return true
}

func activeMember(ctx context.Context, tx pgx.Tx, userID string) bool {
	var active bool
	return tx.QueryRow(ctx, `
		SELECT status = 'ACTIVE' FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, userID).Scan(&active) == nil && active
}

func spaceMembership(member *collaboration.SpaceMemberAccess, organizationID, spaceID string) *policy.SpaceMembership {
	if member == nil {
		return nil
	}
	return &policy.SpaceMembership{OrganizationID: organizationID, SpaceID: spaceID, ActorID: member.UserID,
		Role: conversationSpaceRole(member.Role), Membership: conversationMembershipState(member.Status)}
}

// spaceVisibility 与 internal/api 的 spaceVisibility 一致：空间可见性存为 SHARED / PRIVATE，SHARED 即策略里的 Public。
func spaceVisibility(value string) policy.Visibility {
	if strings.EqualFold(value, "SHARED") {
		return policy.VisibilityPublic
	}
	return policy.VisibilityPrivate
}

func conversationSpaceRole(value string) policy.SpaceRole {
	switch strings.ToUpper(value) {
	case "OWNER", "EDITOR":
		return policy.SpaceRoleAdministrator
	case "MEMBER":
		return policy.SpaceRoleMember
	case "VIEWER":
		return policy.SpaceRoleVisitor
	default:
		return ""
	}
}

func conversationMembershipState(value string) policy.MembershipState {
	if strings.EqualFold(value, "ACTIVE") {
		return policy.MembershipActive
	}
	return policy.MembershipRevoked
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func mapError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrConflict
		case "23503", "23514":
			return ErrInvalidRequest
		case "42501":
			return ErrForbidden
		}
	}
	return err
}

// AuthorizeInvocation 在后台派发或设备取上下文时重新核验原始发起人：成员仍
// ACTIVE、空间可读、且为对话所有者或共享对话的 COMMENTER。它不依赖
// current_actor_id()，因此可在 organization_worker / device_request 上下文中调用。
func (s Service) AuthorizeInvocation(ctx context.Context, tx pgx.Tx, organizationID, conversationID, userID string) (Conversation, error) {
	snapshot, err := (membership.Service{}).LoadActiveActor(ctx, tx, organizationID, userID)
	if err != nil {
		return Conversation{}, ErrForbidden
	}
	if !membership.ValidateUUID(conversationID) {
		return Conversation{}, ErrNotFound
	}
	actor := Actor{UserID: snapshot.UserID, Role: snapshot.Role, AuthorizationVersion: snapshot.AuthorizationVersion}
	var item Conversation
	var memberLevel string
	err = tx.QueryRow(ctx, `
		SELECT c.organization_id::text, c.id::text, c.space_id::text, c.agent_id::text,
		       COALESCE(c.task_id::text, ''), c.owner_user_id::text, c.visibility,
		       c.lifecycle, c.handoff, c.origin, c.version, c.created_at, c.updated_at,
		       COALESCE(cm.access_level, ''), COALESCE(c.crew_id::text, ''), c.workspace_selection
		  FROM openfox.conversations c
		  LEFT JOIN openfox.conversation_members cm
		    ON cm.organization_id = c.organization_id AND cm.conversation_id = c.id
		   AND cm.user_id = $2::uuid
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`,
		conversationID, actor.UserID).Scan(
		&item.OrganizationID, &item.ID, &item.SpaceID, &item.AgentID, &item.TaskID,
		&item.OwnerUserID, &item.Visibility, &item.Lifecycle, &item.Handoff, &item.Origin,
		&item.Version, &item.CreatedAt, &item.UpdatedAt, &memberLevel, &item.CrewID, &item.WorkspaceSelection)
	if errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, ErrNotFound
	}
	if err != nil {
		return Conversation{}, err
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, item.SpaceID, item.ID, policy.ActionReadObject); err != nil {
		return Conversation{}, ErrForbidden
	}
	if item.TaskID != "" {
		if err := s.authorizeSpace(ctx, tx, organizationID, actor, item.SpaceID, item.ID, policy.ActionUpdateObject); err != nil {
			return Conversation{}, err
		}
	}
	if item.OwnerUserID != actor.UserID && (item.Visibility != VisibilityShared || memberLevel != AccessCommenter) {
		return Conversation{}, ErrForbidden
	}
	return item, nil
}

// commentRecipientReadableSQL 判断收件人（om 为其组织成员行）能否读取任务所在空间（s），
// 与 collaboration.SpaceReadableSQL 的规则一致，但按收件人自己的身份计算。
const commentRecipientReadableSQL = `(EXISTS (
	    SELECT 1 FROM openfox.space_members sm
	     WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
	       AND sm.user_id = om.user_id AND sm.status = 'ACTIVE'
	) OR (s.visibility = 'SHARED' AND om.role <> 'GUEST' AND NOT EXISTS (
	    SELECT 1 FROM openfox.space_members sm
	     WHERE sm.organization_id = s.organization_id AND sm.space_id = s.id
	       AND sm.user_id = om.user_id AND sm.status <> 'ACTIVE'
	)))`

// notifyComment 处理评论提交后的关注与通知：
//  1. 评论人自动关注任务；
//  2. 被 @ 的成员各收一条 MENTION；
//  3. 其余成员关注者（排除作者和已收到 MENTION 的人）收一条 TASK_COMMENT。
//
// 收件人必须是 ACTIVE 成员且能读任务所在空间；enqueue_notification 会跳过操作人本人。
func (s Service) notifyComment(ctx context.Context, tx pgx.Tx, spaceID, taskID string, input CommentInput, actor Actor) error {
	if !input.SkipAutoFollow {
		if _, err := tx.Exec(ctx, `
		INSERT INTO openfox.task_followers (organization_id, space_id, task_id, kind, user_id, created_by)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, 'HUMAN', $3::uuid, $3::uuid)
		ON CONFLICT DO NOTHING`, spaceID, taskID, actor.UserID); err != nil {
			return err
		}
	}
	summary := []rune(strings.TrimSpace(input.Body))
	if len(summary) > 200 {
		summary = summary[:200]
	}
	mentioned := make([]string, 0, len(input.Mentions))
	for _, mention := range input.Mentions {
		if mention.UserID != "" && mention.UserID != actor.UserID {
			mentioned = append(mentioned, mention.UserID)
		}
	}
	if len(mentioned) > 0 {
		if _, err := tx.Exec(ctx, `
			SELECT openfox_private.enqueue_notification(
				om.user_id, 'MENTION', NULL::uuid, NULL::uuid, $2::uuid, $3::uuid,
				'comment-mention:' || $3::text || ':' || om.user_id::text, $5)
			  FROM openfox.organization_members om
			  JOIN openfox.spaces s ON s.organization_id = om.organization_id AND s.id = $1::uuid
			 WHERE om.organization_id = openfox_private.current_organization_id()
			   AND om.user_id = ANY($4::uuid[]) AND om.status = 'ACTIVE'
			   AND `+commentRecipientReadableSQL, spaceID, taskID, input.ID, mentioned, string(summary)); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		SELECT openfox_private.enqueue_notification(
			om.user_id, 'TASK_COMMENT', NULL::uuid, NULL::uuid, $2::uuid, $3::uuid,
			'comment:' || $3::text || ':' || om.user_id::text, $5)
		  FROM openfox_private.task_notice_audience(openfox_private.current_organization_id(),$2::uuid,
                (SELECT author_user_id FROM openfox.task_comments WHERE organization_id=openfox_private.current_organization_id() AND task_id=$2::uuid AND id=NULLIF($1,'')::uuid)) om
         WHERE om.user_id <> $6::uuid AND NOT (om.user_id=ANY($4::uuid[]))`, input.ReplyToID, taskID, input.ID, mentioned, string(summary), actor.UserID)
	return err
}

// AddIMParticipant 自动加入未绑定任务的共享 IM 群会话。绑定任务后，
// 仅放行已有成员和所有者，其他发言人必须先接受邀请。
func (s Service) AddIMParticipant(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) error {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return err
	}
	if !membership.ValidateUUID(conversationID) {
		return ErrNotFound
	}
	var spaceID, owner, visibility, origin string
	var taskBound bool
	if err := tx.QueryRow(ctx, `
		SELECT space_id::text, owner_user_id::text, visibility, origin, task_id IS NOT NULL FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid FOR UPDATE`, conversationID).
		Scan(&spaceID, &owner, &visibility, &origin, &taskBound); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if origin != OriginIM || visibility != VisibilityShared {
		return ErrForbidden
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, conversationID, policy.ActionReadObject); err != nil {
		return err
	}
	if owner == actor.UserID {
		return nil
	}
	if taskBound {
		var member bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM openfox.conversation_members WHERE organization_id=openfox_private.current_organization_id() AND conversation_id=$1::uuid AND user_id=$2::uuid)`, conversationID, actor.UserID).Scan(&member); err != nil {
			return err
		}
		if !member {
			return ErrInviteRequired
		}
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.conversation_members (organization_id, conversation_id, user_id, access_level)
		VALUES (openfox_private.current_organization_id(), $1::uuid, $2::uuid, $3)
		ON CONFLICT (organization_id, conversation_id, user_id) DO NOTHING`, conversationID, actor.UserID, AccessCommenter)
	return mapError(err)
}

// CanReadSpace 判断成员能否读取空间里的对象（飞书机器人在入站消息处理前做的权限预检）。
func (s Service) CanReadSpace(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID string) (bool, error) {
	err := s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, spaceID, policy.ActionReadObject)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrNotFound):
		return false, nil
	}
	return false, err
}

// AuthorizeSpaceAction 按空间策略判断成员能否对空间里的对象执行 action（停用空间的对象写入返回 collaboration.ErrSpaceRetired）。
// 拒绝返回 ErrForbidden，不可见返回 ErrNotFound。供不经过 HTTP 层的服务端流程（如任务助手的空间工具）复用同一套判断。
func (s Service) AuthorizeSpaceAction(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, spaceID, objectID string, action policy.Action) error {
	return s.authorizeSpace(ctx, tx, organizationID, actor, spaceID, objectID, action)
}
