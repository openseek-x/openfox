# macOS 桌面更新与 GitHub Actions 发布

2026-11-23 · OpenFox 0.1.2 · macOS arm64

## 用户路径和实现

发行包启动 30 秒后、运行中每 24 小时以及手动检查时，读取构建时固定的 HTTPS 发布源。发现更高版本会后台下载 PKG。主进程复用工作台代理，省略凭据、拒绝重定向并核对 SHA-256，限制 512 MiB 和 20 分钟。托盘、关于页和定时器共享一个进行中的检查。

原生弹窗显示版本号，默认和取消选项都是「稍后」。只有点击「打开系统安装器」才把经过校验的本地文件交给 macOS Installer，用户在系统界面中确认安装。关闭弹窗、普通退出和最低版本策略均不会触发安装。待确认文件在私有临时目录中，已交给 Installer 的文件留在本地应用缓存并保留隔离属性，启动已安装的新版本后清理。首次安装或从 0.1.1 升级可直接下载同一个 PKG。安装完成后重新打开桌面端，新内置 CLI 沿用已有的本地服务重启策略。

初版在签名应用上使用 Squirrel。原生测试证实将 ZIP 交给 Squirrel 即可能触发退出安装，因此安装必须位于明确确认之后。用户随后决定暂时不使用 Apple Developer ID，并选择系统安装器。发行包改用 productbuild 打包 ad hoc 签名的应用，系统管理安装与授权。没有 Apple 公证，安装是否获准取决于设备策略。

## 发布配置和运行顺序

- 桌面 package.json 版本升至 0.1.2，加入发行构建命令、更新模块和测试。release-config.cjs 要求 HTTPS 默认地址与三段数字版本，启用发行更新入口。先验证 arm64 应用，再生成 PKG 和按实际文件计算的 latest.json。
- GitHub Actions 由 desktop-vX.Y.Z 标签触发。macOS arm64 构建任务核对源码版本、运行桌面与设置页测试和类型检查，并解包复核应用标识、版本和签名。独立部署任务只接收验证后的 Web 与桌面制品。
- 仓库变量 OPENFOX_PUBLIC_URL 固定发布源。desktop-production 环境限发布标签使用。OPENFOX_DEPLOY_TARGET 和 OPENFOX_KNOWN_HOSTS 给出目标与固定主机密钥，OPENFOX_DEPLOY_KEY 是专用环境密钥。原服务器管理密钥留在操作者机器上。
- 服务器 root 拥有静态发布命令。部署公钥通过 restrict 和 forced command 限定到此入口。部署任务用只读 Actions 权限取得短时制品地址，经 SSH 传给服务器。服务器从 Actions 存储并行下载，核对上传动作输出的 ZIP 摘要，校验包内路径、大小、版本和 PKG 摘要，持发布锁原子切换 current，并验证公网 HTML、清单和安装包范围下载。失败时恢复原指针。旧的静态资源供已打开页面继续使用。
- 中心二进制、服务配置和数据库不参与本次切换。无 SQL、Schema 或数据迁移。回退 Web 与桌面更新清单时，持相同发布锁切回日志记录的 previous target。已经安装新版的设备需要更高版本修复或手动重装。发布环境的撤销方式为移除专用公钥及 GitHub 环境密钥。

## 验证和发布记录

- 本机已构建 0.1.2 PKG。解包后验证 OpenFox.app 包标识、版本及嵌套代码签名，安装包的实际 SHA-256 与生成清单一致。生产采用 GitHub 构建产物的摘要。
- 桌面回归覆盖下载、摘要错误、用户暂缓、必需更新、并发检查、清单撤回和再次确认。系统安装器测试模拟打开操作，验证了交付文件的内容、持久性以及升级后的清理。测试未向操作者的 Applications 目录安装软件。
- 本次发布以先前交付 0.1.1 的提交为源基线，只叠加本任务的改动。隔离重建基线时，359 项静态文件摘要中 354 项与上次本地发布清单相同，另 5 项为入口和带哈希的 JavaScript 构件。本次上线以发布构建的实际完整制品为准。
- 发行提交在本机通过 130 项桌面测试、35 项设置页测试及工作区类型检查。首次 GitHub 运行成功构建并解包验证 PKG。跨境 SSH 上传数分钟仅传输约 4 MiB，无法在部署时限内传完约 195 MB 制品，已在生产切换前取消。服务器对 GitHub 制品存储的并行 HTTPS 范围请求实测成功，随后改用该通道。保留首次发布标签，用 `-r2` 标签重试同一桌面版本。[首次运行](https://github.com/openseek-x/openfox/actions/runs/45158266801)
- 2026-11-23 19:13（北京时间），标签 `desktop-v0.1.2-r2` 对应的提交 `ae5c36e` 完成生产部署。构建和部署作业均成功，构建包含 4 项发布脚本测试。GitHub 制品 ZIP 的 SHA-256 为 `55d27cb11263c1dcc61bea4df581182678e528047bd04364db840f54a7d77ac5`。[成功发布记录](https://github.com/openseek-x/openfox/actions/runs/45161066440)
- 2026-11-24 从公网完整下载 PKG，HTTP 200，大小为 135,870,671 字节。其 SHA-256 为 `e920f7e91e4873e92680d968796f484d07b2cbcc1c23d89f072d26bdfebea4a4`，与线上更新清单、服务器文件和 CI 制品一致。用 `pkgutil --expand-full` 确认应用标识为 `io.openfox.desktop`、版本为 0.1.2、可执行文件为 arm64，`codesign --verify --deep --strict` 通过。公网登录页与 CI 内的 HTML 摘要一致，中心服务为 active。

## 2026-11-24 改为 GitHub 标签发布

用户确认手动安装使用 DMG，应用内更新使用需确认的 PKG，版本与下载均取自 GitHub。已上线的 0.1.2 二进制绑定旧地址，所以修订客户端版本为 0.1.3。0.1.2 的正式标签指向已部署提交 ae5c36e。将该次 CI 产出的应用原样装入 DMG，挂载后验证签名并逐文件比对 PKG 中的应用。DMG 和原始 PKG 均已核对 GitHub 附件摘要。[0.1.2 Release](https://github.com/openseek-x/openfox/releases/tag/v0.1.2)

0.1.3 从官方仓库的最新正式 Release 读取 vX.Y.Z 标签、对应架构的附件地址和 SHA-256。生产客户端后台下载 PKG，默认暂缓，选择「打开系统安装器」后才进入系统安装界面。手动下载使用 DMG。Electron 的 session.fetch 会取消手动重定向，因此使用同一代理会话的 net.request 检查 GitHub 附件跳转，再从官方 CDN 下载并核对摘要。API 限流或无法访问时保留当前版本，可再次检查。

配置变更包括桌面 package.json 版本、release-config.cjs 的 DMG 目标、desktop-release.yml 的 v* 标签条件与正式 Release 发布任务。desktop-production 环境已允许 v* 标签，发布任务单独使用 contents: write 权限。OPENFOX_PUBLIC_URL 用于工作台地址及向已安装的 0.1.2 提供同一 PKG。新版客户端的下载来源固定为官方 GitHub 仓库。构建验证 DMG 与 PKG 内容一致，附件摘要核对后公开 GitHub Release，随后部署工作台和旧客户端的更新入口。发布失败可在同一运行上重试失败作业。修复已安装的版本需发布更高版本；静态服务可按上文恢复原目录。无 SQL 或数据库迁移。

标签 `v0.1.3` 指向提交 `3d72409`。2026-11-24 14:57（北京时间），Actions 公开 GitHub Release，15:07 完成生产工作台与旧更新清单部署。三项作业全部成功，125 项桌面测试、35 项设置页测试、4 项发布测试及类型检查通过。正式 DMG 与 PKG 的 SHA-256 分别为 `818a0aeac403135580499cf3978a3691d50181860b3aa072f0ce52fb2da35efe` 和 `797e7625f11e645aa4e2906386adfe3c275c45b3af03b16342420c13b1e61613`。[0.1.3 Release](https://github.com/openseek-x/openfox/releases/tag/v0.1.3) · [成功运行](https://github.com/openseek-x/openfox/actions/runs/45343384946)

实测 Electron 主进程通过 GitHub 获取标签及官方 CDN 地址，完整下载正式 PKG，校验摘要后清理，未打开 Installer。公网 DMG 下载期间本机代理出现 TLS 中断，按范围重试并还原完整文件后，SHA-256 与 GitHub 一致。只读挂载后确认应用版本为 0.1.3、主程序为 arm64、签名完整，包内的三个更新相关模块与源码逐字节一致，发行更新开关已开启。旧端的公网清单指向 0.1.3，PKG 摘要与 GitHub 附件一致。新版以公开稳定标签选择更新，自建清单的 min_version 及 OPENFOX_DESKTOP_UPDATE_HOSTS 不参与新版选择。

## 2026-11-24 从 master 推送自动发布

发布入口改为 `master` 的 push，包括 PR 的合并提交。此前 0.1.3 发布标签位于单独的发行历史。此次把完整工作流经 develop 合入 master。CI 核对检出的提交等于事件中的 SHA，测试、打包后在同一 SHA 创建稳定版本标签。工作流在运行器本地写入版本，主、次版本来自桌面 package.json，补丁号取 GitHub 工作流运行序号。沿用前序运行号 3，下一次预计为 v0.1.4。构建失败可能留出版本号空档，重试同一运行保留原号。Git 历史中的 package.json 保留开发版本。

`.github/workflows/desktop-release.yml` 的触发条件改为推送 master，发布队列采用 GitHub 的 queue: max。并发推送依次处理，平台最多允许 100 次运行排队。已有标签仅在指向相同 SHA 时可复用，较旧版本不能取代 GitHub 最新版本或切换生产站点。生产环境 desktop-production 的部署来源改为 master 分支。构建凭据、部署密钥、下载格式及安装确认流程沿用现有配置。无 SQL 或数据库迁移。[GitHub 并发队列说明](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)

本地验证了工作流触发条件、所有 shell 步骤的语法，以及运行号 4、5、5 对应的包版本 0.1.4、0.1.5、0.1.5。用模拟的 GitHub API 验证了新版本发布、较旧运行不提升为 latest、同名标签与 SHA 不符时终止。生产运行的发布摘要链接到实际构建提交及 Release。恢复旧触发方式需同时恢复工作流和环境的标签部署规则。已上线版本保留其原有标签和附件，修复使用后续 master 推送发版。

## 2026-11-24 应用内确认安装 DMG 并重启

桌面端复用定时、托盘和关于页的检查与代理下载。先核对 GitHub Release 上 DMG 与同名 `.dmg.sig` 的 SHA-256，再用构建时内嵌的 Ed25519 公钥验证 DMG。准备就绪后显示「安装并重启」和「稍后」。暂缓保留进程内已验证的文件；退出清理临时目录。只有原生确认框的肯定答复才启动 Sparkle。关于页的「查看更新…」打开该确认框。安装发起的退出复用已有授权，本地服务和已有任务继续运行。

选择 Sparkle 2.10.1 处理应用包替换、签名检查与重新启动。构建脚本验证官方二进制 SDK 和同标签 CLI 源码的固定 SHA-256，用系统 clang 编译 CLI 并将带许可证的辅助应用放入 Contents/Helpers。Sparkle 仅接受 HTTP(S) 更新包地址；确认安装后，主进程在随机的 IPv4 回环端口及随机路径上提供本次已验证的 DMG，避免系统更新器用另一套代理重复下载。Sparkle 在解包前再次验签。保留原发布流水线的 PKG 产物和静态清单，已有客户端可以使用原入口获取带新更新器的应用。原版本遇到系统安装器拦截的用户可从本次发布的 DMG 手动安装一次。

### 发布配置

- `apps/desktop/package.json` 的发行打包命令先构建辅助程序，同时将主进程的 Sparkle 安装模块打包。普通开发构建沿用手动下载。
- `apps/desktop/release-config.cjs` 将辅助程序及公钥写入发行包，将 CFBundleVersion 固定为本次发布版本；启用 Sparkle 解包前验签，安装由应用内确认触发。`apps/desktop/sparkle/Info.plist` 定义无 Dock 窗口的独立更新辅助程序。
- `.github/workflows/desktop-release.yml` 验证 DMG 内的签名、公钥、版本和辅助程序，在独立步骤签署已完成构建的 DMG；发布前核对 DMG、签名文件及 PKG 的 GitHub 摘要。
- 仓库变量 `OPENFOX_UPDATE_PUBLIC_KEY` 存放 Base64 原始公钥，仓库 Secret `OPENFOX_UPDATE_PRIVATE_KEY` 存放 PKCS#8 PEM 私钥。已按维护者授权配置并核对远端公钥。正式 Ed25519 种子的备份在维护者本机登录钥匙串，服务为 `io.openfox.desktop.release-signing`、账户为 `openseek-x/openfox`。密钥不匹配会中止签名。后续版本复用此密钥。
- 不涉及 SQL、数据库 Schema、数据迁移或服务配置。

### 验证与回退

在 macOS 15.7.7 arm64 运行桌面测试 125 项、关于页所在测试文件 35 项、静态发布测试 4 项和工作区类型检查，全部通过。覆盖签名不匹配（包括同步改写摘要）、签名文件超限、网络中断、暂缓、重复检查、发布撤回和失败重试。发行工作流通过 YAML 和内嵌 shell 语法检查。

本机使用已安装的 Electron 45.0.0 构建 DMG 和 PKG。首次构建因下载 Electron 时 DNS 解析失败，改用相同版本的本机发行文件完成。用两份隔离的真实 Electron 应用包、独立应用标识、临时用户目录和测试密钥验证生产主进程。测试夹具保留 Electron 查找子进程所需的应用名称；模拟 GitHub 发布响应、文件传输和原生按钮选择。4.0.0 首次选择「稍后」，仍为同一进程；第二次确认后 Sparkle 安装 DMG，4.0.1 从原位置以新 PID 启动，版本和深度代码签名检查通过。其间未出现额外的退出确认框。本机日常使用的 OpenFox 保持 0.1.3。

设备若禁止替换应用，用户可以从正式 Release 的 DMG 安装。需要回退时，将修复或还原提交作为更高版本重新发布；保留同一签名密钥，使已安装的 Sparkle 客户端能够验签。正式用户设备上的授权提示仍由 macOS 策略决定。
