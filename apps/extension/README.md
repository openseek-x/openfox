# OpenFox Chrome 侧边栏

浏览网页时，可以在 Chrome 侧边栏选择 OpenFox 空间和 Agent，开始新对话或接续历史会话。勾选“引用当前页面”后，这一次发送的消息会带上页面标题、地址和选中的文字。

## 安装与连接

1. 在仓库根目录运行 `pnpm install --frozen-lockfile` 和 `pnpm --filter @openfox/extension build`。
2. 在 Chrome 116 或更新版本打开 `chrome://extensions`，开启开发者模式，点击“加载已解压的扩展程序”，选择 `apps/extension/dist`。
3. 在 OpenFox 网页版设置里打开“个人访问令牌”，创建并复制令牌。
4. 在任意网页点击工具栏上的 OpenFox 图标，输入工作台网址与令牌，点击“连接”并允许访问该服务器。支持 HTTPS；本机开发还可以使用 `http://localhost:8080` 或 `http://127.0.0.1:8080`。只填写来源地址，省略路径。

工具栏操作也可以通过 Alt+Shift+C（macOS 为 Option+Shift+C）触发。快捷键冲突时可在 `chrome://extensions/shortcuts` 设置。

## 对话与引用

- 选择空间与支持单聊的 Agent，输入文字后点击“发送”，或按 Ctrl+Enter / ⌘+Enter。第一条消息创建私有会话。
- 在“历史会话”中打开当前空间已有的对话，“加载更多会话”和“查看更早消息”可继续翻页。已关闭的会话可阅读。
- 要引用网页，先在目标网页调用扩展，选中文字并勾选“引用当前页面”。扩展读取主页面的选区，地址保留来源与路径，选区上限为 8,000 个字符。成功发送后，勾选自动清除。换到其他网站时需要重新点击扩展图标取得临时访问权。
- 网络中断后，可以用原草稿重试。扩展在当前面板内复用上次发送时的正文、页面快照与消息 ID。关闭面板会结束这次草稿。
- 回复通过 OpenFox 的消息接口更新。运行 Agent 需要它在 OpenFox 中具备可用的执行环境。

## 权限与账号

| 权限                     | 使用时机                                                 |
| ------------------------ | -------------------------------------------------------- |
| `sidePanel`              | 用户点击图标或快捷键时打开侧边栏                         |
| `storage`                | 校验账号后，将地址与令牌保存在该浏览器的扩展本地存储     |
| 可选站点权限             | 用户连接时，按填写的服务器主机申请，调用现有 OpenFox API |
| `activeTab`、`scripting` | 用户主动发送网页引用时，在当前标签页读取标题、地址与选区 |

点击“断开连接”可清除本机保存的令牌。要吊销令牌，请在 OpenFox 的个人访问令牌设置中操作。接入使用现有令牌权限。本版覆盖 Muse 风格的侧边栏对话和网页引用，Agent 操作浏览器、个人设备 MCP、Meta 云端连接器不在交付范围内。

## 开发校验

```sh
pnpm --filter @openfox/extension typecheck
pnpm exec vitest run apps/extension
pnpm --filter @openfox/extension build
```
