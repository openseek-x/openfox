package integrations

// 钉钉：应用校验、配置指引、个人授权。应用 / 个人绑定的数据流与飞书共用（FeishuApp / FeishuPersonalBinding 等类型、
// 状态机、长连接状态、通知出站都复用），这里只放钉钉协议相关的分支。
//
// 与飞书的差异：
//   - 钉钉没有「读取机器人信息」接口：校验只做 AppKey / AppSecret 换 accessToken，机器人名称显示为 AppKey；
//   - 个人授权不使用 PKCE（官方网页登录文档没有 code_challenge 参数），只靠 state；
//   - 个人连接的 settings.union_id 存 unionId（contact/users/me 给出，开发者账号范围内唯一），settings.staff_id 存员工 userid
//     （机器人单聊发消息要用）。入站消息只按 unionId 匹配成员：员工 userid 只在一个企业内唯一，不同企业可能重复，
//     所以入站时用机器人应用自己的 accessToken 把 senderStaffId 换成 unionId（topapi/v2/user/get），换不到就按未绑定拒绝；
//   - 没有链接预览。

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"openfox/internal/collaboration"
	"openfox/internal/integrations/dingtalk"
	"openfox/internal/integrations/feishu"
)

// IMConfigured 判断当前服务商的客户端是否已配置。
func (s Service) IMConfigured() bool {
	if s.prov() == ProviderDingTalk {
		return s.DingTalk != nil
	}
	if s.prov() == ProviderWeCom {
		return s.WeCom != nil
	}
	return s.Feishu != nil
}

func (s Service) dingTalkGuide() FeishuGuide {
	dev := "https://open-dev.dingtalk.com"
	if s.DingTalk != nil && s.DingTalk.DevURL != "" {
		dev = s.DingTalk.DevURL
	}
	return FeishuGuide{
		OpenPlatformURL: dev + "/app",
		RedirectURI:     s.FeishuRedirectURI(),
		// 钉钉没有任务链接预览。
		LinkPreviewPattern: "",
		// 应用需要开通的权限点（官方文档：机器人发消息 qyapi_robot_sendmsg；成员信息读权限 qyapi_get_member，用于 unionId ↔ userid 互换；
		// 个人登录读取通讯录个人信息 Contact.User.Read）。
		ScopesJSON: `{
  "permissions": [
    "qyapi_robot_sendmsg",
    "qyapi_get_member",
    "Contact.User.Read"
  ]
}`,
		Events:    []string{},
		Callbacks: []string{dingtalk.TopicBotMessage},
	}
}

// dingTalkValidate 用 AppKey / AppSecret 换 accessToken（不碰数据库）。校验必须真的问一次钉钉：先丢弃缓存。
func (s Service) dingTalkValidate(ctx context.Context, appKey, secret string) (feishu.Bot, error) {
	if s.DingTalk == nil {
		return feishu.Bot{}, ErrNotConfigured
	}
	s.DingTalk.ClearToken(appKey, secret)
	if _, err := s.DingTalk.AccessToken(ctx, appKey, secret); err != nil {
		if errors.Is(err, dingtalk.ErrInvalidCredentials) {
			return feishu.Bot{}, ErrFeishuCredentialsInvalid
		}
		return feishu.Bot{}, ErrFeishuUnavailable
	}
	// 钉钉没有机器人信息接口：名称用 AppKey（botDisplayName 在名称为空时回落到 AppKey）。
	return feishu.Bot{}, nil
}

// dingTalkExchange 用授权码换取用户令牌、读取身份，再用应用 accessToken 把 unionId 换成员工 userid（事务外；令牌随即丢弃）。
func (s Service) dingTalkExchange(ctx context.Context, target FeishuCallbackTarget, code string) FeishuCallbackOutcome {
	fail := FeishuCallbackOutcome{Failure: ReasonExchangeFailed}
	if s.DingTalk == nil {
		return fail
	}
	userToken, err := s.DingTalk.ExchangeCode(ctx, target.AppID, target.Secret, code)
	if err != nil {
		return fail
	}
	user, err := s.DingTalk.UserInfo(ctx, userToken)
	if err != nil {
		return fail
	}
	var staffID string
	for attempt := 0; attempt < 2 && staffID == ""; attempt++ {
		appToken, err := s.DingTalk.AccessToken(ctx, target.AppID, target.Secret)
		if err != nil {
			return fail
		}
		staffID, err = s.DingTalk.StaffIDByUnionID(ctx, appToken, user.UnionID)
		if errors.Is(err, dingtalk.ErrTokenExpired) {
			s.DingTalk.ClearToken(target.AppID, target.Secret)
			continue
		}
		if err != nil {
			// 扫码的人不是本企业员工（找不到 userid）：不能收通知，也认不出机器人消息，按换取失败处理。
			return fail
		}
	}
	if staffID == "" {
		return fail
	}
	return FeishuCallbackOutcome{AppID: target.AppID, User: feishu.User{
		Name: user.Name, AvatarURL: user.AvatarURL, OpenID: user.OpenID, UnionID: user.UnionID},
		Extra: map[string]string{"staff_id": staffID}}
}

// CredentialBotInput 是钉钉机器人（任务助手 / Agent）的直连请求：钉钉官方没有扫码创建应用的接口，
// 机器人对应的钉钉应用由管理员在开发者后台创建，这里填它的 AppKey / AppSecret。
type CredentialBotInput struct {
	RegistrationInput
	AppKey    string
	AppSecret string
}

// CreateCredentialBot 在发起人身份的事务里建立应用连接、机器人（与任务助手的内置 Agent）。校验（钉钉换 accessToken）必须已在事务外完成。
// plan 来自 RegistrationPrepare（已校验权限、冲突与 Runtime）。返回新机器人。
func (s Service) CreateCredentialBot(ctx context.Context, tx pgx.Tx, organizationID string, actor collaboration.Actor, plan RegistrationPlan,
	appKey, secret string) (FeishuBot, []AuditEntry, error) {
	if err := requireFeishuMember(ctx, tx, organizationID, actor); err != nil {
		return FeishuBot{}, nil, err
	}
	item := regItem{Purpose: plan.Purpose, SpaceID: plan.SpaceID, AgentID: plan.AgentID, RequestedBy: actor.UserID, Provider: s.prov(),
		Config: mustJSON(map[string]any{"runtime": json.RawMessage(plan.Config), "extra_prompt": plan.ExtraPrompt})}
	var botID string
	worker := &IMWorker{Service: s}
	// 保存点：insertBot 遇到唯一约束冲突会让事务进入中止状态，回滚到保存点后才能继续查询原因。
	sp, err := tx.Begin(ctx)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	failure, err := worker.insertBot(ctx, sp, organizationID, item, actor, selectionFor(organizationID, actor),
		feishu.RegistrationResult{ClientID: appKey, ClientSecret: secret}, feishu.Bot{}, "", &botID)
	if err != nil || failure != "" {
		_ = sp.Rollback(ctx)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return FeishuBot{}, nil, ErrFeishuAppInUse
		}
		return FeishuBot{}, nil, err
	}
	if failure == "" {
		if err := sp.Commit(ctx); err != nil {
			return FeishuBot{}, nil, err
		}
	}
	switch failure {
	case "":
	case RegErrAssistantExists:
		return FeishuBot{}, nil, ErrFeishuAssistantExists
	case RegErrAgentConnected:
		return FeishuBot{}, nil, ErrFeishuAgentConnected
	default:
		// insertBot 把「同一个 AppKey 已被连接」也归为保存失败：这里区分出来给调用方更明确的 409。
		var inUse bool
		if err := tx.QueryRow(ctx, forProv(`
			SELECT EXISTS (SELECT 1 FROM openfox.integration_connections
			                WHERE provider = 'feishu' AND scope = 'ORGANIZATION' AND lower(external_id) = lower($1) AND status <> 'DISCONNECTED')`,
			s.prov()), appKey).Scan(&inUse); err == nil && inUse {
			return FeishuBot{}, nil, ErrFeishuAppInUse
		}
		return FeishuBot{}, nil, ErrInvalidRequest
	}
	bot, err := s.loadBot(ctx, tx, organizationID, actor, botID)
	if err != nil {
		return FeishuBot{}, nil, err
	}
	audit := AuditEntry{Action: "integration.connection_created", ResourceID: bot.ConnectionID,
		Metadata: map[string]string{"source": s.prov(), "result": strings.ToLower(plan.Purpose)}}
	return bot, []AuditEntry{audit}, nil
}
