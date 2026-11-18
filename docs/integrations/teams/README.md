# Microsoft Teams（I16）

这是商业云 classic Azure Bot 的单租户接入。使用 Go 标准库 REST/JWT，不安装 Bot Framework、Teams 或 Graph SDK。范围为个人聊天、经空间管理者明确授权的群聊，以及 Graph 核验为 `standard` / `post` / 未归档的频道。shared/private 频道、预览线程、Agent365、Emulator、其他云、附件执行、全群消息历史、主动群发及通知渠道不在本交付范围。

## 准备与配置

OpenFox 没有新增全局环境变量、代理、CA、关闭签名验证或功能开关键。沿现有密封凭据 `Box` 和 `OPENFOX_SERVER_PUBLIC_URL`；公开地址必须 HTTPS 且无 userinfo/query/fragment。缺任一条件时目录显示未配置，不能连接。测试注入客户端只由代码中的服务器测试参数提供，不是部署配置。

每个 Bot 的输入是：固定 Tenant ID、Microsoft App ID、Client Secret、显示名称（1–30 Unicode码点）、目标 Agent 或空间任务助手；任务助手沿已有电脑/grant/Runtime/附加提示词配置。密钥最多4096字节，只在当前请求校验后密封保存，不从GET、应用包或日志返回。更新密钥只允许同一tenant/App及当前version；旧网络结果不能覆盖已移除或新版本。

应用包还要求开发者名称（1–32码点）、版本（如`1.0.0`）及实际由你提供的developer website/privacy/terms HTTPS URL。不会代写法律条款或以示例网址作为部署默认。版本表单初始`1.0.0`，其他输入无默认秘密。一个Agent或空间任务助手沿既有约束只能有一个有效IM Bot；当前active App ID也不能跨OpenFox组织复用。

1. 按 [Microsoft 注册说明](https://learn.microsoft.com/en-us/microsoftteams/platform/concepts/authentication/auth-oauth-card)准备商业云single-tenant classic Bot、App ID与凭据，在Azure Bot中启用Teams渠道。single-tenant凭据本身不证明来访消息属于该tenant；OpenFox仍验证每个入站tenant/recipient。
2. 在OpenFox Teams详情选择可管理目标，输入凭据和应用包信息，点击“核验并保存”。这只调用Connector OAuth校验凭据并保存本地连接，不安装远端应用。
3. 将详情显示的`/api/v1/hooks/teams/{botID}/activities`完整HTTPS消息终结点配置到Azure Bot。botID是公开本地路由标识；安全性来自Bearer RS256验证，不依赖隐藏URL。
4. 下载ZIP并按 [官方应用包要求](https://learn.microsoft.com/en-us/microsoftteams/platform/concepts/build-and-test/apps-package)在Teams中手动发布/安装。ZIP只有根目录`manifest.json`、192px`color.png`和32px透明白色`outline.png`，使用OpenFox图标、manifest1.30、一个bot和personal/groupChat/team三个scope。
5. 频道使用最小Application RSC `ChannelSettings.Read.Group`，需要管理员在安装目标team时同意；只读取频道设置，不读取Graph消息历史。缺授权、缺字段或不支持类型都会明确拒绝执行，不能当成标准频道。[Graph频道接口](https://learn.microsoft.com/en-us/graph/api/channel-get?view=graph-rest-1.0)
6. 本人登录OpenFox生成一次性绑定指令，只发给该Bot个人聊天。24随机字节、10分钟、仅哈希入库、单次消费；格式化XML文字正常还原。Connector成员AAD核验成功后即绑定，网页刷新显示结果，不需要第二次网页确认。群里提交、指令中夹带正文及过期指令不进入普通消息或Runtime。
7. 首个经认证群/频道活动只发现范围，默认关闭。空间管理者打开“群授权与送达记录”，明确同意对群/频道成员公开回复后开启；只有已绑定且仍有OpenFox权限的人真正@Bot才触发。重新开启会创建新授权代次及会话，旧输入/Run不会恢复。

详情分别展示凭据校验时间、已认证入站时间与已确认回复时间。下载包或OAuth成功均不等于安装、连通、送达成功。

## 执行与故障语义

- 入站RS256固定Connector元数据/JWKS、issuer、App audience、有效期、msteams endorsement及签名serviceURL；商业固定主机、TLS、禁止重定向或HTTP降级。JWT/完整Activity/附件/秘密不复制进持久路由。
- Opaque conversation/activity/from ID保真；现有短列用明确元组哈希，不放宽其他provider限制，不切分`conversation.id`猜频道根。
- 实际`conversation_inputs`保留来源、实际actor/member版本、身份和scope代次。Web与Teams输入分Run；只有全REMOTE的终态结果外发，选该Run最后一个真实输入的原消息引用。明确拒绝仅发送固定拒绝文本并关联确切被拒输入，普通SYSTEM/social/proposal消息没有外发资格。
- 权限在入队、PostMessage、准入、执行/工具与发送前重验。保留原5次创建、10次更新、20条/32KiB上下文等限制。没有自动将Teams旁观者加入OpenFox组织。
- 每次终态回复是最多8000 Unicode码点的纯文本；超长显示摘要与OpenFox完整内容链接。只POST原conversation/activity，不因404退化到频道顶层。
- 收到明确429只按可表示的完整Retry-After并有界重试；缺失/无效/溢出等待停止自动排程。写后断连、5xx、不完整成功、过期SENDING等按既有`FAILED/delivery_unknown`保存，UI显示未知并停止重发。没有声称平台exactly-once，也没有人工盲重发按钮。
- 发送前撤权：不发POST。POST已发后撤权：不能撤回请求，迟到成功只补记送达事实，不恢复身份/群权限。必要授权锁不跨网络请求持有。

## 迁移与回退

按迁移清单顺序执行至 156，再应用 [`000157_teams.sql`](../../../db/migrations/000157_teams.sql)。SHA256：`21f32f49e4fe6389841aa788b87da17758585b1d77626498348b0ba426a36780`。没有数据回填或破坏性迁移。新增三张专属表、可空typed context/事件摘要列、租户/唯一/RLS/不可变源保护与受限路由resolver；157复制累计reply trigger时保留Discord排除及原企微逻辑，再排除Teams，避免普通触发器自动外发。

迁移须先于新二进制/worker；使用现有迁移CLI，明确`migrate --dir <累计迁移目录> --apply --database-url-file <私有DSN文件>`。第二次应用应为0，账本应匹配文件SHA。只在新的隔离测试库演练；本交付未操作生产。

回退先通过Teams专属“移除本地连接”停止新输入/回复并清除密钥/当前身份/群授权，等待当前请求最多受控超时完成并记录未知结果，再停止Teams入口/worker并回退应用。保留157列/表、原消息/任务、不可变输入及UNKNOWN事实；不删账本、不改已应用SQL、不把旧身份/队列重置为active，也不重新发送未知请求。不要使用通用连接断开路径绕过专属队列围栏。若需重新安装，由用户重新显式创建连接/绑定/授权；远端应用仍须用户自行处理，OpenFox不自动卸载。

## 验证边界

受控验收使用真实OpenFox UI/API、session/CSRF、独占PostgreSQL、RS256/JWKS、系统校验的自有TLS、全局worker扫描和真实Materialize/Finalize；只有模型返回是合成值，未调用付费模型。真实Microsoft租户、Azure注册、应用商店/管理员发布政策、RSC同意、公网入站及真人送达均未验证。官方draft04声明schema使用现成锁定Ajv8作兼容约束核验，未安装draft04引擎，不能据此宣称Teams发布认证完成。
