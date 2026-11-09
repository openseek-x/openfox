package automations

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// 数据读取：自动化、触发器与运行记录的共同查询。所有语句都在租户事务里执行，依赖 RLS 限定组织。

const automationSelect = `
SELECT a.id::text, a.space_id::text, a.title, a.prompt, a.assignee_kind, a.assignee_id::text,
       COALESCE(ag.name, cr.name, ''), a.notify_user_ids::text[], a.enabled, a.position,
       a.created_by::text, a.created_at, a.updated_at, a.version
  FROM openfox.automations a
  LEFT JOIN openfox.agents ag ON ag.organization_id = a.organization_id AND ag.id = a.assignee_id AND a.assignee_kind = 'AGENT'
  LEFT JOIN openfox.agent_crews cr ON cr.organization_id = a.organization_id AND cr.id = a.assignee_id AND a.assignee_kind = 'CREW'`

const runSelect = `
SELECT ar.id::text, ar.automation_id::text, COALESCE(ar.trigger_id::text, ''), ar.trigger_kind, ar.status,
       ar.reason, ar.trigger_title, ar.created_at, COALESCE(ar.conversation_id::text, ''),
       COALESCE(r.id::text, ''), COALESCE(r.state, ''), r.finished_at, ar.trigger_payload::text
  FROM openfox.automation_runs ar
  LEFT JOIN openfox.conversation_inputs ci ON ci.organization_id = ar.organization_id AND ci.message_id = ar.message_id
  LEFT JOIN openfox.runs r ON r.organization_id = ci.organization_id AND r.id = ci.run_id`

// scanAutomation 读取 automationSelect 的一行（不含触发器与最近运行）。
func scanAutomation(row pgx.Row) (Automation, error) {
	var a Automation
	var kind string
	err := row.Scan(&a.ID, &a.SpaceID, &a.Title, &a.Prompt, &kind, &a.Assignee.ID, &a.Assignee.Name,
		&a.NotifyUserIDs, &a.Enabled, &a.Position, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt, &a.Version)
	a.Assignee.Kind = kind
	if a.NotifyUserIDs == nil {
		a.NotifyUserIDs = []string{}
	}
	return a, err
}

// loadAutomations 读取空间里满足条件的自动化，并填充触发器与最近一次运行。
func loadAutomations(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]Automation, error) {
	rows, err := tx.Query(ctx, automationSelect+` WHERE a.organization_id = openfox_private.current_organization_id() `+where+` ORDER BY a.position, a.created_at, a.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 空间里没有自动化时返回空数组而不是 nil，列表响应的 automations 才不会是 null。
	out := []Automation{}
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err := loadTriggers(ctx, tx, &out[i]); err != nil {
			return nil, err
		}
		last, err := latestRun(ctx, tx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].LastRun = last
	}
	return out, nil
}

// loadOneAutomation 读取单个自动化；不存在时返回 ErrNotFound。
func loadOneAutomation(ctx context.Context, tx pgx.Tx, spaceID, automationID string) (Automation, error) {
	items, err := loadAutomations(ctx, tx, `AND a.space_id = $1::uuid AND a.id = $2::uuid`, spaceID, automationID)
	if err != nil {
		return Automation{}, err
	}
	if len(items) == 0 {
		return Automation{}, ErrNotFound
	}
	return items[0], nil
}

func loadTriggers(ctx context.Context, tx pgx.Tx, a *Automation) error {
	rows, err := tx.Query(ctx, `
		SELECT t.id::text, t.kind, t.enabled, t.config::text, t.next_run_at,
		       t.webhook_token_hash IS NOT NULL, t.webhook_secret_ciphertext IS NOT NULL,
		       t.signature_required, t.ip_allowlist, t.last_source_ip
		  FROM openfox.automation_triggers t
		 WHERE t.organization_id = openfox_private.current_organization_id() AND t.automation_id = $1::uuid
		 ORDER BY t.position, t.id`, a.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	a.Triggers = []Trigger{}
	for rows.Next() {
		var t Trigger
		var config string
		var hasToken, hasSecret, signatureRequired bool
		var allowlist []string
		var lastSource *string
		if err := rows.Scan(&t.ID, &t.Kind, &t.Enabled, &config, &t.NextRunAt, &hasToken, &hasSecret,
			&signatureRequired, &allowlist, &lastSource); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(config), &t.Config); err != nil {
			return err
		}
		if t.Kind == "webhook" {
			if allowlist == nil {
				allowlist = []string{}
			}
			t.Webhook = &WebhookInfo{SignatureRequired: signatureRequired, HasSecret: hasSecret, IPAllowlist: allowlist, LastSourceIP: lastSource}
		}
		a.Triggers = append(a.Triggers, t)
	}
	return rows.Err()
}

// latestRun 读取自动化最近一次运行记录，没有时返回 nil。
func latestRun(ctx context.Context, tx pgx.Tx, automationID string) (*Run, error) {
	items, err := queryRuns(ctx, tx, `AND ar.automation_id = $1::uuid`, 1, automationID)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0].Run, nil
}

// queryRuns 按 created_at、id 倒序返回运行记录，最多返回 limit 行（调用方自行判断是否还有下一页）。
func queryRuns(ctx context.Context, tx pgx.Tx, where string, limit int, args ...any) ([]RunDetail, error) {
	rows, err := tx.Query(ctx, runSelect+` WHERE ar.organization_id = openfox_private.current_organization_id() `+where+
		fmt.Sprintf(` ORDER BY ar.created_at DESC, ar.id DESC LIMIT %d`, limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var details []RunDetail
	for rows.Next() {
		var run Run
		var triggerID, conversationID, runID, state, payload, storedStatus string
		if err := rows.Scan(&run.ID, &run.AutomationID, &triggerID, &run.TriggerKind, &storedStatus, &run.Reason,
			&run.TriggerTitle, &run.CreatedAt, &conversationID, &runID, &state, &run.FinishedAt, &payload); err != nil {
			return nil, err
		}
		run.Status = runStatus(storedStatus, state)
		run.TriggerID = optionalString(triggerID)
		run.ConversationID = optionalString(conversationID)
		run.RunID = optionalString(runID)
		detail := RunDetail{Run: run}
		if err := json.Unmarshal([]byte(payload), &detail.TriggerPayload); err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	return details, rows.Err()
}

// runStatus 把存储状态映射为契约状态：started 按关联 Run 的状态显示，尚未生成 Run 时显示 queued。
func runStatus(stored, state string) string {
	if stored != "started" {
		return stored
	}
	switch state {
	case "SUCCEEDED":
		return "completed"
	case "FAILED":
		return "failed"
	case "CANCELED":
		return "canceled"
	case "RUNNING", "CANCEL_REQUESTED":
		return "running"
	default:
		return "queued"
	}
}

// runTerminal 判断关联 Run 是否已结束；没有 Run（会话输入还没被消费）时视为未结束。
func runTerminal(state string) bool {
	return state == "SUCCEEDED" || state == "FAILED" || state == "CANCELED"
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// newUUID 生成 RFC 4122 第 4 版随机 UUID，与 dispatch 包的做法一致。
func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

// formatCursor 用 RFC3339Nano 时间与 ID 组成的游标，避免 OFFSET 翻页。
func formatCursor(createdAt time.Time, id string) string {
	return createdAt.UTC().Format(time.RFC3339Nano) + "|" + id
}

// NewID 生成新对象的 UUID。授权判断需要对象 ID，创建前由 API 层先生成。
func NewID() (string, error) { return newUUID() }
