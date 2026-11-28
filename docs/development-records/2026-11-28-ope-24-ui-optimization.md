# OPE-24 UI 与交互优化开发记录

2026-11-28，基于本地 `master`（`1f4d3db`）在独立工作树 `/Users/puhuajie/OpenFox-worktrees/OPE-24-dev` 创建 `feat/ope-24-ui-optimization` 分支，系统收口 `ui-change-list-2026-11-28` 全部 13 项 UI 与交互优化（P0 1～4、P1 5～9、P2 10～13）。

本次改动**全部在前端完成**，未修改 `contracts/`、`internal/`、`cmd/`、`db/`，未新增依赖、字体、图片，未修改 `packages/ui/src/tokens.css`。

---

## 一、提交考古与根因定位

需求清单依据 2026-11-24 的实机截图，对照 `master`（含 11-25 合入的 `d420ae3` 与 `5e36904`）逐项核对，避免重复改动或治标不治本：

1. **P0-1 未绑定电脑对话按钮文案**：已在 `d420ae3` 修复（`ComputerNotice.tsx` 按 `state.kind === "noComputer"` 区分「绑定电脑」与「更换电脑」）。本轮不重复修改业务代码，仅在真实浏览器中复验。
2. **P0-2 窄屏对话顶栏「设置」竖排**：`ConversationFrame.tsx` 的 `@container` 此前挂在包含产物面板的外层容器，导致产物面板打开或侧栏拉宽时聊天列宽度判断失真；同时「设置」按钮、任务编号徽标与状态徽标缺少 `whitespace-nowrap shrink-0`，面包屑 Agent 名称和会话切换器缺少完整 `title`。
3. **P0-3 任务列表行日期语义与优先级对齐**：`TaskList.tsx` 行内 `pointer-events-none` 阻断了内部元素的 `title` 悬停提示；`NONE` 优先级渲染的三个圆点容易与「更多」混淆且与信号条底线不齐；`ProposalCard.tsx` 复用 `DueChip` 展示 `start_date` 但未区分字段语义。
4. **P0-4 「正在保存到本机…」无完成态**：存在双重根因——`artifacts/params.ts` 在任意会话挂载时无条件向 `workspaceStore` 写入 `{ tabs: [], selected: null }` 空态，触发无意义的 IndexedDB 事务；`DraftStorageStatus` 未区分视图态同步与用户草稿写入，且保存结束后无完成态反馈。
5. **P1-6 列表/看板视图页签跳变**：`spaceDefaultView.ts` 的 `onChange` 误写为 `marked || Boolean(view && follow)`，当空间未配置 `default_view_id`（`view` 为空）时，用户仅切换一次布局就会把 `follow` 改成 `false`，导致来源栏从默认态突变为「个人视图」。

---

## 二、逐项实现摘要

### P0 缺陷修复
- **P0-1**：保持 `ComputerNotice.tsx` 现有正确实现，通过自动化与真实浏览器复验。
- **P0-2**：`ConversationFrame.tsx` 内层聊天列补充 `@container`；`ConversationHeader.tsx` 为「设置」按钮、任务编号按钮及 `StatusBadge` 补充 `shrink-0 whitespace-nowrap`，为 Agent/小队名（`max-w-[40%] truncate`）及 `SessionSwitcher` 补充 `title`。
- **P0-3 & P2-13**：
  - `parts.tsx` 的 `Pill` 与 `DueChip` 支持 `title`、`aria-label`、`label`、`checkOverdue`；修正 `ProposalCard.tsx` 传入开始日期标签并关闭逾期高亮。
  - `TaskList.tsx` 在展示任意日期列（开始/截止/创建/更新）时顶部展示紧凑对齐列头；右侧日期列采用等宽右对齐槽位，`pointer-events-auto` + 点击透传 `onOpen(task)`，并带完整 `title` 与 `aria-label`；`NONE` 优先级改为固定 `size-4` 基线横线占位（`role="img"` + `aria-label` + `title`）；任务 ID 列由 `w-20` 扩至 `w-24 tabular-nums` 并补 `title`（同步为 `Card.tsx`、`TaskTable.tsx` 的 ID 补 `title`）。
- **P0-4**：
  - `artifacts/params.ts`：未曾保存且处于 `{ tabs: [], selected: null }` 空态时跳过写入。
  - `workspaceStorage.ts`：新增 `hasDirtyContent` 只读属性，区分用户草稿/批注等真实内容键与 `workspace:` / `view:` / `browser:` 视图态键。
  - `WorkspacePersistence.tsx`：`DraftStorageStatus` 忽略视图态 `"saving"`（保留所有 `"failed"` / `"conflict"` 告警），真实内容保存完成后显示「已保存到本机」/「Saved locally」约 1.5s，最后 300ms 淡出隐藏（`motion-reduce:transition-none`）。

### P1 一致性与可发现性
- **P1-5 统一页面头部**：`InboxPage.tsx`、`MarketPage.tsx`、`MyTasksPage.tsx`、组织级 `AgentsPage.tsx`、`SpaceTasksPage.tsx` 统一采用 `min-h-11 border-b px-4` 单行基准页头、可见 `<h1>` 与 `size="28"` 右侧操作按钮。`InboxPage` 将 60px 宽灰块页签改为紧凑下划线 `tablist`（支持左右方向键与 Home/End 切换并保留未读角标）。`MarketPage` 页头仅加在全页容器，不影响 `MarketPickerDialog` 弹窗。
- **P1-6 统一视图来源栏与布局切换**：修复 `spaceDefaultView.ts` 默认跟随丢态问题；`SpaceTasksPage.tsx` 视图来源栏固定为清晰选中态的「空间默认（或空间默认：名称）/ 个人视图」按钮组 + 右侧独立的「空间默认视图」管理按钮；`TaskWorkspace.tsx` 工具栏新增外露 4 布局分段控件（`看板 / 列表 / 表格 / 甘特图`，`role="group"` + `aria-pressed`，窄容器收起文字保留图标与 `<Tooltip>`）。
- **P1-7 工具栏图标提示与空间新建任务入口**：`IconButton.tsx` 支持 `tooltip` 属性（接入 `<Tooltip>` 并关闭原生 `title` 避免双提示，支持鼠标悬停与键盘聚焦，展开浮层时自动隐藏）；`TaskWorkspace`、`Filters`、`DisplayMenu`、`BoardColumnJump` 全部接入；`SpaceTasksPage` 与 `MyTasksPage` 页头右侧补充带文字「新建任务」按钮。
- **P1-8 空状态补下一步**：`InboxPage` 空态补充来源说明与「前往我的任务 / 清除筛选 / 返回收件箱」按钮；`MarketBrowser` 空目录补充下一步说明与「前往组织 Agent」按钮（`skillsOnly` 弹窗仅保留指引文字）；`NewChatPopover.tsx` 导出 `EmptyAgentConversations`，在 `OrgLayout` 与 `ChatPanel` 的「暂无 Agent 会话」空态下补充原地「新建对话」入口。
- **P1-9 Agent 卡片运行状态**：`DirectoryCard.tsx` 向 `AgentAvatar` 传递 `offline={!entry.online}`；Agent 卡片使用设计令牌语义色展示「在线 · 电脑名 / 离线 · 电脑名 / 未绑定电脑」徽标；消除未绑定时「默认 · 你」，改为清晰的所有者/Runtime/模型信息；未绑定且有编辑权限时常驻可点击「绑定电脑」按钮直达 `runtime-settings`；小队卡片保持成员与所有者信息不误显电脑状态。

### P2 导航、去重与命名
- **P2-10 空间树整理**：移除首页侧栏 3 处零散行尾副文案（「AI 队友」「技能 · Agent 模板」「自主工作」），移入 `title` 悬停提示；`SpaceTaskNav` 为 `SubRow` 渲染已有图标并将触发器改名为「更多任务项…」；`SpaceMoreMenu` 触发器改名为「空间设置」并配齿轮图标。
- **P2-11 设置入口去重与对话入口分层**：保留左侧图标栏「全部设置」，移除首页侧栏「组织」下重复的「更多设置」链接；首页侧栏「对话」分节标题行补充「查看全部」（`aria-label="查看全部对话"`）弱链指向 `/o/:orgId/chat`。
- **P2-12 Agent 目录命名区分**：组织级侧栏改为「组织 Agent」（en: `Org Agents`），页面标题改为「组织 Agent 和小队」（en: `Organization agents and crews`）；空间内保留「Agents」，侧栏补充范围 `title`，空间页头面包屑补充「当前空间」范围徽标。

---

## 三、对话 5 处入口主次交互规范（P2-11）

为消除「对话」在产品内多处出现带来的心智混淆，明确以下主次层级与分工：

| 入口位置 | 角色定位 | 目标路由 / 动作 | 适用场景 |
| --- | --- | --- | --- |
| **1. 左侧图标栏「对话」** | **主入口（Primary）** | `/o/:orgId/chat` | 进入完整对话工作台（支持微信 / Slack / 多会话三种组织模式、搜索、置顶、归档与私聊管理）。 |
| **2. 首页侧栏「小助手」** | **专用入口（Dedicated）** | `/o/:orgId/assistant` | 组织级统一默认助手入口，面向日常问答与任务调度，不与普通 Agent 会话混排。 |
| **3. 首页侧栏「对话」分节** | **快捷回访与原地新建（Quick Resume & Create）** | 最近 5 条会话 + 分节标题右侧常驻「+」新建按钮（打开 `NewChatPopover`）；空态仅展示「暂无 Agent 会话」纯文字提示 | 在任务/收件箱工作流中快速切回最近会话或直接点「+」发起新会话；完整列表仍由左侧图标栏「对话」承接。 |
| **4. 对话模块内「新建对话」** | **模块内发起（In-module Creation）** | 打开 `NewChatPopover` 选人/选 Agent/选小队 | 在对话页签顶栏、主区空态或「暂无 Agent 会话」空态中原地发起新会话。 |
| **5. Agent 卡片 / 成员页 / 任务详情** | **上下文发起（Contextual Entry）** | `/o/:orgId/chat?agent=...` 或任务关联对话 | 在明确上下文对象（某个 Agent、某位成员或某条任务）时直接发起定向对话，无需重新选择对象。 |

---

## 四、已确认与自主决策记录

- **已由人工门确认（`1A 2A 3A 4A【进入开发】`）**：
  - 1A：任务页视图来源栏固定为「空间默认 / 个人视图」+ 右侧「空间默认视图」管理按钮，工具栏外露 4 布局分段切换。
  - 2A：统一移除 3 处零散侧栏行尾提示改为 `title`，区分「更多任务项…」与「空间设置」，`SpaceTaskNav` 子项补齐图标。
  - 3A：保留左侧图标栏「全部设置」，移除首页侧栏「组织」下重复的「更多设置」，账号菜单保留个人快捷项，首页侧栏「对话」补「查看全部」弱链并沉淀对话 5 入口规范文档。
  - 4A：全量执行 P0 + P1 + P2，中英文同步。
- **实现阶段自主技术决策**：
  1. 工具栏布局切换使用 `role="group"` + `aria-pressed`，避免与 `DisplayMenu` 弹窗内 `role="radio"` 产生语义和选择器冲突。
  2. 首页侧栏「对话」的弱链视觉文案为「查看全部」，无障碍名称为「查看全部对话」（`View all chats`），满足 WCAG 2.5.3 Label in Name 且与进阶指引的「查看全部」无读屏歧义。
  3. Agent 卡片未绑定电脑时的「绑定电脑」按钮置于状态行内常驻可点，避免放于悬停隐藏区导致鼠标移入或键盘 Tab 聚焦时按钮消失。
  4. Crew 卡片不显示单机电脑绑定状态，保留小队徽标与成员数语义。

---

## 五、配置、数据与回退

无新增依赖、无配置文件、无后端 API / 数据库 / 迁移变更。复用现有本地存储键与设计令牌。如需回退，直接回退本分支提交即可。

---

## 六、验证记录

- **格式与类型检查**：`pnpm lint`（`prettier --check` + `pnpm -r typecheck` + `tsc -p tests/frontend/tsconfig.json`）通过。
- **单元测试与组件集成测试**：
  - 受影响模块 Vitest 25 个文件（`packages/i18n/`、`packages/ui/src/components/`、`conversations`、`tasks`、`inbox`、`market`、`agents`、`shell`）共 **234 项全部通过**（含中英文键集与占位符一致性校验）。
  - 桌面端壳测试 `pnpm --filter @openfox/desktop test` 共 **128 项全部通过**。
- **构建**：`pnpm build` 通过。
- **真实浏览器 Playwright E2E（模拟 API）**：
  - `OPENFOX_WEB_PORT=4193 pnpm exec playwright test tests/frontend/e2e/ope24-ui-optimization.spec.ts`：**4/4 通过**（6.1s），覆盖浅色桌面（1440×900）、深色桌面（1440×900）、窄屏（680×800 与 960×640 + 480px 最宽侧栏）与英文界面（960×640），均验证根级与主内容区横向溢出为 0，并完成 14 张实机渲染截图目视核对。
  - `tests/frontend/e2e/scroll-containment.spec.ts`：11 项通过、1 项跳过（live fixture）、1 项基线既有失败（`DocsPage` 在 `d420ae3` 窄屏收起 `main aside` 后与旧断言不一致，本次未改动 `docs` 模块）。
- **未验证边界**：未在连接真实后端/数据库与 Electron 原生窗口环境中端到端验证 live fixture 用例。

---

## 七、第 2 轮返修（PR #37 复测反馈）

### 7.1 触发与范围

- 触发：综合复测结论 e9e2ea28、Web 报告 7e12ae85、macOS 报告 88d975cf；PR #37 的 head `d133880` 与 `develop` 冲突。
- 沿用分支 `feat/ope-24-ui-optimization` 与 PR #37，不新建 PR。只改前端，未动 `contracts/`、`internal/`、`cmd/`、`db/`。

### 7.2 实际改动

1. **合并 `origin/develop`（`b395002`）**：合并提交 `c79d5e8`，解决 5 处文本冲突：`AgentsPage.tsx`、`DirectoryCard.tsx`、`ConversationHeader.tsx`、`InboxPage.tsx`、`OrgLayout.tsx`。`develop` 另外改了 `agents/queries.ts`、`TaskPage.tsx`、`TaskConversations.tsx`（无文本冲突），返修在合并后的版本上进行。
2. **UI-02（P0）任务页 AI 面板长标题读不到全称**：`packages/app/src/tasks/TaskPage.tsx` 的 `TaskAiPanel` 标题 `span` 原本带 `truncate` 却没有 `title`。现在补上 `title`，内容为「Agent 或小队名 · 会话标题」，空值经 `filter(Boolean)` 过滤。
3. **UI-01 / UI-09 设备与目录在线状态不刷新**：
   - `packages/app/src/agents/queries.ts` 里的 `useDevices` 原来没有轮询，`computers/queries.ts` 的同名钩子才有 30 秒轮询，两份实现不一致。现在删除 `agents/queries.ts` 中的重复实现，改为 `export { useDevices } from "../computers/queries"`，只保留一份带 30 秒轮询的实现。
   - `useDirectory` 增加第三个参数 `poll`（默认 `false`），为 `true` 时按 `DIRECTORY_POLL_MS = 30_000` 轮询。只有展示在线状态的 `AgentsPage` 传 `true`，其他调用方不额外轮询。
   - `packages/app/src/conversations/ComputerNotice.tsx`：更正注释，原写「侧栏每 30 秒刷新同一份缓存」，实际由 `computers` 模块的 `useDevices` 轮询，与侧栏入口是否挂载无关。
4. **测试**：`tests/frontend/e2e/ope24-ui-optimization.spec.ts` 新增 `OPE-24 UI-02` 用例（提交 `e7ef590`），在 700px 与 900px 下打开关联任务的 AI 面板，断言页头 `title` 为「Agent 名称 · 会话标题」全文且根级与主区无横向溢出；为此在模拟数据中补了关联任务会话 `c2` 及任务详情相关接口。

### 7.3 需人拍板（未擅自实现，保持默认）

- Q5：AI 页头是否增加任务号、任务状态与独立设置。默认不加，本轮只修长标题提示。
- Q6：UI-08 侧栏「暂无 Agent 会话＋新建对话」空态保留、精简还是移除。默认保持现状。

### 7.4 配置、数据与回退

无新增依赖、无配置文件、无后端接口、数据库或迁移变更。回退方式：回退本轮的合并提交 `c79d5e8` 与测试提交 `e7ef590`。

### 7.5 验证（HEAD `e7ef590`，实际运行）

| 校验项 | 命令 | 结果 |
| --- | --- | --- |
| 前端单测 | `pnpm exec vitest run packages/app/src/conversations packages/app/src/agents packages/app/src/tasks` | 通过，114 文件、967/967 用例 |
| E2E（模拟 API） | `OPENFOX_WEB_PORT=4203 pnpm exec playwright test tests/frontend/e2e/ope24-ui-optimization.spec.ts` | 通过，5/5 |
| 格式与类型检查 | `pnpm lint`（`set -o pipefail`） | 通过，退出码 0 |
| 生产构建 | `pnpm build` | 构建完成并输出 `dist`，产物时间晚于本轮测试开始时间；未取到退出码 |

**真实后端复验（非模拟，部分覆盖）**：在本任务 worktree 的开发服务（代理到临时夹具后端）上，用夹具账号登录后打开组织 Agent 页，浅色 1440×900、深色 1440×900、浅色 680×800（DPR 均为 1）下，Agent 卡片显示「离线 · Unnamed device」，根级与主区横向溢出为 0；680px 下页头高 44px，三个按钮均 28px 高、不折行。截图见 `.hifox-task/evidence/live-*.png`。

**未复验项**：

- 真实页面上的「未绑定电脑」状态：夹具 Agent 绑定设备后，应用拒绝解绑（403）与置空电脑配置（400），未绕过校验；该状态仅由模拟 E2E 覆盖。
- 电脑由离线变在线后，驻留的会话横幅与目录卡片自动刷新：夹具设备没有心跳，无法在真实页面上让设备上线。
- UI-02 在真实页面的 700px / 900px 表现：夹具里没有任务和关联会话，仅有模拟 E2E 覆盖。
- Electron 960×640 窗口。

---

## 八、用户截图标注返修（侧栏「对话」分组标题「+」与空态去重）

### 8.1 触发与改动

按用户回传截图标注（`image-01a22253-05cb-7246-aeb9-f68ca1759465.png`）调整侧栏「对话」分节与空态：

1. **`packages/app/src/conversations/NewChatPopover.tsx`**：`NewChatButton` 新增 `variant="section"` 变体（`size-5`、`rounded-md`、`<Icon name="plus" size={14} />`，复用与「我的空间」分节 `+` 一致的侧栏语义令牌），删除不再使用的 `EmptyAgentConversations`。
2. **`packages/app/src/shell/OrgLayout.tsx`**：首页侧栏「对话」分组标题行右侧由「查看全部」链接替换为常驻 `<NewChatButton orgId={orgId} variant="section" className="mt-3 ml-auto" />`；空会话态恢复为纯文字「暂无 Agent 会话」，不再在空态下方重复渲染「新建对话」按钮。
3. **`packages/app/src/conversations/ChatPanel.tsx`**：对话侧栏已有顶栏 `<NewChatButton>`，空态同步恢复纯文字「暂无 Agent 会话」并移除无用 `orgId` 传参。
4. **`packages/i18n/src/messages/{zh-CN,en-US}.ts`**：同步清理不再使用的 `shell.chatViewAll` / `shell.chatViewAllLabel`。
5. **测试同步**：更新 `navLayout.test.tsx`、`conversationPolish.test.tsx` 与 `tests/frontend/e2e/ope24-ui-optimization.spec.ts`（断言「查看全部对话」移除、「+」按钮与「对话」标题同排且空态不重复放按钮）。

### 8.2 验证记录

| 校验项 | 实际命令 | 结果 |
| --- | --- | --- |
| 单元与组件测试 | `pnpm exec vitest run packages/i18n/ packages/app/src/shell/ packages/app/src/conversations/` | **通过**（58 文件全过） |
| 格式、Lint 与类型检查 | `pnpm lint` | **通过**（退出码 0） |
| Web 生产构建 | `pnpm build` | **通过**（退出码 0） |
| OPE-24 Playwright E2E（`SIMULATED`） | `OPENFOX_WEB_PORT=4203 OPE24_CAPTURE_DIR=.hifox-task/evidence pnpm exec playwright test tests/frontend/e2e/ope24-ui-optimization.spec.ts` | **通过**（5/5，7.6s） |
| 真实后端视觉与交互复验（4193 → 18094） | Playwright 连接 `http://127.0.0.1:4193`，覆盖 1440×900 浅色/深色与 680×800 窄屏抽屉（DPR=1） | **通过**（`dy=1px`，点击「+」正常弹出选人浮层，浅/深/窄屏溢出均为 `[0, 0]`） |
