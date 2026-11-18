# Slack 部署配置

本目录为 I15 的独立配置交付；功能仍在实施和受控验收中，尚未完成真实 Slack 安装或消息联调。模板不会创建应用、修改服务器或向 Slack 发送消息。

使用一个部署中央 App 连接各组织的 workspace、证明成员本人身份、发送通知及任务链接预览。Agent 和空间任务助手分别拥有自己的独立 App，由 OpenFox 的创建流程管理。不要将中央 App 的凭据当作 Agent 的凭据，也不要将创建成功视为安装或回调验证成功。

## 服务器配置

| 键 | 默认值 / 格式 | 用途与环境影响 |
| --- | --- | --- |
| `OPENFOX_SLACK_CLIENT_ID` | 空；中央 App 的 client ID | 组织 OAuth 和个人 OIDC 的机密 Web client |
| `OPENFOX_SLACK_CLIENT_SECRET_FILE` | 空；绝对路径、文件权限0600 | 中央 client secret；由服务进程身份读取，不使用明文 secret 环境变量 |
| `OPENFOX_SLACK_SIGNING_SECRET_FILE` | 空；绝对路径、文件权限0600 | 验证中央 Events API 原始请求，包括 URL challenge；不是 bot access token |
| `OPENFOX_SERVER_PUBLIC_URL` | 沿现有服务器配置；Slack 要求实际公开 HTTPS 地址 | 固定外部回调与任务链接的可信来源，不以请求 Host 或用户输入替代 |

沿用 OpenFox 现有集成加密密钥配置，不新增每 Agent 的环境变量。动态 App secrets 和 bot access/refresh 凭据由服务器密封保存；个人 OIDC 凭据仅用于本人身份验证，不作为长期个人 Token 保存。新中央配置三项缺省时 Slack 显示未配置，其他 IM 不受影响。具体启动校验结果需与最终服务端配置测试一起验收。

密钥文件由部署方通过现有秘密挂载机制提供，仅服务身份可读，不提交到仓库、截图、终端历史或工单。更换 client ID、signing secret 或 PublicURL 后，旧中央回调验证事实不再适用于新配置，需再次在 Slack 验证；不能仅恢复数据库布尔值跳过验证。

## 中央 App 的配置顺序（尚未执行）

1. 按本版迁移记录应用完整迁移链，再启动本版 API 与 worker；迁移145只在获授权的独占隔离库演练，本文不提供生产执行授权。
2. 在 Slack 开发平台为部署创建中央 App，选择普通 workspace，先取得该 App 的 client ID、client secret 和 signing secret。该动作由部署管理员执行；本次没有创建真实 App。先创建应用再配置其签名回调，避免服务端尚无 signing secret 时就要求 URL 验证。
3. 配置上表三个键、现有 PublicURL 与密封密钥。反向代理须把外部 `/api/v1/...` 转发到 Go handler 的 `/v1/...`，保留原始请求正文及 Slack 签名头。不要让登录页、重定向或代理重新编码 JSON 截断 Events API。
4. 复制[中央 manifest 模板](central-app-manifest.template.json)，将 `https://openfox.example.com` 的三个完整地址替换为实际 PublicURL，`features.unfurl_domains` 替换为该地址的主机名。若部署有合法路径前缀，完整地址要保留该前缀，unfurl 域名只保留主机名。示例域名不能用于实际安装。
5. 将配置更新到已创建的中央 App；在 Events API / App Manifest 页面手动验证 Request URL。OpenFox 只有收到当前配置 signing secret 验证通过的 challenge 才记录验证事实。所有组织安装共用该固定中央 URL，不为每个 workspace 覆盖它。[官方 manifest 合同](https://docs.slack.dev/reference/app-manifest/)
6. 配置两个 exact redirect：`${PUBLIC_URL}/api/v1/integrations/slack/callback` 和 `${PUBLIC_URL}/api/v1/integrations/slack/identity/callback`。组织安装与个人 OIDC 是两次独立流程；个人仅请求 `openid profile`，不在组织 bot scopes 中混入、不索取邮箱。采用服务器 client secret 流程，不把 App 改为 public client。[OAuth 安装](https://docs.slack.dev/authentication/installing-with-oauth/)、[Sign in with Slack](https://docs.slack.dev/authentication/sign-in-with-slack/)
7. 中央 bot scopes 仅包含模板中的9项。启用 token rotation、App Home 消息入口可写；关闭 org deploy 与 Socket Mode。不申请用户发言、`chat:write.public`、邮箱、文件、搜索或 admin scopes。若需要多个 workspace，部署方还须完成 Slack 的分发/工作区审批；OpenFox 的组织管理权不替代 Slack 审批。
8. 在 OpenFox 组织集成页明确发起组织安装，再由每名成员单独证明本人身份。组织安装者不会因此自动获得个人绑定。当前用户在通知页明确开启 Slack；私聊事件仍默认关闭。检查实际连接、回调和权限状态后，才进行另外获授权的真实消息验收。

`${PUBLIC_URL}` 是步骤说明中的实际 `OPENFOX_SERVER_PUBLIC_URL`，不是新环境变量。固定中央事件地址为 `${PUBLIC_URL}/api/v1/integrations/slack/events/central`；不要把 Go handler 的无 `/api` 路径直接填到使用当前反向代理的 Slack 配置。

## Agent 和任务助手 App

当前组织已安装中央 App、操作者已证明同 workspace 本人身份且具有目标管理权后，在 OpenFox 选择 Agent 或空间任务助手并创建独立 App。沿用同一 Agent/空间只能有一个 active IM bot 的跨平台限制；不要先在平台批量创建再尝试绕过本地唯一约束。

创建流程输入的是本次使用的 App Configuration refresh token。它拥有该用户在 workspace 下的应用管理能力，权限范围大于“创建这一个 App”；一次 rotate 会改变配置凭据链。OpenFox 校验返回 team/user，再短暂加密保存 access token（最多15分钟且不超过实际到期），不保留输入或新返回的配置 refresh token。不要把 bot Token、普通 OAuth refresh 或另一个人的配置凭据混用。[官方配置令牌说明](https://docs.slack.dev/app-manifests/configuring-apps-with-app-manifests/)

服务端生成固定 scopes、manifest 和回调，用户不能提交任意 manifest、API host 或执行命令。创建成功后，用页面给出的 App 设置入口手动验证动态 Request URL，并完成该 App 的 OAuth 安装；动态地址为 `${PUBLIC_URL}/api/v1/integrations/slack/events/{app UUID}`。URL验证和安装可分别完成，当前权限与两项条件都有效才启用。每个 App 使用自己的 signing secret，中央 URL 不能替代动态 URL。

配置 rotate 或 App create 的响应丢失时可能已发生远端操作。OpenFox 保留未知状态，不自动新建同目标 App，不承诺 export 可找回丢失的 client/signing secret。按页面诊断在 Slack 管理平台核对；删除孤立 App 或卸载真实安装需要操作者明确执行，不能将本地取消理解为远端删除。

首版支持普通人类成员、普通非共享公有/私有频道和单人 DM。频道内执行前需当前管理者确认群内可见回复及任务信息；当前本人绑定、频道成员身份、源对象读权和实际工具权限仍需逐次检查。关闭后重开使用新 epoch，旧排队输入/输出不会因此恢复。空间任务助手保留每 Run 成功创建5项、更新10项限制。

## 通知、事件与未知发送

中央订阅 `link_shared`、`message.im`、`app_uninstalled`、`tokens_revoked`。中央人类消息只用于引导返回 OpenFox/Agent App，不转为任务评论或任意 Runtime 执行。撤销事件中的 `tokens.bot` 是用户 ID 数组而不是秘密 Token；卸载与撤销事件可能乱序。[tokens_revoked](https://docs.slack.dev/reference/events/tokens_revoked/)、[app_uninstalled](https://docs.slack.dev/reference/events/app_uninstalled/)

通知沿用42个事件键、渠道细项、组静音和版本，发送前核对源对象当前权限，只发给已证明本人的单用户 DM。任务 rich unfurl 仅限配置域名、准确任务路径以及已明确授权公开的频道；无公开授权时保留普通链接，不暴露私有摘要。Slack 频道预览不具备“每位查看者独立 OpenFox ACL”的承诺。

回复按固定线程、确定分段和持久回执处理。正常返回 `ok=true` 且身份/频道/ts等字段有效才视为成功；超时、断连或部分成功类错误进入 UNKNOWN，不能因为用户未看到消息就自动重发。若平台不提供可核对的精确 own receipt，状态保持未知。人工重发可能重复，须由使用者明确确认；已经成功的段不重新发送。

历史上下文是有界读取，不承诺全频道记录。不同分发类型的 `history/replies` 配额不同，新商业分发且未 Marketplace 批准时可能每分钟1次、每页15条；不假定动态 Agent App 自动属于内部豁免。按实际 `Retry-After` 延迟，不能用更多 App 绕过限流。[Slack 当前限流说明](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps/)

## 验证与回退

本目录当前仅完成模板 JSON、字段/路径与官方协议核对；不代表本地 API/PG/worker、真实浏览器或真实 Slack 验收完成。批22最终交付将分别列实际受控测试和未验证平台范围。真实平台还需单独核对安装审批、手动 Request URL 验证、OIDC claims、refresh/revoke时序、metadata回声、实际限流和最终送达。

回退先停止 Slack 新接收/派发、断开本地安装并使旧代次失效，排空自有在途请求，清短期配置秘密，再回退应用/配置；保留任务、会话、来源及 UNKNOWN 审计和扩展 schema。停止本地不等于撤销 Slack 授权，也不能撤回已发送消息。远端 App 删除、卸载或 secret 轮换由管理员按明确目标执行；本次没有执行。只移除中央环境配置不会自动清理动态 App 或远端安装，不能用旧 worker 普通重试处理新 UNKNOWN 行。

本目录无 SQL、依赖或自动部署脚本。迁移145由后端专项记录交付；不要执行破坏性 down、删除用户数据或修改历史迁移 checksum。
