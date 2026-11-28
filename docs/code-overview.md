# OpenFox 代码现状

OpenFox 把团队工作台和执行 Agent 的电脑连接起来。中心保存组织、任务、对话、运行记录和产物，用户自己的电脑负责启动 Runtime。本次以 `master@1f4d3db` 为梳理基线，并记录 OPE-8 对基线问题的修复。

## 从哪里读代码

| 入口                                                                       | 职责                                                                     |
| -------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `cmd/openfox-server`、`internal/platform`                                  | 加载配置，提供存活与就绪检查，显式执行数据库迁移，启动 API 和后台 worker |
| `internal/api`、`internal/identity`、`internal/membership`                 | 注册 HTTP 方法和路径，验证会话、设备或回调身份，选择组织并授权           |
| `internal/collaboration`、`internal/conversations`、`internal/automations` | 任务、评论、对话、通知和自动化                                           |
| `internal/dispatch`、`internal/execution`、`db/migrations`                 | 运行状态、调度、数据库容量预留、领取、续租、回收                         |
| `cmd/openfox`、`internal/daemon`、`internal/runtime`                       | 命令行、设备注册、WSS、执行进程、工作目录、本机日志和恢复                |
| `internal/imports`、`internal/integrations`                                | 导入作业、第三方账户绑定、入站事件、出站同步和重试                       |
| `apps/web`、`packages/app`、`packages/api`                                 | React 工作台、业务页面、HTTP 客户端与 OpenAPI 生成类型                   |
| `packages/ui`、`packages/i18n`、`apps/desktop`                             | 共享组件、简体中文和英文、加载同一工作台的 Electron 应用                 |

基线的 Go 非测试代码约 24.2 万行，前端约 20.7 万行。OpenAPI 基线描述了 630 个路径、757 项方法与路径组合，其中 17 个路径项引用外部文件。Go 的一次注册可以处理多条文档路径，同一文档模板也可以服务多个独立注册的渠道。可执行核对入口是 `pnpm check:api`。

## 一条任务怎样执行

1. 工作台通过同源 `/api/v1/` 发起请求。中心按入口验证用户、PAT 或设备凭据。Cookie 写请求校验 CSRF。服务在带组织上下文的 pgx 事务中读写，PostgreSQL 行级安全约束限制租户边界。
2. 对话消息产生 Run。中心的 dispatch worker 检查权限、并发、设备和空间条件，在数据库预留容量并生成 offer。设备的 WSS 连接接收唤醒，短线重连期间也可用 HTTPS 查询 offer。
3. daemon 领取后使用本机 SQLite journal 记录 Run 和代次，准备目录并向中心请求启动许可。Runtime 在本机执行。设备通过 HTTPS 续租、上传事件和结果，中心在终态释放预留。备份恢复提升中心纪元后，设备必须确认新纪元才能领取或开始新任务。
4. React Query 缓存工作台读模型。组织 SSE 驱动查询失效，Run 事件更新对话视图。任务评论和导入评论均写入 PostgreSQL，数据库触发器驱动通知与相关队列。交互写入保留作者与权限检查，导入及同步写入另有来源标识和重放去重。

派发和恢复的协议见[执行契约](../contracts/shared/execution.md)。请求字段、路径参数及响应见[OpenAPI](../contracts/openapi.yaml)。

## 构建、验证与运行

| 命令                                                  | 覆盖范围                                                                                                                  |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `make test`                                           | Go 包内测试。配置数据库的用例按各自条件运行                                                                               |
| `make check`                                          | Go 格式、测试、vet、迁移清单，以及临时 PostgreSQL 上的必需集成套件。非预期跳过会使检查失败                                |
| `pnpm check:api`                                      | 用真实路由器匹配展开后的方法和路径，检查源码直接注册的接口、设备事件正反例及生成文件漂移                                  |
| `pnpm check`                                          | 契约、Prettier、React Hooks、TypeScript、全量 Vitest、桌面 Node 测试及 Web 生产构建                                       |
| `python3 tests/integration/run_all.py --browser-only` | 在独立 PostgreSQL 和中心服务上运行 Playwright。先运行 `pnpm exec playwright install chromium`，测试入口会构建真实静态页面 |
| `bash deploy/test-center.sh <镜像> <版本>`            | 在临时 Docker 网络和 PostgreSQL 上核对镜像版本、175 个迁移、存活与就绪端点                                                |

Go 版本由 `go.mod` 固定，Node 和 pnpm 要求在根 `package.json`。本机准备 PostgreSQL 17 时，将 `initdb`、`pg_ctl` 和相应客户端放入 PATH。集成运行器按包隔离数据库，另为需要独立命名空间的渠道建库。诊断日志默认位于 `work/integration-*`。外部账号、已安装 Runtime 和专用渠道浏览器夹具的验收有单独运行条件。[集成发布验收](../tests/integration/wp16/README.md)

中心 API 和 worker 随同一个服务进程启动。进程内限流器按实例计数，数据库中的容量预留和去重记录跨连接共享。容器默认选择 production，要求显式配置 HTTPS 与凭据。本地 Compose 示例选择 development。CI 在面向 develop、master 的 PR 上运行，同一提交通过门禁后，master 发布才能制作桌面和中心制品。强制拦截 PR 合并由 GitHub 分支保护的必需检查配置决定。[部署和镜像使用](../deploy/README.md)

## 平台和能力范围

| 交付物       | 当前范围                                                                                                                                                           |
| ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 中心         | 发布流程制作 Linux amd64 Docker 镜像归档及 SHA-256。开发机可按本机架构构建镜像。数据保存在 PostgreSQL 和持久产物卷中                                               |
| Web、桌面    | React 19 + TanStack Query + SSE。Electron 加载配置的 Web 地址。桌面发布流程构建 Apple 芯片 Mac 的 DMG 和 PKG，使用 Sparkle 更新                                    |
| 本机执行     | 适配 Codex、Claude Code、OpenCode、pi、CodeBuddy、Gemini CLI、Hermes。启动协议、权限档、MCP 和技能支持随适配器变化。Hermes 使用固定的 macOS arm64 构建及嵌入式摘要 |
| 操作系统验证 | 本机 daemon 服务管理和机器代理有 macOS 专有实现。Linux 中心容器的验证范围与桌面安装、Windows daemon、真实厂商账号及签名公证分别记录在发布验收中                    |

## 24 项发现的处理结果

| 编号 | 发现                       | 处理                                                                                                                       |
| ---- | -------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| 1    | PR 和发布缺少完整检查      | 新增可复用质量工作流，发布 job 依赖质量检查                                                                                |
| 2    | 缺库时数据库测试跳过       | 完整检查自行创建集群，覆盖集成目录和三个内部包，拒绝必需用例跳过。修复注册测试的组织夹具、目录测试调用和恢复后旧纪元的派发 |
| 3    | 中心镜像和发布入口缺失     | 增加非 root 镜像、Compose 构建、实际数据库冒烟和版本化归档                                                                 |
| 4    | 项目规则仅在本机可见       | 根目录跟踪 `AGENTS.md`，包含共享开发规则                                                                                   |
| 5    | 闲置的内存调度和容量模型   | 删除无生产调用方的 Engine、Scheduler 和 capacity，保留执行状态与 Run，生产派发沿用数据库事务                               |
| 6    | 集成包职责与命名           | 多渠道共用的配置改名为 `IMBotRuntime`，撤掉 API 初始化后被覆盖的一次服务赋值。渠道处理继续按各自协议维护                   |
| 7    | 配置层依赖 Office 业务包   | 预览服务地址校验归入配置包。Teams 会话路由继续共用协议类型和校验                                                           |
| 8    | 评论有多个写入点           | 交互、导入、设备工具和同步的身份、幂等键及事务语义不同。保留各路径，以现有数据库用例核对                                   |
| 9    | 鉴权散布于处理函数         | 用户、设备及外部回调有不同身份模型。检查由相关身份、授权、CSRF 和跨租户回归覆盖                                            |
| 10   | worker 与 API 同进程       | 部署说明标明进程和限流范围。独立扩容仍需实测业务负载                                                                       |
| 11   | 业务目录体量大             | 前文给出领域入口。本次整理配置、调度和渠道类型职责，目录规模用于后续维护评估                                               |
| 12   | OpenAPI 与实现有差异       | 补 16 个业务操作和健康检查别名，约束飞书、Lark、钉钉的操作差异，重新生成 TypeScript。任务类型直接投影生成类型              |
| 13   | 迁移 README 索引不全       | 索引覆盖 1–175 及当前文件名。测试逐项核对顺序和链接                                                                        |
| 14   | 文档入口及发布引用失效     | 文档首页补代码、集成及开发记录入口，发布清单指向实际支持说明与验收脚本                                                     |
| 15   | 设备事件样例和旧编号表     | 按 JSON Schema 校正正例，CI 检验正反例。索引链接历史重编号表，保留来源和校验和                                             |
| 16   | 174、175 的开发记录        | 已有本机电脑设置开发记录，迁移页补上入口                                                                                   |
| 17   | Hooks 规则未执行           | 在 lint 中启用调用顺序与依赖检查，修正实际 Hook 依赖                                                                       |
| 18   | 重复存储和渲染逻辑         | 创建任务及表情面板复用容错读写。聊天 Markdown 和简化预览共用链接过滤。SheetJS 与 BetterOffice 分别服务导入和预览           |
| 19   | 阶段编号影响定位           | 新说明按业务领域组织，保留已有夹具、脚本与历史记录的可追溯名称                                                             |
| 20   | 测试辅助代码可能进入发布包 | 用生产构建的 source map 核对实际模块。本轮 35 个 JS map 中未检出 Testing Library。测试代码继续参与类型检查                 |
| 21   | 中心版本固定为 dev         | 构建时注入版本，镜像冒烟核对 `version` 输出                                                                                |
| 22   | 开发专用环境开关           | 镜像显式默认 production，开发 Compose 单独选择环境。已有配置校验覆盖公共地址和开发验证码                                   |
| 23   | 平台支持边界不易查找       | 上表区分中心归档、桌面安装包、本机适配器及外部验收条件                                                                     |
| 24   | 生成类型和 Hermes 清单较大 | OpenAPI 是接口源，类型由 `pnpm gen:api` 生成。Hermes 清单实际参与运行时校验，来源与准备脚本位于 `tools/hermes`             |

本轮实施细节和观察到的验证结果见[OPE-8 开发记录](development-records/2026-11-25-ope-8-code-quality.md)。
