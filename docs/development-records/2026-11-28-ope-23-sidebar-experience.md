# OPE-23 侧边栏体验优化（浏览器、批注与工作区对齐 ChatGPT 客户端体感）

2026-11-28，在最新 `origin/master`（`06125c5`，v0.1.16）上于独立工作树 `/Users/puhuajie/OpenFox-worktrees/OPE-23-dev` 维护 `feat/ope-23-sidebar-experience` 分支，按确认方案（`1最新master + 2A + 3A`）及二轮 UI 布局重构反馈完成双侧栏统一、Studio 级工作区与浏览器界面重设计、桌面端内置浏览器性能与三模式批注体验，以及批注/评论/提案全链路融合。

## 目标与取舍

1. **左右双侧栏统一体感与 44px 单行 Studio 顶栏（对齐 ChatGPT 桌面端 / Arc）**：
   - 左侧导航栏支持全局 `⌘\` / `Ctrl+\` 一键开合（窄屏切换抽屉），收敛收起态 Rail 空白占位并补齐折叠按钮静息可见性与快捷键提示；拖拽分隔条 `PanelResizeHandle` 增加悬停/聚焦/拖拽视觉指示线与**双击复原默认宽度**（同步覆盖左侧导航、右侧工作区与任务 AI 面板三处分隔条）；快捷键设置页 `ShortcutsPage` 新增「侧边栏与工作区」分组展示 `⌘\` / `⌘⇧\`。
   - 右侧工作区默认宽度由 `960px` 收口为 `640px`（`min: 280` / `max: 1280` / `remaining: 360`），建立 `bg-surface-canvas`（顶栏框架）与 `bg-surface-card`（内容画布）双层表面层次；通过 `WorkspaceToolbarTarget` Portal 与 `ResizeObserver` 宽度感知（`wideHeader = expanded || panelWidth >= 460`），在宽态下将文件预览（`ArtifactPreview`）与文档画布（`NativeCanvas`）的操作按钮直接合并进同一条 **44px 单行工作区顶栏**（与左侧 `ConversationHeader` 44px 像素对齐、消除重复文件名并释放 +52px 纵向阅读面积），在极窄宽度（`< 460px` 如 280px）平滑回落为 `40px` 紧凑次栏。
   - 新增 `WorkspacePanelContext` 与统一顶栏入口 `WorkspaceHeaderButtons`（挂载至普通会话 `ConversationHeader`、小助手 `AssistantHeader` 与任务会话 `TaskConversationTools`），提供 macOS 桌面端一键直达浏览器、右侧工作区一键开关（`⌘⇧\` / `Ctrl+Shift+\` 开合并记忆上次标签）及待发送批注计数角标；工作区已展开时自动隐藏对话顶栏冗余 `[◫]`（浏览器已选中时隐藏冗余 `[🌐]`），并将 `ConversationHeader` 的「设置」文字断点调整为 `@2xl:not-sr-only`，彻底避免分栏时对话标题被挤压截断。
   - 新标签启动页 `WorkspaceLauncher` 重构为居中 Studio 布局（`max-w-md` 卡片层 + `⌘⇧\` 键帽 + 撑满宽度的圆角阴影 `DocumentPicker` 搜索框 + 桌面端 **2×2** / Web 端 **3 列**均衡磁贴网格 + 分组描边式「最近产物」卡片列表）。
   - 已保存评论（`COMMENTS_PANEL`）与修改提案（`PROPOSALS_PANEL`）改为右侧内联标签页（保留「更多」菜单入口及独立无上下文时的 `Dialog` 兜底），避免弹窗遮挡原生浏览器视图；`WorkspaceComments` 重构为顶部固定状态筛选栏（`全部 / 未解决 / 已解决`）+ 自定义描边卡片（去除原生 `<details>` 折角三角，改用状态圆点、来源图标、状态胶囊与旋转箭头）并接入文件定位 `requestFileLocation`；`WorkspaceProposals` 统一为居中图标空态与状态高亮卡片。
2. **嵌入式浏览器性能修复、88px 双行顶栏与统一底部 Studio Dock**：
   - 修复对话流式输出期间浏览器高频重排卡顿：`BrowserPanel` 在 `layout` 监听中按取整 `{ visible, x, y, width, height }` 快照去重，仅可见性或几何真实变化时才发送 `layout` IPC；`apps/desktop/browser/annotations.cjs` 缓存 `overlayBounds / overlayVisible` 并校验 `window.contentView.children.at(-1) === overlay` 顶层顺序，消除重复 `setBounds` 与 `removeChildView/addChildView` 闪烁。
   - 桌面端 `apps/desktop/browser/host.cjs` 与 `preload.cjs` 增加两项受控增强：内置网页聚焦时透传 `⌘\` / `⌘⇧\` 至主窗口切换左右侧栏；新增受控 `open-external` 命令（仅读取主进程受信任 `group.tabs` 中当前标签 URL 并经 `normalizeURL` 校验 `http(s)` 后调用 `shell.openExternal`，且不解除 Agent 控制授权；`BrowserPanel` 失败仅局部告警而不标记浏览器会话崩溃）。
   - 浏览器界面摒弃旧版顶部最多 4 行（172px）堆叠栏：收口为 **Safari/Arc 风格双行 88px 顶栏**（44px 标签栏 + 44px 地址栏，内联聚焦清空/一键复制按钮与 1.5px 底部加载细线），并将批注三模式分段条（`元素 / 文字 / 区域` + 提示 + `Esc` 键帽 + `退出`）、Agent 共用状态胶囊与「接管」、以及重绑提示统一收口至视口下方的 **Bottom Studio Dock**（与 PDF/图片/Office/表格底栏形成一致空间语法，遵守 Electron `WebContentsView` 原生视图叠放约束，切换模式时网页顶部零垂直跳动）；重绘 `about:blank` 空白起始页；`BrowserAnnotationEditor` 展示选中模式与摘要微标签，`BrowserRebindEditor` 统一为设计系统 `Button`。
3. **批注、预览底栏与 CSV 全链路打磨**：
   - `drafts.ts` 与 `DraftAnnotations` 新增待发送文件/网页批注的就地编辑（`updateFileComment` / `updateBrowserComment`，保留 8 KiB/256 KiB 校验）、网页区域批注截图缩略图预览（规范释放 Object URL）与文件批注精确原位定位（`requestFileLocation`）；`DraftAnnotations` 视觉收敛为单层圆角附着托盘，与聊天输入框左右精准对齐。
   - `ArtifactPreview`、`NativeCanvas` 与 `BrowserPanel` 批注按钮统一带待发送批注角标（`bg-accent text-fg-inverse` 适配深浅四套主题对比度）。
   - `ArtifactReview` 移除嵌入模式下 CSV 表格硬编码 `max-h-80`，改为占满剩余高度并支持按目标单元格自动切页定位；`VisualReview`、`OfficePages` 与 `SpreadsheetReview` 底栏统一为胶囊缩放控件（`− / 百分比 / +`）与分段模式切换。

## 约束与边界

- 纯前端与 `apps/desktop/browser` 局部桌面壳增强，未修改 Go 后端（`contracts/`、`internal/`、`cmd/`、`db/`）、未新增 npm 依赖/字体/图片素材、未修改 `.pen` 文件或生成文件 `schema.gen.ts`。
- 宽态下 `ArtifactPanel` 单行顶栏严格锁定 `44px`（与对话顶栏齐平），窄态（`< 460px`）自动切换为 `44px + 40px` 双行布局，已同步更新 `artifact-preview.spec.ts` 几何断言。

## 验证

- **格式、静态检查与构建**：`pnpm lint`（Prettier、ESLint、7 个子包及 `tests/frontend` 全仓 TypeScript）与 `pnpm build` 全部通过（0 错误、0 警告）。
- **单元测试（Vitest）**：全仓 `vitest run` **245 个文件、2,409 项全部通过**（含 `BrowserPanel.test.tsx`、`artifacts.test.tsx`、`ArtifactReview.test.tsx`、`WorkspaceComments.test.tsx`、`panelWidth.test.tsx`、`navLayout.test.tsx`、`conversationNavigation.test.tsx`）。
- **桌面端单元测试与真实 Electron 集成测试**：
  - `pnpm --filter @openfox/desktop test`：**129 / 129 全部通过**。
  - 真实 Electron 进程执行 `node --test --test-concurrency=1 browser/annotations.electron.test.cjs browser/host.electron.test.cjs`：**2 / 2 全部通过**。
- **Playwright E2E（模拟 API）**：运行 7 个相关规格（`artifact-preview.spec.ts`、`artifact-annotation-mode.spec.ts`、`artifact-text-review.spec.ts`、`artifact-html-review.spec.ts`、`workspace-canvas.spec.ts`、`artifact-regions.spec.ts`、`artifact-pdf-text.spec.ts`），**20 / 20 全部通过**（含新 44px 单行顶栏几何契约与长会话滚动隔离）。
- **实机浏览器视觉验收（模拟 API，Chrome DPR 2，CSS 像素归一化）**：覆盖深色 1440×900 统一 44px 分栏、浅色 1440×900 Web 3 列启动页、深色内联评论卡片与单层待发送批注托盘、深色桌面端浏览器 88px 顶栏 + 底部三模式/Agent Studio Dock、1120×760 下 280px 极窄侧栏自动回落、浅色快捷键设置页、浅色 macOS 桌面 2×2 启动页、浅色浏览器空白起始页共 **8 组场景**；全部 `overflowX: false`、0 条页面异常，逐张目视核对通过。

## 配置、数据与回退

无配置文件、接口契约、SQL 或持久化数据结构破坏性变更；工作区标签状态沿用既有本地存储键，新增 `comments` / `proposals` 路由参数向后无兼容负担；回退直接回滚本分支提交即可。
