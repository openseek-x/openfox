# OpenFox 项目开发规则

- 项目处于初期开发阶段。按当前需求维护一套清晰一致的实现，无须为旧版本、接口、配置或历史数据添加兼容层。
- 修改接口时同步更新 OpenAPI 契约并运行 `pnpm gen:api`。提交前按改动范围运行 `make check`、`pnpm check` 和相关浏览器回归。
- 新增或修改的代码注释使用中文。每个完成的任务在 `docs/development-records/` 记录改动和实际验证结果。
