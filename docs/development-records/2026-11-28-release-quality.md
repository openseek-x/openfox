# 缩短 GitHub 发布质量检查

最近一次发布 PR 从触发到完成用了 41 分 20 秒，其中中心镜像检查用了 1 分 9 秒。随后 `master` 发布的全量质量检查又用了 38 分 22 秒。Go 约 18 分钟，前端约 10 分钟，浏览器约 8 分钟，三项原本在一台 macOS runner 上串行执行。[发布 PR](https://github.com/openseek-x/openfox/actions/runs/46037062397) · [对应发布](https://github.com/openseek-x/openfox/actions/runs/46042751644)

## 工作流变更

| 触发场景 | 执行的检查 |
| --- | --- |
| 同仓 `develop → master` 发布 PR | 静态部署测试和 Linux 中心镜像冒烟，覆盖版本、迁移和 HTTP 健康检查 |
| 其他面向 `develop` 或 `master` 的 PR | 运行上述预检，同时分别在两台 runner 上执行 Go 回归、前端与真实 API 浏览器回归 |
| 推送到 `master` | 在最终提交上运行两组回归和静态部署测试，成功后打包。正式中心镜像通过同一个冒烟测试后才允许发布 |

`.github/workflows/quality.yml` 按事件和 PR 来源选择检查。`master` 允许直接推送，因此在发布提交上保留全量回归。两组回归沿用现有工具版本与资源限制。两个 runner 的准备工作有额外开销，实际耗时取决于 GitHub 调度。

`.github/workflows/desktop-release.yml` 移除了打包阶段重复的桌面单测、更新界面单测、类型检查和静态部署测试。前三项已由 `pnpm check` 覆盖，第四项由质量工作流执行。版本号注入后的安装器验证、签名、镜像冒烟和发布顺序仍由打包流程负责。`deploy/README.md` 已同步。数据库、SQL、迁移、密钥和仓库设置均无变更。

## 验证与回滚

- 用五组事件样本本地验证实际 YAML 条件，覆盖同仓发布 PR、功能 PR、直达 `master` 的修复 PR、fork 的同名 `develop` 分支和 `master` 推送。五组结果均符合上表，并检查了矩阵分工及发布、部署的依赖关系。
- `python3 deploy/test_publish_static.py` 通过全部 4 项测试。
- 本地 Docker 构建 Linux amd64 中心镜像，并运行 `bash deploy/test-center.sh`。镜像版本匹配，临时 PostgreSQL 17 成功应用 175/175 项迁移，`/live` 和 `/ready` 分别返回 `ok` 和 `ready`。
- `actionlint v1.7.12` 通过质量工作流检查。发布工作流仅报告原有的 `concurrency.queue` 提示，与修改前逐条对比一致。GitHub 支持该排队配置。[GitHub 排队配置说明](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)
- 直接推送 `develop` 不会触发发布。完整 Go、前端及浏览器回归和新版流水线的实际耗时需在相应 GitHub 运行中验证。发现回归时，使用 `git revert` 撤销本次提交即可还原原有检查顺序。
