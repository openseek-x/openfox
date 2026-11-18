const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const net = require("node:net");
const crypto = require("node:crypto");
const { spawn } = require("node:child_process");
const { once } = require("node:events");
// Use Playwright's already bundled ws fixture dependency; install nothing.
const { createRequire } = require("node:module");
const playwrightRequire = createRequire(
  createRequire(require.resolve("@playwright/test")).resolve("playwright"),
);
const { wsServer: WebSocketServer } = require(
  path.join(
    path.dirname(playwrightRequire.resolve("playwright-core/package.json")),
    "lib/utilsBundle.js",
  ),
);
const { _electron: electron } = require("@playwright/test");

// Requires a freshly built real CLI. Only center/provider responses are fixtures;
// Electron main/preload, owner requests, CLI private files and daemon are real.
test(
  "real Electron owner IPC → CLI → running daemon proxy lifecycle",
  { timeout: 120000, skip: !process.env.OPENFOX_R10_TEST_CLI },
  async () => {
    const directory = await fs.mkdtemp("/private/tmp/of-r10-e-");
    const home = path.join(directory, "home"),
      profile = path.join(directory, "profile");
    const configDir = path.join(
      home,
      "Library",
      "Application Support",
      "OpenFox",
    );
    await fs.mkdir(configDir, { recursive: true, mode: 0o700 });
    const orgId = "11111111-1111-4111-8111-111111111111",
      deviceId = "22222222-2222-4222-8222-222222222222";
    let app,
      daemon,
      owner = true,
      origin,
      connects = 0;
    const proxyRequests = [],
      centerRequests = [],
      sockets = new Set();
    const center = http.createServer((req, res) => {
      centerRequests.push({
        path: req.url,
        session: !!req.headers.cookie?.includes(
          "openfox_session=controlled-owner",
        ),
      });
      res.setHeader("Content-Type", "application/json");
      if (req.url === "/") {
        res.setHeader("Content-Type", "text/html");
        res.end(
          '<!doctype html><title>Machine proxy IPC fixture</title><button id="read">Read machine proxy</button><output id="result"></output>',
        );
        return;
      }
      if (req.url.endsWith("/auth/challenge")) {
        res.end(
          JSON.stringify({
            challenge_id: "controlled",
            nonce: crypto.randomBytes(32).toString("base64url"),
            challenge: crypto.randomBytes(32).toString("base64url"),
          }),
        );
        return;
      }
      if (req.url.endsWith("/auth/token")) {
        res.end(
          JSON.stringify({
            token: "synthetic-device-session",
            expires_at: new Date(Date.now() + 7200000).toISOString(),
          }),
        );
        return;
      }
      if (req.url.endsWith("/offers")) {
        res.end('{"offers":[]}');
        return;
      }
      if (req.url.includes("/commands")) {
        res.end('{"commands":[]}');
        return;
      }
      if (!req.headers.cookie?.includes("openfox_session=controlled-owner")) {
        res.writeHead(403);
        res.end("{}");
        return;
      }
      res.end(
        JSON.stringify(
          req.url.endsWith("/center")
            ? { center_url: origin }
            : req.url === "/api/v1/me"
              ? { id: "owner" }
              : { id: deviceId, owner_user_id: owner ? "owner" : "other" },
        ),
      );
    });
    const ws = new WebSocketServer({ noServer: true });
    center.on("upgrade", (req, socket, head) =>
      ws.handleUpgrade(req, socket, head, (connection) => {
        connects++;
        ws.emit("connection", connection, req);
        connection.on("message", (bytes) => {
          const m = JSON.parse(bytes);
          if (["hello", "heartbeat"].includes(m.type))
            connection.send(
              JSON.stringify({
                protocol_major: 1,
                protocol_minor: 0,
                message_id: m.message_id,
                center_epoch: 1,
                type: m.type + ".ack",
                computer_id: deviceId,
                payload: {
                  device_id: deviceId,
                  center_epoch: 1,
                  version: 1,
                  received_at: new Date().toISOString(),
                },
              }),
            );
        });
      }),
    );
    const proxy = http.createServer((req, res) => {
      proxyRequests.push(req.url);
      const u = new URL(req.url);
      if (u.origin !== origin) {
        res.writeHead(403);
        res.end();
        return;
      }
      const forwarded = http.request(
        u,
        { method: req.method, headers: req.headers, agent: false },
        (response) => {
          res.writeHead(response.statusCode, response.headers);
          response.pipe(res);
        },
      );
      forwarded.on("error", () => {
        res.writeHead(502);
        res.end();
      });
      req.pipe(forwarded);
    });
    proxy.on("upgrade", (req, socket, head) => {
      proxyRequests.push(req.url);
      const u = new URL(req.url);
      if (u.origin !== origin) {
        socket.destroy();
        return;
      }
      const upstream = net.connect(Number(u.port), u.hostname, () => {
        upstream.write(
          `${req.method} ${u.pathname} HTTP/1.1\r\n` +
            Object.entries(req.headers)
              .map(([k, v]) => `${k}: ${v}`)
              .join("\r\n") +
            "\r\n\r\n",
        );
        if (head.length) upstream.write(head);
        socket.pipe(upstream).pipe(socket);
      });
      for (const s of [socket, upstream]) {
        sockets.add(s);
        s.on("close", () => sockets.delete(s));
        s.on("error", () => {
          socket.destroy();
          upstream.destroy();
        });
      }
    });
    const wait = async (check, label) => {
      const until = Date.now() + 15000;
      while (Date.now() < until) {
        if (await check()) return;
        await new Promise((r) => setTimeout(r, 100));
      }
      throw Error("timed out: " + label);
    };
    const env = {
      ...process.env,
      HOME: home,
      TMPDIR: directory,
      PATH: "/usr/bin:/bin:/usr/sbin:/sbin",
    };
    for (const k of [
      "HTTP_PROXY",
      "HTTPS_PROXY",
      "ALL_PROXY",
      "NO_PROXY",
      "http_proxy",
      "https_proxy",
      "all_proxy",
      "no_proxy",
      "NODE_EXTRA_CA_CERTS",
      "SSL_CERT_FILE",
      "SSL_CERT_DIR",
    ])
      delete env[k];
    const configPath = path.join(configDir, "config.json"),
      policyPath = path.join(configDir, "machine-proxy.json");
    const readPolicy = async () =>
      JSON.parse(await fs.readFile(policyPath, "utf8"));
    const writeConfig = async (config) =>
      fs.writeFile(configPath, JSON.stringify(config), { mode: 0o600 });
    let config;
    const startDaemon = () => {
      const child = spawn(
        process.env.OPENFOX_R10_TEST_CLI,
        [
          "daemon",
          "run",
          "--config",
          configPath,
          "--heartbeat-interval",
          "1s",
          "--offer-poll-interval",
          "1s",
        ],
        { env, stdio: ["ignore", "pipe", "pipe"] },
      );
      let log = "";
      child.stdout.on("data", (b) => {
        log = (log + b).slice(-20000);
      });
      child.stderr.on("data", (b) => {
        log = (log + b).slice(-20000);
      });
      child.output = () => log;
      return child;
    };
    const stopDaemon = async () => {
      if (!daemon) return;
      const child = daemon;
      daemon = null;
      if (child.exitCode !== null || child.signalCode) return;
      child.kill("SIGTERM");
      let timer;
      try {
        await Promise.race([
          once(child, "exit"),
          new Promise((_, reject) => {
            timer = setTimeout(() => {
              child.kill("SIGKILL");
              reject(Error("daemon did not stop"));
            }, 8000);
          }),
        ]);
      } finally {
        clearTimeout(timer);
      }
    };
    try {
      center.listen(0, "::");
      await once(center, "listening");
      origin = `http://localhost.:${center.address().port}`;
      proxy.listen(0, "127.0.0.1");
      await once(proxy, "listening");
      const key = path.join(configDir, "device-key"),
        token = path.join(configDir, "device-token");
      await fs.writeFile(
        key,
        "ed25519-seed:" + crypto.randomBytes(32).toString("base64url"),
        { mode: 0o600 },
      );
      await fs.writeFile(token, "synthetic-device-session", { mode: 0o600 });
      config = {
        version: 1,
        center_url: origin,
        wss_url: origin.replace("http:", "ws:") + "/api/v1/device/connect",
        device_id: deviceId,
        organization_id: orgId,
        key_id: "controlled-key",
        private_key_file: key,
        token_file: token,
        workspace_root: path.join(directory, "work"),
        center_epoch: 1,
        runtime_executable: path.join(directory, "absent-cli"),
      };
      await writeConfig(config);
      const entry = path.join(directory, "main.cjs");
      await fs.writeFile(
        entry,
        `const electron=require('electron');require('node:os').homedir=()=>${JSON.stringify(home)};electron.app.setPath('userData',${JSON.stringify(profile)});electron.app.setName('OpenFox R10 isolated test');require(${JSON.stringify(path.join(__dirname, "main.cjs"))});`,
      );
      const launch = async () => {
        app = await electron.launch({
          executablePath: require("electron"),
          args: [entry],
          env: {
            ...env,
            OPENFOX_DESKTOP_WEB_URL: origin,
            OPENFOX_DESKTOP_OPENFOX_BINARY: process.env.OPENFOX_R10_TEST_CLI,
          },
        });
        const page = await app.firstWindow();
        await page.waitForURL(origin + "/");
        await page.waitForFunction(() =>
          Boolean(window.openfoxDesktop?.getMachineProxy),
        );
        return page;
      };
      let page = await launch();
      await app.evaluate(
        async ({ BrowserWindow }, url) =>
          BrowserWindow.getAllWindows()[0].webContents.session.cookies.set({
            url,
            name: "openfox_session",
            value: "controlled-owner",
            httpOnly: true,
            sameSite: "lax",
          }),
        origin,
      );
      const target = { orgId, deviceId, centerUrl: origin };
      const invoke = async (method, input = target) =>
        page.evaluate(
          async ({ method, input }) => {
            try {
              return { result: await window.openfoxDesktop[method](input) };
            } catch (e) {
              return { error: String(e) };
            }
          },
          { method, input },
        );
      try {
        await wait(
          async () => Boolean((await invoke("getMachineProxy")).result),
          "owner IPC ready",
        );
      } catch (e) {
        console.log("requests", JSON.stringify(centerRequests.slice(-12)));
        console.log(
          "cookies",
          await app.evaluate(
            async ({ BrowserWindow }, url) =>
              (
                await BrowserWindow.getAllWindows()[0].webContents.session.cookies.get(
                  { url },
                )
              ).map((c) => ({ name: c.name, domain: c.domain })),
            origin,
          ),
        );
        throw e;
      }
      // DOM interaction crosses the actual exposed preload, not ipcMain called by a test.
      await page.evaluate((target) => {
        document.querySelector("#read").onclick = async () => {
          document.querySelector("#result").textContent = JSON.stringify(
            await window.openfoxDesktop.getMachineProxy(target),
          );
        };
      }, target);
      await page.locator("#read").click();
      await page.waitForFunction(() =>
        document.querySelector("#result").textContent.includes("DISABLED"),
      );
      let view = (await invoke("getMachineProxy")).result;
      assert.equal(view.revision, 0);
      await invoke("saveProxy", { mode: "direct" });
      view = (
        await invoke("saveMachineProxy", {
          ...target,
          expected_revision: view.revision,
          scope_token: view.scope_token,
          mode: "FOLLOW_DESKTOP",
        })
      ).result;
      assert.equal(view.mode, "FOLLOW_DESKTOP");
      assert.equal(view.revision, 1);
      const proxyConfig = {
        mode: "manual",
        protocol: "http",
        host: "127.0.0.1",
        port: proxy.address().port,
        bypass: "",
        authenticated: false,
      };
      const followed = await invoke("saveProxy", proxyConfig);
      assert.equal(followed.result.machine_proxy, "synced");
      view = (await invoke("getMachineProxy")).result;
      assert.equal(view.revision, 2);
      assert.equal(view.effective.port, proxy.address().port);
      daemon = startDaemon();
      await wait(() => connects > 0, "daemon WS");
      await wait(async () => {
        const v = (await invoke("getMachineProxy")).result;
        return v?.runtime?.connection_revision === 2;
      }, "connected revision");
      assert.ok(proxyRequests.some((p) => p.includes("/auth/challenge")));
      assert.ok(proxyRequests.some((p) => p.includes("/auth/token")));
      await wait(
        () => proxyRequests.some((p) => p.includes("/offers")),
        "supervisor HTTP",
      );
      const priorConnects = connects;
      const direct = await invoke("saveProxy", { mode: "direct" });
      assert.equal(direct.result.machine_proxy, "synced");
      await wait(async () => {
        const v = (await invoke("getMachineProxy")).result;
        return (
          v?.revision === 3 &&
          v.runtime.loaded_revision === 3 &&
          v.runtime.connection_revision === 2
        );
      }, "saved/loaded differs from live WS");
      assert.equal(connects, priorConnects);
      owner = false;
      const denied = await invoke("saveProxy", proxyConfig);
      assert.equal(denied.result.machine_proxy, "unverified");
      assert.equal((await readPolicy()).revision, 3);
      assert.match(
        (await invoke("getMachineProxy")).error,
        /local service request failed/,
      );
      owner = true;
      view = (await invoke("getMachineProxy")).result;
      assert.equal(view.revision, 4);
      assert.equal(view.effective.port, proxy.address().port);
      // Desktop closure and daemon restart retain the last verified snapshot.
      await app.close();
      app = null;
      assert.equal((await readPolicy()).revision, 4);
      await stopDaemon();
      daemon = startDaemon();
      await wait(() => connects > priorConnects, "restart reconnect");
      page = await launch();
      await app.evaluate(
        async ({ BrowserWindow }, url) =>
          BrowserWindow.getAllWindows()[0].webContents.session.cookies.set({
            url,
            name: "openfox_session",
            value: "controlled-owner",
            httpOnly: true,
            sameSite: "lax",
          }),
        origin,
      );
      await wait(
        async () => Boolean((await invoke("getMachineProxy")).result),
        "restored owner session",
      );
      view = (await invoke("getMachineProxy")).result;
      assert.equal(view.revision, 4);
      await wait(async () => {
        const v = (await invoke("getMachineProxy")).result;
        return v?.runtime?.connection_revision === 4;
      }, "snapshot after restart");
      config = { ...config, key_id: "replacement-key" };
      await writeConfig(config);
      view = (await invoke("getMachineProxy")).result;
      assert.equal(view.problem, "machine_proxy_registration_changed");
      assert.equal(view.effective, undefined);
      assert.equal(view.mode, "DISABLED");
      const cleared = await invoke("saveMachineProxy", {
        ...target,
        expected_revision: view.revision,
        scope_token: view.scope_token,
        mode: "DISABLED",
      });
      assert.equal(cleared.result.mode, "DISABLED");
      assert.equal(cleared.result.effective, undefined);
      console.log(
        JSON.stringify({
          real_cli: true,
          real_daemon: true,
          owner_ipc: true,
          proxy_requests: proxyRequests.length,
          ws_connects: connects,
          registered_key_endpoint_hidden: true,
          desktop_closed_snapshot_retained: true,
          revisions: [0, 1, 2, 3, 4, 5],
        }),
      );
    } catch (e) {
      console.log("daemon diagnostic", daemon?.output());
      throw e;
    } finally {
      await app?.close();
      await stopDaemon();
      for (const c of ws.clients) c.terminate();
      ws.close();
      for (const s of sockets) s.destroy();
      proxy.closeAllConnections();
      center.closeAllConnections();
      await Promise.all([
        new Promise((r) => proxy.close(r)),
        new Promise((r) => center.close(r)),
      ]);
      await fs.rm(directory, { recursive: true, force: true });
    }
  },
);
