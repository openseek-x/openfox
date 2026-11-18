const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const https = require("node:https");
const crypto = require("node:crypto");
const { execFileSync } = require("node:child_process");
const { once } = require("node:events");
const { _electron: electron } = require("@playwright/test");

test(
  "real Electron isolates websites, enforces proxy routes and restores native view after overlay",
  { timeout: 90000 },
  async (t) => {
    const directory = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-browser-electron-"),
    );
    const evidence =
      process.env.OPENFOX_BROWSER_EVIDENCE_DIR ||
      path.join(directory, "evidence");
    await fs.mkdir(evidence, { recursive: true });
    t.after(() => fs.rm(directory, { recursive: true, force: true }));
    execFileSync(
      "openssl",
      [
        "req",
        "-x509",
        "-newkey",
        "rsa:2048",
        "-nodes",
        "-keyout",
        path.join(directory, "key.pem"),
        "-out",
        path.join(directory, "cert.pem"),
        "-days",
        "1",
        "-subj",
        "/CN=127.0.0.1",
      ],
      { stdio: "ignore" },
    );
    const certificate = await fs.readFile(
      path.join(directory, "cert.pem"),
      "utf8",
    );
    const key = await fs.readFile(path.join(directory, "key.pem"));
    const token = crypto.randomBytes(3).toString("hex").toUpperCase();
    let protectedHits = 0;
    const protectedServer = http.createServer((_req, res) => {
      protectedHits++;
      res.setHeader("Access-Control-Allow-Origin", "*");
      res.end("private");
    });
    protectedServer.on("upgrade", (_req, socket) => {
      protectedHits++;
      socket.destroy();
    });
    protectedServer.listen(0, "127.0.0.1");
    await once(protectedServer, "listening");
    const protectedOrigin = `http://127.0.0.1:${protectedServer.address().port}`;
    const seen = [];
    const handler = (req, res) => {
      seen.push({ path: req.url, cookie: req.headers.cookie || "" });
      res.setHeader("Content-Type", "text/html; charset=utf-8");
      if (req.url === "/redirect-private") {
        res.writeHead(302, { Location: protectedOrigin });
        res.end();
        return;
      }
      res.end(
        `<!doctype html><meta charset="utf-8"><title>Browser verification</title><style>body{margin:0;background:#f5f7fc;color:#152238;font:18px system-ui}main{padding:32px}h1{font-size:30px}input{padding:10px;font:18px system-ui}svg{display:block;margin:20px 0}footer{margin-top:900px}</style><main><small>网页原始布局 · Native WebContentsView</small><h1>Screenshot verification</h1><p>Visual code: <b style="font:36px monospace">${token}</b></p><svg width="500" height="140"><circle cx="85" cy="70" r="52" fill="#df3030"/><rect x="225" y="20" width="105" height="105" fill="#2463eb"/></svg><p>用户与 Agent 看到同一页面</p><input id="draft" placeholder="网页中的输入不会丢失"><footer>Scrollable content</footer></main>`,
      );
    };
    const server = http.createServer(handler);
    const secureServer = https.createServer(
      { key, cert: certificate },
      handler,
    );
    for (const item of [server, secureServer]) {
      item.on("upgrade", (req, socket) => {
        const accept = crypto
          .createHash("sha1")
          .update(
            req.headers["sec-websocket-key"] +
              "258EAFA5-E914-47DA-95CA-C5AB0DC85B11",
          )
          .digest("base64");
        socket.write(
          `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`,
        );
        socket.on("data", () => {
          socket.end(Buffer.from([0x88, 0x00]));
        });
        socket.on("error", () => {});
      });
      item.listen(0, "127.0.0.1");
      await once(item, "listening");
    }
    const origin = `http://127.0.0.1:${server.address().port}`;
    const secureOrigin = `https://127.0.0.1:${secureServer.address().port}`;
    t.after(() => {
      for (const item of [server, secureServer, protectedServer]) {
        item.closeAllConnections();
        item.close();
      }
    });
    const application = await electron.launch({
      executablePath: require("electron"),
      args: [path.join(__dirname, "fixture-main.cjs")],
      env: {
        ...process.env,
        OPENFOX_BROWSER_TEST_CONFIG: JSON.stringify({
          directory,
          evidence,
          origin,
          secureOrigin,
          certificate,
        }),
      },
    });
    t.after(() => application.close());
    // Wait for the real fixture startup; polling has a finite deadline.
    for (let attempt = 0; ; attempt++) {
      if (await application.evaluate(() => Boolean(globalThis.browserFixture)))
        break;
      assert.ok(attempt < 100, "fixture startup deadline");
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    const initial = await application.evaluate(async () => {
      const { surface } = globalThis.browserFixture;
      const prefs = surface.contents.getLastWebPreferences();
      return {
        prefs: {
          sandbox: prefs.sandbox,
          nodeIntegration: prefs.nodeIntegration,
          contextIsolation: prefs.contextIsolation,
          preload: prefs.preload || null,
        },
        globals: await surface.contents.executeJavaScript(
          "({require:typeof require,process:typeof process,bridge:typeof openfoxDesktop,cookie:document.cookie,dpr:devicePixelRatio})",
        ),
      };
    });
    assert.deepEqual(initial.prefs, {
      sandbox: true,
      nodeIntegration: false,
      contextIsolation: true,
      preload: null,
    });
    assert.deepEqual(
      { ...initial.globals, dpr: undefined },
      {
        require: "undefined",
        process: "undefined",
        bridge: "undefined",
        cookie: "",
        dpr: undefined,
      },
    );
    assert.ok(
      seen.every((request) => !request.cookie.includes("main_session")),
    );
    const capture = await application.evaluate(async () =>
      globalThis.browserFixture.save("browser-page.png"),
    );
    assert.ok(capture.bytes > 1000);
    await application.evaluate(async () => {
      const { surface } = globalThis.browserFixture;
      surface.contents.debugger.attach("1.3");
      const result = await surface.contents.debugger.sendCommand(
        "Runtime.evaluate",
        {
          expression:
            "document.querySelector('#draft').value='kept input';scrollTo(0,180);document.querySelector('#draft').value",
          returnByValue: true,
        },
      );
      if (result.result.value !== "kept input")
        throw new Error("CDP input failed");
    });
    const before = await application.evaluate(() =>
      globalThis.browserFixture.surface.contents.executeJavaScript(
        "({scroll:scrollY,input:document.querySelector('#draft').value})",
      ),
    );
    const frozenGeneration = await application.evaluate(() =>
      globalThis.browserFixture.freeze(),
    );
    await application.evaluate(() =>
      globalThis.browserFixture.save("browser-overlay.png", true),
    );
    assert.equal(
      await application.evaluate(() =>
        globalThis.browserFixture.surface.view.getVisible(),
      ),
      false,
    );
    await application.evaluate(() => globalThis.browserFixture.resume());
    assert.deepEqual(
      await application.evaluate(() =>
        globalThis.browserFixture.surface.contents.executeJavaScript(
          "({scroll:scrollY,input:document.querySelector('#draft').value})",
        ),
      ),
      before,
    );
    assert.equal(
      await application.evaluate(
        () => globalThis.browserFixture.surface.generation,
      ),
      frozenGeneration,
    );
    await application.evaluate(() =>
      globalThis.browserFixture.save("browser-restored.png", true),
    );
    const sockets = await application.evaluate(
      async (_electron, urls) => {
        const code = `Promise.all(${JSON.stringify(urls)}.map(url=>new Promise(resolve=>{const socket=new WebSocket(url);const timer=setTimeout(()=>{socket.close();resolve('timeout')},5000);socket.onopen=()=>{clearTimeout(timer);socket.close();resolve('open')};socket.onerror=()=>{clearTimeout(timer);resolve('denied')}})))`;
        return globalThis.browserFixture.surface.contents.executeJavaScript(
          code,
        );
      },
      [
        origin.replace("http:", "ws:"),
        secureOrigin.replace("https:", "wss:"),
        protectedOrigin.replace("http:", "ws:"),
      ],
    );
    assert.deepEqual(sockets, ["open", "open", "denied"]);
    const denied = await application.evaluate(
      async (_electron, url) =>
        globalThis.browserFixture.surface.contents.executeJavaScript(
          `Promise.all([fetch(${JSON.stringify(url)}).then(r=>r.status).catch(()=>0),new Promise(resolve=>{const image=new Image();image.onload=()=>resolve('loaded');image.onerror=()=>resolve('denied');image.src=${JSON.stringify(url + "/image")}})])`,
        ),
      protectedOrigin,
    );
    assert.ok([0, 403].includes(denied[0]));
    assert.equal(denied[1], "denied");
    await application.evaluate(async (_electron, url) => {
      try {
        await globalThis.browserFixture.surface.navigate(url);
      } catch {}
    }, origin + "/redirect-private");
    assert.equal(protectedHits, 0);
    await application.evaluate(
      (_electron, url) => globalThis.browserFixture.surface.navigate(url),
      secureOrigin,
    );
    assert.equal(
      await application.evaluate(() =>
        globalThis.browserFixture.surface.contents.getURL(),
      ),
      secureOrigin + "/",
    );
    await assert.rejects(
      application.evaluate(
        (_electron, url) => globalThis.browserFixture.surface.navigate(url),
        origin.replace("http:", "https:"),
      ),
      /ERR_BLOCKED_BY_CLIENT/,
    );
    await assert.rejects(
      application.evaluate(() =>
        globalThis.browserFixture.surface.navigate("file:///etc/passwd"),
      ),
      /INVALID_URL/,
    );
    await assert.rejects(
      application.evaluate(() =>
        globalThis.browserFixture.surface.place({
          x: 0,
          y: 0,
          width: 2000,
          height: 100,
        }),
      ),
      /INVALID_BOUNDS/,
    );
    const size = await application.evaluate(() => {
      const { window, surface } = globalThis.browserFixture;
      window.setContentSize(900, 700);
      surface.place({ x: 340, y: 80, width: 520, height: 580 });
      return surface.view.getBounds();
    });
    assert.deepEqual(size, { x: 340, y: 80, width: 520, height: 580 });
    const hitsBeforeProxyShutdown = seen.length;
    await application.evaluate(() =>
      globalThis.browserFixture.network.dispose(),
    );
    await assert.rejects(
      application.evaluate(
        (_electron, url) => globalThis.browserFixture.surface.navigate(url),
        origin + "/proxy-offline",
      ),
      /ERR_PROXY_CONNECTION_FAILED|ERR_TUNNEL_CONNECTION_FAILED/,
    );
    assert.equal(
      seen.length,
      hitsBeforeProxyShutdown,
      "proxy failure must not fall back to a direct connection",
    );
    await application.evaluate(() => globalThis.browserFixture.surface.close());
    assert.equal(
      await application.evaluate(
        ({ webContents }) => webContents.getAllWebContents().length,
      ),
      1,
    );
    await fs.writeFile(
      path.join(evidence, "electron-result.json"),
      JSON.stringify(
        {
          verifiedAt: new Date().toISOString(),
          electron: require("electron/package.json").version,
          capture,
          deviceScaleFactor: initial.globals.dpr,
          visualCode: token,
          checks: [
            "isolated partition/no preload/no Node",
            "authenticated proxy HTTP/HTTPS/WS/WSS",
            "private fetch/image/redirect/websocket blocked",
            "exact scheme grant",
            "proxy failure never falls back to direct access",
            "CDP input",
            "capture/hide/overlay/restore preserves scroll/input/document",
            "resize bounds",
            "close releases webContents",
          ],
          protectedHits,
        },
        null,
        2,
      ) + "\n",
    );
  },
);
