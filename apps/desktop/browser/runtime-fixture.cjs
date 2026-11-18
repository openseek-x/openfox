// Explicit integration-test process only; excluded from desktop packaging.
const fs = require("node:fs/promises");
const configPath =
  process.env.OPENFOX_BROWSER_FIXTURE_CONFIG || process.argv[2];

if (process.versions.electron) {
  const { app, BrowserWindow, webContents } = require("electron");
  const { createBrowserHost } = require("./host.cjs");
  void (async () => {
    const config = JSON.parse(await fs.readFile(configPath, "utf8"));
    app.setName("OpenFox Runtime Browser Test");
    app.setPath("userData", config.profile);
    await app.whenReady();
    const window = new BrowserWindow({
      width: 900,
      height: 700,
      webPreferences: { sandbox: true },
    });
    await window.loadURL(
      "data:text/html,<title>OpenFox Runtime Browser Test</title>",
    );
    const host = createBrowserHost({
      window,
      authorize: async () => ({
        center: "https://browser-test.invalid",
        userId: config.owner,
        deviceId: config.device,
      }),
      networkOptions: () => ({ allowedOrigins: [config.origin] }),
      connectControl: async (_input, _identity, control) => ({
        root: config.root,
        socketPath: config.socket,
        claim: {
          organization_id: config.org,
          binding_id: config.binding,
          claim_secret: "fixture-claim",
          host_instance_id: control.hostInstanceID,
          host_epoch: control.hostEpoch,
          revision: 1,
        },
      }),
    });
    const mounted = await host.mount({
      orgId: config.org,
      conversationId: config.conversation,
    });
    let state = await host.command({
      lease: mounted.lease,
      action: "open",
      url: config.origin,
    });
    await host.command({
      lease: state.lease,
      action: "layout",
      visible: true,
      bounds: { x: 0, y: 0, width: 900, height: 650 },
    });
    const page = webContents
      .getAllWebContents()
      .find((c) => c.getURL().startsWith(config.origin));
    if (page?.isLoading())
      await new Promise((resolve) => page.once("did-stop-loading", resolve));
    state = await host.command({
      lease: state.lease,
      action: "allow-control",
      runId: config.run,
      generation: 1,
      origins: [config.origin],
    });
    globalThis.browserRuntimeFixture = { host, state, page };
  })().catch((error) => {
    process.stderr.write(
      `fixture main: ${String(error.code || error.message).slice(0, 1000)}\n`,
    );
    app.exit(1);
  });
} else {
  const { _electron } = require("@playwright/test");
  void (async () => {
    const config = JSON.parse(await fs.readFile(configPath, "utf8"));
    const application = await _electron.launch({
      executablePath: require("electron"),
      args: [__filename, configPath],
      env: { ...process.env, OPENFOX_BROWSER_FIXTURE_CONFIG: configPath },
    });
    let stopping = false;
    const close = async () => {
      if (stopping) return;
      stopping = true;
      const timer = setTimeout(
        () => application.process().kill("SIGKILL"),
        5000,
      );
      try {
        await application.evaluate(async () => {
          await globalThis.browserRuntimeFixture?.host.dispose();
        });
        await application.close();
      } finally {
        clearTimeout(timer);
        process.exit(0);
      }
    };
    process.once("SIGTERM", close);
    process.stdin.once("data", close);
    process.stdin.once("end", close);
    for (let i = 0; i < 200; i++) {
      if (
        await application.evaluate(() =>
          Boolean(globalThis.browserRuntimeFixture),
        )
      ) {
        await fs.writeFile(config.ready, "ready", { mode: 0o600 });
        return;
      }
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    await close();
  })().catch((error) => {
    process.stderr.write(
      `fixture driver: ${String(error.message).slice(0, 1000)}\n`,
    );
    process.exit(1);
  });
}
