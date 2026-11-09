package automations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 后台处理分两段：
//  1. PendingWork 在组织 worker 事务里只读地列出到期的定时触发器与待处理事件，得到需要启动的运行；
//  2. ApplyPending 对每一项在「创建者身份」的事务里启动运行（会话与消息以创建者的身份写入），并推进定时触发器。
//
// 每一项都带 dedupe_key，因此中途失败后重试不会重复建运行。

const (
	dueScheduleBatch = 100
	eventBatch       = 200
)

// PendingRun 是一次待启动的运行。CreatorID / CreatorVersion 用于打开创建者身份的事务。
type PendingRun struct {
	Kind           string
	AutomationID   string
	CreatorID      string
	CreatorVersion uint64
	Source         Source
	// TriggerID 与 NextRunAt 只用于定时触发器：启动后推进到的下一次时刻。
	TriggerID string
	NextRunAt time.Time
	// EventID 只用于事件触发：所有运行完成后标记事件已处理。
	EventID string
}

// PendingWork 只读取数据，返回需要启动的运行，以及本轮读到的事件 ID（事件的已处理标记由调用方在运行完成后写入）。
func (s Service) PendingWork(ctx context.Context, tx pgx.Tx) ([]PendingRun, []string, error) {
	pending, err := s.pendingSchedules(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	events, eventRuns, err := s.pendingEvents(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	return append(pending, eventRuns...), events, nil
}

// pendingSchedules 列出到期的定时触发器。错过的多个周期只产生一次运行，下一次时刻是 now 之后的第一个匹配时刻。
func (s Service) pendingSchedules(ctx context.Context, tx pgx.Tx) ([]PendingRun, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.automation_id::text, t.next_run_at::text, t.config::text, a.created_by::text
		  FROM openfox.automation_triggers t
		  JOIN openfox.automations a ON a.organization_id = t.organization_id AND a.id = t.automation_id
		 WHERE t.organization_id = openfox_private.current_organization_id()
		   AND t.enabled AND a.enabled AND t.kind = 'schedule' AND t.next_run_at <= $1::timestamptz
		 ORDER BY t.next_run_at, t.id
		 LIMIT $2`, s.now(), dueScheduleBatch)
	if err != nil {
		return nil, err
	}
	type due struct {
		triggerID, automationID, plannedAt, config, creatorID string
	}
	var dues []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.triggerID, &d.automationID, &d.plannedAt, &d.config, &d.creatorID); err != nil {
			rows.Close()
			return nil, err
		}
		dues = append(dues, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	now := s.now()
	var out []PendingRun
	for _, d := range dues {
		var config TriggerConfig
		if err := json.Unmarshal([]byte(d.config), &config); err != nil || config.Schedule == nil {
			return nil, invalidf("定时触发器配置无效")
		}
		next, err := NextRunAfter(*config.Schedule, now)
		if err != nil {
			return nil, err
		}
		version, err := creatorVersion(ctx, tx, d.creatorID)
		if err != nil {
			return nil, err
		}
		out = append(out, PendingRun{
			Kind: "schedule", AutomationID: d.automationID, CreatorID: d.creatorID, CreatorVersion: version,
			TriggerID: d.triggerID, NextRunAt: next,
			Source: Source{
				TriggerID: d.triggerID, Kind: "schedule",
				Title:     "定时运行（计划 " + d.plannedAt + "）",
				Payload:   map[string]any{"planned_at": d.plannedAt},
				Context:   "- 触发原因：定时（计划时间 " + d.plannedAt + "）",
				DedupeKey: "schedule:" + d.triggerID + ":" + d.plannedAt,
			},
		})
	}
	return out, nil
}

// ApplyPending 在创建者身份的事务里启动一次运行。停用与找不到自动化不算失败，直接跳过。
func (s Service) ApplyPending(ctx context.Context, tx pgx.Tx, organizationID string, p PendingRun) error {
	if _, err := s.StartRun(ctx, tx, organizationID, p.AutomationID, p.Source); err != nil && !errors.Is(err, ErrDisabled) && !errors.Is(err, ErrNotFound) {
		return err
	}
	if p.Kind != "schedule" {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.automation_triggers SET next_run_at = $2
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, p.TriggerID, p.NextRunAt)
	return err
}

// MarkEventsProcessed 在组织事务里标记事件已处理。
func MarkEventsProcessed(ctx context.Context, tx pgx.Tx, eventIDs []string, at time.Time) error {
	if len(eventIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE openfox.automation_events SET processed_at = $2
		 WHERE organization_id = openfox_private.current_organization_id() AND id = ANY($1::uuid[]) AND processed_at IS NULL`,
		eventIDs, at)
	return err
}

// eventRow 是待处理事件。
type eventRow struct {
	ID        string
	Kind      string
	SpaceID   string
	TaskID    string
	StatusID  string
	CommentID string
}

// pendingEvents 读取至多 200 条待处理事件并匹配触发器：task_status 需要状态命中且筛选条件全部满足，
// comment 需要评论正文包含关键词（不区分大小写）。每条事件对每个匹配的触发器产生一项运行。
func (s Service) pendingEvents(ctx context.Context, tx pgx.Tx) ([]string, []PendingRun, error) {
	rows, err := tx.Query(ctx, `
		SELECT e.id::text, e.kind, e.space_id::text, e.task_id::text, COALESCE(e.status_id::text, ''), COALESCE(e.comment_id::text, '')
		  FROM openfox.automation_events e
		 WHERE e.organization_id = openfox_private.current_organization_id() AND e.processed_at IS NULL
		 ORDER BY e.created_at, e.id
		 LIMIT $1`, eventBatch)
	if err != nil {
		return nil, nil, err
	}
	var events []eventRow
	for rows.Next() {
		var e eventRow
		if err := rows.Scan(&e.ID, &e.Kind, &e.SpaceID, &e.TaskID, &e.StatusID, &e.CommentID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	ids := make([]string, 0, len(events))
	var out []PendingRun
	for _, e := range events {
		ids = append(ids, e.ID)
		var runs []PendingRun
		var err error
		switch e.Kind {
		case "task_status":
			runs, err = s.matchTaskStatus(ctx, tx, e)
		case "comment":
			runs, err = s.matchComment(ctx, tx, e)
		}
		if err != nil {
			return nil, nil, err
		}
		out = append(out, runs...)
	}
	return ids, out, nil
}

// taskBrief 是事件处理需要的任务信息。
type taskBrief struct {
	DisplayID       string
	Title           string
	ProjectID       string
	TypeID          string
	AssigneeID      string
	ExecutorAgentID string
	Priority        string
	LabelIDs        []string
}

// loadTaskBrief 读取未删除任务的筛选属性；任务已删除时返回 false。
func loadTaskBrief(ctx context.Context, tx pgx.Tx, taskID string) (taskBrief, bool, error) {
	var b taskBrief
	err := tx.QueryRow(ctx, `
		SELECT s.prefix || '-' || t.sequence::text, t.title, COALESCE(t.project_id::text, ''), t.type_id::text,
		       COALESCE(t.assignee_id::text, ''), COALESCE(t.executor_agent_id::text, ''), t.priority,
		       ARRAY(SELECT tl.label_id::text FROM openfox.task_labels tl
		              WHERE tl.organization_id = t.organization_id AND tl.task_id = t.id)
		  FROM openfox.tasks t
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.id = $1::uuid AND t.deleted_at IS NULL`,
		taskID).Scan(&b.DisplayID, &b.Title, &b.ProjectID, &b.TypeID, &b.AssigneeID, &b.ExecutorAgentID, &b.Priority, &b.LabelIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return taskBrief{}, false, nil
	}
	if err != nil {
		return taskBrief{}, false, err
	}
	return b, true, nil
}

// matchTaskStatus 为该空间启用的任务状态触发器生成运行项。
func (s Service) matchTaskStatus(ctx context.Context, tx pgx.Tx, e eventRow) ([]PendingRun, error) {
	task, ok, err := loadTaskBrief(ctx, tx, e.TaskID)
	if err != nil || !ok {
		return nil, err
	}
	var statusName string
	if err := tx.QueryRow(ctx, `
		SELECT name FROM openfox.task_statuses
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, e.StatusID).Scan(&statusName); err != nil {
		return nil, err
	}
	triggers, err := enabledTriggers(ctx, tx, e.SpaceID, "task_status")
	if err != nil {
		return nil, err
	}
	var out []PendingRun
	for _, t := range triggers {
		var config TriggerConfig
		if err := json.Unmarshal([]byte(t.config), &config); err != nil {
			return nil, err
		}
		if !containsString(config.StatusIDs, e.StatusID) || !MatchTaskFilters(config.Filters, task.snapshot()) {
			continue
		}
		run, err := pendingFor(ctx, tx, t, e.ID, Source{
			TriggerID: t.id, Kind: "task_status",
			Title:     "任务 " + task.DisplayID + " 变为 " + statusName,
			Payload:   map[string]any{"event_id": e.ID, "task_id": e.TaskID, "status_id": e.StatusID},
			Context:   "- 触发原因：任务状态变更为「" + statusName + "」\n- 任务：" + task.DisplayID + " " + task.Title,
			DedupeKey: "event:" + e.ID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// matchComment 为该空间启用的评论触发器生成运行项：评论正文包含关键词（不区分大小写）即命中。
func (s Service) matchComment(ctx context.Context, tx pgx.Tx, e eventRow) ([]PendingRun, error) {
	var body, displayID, title string
	err := tx.QueryRow(ctx, `
		SELECT c.body, s.prefix || '-' || t.sequence::text, t.title
		  FROM openfox.task_comments c
		  JOIN openfox.tasks t ON t.organization_id = c.organization_id AND t.id = c.task_id
		  JOIN openfox.spaces s ON s.organization_id = t.organization_id AND s.id = t.space_id
		 WHERE c.organization_id = openfox_private.current_organization_id() AND c.id = $1::uuid AND t.deleted_at IS NULL`,
		e.CommentID).Scan(&body, &displayID, &title)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	triggers, err := enabledTriggers(ctx, tx, e.SpaceID, "comment")
	if err != nil {
		return nil, err
	}
	lowerBody := strings.ToLower(body)
	var out []PendingRun
	for _, t := range triggers {
		var config TriggerConfig
		if err := json.Unmarshal([]byte(t.config), &config); err != nil {
			return nil, err
		}
		if config.Contains == "" || !strings.Contains(lowerBody, strings.ToLower(config.Contains)) {
			continue
		}
		run, err := pendingFor(ctx, tx, t, e.ID, Source{
			TriggerID: t.id, Kind: "comment",
			Title:     "任务 " + displayID + " 收到包含「" + config.Contains + "」的评论",
			Payload:   map[string]any{"event_id": e.ID, "task_id": e.TaskID, "comment_id": e.CommentID},
			Context:   "- 触发原因：评论包含「" + config.Contains + "」\n- 任务：" + displayID + " " + title + "\n- 评论内容：\n" + truncateRunes(body, 2000),
			DedupeKey: "event:" + e.ID,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

// pendingFor 把事件的一次匹配包装成运行项，并带上创建者的身份信息。
func pendingFor(ctx context.Context, tx pgx.Tx, t enabledTriggerRow, eventID string, source Source) (PendingRun, error) {
	var creatorID string
	if err := tx.QueryRow(ctx, `
		SELECT created_by::text FROM openfox.automations
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, t.automationID).Scan(&creatorID); err != nil {
		return PendingRun{}, err
	}
	version, err := creatorVersion(ctx, tx, creatorID)
	if err != nil {
		return PendingRun{}, err
	}
	return PendingRun{
		Kind: source.Kind, AutomationID: t.automationID, CreatorID: creatorID, CreatorVersion: version,
		Source: source, EventID: eventID,
	}, nil
}

// creatorVersion 读取创建者当前的授权版本，用于打开创建者身份的事务。创建者已不是成员时返回 1，
// 随后启动运行会因非活跃成员记为 failed。
func creatorVersion(ctx context.Context, tx pgx.Tx, creatorID string) (uint64, error) {
	var version uint64
	err := tx.QueryRow(ctx, `
		SELECT authorization_version FROM openfox.organization_members
		 WHERE organization_id = openfox_private.current_organization_id() AND user_id = $1::uuid`, creatorID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 1, nil
	}
	return version, err
}

// enabledTriggerRow 是某空间某类型的启用触发器（自动化本身也必须启用）。
type enabledTriggerRow struct {
	id           string
	automationID string
	config       string
}

func enabledTriggers(ctx context.Context, tx pgx.Tx, spaceID, kind string) ([]enabledTriggerRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.automation_id::text, t.config::text
		  FROM openfox.automation_triggers t
		  JOIN openfox.automations a ON a.organization_id = t.organization_id AND a.id = t.automation_id
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.space_id = $1::uuid
		   AND t.kind = $2 AND t.enabled AND a.enabled
		 ORDER BY t.id`, spaceID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []enabledTriggerRow
	for rows.Next() {
		var t enabledTriggerRow
		if err := rows.Scan(&t.id, &t.automationID, &t.config); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (b taskBrief) snapshot() TaskSnapshot {
	return TaskSnapshot{
		ProjectID: b.ProjectID, TypeID: b.TypeID, AssigneeID: b.AssigneeID,
		ExecutorAgentID: b.ExecutorAgentID, Priority: b.Priority, LabelIDs: b.LabelIDs,
	}
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
