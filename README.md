# OpenFox

**OpenFox 是可自部署的团队 Agent 协作工作台。**团队在同一个空间里管理任务、Agent 和对话，将工作派发到已注册的电脑执行，并在工作台查看过程、结果与通知。

项目包含 Go 中心服务与本地执行端、React/TypeScript Web 工作台、Electron 桌面端和 PostgreSQL 数据库。适合希望自己部署协作平台、让 Agent 参与日常任务，同时保留组织、成员和执行设备管理能力的团队。

> **项目状态**：代码持续开发中。部分外部集成需要平台凭据；企业微信机器人、真实 SMTP 投递和 PingCode 个人评论写回尚未完成真实平台端到端验证。部署前请先在测试环境验证所需功能。

## 能做什么

| 能力         | 说明                                                                               |
| ------------ | ---------------------------------------------------------------------------------- |
| 团队任务协作 | 按组织和空间管理成员、任务、状态、评论、文档及附件；提供看板、列表和表格视图。     |
| Agent 执行   | 配置 Agent 与执行电脑，在对话或任务中发起运行，查看运行状态、事件和产物。          |
| 自动化与通知 | 按规则触发工作，集中查看站内通知，并按配置使用邮件或即时通讯渠道。                 |
| 外部工作流   | 连接代码托管和项目管理平台，导入任务或建立持续同步；具体能力与授权要求因平台而异。 |

## 为什么选择 OpenFox

- **工作上下文集中**：任务、对话、运行状态与产物在同一工作台，便于团队接续和追踪 Agent 的工作。
- **执行留在自己的电脑**：中心服务负责权限与调度，本地执行端运行 Agent；团队自行管理执行设备。
- **组织边界清晰**：按组织、空间和成员授权，组织数据由 PostgreSQL 租户隔离策略保护。
- **可接入现有工具**：通过导入、同步和通知渠道连接团队已有工作流，按需启用各平台能力。

## 项目结构

| 路径                              | 内容                                               |
| --------------------------------- | -------------------------------------------------- |
| `cmd/openfox-server`、`internal/` | Go API、身份与权限、调度、集成及后台任务           |
| `cmd/openfox`                     | 本地电脑注册、执行与管理命令                       |
| `apps/web`、`packages/`           | Web 入口、共享 React 界面、国际化与 API 客户端     |
| `apps/desktop`                    | Electron 桌面端，加载同源 Web 工作台并连接本地命令 |
| `db/migrations`、`contracts`      | PostgreSQL 前向迁移与版本化接口契约                |
| `deploy`、`tests`                 | 部署示例、恢复说明与测试                           |

## 本地开发

需要 Go 1.27.3、Node.js 22.22.2 或更新的 22.x 版本，以及 pnpm 11.15.1；仓库的本地 Compose 示例使用 PostgreSQL 17。先创建**专用开发数据库**，把连接 URL 写入仓库外、仅自己可读的文件；不要把密码提交到 Git。

```sh
export OPENFOX_DATABASE_URL_FILE=/path/to/openfox-dev-database.url
go run ./cmd/openfox-server migrate --check
go run ./cmd/openfox-server migrate --apply --database-url-file "$OPENFOX_DATABASE_URL_FILE"
```

迁移完成后，在第一个终端启动 API。开发环境可配置仅用于本地登录的六位验证码；生产环境会拒绝此配置。

```sh
export OPENFOX_ENV=development
export OPENFOX_AUTH_EMAIL_OTP_UNIVERSAL_CODE=123456
go run ./cmd/openfox-server serve
```

在第二个终端启动 Web 工作台：

```sh
pnpm install --frozen-lockfile
pnpm dev
```

打开 `http://127.0.0.1:4173/login`，用邮箱和上面设置的开发验证码登录。Web 开发服务器默认将 `/api` 代理至 `http://127.0.0.1:8080`。如果 API 地址不同，设置 `OPENFOX_WEB_API_TARGET`；更多说明见 [Web 开发文档](apps/web/README.md)。

桌面端需要先构建本地 CLI，再按 [桌面端文档](apps/desktop/README.md) 启动。它加载服务端提供的 Web 页面，不内嵌一份独立工作台。

## 验证与部署

```sh
make check          # Go 测试、vet 与迁移清单检查
pnpm check          # 前端格式、类型、测试与构建
```

`serve` **不会自动执行数据库迁移**。部署时先备份并检查目标数据库，再显式执行迁移、启动服务，最后发布 `apps/web/dist`；Web 与 API 应通过同一 HTTPS 域名提供。仓库中的 Compose 文件只是本地开发示例，不是一键生产部署方案。详见 [部署说明](deploy/README.md)、[迁移说明](db/migrations/README.md) 和 [API 契约](contracts/openapi.yaml)。

## 参与项目

欢迎通过 Issue 反馈问题或提交 Pull Request。修改接口后请同步更新 `contracts/openapi.yaml` 并运行 `pnpm gen:api`；提交前运行与改动相关的测试。

项目采用 [Apache License 2.0](LICENSE) 许可证。
