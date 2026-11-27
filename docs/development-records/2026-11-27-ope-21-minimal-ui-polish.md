# OPE-21 全站简约大气视觉风格优化

2026-11-27，在本地 `master`（`1f4d3db`）上于独立工作树创建 `feat/ope-21-minimal-ui-polish` 分支，落实「全站深度视觉重塑 + 现代极简轻边框（1C + 2A + 3A）」方案。

## 目标与取舍

1. **三层表面层次与轻描边**：在浅色、深色、魔法蓝、经典深色四套主题下统一 `surface-app < surface-sidebar < surface-card` 视觉阶梯；弱化 `line-default / line-soft / line-subtle / line-floating` 描边对比并建立柔和的 `shadow-card / shadow-popover / shadow-dialog` 分层阴影，减少网格切割感。严格保留 `--color-surface-card` 主值（浅 `#ffffff` / 深 `#27272a`）与全站 10 个 `--density-*` CSS 变量，确保双密度与既有断言不受破坏。
2. **导航退后、内容居中**：`OrgLayout` 与 `SettingsLayout` 侧栏切至次级底色 `bg-surface-sidebar` + `ring-1 ring-line-floating`，导航项收口为 13px（`text-body-sm`）次要色退后，主画布以 `bg-surface-card shadow-card ring-1 ring-line-floating` 居中突出；7 处侧栏子组件同步对齐 `surface-sidebar-hover / surface-sidebar-active`。
3. **四项结构降噪与全站标题收口**：
   - 任务页 `SpaceTasksPage` 将默认视图来源与管理操作合并至 `min-h-11` 单层页头右侧，消除顶部双横线割裂。
   - 看板卡片 `Card` 按条件渲染元信息与底栏容器，消除无标签/无日期任务的多余底部留白。
   - 收件箱 `InboxPage` 将 `h-15` 大块 Tab 重构为与全站对齐的 `h-11` 轻量水平页签栏，完整保留 `role="tab"`、`aria-selected` 与分区计数。
   - 统一全站二级页面与设置页 `<h1>` 字阶与 `h-11` 页头基线；登录页 `LoginPage` / `AuthLayout` 升级为居中轻阴影卡片焦点容器；桌面端离线页 `apps/desktop/renderer/offline.css` 与 `apps/web/index.html` `theme-color` 同步新版中性色阶。
4. **技术债清理**：清零全站 5 处无效表面类名（`bg-surface-default / panel / subtle`）、1 处无效 `bg-brand`（`DirectChat.tsx:106` 修复为 `bg-danger text-fg-inverse`）及 19 处原生 Tailwind `shadow-sm/md/lg`，未新增任何依赖、字体、图片或第三方素材。

## 验证

- **格式、类型检查与构建**：`pnpm lint`（Prettier + 全仓 TypeScript）与 `pnpm build` 全部通过。
- **单元测试（Vitest）**：覆盖 `packages/ui`（新增 `Segmented` 切换测试）、`packages/app/src/tasks/board/card.test.tsx`（新增 2 项空行消除与密度钩子测试）以及收件箱、偏好、Agent、市场、自动化、迭代、专项、视图、对话、组织与设置共 51 个测试文件，**519 / 519 全部通过**。
  - 注：`packages/app/src/shell/navMemory.test.tsx` 的「切换组织后记录不串」在基线 `master` 即存在既有失败（17/18 通过，参见 `2026-11-24-workspace-reading-chat.md`），本轮未改动导航记忆与外部点击关闭逻辑。
- **Playwright E2E（模拟 API）**：运行 `OPENFOX_WEB_PORT=4191 pnpm test:e2e density.spec.ts scroll-containment.spec.ts`：`density.spec.ts` 中英文深浅主题双密度同步与甘特背景色 2/2 全部通过；`scroll-containment.spec.ts` 11/12 通过（唯一失败为基线提交 `d420ae3` 将窄屏文档目录改为抽屉所致，本轮未改动文档布局逻辑）。
- **实机浏览器视觉验证（模拟 API，DPR 2，CSS 像素归一化）**：覆盖 `light`、`dark`、`classic-dark`、`magic-blue` 四套主题，以及 1440×900 看板/收件箱/偏好设置/登录、960×680 深色收件箱、680×860 窄屏看板共 9 组场景；全部 `overflowX: false`、0 条 console/page error，逐张目视核对五项视觉标准通过。

## 配置、数据与回退

本轮纯前端样式与组件表现优化，无新增依赖、无配置文件/接口/SQL/数据迁移变更；回退直接回滚本分支提交即可。
