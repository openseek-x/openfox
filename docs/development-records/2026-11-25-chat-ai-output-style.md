# 对话区 AI 输出样式精简（OPE-12）

2026-11-25，从本地 master 的 1f4d3db 切出 `feat/ope-12-chat-ai-output-style`，在独立工作树 `/Users/puhuajie/OpenFox-worktrees/OPE-12-dev` 中开发。

## 任务与范围

对话区 AI 回复观感偏重，希望更简洁易读。先在真实页面渲染一条典型回复，内容含标题、列表、嵌套列表、表格、引用、代码块、分隔线和工具过程，再定位问题。实测发现五处问题：

1. 过程说明（如“我先查看请求拦截器……”）与最终结论同色同字重，读者分不清哪句是过程、哪句是结论。
2. 三列短表格被拉满到 766 px，列间大片空白，行悬停底色在阅读场景里是噪音。
3. 引用块同时有底色、3 px 左边框和圆角，看起来像一张卡片。
4. 列表每项 28 px（24 px 行高加 4 px 间距），嵌套列表显得松散。
5. 结尾分隔线偏重。

非目标：设计令牌、代码块（复制、30 行折叠）、消息操作栏（d420ae3 刚调整）、自写的 `Markdown.tsx`（评论、私聊、文件预览使用）、依赖、接口与数据。

初稿还怀疑工具调用行、标题层级和 Git 统计卡有问题。实测工具过程结束后已收成“已完成 x 秒”“已执行 n 项操作”两行，标题间距合理，统计卡只在有 Git 摘要时出现，因此这三项不在本次范围内。

## 实现

只调整元素类名，没有引入 `@tailwindcss/typography`，也没有新增令牌。

- `ChatMarkdown.tsx`
  - 引用块去掉底色和圆角，改为 2 px 左边线加次要文字色。
  - 表格外框改为 `w-fit max-w-full overflow-x-auto`，表格去掉 `w-full` 与行悬停底色，表头底色保留。
  - 列表项间距从 4 px 改为 2 px，嵌套列表上边距同步收紧。
  - 分隔线改用弱分隔色 `line-soft`。
- `steps/rows.tsx`：`AgentTextRow` 外加 `text-fg-muted` 容器，并标记 `data-step-text`。起初试用 `fg-secondary`，截图里与正文几乎无法区分，所以改用与思考详情一致的 `fg-muted`。代码块、行内代码和链接自带颜色，不受影响。
- 测试：`ChatMarkdown.test.tsx` 新增引用、短表与宽表、分隔线的用例；`steps.test.tsx` 新增过程说明弱化、最终回复不受影响、空说明不渲染的用例。

## 验证

- 真实浏览器（Chromium 1440 × 1500，Playwright 模拟接口数据）对比同一条回复：整轮高度 945 → 917 px，正文 765 → 737 px，表格外框 766 → 301 px，引用底色变为透明。标题与段落字号、块间距序列保持不变。过程说明浅色为 rgb(113,113,123)、深色为 rgb(161,161,170)，最终回复颜色不变。
- 用同样的类名克隆 14 列宽表：外框钳制在消息宽度 766 px，可横向滚动，页面无横向溢出。
- `fg-muted` 对比度：浅色白底 4.83、浅色 canvas 4.71、深色 7.76、魔法蓝 5.81、经典深色 7.06，均满足 AA。
- 对话模块 51 个文件 438 项、Agent 设置 10 个文件 57 项通过。全量 Vitest 2317 项通过、2 项失败：
  - `navMemory` 的“切换组织后记录不串”在基线上同样失败，与 2026-11-24 记录一致。
  - `wechat` 的扫码用例单独重跑时，改动前后均通过，属于并行负载下的偶发失败。
- `pnpm typecheck`、`pnpm build`、改动文件的 Prettier 检查和 `git diff --check` 均通过。
- 端到端：`artifact-annotation-mode` 4 项通过，产物 Markdown 批注前后截图一致。`prompt-kit` 15 项中 13 项通过，另外 2 项“task AI panel resizing”期望面板宽 580 px、实际 541 px，在基线上同样失败。该用例检查 `TaskPage` 的 AI 面板宽度，与本次改动无关。
- 未验证：Electron 原生窗口、真实 Agent 运行。

## 问题与调整

- 截图脚本最初用 `__dirname`，在 ESM 下不存在，改为固定证据目录。
- 宽表校验第一次取到用户气泡（第一个 `message-content`），改为取包含表格的回复。
- 新增用例原本用 `not.toContain("hover")`，但表头底色令牌名 `bg-surface-hover` 里也含该子串，误报失败。改为只排除 `tr:hover`。
- 过程说明颜色从 `fg-secondary` 改为 `fg-muted`，原因见“实现”一节。

## 影响面

`ChatMarkdown` 还用于产物 Markdown 预览与批注模式，那里的表格、引用和分隔线会同步变化，保持一致。`AgentTextRow` 也用于 Agent 设置里的运行记录弹窗，那里的过程说明同样会变弱。

## 配置、SQL 与迁移

无配置文件、依赖、接口、SQL、表结构或数据迁移改动。

## 风险与回退

- 剩余风险：Electron 原生窗口和真实 Agent 长回复未实测。中文长表头在窄面板中依赖 `whitespace-nowrap` 加横向滚动，行为与改动前相同。
- 后续：Markdown 排版令牌化，以及 `Markdown.tsx` 与 `ChatMarkdown` 的统一，可作为架构后续单独评估。
- 回退：单个提交，`git revert` 即可，无数据影响。
