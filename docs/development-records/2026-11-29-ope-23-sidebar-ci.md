# OPE-23 · 统一侧栏开关并修复 Web 回归

原提交在中英文浏览器中复现两项问题。会话产物面板显示已验收的 640px，测试仍按旧默认值计算出 781px。收起全局侧栏会在 Logo 下插入一行按钮，使四个导航图标同时下移 34px。

按选定的 B 方案，收件箱、成员、设置和会话通过组织标题栏中的同一按钮收起、展开左侧导航。窄窗下，同一位置打开导航抽屉，抽屉提供关闭按钮。桌面标题栏保留窗口拖动区和 macOS 窗口按钮的空间。右栏宽度断言按 640px 默认值和会话保留空间计算。

运行 `OPENFOX_WEB_PORT=42836 pnpm exec playwright test tests/frontend/e2e/prompt-kit.spec.ts --grep 'resizable conversation panels|global sidebar resize and full collapse'`，修复前四项失败，修复后四项通过。真实 Chromium 检查了中英文四条路由、键盘焦点、开关和图标位置、拖宽后刷新、窄屏抽屉及右侧面板边界。定向用例的 API 数据为 SIMULATED。
