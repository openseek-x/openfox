import { defineConfig } from "@playwright/test";

// 与 apps/web/rsbuild.config.ts 共用 OPENFOX_WEB_PORT（默认 4173）。
const webPort = process.env.OPENFOX_WEB_PORT ?? "4173";
const webOrigin = `http://127.0.0.1:${webPort}`;

export default defineConfig({
  testDir: "./tests/frontend/e2e",
  outputDir: "./tests/frontend/test-results",
  timeout: 60000,
  workers: 1,
  use: {
    baseURL: webOrigin,
    viewport: { width: 1440, height: 900 },
    // 界面按浏览器语言选择词典；断言使用简体中文文案
    locale: "zh-CN",
    trace: "off",
  },
  webServer: {
    command: "pnpm dev",
    url: webOrigin,
    reuseExistingServer: false,
    timeout: 60000,
  },
});
