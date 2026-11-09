# Run 事件、结果与产物投递契约

PostgreSQL 是已接受事件、最终结果与产物元数据的权威来源。浏览器使用 REST 快照加 SSE 实时追加；
Daemon 通过已认证、幂等的 HTTPS 写入上报。接口与 schema 的权威定义在 `contracts/openapi.yaml`
（RunResponse、RunResult、RunEventPage、RunEventInput、Step 等），设备事件的 JSON Schema 见
`contracts/schemas/device-event.schema.json`。

## 事件信封

每条已接受的事件包含：

- `id`：设备生成的稳定 UUID；
- `run_id`、`generation` 与正整数 `sequence`（同一代次内由 Daemon 从 1 起分配）；
- `type`：事件类型，见下表；
- `safe_payload`：`{"text"?: string (≤ 8 KiB), "data"?: object}`，单条 ≤ 64 KiB，由设备脱敏，
  不得包含凭据、私钥、Cookie 或未脱敏的环境变量值；
- `stream_seq`：中心分配的流位置，用于 `GET /runs/{run}/events` 分页；
- `occurred_at`（设备时间）与 `received_at`（中心时间）。

唯一键是 `(organization_id, run_id, generation, sequence)`。同一位置重复上报相同的 id / type / payload
是幂等成功，否则 409 `DELIVERY_CONFLICT`。事件可以乱序到达；ACK 中的 `highest_contiguous`
是当前代次从 1 起连续已接受的最大序号。

| type | 来源 | 说明 |
| --- | --- | --- |
| `started` | 设备 | Runtime 会话就绪；data = `{thread_id, turn_id}` |
| `output` | 设备 | 最终回复正文（text）；结果 output 由它汇总 |
| `tool` | 设备 | 旧版过程事件：commentary 正文或工具调用摘要；data.kind = commentary / commandExecution / … |
| `warning` | 设备 | 非致命诊断（重连、配置告警） |
| `failed` / `completed` / `cancelled` | 设备 | Runtime 回合的终态 |
| `step` | 设备 | 执行步骤 v1，见下文 |
| `run.result` | 中心 | 结果写入时在同一事务中生成；data = `{result_id, status, unverified}` |
| `run.reconciled` | 中心 | 核对完成 |

旧类型保持不变并继续上报。中心生成的事件取当前最大序号 + 1，因此 Daemon 必须先送达全部过程事件
再上报结果；结果之后补传的事件如果和中心事件占用同一序号，会被 409 拒绝并丢弃。

## 执行步骤（type = step，schema v1）

`safe_payload.data = {"schema": 1, "step": Step}`。中心校验 `schema == 1`、`step.id` 非空且不超过 64 字节、
`step.kind` 与 `step.state` 在枚举内；不合法时整批 400。其余字段只透传。

- kind：`prep_env`、`thinking`、`agent_text`、`exec`、`patch`、`write_file`、`read`、`search`、
  `web_search`、`web_fetch`、`todo`、`step_group`、`turn_end`。
- state：`running`、`done`、`failed`、`interrupted`、`denied`。
- 固定 id：`prep`（prep_env，Daemon 生成）、`plan`（todo，同一回合）、`turn_end`。

合并规则（前端按 `(generation, sequence)` 排序后折叠）：

1. 同一 `step.id` 的多条事件合并为一个步骤；后到的覆盖先到的同名字段。
2. `append` 追加到 `text`，`output_append` 追加到 `output`；带 `text` / `output` 的事件给出完整值，替换已累积的内容。
3. `state ≠ running` 的事件是终态，携带该步骤的完整字段。
4. 回合结束（`turn_end`）时仍在运行的步骤由适配器以 `interrupted` 结束。
5. 一个 Run（所有 generation）里只要出现过 `prep_env` 以外的 `step` 事件（即 Runtime 适配器自己上报步骤），前端就忽略
   `tool` 事件，否则会重复显示；`output`、`warning`、`failed`、`completed`、`cancelled` 照常处理。
   `prep_env` 由 supervisor 对所有 Runtime 生成，不能据此判断适配器会上报步骤；只有 `prep_env` 或没有
   `step` 事件的 Run 仍按旧事件渲染。

增量与上限（Codex 适配器）：

- 增量按 item 缓冲，累计到 4 KiB 或距首个未发增量满 250 ms 时发出（进程层每 250 ms 定时刷新）；
  单条 `*_append` ≤ 8 KiB，单个步骤的流式增量合计 ≤ 256 KiB，超出后只等终态。
- exec 终态 `output` ≤ 32 KiB（保留尾部），patch 单文件 diff ≤ 24 KiB、整步 ≤ 40 KiB，
  thinking / agent_text ≤ 32 KiB，MCP / 动态工具输出 ≤ 8 KiB；被截断时 `truncated: true`。
- 单条步骤载荷序列化后 ≤ 48 KiB（Daemon 的 data 上限是 56 KiB，中心单条上限是 64 KiB）。
- 工作目录内的路径上报为相对路径；所有文本经环境变量脱敏。

Codex app-server 映射：

| Codex | 步骤 |
| --- | --- |
| `item/started` / `item/completed` · reasoning | thinking（summary 与 content 用空行拼接）；增量 `item/reasoning/summaryTextDelta`（或 `textDelta`，以先到者为准），`summaryPartAdded` 插入空行 |
| agentMessage（phase = commentary） | agent_text；增量 `item/agentMessage/delta`。其他 phase 是最终回复，只产生 `output` 事件 |
| commandExecution，commandActions 全是 read / search / listFiles | 每个动作一个 read / search 步骤（id = item.id，第 n 个动作为 `item.id:n`）；search 从输出汇总 results（≤ 20）与 total_files |
| commandExecution（其他） | exec：command、cwd、exit_code、duration_ms、output；增量 `item/commandExecution/outputDelta` → output_append。状态 inProgress → running，completed → done（exit_code ≠ 0 时为 failed），failed → failed，declined → denied（terminated_before_output） |
| fileChange | patch：files[].path / kind / move_path / diff，additions 与 deletions 由 diff 计数 |
| webSearch | web_search（query、results ≤ 10）；action 为 openPage / findInPage 时为 web_fetch（url） |
| mcpToolCall / dynamicToolCall | exec，command = `server.tool` / `namespace.tool`，output 为文本结果或错误说明 |
| `turn/plan/updated` | todo（id = plan）；全部 completed 时 done |
| `turn/completed` / 终态 `error` | turn_end：final_state done / failed / cancelled，duration_ms 取 turn.durationMs，缺失时取 turn/start 到结束 |
| `thread/tokenUsage/updated` | 不产生步骤；最后一次 total 随结果上报为 usage |
| userMessage、plan、contextCompaction、imageView 等 | 忽略 |

prep_env（Daemon）：claim 后开始（workdir 运行中）；EXPLICIT 目录模式会加一个 repo 子步骤（label 为逻辑根名，
不上报本机路径）；取得启动许可前进入 runtime；Runtime 启动成功后变为 done，并补上 session 子步骤。
prepare-failed 时当前子步骤与整体都是 failed；启动前取消或租约丢失时是 interrupted。

## 结果

一个 Run 最多一个最终结果，status 为 `SUCCEEDED`、`FAILED`、`CANCELED` 或 `TIMED_OUT`。同一结果 ID
与相同载荷是幂等的；不同载荷 409。中心在同一事务中写结果、置 Run 终态、生成 `run.result` 并回写会话。
`reconciliation_required` 为真时拒绝结果（409）。

`usage`（可选）= `{input, cached_input, cache_write_input, output, reasoning_output}`，均为非负整数，
写入 `run_results.usage`。旧 daemon 不上报时为 NULL，统计按 0 计。usage 参与规范化哈希；省略时哈希
与旧版本一致。中心对请求体严格解码，因此新 daemon 遇到旧中心以 400 `INVALID_REQUEST` 拒绝 usage 时，
会去掉 usage 立即重试一次。

RunResponse 在 Run 字段之外还返回 `failure_code`、`started_at`、`usage`（结果里的值或 null），以及
`duration_ms`（started_at → finished_at，任一为空时为 null）。Agent 统计中的 input_tokens / output_tokens
汇总 `run_results.usage`。

## 产物

产物是与 Run（以及可选的结果）关联的有界文件，单个上限 20 MiB。数据库存储名称、存储键、大小、SHA-256、
MIME 类型、状态与保留期限。存储键不透明，文件路径从不作为授权依据。上传先写临时文件并计算哈希、检查大小，
不覆盖已有键，然后再写数据库行。每次下载都会重新检查组织、空间、会话与 Run 的可见性。

## 用户接口

都在 `/v1` 下，沿用会话、CSRF、租户选择与对象授权边界：

- `GET /organizations/{org}/conversations/{conversation}/runs`
- `GET /organizations/{org}/runs/{run}`
- `GET /organizations/{org}/runs/{run}/events?after=<stream_seq>&limit=<n>`：按 stream_seq 升序分页，
  `next_cursor` 为 0 或省略时表示没有更多
- `GET /organizations/{org}/runs/{run}/result`
- `GET /organizations/{org}/runs/{run}/artifacts`、`…/artifacts/{artifact}`
- `POST /organizations/{org}/runs/{run}/cancel`（Idempotency-Key 必填）
- `GET /organizations/{org}/events`（SSE，统一事件流）

### SSE

每帧是 `id`、`event` 和 JSON `data`，另有 15 秒一次的注释心跳；出错时推送 `event: error`（data = `{code}`）
后关闭连接。事件分两类：

- `run.event`：data 是 RunStreamEvent（即上面的事件信封）。只推送当前用户能看到的对话里的 Run。
- 组织事件（`task.changed`、`inbox.changed`、`run.state`、`conversation.updated`、`conversation.message`）：
  data 是 OrganizationEvent，只是失效通知，客户端据此刷新对应查询。

游标（v2）：`v2.<run_txid>.<run_seq>.<org_txid>.<org_id>`，客户端原样保存最后收到的 id，重连时通过
`after` 或 `Last-Event-ID` 带上（`after` 优先）。推送只包含快照时刻已结束的事务写入的事件，按（事务号，序号）
严格递增，不漏发也不重发。兼容规则：纯数字游标 n 视为 Run 游标 `0.n`，组织事件从连接时刻开始推送；不带游标时，
Run 事件从头推送，组织事件从连接时刻开始。每秒轮询一次，每类每次最多 100 条；组织事件保留 7 天。

前端拉取 Run 时间线：先循环 `GET /runs/{run}/events` 拉全，再把 SSE 收到的 `run.event`（data.run_id 匹配）
追加进缓存，按 `(generation, sequence)` 去重排序；`run.state` 只刷新 Run 详情。

## Daemon 接口

设备 Bearer 会话使用：

- `POST /device/organizations/{org}/runs/{run}/events`：≤ 100 条、合计 ≤ 256 KiB
- `POST /device/organizations/{org}/runs/{run}/result`
- `POST /device/organizations/{org}/runs/{run}/artifacts`

设备必须是该 Run 的执行电脑。Daemon 上传规则：

- 序号由每个 Run 的上传器统一分配（prep_env 步骤与 Runtime 事件共用）。每 500 ms 或满 50 条 / 128 KiB 上传一批，
  上传在独立协程进行，不阻塞 Runtime 事件泵；内存队列超过 1000 条时，后续事件直接写入本地 outbox。
- 瞬时失败重试 3 次后，把该批写入本地 journal（runs.db 的 event_outbox 表），每个 Run 最多保留 2000 条，
  超出时丢弃序号最小的。之后在本 Run 的下一次成功上传、每个轮询周期以及 Daemon 退出前（时限 10 秒）补传。
  中心永久拒绝（4xx）的批次会逐条重试，被拒的事件记录日志后丢弃；outbox 记录保留 7 天。
- 结果上报之前，先送达该 Run 内存队列里的全部事件。
