package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations/wecom"
)

type wecomIncoming struct {
	MessageID string `json:"msgid"`
	BotID     string `json:"aibotid"`
	ChatType  string `json:"chattype"`
	Type      string `json:"msgtype"`
	From      struct {
		UserID string `json:"userid"`
	} `json:"from"`
	Text struct {
		Content string `json:"content"`
	} `json:"text"`
}

func (m *LongConnManager) runWeComSession(ctx context.Context, key sessionKey, botID, secret string) {
	if m.Service.WeCom == nil {
		m.markPermanent(ctx, key, errIMClientMissing)
		return
	}
	failures := 0
	for {
		var connected *wecom.Session
		err := m.Service.WeCom.Run(ctx, wecom.Config{
			BotID: botID, Secret: secret,
			OnReady: func(session *wecom.Session) {
				connected = session
				m.mu.Lock()
				if m.wecomSessions == nil {
					m.wecomSessions = map[sessionKey]*wecom.Session{}
				}
				m.wecomSessions[key] = session
				m.mu.Unlock()
				failures = 0
				m.markConnected(ctx, key)
			},
			Handler: func(eventCtx context.Context, message wecom.Message) error {
				return m.handleWeComMessage(eventCtx, key, botID, message)
			},
		})
		m.mu.Lock()
		if m.wecomSessions[key] == connected {
			delete(m.wecomSessions, key)
		}
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if wecom.IsPermanent(err) {
			m.markPermanent(ctx, key, err)
			return
		}
		failures++
		m.markReconnecting(ctx, key, failures, err)
		wait := time.NewTimer(m.backoff(err, failures))
		select {
		case <-ctx.Done():
			wait.Stop()
			return
		case <-wait.C:
		}
	}
}

func (m *LongConnManager) wecomLease(ctx context.Context, key sessionKey) (bool, error) {
	var owned bool
	err := m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM openfox.im_connection_states state
			    JOIN openfox.integration_connections conn
			      ON conn.organization_id = state.organization_id AND conn.id = state.connection_id
			   WHERE state.organization_id = openfox_private.current_organization_id()
			     AND state.connection_id = $1::uuid AND state.lease_owner = $2
			     AND state.lease_expires_at > clock_timestamp() AND state.state = 'CONNECTED'
			     AND conn.provider = 'wecom' AND conn.status = 'ACTIVE')`, key.connectionID, m.instance()).Scan(&owned)
	})
	return owned, err
}

func (m *LongConnManager) SendWeCom(ctx context.Context, organizationID, connectionID, chatID, content string) error {
	key := sessionKey{organizationID: organizationID, connectionID: connectionID}
	owned, err := m.wecomLease(ctx, key)
	if err != nil {
		return err
	}
	if !owned {
		return wecom.ErrUnavailable
	}
	m.mu.Lock()
	session := m.wecomSessions[key]
	m.mu.Unlock()
	if session == nil {
		return wecom.ErrUnavailable
	}
	return session.Send(ctx, chatID, content)
}

func (m *LongConnManager) handleWeComMessage(ctx context.Context, key sessionKey, externalBotID string, message wecom.Message) error {
	var inbound wecomIncoming
	if json.Unmarshal(message.Body, &inbound) != nil || inbound.BotID != externalBotID || inbound.ChatType != "single" ||
		inbound.MessageID == "" || len(inbound.MessageID) > 128 || inbound.From.UserID == "" || len(inbound.From.UserID) > 128 ||
		len([]rune(inbound.Text.Content)) > 8000 {
		return nil
	}
	return m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var botID string
		err := tx.QueryRow(ctx, `
			SELECT bot.id::text FROM openfox.im_bots bot
			  JOIN openfox.integration_connections conn ON conn.organization_id = bot.organization_id AND conn.id = bot.connection_id
			  JOIN openfox.im_connection_states state ON state.organization_id = conn.organization_id AND state.connection_id = conn.id
			 WHERE bot.organization_id = openfox_private.current_organization_id() AND conn.id = $1::uuid
			   AND conn.provider = 'wecom' AND conn.external_id = $2 AND conn.status = 'ACTIVE'
			   AND bot.removed_at IS NULL AND state.lease_owner = $3 AND state.lease_expires_at > clock_timestamp()`,
			key.connectionID, externalBotID, m.instance()).Scan(&botID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var inboundID string
		err = tx.QueryRow(ctx, `
			INSERT INTO openfox.im_inbound_messages
			    (organization_id, bot_id, remote_message_id, chat_id, chat_type, sender_open_id, sender_union_id, message_type, text)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, 'p2p', $3, $3, $4, $5)
			ON CONFLICT DO NOTHING RETURNING id::text`, botID, inbound.MessageID, inbound.From.UserID,
			inbound.Type, inbound.Text.Content).Scan(&inboundID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if inbound.Type != "text" || strings.TrimSpace(inbound.Text.Content) == "" {
			return nil
		}
		bound, err := m.Service.ConsumeWeComBindingCode(ctx, tx, botID, inbound.From.UserID, strings.TrimSpace(inbound.Text.Content))
		if err != nil || !bound {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE openfox.im_inbound_messages
			   SET status = 'REJECTED', result = 'bound', finished_at = clock_timestamp()
			 WHERE organization_id = openfox_private.current_organization_id() AND id = $1::uuid`, inboundID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.im_reply_outbox (organization_id, bot_id, chat_id, body)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, '企业微信账号已绑定到 OpenFox。')`, botID, inbound.From.UserID)
		return err
	})
}
