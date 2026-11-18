const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { promisify } = require("node:util");
const { execFile } = require("node:child_process");
const { _electron } = require("@playwright/test");

test(
  "transparent native shield preserves the page and separates user takeover from CDP input",
  { timeout: 30000 },
  async (t) => {
    const root = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-input-shield-"),
    );
    t.after(() => fs.rm(root, { recursive: true, force: true }));
    const entry = path.join(root, "main.cjs");
    await fs.writeFile(
      entry,
      `
    const {app,BrowserWindow}=require('electron');
    globalThis.testRequire = require;
    app.setPath('userData',${JSON.stringify(root)});
    app.setName('OpenFox Input Shield Test');
    app.whenReady().then(async()=>{
      const w=new BrowserWindow({width:800,height:600,webPreferences:{sandbox:true,contextIsolation:true}});
      await w.loadURL('data:text/html,'+encodeURIComponent('<!doctype html><style>html,body{margin:0;height:100%;background:#c5f1e7}button{margin:40px;width:180px;height:70px;background:#2767d8;color:white;border:0}</style><button onclick="this.textContent=String(Number(this.textContent)+1)">0</button><h1>OpenFox takeover test</h1>'));
    });
  `,
    );
    const application = await _electron.launch({
      executablePath: require("electron"),
      args: [entry],
    });
    t.after(async () => {
      const timer = setTimeout(
        () => application.process().kill("SIGKILL"),
        5000,
      );
      try {
        await application.close();
      } finally {
        clearTimeout(timer);
      }
    });
    const page = await application.firstWindow();
    await page
      .getByRole("heading", { name: "OpenFox takeover test" })
      .waitFor();
    const windowID = await application.evaluate(
      ({ BrowserWindow }) =>
        BrowserWindow.getAllWindows()[0].getMediaSourceId().split(":")[1],
    );
    const capture = async (name) => {
      const file = path.join(root, name);
      await promisify(execFile)("/usr/sbin/screencapture", [
        "-x",
        "-l",
        windowID,
        file,
      ]);
      return file;
    };
    await capture("before.png");
    await application.evaluate(
      async ({ BrowserWindow }, modulePath) => {
        const window = BrowserWindow.getAllWindows()[0];
        const { createInputShield } = globalThis.testRequire(modulePath);
        globalThis.shieldTest = {
          window,
          takeovers: 0,
          initialChildren: window.contentView.children.length,
        };
        const shield = createInputShield({
          window,
          takeOver: () => {
            globalThis.shieldTest.takeovers++;
          },
        });
        globalThis.shieldTest.shield = shield;
        await shield.ready;
        const [width, height] = window.getContentSize();
        shield.show({ x: 0, y: 0, width, height });
        await shield.contents.capturePage();
      },
      path.join(__dirname, "input-shield.cjs"),
    );
    await capture("shield.png");
    const pixels = await application.evaluate(({ nativeImage }, directory) => {
      const before = nativeImage.createFromPath(
        globalThis.testRequire("node:path").join(directory, "before.png"),
      );
      const shield = nativeImage.createFromPath(
        globalThis.testRequire("node:path").join(directory, "shield.png"),
      );
      const size = before.getSize();
      // Sample the colored page well inside the window, excluding window chrome.
      const rect = {
        x: Math.floor(size.width / 2),
        y: Math.floor(size.height / 2),
        width: 100,
        height: 100,
      };
      return {
        equal: before
          .crop(rect)
          .toBitmap()
          .equals(shield.crop(rect).toBitmap()),
        sample: [...shield.crop(rect).toBitmap().subarray(0, 4)],
      };
    }, root);
    assert.equal(pixels.equal, true, "input shield must remain transparent");
    assert.notDeepEqual(pixels.sample, [255, 255, 255, 255]);
    const counts = await application.evaluate(async () => {
      const { window, shield } = globalThis.shieldTest;
      const contents = window.webContents;
      contents.debugger.attach("1.3");
      await contents.debugger.sendCommand("Input.dispatchMouseEvent", {
        type: "mousePressed",
        x: 100,
        y: 70,
        button: "left",
        clickCount: 1,
      });
      await contents.debugger.sendCommand("Input.dispatchMouseEvent", {
        type: "mouseReleased",
        x: 100,
        y: 70,
        button: "left",
        clickCount: 1,
      });
      const beforeUser = globalThis.shieldTest.takeovers;
      shield.contents.sendInputEvent({
        type: "mouseDown",
        x: 100,
        y: 70,
        button: "left",
        clickCount: 1,
      });
      shield.contents.sendInputEvent({
        type: "mouseUp",
        x: 100,
        y: 70,
        button: "left",
        clickCount: 1,
      });
      return { beforeUser, afterUser: globalThis.shieldTest.takeovers };
    });
    assert.equal(
      counts.beforeUser,
      0,
      "Agent injection must not trigger takeover",
    );
    assert.equal(
      counts.afterUser,
      1,
      "user input must immediately revoke control",
    );
    await assert.doesNotReject(() =>
      page.getByRole("button", { name: "1", exact: true }).waitFor(),
    );
    const released = await application.evaluate(async ({ webContents }) => {
      const { window, shield } = globalThis.shieldTest;
      const id = shield.contents.id;
      await shield.close();
      return {
        alive: Boolean(webContents.fromId(id)),
        children: window.contentView.children.length,
        initialChildren: globalThis.shieldTest.initialChildren,
      };
    });
    assert.equal(released.alive, false);
    assert.equal(released.children, released.initialChildren);
    if (process.env.OPENFOX_BROWSER_EVIDENCE_DIR) {
      await fs.mkdir(process.env.OPENFOX_BROWSER_EVIDENCE_DIR, {
        recursive: true,
      });
      for (const name of ["before.png", "shield.png"])
        await fs.copyFile(
          path.join(root, name),
          path.join(process.env.OPENFOX_BROWSER_EVIDENCE_DIR, `input-${name}`),
        );
      await fs.writeFile(
        path.join(
          process.env.OPENFOX_BROWSER_EVIDENCE_DIR,
          "input-shield-result.json",
        ),
        JSON.stringify({ pixels, counts, released }, null, 2),
      );
    }
  },
);
