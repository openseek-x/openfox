package executiondelivery

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/conversations"
)

// MaxCursorLength 限制客户端回传的游标长度（v2 游标最长约 90 字符）。
const MaxCursorLength = 200

// StreamCursor 是统一事件流的位置：Run 事件按 (txid, stream_seq)，组织事件按 (txid, id)。
// txid 是写入事务号（xid8），只投递快照 xmin 之前的事务写入的行，因此两段游标都严格单调、不漏不重。
// OrgFromWatermark 表示组织游标未知（不带游标或旧的纯数字游标），建连时从当前水位开始。
type StreamCursor struct {
	RunTxid          uint64
	RunSeq           int64
	OrgTxid          uint64
	OrgID            int64
	OrgFromWatermark bool
}

// ResolveWatermark anchors a new connection before the server acknowledges it.
// Explicit v2 cursors already identify their position.
func (c StreamCursor) ResolveWatermark(ctx context.Context, tx pgx.Tx) (StreamCursor, error) {
	if !c.OrgFromWatermark {
		return c, nil
	}
	var xmin string
	if err := tx.QueryRow(ctx, `SELECT pg_snapshot_xmin(pg_current_snapshot())::text`).Scan(&xmin); err != nil {
		return StreamCursor{}, err
	}
	value, err := strconv.ParseUint(xmin, 10, 64)
	if err != nil {
		return StreamCursor{}, err
	}
	// (xmin, 0) includes transactions still open now or started afterwards.
	c.OrgTxid, c.OrgID, c.OrgFromWatermark = value, 0, false
	return c, nil
}

// String 输出 SSE id：v2.<run_txid>.<run_seq>.<org_txid>.<org_id>。
func (c StreamCursor) String() string {
	return "v2." + strconv.FormatUint(c.RunTxid, 10) + "." + strconv.FormatInt(c.RunSeq, 10) + "." +
		strconv.FormatUint(c.OrgTxid, 10) + "." + strconv.FormatInt(c.OrgID, 10)
}

// ParseStreamCursor 解析 after / Last-Event-ID。空串 = Run 事件从头、组织事件从当前水位；
// 旧的纯数字 n = Run 游标 (0, n)、组织事件从当前水位。
func ParseStreamCursor(raw string) (StreamCursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return StreamCursor{OrgFromWatermark: true}, nil
	}
	if len(raw) > MaxCursorLength {
		return StreamCursor{}, ErrInvalidRequest
	}
	if !strings.HasPrefix(raw, "v2.") {
		seq, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || seq < 0 {
			return StreamCursor{}, ErrInvalidRequest
		}
		return StreamCursor{RunSeq: seq, OrgFromWatermark: true}, nil
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 5 {
		return StreamCursor{}, ErrInvalidRequest
	}
	runTxid, errRunTxid := strconv.ParseUint(parts[1], 10, 64)
	runSeq, errRunSeq := strconv.ParseInt(parts[2], 10, 64)
	orgTxid, errOrgTxid := strconv.ParseUint(parts[3], 10, 64)
	orgID, errOrgID := strconv.ParseInt(parts[4], 10, 64)
	if errRunTxid != nil || errRunSeq != nil || errOrgTxid != nil || errOrgID != nil || runSeq < 0 || orgID < 0 {
		return StreamCursor{}, ErrInvalidRequest
	}
	return StreamCursor{RunTxid: runTxid, RunSeq: runSeq, OrgTxid: orgTxid, OrgID: orgID}, nil
}

// streamConversationVisibleSQL 判断当前成员能否看到对话 c（所有者，或 SHARED 对话的成员且组织成员 ACTIVE），
// 并按协作策略要求对话所在空间 s 可读（$1 为大写组织角色）。
const streamConversationVisibleSQL = `(c.owner_user_id = openfox_private.current_actor_id()
	        OR (c.visibility = 'SHARED' AND EXISTS (
	            SELECT 1 FROM openfox.conversation_members cm
	              JOIN openfox.organization_members om
	                ON om.organization_id = cm.organization_id AND om.user_id = cm.user_id
	             WHERE cm.organization_id = c.organization_id AND cm.conversation_id = c.id
	               AND cm.user_id = openfox_private.current_actor_id() AND om.status = 'ACTIVE')))
	   AND ` + collaboration.SpaceReadableSQL

// ListVisibleEvents 是统一事件流的一次轮询：在同一事务里分别取 Run 事件与组织事件，各最多 limit 条。
// 只扫描快照 xmin 之前提交的行；不可见的行不返回但游标越过它们，避免反复扫描。
// 先取 Run 事件再取组织事件：RunCursors 的组织段是轮询前的位置，OrgCursors 的 Run 段是本轮扫描后的位置，
// 客户端用任意一帧的 id 重连都不会漏掉尚未发送的另一类事件。
// 组织事件可见性：定向事件由 RLS 限定收件人；带空间的要求空间可读；带对话的要求对话可见；都没有的对组织可见。
func (s Service) ListVisibleEvents(ctx context.Context, tx pgx.Tx, organizationID string, actor conversations.Actor, cursor StreamCursor, limit int) (StreamPage, error) {
	if limit < 1 || limit > MaxPageSize {
		return StreamPage{}, ErrInvalidRequest
	}
	role := strings.ToUpper(string(actor.Role))
	cursor, err := cursor.ResolveWatermark(ctx, tx)
	if err != nil {
		return StreamPage{}, err
	}
	page := StreamPage{RunEvents: []Event{}, OrgEvents: []OrganizationEvent{}}
	rows, err := tx.Query(ctx, `
		SELECT e.organization_id::text, e.id::text, e.run_id::text, e.generation,
		       e.sequence, e.stream_seq, e.type,
		       CASE WHEN v.visible THEN e.safe_payload END, e.payload_hash, e.occurred_at, e.received_at,
		       COALESCE(e.txid, '0'::xid8)::text, v.visible
		  FROM openfox.run_events e
		  CROSS JOIN LATERAL (
		      SELECT EXISTS (
		          SELECT 1
		            FROM openfox.runs r
		            JOIN openfox.conversations c
		              ON c.organization_id = r.organization_id AND c.id = r.conversation_id
		            JOIN openfox.spaces s
		              ON s.organization_id = c.organization_id AND s.id = c.space_id
		           WHERE r.organization_id = e.organization_id AND r.id = e.run_id
		             AND `+streamConversationVisibleSQL+`
		      ) AS visible
		  ) v
		 WHERE e.organization_id = openfox_private.current_organization_id()
		   AND (COALESCE(e.txid, '0'::xid8), e.stream_seq) > ($2::text::xid8, $3)
		   AND COALESCE(e.txid, '0'::xid8) < pg_snapshot_xmin(pg_current_snapshot())
		 ORDER BY COALESCE(e.txid, '0'::xid8), e.stream_seq
		 LIMIT $4`, role, strconv.FormatUint(cursor.RunTxid, 10), cursor.RunSeq, limit)
	if err != nil {
		return StreamPage{}, err
	}
	for rows.Next() {
		var event Event
		var txid string
		var visible bool
		if err := rows.Scan(&event.OrganizationID, &event.ID, &event.RunID, &event.Generation,
			&event.Sequence, &event.StreamSequence, &event.Type, &event.Payload,
			&event.PayloadHash, &event.OccurredAt, &event.ReceivedAt, &txid, &visible); err != nil {
			rows.Close()
			return StreamPage{}, err
		}
		if cursor.RunTxid, err = strconv.ParseUint(txid, 10, 64); err != nil {
			rows.Close()
			return StreamPage{}, err
		}
		cursor.RunSeq = event.StreamSequence
		if visible {
			page.RunEvents = append(page.RunEvents, event)
			page.RunCursors = append(page.RunCursors, cursor)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return StreamPage{}, err
	}
	rows, err = tx.Query(ctx, `
		SELECT oe.id, oe.txid::text, oe.type, oe.entity, oe.entity_id::text, oe.version,
		       oe.space_id::text, oe.conversation_id::text,
		       CASE WHEN v.visible THEN oe.payload END, oe.created_at, v.visible
		  FROM openfox.organization_events oe
		  CROSS JOIN LATERAL (
		      SELECT (oe.space_id IS NULL OR EXISTS (
		                  SELECT 1 FROM openfox.spaces s
		                   WHERE s.organization_id = oe.organization_id AND s.id = oe.space_id
		                     AND `+collaboration.SpaceReadableSQL+`))
		         AND (oe.conversation_id IS NULL OR EXISTS (
		                  SELECT 1
		                    FROM openfox.conversations c
		                    JOIN openfox.spaces s
		                      ON s.organization_id = c.organization_id AND s.id = c.space_id
		                   WHERE c.organization_id = oe.organization_id AND c.id = oe.conversation_id
		                     AND `+streamConversationVisibleSQL+`)) AS visible
		  ) v
		 WHERE oe.organization_id = openfox_private.current_organization_id()
		   AND (oe.txid, oe.id) > ($2::text::xid8, $3)
		   AND oe.txid < pg_snapshot_xmin(pg_current_snapshot())
		 ORDER BY oe.txid, oe.id
		 LIMIT $4`, role, strconv.FormatUint(cursor.OrgTxid, 10), cursor.OrgID, limit)
	if err != nil {
		return StreamPage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var event OrganizationEvent
		var id int64
		var txid string
		var entityID *string
		var visible bool
		if err := rows.Scan(&id, &txid, &event.Type, &event.Entity, &entityID, &event.Version,
			&event.SpaceID, &event.ConversationID, &event.Payload, &event.CreatedAt, &visible); err != nil {
			return StreamPage{}, err
		}
		if cursor.OrgTxid, err = strconv.ParseUint(txid, 10, 64); err != nil {
			return StreamPage{}, err
		}
		cursor.OrgID = id
		if visible {
			event.ID = entityID
			if len(event.Payload) == 0 {
				event.Payload = json.RawMessage("{}")
			}
			event.CreatedAt = event.CreatedAt.UTC()
			page.OrgEvents = append(page.OrgEvents, event)
			page.OrgCursors = append(page.OrgCursors, cursor)
		}
	}
	if err := rows.Err(); err != nil {
		return StreamPage{}, err
	}
	page.Cursor = cursor
	return page, nil
}
