# 集成与发布验收

`run.sh` 是发布验收入口。它分别执行数据库、授权、
设备、Agent、事件、核对、清理和备份恢复测试，再运行 Go/前端回归与静态检查。

每个检查项都写入独立的日志和结果行。`report.py` 生成 `report.md` 与 `report.json`：

- `pass` 表示对应命令真实返回成功；
- `fail` 表示命令失败；
- `not-run` 表示环境或测试夹具缺失，不能折算为通过；
- `blocked` 表示命令因当前权限/环境限制无法运行，不能折算为通过；
- 任一 required case 不是 `pass`，发布门就是 `BLOCKED`，脚本返回非零。

## 运行

```sh
bash tests/integration/wp16/run.sh
```

报告默认写入 `/private/tmp/openfox-wp16-report.*`。可通过
`OPENFOX_WP16_ARTIFACT_DIR` 指定一个权限为 0700 的本地目录。真实 Web/API 链路
可设置 `OPENFOX_WP16_RUN_LIVE=1` 让入口创建 disposable PostgreSQL，也可以提供
仅限 disposable PostgreSQL 的 `OPENFOX_WP11_PG_SOCKET` 和
`OPENFOX_WP11_PG_PORT`；真实 Runtime/电脑验收必须提供经过授权的实际夹具，脚本
不会用伪 Runtime 或 demo 结果冒充该证据。

发布验收复用各项隔离测试入口，不改变生产数据库，不执行生产迁移、恢复、
发布或部署。数据库和备份测试产生的临时数据均位于 `/private/tmp` 并在测试结束
时删除；报告日志可能包含测试环境信息，应按敏感运维证据处理。
