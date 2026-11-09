import { defineConfig } from "@rsbuild/core";
import { pluginReact } from "@rsbuild/plugin-react";
import tailwindcss from "@tailwindcss/postcss";

const apiTarget = process.env.OPENFOX_WEB_API_TARGET ?? "http://127.0.0.1:8080";
const target = new URL(apiTarget);
if (
  !["http:", "https:"].includes(target.protocol) ||
  target.username ||
  target.password
) {
  throw new Error(
    "OPENFOX_WEB_API_TARGET must be an HTTP(S) origin without credentials",
  );
}
if (target.pathname !== "/" || target.search || target.hash) {
  throw new Error(
    "OPENFOX_WEB_API_TARGET must not include a path, query, or fragment",
  );
}

// 开发服务器端口；本机 4173 被其他会话占用时可用 OPENFOX_WEB_PORT 指定。
const port = Number(process.env.OPENFOX_WEB_PORT ?? 4173);
if (!Number.isInteger(port) || port < 1024 || port > 65535) {
  throw new Error("OPENFOX_WEB_PORT must be an integer between 1024 and 65535");
}

export default defineConfig({
  plugins: [pluginReact()],
  source: { entry: { index: "./src/main.tsx" } },
  html: { template: "./index.html" },
  // 网页由静态服务器在根路径提供（桌面端也加载同一个网页地址），深层路由刷新时资源路径要从根开始
  output: { assetPrefix: "/" },
  server: {
    host: "127.0.0.1",
    port,
    strictPort: true,
    proxy: { "/api": { target: target.origin, changeOrigin: false } },
  },
  tools: { postcss: { postcssOptions: { plugins: [tailwindcss()] } } },
});
