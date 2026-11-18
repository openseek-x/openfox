package agents

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/tenancy"
)

// Agent 详情页的统计与运行历史。

type Stats struct {
	TaskCount    int64      `json:"task_count"`
	RunCount     int64      `json:"run_count"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	LastRunAt    *time.Time `json:"last_run_at"`
}

type RunSummary struct {
	ID                string  `json:"id"`
	SpaceID           string  `json:"space_id"`
	ConversationID    string  `json:"conversation_id"`
	ConversationTitle *string `json:"conversation_title"`
	TaskID            *string `json:"task_id"`
	TaskDisplayID     *string `json:"task_display_id"`
	AgentID           string  `json:"agent_id"`
	ActorID           string  `json:"actor_id"`
	State             string  `json:"state"`
	// WaitReason 是排队原因（与 RunResponse 同义），非排队为空串。
	WaitReason    string     `json:"wait_reason"`
	Reason        string     `json:"reason"`
	ConfigVersion int64      `json:"config_version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	FinishedAt    *time.Time `json:"finished_at"`
	StartedAt     *time.Time `json:"started_at"`
	// DurationMS 为 started_at → finished_at 的毫秒数，任一为空时为 null。
	DurationMS *int64 `json:"duration_ms"`
	// Usage 为 run_results.usage 原样输出（{input, cached_input, cache_write_input, output, reasoning_output}），没有结果或未上报时为 null。
	Usage json.RawMessage `json:"usage"`
}

type RunPage struct {
	Runs       []RunSummary `json:"runs"`
	NextCursor *string      `json:"next_cursor"`
}

const (
	DefaultRunPageSize = 20
	MaxRunPageSize     = 100
)

// Stats：任务数 = 该 Agent 为处理人（task_assignees）或执行者（executor_agent_id）的未删除任务；
// token 用量汇总该 Agent 全部 Run 结果的 run_results.usage（input / output；未上报用量的结果按 0 计）。
func (s PostgresStore) Stats(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string) (Stats, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return Stats{}, err
	}
	var result Stats
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := requireReadableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM (
			            SELECT x.task_id FROM openfox.task_assignees x
			             WHERE x.organization_id = openfox_private.current_organization_id() AND x.agent_id = $1::uuid
			            UNION
			            SELECT t.id FROM openfox.tasks t
			             WHERE t.organization_id = openfox_private.current_organization_id() AND t.executor_agent_id = $1::uuid
			        ) refs
			          JOIN openfox.tasks t ON t.organization_id = openfox_private.current_organization_id()
			                              AND t.id = refs.task_id AND t.deleted_at IS NULL),
			       count(r.id), max(r.created_at),
			       COALESCE(sum((rr.usage->>'input')::bigint), 0)::bigint,
			       COALESCE(sum((rr.usage->>'output')::bigint), 0)::bigint
			  FROM openfox.runs r
			  LEFT JOIN openfox.run_results rr ON rr.organization_id = r.organization_id AND rr.run_id = r.id
			 WHERE r.organization_id = openfox_private.current_organization_id() AND r.agent_id = $1::uuid`,
			agentID).Scan(&result.TaskCount, &result.RunCount, &result.LastRunAt, &result.InputTokens, &result.OutputTokens)
	})
	return result, err
}

// conversationVisibleSQL：会话归当前成员所有，或是 SHARED 会话且当前成员是其 ACTIVE 组织成员的会话成员。
const conversationVisibleSQL = `(cv.owner_user_id = openfox_private.current_actor_id()
	OR (cv.visibility = 'SHARED' AND EXISTS (
	    SELECT 1 FROM openfox.conversation_members cm
	      JOIN openfox.organization_members om
	        ON om.organization_id = cm.organization_id AND om.user_id = cm.user_id AND om.status = 'ACTIVE'
	     WHERE cm.organization_id = cv.organization_id AND cm.conversation_id = cv.id
	       AND cm.user_id = openfox_private.current_actor_id())))`

// Runs 按 created_at desc, id desc 分页；只返回当前成员能看到的会话里的运行，且会话所在空间可读。
func (s PostgresStore) Runs(ctx context.Context, selection tenancy.OrganizationSelection, actor Actor, spaceID, agentID string, limit int, cursor string) (RunPage, error) {
	spaceID, agentID, err := s.agentRef(selection, actor, spaceID, agentID)
	if err != nil {
		return RunPage{}, err
	}
	if limit == 0 {
		limit = DefaultRunPageSize
	}
	if limit < 1 || limit > MaxRunPageSize {
		return RunPage{}, ErrInvalidRequest
	}
	var after *time.Time
	afterID := ""
	if cursor != "" {
		at, id, ok := DecodeRunCursor(cursor)
		if !ok {
			return RunPage{}, ErrInvalidRequest
		}
		after, afterID = &at, id
	}
	page := RunPage{Runs: make([]RunSummary, 0, limit)}
	role := strings.ToUpper(string(actor.Role))
	err = s.run(ctx, selection, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := requireReadableAgent(ctx, tx, selection.OrganizationID, spaceID, agentID, actor); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT r.id::text, r.space_id::text, r.conversation_id::text, cv.title, r.task_id::text,
			       ts.prefix, t.sequence, r.agent_id::text, r.actor_id::text, r.state, r.wait_reason, r.reason,
			       r.config_version, r.created_at, r.updated_at, r.finished_at, r.started_at,
			       (extract(epoch FROM r.finished_at - r.started_at) * 1000)::bigint, rr.usage
			  FROM openfox.runs r
			  LEFT JOIN openfox.run_results rr ON rr.organization_id = r.organization_id AND rr.run_id = r.id
			  JOIN openfox.conversations cv ON cv.organization_id = r.organization_id AND cv.id = r.conversation_id
			  JOIN openfox.spaces s ON s.organization_id = r.organization_id AND s.id = r.space_id
			  LEFT JOIN openfox.tasks t ON t.organization_id = r.organization_id AND t.id = r.task_id AND t.deleted_at IS NULL
			  LEFT JOIN openfox.spaces ts ON ts.organization_id = t.organization_id AND ts.id = t.space_id
			 WHERE r.organization_id = openfox_private.current_organization_id() AND r.agent_id = $2::uuid
			   AND `+conversationVisibleSQL+`
			   AND `+collaboration.SpaceReadableSQL+`
			   AND ($3::timestamptz IS NULL OR (r.created_at, r.id) < ($3::timestamptz, $4::uuid))
			 ORDER BY r.created_at DESC, r.id DESC
			 LIMIT $5`, role, agentID, after, nullableUUID(afterID), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RunSummary
			var prefix *string
			var sequence *int64
			var usage []byte
			if err := rows.Scan(&item.ID, &item.SpaceID, &item.ConversationID, &item.ConversationTitle, &item.TaskID,
				&prefix, &sequence, &item.AgentID, &item.ActorID, &item.State, &item.WaitReason, &item.Reason, &item.ConfigVersion,
				&item.CreatedAt, &item.UpdatedAt, &item.FinishedAt, &item.StartedAt, &item.DurationMS, &usage); err != nil {
				return err
			}
			if len(usage) > 0 {
				item.Usage = json.RawMessage(usage)
			}
			if prefix != nil && sequence != nil {
				display := *prefix + "-" + strconv.FormatInt(*sequence, 10)
				item.TaskDisplayID = &display
			}
			page.Runs = append(page.Runs, item)
		}
		return rows.Err()
	})
	if err != nil {
		return RunPage{}, err
	}
	if len(page.Runs) > limit {
		last := page.Runs[limit-1]
		next := EncodeRunCursor(last.CreatedAt, last.ID)
		page.Runs, page.NextCursor = page.Runs[:limit], &next
	}
	return page, nil
}

// EncodeRunCursor / DecodeRunCursor：游标为 base64url("RFC3339Nano|uuid")。
func EncodeRunCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(at.UTC().Format(time.RFC3339Nano) + "|" + id))
}

func DecodeRunCursor(value string) (time.Time, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return time.Time{}, "", false
	}
	at, id, found := strings.Cut(string(raw), "|")
	if !found || !membership.ValidateUUID(id) {
		return time.Time{}, "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return time.Time{}, "", false
	}
	return parsed, id, true
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
