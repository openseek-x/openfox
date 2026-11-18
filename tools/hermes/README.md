# Hermes 固定构建准备

本材料对应用户已批准的 R05 公网模型方案、可信读取/搜索入口和临时快照范围。协议探测只证明安装与 ACP 就绪，不等于模型认证或供应商端到端验收。

仅适用 macOS arm64：Hermes v0.23.1，固定 commit `3b421578588243f920886e6fe79a6faa87ea026e`，Astral Python 3.14.9，uv 0.13.5，66 个固定 core/ACP wheels，以及官方 ripgrep 15.2.0 arm64。不会安装完整可选工具集、修改真实 HOME、LaunchAgent、Keychain 或现有 Hermes 服务。

所有下载 URL、大小与 SHA-256 均在 [artifacts.lock.json](artifacts.lock.json)。Hermes 来自 NousResearch 官方固定 commit；Python/uv 来自官方 Astral release；wheels 是该版本官方 PM 锁与依赖闭包中已核验的 PyPI 发行物。不是从 PATH 发现任意 `hermes` 或 `rg`。OpenFox 的内嵌可信 Python 入口安装固定工具门禁后，以 `runpy` 进入官方 `hermes_cli.main`，不修改第三方源码。

在仓库根目录，由设备所有者选择两个新的目录（示例目录已存在时先换名称，不覆盖）：

```sh
python3 tools/hermes/download.py --directory /private/tmp/openfox-hermes-artifacts
python3 tools/hermes/prepare.py --artifacts /private/tmp/openfox-hermes-artifacts --destination /private/tmp/openfox-hermes-install
```

第一条只下载并校验 70 个固定文件，已有不同文件会拒绝覆盖。第二条再次核验全部文件，离线提取官方源码/Python/uv，使用 `--offline --no-index --no-build --no-deps --require-hashes` 安装固定依赖闭包。准备阶段仅在新目标中删除 66 个 uv 生成的 `*.dist-info/uv_cache.json` 缓存时间索引，使不同 cache 位置的构建一致；这些不是 wheel 运行代码。运行时仍拒绝额外 uv 索引，不忽略未知文件。目标必须不存在；失败后目录保留供检查，不自动删用户文件。脚本不运行 Hermes，不获取账户或模型，不注册持久服务。`requirements.txt` 是完整 66 包固定集合，`--no-deps` 禁止扩展求解新包。

然后在启动 OpenFox daemon 的同一环境中设置准备输出的两条绝对路径：

```sh
export OPENFOX_HERMES_ROOT=/private/tmp/openfox-hermes-install/source-unpack/hermes-agent-3b421578588243f920886e6fe79a6faa87ea026e
export OPENFOX_HERMES_PYTHON=/private/tmp/openfox-hermes-install/runtime/venv/bin/python
openfox runtime probe --provider hermes --timeout 30s --json
```

本例临时目录适合验证。长期使用可由设备所有者选独立可写安装位置，再重新离线准备；不能把安装放在 `.hermes`、`.ssh`、`.aws`、`__pycache__` 或 `.env*` 目录下，这些路径被运行沙箱明确拒读。不要只复制 venv 的 `bin/python`。不支持 `--executable`、`--supported-version`、自行修改源码或额外 wheels。

探测核验内嵌可信清单的 29,608 个文件/链接身份，并在沙箱内执行 `acp --check` 和 `initialize`。官方 ACP 自报版本可能为 `0.0.0`，不是可信版本依据。安装生成的 66 个 `*.dist-info/RECORD` 会随 console script shebang 路径改变，属于不执行的安装索引；不计入可信文件集，原生沙箱逐路径拒读。固定 rg archive SHA-256 为 `3750b2e93f37e0c692657da574d7019a101c0084da05a790c83fd335bad973e4`，可执行文件为 `a326a1fb48074202e9ad41e4cd1e389eeea372c8c6f7d7e80da81176d5d9430e`。其他源码、Python、库、元数据按固定 SHA 校验；额外 `.pth`、修改的代码或缺失文件拒绝。既存 `__pycache__` 不受信任且拒读，使用新 Run 的独立 cache prefix。

成功返回 `protocol_ready=true`、`requires_run_configuration=true`、`runnable=false`、`login_status=UNKNOWN`。这只表示固定协议就绪，没有调用模型、验证账户或测量供应商容量。

模型输入通过现有 Agent / 电脑 Runtime 默认值界面保存，不放进这两个设备路径变量：

| 名称 | 行为 |
| --- | --- |
| `HERMES_API_KEY` | 必填无默认；加密保存，Run 内真实 key 仅进入 Go 转发器内存/唯一 HTTPS 目标。 |
| `HERMES_BASE_URL` | 必填无默认；公网 DNS HTTPS:443 API base，如 `https://provider.example/v1`，不带完整 `/chat/completions`、查询、片段或转义路径。 |
| `HERMES_MODEL` | 当已有模型解析结果为 `default` 时使用；须为明确模型名，不能 `auto/default`。 |
| `HERMES_CONTEXT_LENGTH` | 必填无默认；ASCII 十进制 `64000..2147483647`；是写入者声明值，不是供应商已验证事实。 |

仅标准 Chat Completions；不支持企业代理、保留地址 DNS、DoH fallback、回环/私网模型、重定向、自定义 CA/header。每次 Run 新建只读工作目录授权、独立 HOME/profile/native session 与前台 gateway；精确自有 loopback 端口可用，其他出站由沙箱拒绝。真实 key 不传原生。Run 完成后关闭转发器、回收自有子进程、删除实际 Run 目录；为了避免官方长 Unix socket 路径回退到共享地址，长 daemon 状态路径下改用短的专属 `/private/tmp/openfox-hermes-*` 根。

工具只开放 `read_file` 与 `search_files`，READ_ONLY 和 PLAN 使用同一边界；没有 shell、写入、插件或 managed worker。读取通过已授权工作区的 dirfd 与 `O_NOFOLLOW` 打开普通文件；可信代码/系统库的加载许可不成为模型读取许可。私有数字后缀 `openfox-hermes-*` Run 命名空间不可用作工作区/工具输入，包含本 Run 状态根的宽目录也拒绝。

搜索仅支持单工作区内单一根的 `target=content`、`order=discovery`，输出 `content/files_only/count`；limit 1..200（默认50）、offset≥0 且 offset+limit≤2000、context 0..20。每次新建自有临时快照：每文件8MiB、总64MiB、10,000文件、50,000枚举条目、深度64、复制与搜索合计60秒、单并发；超限或源文件在复制中变化明确失败，不返回截断成功。普通仓库会保守剪枝明确忽略的依赖目录；复杂规则或宽 `file_glob` 可能需缩小搜索根。保持官方 rg 的工作区忽略规则、隐藏/二进制行为、敏感内容过滤和连续重复3次告警/4次拒绝；`.git` 控制文件只供忽略元数据，不能作为正文。外部 Git worktree 元数据和全局用户 ignore 不读取并给出提示，跨页无快照锁定保证，结果发现顺序可能变化。返回路径映回源工作区。

每个原生参与者持有父存活只读 FD；OpenFox 进程被 SIGKILL 后收到 EOF，只终止自有直接 rg 子进程并清理本 Run 状态。信号许可只含自身和直接 children，不允许同 profile 兄弟/任意同用户进程。固定 gateway 的最终退出函数有仅父死亡时生效的薄包装，防止其 `os._exit` 跳过清理；没有常驻 guardian 或系统服务。正常取消与进程强制结束已分别受控验证。

已知依赖元数据冲突：官方锁/override 固定 Pillow 13.0.0，而 browser-harness 元数据仍写旧上界；原生 core/ACP 已受控验证，`uv pip check` 的这个冲突不能记成通过。可选浏览器/vision 功能不在本批范围。

回退：停止/排空 Hermes Runs，显式修改或归档其 Agent，再回退应用；保留迁移 138 扩大的约束和业务/加密数据。设备所有者可移除上述两条环境设置；OpenFox 不自动删除可信安装或下载缓存，也不切换到其他 Runtime。
