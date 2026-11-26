import { defineConfig } from "@rsbuild/core";
import { pluginReact } from "@rsbuild/plugin-react";
import tailwindcss from "@tailwindcss/postcss";

// 扩展侧边栏的独立构建：dist 目录可直接作为“已解压的扩展程序”加载。
// MV3 的内容安全策略禁止内联脚本，所以不开启脚本内联，资源一律用相对路径。
// 发行包与桌面端共用部署地址，本地默认值来自 Web 工作台的开发配置。
const workbench = new URL(
  process.env.OPENFOX_PUBLIC_URL ?? "http://127.0.0.1:4173",
);
if (
  workbench.username ||
  workbench.password ||
  workbench.pathname !== "/" ||
  workbench.search ||
  workbench.hash ||
  !(
    workbench.protocol === "https:" ||
    (workbench.protocol === "http:" &&
      ["127.0.0.1", "localhost"].includes(workbench.hostname))
  )
) {
  throw new Error("OPENFOX_PUBLIC_URL 必须是 HTTPS 工作台来源或本机开发来源");
}
export default defineConfig({
  plugins: [pluginReact()],
  source: { entry: { sidepanel: "./src/sidepanel/main.tsx" } },
  html: { template: "./sidepanel.html" },
  output: {
    assetPrefix: "./",
    copy: [
      {
        from: "manifest.json",
        to: "manifest.json",
        transform: (content) =>
          JSON.stringify(
            {
              ...JSON.parse(content.toString()),
              homepage_url: workbench.origin + "/",
              host_permissions: [
                workbench.protocol + "//" + workbench.hostname + "/*",
              ],
            },
            null,
            2,
          ),
      },
      {
        from: "../../packages/ui/src/icons/LICENSE-lucide.txt",
        to: "licenses/lucide.txt",
      },
      { from: "../../packages/ui/src/icons/openfox.png", to: "openfox.png" },
    ],
    filenameHash: false,
    sourceMap: { js: false, css: false },
  },
  tools: { postcss: { postcssOptions: { plugins: [tailwindcss()] } } },
});
