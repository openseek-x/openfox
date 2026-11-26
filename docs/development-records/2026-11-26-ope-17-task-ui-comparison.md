# OPE-17 任务界面与交互：OpenFox 与 HiFox 对比及优化方案

- 日期：2026-11-26
- 任务：OPE-17「对比我们与 HiFox 的任务界面及交互差异，并提出优化方案」
- 分支：`docs/ope-17-task-ui-comparison`（从本地 `master@1f4d3db` 切出）
- 交付形态：只交付分析文档，业务代码零改动

## 一、任务摘要与范围

对比两边任务相关界面（列表、详情、创建与编辑、状态流转、评论与动态），重点看操作路径、状态切换和反馈方式，给出 OpenFox 的优化建议。

非目标：
- 不改 `packages/`、`internal/`、`db/` 下任何代码，不新增接口和迁移。
- 不另建任务。建议是否立项由人决定。
- 不重做已经定过的决策（见「排除项」）。

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

## 七、实施与验证

实施步骤：
1. 读取任务和评论（评论为空），从本地 `master` 切出独立 worktree 与分支，不在主工作区操作。
2. 梳理 `packages/app/src/tasks` 下列表、详情、创建、批量、看板、表格代码，以及 `internal/collaboration` 的状态写入与分拣代码。
3. 对照 HiFox 历史快照和功能复核表，列出差异 C1–C9。
4. 自省深潜：阅读台账 63 条（精读 55 条），18 个核心文件近 30 天提交考古，三类证伪。结论是 C9（Duplicate 无目标）成立且范围比初诊大，C3 撤回，C2、C8 降为备选。
5. 编写本文档。写之前重新核对了文中引用的行号。

验证：
- 文中每个 `file:line` 在 `master@1f4d3db` 上用脚本逐条检查过，25 处引用的文件都存在且行号在范围内；关键符号在 `develop@0621607` 上也复核过（见第二节）。
- `git diff --stat master...HEAD` 只有本文件一个新增。
- 纯文档改动，没有运行 `make check`、`pnpm lint/test/build`，因为没有代码、配置或契约变化。

## 八、问题、根因与决策

| 问题 | 根因 | 处理 |
| --- | --- | --- |
| 初诊只看了详情的状态选择器 | 只顺着 HiFox 快照里的入口找对应代码 | 自省时从 `statusGroups` 反查全部调用方，扩大到 6 个界面入口和 3 个非界面入口 |
| 初诊漏了动态流已内嵌会话行 | `Timeline.tsx` 只读了前 140 行 | 补读 217-245 行后更正 |
| C3 证据看错 | 快照 1220 标题含「子任务」，实际是任务类型菜单 | 撤回 C3 |
| 写文档时发现内置「重复了…」关联类型 | 初诊只搜了 `duplicate_of` 字段 | 在 D1 现状里补上这条旁路，D1 的结论收窄为「缺状态加目标的组合」 |
| 评审发现排除表里 C5 的描述与初诊不符 | 起草时凭记忆写，没有回查初诊 | 按初诊原文改正，并重新逐条核对文中全部引用 |
| 远端 `develop` 领先 `master` 40 个提交，部分引用文件有改动 | 分支按规定从本地 `master` 切出，而提测 PR 以 `develop` 为目标 | 在 `develop@0621607` 上复核结论与行号，补了偏移对照表 |
| 新文件被本地排除规则忽略 | 仓库主人的 `.git/info/exclude` 第 15 行排除了 `/docs/development-records/`，但 `master` 上这个目录已有 8 份被跟踪的记录，`d420ae3` 也是在同样规则下新增的记录 | 只对本文件用 `git add -f`，不改排除文件 |

自主决策（待人追认）：
1. 交付形态：只交付文档（备选：顺手修 D1；另建任务）。
2. 重复任务建议推荐方案 P（备选 Q、R）。
3. D3、D4 作为备选；C3 撤回；C5 不进建议。
4. D2 标注「需先核对 parity 记录」，不直接推荐实现。
5. 本地排除规则忽略了开发记录目录，按 `d420ae3` 的先例对本文件用 `git add -f`（备选：修改排除文件，或不提交记录）。

## 九、配置变更

无。

## 十、SQL 与迁移

无。本次没有任何 SQL、表结构迁移或数据迁移。

## 十一、剩余风险与回滚

- HiFox 一侧证据截至 2026-11-13，当前版本可能已经变化。立项 D1 前建议在 HiFox 上真机确认一次。
- D1 的「无目标 Duplicate」是代码阅读推断，没有运行复现。立项前建议在本地选一次 Duplicate 状态确认。
- `UpdateTask` 的部分调用方（会话、外部同步、任务扩展接口）只读了调用行，方案 Q 的影响面需要立项时再逐个核实。
- 回滚：删除本文件即可，没有其他影响。
