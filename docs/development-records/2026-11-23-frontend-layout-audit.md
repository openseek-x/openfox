# 前端窗口与滚动检查 · 2026-11-23

## 问题与检查方法

市场页可以滚过整个应用，露出大块空白。侧栏进阶指引里表示「已完成」的屏幕阅读器文字使用绝对定位，原滚动区没有为它提供定位基准。另一个独立问题是市场卡片给两行摘要预留的固定高度大于两个实际行高，露出了第三行的上缘。

检查使用本地 Go API、临时 PostgreSQL、简体中文账号和 Chromium。对照 Electron 窗口的最小尺寸 960×640，并在 1440×900 重测。侧栏分别取默认、最宽 480 像素和折叠。分别测量文档根节点宽高、主内容区横向滚动宽度及多行省略文字的实际行高。主内容区和表格自身可正常滚动。单独检查键盘操作、分页、浮层边界和长数据，避免被空状态掩盖。

| 检查范围 | 实际场景 |
| --- | --- |
| 工作区 | 收件箱、我的任务、会话和私聊、助手、成员、Agent 目录与抽屉、市场 |
| 空间 | 任务看板、列表、表格、甘特图、任务详情、分诊、回收站、迭代和专项列表与详情、视图创建与编辑、自动化、文档编辑、技能、空间 Agent |
| 设置 | 个人偏好、指引、通知、账号绑定、组织管理、电脑和 Agent、空间各设置页与任务类型详情、34 个集成详情入口 |
| 独立入口 | 登录、创建组织入口、加入组织、无效邀请、无令牌分享、缺少授权结果的回调和未知路由 |
| 容器与交互 | 用键盘拉宽和折叠侧栏、打开搜索和任务优先级浮层、抽屉开着时调整窗口、折叠和恢复文档目录、市场加载第二页并切换模板 |

## 定位和修复

下表是在已有市场整窗修复的基础上，960×640、侧栏 480 像素、常规测试数据下测得的主内容区多余横向宽度。

| 位置 | 修复前 | 修复 |
| --- | ---: | --- |
| 空间甘特图 | 57 px | 工具栏可换行，时间轴保留独立滚动 |
| 空间文档详情 | 19 px | 目录宽度受当前面板约束，保留编辑区和标题操作 |
| 空间 Agent | 8 px | 长空间名可截断，目录按可用宽度排一到三列 |
| 偏好设置 | 88 px | 边框色板按设置卡宽度换行 |
| 开始使用 | 117 px | 窄内容列上下排布，较宽时恢复双栏和固定的进度区 |
| 账号绑定 | 148 px | 状态区域可换行，给服务名和用途说明留出宽度 |
| 电脑、Agent 管理 | 13 / 34 px | 共享和私有表格各自横向滚动 |
| 空间管理 | 219 px | 列表保留可读列宽，横向滚动限于列表 |
| 空间任务标签 | 239 px | 外层占满可用宽度，筛选工具可换行，滚动保留在表格；默认侧栏下原本也多出 19 px |

此前已修复市场技能和模板摘要、侧栏指引、表头的隐藏文字、末行迭代菜单和添加自定义字段时的长任务类型列表。摘要预留两个实际行高，隐藏说明由相应滚动区或表头定位；迭代操作按钮直接提供无障碍名称。

另外补测 31 个组织时，切换菜单原本高出窗口，上沿落在 -505 px。共享菜单现在限制在视口内，方向键、Home 和 End 可访问首尾，内部内容可滚动。在最窄面板展示四成员的市场精选包时，三个包名原本均为零宽。精选包现随面板宽度调整列数。按空间名的 32 字符上限验证中英文长名称时，还定位到任务、迭代、专项、视图和技能页的页头。空间名可截断并保留完整提示，模块名称保留在页头。

英文界面另有四处控件溢出。组织 Agent 目录的操作区多出 10 px，迭代工具栏多出 51 px，个人简介多出 12 px，云效集成的操作按钮多出 74 px。操作区改为可换行，简介按所在列的宽度截断并保留编辑按钮。Beta 设置入口在测试账号上跳转到个人资料，计入同一问题。

## 验证与范围

主矩阵共 100 个页面或路由状态 × 2 个窗口尺寸 × 3 个侧栏状态 × 2 种界面语言。语言通过浏览器夹具切换账号偏好响应，页面数据来自本地真实后端。1,200 次测量的文档根节点额外滚动范围与主内容区多余横向宽度均为零，未记录到未处理的浏览器异常或失败的 API 响应。另对 29 个空间路由使用 32 个宽拉丁字符和 32 个汉字，测量页头和滚动边界。

34 个集成入口使用本地配置。真实市场目录为空，长目录分页、技能、模板、精选包通过浏览器请求夹具另测。Electron 45 的隔离窗口使用项目 preload 和同一个本地渲染页面，在 macOS 上验证了市场、偏好、空间管理、甘特图和文档五处。没有启动正式客户端后台服务。

原有 Vitest 组件断言主要覆盖数据和行为，jsdom 不计算真实文本和滚动几何。Playwright 的默认窗口为 1440×900。此次在 tests/frontend/e2e/scroll-containment.spec.ts 保留 960×640 回归，使用真实浏览器断言滚动归属、两行摘要、末行操作、菜单首尾焦点和宽窄切换。

| 验证 | 结果 |
| --- | --- |
| 类型与构建 | `pnpm typecheck`、`pnpm build` 通过 |
| Vitest | 首轮四个 worker 并发时一个任务移动用例等待按钮超时。该文件单跑 25/25，全套三 worker 重跑 2,300/2,300。随后调整页头和文档目录，受影响模块重跑 460/460 |
| 桌面单元测试 | `pnpm --filter @openfox/desktop test`，122/122 |
| Playwright | 常规套件执行时 62 项通过、58 项因前置条件跳过。最终专项使用临时后端运行 `pnpm exec playwright test scroll-containment.spec.ts`，13/13 通过，其中一项通过真实登录遍历六个设置页 |
| 市场交互 | 本地构造 60 个技能，滚到第二页末项，切换技能和模板、切换进阶指引并调整窗口尺寸，标题栏固定，根节点无额外滚动 |

复测可运行 `pnpm exec playwright test scroll-containment.spec.ts`。真实后端用例沿用项目的 `tests/frontend/run-live.sh` 临时数据库流程，常规用例使用测试内的固定数据。

本次只改前端布局、浏览器测试和本记录。无产品配置、API、SQL 或数据库迁移改动。需要撤回时可按页面还原布局代码，共享菜单的组件类和对应 CSS 应一起还原。未验证第三方账号真实授权、外部市场内容、原生后台服务、打包签名或发布。

## 本地测试数据

临时集群按顺序执行已有的 db/migrations 中 172 个 SQL 文件、tests/integration/db/fixture.sql、tests/frontend/fixture.sql，以及 tests/frontend 中的 hifox-batch3-fixture.sql 和 hifox-batch6-fixture.sql。为进入真实详情页，在同一临时数据库额外执行如下事务。应用数据库无需执行这些语句。

```sql
\set org '00000000-0000-0000-0000-0000000000a1'
\set space '00000000-0000-0000-0000-0000000000aa'
\set usr '00000000-0000-0000-0000-000000000001'
\set sample '00000000-0000-4000-8000-000000000099'
BEGIN;
UPDATE openfox.spaces SET iterations_enabled=true, iteration_mode='manual', triage_enabled=true WHERE id=:'space';
INSERT INTO openfox.projects(organization_id,id,name,created_by) VALUES(:'org',:'sample','布局专项',:'usr');
INSERT INTO openfox.project_spaces(organization_id,project_id,space_id,is_primary) VALUES(:'org',:'sample',:'space',true);
INSERT INTO openfox.iterations(organization_id,space_id,id,number,name,starts_at,ends_at) VALUES(:'org',:'space',:'sample',1,'布局迭代',now(),now()+interval '7 days');
INSERT INTO openfox.views(organization_id,space_id,id,name,created_by) VALUES(:'org',:'space',:'sample','布局视图',:'usr');
INSERT INTO openfox.doc_nodes(organization_id,space_id,id,kind,title,body,owner_user_id,created_by,updated_by) VALUES(:'org',:'space',:'sample','doc','布局文档',repeat(E'布局检查正文\n\n',80),:'usr',:'usr',:'usr');
INSERT INTO openfox.automations(organization_id,space_id,id,title,prompt,assignee_kind,assignee_id,created_by,enabled) VALUES(:'org',:'space',:'sample','布局自动化','测试内容','AGENT','00000000-0000-0000-0000-0000000001a8',:'usr',false);
INSERT INTO openfox.direct_chats(organization_id,id,user_low,user_high) VALUES(:'org',:'sample',:'usr','00000000-0000-0000-0000-0000000000c3');
COMMIT;
```
