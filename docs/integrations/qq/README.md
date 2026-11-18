# QQ 组织接入部署

I14 接入普通 Agent 的 QQ 单聊和群 @；不是 QQ 用户登录器，不支持任务助手、通知、附件或主动推送。界面扫码调用腾讯公开 SDK 的 `startQrConnect`，不自行实现 SDK 的内部协议。平台审核、真实扫码、账号权限及公网回调投递尚未在真实 QQ 环境验收；本仓库的验证使用固定 SDK 原生进程、隔离 TLS、真实 HTTP/PG/worker/UI。

## 固定组件及准备

使用 Node **22.23.1** 和 `@tencent-connect/qqbot-connector` **1.2.0**，唯一运行依赖 `qrcode-terminal` **0.12.0**。不运行 npm、安装脚本或依赖自动解析。不复制 SDK 实现到 OpenFox；SDK npm 元数据声明 `UNLICENSED`，使用方需核对其适用许可。qrcode-terminal 保留包内 Apache-2.0 LICENSE。

从以下原始发布地址取得两个归档，校验后只在全新专属目录准备：

- [腾讯 SDK 1.2.0](https://registry.npmjs.org/@tencent-connect/qqbot-connector/-/qqbot-connector-1.2.0.tgz)：SHA-256 `f3cca8f961051697d0ffc00e784755dfef637d89164725cb441eaf9e616b2bd9`。
- [qrcode-terminal 0.12.0](https://registry.npmjs.org/qrcode-terminal/-/qrcode-terminal-0.12.0.tgz)：SHA-256 `3a6260c4e0d80bd527a3f930e90ea2348c03646621f25aa0bd960ee205a0a706`。
- [Node 22.23.1 官方发布目录及 SHASUMS](https://nodejs.org/dist/v22.23.1/)；服务器应使用独立固定 Node 可执行文件。仓库 `provenance.json` 包含 macOS arm64、Linux amd64/arm64 的可执行文件 SHA-256。当前只实际运行过 macOS arm64，Linux 运行尚未验证。

在仓库根目录执行（示例路径须按部署环境选择；脚本不联网、不执行归档代码）：

```sh
python3 tools/qq-connector/prepare.py \
  --connector /absolute/downloads/qqbot-connector-1.2.0.tgz \
  --qrcode /absolute/downloads/qrcode-terminal-0.12.0.tgz \
  --target /absolute/deployment/qq-connector-1.2.0
```

脚本验证归档和全部 35 个文件，只接受普通文件，拒绝重复、符号链接、目录穿越及未列入清单内容。目标必须不存在。部署用户应拥有目标及所有祖先目录，避免其他用户写入；运行时拒绝组件自身组/其他用户可写、符号链接及额外文件。Node 文件必须有执行权限且不可被组/其他用户写入。

## 配置与执行顺序

1. 按原数据库流程应用 `000148_qq.sql`，在本批只针对新隔离库演练。迁移增加 QQ 专属控制、身份、收件、会话和回复表，并给 `im_reply_outbox` 增加 `source_terminal boolean NOT NULL DEFAULT false` 及要求 Run 来源的 CHECK。新建表不回填旧输入；无破坏性操作。
2. 保持服务器现有密钥加密配置，设置实际 HTTPS `OPENFOX_SERVER_PUBLIC_URL`。回调为该地址加 `/api/v1/hooks/qq/{secret}`；不要把含秘密地址写进日志或公开文档。
3. 设置下列两个键并重启同配置的服务实例。二者默认空、必须成对且为绝对路径。留空时不提供扫码能力，不下载或执行程序；QQ 加密与 HTTPS 地址可用性和扫码部署可用性分别显示。

| 键 | 值与影响 |
| --- | --- |
| `OPENFOX_QQ_CONNECTOR_NODE_PATH` | 固定 Node 22.23.1 可执行文件绝对路径；进程启动前校验 SHA-256。 |
| `OPENFOX_QQ_CONNECTOR_ROOT` | 上述准备目录绝对路径；每次扫码开始前校验固定 35 文件。 |

4. 有权限的成员选择当前可管理 Agent，界面显示腾讯官方二维码并在官方页面确认。服务端每实例最多 5 个自有扫码子进程，每会话 10 分钟；成功凭据密封保存，通过 Token 核验后才永久占用 AppID。
5. 在腾讯平台配置服务端显示的秘密回调地址、启用所需 C2C/群 @/好友删除/机器人移群事件；签名 challenge 通过后仍需一次有效签名事件才能激活。无效签名不接受；已验签事件的暂时持久化失败回复官方 `op=12,d=1`，提交后 `d=0`。
6. 本人发送一次性私聊验证码并回到 OpenFox 确认。群授权由群主/管理员在群 @ 机器人提交证明后，当前空间管理者明确同意公开回复；每个群候选身份须邀请对应 OpenFox 成员本人接受。两种标识不互相推断。

## 失败、诊断与回退

仅原始 QQ 输入所属 Run 的最终文字回原会话；一条最多 8,000 Unicode 码点、原消息 5 分钟窗口、固定 `msg_seq=1`。不会借新消息续期。未知远端结果不自动重发，界面只显示诊断状态。扫码停止也不代表远端 App 未创建；先等待本地 helper 退出或原截止时间＋停止宽限，再由管理员明确检查远端并结束记录。

本地断开使后续接收、执行及回复失效，保留历史和已核验 AppID。释放绑定只释放 Agent 槽位，不释放跨组织 AppID 占用，也不删除腾讯应用。换凭据会使旧身份/会话失效，需重新绑定。

回退先本地断开并等待运行排空，停止含 QQ 的 worker，再清空两个扫码配置键、重启回退应用；保留迁移列及专属表，不执行 DROP 或回填。远端应用与回调由部署管理员明确处理。此开发任务未执行生产迁移、真实账号操作、真实外发或部署。
