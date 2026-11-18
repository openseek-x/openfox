const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const { createHash } = require("node:crypto");
const { once } = require("node:events");
const { _electron } = require("@playwright/test");

test(
  "native page controls target fresh elements across frames, bound screenshots, and revoke before input",
  { timeout: 45000 },
  async (t) => {
    const root = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-page-control-"),
    );
    t.after(() => fs.rm(root, { recursive: true, force: true }));
    let origin;
    const server = http.createServer((req, res) => {
      if (req.url === "/download") {
        res.setHeader(
          "Content-Disposition",
          'attachment; filename="download.txt"',
        );
        res.end("download fixture");
        return;
      }
      res.setHeader("Content-Type", "text/html; charset=utf-8");
      if (req.url === "/frame") {
        res.end(
          '<!doctype html><style>body{margin:10px}</style><button onclick="this.textContent=\'Frame clicked\'">Frame button</button><input aria-label="Frame field"><script>console.log("frame-console-fixture")</script>',
        );
        return;
      }
      res.end(
        `<!doctype html><title>Page control</title><style>body{background:#b9f4e5}iframe{width:400px;height:160px;transform:scale(.85);transform-origin:top left;border:8px solid blue}</style><button id="action" onclick="this.textContent='Clicked'">Primary button</button><input aria-label="Name"><input type="password" aria-label="Password" value="NEVER_RETURN_THIS"><label><input type="checkbox">Agree</label><select aria-label="Choice"><option value="a">A</option><option value="b">B</option></select><input type="file" aria-label="Upload"><a href="/download">Download fixture</a><p><iframe src="${origin.replace("127.0.0.1", "localhost")}/frame"></iframe></p><script>console.log('fixture-ready')</script>`,
      );
    });
    server.listen(0, "::");
    await once(server, "listening");
    origin = `http://127.0.0.1:${server.address().port}`;
    t.after(
      () =>
        new Promise((resolve) => {
          server.closeAllConnections();
          server.close(resolve);
        }),
    );
    const entry = path.join(root, "main.cjs");
    await fs.writeFile(
      entry,
      `
    const {app,BrowserWindow,WebContentsView}=require('electron');
    const {createPageControl}=require(${JSON.stringify(path.join(__dirname, "page-control.cjs"))});
    const {createBrowserFiles}=require(${JSON.stringify(path.join(__dirname, "files.cjs"))});
    app.setPath('userData',${JSON.stringify(root)});
    app.setName('OpenFox Page Control Test');
    app.whenReady().then(async()=>{
      const window=new BrowserWindow({width:900,height:700,webPreferences:{sandbox:true}});
      await window.loadURL('data:text/html,<title>OpenFox Page Control Test</title>');
      const view=new WebContentsView({webPreferences:{sandbox:true,contextIsolation:true}});
      window.contentView.addChildView(view);view.setBounds({x:0,y:0,width:900,height:650});
      await view.webContents.loadURL(${JSON.stringify(origin)});
      const controller=createPageControl(view.webContents);await controller.ready;
      const files=createBrowserFiles({root:${JSON.stringify(path.join(root, "browser"))}});
      files.start('10000000-0000-4000-8000-000000000001',1);
      view.webContents.session.on('will-download',(_event,item)=>files.captureDownload(item,'tab'));
      globalThis.fixture={window,view,controller,files,allowed:true};
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
    for (let i = 0; i < 100; i++) {
      if (await application.evaluate(() => Boolean(globalThis.fixture))) break;
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    const invoke = (method, args = {}) =>
      application.evaluate(
        async (_electron, { method, args }) => {
          const { controller } = globalThis.fixture;
          const guard = (url) => {
            if (!globalThis.fixture.allowed)
              throw Object.assign(new Error("CONTROL_REVOKED"), {
                code: "CONTROL_REVOKED",
              });
            if (
              globalThis.fixture.blockedOrigin &&
              url?.startsWith(globalThis.fixture.blockedOrigin)
            )
              throw Object.assign(new Error("ORIGIN_NOT_ALLOWED"), {
                code: "ORIGIN_NOT_ALLOWED",
              });
          };
          try {
            const result =
              method === "frames" ||
              method === "screenshot" ||
              method === "console"
                ? await controller[method](guard)
                : await controller[method](args, guard);
            return { result };
          } catch (error) {
            return { error: error.code || error.message };
          }
        },
        { method, args },
      );
    const frames = (await invoke("frames")).result;
    assert.equal(frames.length, 2);
    const parent = frames.find((f) => !f.parent_id),
      child = frames.find((f) => f.parent_id);
    assert.match(child.url, /localhost/);
    const snapshot = async (frame) => {
      const result = await invoke("snapshot", { frame_id: frame.frame_id });
      assert.equal(result.error, undefined);
      return result.result;
    };
    const action = async (frame, name, args) => {
      const page = await snapshot(frame);
      const target = page.nodes.find((node) => node.name === name);
      assert.ok(target, `${name} missing`);
      return invoke("act", {
        frame_id: page.frame_id,
        document_generation: page.document_generation,
        ref: target.ref,
        ...args,
      });
    };
    let page = await snapshot(parent);
    assert.equal(JSON.stringify(page).includes("NEVER_RETURN_THIS"), false);
    assert.equal(
      (await action(parent, "Name", { action: "fill", text: "OpenFox value" }))
        .error,
      undefined,
    );
    assert.equal(
      (await action(parent, "Agree", { action: "check", checked: true })).error,
      undefined,
    );
    assert.equal(
      (await action(parent, "Choice", { action: "select", values: ["b"] }))
        .error,
      undefined,
    );
    assert.equal(
      (
        await action(child, "Frame field", {
          action: "fill",
          text: "inside frame",
        })
      ).error,
      undefined,
    );
    assert.equal(
      (await action(child, "Frame button", { action: "click" })).error,
      undefined,
    );
    const filled = await snapshot(child);
    assert.equal(
      filled.nodes.find((node) => node.name === "Frame field")?.value,
      "inside frame",
    );
    assert.ok(
      (await snapshot(child)).nodes.some(
        (node) => node.name === "Frame clicked",
      ),
    );
    page = await snapshot(parent);
    assert.equal(
      page.nodes.find((node) => node.name === "Name")?.value,
      "OpenFox value",
    );
    assert.equal(
      page.nodes.find((node) => node.name === "Agree")?.checked,
      "true",
    );
    const primary = page.nodes.find((node) => node.name === "Primary button");
    const stale = {
      action: "click",
      frame_id: page.frame_id,
      document_generation: page.document_generation,
      ref: primary.ref,
    };
    await application.evaluate(async () => {
      await globalThis.fixture.view.webContents.executeJavaScript(
        "document.body.insertAdjacentHTML('beforeend','<div id=cover style=\"position:fixed;inset:0;background:white;z-index:9999\"></div>')",
      );
    });
    assert.equal((await invoke("act", stale)).error, "TARGET_NOT_VISIBLE");
    await application.evaluate(async () => {
      await globalThis.fixture.view.webContents.executeJavaScript(
        "document.querySelector('#cover').remove()",
      );
    });
    await application.evaluate(async () => {
      await globalThis.fixture.view.webContents.executeJavaScript(
        "document.querySelector('#action').outerHTML='<button>Replacement</button>'",
      );
    });
    assert.equal((await invoke("act", stale)).error, "STALE_TARGET");
    await application.evaluate(async () => {
      await globalThis.fixture.view.webContents.loadURL(
        globalThis.fixture.view.webContents.getURL(),
      );
    });
    assert.equal((await invoke("act", stale)).error, "STALE_TARGET");
    page = await snapshot(parent);
    const fresh = page.nodes.find((node) => node.name === "Primary button");
    await application.evaluate(() => {
      globalThis.fixture.allowed = false;
    });
    assert.equal(
      (
        await invoke("act", {
          ...stale,
          ref: fresh.ref,
          document_generation: page.document_generation,
        })
      ).error,
      "CONTROL_REVOKED",
    );
    await application.evaluate(() => {
      globalThis.fixture.allowed = true;
    });
    const screenshot = (await invoke("screenshot")).result;
    assert.equal(screenshot.mime_type, "image/png");
    const png = Buffer.from(screenshot.data, "base64");
    assert.ok(png.length > 1000 && png.length <= 4 * 1024 * 1024);
    assert.ok(png.readUInt32BE(16) <= 2048 && png.readUInt32BE(20) <= 2048);
    const values = await application.evaluate(async () =>
      globalThis.fixture.view.webContents.executeJavaScript(
        "({name:document.querySelector('[aria-label=Name]').value,button:document.querySelector('#action').textContent})",
      ),
    );
    assert.equal(values.button, "Primary button");
    const uploadPath = path.join(
      root,
      "browser",
      "transfers",
      "upload-fixture",
      "0",
      "report.txt",
    );
    await fs.mkdir(path.dirname(uploadPath), { recursive: true, mode: 0o700 });
    await fs.writeFile(uploadPath, "upload fixture", { mode: 0o600 });
    const uploaded = await application.evaluate(
      async (_electron, file) => {
        const { controller, files, view } = globalThis.fixture;
        const guard = () => {};
        const page = await controller.snapshot({}, guard);
        const ref = page.nodes.find((node) => node.name === "Upload");
        const paths = await files.uploads([file], guard);
        await controller.upload(
          {
            frame_id: page.frame_id,
            document_generation: page.document_generation,
            ref: ref.ref,
          },
          paths,
          guard,
        );
        return view.webContents.executeJavaScript(
          "document.querySelector('input[type=file]').files[0].text()",
        );
      },
      {
        path: uploadPath,
        size: 14,
        sha256: createHash("sha256").update("upload fixture").digest("hex"),
      },
    );
    assert.equal(uploaded, "upload fixture");
    assert.equal(
      (await action(parent, "Download fixture", { action: "click" })).error,
      undefined,
    );
    let downloaded;
    for (let attempt = 0; attempt < 100; attempt++) {
      downloaded = await application.evaluate(() =>
        globalThis.fixture.files
          .list("tab")
          .find((file) => file.name === "download.txt"),
      );
      if (downloaded?.state === "completed" || downloaded?.state === "failed")
        break;
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    assert.equal(downloaded?.state, "completed");
    const download = await application.evaluate(
      (_electron, id) => globalThis.fixture.files.get("tab", id),
      downloaded.id,
    );
    assert.equal(
      await fs.readFile(download.file.path, "utf8"),
      "download fixture",
    );
    await application.evaluate(async () => {
      await globalThis.fixture.view.webContents.executeJavaScript(
        "document.querySelector('iframe').remove()",
      );
    });
    assert.equal((await invoke("frames")).result.length, 1);
    assert.ok(
      (await snapshot(parent)).nodes.some((node) => node.name === "Name"),
    );
    await application.evaluate((_electron, origin) => {
      globalThis.fixture.blockedOrigin = origin;
    }, new URL(child.url).origin);
    // Retained console messages keep their original origin after iframe removal.
    assert.equal((await invoke("console")).error, "ORIGIN_NOT_ALLOWED");
    await application.evaluate(() => {
      globalThis.fixture.blockedOrigin = null;
    });
    // Playwright otherwise auto-dismisses Chromium dialogs before our
    // production CDP controller can handle them.
    for (const page of application.context().pages())
      page.on("dialog", () => {});
    await application.evaluate(async () => {
      await globalThis.fixture.view.webContents.debugger.sendCommand(
        "Runtime.evaluate",
        {
          expression:
            "setTimeout(() => {window.dialogAnswer = confirm('Fixture confirmation')}, 0)",
        },
      );
    });
    let dialog;
    for (let attempt = 0; attempt < 100; attempt++) {
      dialog = (await invoke("console")).result.dialog;
      if (dialog) break;
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    assert.equal(dialog?.type, "confirm");
    assert.equal(
      (await invoke("act", { action: "dialog", accept: true })).error,
      undefined,
    );
    assert.equal(
      await application.evaluate(async () =>
        globalThis.fixture.view.webContents.executeJavaScript(
          "window.dialogAnswer",
        ),
      ),
      true,
    );
    await application.evaluate(async () => {
      await globalThis.fixture.files.close();
    });
    await assert.rejects(fs.stat(download.file.path), { code: "ENOENT" });
    const disconnected = await application.evaluate(() => {
      globalThis.fixture.controller.close();
      return !globalThis.fixture.view.webContents.debugger.isAttached();
    });
    assert.equal(disconnected, true);
    if (process.env.OPENFOX_BROWSER_EVIDENCE_DIR) {
      await fs.mkdir(process.env.OPENFOX_BROWSER_EVIDENCE_DIR, {
        recursive: true,
      });
      await fs.writeFile(
        path.join(process.env.OPENFOX_BROWSER_EVIDENCE_DIR, "page-control.png"),
        png,
      );
      await fs.writeFile(
        path.join(
          process.env.OPENFOX_BROWSER_EVIDENCE_DIR,
          "page-control-result.json",
        ),
        JSON.stringify(
          {
            frames: frames.length,
            crossFrame: true,
            staleTarget: true,
            revokedInput: true,
            fileUpload: true,
            fileDownload: true,
            detachedFrame: true,
            dialogAccepted: true,
            consoleOriginChecked: true,
            screenshotBytes: png.length,
            disconnected,
          },
          null,
          2,
        ),
      );
    }
  },
);
