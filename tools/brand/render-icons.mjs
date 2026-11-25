// 从卷尾狐母版导出各端所需图标。在 macOS 上运行，以便同时生成 ICNS。
import { execFileSync } from "node:child_process";
import { copyFile, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { chromium } from "@playwright/test";

const root = fileURLToPath(new URL("../../", import.meta.url));
const asset = (name) => path.join(root, name);
const source = await readFile(
  asset("packages/ui/src/icons/openfox.svg"),
  "utf8",
);
const desktop = "apps/desktop";
const teams = "internal/integrations/teams/icons";
const temporary = await mkdtemp(path.join(tmpdir(), "openfox-icons-"));
const iconset = path.join(temporary, "OpenFox.iconset");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ deviceScaleFactor: 1 });

  async function render(filename, size, variant) {
    await page.setViewportSize({ width: size, height: size });
    await page.setContent(`<style>
      * { box-sizing: border-box }
      html, body { width: 100%; height: 100%; margin: 0 }
      body { display: grid; place-items: center }
      main { width: 100%; height: 100%; display: grid; place-items: center }
      svg { display: block; width: 100%; height: 100% }
      .dock { width: 82%; height: 82%; background: #211d1a; border-radius: 22% }
      .dock svg, .teams svg { width: 82%; height: 82% }
      .teams { background: #211d1a }
      .tray path { fill: black }
      .outline path { fill: white; shape-rendering: crispEdges }
    </style><main class="${variant}">${source}</main>`);
    await page.screenshot({ path: filename, omitBackground: true });
  }

  await render(asset("packages/ui/src/icons/openfox.png"), 96, "mark");
  await copyFile(
    asset("packages/ui/src/icons/openfox.png"),
    asset(`${desktop}/renderer/openfox.png`),
  );
  await render(asset(`${desktop}/renderer/openfoxTemplate.png`), 16, "tray");
  await render(asset(`${desktop}/renderer/openfoxTemplate@2x.png`), 32, "tray");
  await render(asset(`${teams}/color.png`), 192, "teams");
  await render(asset(`${teams}/outline.png`), 32, "outline");

  await mkdir(iconset);
  for (const size of [16, 32, 128, 256, 512]) {
    await render(path.join(iconset, `icon_${size}x${size}.png`), size, "dock");
    await render(
      path.join(iconset, `icon_${size}x${size}@2x.png`),
      size * 2,
      "dock",
    );
  }
  await copyFile(
    path.join(iconset, "icon_512x512@2x.png"),
    asset(`${desktop}/build/icon.png`),
  );
  execFileSync("iconutil", [
    "-c",
    "icns",
    "-o",
    asset(`${desktop}/build/icon.icns`),
    iconset,
  ]);
} finally {
  await browser.close();
  await rm(temporary, { recursive: true, force: true });
}
