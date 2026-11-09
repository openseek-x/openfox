package integrations

// 组织连接的 Webhook 配置（Payload URL、签名密钥、最近投递）与重新生成。

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/membership"
)

// WebhookEvents 是需要在 GitHub 里勾选的事件。
var WebhookEvents = []string{"pull_request", "push", "issues", "issue_comment"}

// webhookEventsFor 是各服务商 Webhook 里需要勾选的事件（界面原样显示）。
func webhookEventsFor(provider string) []string {
	switch provider {
	case ProviderGitLab, ProviderGitLabCN:
		return []string{"Merge request events", "Push events", "Issues events", "Comments"}
	case ProviderGitee:
		return []string{"Push", "Pull Request", "Issues", "Comments"}
	}
	return append([]string(nil), WebhookEvents...)
}

type WebhookDelivery struct {
	Event       string    `json:"event"`
	Action      string    `json:"action"`
	Status      string    `json:"status"`
	Detail      string    `json:"detail"`
	LinkedCount int       `json:"linked_count"`
	ReceivedAt  time.Time `json:"received_at"`
}

// Webhook 对应契约 IntegrationWebhook。Secret 是签名密钥明文，只返回给所有者 / 管理员。
type Webhook struct {
	ConnectionID   string            `json:"connection_id"`
	PayloadURL     string            `json:"payload_url"`
	ContentType    string            `json:"content_type"`
	Secret         string            `json:"secret"`
	Events         []string          `json:"events"`
	LastDeliveryAt *time.Time        `json:"last_delivery_at"`
	Recent         []WebhookDelivery `json:"recent"`
	Version        uint64            `json:"version"`
}

// PayloadURL 生成 Webhook 地址（路径带服务商标识）。
func (s Service) PayloadURL(provider, token string) string {
	return strings.TrimRight(s.PublicURL, "/") + "/api/v1/hooks/git/" + provider + "/" + token
}

// GetWebhook 返回 Webhook 配置。调用方需要写审计 integration.webhook_secret_viewed。
func (s Service) GetWebhook(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, connectionID string) (Webhook, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Webhook{}, err
	}
	if !CanManage(actor.Role) {
		return Webhook{}, ErrForbidden
	}
	if !membership.ValidateUUID(connectionID) {
		return Webhook{}, ErrNotFound
	}
	return s.loadWebhook(ctx, tx, organizationID, connectionID, false)
}

// RotateWebhook 重新生成地址令牌与签名密钥（version 做 CAS），旧地址立即失效。
func (s Service) RotateWebhook(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, connectionID string, version uint64) (Webhook, error) {
	if err := (collaboration.Service{}).CheckActor(ctx, tx, organizationID, actor); err != nil {
		return Webhook{}, err
	}
	if !CanManage(actor.Role) {
		return Webhook{}, ErrForbidden
	}
	if !membership.ValidateUUID(connectionID) {
		return Webhook{}, ErrNotFound
	}
	if version == 0 {
		return Webhook{}, ErrInvalidRequest
	}
	current, err := s.loadWebhook(ctx, tx, organizationID, connectionID, true)
	if err != nil {
		return Webhook{}, err
	}
	if current.Version != version {
		return Webhook{}, ErrVersionConflict
	}
	var sealed []byte
	if err := tx.QueryRow(ctx, `
		SELECT credentials_sealed FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, connectionID).Scan(&sealed); err != nil {
		return Webhook{}, err
	}
	creds, err := s.openCredentials(organizationID, connectionID, sealed)
	if err != nil {
		return Webhook{}, err
	}
	if creds.WebhookToken, err = randomBase64URL(32); err != nil {
		return Webhook{}, err
	}
	if creds.WebhookSecret, err = randomHex(32); err != nil {
		return Webhook{}, err
	}
	blob, keyID, err := s.sealCredentials(organizationID, connectionID, creds)
	if err != nil {
		return Webhook{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE openfox.integration_connections
		   SET credentials_sealed = $2, credentials_key_id = $3, webhook_token_hash = $4, webhook_last_delivery_at = NULL,
		       updated_at = clock_timestamp(), version = version + 1
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid AND version = $5`,
		connectionID, blob, keyID, TokenHash(creds.WebhookToken), int64(version))
	if err != nil {
		return Webhook{}, err
	}
	if tag.RowsAffected() != 1 {
		return Webhook{}, ErrVersionConflict
	}
	return s.loadWebhook(ctx, tx, organizationID, connectionID, false)
}

func (s Service) loadWebhook(ctx context.Context, tx pgx.Tx, organizationID, connectionID string, lock bool) (Webhook, error) {
	query := `
		SELECT credentials_sealed, webhook_last_delivery_at, version, provider
		  FROM openfox.integration_connections
		 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid
		   AND provider IN ` + gitProviderIn + ` AND scope = 'ORGANIZATION' AND status <> 'DISCONNECTED'`
	if lock {
		query += " FOR UPDATE"
	}
	var sealed []byte
	var lastDelivery *time.Time
	var version int64
	var provider string
	err := tx.QueryRow(ctx, query, connectionID).Scan(&sealed, &lastDelivery, &version, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return Webhook{}, ErrNotFound
	}
	if err != nil {
		return Webhook{}, err
	}
	creds, err := s.openCredentials(organizationID, connectionID, sealed)
	if err != nil {
		return Webhook{}, err
	}
	if creds.WebhookToken == "" || creds.WebhookSecret == "" {
		return Webhook{}, ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		SELECT event, action, status, detail, linked_count, received_at
		  FROM openfox.integration_webhook_deliveries
		 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid
		 ORDER BY received_at DESC, id LIMIT 10`, connectionID)
	if err != nil {
		return Webhook{}, err
	}
	defer rows.Close()
	recent := make([]WebhookDelivery, 0, 10)
	for rows.Next() {
		var item WebhookDelivery
		if err := rows.Scan(&item.Event, &item.Action, &item.Status, &item.Detail, &item.LinkedCount, &item.ReceivedAt); err != nil {
			return Webhook{}, err
		}
		recent = append(recent, item)
	}
	if err := rows.Err(); err != nil {
		return Webhook{}, err
	}
	return Webhook{ConnectionID: connectionID, PayloadURL: s.PayloadURL(provider, creds.WebhookToken), ContentType: "application/json",
		Secret: creds.WebhookSecret, Events: webhookEventsFor(provider), LastDeliveryAt: lastDelivery, Recent: recent,
		Version: uint64(version)}, nil
}
