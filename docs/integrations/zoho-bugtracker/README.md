# Zoho BugTracker（US，只读一次性导入）

此适配使用 Zoho **BugTracker REST** 文档公开的合同，不把 Zoho Projects V3 或 Projects SDK 字段视作 BugTracker 合同。支持组织 Portal 授权、本人身份绑定、选定 active 项目的 open/closed Bug 一次性导入。仅读取标题、来源 ID、状态、severity、reporter_id 和创建时间。正文留空表示未导入；不支持远端负责人、评论、附件、持续同步或业务写回。

**生命周期限制：** Zoho 的共用 REST 概览含弃用提示，Projects 有自己的 V2 迁移公告。当前证据不能给出 BugTracker 专属停用日期，也不能保证长期可用或所有租户可用。真实 US 租户 OAuth/权限/读取尚未验证；受控协议测试不能替代平台验收。接口失效时任务停止并给出错误，已导入副本保留。

## 部署配置

无新增依赖或功能开关。唯一新 OpenFox 环境键是 `OPENFOX_ZOHO_OAUTH_APPS_FILE`，默认空（未配置，UI 禁止开始授权）。非空时必须为绝对路径、非符号链接、普通文件，权限严格 `0600`，最大 16 KiB。内容严格 JSON，无额外字段，组织和个人应用 ID 必须不同：

```json
{
  "region": "US",
  "organization": {"client_id": "organization-client-id", "client_secret": "organization-client-secret"},
  "identity": {"client_id": "identity-client-id", "client_secret": "identity-client-secret"}
}
```

两个应用在 Zoho 注册相同精确回调：`<OPENFOX_SERVER_PUBLIC_URL>/api/v1/integrations/zoho_bugtracker/callback`。沿用现有 Public URL、登录会话和数据库加密主密钥配置。生产回调使用 HTTPS。固定 Accounts/REST 主机分别为 `accounts.zoho.com` 和 `bugtracker.zoho.com`，不接受租户输入地址，不支持其他数据中心。

- 组织应用请求 `ZohoBugTracker.portals.READ`、`projects.READ`、`users.READ`、`bugs.READ`（每个完整前缀均为 `ZohoBugTracker.`），offline 授权；组织 OWNER/ADMIN 必须使用远端 Portal 的启用管理员完成确认。只保存组织连接的密封 access/refresh token。
- 个人应用仅请求 `ZohoBugTracker.portals.READ`，online 授权。用 `login_id` 与组织应用读取的同 Portal 启用用户 ID、本人邮箱核验；个人 access/refresh token 不写数据库，包括待确认阶段。本人最终确认后才绑定。
- state 哈希、PKCE S256、10 分钟会话、一次消费、本人确认与父连接版本校验；令牌、密钥和原始错误正文不进入日志、URL或前端存储。未完成的组织待确认密文由现有 OAuth 维护循环清理，不新增 worker。
- 普通刷新只改变凭据版本；重授权、断开改变连接/凭据代次。非轮换 refresh token 保留；GET 401 最多协调刷新并重试一次；403 不循环刷新。共享预算每两秒最多准入一个 HTTP，请求等待不占数据库事务；429 的有效 Retry-After 不缩短，缺省按官方锁定窗口 30 分钟，非法或不可表示值停止自动调度。

应用文件变更或本地解绑不自动撤销 Zoho 授权。重新授权由用户明确完成；远端授权管理需在 Zoho 手动进行。

## 导入及权限

每次选一个项目，range=100、index 从 1 顺序抓取 open 和 closed；单草稿最多 5,000 行、10 MiB、单格 64 KiB、160 次读取、两小时总期限，每次现有 import worker 最多五页。分页时来源可能变化，因此不宣称远端事务快照。重复来源 ID 内容冲突、重复页无进展、超限或畸形响应会停止，不能截断后宣称完成。全部读取完成才允许映射和创建任务。

类型统一为 Bug，用户明确选择空间默认类型或现有类型；状态和 severity 按稳定来源 ID 显式映射，名称仅展示。维持组织现有优先级档数，不自动升档。没有 severity 时为 NONE。closed 不推造 completed_at。

来源报告人按 `reporter_id` 保存，界面明确显示“来源报告人/映射成员”。新任务 `task.imported.safe_diff` 保存原始十进制 `source_reporter_id`、`source_reporter_region=US`、报告人名称及导入时选定的成员或外部账号。**导入操作者保持真实用户，不伪造 task.created 或改写本地创建者。** 自动绑定建议在执行时再次核验当前父连接、本人绑定、邮箱及成员/空间权限；个人解绑仅影响后续新导入，历史映射不改。人工映射也复核目标当前有效性。来源身份缺失或失效时保留来源事实，不猜为负责人。

源实例为 Portal＋project 的稳定摘要，外部 ID 沿既有 import_mappings 保留。重复导入默认跳过已有任务。没有主动开启的写回、Hook 或轮询同步。组织断开、配置移除、权限丢失后抓取停止；副本与历史保留。

## SQL、执行及回退

迁移 `000152_zoho_bugtracker.sql`：扩充 import source CHECK；增加组织授权 runtime、一次性 crawl、私有共享读取预算；增加个人连接唯一/shape 约束和 RLS；扩展已有 OAuth 维护扫描以清理过期 Zoho 会话。无历史回填。只在新独占测试库演练，未操作生产。

执行顺序：备份/核对目标库 → 按仓库迁移命令顺序应用全部待执行迁移（含并行工作包 151 后再 152）→ 校验账本 SHA 和重复执行零变更 → 部署匹配代码 → 写入应用文件并配置环境键 → 注册两个精确回调 → 用户明确授权。实际平台授权与部署不属于本次本地交付。

保留数据回退：停止当前进程/worker，移除新配置键，恢复此前应用代码；保留 152 表、列、约束及导入记录，不运行破坏性 down SQL。需要停止本地连接时在当前版本先使用管理页“断开”，清除密封凭据并使排队来源失效。撤销某次导入仅用既有导入撤回功能及其权限/冲突检查，不自动删除远端数据。恢复时先还原应用文件，再由用户明确重授权/新建导入。迁移账本不得回写或用旧 SQL 覆盖。

## 官方依据

- [BugTracker OAuth](https://www.zoho.com/projects/help/rest-api/bugtracker-api.html)：OAuth 服务与 online/offline 授权。
- [Portal](https://www.zoho.com/projects/help/rest-api/bugtracker-portal-api.html)：login_id、Portal ID 和角色。
- [用户](https://www.zoho.com/projects/help/rest-api/bugtracker-users-api.html)：active、用户 ID、邮箱、角色。
- [项目](https://www.zoho.com/projects/help/rest-api/bugtracker-projects-api.html)：项目 ID、名称、状态与分页。
- [Bug](https://www.zoho.com/projects/help/rest-api/bugtracker-bugs-api.html)：已公开 GET 字段和 open/closed 查询。
- [REST 概览](https://www.zoho.com/projects/help/rest-api/zohoprojectsapi.html)：BugTracker 域名、分页/限流与弃用提示。
- [Zoho 服务端 OAuth](https://www.zoho.com/developer/oauth/web-server-apps/overview.html)、[刷新](https://www.zoho.com/developer/oauth/web-server-apps/refresh-access-token.html)、[PKCE](https://www.zoho.com/developer/oauth/mobile-and-desktop-apps/get-authorization-code.html)：OAuth/令牌协议依据；具体应用实际兼容性仍需真实租户验收。
