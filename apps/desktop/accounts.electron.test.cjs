const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { once } = require("node:events");
const { _electron } = require("@playwright/test");
const { openAccounts } = require("./accounts.cjs");

async function localSite(t) {
  const server = http.createServer((_request, response) => {
    response.writeHead(200, { "Content-Type": "text/html" });
    response.end("<title>Account test</title>");
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const stop = async () => {
    if (!server.listening) return;
    const closed = once(server, "close");
    server.close();
    server.closeAllConnections();
    await closed;
  };
  t.after(stop);
  return { origin: "http://127.0.0.1:" + server.address().port, stop };
}

test(
  "offline retry opens the configured origin with its selected account",
  { timeout: 60000 },
  async (t) => {
    const root = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-accounts-"));
    const profile = path.join(root, "profile");
    await fs.mkdir(profile);
    const first = await localSite(t);
    const second = await localSite(t);
    const saved = await openAccounts(profile, second.origin);
    await saved.select(null);
    await saved.name("Saved login");
    const settings = path.join(profile, "desktop-settings.json");
    const entry = path.join(root, "main.cjs");
    await fs.writeFile(
      entry,
      [
        'const { app } = require("electron");',
        'app.setPath("userData", ' + JSON.stringify(profile) + ");",
        'require("node:os").homedir = () => ' + JSON.stringify(root) + ";",
        "require(" + JSON.stringify(path.join(__dirname, "main.cjs")) + ");",
      ].join("\n"),
    );
    let application;
    try {
      application = await _electron.launch({
        executablePath: require("electron"),
        args: [entry],
        env: {
          ...process.env,
          HOME: root,
          // Override any packaged default so the first window has no configured URL.
          OPENFOX_DESKTOP_WEB_URL: " ",
          OPENFOX_DESKTOP_OPENFOX_BINARY: path.join(root, "unused-cli"),
        },
      });
      const offline = await application.firstWindow();
      await offline.waitForURL(
        (url) => url.searchParams.get("reason") === "not_configured",
      );
      await fs.writeFile(settings, JSON.stringify({ web_url: first.origin }));
      await application.evaluate(() => {
        delete process.env.OPENFOX_DESKTOP_WEB_URL;
      });

      async function replacement(origin, action) {
        const [page] = await Promise.all([
          application.waitForEvent("window", { timeout: 10000 }),
          action(),
        ]);
        await page.waitForURL(origin + "/");
        return page;
      }
      const firstLogin = await replacement(first.origin, () =>
        offline.locator("#retry").click(),
      );
      assert.deepEqual(
        await firstLogin.evaluate(() => window.openfoxDesktop.accounts()),
        [{ id: "default", name: "", current: true }],
      );
      await firstLogin.evaluate(async () => {
        document.cookie = "slot=first";
        await window.openfoxDesktop.nameAccount("First login");
      });
      const added = await replacement(first.origin, () =>
        firstLogin.evaluate(() => {
          void window.openfoxDesktop.selectAccount(null);
        }),
      );
      const slots = await added.evaluate(() =>
        window.openfoxDesktop.accounts(),
      );
      assert.equal(slots.length, 2);
      assert.ok(slots.some((s) => s.id !== "default" && s.current));
      assert.equal(await added.evaluate(() => document.cookie), "");
      const original = await replacement(first.origin, () =>
        added.evaluate(() => {
          void window.openfoxDesktop.selectAccount("default");
        }),
      );
      assert.equal(
        await original.evaluate(() => document.cookie),
        "slot=first",
      );

      await first.stop();
      await original.evaluate(() => location.reload());
      const retry = original.locator("#retry");
      await retry.waitFor({ state: "visible" });
      assert.equal(
        new URL(original.url()).searchParams.get("reason"),
        "unreachable",
      );
      await fs.writeFile(settings, JSON.stringify({ web_url: second.origin }));
      const restored = await replacement(second.origin, () => retry.click());
      assert.deepEqual(
        await restored.evaluate(() => window.openfoxDesktop.accounts()),
        saved.list(),
      );
      assert.equal(
        await application.evaluate(
          ({ BrowserWindow, session }, partition) =>
            BrowserWindow.getAllWindows()[0].webContents.session ===
            session.fromPartition(partition),
          saved.partition(),
        ),
        true,
      );
    } finally {
      if (application) {
        await Promise.all([
          application.waitForEvent("close"),
          application.evaluate(({ app }) => {
            setImmediate(() => app.exit(0));
          }),
        ]);
      }
      await fs.rm(root, { recursive: true, force: true });
    }
  },
);
