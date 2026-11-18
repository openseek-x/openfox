# Office 保真与源定位调查

这是阶段 6 的隔离验证工具，不是应用中的 Office 渲染器，也不计入默认前端测试。测试不读取桌面登录信息、不调用模型、不上传文件。

## 依赖与运行

从仓库根目录执行，依赖仅安装到被忽略的 `work/office-stage6`：

```sh
npm install --prefix work/office-stage6 --ignore-scripts --save-exact --no-audit --no-fund @betteroffice/docx@0.5.1 @betteroffice/pptx@0.5.1 @betteroffice/xlsx@0.5.1 @betteroffice/fonts@0.4.2 @betteroffice/fonts-cjk@0.4.2
node tests/frontend/office-fidelity/run.cjs
python3 tests/frontend/office-fidelity/verify_sources.py
```

需要已安装的项目 Electron 和 Playwright。三种基础样本位于 `tests/frontend/e2e/fixtures/office-preview`；`complex.py` 记录独立生成 Word/Excel 样本的方法，重新生成需要 python-docx、openpyxl 和 Pillow。已提交样本可直接使用，不需要安装这些 Python 包。

本轮完整复现还需要在 `work/office-stage6/fixtures` 放置：

- `user-deck.pptx`：用户授权验证的原始 11 页 PPT 的副本。原文件不纳入仓库，路径和内容均不硬编码在执行脚本中。
- `no-freeze.xlsx`：用 openpyxl 打开 `complex.xlsx`，仅将首张表的 `freeze_panes` 设置为 `None`，保存至此。
- `recalculated.xlsx`：用隔离 profile 的 LibreOffice 将 `complex.xlsx` 重新保存为 XLSX 的副本，补齐图表缓存。仅用于诊断，不能替代原文件的验收结果。

缺少上述样本会明确报错，不能把跳过私有样本当作通过。依赖版本、包完整性、样本哈希和本次审计结果见开发记录的 `dependency-provenance.json`。

## 验证方式

`run.cjs` 启动独立、隐藏的 Electron 测试窗口及仅监听 loopback 的静态资源服务；拦截非测试来源请求。WASM、字体和文档均来自本机。输出每个页面/工作表截图与耗时、进程内存采样；完整渲染树仅留在忽略目录。每种格式完成后释放引擎句柄和图片。

`verify_sources.py` 用 Python ZIP/XML 独立解析原文档，对照引擎输出：

- Word：文件 SHA、part、段落序号、正文和重复文字身份；页内文字片段几何信息。
- PPT：part SHA、XML 路径、原始 shape ID、文字和绘制对象身份，包含用户文件全部 11 页。
- Excel：worksheet part SHA、单元格地址、文本和公式，以及 D3 坐标往返。

源定位脚本通过仅说明这些样本的映射成立，**不代表保真通过**。`source-verification.json` 明确保留 Excel 保真失败。脚本不是服务端鉴权或可写范围校验的替代品。

## 对照与结论边界

本机 LibreOffice 生成 PDF 对照；临时 Fontconfig 加载同一批开放字体和系统字体。没有改系统字体配置，也没有连接远端 Gotenberg 或 Microsoft Office。转换同名的 DOCX/XLSX 时应使用不同输出目录，避免 `complex.pdf` 相互覆盖。

结果和实际图片见 内部记录「阶段 6 记录」（独立源码包未附）。本轮确认了 Excel 图片缺失、无图表缓存时图表空白、冻结列切过合并标题时裁切，以及 Word 字重/斜体和表格间距差异。因此没有把这套实验代码替换到产品预览中。

首次字体/WASM加载与后续热运行在同一进程中混合发生；记录的耗时与内存不是稳定基准测试、并发容量或泄漏验收。

## 来源与许可

引擎 API 来自锁定版本 BetterOffice 发布包（Apache-2.0），字体包还包含 OFL-1.1 字体。参考了 OpenCode 固定提交的 Word retained-layout 初始化顺序，许可保留在 [NOTICE.md](NOTICE.md)。生产接入时需保留实际分发依赖的 LICENSE/字体许可；本目录不分发引擎或字体二进制。

## 阶段 7 续验

历史阶段 6 截图保留不变。Word 的主字体选择和 Canvas 样式合成已修正，Word/PPT 已接入应用，实际实现及测试在 `packages/app/src/conversations/artifacts/{word,ppt}`、`OfficePages.tsx` 和 `tests/frontend/e2e/artifact-office-native.spec.ts`；Excel 随后通过复杂样本的聚焦验收，并接入 `artifacts/sheet`：使用受限渲染副本补缓存/图片，以同一位图组合冻结区，源锚点仍来自原文件。详见同一开发记录的阶段 7 进展，不能将上文的历史结论当作最新产品状态。

可用 `OFFICE_FIDELITY_FORMAT=docx|pptx|xlsx` 限定格式、`OFFICE_FIDELITY_EVIDENCE=work/...` 将实验截图放到独立目录，`OFFICE_FIDELITY_RENDERER=work/.../render.mjs` 指定诊断脚本。它们仅作用于此显式实验命令。不要覆盖阶段 6 已归档证据，也不要把修改过的内存工作簿或诊断副本当作原文件验收通过。

Excel 生产预览的 E2E 样本投影和渲染副本可在仓库根目录重建：

```sh
go run tests/frontend/office-fidelity/spreadsheet_fixture.go
pnpm build
pnpm exec playwright test tests/frontend/e2e/artifact-office-native.spec.ts --grep Excel --workers=1
```

生成器只覆盖 `complex.xlsx.json` 和 `complex.xlsx.render.xlsx`，不改动原始 `complex.xlsx`。单测验证原始字节、源单元格和其它 ZIP parts 不变；副本 SHA 在 Worker 中复核。派生文件仅用于预览，不能当作修改后的产物。

## 阶段 14：修改后往返样本

```sh
go run ./tests/frontend/office-fidelity/roundtrip /tmp/openfox-office-roundtrip
pnpm build
pnpm exec playwright test artifact-office-native.spec.ts
```

生成器只读取上述原始样本，将生产 `office.Replace` 的候选、源投影与 Excel 派生预览写入指定目录；重新解析修改结果，核对未编辑 ZIP 部件字节不变。E2E 自动在临时目录运行生成器，测试结束清理：以真实 Worker、字体/WASM、构建产物与部署 CSP 验证三格式候选渲染、批注开关布局和实际下载哈希。浏览器 API 使用夹具；真实 API 的候选上传/下载与基线约束另由 PostgreSQL 集成测试覆盖，不调用模型服务。

`OPENFOX_STAGE14_EVIDENCE=/absolute/directory` 可额外保存候选截图，仅影响测试输出。支持范围及限制见 [用户说明](../../../docs/artifact-reviews.md)。
