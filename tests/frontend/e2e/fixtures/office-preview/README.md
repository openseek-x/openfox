# Office 版式回归样本

- `sample.*.json`：由仓库 `internal/office/testdata/sample.docx|xlsx|pptx` 经 `office.Read(...).View()` 生成的原文件文字投影。
- `sample.*.pdf`：同一组原文件通过 `gotenberg/gotenberg:8.32.0` 的 `/forms/libreoffice/convert` 实际转换，关闭表单和批注导出；不是手写 PDF 或文字列表。
- `styled.py` / `styled.pptx`：独立 python-pptx 生成的中文标题、深蓝背景及柱状图，验证完整幻灯片版式。
- `styled.pptx.pdf`：同一镜像从上述样本转换，嵌入 Noto Sans CJK SC 和 Carlito 的字形子集。
- `complex.py` / `complex.docx` / `complex.xlsx` / `complex-logo.png`：阶段 6 独立生成的中文两页 Word、两张工作表 Excel 样本，含重复文字、分页、页眉页脚、图片、合并单元格、公式、冻结窗格及无缓存的柱状图。用于发现原生 Office 引擎保真和源定位的具体缺口，详见 `tests/frontend/office-fidelity/README.md`。

浏览器测试会解析真实 PDF，在 canvas 中检查绘制结果、开关批注前后的像素和布局、保留实例、页码及提交锚点。Office 文字修改通过明确选择源文字完成，不推测 PDF 文字与 OOXML 的对应关系。

这些 PDF 中嵌入的字形来自 Noto Sans CJK SC、Carlito 和 Caladea，镜像随字体提供 SIL Open Font License 1.1。字体子集用于保留 PDF 的文字与版式。[文档嵌入说明](https://openfontlicense.org/ofl-faq/) · [镜像字体配置](https://github.com/gotenberg/gotenberg/blob/v8.32.0/build/Dockerfile)
