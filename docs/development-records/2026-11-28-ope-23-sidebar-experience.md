# OPE-23 侧边栏体验优化（浏览器、批注与工作区对齐 ChatGPT 客户端体感）

2026-11-28，在最新 `origin/master`（`06125c5`，v0.1.16）上于独立工作树 `/Users/puhuajie/OpenFox-worktrees/OPE-23-dev` 维护 `feat/ope-23-sidebar-experience` 分支，按确认方案（`1最新master + 2A + 3A`）完成双侧栏统一、桌面端内置浏览器性能与三模式批注体验、以及批注/评论/提案全链路融合。

## 目标与取舍

1. **左右双侧栏统一体感（对齐 ChatGPT 桌面端）**：
   - 左侧导航栏支持全局 `⌘\` / `Ctrl+\` 一键开合（窄屏切换抽屉），收敛收起态 Rail 空白占位并补齐折叠按钮静息可见性与快捷键提示；拖拽分隔条 `PanelResizeHandle` 增加悬停/聚焦/拖拽视觉指示线与**双击复原默认宽度**（同步覆盖左侧导航、右侧工作区与任务 AI 面板三处分隔条）；快捷键设置页 `ShortcutsPage` 新增「侧边栏与工作区」分组展示 `⌘\` / `⌘⇧\`。
   - 右侧工作区默认宽度由 `960px` 收口为 `640px`（`min: 280` / `max: 1280` / `remaining: 360`），在 1440px 主流屏下对话流与预览区比例更均衡；新增 `WorkspacePanelContext` 与统一顶栏入口 `WorkspaceHeaderButtons`（挂载至普通会话 `ConversationHeader`、小助手 `AssistantHeader` 与任务会话 `TaskConversationTools`），提供 macOS 桌面端一键直达浏览器、右侧工作区一键开关（`⌘⇧\` / `Ctrl+Shift+\` 开合并记忆上次标签）及待发送批注计数角标；小助手页「更多」菜单补齐产物、浏览器、已保存评论与修改提案入口。
   - 工作区标签条支持滚轮横向滚动、弹性宽度（`min-w-[100px] max-w-[min(184px,calc(100cqw-100px))]` 确保 280px 窄栏仍保留关闭按钮）与拖拽插入指示线；新标签启动页 `WorkspaceLauncher` 集成文档搜索、网页/产物/已保存评论/修改提案快捷入口与最近 5 条产物直达，整体 `max-w-sm` 与 `DocumentPicker` 左右边缘对齐。
   - 已保存评论（`COMMENTS_PANEL`）与修改提案（`PROPOSALS_PANEL`）在会话工作区内改为右侧内联标签页（保留「更多」菜单入口及独立无上下文时的 `Dialog` 兜底），避免弹窗遮挡原生浏览器视图导致浏览器闪烁隐藏；`WorkspaceComments` 新增「全部 / 未解决 / 已解决」分段筛选并接入文件定位 `requestFileLocation`。
2. **嵌入式浏览器性能修复与三模式批注体验**：
   - 修复对话流式输出期间浏览器高频重排卡顿：`BrowserPanel` 在 `layout` 监听中按取整 `{ visible, x, y, width, height }` 快照去重，仅可见性或几何真实变化时才发送 `layout` IPC；`apps/desktop/browser/annotations.cjs` 缓存 `overlayBounds / overlayVisible` 并校验 `window.contentView.children.at(-1) === overlay` 顶层顺序，消除重复 `setBounds` 与 `removeChildView/addChildView` 闪烁。
   - 桌面端 `apps/desktop/browser/host.cjs` 与 `preload.cjs` 增加两项受控增强：内置网页聚焦时透传 `⌘\` / `⌘⇧\` 至主窗口切换左右侧栏；新增受控 `open-external` 命令（仅读取主进程受信任 `group.tabs` 中当前标签 URL 并经 `normalizeURL` 校验 `http(s)` 后调用 `shell.openExternal`，且不解除 Agent 控制授权；`BrowserPanel` 失败仅局部告警而不标记浏览器会话崩溃）。
   - 浏览器批注开启后，在 52px 主工具栏下方按需展开三模式分段条（`元素 / 文字 / 区域` + 对应操作提示 + 退出按钮，不引入 `<select>` 下拉框），支持 `Esc` 退出（前端携带当前 `mode` 且 `annotations.cjs` 兼容 `enabled: false` 省略 `mode`）；地址栏补齐加载进度条（底部 2px 绝对定位、零高度位移）、聚焦清空按钮、复制网址与「在系统浏览器中打开」；Agent 共用浏览器时展示可见状态横幅与一键「接管」；空白页提供快捷键引导与聚焦按钮；`BrowserAnnotationEditor` 顶部展示选中模式与摘要微标签，`BrowserRebindEditor` 切换为设计系统 `Button`。
3. **批注、预览底栏与 CSV 全链路打磨**：
   - `drafts.ts` 与 `DraftAnnotations` 新增待发送文件/网页批注的就地编辑（`updateFileComment` / `updateBrowserComment`，保留 8 KiB/256 KiB 校验）、网页区域批注截图缩略图预览（规范释放 Object URL）与文件批注精确原位定位（`requestFileLocation`）。
   - `ArtifactPreview` 与 `NativeCanvas` 批注按钮统一为 `size="32" variant="round"` 视觉规格并显示当前文件/文档待发送批注角标（`bg-accent text-fg-inverse` 适配深浅四套主题对比度）。
   - `ArtifactReview` 移除嵌入模式下 CSV 表格硬编码 `max-h-80`，改为占满剩余高度并支持按目标单元格自动切页定位；`VisualReview`、`OfficePages` 与 `SpreadsheetReview` 底栏统一为胶囊缩放控件（`− / 百分比 / +`）与分段模式切换。

## 约束与边界

- 纯前端与 `apps/desktop/browser` 局部桌面壳增强，未修改 Go 后端（`contracts/`、`internal/`、`cmd/`、`db/`）、未新增 npm 依赖/字体/图片素材、未修改 `.pen` 文件或生成文件 `schema.gen.ts`。
- 严格保留 `ArtifactPanel` 常态顶栏 `44px + 52px = 96px` 几何契约及 `BrowserPanel` 非批注态单行 52px 工具栏结构。

## 验证

- **格式、静态检查与构建**：`pnpm lint`（Prettier、ESLint、7 个子包及 `tests/frontend` 全仓 TypeScript）与 `pnpm build` 全部通过（0 错误、0 警告）。
- **单元测试（Vitest）**：全仓 `vitest run` **245 个文件、2,409 项全部通过**（含新增/更新的 `BrowserPanel.test.tsx` 布局 IPC 去重/三模式批注/`Esc` 退出/`open-external` 失败处理、`artifacts.test.tsx` 顶栏工作区开关/`⌘⇧\`/启动页最近产物、`ArtifactReview.test.tsx` 待发送批注就地编辑、`WorkspaceComments.test.tsx` 状态筛选、`panelWidth.test.tsx` 双击复原宽度、`navLayout.test.tsx` `⌘\` 切换左侧栏、`conversationNavigation.test.tsx` 内联评论/提案标签）。
- **桌面端单元测试与真实 Electron 集成测试**：
  - `pnpm --filter @openfox/desktop test`：**129 / 129 全部通过**。
  - 真实 Electron 进程执行 `node --test --test-concurrency=1 browser/annotations.electron.test.cjs browser/host.electron.test.cjs`：**2 / 2 全部通过**（含原生叠层批注选区、布局保护、跨会话隔离与弹窗登录）。
- **Playwright E2E（模拟 API）**：运行 7 个相关规格（`artifact-preview.spec.ts`、`artifact-annotation-mode.spec.ts`、`artifact-text-review.spec.ts`、`artifact-html-review.spec.ts`、`workspace-canvas.spec.ts`、`artifact-regions.spec.ts`、`artifact-pdf-text.spec.ts`），**20 / 20 全部通过**（含 96px 几何契约、长会话滚动隔离与新增双侧栏/内联评论提案/快捷键端到端用例）。
- **实机浏览器视觉验收（模拟 API，Chrome DPR 2，CSS 像素归一化）**：覆盖深色 1440×900 分栏、浅色 1440×900 启动页、深色内联评论与待发送批注就地编辑、深色桌面端浏览器三模式批注与 Agent 控制横幅、1120×760 下 280px 窄侧栏、浅色快捷键设置页共 6 组场景；全部 `overflowX: false`、0 条页面异常，逐张目视核对五项标准并修正启动页 `max-w-md → max-w-sm` 右缘对齐后复检通过。

## 配置、数据与回退

无配置文件、接口契约、SQL 或持久化数据结构破坏性变更；工作区标签状态沿用既有本地存储键，新增 `comments` / `proposals` 路由参数向后无兼容负担；回退直接回滚本分支提交即可。
