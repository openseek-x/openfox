package integrations

// 钉钉消息文本：机器人发 sampleMarkdown（title + text）。出站队列里 card 列存 {"title","text"} 的 JSON，
// 和飞书存卡片 JSON 的位置相同，worker 按服务商解释。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"openfox/internal/integrations/dingtalk"
	"openfox/internal/integrations/feishu"
)

var dingTalkMDEscaper = strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "~", "\\~", "`", "\\`",
	"[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)", "#", "\\#", "<", "\\<", ">", "\\>")

func dingTalkPayload(title, text string) string {
	encoded, _ := json.Marshal(map[string]string{"title": title, "text": text})
	return string(encoded)
}

// buildDingTalkNotification 生成通知消息：标题、任务（编号 · 标题）、摘要与「在 OpenFox 中查看」链接。
func buildDingTalkNotification(kind, heading, summary, link string) string {
	var text strings.Builder
	text.WriteString("### " + cardTitle(kind) + "\n\n")
	if heading != "" {
		text.WriteString("**" + dingTalkMDEscaper.Replace(heading) + "**\n\n")
	}
	text.WriteString(dingTalkMDEscaper.Replace(cardSummary(summary)))
	if link != "" {
		text.WriteString("\n\n[" + notifyButtonText + "](" + link + ")")
	}
	return dingTalkPayload(cardTitle(kind), text.String())
}

// buildDingTalkReply 生成 Agent 回复：正文保持 Agent 输出的 markdown（钉钉支持常用子集），过长截断，末尾附会话链接。
func buildDingTalkReply(body, link string) string {
	body = strings.TrimSpace(body)
	if len([]rune(body)) > maxReplyRunes {
		body = limitRunes(body, maxReplyRunes) + replyTruncatedNote
	}
	if link != "" {
		body += "\n\n[" + replyLinkText + "](" + link + ")"
	}
	return dingTalkPayload("OpenFox", body)
}

// splitDingTalkPayload 解开 dingTalkPayload。
func splitDingTalkPayload(payload string) (title, text string) {
	var parsed struct{ Title, Text string }
	_ = json.Unmarshal([]byte(payload), &parsed)
	return parsed.Title, parsed.Text
}

// mapDingTalkError 把钉钉客户端的错误换成 worker 里按飞书错误分类处理的错误（限流、凭证无效、用户不可达、可重试），
// 这样通知与回复的重试 / 退避 / 标记需要处理的逻辑只写一份。
func mapDingTalkError(err error) error {
	var limited *dingtalk.RateLimitedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &limited):
		return &feishu.RateLimitedError{RetryAfter: limited.RetryAfter}
	case errors.Is(err, dingtalk.ErrInvalidCredentials):
		return feishu.ErrInvalidCredentials
	case errors.Is(err, dingtalk.ErrUserUnreachable):
		return feishu.ErrUserUnreachable
	case errors.Is(err, dingtalk.ErrTokenExpired):
		return feishu.ErrTokenExpired
	default:
		return err
	}
}

// dingTalkSendOnce 取（或使用同一批里缓存的）accessToken 并调用 send；令牌失效时清缓存重试一次。
func dingTalkSendOnce(ctx context.Context, client *dingtalk.Client, tokens map[string]string, connectionID, appKey, secret string,
	send func(token string) (string, error)) (string, error) {
	if client == nil {
		return "", errIMClientMissing
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, ok := tokens[connectionID]
		if !ok {
			var err error
			if token, err = client.AccessToken(ctx, appKey, secret); err != nil {
				return "", mapDingTalkError(err)
			}
			tokens[connectionID] = token
		}
		id, err := send(token)
		if errors.Is(err, dingtalk.ErrTokenExpired) {
			client.ClearToken(appKey, secret)
			delete(tokens, connectionID)
			continue
		}
		return id, mapDingTalkError(err)
	}
	return "", feishu.ErrUnavailable
}

// sendDingTalk 发送一条通知：机器人单聊发给员工 userid。robotCode 用 AppKey（企业内部应用机器人的 robotCode 与 AppKey 相同）。
func (w *FeishuNotifyWorker) sendDingTalk(ctx context.Context, tokens map[string]string, connectionID string, target notifyTarget, secret string) (string, error) {
	client := w.Service.DingTalk
	title, text := splitDingTalkPayload(target.card)
	return dingTalkSendOnce(ctx, client, tokens, connectionID, target.appID, secret, func(token string) (string, error) {
		return client.SendUserMarkdown(ctx, token, target.appID, target.openID, title, text)
	})
}

// sendDingTalkReply 发送一条机器人回复：chat_id 以 u: 开头是单聊（员工 userid），以 g: 开头是群聊（openConversationId）。
func (w *IMWorker) sendDingTalkReply(ctx context.Context, tokens map[string]string, target imOutboxTarget) (string, error) {
	client := w.Service.DingTalk
	title, text := splitDingTalkPayload(target.card)
	chatID := target.item.ChatID
	return dingTalkSendOnce(ctx, client, tokens, target.connectionID, target.appID, target.secret, func(token string) (string, error) {
		switch {
		case strings.HasPrefix(chatID, "u:"):
			return client.SendUserMarkdown(ctx, token, target.appID, strings.TrimPrefix(chatID, "u:"), title, text)
		case strings.HasPrefix(chatID, "g:"):
			return client.SendGroupMarkdown(ctx, token, target.appID, strings.TrimPrefix(chatID, "g:"), title, text)
		}
		return "", dingtalk.ErrUserUnreachable
	})
}
