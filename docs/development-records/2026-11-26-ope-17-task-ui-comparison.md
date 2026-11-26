# OPE-17 任务界面与交互：OpenFox 与 HiFox 对比及优化方案

- 日期：2026-11-26
- 任务：OPE-17「对比我们与 HiFox 的任务界面及交互差异，并提出优化方案」
- 分支：`docs/ope-17-task-ui-comparison`（从本地 `master@1f4d3db` 切出）
- 交付形态：分析文档 + 实现 D1 第一期（通用「标记为重复任务」）与 D2（属性侧栏整栏收起）。第一版只交文档，人工在 2026-11-26 选择 Q1=D 后补做实现

## 一、任务摘要与范围

对比两边任务相关界面（列表、详情、创建与编辑、状态流转、评论与动态），重点看操作路径、状态切换和反馈方式，给出 OpenFox 的优化建议。

非目标：
- 不新增迁移，不改表结构。
- 不做 D1 第二期（服务端不变量，方案 Q）：PATCH、批量接口、Agent 工具、外部同步写入 Duplicate 的行为不变。
- 不做 D3、D4。
- 不另建任务；不重做已经定过的决策（见「排除项」）。

## 二、证据来源与等级

| 等级 | 含义 |
| --- | --- |
| 已证实 | 读到了 OpenFox 代码（附 `file:line`），或读到了 HiFox 历史快照、审计表 |
| 推断 | 根据代码阅读推出的运行结果，没有实际运行复现 |
| 未验证 | 没有证据，只是待核实的问题 |

证据边界：
- OpenFox 一侧以 `master@1f4d3db` 为准。
- HiFox 一侧只有历史材料：2026-11-03 的交互快照（`work/full-interaction-audit-2026-11-03/`，下文写作「快照 编号」）和 2026-11-13 的功能复核表（`work/hifox/comparison-2026-11-13/deep-audit/全量功能复核表.csv`）。本轮没有登录 HiFox 做真机对照，HiFox 当前版本可能已经变化。
- 本轮没有启动 OpenFox 做运行复现。

在 `develop@0621607` 上的复核：本分支从 `master` 切出，`develop` 之后又合入了 PR #23–#26（OPE-11 到 OPE-14），改动了 37 个任务相关前端文件。按提交时的状态复核：

- 后端：`internal/collaboration`、`internal/spacetools`、`internal/imports` 下没有改动；新增内容里没有任何与 Duplicate 类别或 `duplicate_of_task_id` 相关的行为变化（只有测试夹具里加了 `duplicate_of: null`）。
- 前端：结论都还成立，只是部分行号偏移。

| 文件 | master@1f4d3db | develop@0621607 |
| --- | --- | --- |
| `packages/app/src/tasks/pickers.tsx`（`statusGroups`） | 69-79 | 70-80，函数体没变 |
| `packages/app/src/tasks/detail/propertyPickers.tsx` | 260 | 267 |
| `packages/app/src/tasks/create/CreateTaskDialog.tsx` | 735 | 736；候选状态过滤 223-225 没变 |
| `packages/app/src/conversations/ConversationView.tsx`（重试） | 388-443 | 396-451 |
| `packages/app/src/tasks/detail/Timeline.tsx`（会话行） | 217-245 | 217-246 |
| 其余引用 | — | 行号不变 |

## 三、分界面对比总览

| 界面 | 两边一致或已追平 | 有差异 | 结论 |
| --- | --- | --- | --- |
| 列表 | 看板、列表、表格、甘特四种布局；分组折叠；行首勾选和批量栏（上限 100 条）；表格单元格内联改字段；筛选先选字段再选值 | 无明显缺口。OpenFox 多一个统计开关（OpenFox 独有） | 不建议改动 |
| 详情 | 抽屉与全页共用 `TaskDetailView`；子任务行内创建；关注者；关联；父任务选择 | 更多菜单缺「标记为重复任务」（D1）；属性栏不能整栏收起（D2）；关联入口三项对一项（D4） | D1 优先，D2 可选 |
| 创建与编辑 | 创建弹窗支持 Agent 模式；字段内联编辑 | 创建弹窗的状态候选里可以直接选 Duplicate 类别，但没有重复目标（D1 的一部分） | 随 D1 一起处理 |
| 状态流转 | 详情、表格、看板卡片菜单、批量栏都能改状态；处理人与执行 Agent 分开两个入口 | 进入 Duplicate 类别时没有任何引导或约束（D1） | D1 优先 |
| 评论与动态 | 评论框含草稿、提及、快捷回复、附件；动态流里内嵌会话行 | 运行失败后要进对话视图才能重试，HiFox 动态条目直接带「重试」（D3） | 备选 |

## 四、差异与建议

### D1 重复任务没有闭环（强，建议优先）

现状：
- 【已证实】HiFox 详情更多菜单有「标记为重复任务」（快照 1215、1222）；OpenFox 详情更多菜单没有（快照 1227、1228）。
- 【已证实】`duplicate_of_task_id` 全仓只有一处写入：`internal/collaboration/triage.go:1184`，属于分拣流程 `leaveTriage`。分拣流程要求任务正在分拣中，否则返回 `NOT_IN_TRIAGE`（`triage.go:25`、`triage.go:1072`）。也就是说，只有分拣台能把任务标成「重复于某任务」。
- 【已证实】通用改状态路径 `UpdateTask` 只检查目标状态属于该任务类型的工作流（`internal/collaboration/tasks_write.go:1167`），不读也不写重复目标。
- 【已证实】默认工作流播种了 Duplicate 状态（`db/migrations/000007_wp04_collaboration.sql:204`、`000017_wp18_email_signup_onboarding.sql:188`）。数据库对重复目标只有「不能指向自己」和「同组织外键」两条约束（`db/migrations/000036_triage.sql:74-76`）。
- 【已证实】前端状态候选 `statusGroups` 原样列出工作流全部状态，不按类别过滤（`packages/app/src/tasks/pickers.tsx:69-79`）。它的调用方有：详情状态选择器（`detail/propertyPickers.tsx:260`）、表格（`table/TaskTable.tsx:205`）、批量栏（`bulk/BulkActionBar.tsx:167`）、创建弹窗（`create/CreateTaskDialog.tsx:735`，只排除分拣类别，见 `:223-225`）、筛选（`filters/options.tsx:55`）、自动化（`automations/StatusEditor.tsx:114`）。看板卡片菜单 `TaskMenu.tsx:152-166` 另外列出全部状态。
- 【已证实】非界面入口同样不看类别：批量接口只检查状态属于工作流（`internal/collaboration/task_batch.go:242`）；Agent 工具按状态名查工作流（`internal/spacetools/spacetools.go:476-484`）；外部项目同步直接调用 `UpdateTask` 写状态（`internal/imports/project_sync.go:510`）。
- 【已证实】OpenFox 还有一条旁路：内置关联类型「重复了… / 被…重复」（`internal/collaboration/relation_types.go:20`，文案 `packages/i18n/src/messages/zh-CN.ts:942`），可以在关联弹窗里手工建立重复关系。但它只是关联，不改状态，也不写 `duplicate_of_task_id`，详情属性里「重复于」那一行不会显示。
- 【推断】在上述任一入口把状态改成 Duplicate，得到的是「状态显示重复、却不知道重复了谁」的任务。状态离开 Duplicate 后，分拣时写入的重复目标不会被清空（全仓没有清空代码）。没有运行复现。

影响：用户能把任务关成「重复」，但看不到它重复了哪个任务，被重复的任务动态里也没有记录。HiFox 一侧在详情就能一步完成「选目标 + 关闭」。

建议设计对比（三选一，推荐 P）：

| 方案 | 做法 | 改动面 | 风险 |
| --- | --- | --- | --- |
| P 分期（推荐 ✅） | 第一期：新增显式的「标记为重复」接口，不要求任务在分拣中，复用 `leaveTriage` 的目标校验和动态写入；详情更多菜单加入口；详情、表格、看板菜单在选中 Duplicate 类别时改走目标选择器（复用 `TaskSearchDialog` 的 `duplicatePicker`）；批量栏和创建弹窗对 Duplicate 类别禁用或隐藏。第二期再评估服务端不变量 | 后端 1 个接口 + 前端 4 处入口 + OpenAPI 生成 | 第一期 PATCH 接口、Agent 工具、外部同步仍能绕过 |
| Q 服务端不变量 | `UpdateTask` 在目标类别为 DUPLICATE 时要求带重复目标，离开时清空 | 一处管住全部入口 | `UpdateTask` 有 8 个非测试调用方（批量、分拣两处、外部同步、会话两处、任务扩展接口、Agent 工具），全部要核实，爆炸半径最大 |
| R 只在前端隐藏 Duplicate | 状态候选里去掉 Duplicate | 最小 | 不可取：分拣之外就没有任何标重复的入口了 |

推荐 P 的理由：先补上用户最常走的界面路径，避免一次改动共享写路径；第二期是否做 Q，取决于 Agent 工具和外部同步实际会不会写入 Duplicate。

### D2 属性栏不能整栏收起（小，可选）

- 【已证实】HiFox 详情属性栏有「收起属性侧栏」按钮（快照 1222）。
- 【已证实】OpenFox 属性栏只在非窄宽度渲染（`packages/app/src/tasks/detail/TaskDetailDrawer.tsx:988-1006`），没有整栏收起控件；`detail/WideProperties.tsx:34` 起只有分区折叠，默认展开。
- 【未验证】在追平进度、审计表和交互台账里没有搜到这一项的决策记录。搜不到不等于没决策过。

建议：先核对 `docs/hifox-parity/` 里有没有把这一项定为「不做」。如果没有，在属性栏顶部加收起按钮，状态存本机偏好。纯前端，只影响非窄宽度。

### D3 运行失败后重试入口更深（小，备选）

- 【已证实】OpenFox 动态流已经内嵌会话行（`packages/app/src/tasks/detail/Timeline.tsx:217-245`），行内有状态徽标和「查看对话」；单次运行重试只在对话视图里（`packages/app/src/conversations/ConversationView.tsx:388-443`）。
- 【已证实】HiFox 动态流里的 Agent 运行条目直接带「重试」按钮（快照 1222）。
- 【推断】这是入口深度差一层，不是能力缺口。

建议：在会话行的失败态上直接放「重试」，复用对话视图的重试端点。需要先确认会话摘要里带着可重试的运行 ID。

### D4 关联入口数量（备选，价值低）

- OpenFox 详情更多菜单有三项：添加链接、创建关联任务、关联已有任务；HiFox 只有一项「添加关联任务」（快照 1215）。
- 任务选择器已支持粘贴编号和任务链接（`packages/app/src/tasks/detail/TaskSearchDialog.tsx:26-38`），但仅限同空间。

建议：暂不改。三个入口语义清楚，合并收益小。

## 五、排除项

| 编号 | 原差异 | 排除原因 |
| --- | --- | --- |
| C3 | 「添加子任务」入口位置 | 撤回：快照 1220 实际是任务类型菜单，两边详情工具栏都有「添加子任务」 |
| C5 | HiFox CLI 的任务列表能按状态变更时间、metadata、自定义字段筛选；OpenFox 筛选字段里没有状态变更时间（`packages/app/src/tasks/model.ts`） | 只有 `hifox task list --help` 的 CLI 帮助，没有 HiFox 界面入口的证据 |
| C6 | 处理人与执行 Agent 分开两个入口 | 2026-11-03 已定决策 D05，有意为之 |
| C7 | 列表统计开关 | OpenFox 独有能力，不算缺口 |

## 六、建议优先级

| 优先级 | 编号 | 内容 | 前置条件 |
| --- | --- | --- | --- |
| 1 | D1 | 重复任务闭环（方案 P 第一期） | 产品确认是否沿用 HiFox 的「标记为重复任务」交互 |
| 2 | D2 | 属性栏整栏收起 | 核对 parity 记录 |
| 3 | D3 | 会话行失败态直接重试 | 确认会话摘要带运行 ID |
| — | D4 | 合并关联入口 | 暂不做 |

## 七、已实现的方案

### D1 第一期：通用「标记为重复任务」

后端：
- 新接口 `POST /v1/organizations/{organizationID}/spaces/{spaceID}/tasks/{taskID}/duplicate`（`operationId: markTaskDuplicate`）。请求体 `{version, duplicate_of_task_id, status_id?}`，返回 `Task`。
  - 鉴权与 PATCH 一致：`authorizeSpace(..., ActionUpdateObject, taskID)`，停用空间返回 409 `SPACE_RETIRED`。
  - 乐观锁：`version` 不符返回 409 `VERSION_CONFLICT`，不写任何数据。
  - 目标不是 UUID、指向自己、不存在或不可见：400 `INVALID_DUPLICATE`（与分拣台一致）。
  - `status_id` 不在任务工作流里或类别不是 DUPLICATE：400 `INVALID_DUPLICATE_STATUS`（新错误码）。省略时取工作流里排序最前的 DUPLICATE 状态；工作流里没有：409 `STATUS_CATEGORY_MISSING`。
  - 不要求任务在分拣中。
- `internal/collaboration/triage.go`：把 `leaveTriage` 的「改状态 + 写重复目标 + 给目标记动态」抽成 `closeInCategory`，分拣台拒绝、分拣台标记重复和新接口共用。分拣台的行为不变（集成测试 `TestTriage*` 全部通过）。
- `internal/collaboration/task_duplicate.go`：新增 `MarkTaskDuplicate` 和 `requireStatusCategory`。
- `internal/api/tasks_ext.go`：注册路由和处理函数。

前端：
- `MarkDuplicateDialog`：包装分拣台的目标选择器 `TaskSearchDialog duplicatePicker`，候选只含同空间、未被标记为重复的任务；取消不写数据。
- `useMarkDuplicate`：与 `useUpdateTask` 共用串行队列 `task-write:${orgId}`，发送时取缓存里的最新 version；不做乐观更新；成功后写缓存、提示「已标记为重复任务」、刷新计数和两个任务的动态。
- 入口：
  - 详情更多菜单新增「标记为重复任务」，只在工作流有重复类别状态时显示，只读时禁用。
  - 选中重复类别状态时改走目标选择器：详情属性栏状态选择器、详情更多菜单的「状态」子菜单、表格状态单元格、看板卡片和列表行的「移动到」、按状态分列的看板拖动（普通看板与分组看板）。
  - 不再提供重复类别：批量操作栏、新建任务弹窗、分拣台「接受」弹窗的状态候选。
- 文案：`tasks.detail.markDuplicate`、`tasks.errors.invalidDuplicateStatus`（中英两份）；其余复用分拣台已有文案。

### D2：属性侧栏整栏收起

- 前置核对：`docs/hifox-parity/` 里没有把这一项定为「不做」的记录；2026-10-27 的设计稿记录过「右侧属性栏收起，改成标题上下两组胶囊」，收起后的布局沿用这条设计。
- 宽布局属性侧栏右上角新增「收起属性侧栏」按钮。收起后侧栏不渲染，属性改用窄布局同款的胶囊行显示在标题下方，顶部右侧出现「展开属性侧栏」按钮。
- 收起状态存本机：`localStorage` 键 `openfox:task-properties-collapsed`，值 `"1"` 表示收起；展开时删除这个键。读写失败按展开处理。
- 窄布局（容器宽度小于 720px）行为不变，不显示收起和展开按钮。

## 八、实施与验证

实施步骤：
1. 读取任务和评论（评论为空），从本地 `master` 切出独立 worktree 与分支，不在主工作区操作。
2. 梳理任务相关前后端代码，对照 HiFox 历史快照和功能复核表，列出差异 C1–C9。
3. 自省深潜：阅读台账 63 条（精读 55 条），18 个核心文件近 30 天提交考古，三类证伪。
4. 编写分析文档，提交 `1c6a933` 并提测。
5. 人工选择 Q1=D 后，按第七节实现 D1 第一期与 D2，补充测试，真实页面验收，评审后提交。

验证（全部实际运行）：

| 项目 | 命令 | 结果 |
| --- | --- | --- |
| 新接口集成测试（真实 PostgreSQL） | 一次性集群上 `go test ./tests/integration/stage3 -run '^TestTriage\|^TestMarkTaskDuplicate'` | 7 项全部通过 |
| stage3 全部集成测试 | 同上，`-run .` | 295 项通过；3 项失败，都报「需要独占 I07/I13 数据库」，是测试环境前提，与本改动无关 |
| 后端全量 | `make check` | 本分支改动的包 `internal/collaboration`、`internal/api`、`tests/integration/stage3` 通过。`internal/runtime`、`internal/runtime/codex`、`cmd/openfox` 在全量并发时超时，单独重跑通过；`tests/integration/stage8d` 编译失败（`ReadWorkDirectoryPath` 参数不符），本分支没有改动这些目录 |
| 前端格式与类型 | `pnpm lint` | 通过 |
| 前端测试（新增） | `vitest run packages/app/src/tasks/duplicate17.test.tsx` | 9 项通过 |
| 前端测试（任务与分拣目录） | `vitest run packages/app/src/tasks packages/app/src/triage` | 369 项通过 |
| 前端全量 | `pnpm test` | 2323 项通过，1 项失败：`shell/navMemory.test.tsx`「切换组织后记录不串」。在 `master` 上同样失败，在 `develop` 上已通过，与本改动无关 |
| 构建 | `pnpm build` | 通过 |
| 与 develop 合并 | `git merge-tree --write-tree origin/develop HEAD` | 无冲突。在合并结果上 `pnpm gen:api` 无差异、`pnpm typecheck` 通过、任务与分拣目录 371 项通过、`go build ./...` 通过。全量前端测试首次运行有 2 项（对话分叉、Bitbucket 诊断，本分支未改动）超时，单独重跑两次都通过 |
| 真实页面 | 一次性 PostgreSQL + 真实服务端 + 前端开发服务器，Playwright 操作 | 见下 |

真实页面验收（1440×900，接口和数据都是真的，不是模拟）：
- 详情更多菜单出现「标记为重复任务」；选择 CHK-1 后服务端返回 `status_category=DUPLICATE`、`duplicate_of=CHK-1`，动态里出现「把状态从 Todo 改为 Duplicate」，提示「已标记为重复任务」。
- 属性栏状态选择器选 Duplicate 时弹出目标选择器；按 Esc 取消后任务仍是 Todo，version 仍为 1。
- 收起属性侧栏后属性显示为标题下方的胶囊行；刷新页面后仍是收起状态（本机记为 `"1"`）；展开后恢复侧栏。
- 批量操作栏的状态候选为 Triage、Backlog、Todo、In Progress、Done、Cancelled，没有 Duplicate。

## 九、问题、根因与决策

| 问题 | 根因 | 处理 |
| --- | --- | --- |
| 初诊只看了详情的状态选择器 | 只顺着 HiFox 快照里的入口找对应代码 | 自省时从 `statusGroups` 反查全部调用方，扩大到 6 个界面入口和 3 个非界面入口 |
| 初诊漏了动态流已内嵌会话行 | `Timeline.tsx` 只读了前 140 行 | 补读 217-245 行后更正 |
| C3 证据看错 | 快照 1220 标题含「子任务」，实际是任务类型菜单 | 撤回 C3 |
| 评审发现排除表里 C5 的描述与初诊不符 | 起草时凭记忆写，没有回查初诊 | 按初诊原文改正，并重新逐条核对文中全部引用 |
| 远端 `develop` 领先 `master` 40 个提交 | 分支按规定从本地 `master` 切出，提测 PR 以 `develop` 为目标 | 在 `develop` 上复核结论；import 放在不相邻的位置；用 `merge-tree` 和合并结果上的测试确认 |
| 新文件被本地排除规则忽略 | 仓库主人的 `.git/info/exclude` 排除了 `/docs/development-records/`，但 `master` 上已有 8 份被跟踪的记录 | 只对本文件用 `git add -f`，不改排除文件 |
| 一次性 PostgreSQL 启动报 "postmaster became multithreaded during startup" | macOS 上没有有效的 `LC_ALL` | 启动脚本里设置 `LC_ALL=en_US.UTF-8` |
| 在 `packages/app` 下直接跑 vitest，301 项报 `localStorage is not defined` | `vitest.config.ts` 在仓库根目录 | 改从仓库根目录运行 |
| 真实页面上侧栏顶部出现两个「属性」标题 | 新加的收起行带了标题，和下方属性卡片自带的标题重复 | 去掉收起行的文字，只留按钮 |
| 截图工具把快照写进了主工作区 `.playwright-mcp/` | 该工具的输出目录固定在主工作区 | 删除本次写入的两个文件（核对主工作区 `git status` 无变化），改用从本 worktree 运行的独立 Playwright 脚本 |

决策：
1. 交付形态：第一版只交文档（自主决策 A）。人工改选 D：在本任务实现 D1 第一期与 D2。
2. 重复任务采用方案 P 第一期（备选 Q、R，理由见 D1）。
3. D3、D4 作为备选，本次不做；C3 撤回；C5 不进建议。
4. D2 先核对 parity 记录，确认没有「不做」的决策后再实现。
5. 本地排除规则忽略开发记录目录，按 `d420ae3` 的先例对本文件用 `git add -f`。
6. 新接口用单独的 POST，不扩展 PATCH：PATCH 背后的 `UpdateTask` 有 8 个非测试调用方，扩展它会把第二期的不变量问题提前带进来。
7. 拖进重复列时先弹出目标选择器，确认前不做乐观移动；目标列里的落点位置不保留（取消时卡片留在原列）。
8. 批量修改、新建任务、分拣台「接受」不提供重复类别：这些入口没法逐个选目标。

## 十、配置变更

- 没有服务端配置或环境变量变更。
- 新增浏览器本机偏好键 `openfox:task-properties-collapsed`（`packages/app/src/storage.ts`）：`"1"` 表示收起属性侧栏，不存在表示展开（默认）。只影响当前浏览器，不同步到服务端，不需要初始化或迁移。

## 十一、SQL 与迁移

无。本次没有 SQL、表结构迁移或数据迁移。新接口只用到已有的 `tasks.duplicate_of_task_id` 列和已有的状态表。

## 十二、剩余风险与回滚

- D1 第二期没做：PATCH、批量接口、Agent 工具、外部同步仍能把任务改成 Duplicate 而不带目标；离开 Duplicate 时不清空 `duplicate_of_task_id`。
- 服务端不阻止「目标本身也是重复任务」或互相指向，只在前端选择器里过滤；与分拣台现有行为一致。
- 表格状态单元格和看板卡片菜单有前端测试；看板拖进重复列既没有测试，也没有在真实页面上验证，只做了代码审阅。真实页面截图覆盖了详情更多菜单、属性栏状态选择器、批量栏和属性侧栏。
- HiFox 一侧证据截至 2026-11-13，当前版本可能已经变化。
- 回滚：回退提交即可。没有迁移，新接口没有其他调用方；本机偏好键残留不影响旧版本。
