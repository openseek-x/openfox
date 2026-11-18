// Explicit investigation runner. Does not access the user's desktop profile.
const fs = require("node:fs/promises");
const path = require("node:path");
const http = require("node:http");
const { _electron: electron } = require("@playwright/test");
const { createRequire } = require("node:module");
const requireDesktop = createRequire(path.resolve("apps/desktop/package.json"));
const root = path.resolve("work/office-stage6");
const evidence = path.resolve(
  process.env.OFFICE_FIDELITY_EVIDENCE ??
    "docs/development-records/evidence/office-stage6-20261117",
);
const fixtures = path.resolve("tests/frontend/e2e/fixtures/office-preview");
const mime = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".mjs": "text/javascript",
  ".wasm": "application/wasm",
  ".ttf": "font/ttf",
  ".otf": "font/otf",
  ".json": "application/json",
};

(async () => {
  await fs.mkdir(evidence, { recursive: true });
  const server = http.createServer(async (req, res) => {
    try {
      const name = decodeURIComponent(new URL(req.url, "http://test").pathname);
      if (name === "/") {
        res.setHeader("Content-Type", "text/html");
        res.end(
          '<!doctype html><meta charset="utf-8"><div id="pages"></div><script type="module" src="/render.mjs"></script>',
        );
        return;
      }
      let file;
      if (name === "/render.mjs")
        file = path.resolve(
          process.env.OFFICE_FIDELITY_RENDERER ??
            path.join(__dirname, "render.mjs"),
        );
      if (
        /^\/deps\/[a-z]+\/[a-zA-Z0-9_./-]+$/.test(name) &&
        !name.includes("..")
      )
        file = path.join(root, "node_modules/@betteroffice", name.slice(6));
      if (/^\/fonts\/[a-zA-Z0-9_-]+\.(ttf|otf)$/.test(name)) {
        const pack =
          name.includes("SC-") ||
          name.includes("JP-") ||
          name.includes("KR-") ||
          name.includes("TC-")
            ? "fonts-cjk"
            : "fonts";
        file = path.join(
          root,
          "node_modules/@betteroffice",
          pack,
          "assets",
          path.basename(name),
        );
      }
      if (/^\/fixtures\/[a-zA-Z0-9_.-]+$/.test(name))
        file = path.join(
          /user-deck|no-freeze|recalculated/.test(name)
            ? path.join(root, "fixtures")
            : fixtures,
          path.basename(name),
        );
      if (!file) throw Error("Unknown fixture resource");
      res.setHeader(
        "Content-Type",
        mime[path.extname(file)] ?? "application/octet-stream",
      );
      res.end(await fs.readFile(file));
    } catch (error) {
      res.writeHead(404);
      res.end(String(error));
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${server.address().port}`;
  const entry = path.join(root, "electron.cjs");
  await fs.writeFile(
    entry,
    `const {app,BrowserWindow}=require('electron');app.setPath('userData',${JSON.stringify(path.join(root, "profile"))});app.whenReady().then(()=>{let w=new BrowserWindow({show:false,width:1400,height:1000,webPreferences:{sandbox:true,contextIsolation:true,nodeIntegration:false}});w.loadURL(${JSON.stringify(origin)});});`,
  );
  let app;
  const summary = {};
  try {
    app = await electron.launch({
      executablePath: requireDesktop("electron"),
      args: [entry],
    });
    const page = await app.firstWindow();
    page.on("pageerror", (error) => console.log("pageerror:", error.message));
    await page.route("**/*", (route) => {
      const url = new URL(route.request().url());
      return url.origin === origin || url.protocol === "data:"
        ? route.continue()
        : route.abort();
    });
    await page.waitForFunction(() => window.ready, null, { timeout: 30000 });
    for (const [name, kind] of [
      ["complex.docx", "docx"],
      ["styled.pptx", "pptx"],
      ["user-deck.pptx", "pptx"],
      ["complex.xlsx", "xlsx"],
      ["no-freeze.xlsx", "xlsx"],
      ["recalculated.xlsx", "xlsx"],
    ]) {
      if (
        process.env.OFFICE_FIDELITY_FORMAT &&
        kind !== process.env.OFFICE_FIDELITY_FORMAT
      )
        continue;
      const started = Date.now();
      try {
        const result = await page.evaluate(
          ([name, kind]) => window.renderOffice(name, kind),
          [name, kind],
        );
        await fs.writeFile(
          path.join(root, `${name}.json`),
          JSON.stringify(result, null, 2),
        );
        const canvases = page.locator("canvas");
        const count = await canvases.count();
        for (let i = 0; i < count; i++)
          await canvases
            .nth(i)
            .screenshot({ path: path.join(evidence, `${name}-${i + 1}.png`) });
        summary[name] = {
          pagesOrSheets: count,
          inputBytes: result.inputBytes,
          elapsedMs: result.elapsedMs,
          mapped: result.mapped?.ok,
          memory: await app.evaluate(({ app }) =>
            app
              .getAppMetrics()
              .filter((x) => x.type === "Tab")
              .map((x) => x.memory),
          ),
        };
        console.log(name, JSON.stringify(summary[name]));
      } catch (error) {
        summary[name] = {
          error: String(error),
          elapsedMs: Date.now() - started,
        };
        console.log(name, String(error));
      }
    }
    await fs.writeFile(
      path.join(evidence, "results.json"),
      JSON.stringify(summary, null, 2) + "\n",
    );
    if (Object.values(summary).some((x) => x.error)) process.exitCode = 1;
  } finally {
    if (app) await app.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
