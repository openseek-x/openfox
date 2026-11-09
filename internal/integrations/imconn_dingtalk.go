package integrations

// 钉钉 Stream 长连接会话：与飞书共用租约、状态机、事件去重表和入站消息队列，
// 这里只放钉钉协议相关的重连循环与机器人消息解析。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"openfox/internal/integrations/dingtalk"
)

func (m *LongConnManager) runDingTalkSession(ctx context.Context, key sessionKey, appKey, secret, accountKind string) {
	client := m.Service.DingTalk
	if client == nil {
		m.markPermanent(ctx, key, errIMClientMissing)
		return
	}
	failures := 0
	for {
		err := client.Run(ctx, dingtalk.Config{AppKey: appKey, Secret: secret,
			OnConnected: func() {
				failures = 0
				m.markConnected(ctx, key)
			},
			Handler: m.dingTalkHandler(key, appKey, secret, accountKind),
		})
		if ctx.Err() != nil {
			return
		}
		if dingtalk.IsPermanent(err) {
			m.markPermanent(ctx, key, err)
			return
		}
		failures++
		// 服务端要求断开（disconnect）是正常的负载均衡：不计入失败，立即重连。
		wait := time.Duration(0)
		if errors.Is(err, dingtalk.ErrDisconnected) {
			failures = 0
		} else {
			m.markReconnecting(ctx, key, failures, err)
			wait = m.backoff(err, failures)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// dingTalkMessage 是机器人消息回调（topic /v1.0/im/bot/messages/get）里用到的字段。
type dingTalkMessage struct {
	ConversationID   string `json:"conversationId"`
	ConversationType string `json:"conversationType"` // 1 单聊 2 群聊
	MsgID            string `json:"msgId"`
	MsgType          string `json:"msgtype"`
	SenderStaffID    string `json:"senderStaffId"`
	SenderNick       string `json:"senderNick"`
	IsInAtList       bool   `json:"isInAtList"`
	RobotCode        string `json:"robotCode"`
	ChatbotCorpID    string `json:"chatbotCorpId"`
	SenderCorpID     string `json:"senderCorpId"`
	Text             struct {
		Content string `json:"content"`
	} `json:"text"`
}

// dingTalkChatID 把会话编码进 chat_id：单聊是 u:<员工 userid>，群聊是 g:<conversationId>。
// 发送回复时要知道是发单聊（需要员工 userid）还是发群聊（需要 openConversationId），编码在 chat_id 里就不需要新增列。
func dingTalkChatID(conversationType, conversationID, staffID string) (chatID, chatType string) {
	if conversationType == "1" {
		return "u:" + staffID, "p2p"
	}
	return "g:" + conversationID, "group"
}

// dingTalkHandler 返回某个连接的消息处理函数：机器人连接收到的消息按 msgId 去重后入队（快速 ACK），组织应用连接只记录。
func (m *LongConnManager) dingTalkHandler(key sessionKey, appKey, secret, accountKind string) dingtalk.Handler {
	return func(ctx context.Context, msg dingtalk.Message) error {
		m.touchEvent(ctx, key)
		if msg.Type != "CALLBACK" || msg.Topic != dingtalk.TopicBotMessage {
			return nil
		}
		var message dingTalkMessage
		if json.Unmarshal(msg.Data, &message) != nil || message.MsgID == "" {
			return nil
		}
		fresh, err := m.recordInbound(ctx, key, "dt:"+message.MsgID, "bot_message")
		if err != nil {
			return err
		}
		if !fresh || (accountKind != AccountKindTaskAssistant && accountKind != AccountKindAgent) {
			return nil
		}
		// 回调里发送人所在企业与机器人所在企业不一致：丢弃（日志不含个人信息）。
		if message.ChatbotCorpID != "" && message.SenderCorpID != "" && message.ChatbotCorpID != message.SenderCorpID {
			m.logger().Warn("dingtalk_message_dropped", "connection_id", key.connectionID, "reason", "corp_mismatch")
			return nil
		}
		// 员工 userid 只在一个企业内唯一，不能拿它匹配成员：用机器人应用自己的 accessToken 换成 unionId。
		// 换不到（不是本企业员工、没开通权限、钉钉不可达）时 unionId 留空，入站 worker 会按「未绑定」拒绝并回复提示。
		unionID, corpID, err := m.dingTalkUnionID(ctx, appKey, secret, message.SenderStaffID)
		if err != nil {
			m.logger().Warn("dingtalk_union_lookup_failed", "connection_id", key.connectionID, "error", sanitizeLastError(err))
			unionID = ""
		}
		if corpID != "" && message.ChatbotCorpID != "" && corpID != message.ChatbotCorpID {
			m.logger().Warn("dingtalk_message_dropped", "connection_id", key.connectionID, "reason", "corp_mismatch")
			return nil
		}
		if err := m.enqueueDingTalkMessage(ctx, key, message, unionID); err != nil {
			m.logger().Warn("dingtalk_message_enqueue_failed", "connection_id", key.connectionID, "error", sanitizeLastError(err))
		}
		return nil
	}
}

// enqueueDingTalkMessage 把机器人消息写入 im_inbound_messages。钉钉只会把单聊和群里 @ 了机器人的消息推给机器人，所以不用再判断 @；
// 没有员工 userid 的发送人（外部群的外部用户）丢弃；非文本消息入队时文本为空，由 IMWorker 回复「暂不支持」。
func (m *LongConnManager) enqueueDingTalkMessage(ctx context.Context, key sessionKey, message dingTalkMessage, unionID string) error {
	if message.SenderStaffID == "" || message.ConversationID == "" {
		return nil
	}
	chatID, chatType := dingTalkChatID(message.ConversationType, message.ConversationID, message.SenderStaffID)
	text := ""
	if message.MsgType == "" || message.MsgType == "text" {
		text = strings.TrimSpace(message.Text.Content)
	}
	return m.inOrganization(ctx, key.organizationID, func(ctx context.Context, tx pgx.Tx) error {
		var botID string
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM openfox.im_bots
			 WHERE organization_id = openfox_private.current_organization_id() AND connection_id = $1::uuid AND removed_at IS NULL`,
			key.connectionID).Scan(&botID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		msgType := message.MsgType
		if msgType == "" {
			msgType = "text"
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO openfox.im_inbound_messages (organization_id, bot_id, remote_message_id, chat_id, chat_type, sender_open_id,
			       sender_union_id, message_type, text, parent_remote_id)
			VALUES (openfox_private.current_organization_id(), $1::uuid, $2, $3, $4, $5, $6, $7, $8, '')
			ON CONFLICT DO NOTHING`, botID, clip(message.MsgID, 128), clip(chatID, 128), chatType,
			clip(message.SenderStaffID, 128), clip(unionID, 128), clip(msgType, 32), text)
		return err
	})
}

const (
	dingTalkUnionTTL        = 10 * time.Minute
	dingTalkUnionCacheLimit = 1024
)

type dingTalkUnionEntry struct {
	unionID, corpID string
	expires         time.Time
}

// dingTalkUnionID 把发送人的员工 userid 换成 unionId（与所属企业 corpId）。结果在进程内缓存（TTL 10 分钟、最多 1024 条，
// 满了先清过期的，仍满就随机丢一条），缓存键带 AppKey，不同应用 / 企业的相同 userid 不会互相命中。
// 只有成功的结果才缓存：失败（含「找不到员工」）每次都重新问，避免员工刚入职 / 刚开通权限时被长期拒绝。
func (m *LongConnManager) dingTalkUnionID(ctx context.Context, appKey, secret, staffID string) (string, string, error) {
	client := m.Service.DingTalk
	if client == nil {
		return "", "", errIMClientMissing
	}
	cacheKey := appKey + "\x00" + staffID
	m.mu.Lock()
	if entry, ok := m.unionCache[cacheKey]; ok && time.Now().Before(entry.expires) {
		m.mu.Unlock()
		return entry.unionID, entry.corpID, nil
	}
	m.mu.Unlock()
	var profile dingtalk.StaffProfile
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.AccessToken(ctx, appKey, secret)
		if err != nil {
			return "", "", err
		}
		profile, err = client.UnionIDByStaffID(ctx, token, staffID)
		if errors.Is(err, dingtalk.ErrTokenExpired) {
			client.ClearToken(appKey, secret)
			continue
		}
		if err != nil {
			return "", "", err
		}
		break
	}
	if profile.UnionID == "" {
		return "", "", dingtalk.ErrUserUnreachable
	}
	m.mu.Lock()
	if m.unionCache == nil {
		m.unionCache = map[string]dingTalkUnionEntry{}
	}
	if len(m.unionCache) >= dingTalkUnionCacheLimit {
		now := time.Now()
		for k, v := range m.unionCache {
			if now.After(v.expires) {
				delete(m.unionCache, k)
			}
		}
		for k := range m.unionCache {
			if len(m.unionCache) < dingTalkUnionCacheLimit {
				break
			}
			delete(m.unionCache, k)
		}
	}
	m.unionCache[cacheKey] = dingTalkUnionEntry{unionID: profile.UnionID, corpID: profile.CorpID, expires: time.Now().Add(dingTalkUnionTTL)}
	m.mu.Unlock()
	return profile.UnionID, profile.CorpID, nil
}
