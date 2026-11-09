package conversations

// 对话导航：列表筛选与 (updated_at, id) 复合游标、个人置顶 / 已读、
// 会话状态派生、重命名 / 归档、绑定任务与转为任务。
// 置顶与已读写 conversation_user_state（RLS 限本人），不修改 conversations.version。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
	"openfox/internal/policy"
	"openfox/internal/runtimepolicy"
)

var (
	ErrTaskBound         = errors.New("conversations: conversation already has a task")
	ErrTaskSpaceMismatch = errors.New("conversations: task is outside the conversation space")
)

// 会话派生状态（列表与详情的 status 字段）。
const (
	StatusWorking = "WORKING"
	StatusQueued  = "QUEUED"
	StatusWaiting = "WAITING"
	StatusReview  = "REVIEW"
	StatusError   = "ERROR"
	StatusDone    = "DONE"
	StatusAborted = "ABORTED"
	StatusIdle    = "IDLE"
)

// 列表的归档筛选。
const (
	ArchivedExclude = "exclude"
	ArchivedInclude = "include"
	ArchivedOnly    = "only"
)

const (
	maxPreviewRunes = 120
	maxQueryRunes   = 200
	// convertTitleRunes 是转为任务时从首条用户消息截取的标题长度。
	convertTitleRunes = 120
)

// ListQuery 是 GET /conversations 的筛选条件。Cursor 为不透明字符串；纯数字为旧版
// updated_at 纳秒游标（只按 updated_at 严格小于翻页）。
type ListQuery struct {
	Limit      int
	Cursor     string
	AgentID    string
	TaskID     string
	Archived   string
	PinnedOnly bool
	Q          string
}

// SummaryPage 是对话列表的一页；NextCursor 为空表示没有下一页。
type SummaryPage struct {
	Items      []Summary
	NextCursor string
	TaskStats  *TaskStats
}

// TaskStats summarizes visible conversations across every page, and all their task-bound runs.
type TaskStats struct {
	ConversationCount int64 `json:"conversation_count"`
	RunCount          int64 `json:"run_count"`
	ReportedRuns      int64 `json:"reported_runs"`
	Input             int64 `json:"input"`
	Output            int64 `json:"output"`
	CacheRead         int64 `json:"cache_read"`
	CacheWrite        int64 `json:"cache_write"`
}

// Summary 是导航用的会话摘要：基础字段 + 个人状态 + 最近消息 + Agent / 任务 / Run + 派生状态。
type Summary struct {
	OfferParticipant bool `json:"offer_participant"`
	Conversation
	Title          *string         `json:"title"`
	ArchivedAt     *time.Time      `json:"archived_at"`
	Pinned         bool            `json:"pinned"`
	LastReadSeq    int64           `json:"last_read_seq"`
	Unread         bool            `json:"unread"`
	LastMessage    *LastMessage    `json:"last_message"`
	RecentMessages []RecentMessage `json:"recent_messages"`
	Agent          SummaryAgent    `json:"agent"`
	Task           *SummaryTask    `json:"task"`
	Run            *SummaryRun     `json:"run"`
	StatusMark     *string         `json:"status_mark"`
	Status         string          `json:"status"`
	// 对话级覆盖。Overrides 是覆盖本身（null = 沿用 Agent 配置）；
	// Effective 是下一次运行会采用的值；AgentDefaults 是 Agent 当前配置的值（不叠加覆盖），
	// 前端用它显示「恢复 Agent 默认」的目标值，不需要另外读取 Agent 配置（没有配置读取权限的成员也能看到）。
	// 小队对话没有覆盖，Effective 与 AgentDefaults 为 null（每条消息由不同成员执行）。
	Overrides     Overrides        `json:"overrides"`
	Effective     *EffectiveConfig `json:"effective"`
	AgentDefaults *EffectiveConfig `json:"agent_defaults"`
}

type LastMessage struct {
	Seq        int64     `json:"seq"`
	AuthorKind string    `json:"author_kind"`
	Preview    string    `json:"preview"`
	CreatedAt  time.Time `json:"created_at"`
}

// RecentMessage 是卡片使用的最近两条正文摘要；按 seq 升序，排除系统/动作消息。
type RecentMessage struct {
	LastMessage
	AuthorName string `json:"author_name"`
}

type SummaryAgent struct {
	ID          string          `json:"id"`
	SpaceID     string          `json:"space_id"`
	BuiltinKind string          `json:"builtin_kind,omitempty"`
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Avatar      json.RawMessage `json:"avatar"`
	Online      bool            `json:"online"`
}

type SummaryTask struct {
	ID        string `json:"id"`
	DisplayID string `json:"display_id"`
	Title     string `json:"title"`
}

// SummaryRun 是会话当前的活动 Run；没有活动 Run 时为最近一次创建的 Run。
type SummaryRun struct {
	ID         string     `json:"id"`
	State      string     `json:"state"`
	WaitReason string     `json:"wait_reason"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// ConversationUpdate 是 PATCH /conversations/{c}：Title 非空时设置标题，ClearTitle 清除标题，Archived 归档 / 恢复。
type ConversationUpdate struct {
	Title      *string
	ClearTitle bool
	Archived   *bool
	StatusMark *string // NONE clears the mark.
}

// ConvertInput 是「转为任务」：TaskID 为新任务 ID；Title 为空时取会话标题，再取首条用户消息的首行。
type ConvertInput struct {
	TaskID    string
	Title     string
	TypeID    string
	RequestID string
}

// navCursor 是复合游标的编码内容：updated_at 的 Unix 微秒（与 timestamptz 精度一致）与会话 ID。
type navCursor struct {
	UpdatedAt int64  `json:"u"`
	ID        string `json:"i"`
}

func encodeNavCursor(updatedAt time.Time, id string) string {
	payload, _ := json.Marshal(navCursor{UpdatedAt: updatedAt.UnixMicro(), ID: id})
	return base64.RawURLEncoding.EncodeToString(payload)
}

// parseNavCursor 解析游标：空串表示第一页；纯数字为旧版纳秒游标（返回的 ID 为空）。
func parseNavCursor(raw string) (time.Time, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, "", nil
	}
	if nanos, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if nanos <= 0 {
			return time.Time{}, "", nil
		}
		return time.Unix(0, nanos).UTC(), "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", ErrInvalidRequest
	}
	var cursor navCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.UpdatedAt <= 0 || !membership.ValidateUUID(cursor.ID) {
		return time.Time{}, "", ErrInvalidRequest
	}
	return time.UnixMicro(cursor.UpdatedAt).UTC(), cursor.ID, nil
}

func (q ListQuery) normalize() (ListQuery, error) {
	q.AgentID = strings.TrimSpace(q.AgentID)
	q.TaskID = strings.TrimSpace(q.TaskID)
	q.Q = strings.TrimSpace(q.Q)
	q.Archived = strings.ToLower(strings.TrimSpace(q.Archived))
	if q.Archived == "" {
		q.Archived = ArchivedExclude
	}
	if q.Limit < 1 || q.Limit > MaxPageSize ||
		(q.AgentID != "" && !membership.ValidateUUID(q.AgentID)) ||
		(q.TaskID != "" && !membership.ValidateUUID(q.TaskID)) ||
		len([]rune(q.Q)) > maxQueryRunes ||
		(q.Archived != ArchivedExclude && q.Archived != ArchivedInclude && q.Archived != ArchivedOnly) {
		return ListQuery{}, ErrInvalidRequest
	}
	return q, nil
}

// $1 is the actor's organization role. The spaces table must be joined as s.
const visibleConversationSQL = `(c.owner_user_id = openfox_private.current_actor_id()
		        OR (c.visibility = 'SHARED' AND EXISTS (
		            SELECT 1 FROM openfox.conversation_members cm
		              JOIN openfox.organization_members om
		                ON om.organization_id = cm.organization_id AND om.user_id = cm.user_id
		             WHERE cm.organization_id = c.organization_id AND cm.conversation_id = c.id
		               AND cm.user_id = openfox_private.current_actor_id()
		               AND om.status = 'ACTIVE')))
		   -- 空间可读按协作策略（SHARED 空间对非访客可读，被撤销的空间成员不可读），$1 为大写组织角色
		   AND ` + collaboration.SpaceReadableSQL

// ListSummaries 返回当前成员可见的会话（所有者，或共享会话的 ACTIVE 成员；空间按协作策略可读），
// 按 updated_at DESC, id DESC 排序。先按条件取一页 ID，再用 = ANY 一次补齐摘要字段。
func (s Service) ListSummaries(ctx context.Context, tx pgx.Tx, organizationID string, actor Actor, query ListQuery) (SummaryPage, error) {
	if err := s.CheckActor(ctx, tx, organizationID, actor); err != nil {
		return SummaryPage{}, err
	}
	query, err := query.normalize()
	if err != nil {
		return SummaryPage{}, err
	}
	cursorTime, cursorID, err := parseNavCursor(query.Cursor)
	if err != nil {
		return SummaryPage{}, err
	}
	var pattern any
	if query.Q != "" {
		pattern = "%" + escapeLike(query.Q) + "%"
	}
	role := strings.ToUpper(string(actor.Role))
	rows, err := tx.Query(ctx, `
		SELECT c.id::text, c.updated_at
		  FROM openfox.conversations c
		  JOIN openfox.spaces s ON s.organization_id = c.organization_id AND s.id = c.space_id
		  LEFT JOIN openfox.conversation_user_state us
		    ON us.organization_id = c.organization_id AND us.conversation_id = c.id
		   AND us.user_id = openfox_private.current_actor_id()
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND ($2::timestamptz IS NULL
		        OR ($3::uuid IS NULL AND c.updated_at < $2::timestamptz)
		        OR ($3::uuid IS NOT NULL AND (c.updated_at, c.id) < ($2::timestamptz, $3::uuid)))
		   AND ($4::uuid IS NULL OR c.agent_id = $4::uuid)
		   AND ($5::uuid IS NULL OR c.task_id = $5::uuid)
		   AND CASE $6::text WHEN 'only' THEN c.archived_at IS NOT NULL
		                     WHEN 'include' THEN true
		                     ELSE c.archived_at IS NULL END
		   AND (NOT $7::boolean OR us.pinned_at IS NOT NULL)
		   AND ($8::text IS NULL OR c.title ILIKE $8::text ESCAPE '\')
		   AND `+visibleConversationSQL+`
		 ORDER BY c.updated_at DESC, c.id DESC
		 LIMIT $9`, role, nullableTime(cursorTime), nullableString(cursorID), nullableString(query.AgentID),
		nullableString(query.TaskID), query.Archived, query.PinnedOnly, pattern, query.Limit+1)
	if err != nil {
		return SummaryPage{}, err
	}
	type pageKey struct {
		id        string
		updatedAt time.Time
	}
	keys := make([]pageKey, 0, query.Limit+1)
	for rows.Next() {
		var key pageKey
		if err := rows.Scan(&key.id, &key.updatedAt); err != nil {
			rows.Close()
			return SummaryPage{}, err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return SummaryPage{}, err
	}
	page := SummaryPage{Items: make([]Summary, 0, query.Limit)}
	if len(keys) > query.Limit {
		keys = keys[:query.Limit]
		last := keys[len(keys)-1]
		page.NextCursor = encodeNavCursor(last.updatedAt, last.id)
	}
	ids := make([]string, len(keys))
	for index, key := range keys {
		ids[index] = key.id
	}
	summaries, err := loadSummaries(ctx, tx, ids)
	if err != nil {
		return SummaryPage{}, err
	}
	for _, id := range ids {
		if item, ok := summaries[id]; ok {
			page.Items = append(page.Items, item)
		}
	}
	if query.TaskID != "" && query.Cursor == "" && query.Q == "" && query.AgentID == "" && !query.PinnedOnly && query.Archived == ArchivedExclude {
		page.TaskStats, err = readTaskStats(ctx, tx, role, query.TaskID)
		if err != nil {
			return SummaryPage{}, err
		}
	}
	return page, nil
}

// The transcript omits archived conversations. Their recorded task usage remains part of history.
// Codex reports inclusive input tokens; the other adapters report fresh input separately.
func readTaskStats(ctx context.Context, tx pgx.Tx, role, taskID string) (*TaskStats, error) {
	var stats TaskStats
	err := tx.QueryRow(ctx, `
        SELECT count(DISTINCT c.id) FILTER (WHERE c.archived_at IS NULL),
               count(r.id), count(rr.usage),
               COALESCE(sum(GREATEST((rr.usage->>'input')::bigint -
                   CASE WHEN COALESCE(r.runtime, cfg.runtime) = 'codex'
                        THEN COALESCE((rr.usage->>'cached_input')::bigint, 0) ELSE 0 END, 0)), 0),
               COALESCE(sum((rr.usage->>'output')::bigint), 0),
               COALESCE(sum((rr.usage->>'cached_input')::bigint), 0),
               COALESCE(sum((rr.usage->>'cache_write_input')::bigint), 0)
          FROM openfox.conversations c
          JOIN openfox.spaces s ON s.organization_id = c.organization_id AND s.id = c.space_id
          LEFT JOIN openfox.runs r ON r.organization_id = c.organization_id AND r.conversation_id = c.id AND r.task_id = c.task_id
          LEFT JOIN openfox.run_results rr ON rr.organization_id = r.organization_id AND rr.run_id = r.id
          LEFT JOIN openfox.agent_config_versions cfg ON r.runtime IS NULL AND cfg.organization_id = r.organization_id
               AND cfg.agent_id = r.agent_id AND cfg.config_version = r.config_version
         WHERE c.organization_id = openfox_private.current_organization_id() AND c.task_id = $2::uuid
           AND `+visibleConversationSQL, role, taskID).Scan(&stats.ConversationCount, &stats.RunCount,
		&stats.ReportedRuns, &stats.Input, &stats.Output, &stats.CacheRead, &stats.CacheWrite)
	return &stats, err
}

// GetSummary 返回单个会话的摘要（可见性规则同 Get）。
func (s Service) GetSummary(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) (Summary, error) {
	if _, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, false); err != nil {
		return Summary{}, err
	}
	return loadSummary(ctx, tx, conversationID)
}

func loadSummary(ctx context.Context, tx pgx.Tx, conversationID string) (Summary, error) {
	items, err := loadSummaries(ctx, tx, []string{conversationID})
	if err != nil {
		return Summary{}, err
	}
	item, ok := items[conversationID]
	if !ok {
		return Summary{}, ErrNotFound
	}
	return item, nil
}

// OnlineWindowSeconds 与 agents.OnlineWindowSeconds 一致（不能直接引用：agents 的测试经 dispatch 间接依赖本包），
// 由 agents 包的测试断言两者相等。
const OnlineWindowSeconds = 90

// summaryOnlineSQL 与 agents 目录的在线判定一致：配置电脑 ACTIVE、授权 ACTIVE、最近心跳在窗口内。
var summaryOnlineSQL = `COALESCE((
	SELECT d.status = 'ACTIVE' AND d.last_seen_at IS NOT NULL
	       AND d.last_seen_at >= clock_timestamp() - make_interval(secs => ` + strconv.Itoa(OnlineWindowSeconds) + `)
	  FROM openfox.physical_devices d
	  JOIN openfox.device_grants g
	    ON g.organization_id = a.organization_id AND g.id = cfg.grant_id
	   AND g.device_id = d.id AND g.status = 'ACTIVE'
	 WHERE d.id = cfg.computer_id), false)`

// loadSummaries 按 ID 批量读取摘要（调用方已完成可见性检查）。
// last_message：seq 最大的一条；run：活动 Run 优先，否则最近创建的一条；
// unread：存在 seq > last_read_seq 且作者不是本人的消息。
func loadSummaries(ctx context.Context, tx pgx.Tx, ids []string) (map[string]Summary, error) {
	result := make(map[string]Summary, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT c.organization_id::text, c.id::text, c.space_id::text, c.agent_id::text,
		       COALESCE(c.task_id::text, ''), c.owner_user_id::text, c.visibility,
		       c.lifecycle, c.handoff, c.origin, c.version, c.created_at, c.updated_at,
		       COALESCE(c.crew_id::text, ''), c.title, c.archived_at, c.status_mark,
        (c.owner_user_id=openfox_private.current_actor_id() OR (c.visibility='SHARED' AND EXISTS(SELECT 1 FROM openfox.conversation_members cm WHERE cm.organization_id=c.organization_id AND cm.conversation_id=c.id AND cm.user_id=openfox_private.current_actor_id() AND cm.access_level='COMMENTER'))),
		       us.pinned_at IS NOT NULL, COALESCE(us.last_read_seq, 0),
		       EXISTS (SELECT 1 FROM openfox.messages um
		                WHERE um.organization_id = c.organization_id AND um.conversation_id = c.id
		                  AND um.seq > COALESCE(us.last_read_seq, 0)
		                  AND um.author_user_id IS DISTINCT FROM openfox_private.current_actor_id()),
		       lm.seq, lm.author_kind, lm.preview, lm.created_at, rm.messages,
		       a.id::text, a.space_id::text, COALESCE(a.builtin_kind,''), a.name, a.title, a.avatar, `+summaryOnlineSQL+`,
		       t.id::text, COALESCE(s.prefix, '') || '-' || COALESCE(t.sequence, 0)::text, t.title,
		       lr.id::text, lr.state, lr.wait_reason, lr.started_at, lr.finished_at,
		       lr.state = 'FAILED' AND EXISTS (
		           SELECT 1 FROM openfox.messages fm
		            WHERE fm.organization_id = c.organization_id AND fm.conversation_id = c.id
		              AND fm.author_kind = 'USER' AND fm.created_at > lr.finished_at),
		       c.runtime_override, c.model_override, c.approval_mode_override, c.reasoning_effort_override, c.overrides_version,
		       COALESCE(cfg.runtime, ''), COALESCE(cfg.model, ''), COALESCE(cfg.approval_mode, ''), c.workspace_selection
		  FROM openfox.conversations c
		  JOIN openfox.spaces s ON s.organization_id = c.organization_id AND s.id = c.space_id
		  JOIN openfox.agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		  LEFT JOIN openfox.agent_config_versions cfg
		    ON cfg.organization_id = a.organization_id AND cfg.agent_id = a.id
		   AND cfg.config_version = a.current_config_version
		  LEFT JOIN openfox.conversation_user_state us
		    ON us.organization_id = c.organization_id AND us.conversation_id = c.id
		   AND us.user_id = openfox_private.current_actor_id()
		  LEFT JOIN openfox.tasks t
		    ON t.organization_id = c.organization_id AND t.space_id = c.space_id AND t.id = c.task_id AND t.deleted_at IS NULL
		  LEFT JOIN LATERAL (
		      SELECT m.seq, m.author_kind,
		             left(regexp_replace(btrim(m.body), '\s+', ' ', 'g'), `+strconv.Itoa(maxPreviewRunes)+`) AS preview,
		             m.created_at
		        FROM openfox.messages m
		       WHERE m.organization_id = c.organization_id AND m.conversation_id = c.id
		       ORDER BY m.seq DESC LIMIT 1) lm ON true
		  LEFT JOIN LATERAL (
            SELECT COALESCE(jsonb_agg(recent_row ORDER BY recent_row.seq), '[]'::jsonb) AS messages
              FROM (
                SELECT m.seq, m.author_kind,
                       left(regexp_replace(btrim(m.body), '\s+', ' ', 'g'), `+strconv.Itoa(maxPreviewRunes)+`) AS preview,
                       m.created_at, COALESCE(ma.name, mu.email, '') AS author_name
                  FROM openfox.messages m
                  LEFT JOIN openfox.agents ma ON ma.organization_id = m.organization_id AND ma.id = m.author_agent_id
                  LEFT JOIN openfox.users mu ON mu.id = m.author_user_id
                 WHERE m.organization_id = c.organization_id AND m.conversation_id = c.id
                   AND m.kind = 'TEXT' AND m.author_kind IN ('USER', 'AGENT')
                 ORDER BY m.seq DESC LIMIT 2
              ) recent_row
          ) rm ON true
          LEFT JOIN LATERAL (
		      SELECT r.id, r.state, r.wait_reason, r.started_at, r.finished_at
		        FROM openfox.runs r
		       WHERE r.organization_id = c.organization_id AND r.conversation_id = c.id
		       ORDER BY (r.state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED')) DESC,
		                r.created_at DESC, r.id DESC
		       LIMIT 1) lr ON true
		 WHERE c.organization_id = openfox_private.current_organization_id()
		   AND c.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item Summary
		var version int64
		var lastSeq *int64
		var lastAuthor, lastPreview *string
		var lastCreated *time.Time
		var recentMessages []byte
		var avatar []byte
		var taskID, taskDisplay, taskTitle *string
		var runID, runState, runWait *string
		var runStarted, runFinished *time.Time
		var userAfterFailure *bool
		var agentConfig runtimepolicy.Config
		var overridesVersion int32
		if err := rows.Scan(&item.OrganizationID, &item.ID, &item.SpaceID, &item.AgentID, &item.TaskID,
			&item.OwnerUserID, &item.Visibility, &item.Lifecycle, &item.Handoff, &item.Origin, &version, &item.CreatedAt, &item.UpdatedAt,
			&item.CrewID, &item.Title, &item.ArchivedAt, &item.StatusMark, &item.OfferParticipant, &item.Pinned, &item.LastReadSeq, &item.Unread,
			&lastSeq, &lastAuthor, &lastPreview, &lastCreated, &recentMessages,
			&item.Agent.ID, &item.Agent.SpaceID, &item.Agent.BuiltinKind, &item.Agent.Name, &item.Agent.Title, &avatar, &item.Agent.Online,
			&taskID, &taskDisplay, &taskTitle,
			&runID, &runState, &runWait, &runStarted, &runFinished, &userAfterFailure,
			&item.Overrides.Runtime, &item.Overrides.Model, &item.Overrides.ApprovalMode, &item.Overrides.ReasoningEffort, &overridesVersion,
			&agentConfig.Runtime, &agentConfig.Model, &agentConfig.ApprovalMode, &item.WorkspaceSelection); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(recentMessages, &item.RecentMessages); err != nil {
			return nil, err
		}
		item.Version = uint64(version)
		item.Overrides.Version = int64(overridesVersion)
		if item.CrewID == "" {
			item.Effective, item.AgentDefaults = effectiveConfigs(agentConfig, item.Overrides)
		}
		if len(avatar) > 0 {
			item.Agent.Avatar = json.RawMessage(avatar)
		}
		if lastSeq != nil {
			item.LastMessage = &LastMessage{Seq: *lastSeq, AuthorKind: deref(lastAuthor), Preview: deref(lastPreview), CreatedAt: *lastCreated}
		}
		if taskID != nil {
			item.Task = &SummaryTask{ID: *taskID, DisplayID: deref(taskDisplay), Title: deref(taskTitle)}
		}
		if runID != nil {
			item.Run = &SummaryRun{ID: *runID, State: deref(runState), WaitReason: deref(runWait), StartedAt: runStarted, FinishedAt: runFinished}
		}
		item.Status = DeriveStatus(item.Lifecycle, item.Handoff, item.Run, userAfterFailure != nil && *userAfterFailure)
		result[item.ID] = item
	}
	return result, rows.Err()
}

// DeriveStatus 计算会话的派生状态，按以下优先级取第一个成立的：
//  1. 活动 Run：QUEUED → QUEUED；PREPARING / RUNNING / CANCEL_REQUESTED → WORKING；
//  2. lifecycle = COMPLETED → DONE（用户显式完成，优先于最近 Run 的失败 / 取消）；
//  3. handoff = WAITING_REPLY → WAITING；WAITING_REVIEW → REVIEW；
//  4. 最近 Run FAILED 且之后没有新的用户消息 → ERROR；
//  5. 最近 Run CANCELED → ABORTED；
//  6. 其他 → IDLE。
func DeriveStatus(lifecycle, handoff string, run *SummaryRun, userMessageAfterFailure bool) string {
	if run != nil {
		switch run.State {
		case "QUEUED":
			return StatusQueued
		case "PREPARING", "RUNNING", "CANCEL_REQUESTED":
			return StatusWorking
		}
	}
	if lifecycle == LifecycleCompleted {
		return StatusDone
	}
	switch handoff {
	case HandoffWaitingReply:
		return StatusWaiting
	case HandoffWaitingReview:
		return StatusReview
	}
	if run != nil {
		switch run.State {
		case "FAILED":
			if !userMessageAfterFailure {
				return StatusError
			}
		case "CANCELED":
			return StatusAborted
		}
	}
	return StatusIdle
}

// SetPinned 置顶 / 取消置顶（个人状态，任何能读会话的成员都可以操作）。重复操作幂等。
func (s Service) SetPinned(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, pinned bool) error {
	if _, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, false); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.conversation_user_state (organization_id, user_id, conversation_id, pinned_at)
		VALUES (openfox_private.current_organization_id(), openfox_private.current_actor_id(), $1::uuid,
		        CASE WHEN $2::boolean THEN clock_timestamp() END)
		ON CONFLICT (organization_id, user_id, conversation_id) DO UPDATE
		   SET pinned_at = CASE WHEN $2::boolean
		                        THEN COALESCE(openfox.conversation_user_state.pinned_at, clock_timestamp())
		                        ELSE NULL END,
		       updated_at = clock_timestamp()`, conversationID, pinned)
	return mapError(err)
}

// MarkRead 把已读位置推进到 seq：超过会话当前最大 seq 时截到最大值；只增不减（GREATEST）。
func (s Service) MarkRead(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, seq int64) error {
	if seq < 0 {
		return ErrInvalidRequest
	}
	if _, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, false); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO openfox.conversation_user_state (organization_id, user_id, conversation_id, last_read_seq)
		SELECT c.organization_id, openfox_private.current_actor_id(), c.id, LEAST($2::bigint, c.next_message_seq - 1)
		  FROM openfox.conversations c
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid
		ON CONFLICT (organization_id, user_id, conversation_id) DO UPDATE
		   SET last_read_seq = GREATEST(openfox.conversation_user_state.last_read_seq, EXCLUDED.last_read_seq),
		       updated_at = clock_timestamp()`, conversationID, seq)
	return mapError(err)
}

// UpdateConversation 重命名 / 归档 / 设置展示标识（仅所有者），不改 version 与 updated_at，返回最新摘要。
func (s Service) UpdateConversation(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, input ConversationUpdate) (Summary, error) {
	if err := s.applySessionUpdate(ctx, tx, organizationID, conversationID, actor, input); err != nil {
		return Summary{}, err
	}
	return loadSummary(ctx, tx, conversationID)
}

// applySessionUpdate 是 PATCH /conversations/{c} 与旧别名 PATCH …/session 的共同实现。
func (s Service) applySessionUpdate(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, input ConversationUpdate) error {
	if input.Title == nil && !input.ClearTitle && input.Archived == nil && input.StatusMark == nil {
		return ErrInvalidRequest
	}
	if input.StatusMark != nil && *input.StatusMark != "DONE" && *input.StatusMark != "DISCARDED" && *input.StatusMark != "NONE" {
		return ErrInvalidRequest
	}
	var title any
	if input.Title != nil {
		value := boundDraft(*input.Title, maxSessionTitle+1, false)
		if len([]rune(value)) > maxSessionTitle {
			return ErrInvalidRequest
		}
		if value != "" {
			title = value
		}
	}
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, true)
	if err != nil {
		return err
	}
	if conversation.OwnerUserID != actor.UserID {
		return ErrForbidden
	}
	// 标识和生命周期分别更新。条件写入确保并发关闭先提交时不会改动已关闭会话。
	updated, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET title = CASE WHEN $2 THEN $3::text ELSE title END,
		       archived_at = CASE WHEN $4::boolean IS NULL THEN archived_at
		                          WHEN $4 THEN COALESCE(archived_at, clock_timestamp())
		                          ELSE NULL END,
		       status_mark = CASE WHEN $5::text IS NULL THEN status_mark ELSE NULLIF($5, 'NONE') END
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND ($5::text IS NULL OR lifecycle = 'OPEN')`,
		conversationID, input.Title != nil || input.ClearTitle, title, input.Archived, input.StatusMark)
	if err != nil {
		return mapError(err)
	}
	if updated.RowsAffected() == 0 && input.StatusMark != nil {
		return ErrConversationClosed
	}
	return nil
}

// BindTask 把会话绑定到已有任务：仅所有者；任务须同空间、未删除、空间可读；会话当前没有任务且没有活动 Run。
// 绑定后后续 Run 带上 task_id（派发按任务分类做门禁）。version 加一。
func (s Service) BindTask(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, taskID string) (Summary, error) {
	taskID = strings.TrimSpace(taskID)
	if !membership.ValidateUUID(taskID) {
		return Summary{}, ErrInvalidRequest
	}
	conversation, err := s.lockForTaskBinding(ctx, tx, organizationID, conversationID, actor)
	if err != nil {
		return Summary{}, err
	}
	var taskSpace string
	if err := tx.QueryRow(ctx, `
		SELECT space_id::text FROM openfox.tasks
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND deleted_at IS NULL`, taskID).Scan(&taskSpace); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Summary{}, ErrNotFound
		}
		return Summary{}, err
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, taskSpace, taskID, policy.ActionReadObject); err != nil {
		return Summary{}, err
	}
	if taskSpace != conversation.SpaceID {
		return Summary{}, ErrTaskSpaceMismatch
	}
	if err := attachTask(ctx, tx, conversationID, taskID); err != nil {
		return Summary{}, err
	}
	return loadSummary(ctx, tx, conversationID)
}

// ConvertToTask 用会话标题（或首条用户消息的首行）在会话空间创建任务并绑定。
// 前置条件同 BindTask，另需空间的创建权限。幂等由调用方用 Idempotency-Key 处理。
func (s Service) ConvertToTask(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor, input ConvertInput) (Summary, collaboration.Task, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.TypeID = strings.TrimSpace(input.TypeID)
	if !membership.ValidateUUID(input.TaskID) || (input.TypeID != "" && !membership.ValidateUUID(input.TypeID)) {
		return Summary{}, collaboration.Task{}, ErrInvalidRequest
	}
	conversation, err := s.lockForTaskBinding(ctx, tx, organizationID, conversationID, actor)
	if err != nil {
		return Summary{}, collaboration.Task{}, err
	}
	title := input.Title
	if title == "" {
		if title, err = defaultTaskTitle(ctx, tx, conversationID); err != nil {
			return Summary{}, collaboration.Task{}, err
		}
	}
	if title == "" {
		return Summary{}, collaboration.Task{}, ErrInvalidRequest
	}
	if err := s.authorizeSpace(ctx, tx, organizationID, actor, conversation.SpaceID, input.TaskID, policy.ActionCreateObject); err != nil {
		return Summary{}, collaboration.Task{}, err
	}
	task, err := (collaboration.Service{}).CreateTask(ctx, tx, organizationID, conversation.SpaceID,
		collaboration.Actor{UserID: actor.UserID, Role: actor.Role, AuthorizationVersion: actor.AuthorizationVersion},
		collaboration.TaskInput{ID: input.TaskID, TypeID: input.TypeID, Title: title, RequestID: input.RequestID})
	if err != nil {
		return Summary{}, collaboration.Task{}, err
	}
	if err := attachTask(ctx, tx, conversationID, task.ID); err != nil {
		return Summary{}, collaboration.Task{}, err
	}
	summary, err := loadSummary(ctx, tx, conversationID)
	return summary, task, err
}

// lockForTaskBinding 检查绑定的公共前置条件并锁住会话行：派发物化 Run 时同样先锁会话行，
// 因此锁内检查「无活动 Run」后不会再有 task_id 为空的新 Run 插进来。
func (s Service) lockForTaskBinding(ctx context.Context, tx pgx.Tx, organizationID, conversationID string, actor Actor) (Conversation, error) {
	conversation, err := s.getVisible(ctx, tx, organizationID, conversationID, actor, true)
	if err != nil {
		return Conversation{}, err
	}
	if conversation.OwnerUserID != actor.UserID {
		return Conversation{}, ErrForbidden
	}
	var taskID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(task_id::text, '') FROM openfox.conversations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		 FOR UPDATE`, conversationID).Scan(&taskID); err != nil {
		return Conversation{}, mapError(err)
	}
	if taskID != "" {
		return Conversation{}, ErrTaskBound
	}
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM openfox.runs
		 WHERE organization_id = openfox_private.current_organization_id() AND conversation_id = $1::uuid
		   AND state IN ('QUEUED', 'PREPARING', 'RUNNING', 'CANCEL_REQUESTED'))`, conversationID).Scan(&active); err != nil {
		return Conversation{}, err
	}
	if active {
		return Conversation{}, ErrRunActive
	}
	return conversation, nil
}

func attachTask(ctx context.Context, tx pgx.Tx, conversationID, taskID string) error {
	result, err := tx.Exec(ctx, `
		UPDATE openfox.conversations
		   SET task_id = $2::uuid, version = version + 1, updated_at = clock_timestamp()
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND task_id IS NULL`, conversationID, taskID)
	if err != nil {
		return mapError(err)
	}
	if result.RowsAffected() != 1 {
		return ErrTaskBound
	}
	return nil
}

// defaultTaskTitle 取会话标题；没有标题时取首条用户消息的首个非空行（截到 convertTitleRunes 个字符）。
func defaultTaskTitle(ctx context.Context, tx pgx.Tx, conversationID string) (string, error) {
	var title, body *string
	if err := tx.QueryRow(ctx, `
		SELECT c.title, (SELECT m.body FROM openfox.messages m
		                  WHERE m.organization_id = c.organization_id AND m.conversation_id = c.id
		                    AND m.author_kind = 'USER'
		                  ORDER BY m.seq LIMIT 1)
		  FROM openfox.conversations c
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid`,
		conversationID).Scan(&title, &body); err != nil {
		return "", mapError(err)
	}
	if title != nil && strings.TrimSpace(*title) != "" {
		return strings.TrimSpace(*title), nil
	}
	if body == nil {
		return "", nil
	}
	for _, line := range strings.Split(*body, "\n") {
		if line = boundDraft(line, convertTitleRunes, false); line != "" {
			return line, nil
		}
	}
	return "", nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
