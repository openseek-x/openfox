import { defineConfig } from "@rsbuild/core";
import { pluginReact } from "@rsbuild/plugin-react";
import tailwindcss from "@tailwindcss/postcss";

// 扩展侧边栏的独立构建：dist 目录可直接作为“已解压的扩展程序”加载。
// MV3 的内容安全策略禁止内联脚本，所以不开启脚本内联，资源一律用相对路径。
// public/ 下的 manifest.json 与 background.js 由 rsbuild 原样复制到 dist 根目录。
export default defineConfig({
  plugins: [pluginReact()],
  source: { entry: { sidepanel: "./src/sidepanel/main.tsx" } },
  html: { template: "./sidepanel.html" },
  output: {
    assetPrefix: "./",
    copy: [
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
