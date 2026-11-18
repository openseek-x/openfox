# CodeCommit → OpenFox 桥接

此目录提供单仓库的 EventBridge → Lambda，或 EventBridge → SNS 标准主题 → Lambda 示例。桥接使用 OpenFox 每仓生成的 URL 和 bearer token，转发完整 EventBridge envelope。它不是 SNS HTTP 签名接收器，也不会创建 AWS 资源。OpenFox 仍通过 AWS 权威读取验证事件后处理任务关联；收到 `202 accepted` 只表示事件已持久化，不表示关联或回链成功。

支持范围沿 I13：`aws` 商业分区中 OpenFox 已列出的区域；无 China/GovCloud、自动 AssumeRole、Git 推送或直接 SNS HTTP。每个 Lambda 实例配置一个仓库和一个 OpenFox 桥接地址。多个仓库分别配置，避免在事件数据中选择接收端或凭据。

## 文件与权限

| 文件 | 用途 |
| --- | --- |
| `bridge.py` | 仅 Python 标准库的 Lambda handler；部署包只需此文件 |
| `test_bridge.py` | 合成事件、受控 transport 和本地 TLS 测试；不上传到 Lambda |
| `eventbridge-rule.template.json` | 精确账号、区域、仓库 ARN 的七种已支持事件匹配模式 |
| `iam-read-policy.template.json` | OpenFox 组织连接所用 IAM principal 的只读策略 |
| `iam-pr-backlink-policy.template.json` | 可选 PR 评论回链权限，只允许 `PostCommentForPullRequest` |

**两份 CodeCommit IAM 模板属于填入 OpenFox 的 IAM principal，不属于桥接 Lambda 的执行角色。** 替换 `REGION`、`ACCOUNT_ID`、`EXACT_CASE_SENSITIVE_REPOSITORY_NAME`；名称大小写必须一致。`ListRepositories` 需要 `Resource: "*"`，其余读取和可选评论按已选仓库 ARN 限制。列仓库权限不代表可以读取每个仓库；客户管理 KMS key 的权限仍需管理员单独核对。本模板不授予 KMS 管理或云资源部署权限。[AWS CodeCommit IAM action/resource 表](https://docs.aws.amazon.com/service-authorization/latest/reference/list_codecommit.html)

Lambda 不调用 CodeCommit，不需要上述读取或评论权限。使用当前部署环境所需的最小执行角色；若保留 Lambda 平台日志，只授相应日志写入权限。本示例不记录事件、URL、token 或外部错误正文。不要给函数增加记录整个入参/环境的调试 wrapper。部署和网络权限应由 AWS 管理员按实际环境审阅。

## AWS 侧配置

以下变量只配置到桥接 Lambda，**不是 OpenFox Server 全局环境变量**；均无默认凭据。一个部署版本读取自己的配置，勿让旧版本继续使用已轮换 token。

| 环境变量 | 必填 / 格式 | 影响 |
| --- | --- | --- |
| `OPENFOX_BRIDGE_URL` | 必填；从 OpenFox 管理页复制的完整 HTTPS URL，无 userinfo/query/fragment | 固定 POST 目的地，保留反向代理的路径前缀；不从事件取 URL |
| `OPENFOX_BRIDGE_TOKEN` | 必填；OpenFox 一次显示的 43 字符 base64url token | 仅放入 Authorization header；按 AWS 密钥配置规范限制读取，不写模板、命令行或日志 |
| `CODECOMMIT_REPOSITORY_ARN` | 必填；`arn:aws:codecommit:REGION:ACCOUNT_ID:Name` | 精确验证 envelope 的账号、区域和唯一 resource ARN |
| `CODECOMMIT_SNS_TOPIC_ARN` | SNS 路径必填；直接 EventBridge 路径可不设置 | SNS 入参必须来自这个固定标准主题；不允许 FIFO 主题 |

使用当前受支持的 Python 3.13 或 3.14 runtime，handler 为 `bridge.lambda_handler`，函数 timeout 配为 **30 秒**。无需 pip、boto3、层或额外 relay。受控测试使用本机 Python；实际 AWS runtime 的部署与网络尚未验证。[Lambda Python handler](https://docs.aws.amazon.com/lambda/latest/dg/python-handler.html)、[受支持 runtime](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtimes.html)

桥接通过标准 TLS 信任与 hostname 验证直接访问固定 HTTPS 地址，不读取 HTTP_PROXY/HTTPS_PROXY，也不跟随 redirect。证书和部署网络必须正常；不提供跳过 TLS、私有 CA 上传或代理配置开关。网络不可达、未知 CA、错 hostname 均失败，不降级 HTTP。VPC/NAT 等部署网络由管理员配置，本目录不创建它们。

## 部署顺序（未执行）

1. 为 OpenFox 组织连接准备显式 IAM 凭据与所选区域，按上方只读模板限制仓库。只有确实允许 PR 回链时才另加可选评论策略；OpenFox 中仍以当前 Git 设置和任务权限控制实际发送。
2. 在 OpenFox 添加连接、选择仓库并启用；初始化建立历史基线。保存新生成的 URL/token 到受限配置，勿截图或写入工单。OpenFox 对每仓有独立 token；丢失后轮换，不尝试恢复旧值。
3. 创建单仓库 Lambda，上传 `bridge.py`，按上表设置四项变量和 30 秒 timeout。不要创建 Function URL 或公开 HTTP 入口。保留部署版本，便于失败回退。
4. 在仓库所在账号/区域创建 EventBridge rule，将 `eventbridge-rule.template.json` 占位值替换为精确值。使用完整匹配事件作为 target 输入；不要选字段裁剪、固定输入或 input transformer。规则包含三种 reference 事件及四种 PR 事件；tag reference 由 OpenFox 判为忽略，不被误记为分支提交。[CodeCommit 官方事件结构](https://docs.aws.amazon.com/codecommit/latest/userguide/monitoring-events.html)
5. **直接路径**：target 选择该 Lambda。按 AWS 文档配置函数 resource policy，允许 `events.amazonaws.com` 的 `lambda:InvokeFunction`，`AWS:SourceArn` 限定为这条 rule ARN；审阅并合并所需 statement，不覆盖已有 policy。本目录不提供一条会替换整个策略的命令。[EventBridge 调用权限](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html)
6. **SNS 路径**：rule target 改为指定标准 SNS topic，按同一 AWS 权限文档配置 EventBridge 发布权限。为该 topic 添加 Lambda 订阅；函数的 SNS invocation 权限必须限定精确 topic ARN，并将同值写入 `CODECOMMIT_SNS_TOPIC_ARN`。只将完整 EventBridge JSON 作为 SNS `Message`。SNS→Lambda 是 AWS 原生调用：示例核对 envelope/固定 topic，不抓取 `SigningCertURL`，不处理订阅确认 URL，不把这个检查宣传为 SNS HTTP 验签。[SNS Lambda 触发与权限](https://docs.aws.amazon.com/lambda/latest/dg/with-sns.html)
7. 在实际获授权的测试仓库产生一个测试事件，然后查看 Lambda 执行结果和 OpenFox 的最近事件、初始化及工作诊断。先确认事件持久化，再确认实际关联；若允许回链，另核对 AWS 评论与对应工作状态。此步骤需要真实 AWS 授权，**本次没有执行**。
8. 为失败配置可检查的告警和受控 DLQ/失败目的地及保留策略，限制其访问（事件可能含标题/作者等数据）。同时检查 EventBridge 投递失败和 Lambda 异步执行失败；这两个阶段的失败处置不同。不要将没有 Lambda Error 当成 OpenFox worker 已完成。[Lambda 异步失败处理](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html)

## 重复、失败和边界

单个事件 canonical JSON（UTF-8、键排序、无多余空白、不转义非 ASCII）最多 **256 KiB**。保留原始 EventBridge `id`、`time` 和字段；SNS 的 MessageId、Timestamp、签名等外层元数据不进入转发正文。激活所在秒之前的事件会持久记录为 `IGNORED / before_activation`；秒级时间不能区分该秒内激活前后，因此激活所在秒的事件接受处理。重复 event ID + 相同正文被 OpenFox 幂等接受；相同 ID + 不同正文返回 409。禁止为了绕过冲突生成新 ID/时间。修改 canonical 格式或更换序列化运行时前，必须先排空旧重试，或用同一版本处理存量事件。

SNS 每次最多 **10 records**（示例自己的限制，不是 AWS 平台限额），整批先核验再发送。任一 record 无效时全部不发送；发送到中途失败则抛异常，整批重试时此前成功的事件使用相同正文去重。AWS 异步调用可能重复，重试有限；本地不再套自动重试，也不承诺所有丢失事件必达。OpenFox 发现扫描与工作诊断仍是必要补偿。[SNS 至少一次投递](https://docs.aws.amazon.com/lambda/latest/dg/with-sns.html)

每个网络 I/O timeout 最多 **10 秒**；阶段及响应分块间使用共享 monotonic 28 秒预算，并保留 Lambda context 剩余时间中的 1 秒。剩余不足就抛脱敏异常，不开始下一个请求。Python DNS 和单次标准库调用内部不支持该预算的独立硬抢占，**不声称 DNS 在 10 秒内必停**；30 秒 Lambda timeout 是总执行硬边界。未使用线程、signal 或额外转发器实现超时。

只接受 `202` 且 JSON `accepted: true`、布尔 `duplicate`。响应正文最多 **4 KiB**，不接收压缩正文，不读取/输出错误正文，不跟随 3xx。超过正文预算、格式不符、TLS/网络失败，以及 400/401/404/409/429/5xx 都抛固定脱敏错误，由 AWS 的已配置机制处理；429 不在函数里睡眠等待，避免多层重试。共享预算的检查覆盖请求前、发头前、响应前和每个响应分块；不把 socket timeout 宣称为所有内部读取的绝对 wall-clock deadline。[Python HTTP 客户端](https://docs.python.org/3/library/http.client.html)

| 脱敏错误 | 处理 |
| --- | --- |
| `invalid_configuration` | 核对固定 URL/token/ARN，不把秘密贴到日志 |
| `invalid_sns_envelope` / `event_scope_mismatch` | 核对 topic、rule、账号、区域、单仓资源；禁止使用转换后的事件 |
| `invalid_event` / `invalid_json` / `unsupported_event` / `event_limit` | 检查原始输入的类型/限额；不要改 id/time 冒充新事件 |
| `receiver_rejected` | 查看 OpenFox 的受限服务诊断和配置；轮换、停用、409 冲突均需人工处理 |
| `invalid_receiver_response` / `receiver_response_limit` | 核对 URL 和反向代理，勿把 HTML 登录页当成功 |
| `invocation_budget_exhausted` / `bridge_failed` | 检查失败目的地、网络/TLS和函数时限；错误不含原异常或请求秘密 |

## 验证与回退

从仓库根目录运行 `python3 -B -m unittest discover -s docs/integrations/codecommit -p 'test_bridge.py' -v`。测试只使用合成 token/事件、受控 transport 和回环 TLS 服务；新建临时证书，不修改系统信任。本地 OpenSSL 用于测试证书生成，不是部署依赖。测试覆盖严格 TLS 正负例、固定目的地、重放正文、整批预验、半批失败、拒绝 redirect、正文/响应限额、共享预算与关闭连接。真实 AWS IAM、EventBridge/SNS 投递、Lambda runtime、VPC/KMS 及真实评论都需另行验收。

回退先停用 EventBridge rule/移除新投递目标，保留失败消息和部署版本；需要立即拒绝已排队调用时，在 OpenFox 禁用该仓库或轮换 token。停规则不会撤销已排队调用，轮换后旧调用失败是预期，不能用旧配置恢复绕过。排队事件审查后以原 ID/time/正文受控重放，不清除未知结果再造请求。回退 Python 版本时保持 canonical 格式及配置一致。删除 AWS 资源、策略和失败消息由管理员单独授权；本次未执行。本子片无 OpenFox 配置键、SQL 或数据库迁移。
