# OPE-8 · 后台服务的 Runtime 发现

## 现象与范围

在一台 macOS arm64 开发机上，HiFox 0.4.569 报告了六个 CLI。OpenFox 0.1.3 使用 launchd 提供的 `/usr/bin:/bin:/usr/sbin:/sbin` 时只识别 Codex 和 Claude Code。给原有探测器加上本机 NVM 的 bin 目录后，OpenCode、Pi、Gemini 也能被发现。Cursor 原先没有探测器。

本次对齐的是同机可观察的安装发现、手动路径、启动缓存和心跳行为。HiFox 在多个非活动 Node 版本之间的全部排序规则尚未证实。模型请求、供应商账号登录和新增 Cursor 执行协议不在本次范围内。

## 实现与决定

1. 各 CLI 共用候选查找。依次考虑设备指定的入口或启动参数、对应环境变量、后台服务 PATH、登录 shell PATH、用户与系统工具目录。macOS 最后尝试应用内置的 Codex。单次扫描共用一次登录 shell，按规范路径去重。已激活的 Node 优先，剩余 NVM、fnm 安装按数值版本从高到低尝试。这一排序是 OpenFox 的选择。
2. 每个适配器验证候选。Claude Code、OpenCode 和 Pi 同时检查版本和所需的 CLI 选项或子命令，避免把同名程序算入。Codex 保留真实 CLI 返回的预发布版本。Gemini 优先使用其 CLI 同目录的 Node，仍执行原有的原版制品、Node 和沙箱检查。Cursor 通过独立探测器登记日期版本，不注册执行适配器。
3. 已安装清单先于模型枚举写入心跳。设备页面显示安装入口、命中来源及状态。用户目录显示为 `~`。Cursor 标记为“已发现，暂无执行适配器”。Gemini 继续要求每次 Run 配置模型和凭据。
4. 手动保存使用设备所有者原有的设置命令、设备本地候选清单和乐观版本校验。无效路径或旧版本提交保留先前设置。提交空字符串恢复自动发现。被选安装随后失效时报告失败，保留设置供修正。
5. daemon 在本机 journal 同目录原子写入权限为 0600 的 `runtime-discovery-cache.json`。记录版本、来源、路径、链接目标、文件大小、修改时间、检测时间及搜索环境指纹。重启时，仅重用 72 小时内且匹配的安装记录。首次上报将它们标记为“正在验证 Runtime”，登录、模型和执行能力须由实时探测重新确认。周期探测沿用 10 分钟间隔，手动保存会触发重新扫描。

## 同机验收

| Runtime | 检出的安装入口 | 版本 | 实时报告 |
| --- | --- | --- | --- |
| Claude Code | `~/.local/bin/claude` | 2.1.332 | 可运行 |
| Codex | `/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex` | 0.171.0-alpha.13 | 可运行 |
| Cursor | `~/.local/bin/cursor-agent` | 2026.10.22 | 仅发现 |
| Gemini CLI | `~/.nvm/versions/node/v22.23.1/bin/gemini` | 0.67.0 | 协议就绪，需运行配置 |
| OpenCode | `~/.nvm/versions/node/v22.23.1/bin/opencode` | 1.19.5 | 可运行 |
| Pi | `~/.nvm/versions/node/v22.23.1/bin/pi` | 1.5.0 | 可运行 |

表内四个“可运行”来自适配器的实时报告，本次没有发起模型请求。同机六个安装入口与 HiFox 一致。HiFox 的 Codex 版本显示为 0.171.0，OpenFox 显示该二进制返回的完整后缀。HiFox 的五个用户工具命中其后台 PATH。OpenFox 在最小 PATH 下能从登录 shell 或常见工具目录找到相同入口。CodeBuddy 和 Hermes 在该机没有通过探测。

浏览器验收使用临时 PostgreSQL、测试账号、单独注册的设备和新编译的 daemon。验证了真实心跳、Cursor 状态及按钮、OpenCode 错误路径拒绝、正确路径保存、恢复自动发现，以及重启后从六条缓存记录切换到六条实时记录。机器上原有常驻服务未被替换。截图、最小 PATH 探测结果、启动和实时心跳均保存在任务工作区的 `.hifox-task/evidence/`。

## 配置、接口与回退

- `contracts/openapi.yaml` 的设备 Runtime 增加可选 `install_path`、`discovery_source`、`discovery_only`、`discovery_cached`。设备偏好新增可选 `runtime_paths`，允许 `claude-code`、`opencode`、`pi`、`codebuddy`、`gemini` 和 `cursor`。Codex 复用 `codex_path`。路径上限 1024 个字符（设备端亦限制为 1024 字节）。已运行 `pnpm gen:api` 同步客户端。
- 增加可选环境变量 `OPENFOX_CURSOR_PATH`，用于本机指定 Cursor CLI。其他供应商沿用各自现有设置。没有更改部署配置文件、默认服务 PATH 或第三方依赖。
- SQL 与迁移均无变更。设备路径使用既有 SQLite 偏好 JSON 和条件更新，中心沿用心跳 JSON。缓存文件可由 daemon 自动重建。
- 单一误配置可通过电脑详情页恢复默认路径。整项回退可撤回本分支的代码提交并换回原 daemon、服务端和前端，无需数据回滚。

## 检查与限制

- `go test -p 1 ./cmd/openfox ./internal/runtime/... ./internal/daemon ./internal/daemon/supervisor ./internal/devices ./internal/runtimepolicy` 通过。覆盖候选顺序、失败回退、手动优先级、缓存过期和文件替换、过期设置版本被拒绝、重启恢复、安装清单先于模型及 Cursor 无执行资格。对应改动后另行复测相关包及 `./tests/integration/stage2`。
- `go vet` 已覆盖本次影响的包。`make check-contracts` 通过并校验 175 份既有迁移。`pnpm lint`（含类型检查）及 `pnpm build` 通过。电脑页面测试 8 个文件、145 项全部通过。
- 全量 `make check` 的 stage8d 测试在基线即无法编译。`work_directory_path_test.go:164` 使用三个参数，当前 `ReadWorkDirectoryPath` 需要四个。首次全量并行运行还出现了时间上限失败，受影响的 Runtime 和 supervisor 包已低并发复测通过。
- 以 `VITEST_MAX_WORKERS=3 pnpm test` 重跑完整 Web 测试，2317 项中 2316 项通过。唯一失败为组织切换后导航记忆丢失任务查询参数（`packages/app/src/shell/navMemory.test.tsx:380`）。在未修改的 `master@1f4d3db` 快照单独重跑此用例，得到相同差异；在该快照也复现了 stage8d 编译错误。串行重跑 `make check` 仅余上述 stage8d 失败。桌面套件单独运行 `pnpm --filter @openfox/desktop test`，128 项通过。
- 重启验收曾发现清空路径后的空映射与重新读取时的 nil 产生不同指纹。统一两者的指纹后，已通过真实 daemon 重启并补充回归测试。
- 同机比较限于 macOS arm64；其他系统、HiFox 未公开的候选排序，以及供应商账号真实请求均未做等价性验证。
