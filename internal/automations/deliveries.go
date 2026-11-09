package automations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"openfox/internal/conversations"
)

const deliveryPageSize = 50

// 投递查询与重放。重放新建一条 replay_of 指向原投递的记录，跳过签名与去重，然后启动运行。

// deliverySelect 与 scanDeliveryDetail 的列顺序保持一致。
const deliverySelect = `
SELECT d.id::text, d.trigger_id::text, d.status, d.signature_status, d.provider, d.event, d.received_at,
       d.attempts, COALESCE(d.replay_of::text, ''), COALESCE(d.automation_run_id::text, ''),
       d.last_attempt_at, d.response_status, d.response_body, d.source_ip, d.dedupe_key, d.dedupe_source,
       d.content_type, d.headers::text, d.body, d.body_truncated, d.error
  FROM openfox.webhook_deliveries d
  JOIN openfox.automations a ON a.organization_id = d.organization_id AND a.id = d.automation_id`

// ListDeliveries 返回自动化的投递记录，新的在前，每页 50。
func (s Service) ListDeliveries(ctx context.Context, tx pgx.Tx, spaceID, automationID, cursor string) ([]Delivery, string, error) {
	if _, err := loadOneAutomation(ctx, tx, spaceID, automationID); err != nil {
		return nil, "", err
	}
	where := `AND d.automation_id = $2::uuid`
	args := []any{spaceID, automationID}
	if cursor != "" {
		receivedAt, id, err := parseCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		where += ` AND (d.received_at, d.id) < ($3::timestamptz, $4::uuid)`
		args = append(args, receivedAt, id)
	}
	rows, err := tx.Query(ctx, deliverySelect+` WHERE d.organization_id = openfox_private.current_organization_id() AND a.space_id = $1::uuid `+where+
		` ORDER BY d.received_at DESC, d.id DESC LIMIT 51`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	// 没有投递时也返回空数组：契约要求 deliveries 是数组，nil 切片会被编码成 null。
	out := []Delivery{}
	for rows.Next() {
		var d DeliveryDetail
		if err := scanDeliveryDetail(rows, &d); err != nil {
			return nil, "", err
		}
		out = append(out, d.Delivery)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	rows.Close()
	next := ""
	if len(out) > deliveryPageSize {
		out = out[:deliveryPageSize]
		last := out[deliveryPageSize-1]
		next = formatCursor(last.ReceivedAt, last.ID)
	}
	return out, next, nil
}

// GetDelivery 返回投递详情，包括是否可以重放及不可重放的原因。
func (s Service) GetDelivery(ctx context.Context, tx pgx.Tx, spaceID, automationID, deliveryID string) (DeliveryDetail, error) {
	var d DeliveryDetail
	err := scanDeliveryDetail(tx.QueryRow(ctx, deliverySelect+` WHERE d.organization_id = openfox_private.current_organization_id()
		  AND a.space_id = $1::uuid AND d.automation_id = $2::uuid AND d.id = $3::uuid`, spaceID, automationID, deliveryID), &d)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryDetail{}, ErrNotFound
	}
	if err != nil {
		return DeliveryDetail{}, err
	}
	return d, nil
}

// scanDeliveryDetail 读取 deliverySelect 的一行。
func scanDeliveryDetail(row pgx.Row, d *DeliveryDetail) error {
	var headers string
	var body []byte
	var replayOf, runID string
	err := row.Scan(&d.ID, &d.TriggerID, &d.Status, &d.SignatureStatus, &d.Provider, &d.Event, &d.ReceivedAt,
		&d.Attempts, &replayOf, &runID, &d.LastAttemptAt, &d.ResponseStatus, &d.ResponseBody, &d.SourceIP,
		&d.DedupeKey, &d.DedupeSource, &d.ContentType, &headers, &body, &d.BodyTruncated, &d.Error)
	if err != nil {
		return err
	}
	d.ReplayOf = optionalString(replayOf)
	d.AutomationRunID = optionalString(runID)
	d.Headers = map[string]string{}
	if err := json.Unmarshal([]byte(headers), &d.Headers); err != nil {
		return err
	}
	d.Body = bodyText(body)
	d.Replayable, d.NotReplayableReason = replayability(d.Status, d.SignatureStatus)
	return nil
}

// bodyText 能按 UTF-8 解码就返回文本，否则返回 base64。
func bodyText(body []byte) string {
	if utf8.Valid(body) {
		return string(body)
	}
	return base64.StdEncoding.EncodeToString(body)
}

// replayability 判断投递能否重放：排队中、已拒绝或签名失败的投递不能重放。
func replayability(status, signature string) (bool, *string) {
	reason := ""
	switch {
	case status == "queued":
		reason = "queued"
	case status == "rejected":
		reason = "rejected"
	case signature != "valid" && signature != "not_required":
		reason = "signature"
	}
	if reason == "" {
		return true, nil
	}
	return false, &reason
}

// ReplayDelivery 重放一次投递：复制原投递的请求内容，跳过签名与去重检查，然后启动运行。
// 重放由操作者发起，运行以操作者身份启动（与手动运行一致）。
func (s Service) ReplayDelivery(ctx context.Context, tx pgx.Tx, organizationID, spaceID, automationID, deliveryID string, requester conversations.Actor) (Delivery, error) {
	var status, signature, triggerID, provider, event string
	var body []byte
	var triggerEnabled, automationEnabled bool
	err := tx.QueryRow(ctx, `
		SELECT d.status, d.signature_status, d.trigger_id::text, d.provider, d.event, d.body,
		       t.enabled, a.enabled
		  FROM openfox.webhook_deliveries d
		  JOIN openfox.automations a ON a.organization_id = d.organization_id AND a.id = d.automation_id
		  JOIN openfox.automation_triggers t ON t.organization_id = d.organization_id AND t.id = d.trigger_id
		 WHERE d.organization_id = openfox_private.current_organization_id() AND a.space_id = $1::uuid
		   AND d.automation_id = $2::uuid AND d.id = $3::uuid
		 FOR UPDATE OF d`, spaceID, automationID, deliveryID).Scan(&status, &signature, &triggerID, &provider, &event,
		&body, &triggerEnabled, &automationEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Delivery{}, ErrNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	if replayable, _ := replayability(status, signature); !replayable {
		return Delivery{}, ErrNotReplayable
	}
	if !triggerEnabled || !automationEnabled {
		return Delivery{}, ErrDisabled
	}
	newID, err := newUUID()
	if err != nil {
		return Delivery{}, err
	}
	var receivedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO openfox.webhook_deliveries (
			organization_id, id, automation_id, trigger_id, status, signature_status, provider, event,
			dedupe_key, dedupe_source, content_type, source_ip, headers, body, response_status, replay_of
		)
		SELECT organization_id, $1::uuid, automation_id, trigger_id, 'dispatched', signature_status, provider, event,
		       dedupe_key, dedupe_source, content_type, source_ip, headers, body, 201, id
		  FROM openfox.webhook_deliveries
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $2::uuid
		RETURNING received_at`, newID, deliveryID).Scan(&receivedAt)
	if err != nil {
		return Delivery{}, err
	}
	run, err := s.StartRun(ctx, tx, organizationID, automationID, Source{
		TriggerID: triggerID, Kind: "webhook", Title: webhookTitle(provider, event) + "（重放）",
		Payload:   map[string]any{"provider": provider, "event": event, "delivery_id": newID, "replay_of": deliveryID},
		Context:   webhookContext(provider, event, body),
		DedupeKey: newID, Requester: &requester,
	})
	if err != nil {
		return Delivery{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE openfox.webhook_deliveries SET automation_run_id = $2::uuid
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, newID, run.ID); err != nil {
		return Delivery{}, err
	}
	replayOf := deliveryID
	runID := run.ID
	return Delivery{
		ID: newID, TriggerID: triggerID, Status: "dispatched", SignatureStatus: signature,
		Provider: provider, Event: event, ReceivedAt: receivedAt, Attempts: 1,
		ReplayOf: &replayOf, AutomationRunID: &runID,
	}, nil
}
