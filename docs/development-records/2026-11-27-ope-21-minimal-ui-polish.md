# OPE-21 全站简约大气视觉风格优化

2026-11-27，在本地 `master`（`1f4d3db`）上于独立工作树创建 `feat/ope-21-minimal-ui-polish` 分支，落实「全站深度视觉重塑 + 现代极简轻边框（1C + 2A + 3A）」方案。

## 目标与取舍

1. **三层表面层次与轻描边**：在浅色、深色、魔法蓝、经典深色四套主题下统一 `surface-app < surface-sidebar < surface-card` 视觉阶梯；弱化 `line-default / line-soft / line-subtle / line-floating` 描边对比并建立柔和的 `shadow-card / shadow-popover / shadow-dialog` 分层阴影，减少网格切割感。严格保留 `--color-surface-card` 主值（浅 `#ffffff` / 深 `#27272a`）与全站 10 个 `--density-*` CSS 变量，确保双密度与既有断言不受破坏。
2. **导航退后、内容居中**：`OrgLayout` 与 `SettingsLayout` 侧栏切至次级底色 `bg-surface-sidebar` + `ring-1 ring-line-floating`，导航项收口为 13px（`text-body-sm`）次要色退后，主画布以 `bg-surface-card shadow-card ring-1 ring-line-floating` 居中突出；7 处侧栏子组件同步对齐 `surface-sidebar-hover / surface-sidebar-active`。
3. **四项结构降噪与全站标题收口**：
   - 任务页 `SpaceTasksPage` 将默认视图来源与管理操作合并至 `min-h-11` 单层页头右侧，消除顶部双横线割裂。
   - 看板卡片 `Card` 按条件渲染元信息与底栏容器，消除无标签/无日期任务的多余底部留白。
   - 收件箱 `InboxPage` 将 `h-15` 大块 Tab 重构为与全站对齐的 `h-11` 轻量水平页签栏，完整保留 `role="tab"`、`aria-selected` 与分区计数。
   - 统一全站二级页面、引导/独立页（`JoinPage`、`InviteLinkPage`、`OAuthCompletePage`、`SharedSnapshotPage`）、个人设置子页（`GetStartedPage`、`NotificationsPage`、`InboxNotificationSettings`、`ShortcutsPage`、`TokensPage`）、我的任务与电脑管理（`MyTasksPage`、`ComputersPage`、`LocalComputerCard`、`AgentSettingsPages`）及全局/空间配置页（`CustomFieldsPage`、`LabelsPage`、`RelationTypesPage`、`StatusesPage`、`TaskTypesPage`、`EstimatesPage`、`IterationSettingsPage`、`TriageSettingsPage`）的 `<h1>` 字阶、卡片圆角与柔和描边，实现全站 61 个路由页面 100% 视觉对齐；桌面端离线页 `apps/desktop/renderer/offline.css` 与 `apps/web/index.html` `theme-color` 同步新版中性色阶。
4. **技术债清理**：清零全站 5 处无效表面类名（`bg-surface-default / panel / subtle`）、1 处无效 `bg-brand`（`DirectChat.tsx:106` 修复为 `bg-danger text-fg-inverse`）及 19 处原生 Tailwind `shadow-sm/md/lg`，未新增任何依赖、字体、图片或第三方素材。

## 验证

- **格式、类型检查与构建**：`pnpm lint`（Prettier + 全仓 TypeScript）与 `pnpm build` 全部通过。
- **单元测试（Vitest）**：首轮覆盖 51 个测试文件（519/519 通过，含新增 `Segmented` 与 `card.test.tsx`）；全站补齐 15 组低频/设置/引导子页后追加覆盖 `me/`、`onboarding/`、`my-tasks/`、`computers/`、`settings/`、`spaces/`、`integrations/`、`share/`、`agents/`、`automations/` 共 84 个测试文件，**956 / 956 全部通过**。
  - 注：`packages/app/src/shell/navMemory.test.tsx` 的「切换组织后记录不串」在基线 `master` 即存在既有失败（17/18 通过，参见 `2026-11-24-workspace-reading-chat.md`），本轮未改动导航记忆与外部点击关闭逻辑。
- **Playwright E2E（模拟 API）**：运行 `OPENFOX_WEB_PORT=4191 pnpm test:e2e density.spec.ts` 中英文深浅主题双密度同步与甘特背景色 2/2 全部通过；另首轮 `scroll-containment.spec.ts` 11/12 通过（唯一失败为基线提交 `d420ae3` 将窄屏文档目录改为抽屉所致，本轮未改动文档布局逻辑）。
- **实机浏览器视觉验证（模拟 API，DPR 2，CSS 像素归一化）**：覆盖 `light`、`dark`、`classic-dark`、`magic-blue` 四套主题，以及 1440×900 看板/收件箱/偏好设置/登录/开始使用/任务状态/加入组织、960×680 深色收件箱、680×860 窄屏看板共 12 组场景；全部 `overflowX: false`、0 条 console/page error，逐张目视核对五项视觉标准通过。

## 配置、数据与回退

本轮纯前端样式与组件表现优化，无新增依赖、无配置文件/接口/SQL/数据迁移变更；回退直接回滚本分支提交即可。

## 合并 develop 的冲突解决（提测后补记）

提测后 PR #33 与 develop（`a27bfa5`）出现 6 个文件、11 处冲突。采用把 `origin/develop` 合入任务分支的方式处理，不改写已推送的历史，不需要强推。develop 上先改过这些文件的任务是 OPE-11、OPE-14、OPE-17。

| 文件 | 冲突数 | 取舍 | 原因 |
|---|---|---|---|
| `packages/ui/src/tokens.css` | 5 | 全部取分支一侧 | 保住 app < sidebar < card 的三层层次；magic-blue 的 canvas 取 `#20233e`、classic-dark 的 canvas 取 `#141414`，避免与 card 同色；`of-board-lane` 取 `/55`，与泳道描边一起调整 |
| `packages/app/src/tasks/board/Card.tsx` | 1 | 两边合并 | 采用 develop 的 `data-board-title`（`useBoardPosition.ts` 靠它定位看板标题），并保留分支的 `leading-snug` |
| `packages/app/src/inbox/InboxPage.tsx` | 1 | 两边合并 | 保留分支的 `border-line-subtle` 与过渡动画，行高改用 develop 的窄宽度写法 `py-1.5 @3xl:h-[var(--density-table-row,40px)] @3xl:py-0` |
| `packages/app/src/tasks/board/Board.tsx` | 1 | 取分支一侧 | 泳道使用 `of-board-lane` 加 `border-line-subtle` |
| `packages/app/src/tasks/board/GroupedBoard.tsx` | 2 | 取分支一侧 | 同上 |
| `packages/app/src/me/PreferencesPage.tsx` | 1 | 取分支一侧 | 四主题缩略图色板含 `sidebar`，与三层层次一致 |

### 待人确认

- `of-board-lane` 透明度取 `/55`（分支），develop 为 `/60`。
- 浅色主题 `surface-canvas` 也在 `tokens.css` 的冲突块内（第 1 块），取分支值 `#fafafa`，develop 为 `#fcfcfd`；暗色主题 canvas `#232326` 两边一致，无冲突。

### 合并后验证

- `pnpm lint`（Prettier、ESLint、全仓类型检查）通过。
- `pnpm exec vitest run`：244 个文件、2396 项全部通过。
- `pnpm --filter @openfox/desktop test`：129/129 通过。
- `pnpm build` 通过。
- 本次未重跑 Playwright E2E，也没有重新做浏览器截图验收，交测试侧在合并结果上复测。


## 提测反馈修复 · 去除任务详情页标题与描述内嵌滑条（2026-11-27）

### 问题与根因

测试侧反馈任务详情页正文列右侧存在两条纵向堆叠的内部滚动条：
1. **任务描述 `<textarea>`**（`packages/app/src/tasks/detail/TaskDetailDrawer.tsx` `InlineText` `multiline` 分支）：原实现按硬换行符数量设置 `rows={Math.min(20, Math.max(3, draft.split("\n").length))}`，未考虑长段落自动折行且在 20 行封顶，导致折行正文底部被截断并在外层可滚动正文区内再叠一层内部滚动条（实测 OPE-19 示例 `clientHeight: 100px` / `scrollHeight: 160px`）。
2. **任务标题 `<textarea>`**（同组件非 `multiline` 分支）：虽已配置 `field-sizing-content`，但缺少 `overflow-hidden` 与不支持该特性的浏览器高度回退，在传统滚动条模式或亚像素舍入下仍可能绘制短滚动条轨道。

### 改动说明

- `packages/app/src/tasks/detail/TaskDetailDrawer.tsx`：
  - `InlineText` 标题与描述统一使用 `block w-full min-w-0 resize-none overflow-hidden field-sizing-content` 与 `rows={1}`，移除 `rows` 20 行封顶；多行描述保留 `min-h-[60px]`（保持空态 3 行点击热区），单行标题保留 `min-h-8`。
  - 增加 `supportsFieldSizingContent()`、`findScrollParent()` 与 `syncTextareaHeight()`：在支持 `field-sizing: content` 的引擎下零 JS 开销原生撑开；在不支持该特性的浏览器下自动按 `scrollHeight` 同步高度，测量前后保护外层滚动容器 `scrollTop` 避免跳滚，通过独立 `ResizeObserver` 仅在 `clientWidth` 变化时重算，并在 `scrollHeight === 0`（隐藏/jsdom）时清空内联高度。
- `packages/app/src/tasks/detail/inlineText.test.tsx`：新增 5 项单元测试，覆盖原生类名/属性、30 行超长描述不封顶、非支持环境回退与宽度变化重算、滚动位置保护、`scrollHeight === 0` 保护、交互保存/Esc 与停用空间只读态。

### 本轮验证

- `pnpm exec vitest run packages/app/src/tasks`：40 个测试文件、**360 / 360 全部通过**（含新增 `inlineText.test.tsx` 5/5）。
- `pnpm lint`（Prettier、ESLint、全仓 TypeScript）与 `pnpm build`：全部通过（0 错误）。
- `OPENFOX_WEB_PORT=4197 pnpm test:e2e tests/frontend/e2e/scroll-containment.spec.ts`（模拟 API）：**12 通过、1 跳过（实时后端用例）、0 失败**。
- **实机 Chromium 视觉与几何验收（模拟 API，DPR 2，禁用 overlay scrollbar 复现传统滑条场景）**：覆盖浅色 1440×900、深色 1440×900、窄屏 680×860、窄屏超长折行标题、抽屉模式、停用空间只读态、36 行超长描述、末尾空行、140 字符连续 URL、空描述与删减回缩共 11 组场景，全部满足 `overflowY === "hidden"`、`scrollbarW === 0`、`overflowDelta === 0`、`overflowX === false` 且 0 控制台/页面报错（OPE-19 描述完整撑开至 `160px`，不再截断）。
