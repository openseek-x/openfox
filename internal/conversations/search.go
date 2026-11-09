package conversations

import (
	"context"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
)

// SearchChats merges both kinds before sorting and paging. Direct chat RLS
// enforces participant access; an inactive peer also hides the thread, as in Get.
func SearchChats(ctx context.Context, tx pgx.Tx, scope collaboration.SearchScope, messages bool) ([]collaboration.SearchHit, error) {
	const agentFrom = ` FROM openfox.conversations c
		JOIN openfox.spaces s ON s.organization_id=c.organization_id AND s.id=c.space_id
		JOIN openfox.agents a ON a.organization_id=c.organization_id AND a.id=c.agent_id
		LEFT JOIN openfox.tasks t ON t.organization_id=c.organization_id AND t.space_id=c.space_id AND t.id=c.task_id
		LEFT JOIN openfox.agent_crews crew ON crew.organization_id=c.organization_id AND crew.id=c.crew_id`
	const agentTitle = `COALESCE(t.title,NULLIF(c.title,''),crew.name,NULLIF(a.name,''),'未命名对话')`
	const agentWhere = ` WHERE c.organization_id=openfox_private.current_organization_id()
		AND c.archived_at IS NULL AND t.deleted_at IS NULL
		AND ($3::uuid[] IS NULL OR c.space_id=ANY($3::uuid[])) AND ` + visibleConversationSQL
	const taskNumber = `COALESCE(s.prefix || '-' || t.sequence::text,'')`
	const directFrom = ` FROM openfox.direct_chats d
		JOIN openfox.users peer ON peer.id=CASE WHEN d.user_low=openfox_private.current_actor_id() THEN d.user_high ELSE d.user_low END
		JOIN openfox.organization_members pm ON pm.organization_id=d.organization_id AND pm.user_id=peer.id AND pm.status='ACTIVE'`
	const directWhere = ` WHERE d.organization_id=openfox_private.current_organization_id() AND d.last_seq>0 AND $3::uuid[] IS NULL`
	const peerTitle = `COALESCE(NULLIF(peer.full_name,''),peer.email)`
	// For conversation rows the avatar belongs to the peer or Agent/crew. The
	// timestamp comes from the last message, so a rename never changes its age.
	agent := `SELECT c.id, c.id AS conversation_id, 'agent' AS kind, ` + agentTitle + ` AS title,
		concat_ws(' · ',NULLIF(` + taskNumber + `,''),NULLIF(a.name,''),s.name) AS body,
		c.space_id::text, ` + taskNumber + ` AS task_number,0::bigint AS seq,last.created_at AS stamp,
		'agent' AS sender_kind,COALESCE(crew.id,a.id) AS sender_id,COALESCE(crew.name,a.name) AS sender_name` + agentFrom + `
		LEFT JOIN LATERAL (
			SELECT m.created_at FROM openfox.messages m WHERE m.organization_id=c.organization_id AND m.conversation_id=c.id
			ORDER BY m.seq DESC LIMIT 1
		) last ON true` + agentWhere + ` AND (` + agentTitle + ` ILIKE $2 OR c.title ILIKE $2 OR a.name ILIKE $2 OR crew.name ILIKE $2 OR ` + taskNumber + ` ILIKE $2)`
	direct := `SELECT d.id,d.id,'direct',` + peerTitle + `,peer.email,'','',0::bigint,last.created_at,
		'user',peer.id,` + peerTitle + directFrom + `
		JOIN openfox.direct_messages last ON last.organization_id=d.organization_id AND last.chat_id=d.id AND last.seq=d.last_seq` +
		directWhere + ` AND (peer.email ILIKE $2 OR peer.full_name ILIKE $2)`
	snippet := "body"
	if messages {
		agent = `SELECT m.id,c.id AS conversation_id,'agent' AS kind,` + agentTitle + ` AS title,
			m.body,c.space_id::text,` + taskNumber + ` AS task_number,m.seq,m.created_at AS stamp,
			lower(m.author_kind) AS sender_kind,COALESCE(m.author_user_id,m.author_agent_id) AS sender_id,
			COALESCE(NULLIF(u.full_name,''),u.email,ma.name,'') AS sender_name` + agentFrom + `
			JOIN openfox.messages m ON m.organization_id=c.organization_id AND m.conversation_id=c.id
			LEFT JOIN openfox.users u ON u.id=m.author_user_id
			LEFT JOIN openfox.agents ma ON ma.organization_id=m.organization_id AND ma.id=m.author_agent_id` + agentWhere + ` AND m.body ILIKE $2`
		direct = `SELECT m.id,d.id,'direct',` + peerTitle + `,m.body,'','',m.seq,m.created_at,
			'user',u.id,COALESCE(NULLIF(u.full_name,''),u.email)` + directFrom + `
			JOIN openfox.direct_messages m ON m.organization_id=d.organization_id AND m.chat_id=d.id
			JOIN openfox.users u ON u.id=m.author_id` + directWhere + ` AND m.body ILIKE $2`
		snippet = `substring(body FROM greatest(1,strpos(lower(body),lower($6::text))-30) FOR 160)`
	}
	sql := `SELECT id::text, conversation_id::text, kind, title,` + snippet + `,space_id,task_number,seq,stamp,
		sender_kind,COALESCE(sender_id::text,''),sender_name,` + scope.CountSQL() + `
		FROM (` + agent + ` UNION ALL ` + direct + `) hits
		ORDER BY stamp DESC NULLS LAST, kind, id DESC LIMIT $4 OFFSET $5`
	args := []any{scope.Role, collaboration.SearchPattern(scope.Query), scope.SpaceIDs, scope.Limit, scope.Offset}
	if messages {
		args = append(args, scope.Query)
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []collaboration.SearchHit{}
	for rows.Next() {
		var hit collaboration.SearchHit
		if err := rows.Scan(&hit.ID, &hit.ConversationID, &hit.ConversationKind, &hit.Title, &hit.Subtitle, &hit.SpaceID, &hit.TaskDisplayID, &hit.MessageSeq, &hit.OccurredAt,
			&hit.SenderKind, &hit.SenderID, &hit.SenderName, &hit.Total); err != nil {
			return nil, err
		}
		if messages {
			hit.MessageID = hit.ID
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
