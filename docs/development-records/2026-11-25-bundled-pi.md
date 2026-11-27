# OPE-9 · 桌面安装包内置 Pi

macOS arm64 用户安装并打开 OpenFox 后，可以在未安装 Node、npm 或全局 Pi 的电脑上使用 Pi 运行时。模型账号仍由设备使用者配置。

## 分发与发现

桌面构建下载上游 Pi 1.5.0 的 `pi-darwin-arm64.tar.gz`，按固定 SHA-256 校验。签名前将完整发行目录及同标签的 MIT 许可证放入 `Contents/Resources/pi`，DMG 与 PKG 使用同一份程序。构建也接受经过相同校验的本地归档。[Pi 1.5.0](https://github.com/earendil-works/pi/releases/tag/v1.5.0)

桌面首启创建私有链接指向当前应用的 Pi 目录。移动应用后重新启动会原子刷新链接。设备端沿用现有检查顺序和 Pi 协议验证，在显式配置、环境变量和常规外部安装之后尝试内置入口，并以 `macos_app` 上报来源。安装包、设备上的 Pi 配置和模型凭据分别存放，升级应用时用户继续使用原有模型配置。

## 复核

| 场景 | 实际结果 |
| --- | --- |
| DMG 安装后首启 | 从只读挂载的 DMG 复制至含空格的目录，在空白用户目录启动应用后退出。独立 CLI 在仅有系统命令的 PATH 中探测到 Pi 1.5.0，来源为 macos_app。 |
| 执行 | OpenFox 的 Pi 适配器实际启动包内程序，调用本地模拟模型一次，返回 PACKAGED_PI_OK 并记录会话。供应商响应为 SIMULATED。 |
| 重复启动、移动和路径覆盖 | 链接刷新及发现顺序测试通过；错误的显式入口报告失败。已有非链接文件保持原内容。 |
| 构建完整性 | 联网与本地归档均成功；摘要不符的归档在解压前被拒绝。 |
| 分发检查 | 本地发布构建使用测试更新公钥；PKG 与 DMG 内的应用校验签名，且包内运行时目录逐文件比对。 |

复测可使用 `pnpm --filter @openfox/desktop dist:mac` 构建并打开桌面应用，然后运行 `openfox runtime probe --provider pi --json`。在另有外部 Pi 的电脑上，可从返回的 `discovery_source` 和 `executable` 判断当前使用的安装。测试无需平台账号；真实供应商登录和生产更新发布未验证。
