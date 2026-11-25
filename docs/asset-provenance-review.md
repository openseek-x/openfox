# 公开版图形来源与许可

共享图标表有 313 个 UI 键。通用线形图标来自 npm 发布包 `lucide-static@0.468.0` 的 215 个 SVG，表中 `lucide` 的键对应上游文件名。小助手的对话图形及主导航的五个字形由 OpenFox 绘制。[本地映射](../packages/ui/src/icons/data.ts) · [主导航](../packages/app/src/shell/RailIcon.tsx)

功能图标使用 24 单位网格、圆端和圆角，随界面文字着色。默认边长 16px。边长 ≤14px、15–18px、>18px 分别使用 2.5、2.25、1.75 单位描边。主导航使用 20px 图形和 1.5px 描边，当前入口填实，内线取选中底色。类型、状态和优先级沿用各自的语义色。风格参考取自 HiFox 公开任务详情页中导航、操作与状态图形的层级。[HiFox 任务页](https://hifox.com/features/tasks/)

Lucide 使用 ISC 许可证，其中源自 Feather 的图形适用 MIT 许可证。两份版权及许可声明随源码提供，Web 构建同时复制到 `static/licenses/lucide/LICENSE.txt`。[随附许可全文](../packages/ui/src/icons/LICENSE-lucide.txt) · [上游许可说明](https://lucide.dev/license)

OpenFox 的橙色侧身卷尾狐采用原创矢量母版。侧栏用透明矢量图，浏览器标签页和桌面离线页用 PNG，Dock 和 Teams 彩色图标使用深色底，菜单栏与 Teams 轮廓图使用透明单色模板。在安装项目依赖和 Playwright Chromium 的 macOS 上运行 `node tools/brand/render-icons.mjs` 可重新生成所有平台资源。[卷尾狐母版](../packages/ui/src/icons/openfox.svg) · [导出脚本](../tools/brand/render-icons.mjs)

集成服务、运行时和操作系统标记使用系统字体渲染文字缩写。通用代码托管和分拣空状态使用 Lucide 图形。状态、优先级、进度、图表和加载动画由界面代码绘制，二维码和 PDF 选区几何由对应数据生成。测试 Office 文件及其中的图形来源见夹具说明。[Office 夹具](../tests/frontend/e2e/fixtures/office-preview/README.md)
