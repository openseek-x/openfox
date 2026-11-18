const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const net = require("node:net");
const { once } = require("node:events");
const { execFile } = require("node:child_process");
const { promisify } = require("node:util");
const { _electron: electron } = require("@playwright/test");

test(
  "real main/preload browser IPC isolates conversations, supports popup login and releases contents",
  {
    timeout: process.env.OPENFOX_BROWSER_PUBLIC_SMOKE === "1" ? 180000 : 90000,
  },
  async (t) => {
    const directory = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-browser-host-"),
    );
    t.after(() => fs.rm(directory, { recursive: true, force: true }));
    const orgId = "11111111-1111-4111-8111-111111111111";
    const conversationId = "22222222-2222-4222-8222-222222222222";
    const otherConversation = "33333333-3333-4333-8333-333333333333";
    const deviceId = "44444444-4444-4444-8444-444444444444";
    let deviceOwner = "another-user",
      binding,
      bindingRequests = 0;
    const chat = (id) => ({
      organization_id: orgId,
      id,
      space_id: "s1",
      agent_id: "a1",
      owner_user_id: "user-two",
      visibility: "PRIVATE",
      lifecycle: "OPEN",
      handoff: "NONE",
      origin: "USER",
      version: 1,
      title: "浏览器验收",
      created_at: "2026-11-16T00:00:00Z",
      updated_at: "2026-11-16T00:00:00Z",
      archived_at: null,
      pinned: false,
      last_read_seq: 0,
      unread: false,
      last_message: null,
      agent: {
        id: "a1",
        name: "测试 Agent",
        title: "",
        avatar: null,
        online: false,
      },
      task: null,
      run: null,
      status: "IDLE",
      overrides: {
        runtime: null,
        model: null,
        approval_mode: null,
        version: 0,
      },
      effective: {
        runtime: "codex",
        model: "default",
        approval_mode: "READ_ONLY",
      },
      agent_defaults: {
        runtime: "codex",
        model: "default",
        approval_mode: "READ_ONLY",
      },
    });
    let user = "user-one";
    const site = http.createServer((req, res) => {
      if (req.url === "/download") {
        res.setHeader(
          "Content-Disposition",
          'attachment; filename="fixture.txt"',
        );
        res.end("download fixture");
        return;
      }
      res.setHeader("Content-Type", "text/html; charset=utf-8");
      res.end(
        '<!doctype html><title>Website fixture</title><h1>Website</h1><input id="value"><a id="download" href="/download">Download</a><script>window.addEventListener("message",event=>{if(event.origin===location.origin)document.title=event.data})</script>',
      );
    });
    const center = http.createServer(async (req, res) => {
      if (req.url.startsWith("/api/")) {
        res.setHeader("Content-Type", "application/json");
        if (!req.headers.cookie?.includes("openfox_session=")) {
          res.writeHead(401);
          res.end("{}");
          return;
        }
        if (req.url === "/api/v1/me") {
          res.end(JSON.stringify({ id: user, email: "browser@example.test" }));
          return;
        }
        const pathname = new URL(req.url, "http://fixture").pathname;
        if (pathname === `/api/v1/organizations/${orgId}/devices/${deviceId}`) {
          res.end(JSON.stringify({ id: deviceId, owner_user_id: deviceOwner }));
          return;
        }
        if (
          pathname ===
            `/api/v1/organizations/${orgId}/conversations/${conversationId}/browser-bindings` &&
          req.method === "POST"
        ) {
          if (req.headers["x-csrf-token"] !== "fixture-csrf") {
            res.writeHead(403).end("{}");
            return;
          }
          let raw = "";
          for await (const chunk of req) raw += chunk;
          const input = JSON.parse(raw);
          bindingRequests++;
          binding = {
            id: otherConversation,
            organization_id: orgId,
            conversation_id: conversationId,
            owner_user_id: user,
            device_id: input.device_id,
            host_instance_id: input.host_instance_id,
            host_epoch: input.host_epoch,
            revision: bindingRequests,
            lease_expires_at: new Date(Date.now() + 30000).toISOString(),
          };
          res.end(
            JSON.stringify({ ...binding, claim_secret: "fixture-claim" }),
          );
          return;
        }
        const responses = {
          "/api/v1/me/organizations": {
            organizations: [
              { id: orgId, slug: "qa", name: "浏览器测试", role: "OWNER" },
            ],
          },
          "/api/v1/me/preferences": {
            locale: "zh-CN",
            theme: "dark",
            outline_color: 0,
            send_shortcut: "enter",
            command_shortcuts: {},
            version: 1,
          },
          [`/api/v1/organizations/${orgId}/spaces`]: {
            spaces: [
              {
                id: "s1",
                name: "研发",
                prefix: "DEV",
                visibility: "shared",
                created_at: "2026-11-16T00:00:00Z",
              },
            ],
          },
          [`/api/v1/organizations/${orgId}/agent-directory`]: {
            entries: [],
            archived_count: 0,
            truncated: false,
          },
          [`/api/v1/organizations/${orgId}/devices`]: {
            devices: [],
            next_cursor: null,
          },
          [`/api/v1/organizations/${orgId}/conversations`]: {
            conversations: [chat(conversationId)],
            next_cursor: null,
          },
          [`/api/v1/organizations/${orgId}/conversations/${conversationId}/messages`]:
            { messages: [], next_cursor: 0 },
          [`/api/v1/organizations/${orgId}/conversations/${conversationId}/artifacts`]:
            { artifacts: [] },
          [`/api/v1/organizations/${orgId}/direct-chats`]: {
            chats: [],
            next_before: "",
          },
          [`/api/v1/organizations/${orgId}/inbox/stats`]: {
            counts: {
              inbox: 0,
              inbox_unread: 0,
              inbox_important_unread: 0,
              inbox_other_unread: 0,
              later: 0,
              cleared: 0,
            },
          },
        };
        if (responses[pathname]) {
          res.end(JSON.stringify(responses[pathname]));
          return;
        }
        const match = new RegExp(
          `^/api/v1/organizations/${orgId}/conversations/(${conversationId}|${otherConversation})$`,
        ).exec(req.url);
        if (match) {
          res.end(JSON.stringify(chat(match[1])));
          return;
        }
        res.writeHead(403);
        res.end("{}");
        return;
      }
      if (req.url.startsWith("/static/") || req.url.startsWith("/o/")) {
        const dist = path.resolve(__dirname, "../../web/dist");
        const asset = req.url.startsWith("/static/")
          ? path.resolve(
              dist,
              "." + new URL(req.url, "http://fixture").pathname,
            )
          : path.join(dist, "index.html");
        if (!asset.startsWith(dist + path.sep)) {
          res.writeHead(403).end();
          return;
        }
        try {
          const mime = {
            ".html": "text/html",
            ".js": "text/javascript",
            ".css": "text/css",
            ".woff2": "font/woff2",
          };
          res.setHeader(
            "Content-Type",
            mime[path.extname(asset)] || "application/octet-stream",
          );
          res.end(await fs.readFile(asset));
        } catch {
          res.writeHead(404).end();
        }
        return;
      }
      res.setHeader("Content-Type", "text/html");
      res.end(
        "<!doctype html><title>Trusted app fixture</title><h1>Browser host integration</h1>",
      );
    });
    for (const server of [site, center]) {
      server.listen(0, "127.0.0.1");
      await once(server, "listening");
    }
    t.after(() => {
      for (const server of [site, center]) {
        server.closeAllConnections();
        server.close();
      }
    });
    const siteOrigin = `http://127.0.0.1:${site.address().port}`;
    const centerOrigin = `http://127.0.0.1:${center.address().port}`;
    const profile = path.join(directory, "profile");
    await fs.mkdir(profile);
    await fs.writeFile(
      path.join(profile, "network-proxy.json"),
      JSON.stringify({
        config: {
          mode:
            process.env.OPENFOX_BROWSER_PUBLIC_SMOKE === "1"
              ? "system"
              : "direct",
        },
      }),
    );
    const cli = path.join(directory, "openfox-fixture");
    const cliState = path.join(directory, "cli-state.json");
    await fs.writeFile(
      cliState,
      JSON.stringify({
        registration: { registered: false, reason: "not_registered" },
        daemon: { state: "STOPPED", runtime: "codex", runnable: false },
      }),
    );
    await fs.writeFile(
      cli,
      `#!${process.execPath}\nconst state=JSON.parse(require('node:fs').readFileSync(${JSON.stringify(cliState)},'utf8'));process.stdout.write(JSON.stringify(process.argv[2]==='computer'?state.registration:state.daemon));\n`,
      { mode: 0o700 },
    );
    const packaged = process.env.OPENFOX_BROWSER_PACKAGED_RESOURCES;
    const mainEntry = packaged
      ? path.join(packaged, "app.asar", "main.cjs")
      : path.resolve(__dirname, "../main.cjs");
    const entry = path.join(directory, "main.cjs");
    await fs.writeFile(
      entry,
      `process.on('uncaughtException',error=>{process.stderr.write(String(error.stack)+'\\n');process.exit(1)});const electron=require('electron');require('node:os').homedir=()=>${JSON.stringify(directory)};electron.app.setPath('userData',${JSON.stringify(profile)});electron.app.setName('OpenFox Browser Host Test');${packaged ? `Object.defineProperty(electron.app,'isPackaged',{value:true});Object.defineProperty(process,'resourcesPath',{value:${JSON.stringify(packaged)}});electron.app.setAppPath(${JSON.stringify(path.join(packaged, "app.asar"))});` : ""}require(${JSON.stringify(mainEntry)});`,
    );
    const application = await electron.launch({
      executablePath: require("electron"),
      args: [entry],
      env: {
        ...process.env,
        OPENFOX_DESKTOP_WEB_URL: centerOrigin,
        OPENFOX_DESKTOP_OPENFOX_BINARY: cli,
      },
    });
    application.process().stderr.on("data", (data) => {
      if (/Error|Exception/.test(String(data))) process.stderr.write(data);
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
    await page.waitForURL(centerOrigin + "/");
    await page.waitForFunction(() =>
      Boolean(window.openfoxDesktop?.browserMount),
    );
    await page.evaluate(() =>
      window.openfoxDesktop.onBrowserState(
        (state) => (window.browserState = state),
      ),
    );
    await application.evaluate(async ({ BrowserWindow, dialog }, origin) => {
      // Only the explicit fixture origin permission prompt is accepted in this test.
      const original = dialog.showMessageBox;
      dialog.showMessageBox = (...args) =>
        String(args.at(-1)?.message).startsWith("允许此会话访问本地地址")
          ? Promise.resolve({ response: 1 })
          : original(...args);
      await BrowserWindow.getAllWindows()[0].webContents.session.cookies.set({
        url: origin,
        name: "openfox_session",
        value: "fixture-one",
        httpOnly: true,
      });
    }, centerOrigin);
    const mount = (id = conversationId) =>
      page.evaluate((input) => window.openfoxDesktop.browserMount(input), {
        orgId,
        conversationId: id,
      });
    const command = (state, input) =>
      page.evaluate((value) => window.openfoxDesktop.browserCommand(value), {
        lease: state.lease,
        ...input,
      });
    const website = (expression, url = siteOrigin) =>
      application.evaluate(
        async ({ webContents }, { expression, url }) => {
          const contents = webContents
            .getAllWebContents()
            .find((item) => item.getURL().startsWith(url));
          if (!contents) throw Error("website contents missing");
          return contents.executeJavaScript(expression);
        },
        { expression, url },
      );
    const waitState = async (predicate) => {
      await page.waitForFunction(predicate, undefined, { timeout: 10000 });
      return page.evaluate(() => window.browserState);
    };
    let first = await mount();
    assert.equal(first.tabs.length, 1);
    await command(first, {
      action: "layout",
      visible: true,
      bounds: { x: 400, y: 100, width: 600, height: 500 },
    });
    await command(first, {
      action: "navigate",
      tabId: first.activeTab,
      url: siteOrigin + "/first",
    });
    await waitState(
      () => window.browserState?.tabs[0]?.error === "NETWORK_DENIED",
    );
    await command(first, { action: "allow-origin", url: siteOrigin });
    await command(first, { action: "reload", tabId: first.activeTab });
    await waitState(
      () =>
        window.browserState?.tabs[0]?.title === "Website fixture" &&
        !window.browserState.tabs[0].loading,
    );
    assert.deepEqual(
      await website(
        "({bridge:typeof openfoxDesktop,node:typeof require,cookie:document.cookie})",
      ),
      { bridge: "undefined", node: "undefined", cookie: "" },
    );
    await website(
      "document.cookie='site_login=conversation-one';document.querySelector('#value').value='keep me'",
    );
    await website("window.open('/popup','login');true");
    first = await waitState(
      () =>
        window.browserState?.tabs.length === 2 &&
        window.browserState.tabs.every((tab) => !tab.loading && tab.title),
    );
    assert.equal(
      await website("Boolean(window.opener)", siteOrigin + "/popup"),
      true,
    );
    await website(
      "window.opener.postMessage('login returned',location.origin);window.close();true",
      siteOrigin + "/popup",
    ).catch(() => {});
    first = await waitState(
      () =>
        window.browserState?.tabs.length === 1 &&
        window.browserState.tabs[0].title === "login returned",
    );
    assert.equal(
      await website("document.querySelector('#value').value"),
      "keep me",
    );
    await command(first, {
      action: "navigate",
      tabId: first.activeTab,
      url: siteOrigin + "/second",
    });
    first = await waitState(
      () =>
        window.browserState?.tabs[0]?.url.endsWith("/second") &&
        !window.browserState.tabs[0].loading,
    );
    assert.equal(first.tabs[0].canBack, true);
    await command(first, { action: "back", tabId: first.activeTab });
    await waitState(
      () =>
        window.browserState?.tabs[0]?.url.endsWith("/first") &&
        !window.browserState.tabs[0].loading,
    );
    await command(first, { action: "forward", tabId: first.activeTab });
    await waitState(
      () =>
        window.browserState?.tabs[0]?.url.endsWith("/second") &&
        !window.browserState.tabs[0].loading,
    );
    const downloadPath = path.join(directory, "download.txt");
    await application.evaluate(
      ({ webContents }, { origin, downloadPath }) => {
        const contents = webContents
          .getAllWebContents()
          .find((item) => item.getURL().startsWith(origin));
        contents.session.on("will-download", (_event, item) =>
          item.setSavePath(downloadPath),
        );
      },
      { origin: siteOrigin, downloadPath },
    );
    await website("document.querySelector('#download').click();true");
    first = await waitState(
      () => window.browserState?.downloads[0]?.state === "completed",
    );
    assert.equal(await fs.readFile(downloadPath, "utf8"), "download fixture");
    const firstTab = first.activeTab;
    for (let i = 0; i < 7; i++)
      first = await command(first, { action: "open" });
    assert.equal(first.tabs.length, 8);
    await assert.rejects(command(first, { action: "open" }), /LIMIT_EXCEEDED/);
    for (const tab of first.tabs)
      if (tab.id !== firstTab)
        await command(first, { action: "close", tabId: tab.id });
    let second = await mount(otherConversation);
    await assert.rejects(command(first, { action: "open" }), /STALE_TARGET/);
    await command(second, { action: "allow-origin", url: siteOrigin });
    await command(second, {
      action: "navigate",
      tabId: second.activeTab,
      url: siteOrigin + "/other",
    });
    await waitState(
      () =>
        window.browserState?.tabs[0]?.url.endsWith("/other") &&
        !window.browserState.tabs[0].loading,
    );
    assert.equal(await website("document.cookie", siteOrigin + "/other"), "");
    first = await mount();
    assert.equal(first.tabs[0].id, firstTab);
    assert.equal(
      await website("document.cookie", siteOrigin + "/second"),
      "site_login=conversation-one",
    );
    await assert.rejects(
      command(first, { action: "allow-origin", url: centerOrigin }),
      /UNAVAILABLE/,
    );
    await command(first, {
      action: "navigate",
      tabId: first.activeTab,
      url: centerOrigin + "/api/v1/me",
    });
    await waitState(
      () => window.browserState?.tabs[0]?.error === "PROTECTED_ENDPOINT",
    );
    first = await command(first, { action: "clear" });
    assert.equal(first.tabs.length, 1);
    await command(first, {
      action: "navigate",
      tabId: first.activeTab,
      url: siteOrigin + "/cleared",
    });
    await waitState(
      () =>
        window.browserState?.tabs[0]?.url.endsWith("/cleared") &&
        !window.browserState.tabs[0].loading,
    );
    assert.equal(await website("document.cookie", siteOrigin + "/cleared"), "");
    user = "user-two";
    await application.evaluate(
      async ({ BrowserWindow }, origin) =>
        BrowserWindow.getAllWindows()[0].webContents.session.cookies.set({
          url: origin,
          name: "openfox_session",
          value: "fixture-two",
          httpOnly: true,
        }),
      centerOrigin,
    );
    await waitState(() => window.browserState?.reset === true);
    await assert.rejects(command(first, { action: "open" }), /STALE_TARGET/);
    await page.waitForTimeout(50);
    assert.equal(
      await application.evaluate(
        ({ webContents }) => webContents.getAllWebContents().length,
      ),
      1,
    );
    const newUser = await mount();
    assert.equal(newUser.tabs.length, 1);
    assert.equal(newUser.tabs[0].url, "about:blank");
    await assert.rejects(
      page.evaluate((input) => window.openfoxDesktop.browserMount(input), {
        orgId: otherConversation,
        conversationId,
      }),
      /UNAVAILABLE|FORBIDDEN/,
    );

    // The shipped React app and real native view together (requires build:web).
    await page.addInitScript(
      (id) =>
        localStorage.setItem(
          `openfox:onboarding:${id}`,
          JSON.stringify({ step: "done" }),
        ),
      orgId,
    );
    await page.goto(
      `${centerOrigin}/o/${orgId}/chat/${conversationId}?artifact=browser`,
    );
    const address = page.getByRole("textbox", { name: "输入网址" });
    await address.waitFor({ timeout: 15000 });
    await address.fill(siteOrigin + "/ui");
    await address.press("Enter");
    await page
      .getByRole("button", { name: "允许访问此本地地址" })
      .click({ timeout: 10000 })
      .catch(async (error) => {
        error.message += `\nFixture UI: ${(await page.locator("body").innerText()).slice(-4000)}`;
        throw error;
      });
    await page
      .getByRole("tab", { name: "Website fixture" })
      .waitFor({ timeout: 10000 });
    const workspace = page.getByRole("complementary", { name: "会话产物" });
    assert.equal(await workspace.getByRole("tablist").count(), 1);
    assert.equal(
      await workspace.getByRole("tab", { name: "浏览器", exact: true }).count(),
      0,
    );
    assert.equal(
      await workspace.getByRole("tab", { name: "产物 0 个" }).count(),
      0,
    );
    const nativeBounds = () =>
      application.evaluate(({ BrowserWindow }) =>
        BrowserWindow.getAllWindows()[0]
          .contentView.children.filter(
            (view) =>
              view.webContents &&
              view.webContents !== BrowserWindow.getAllWindows()[0].webContents,
          )
          .filter((view) => view.getVisible())
          .map((view) => view.getBounds()),
      );
    const bounds = await nativeBounds();
    assert.equal(bounds.length, 1);
    assert.ok(bounds[0].width >= 280 && bounds[0].height >= 300);
    // Expanded chrome updates native bounds, and restoring keeps the page alive.
    await page.getByRole("button", { name: "展开工作区", exact: true }).click();
    await page.waitForTimeout(100);
    assert.ok((await nativeBounds())[0].width > bounds[0].width);
    await page
      .getByRole("button", { name: "恢复侧栏宽度", exact: true })
      .click();
    await page.waitForTimeout(100);
    assert.deepEqual(await nativeBounds(), bounds);
    await page
      .getByRole("button", { name: "浏览器更多操作", exact: true })
      .click();
    await page.getByRole("menu", { name: "浏览器更多操作" }).waitFor();
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 0);
    await page.keyboard.press("Escape");
    await page.waitForTimeout(100);
    assert.deepEqual(await nativeBounds(), bounds);
    // The real React editor hands off only after its screenshot has loaded.
    const annotate = page.getByRole("button", { name: "批注", exact: true });
    assert.equal(await annotate.getAttribute("aria-pressed"), "false");
    const originalLayout = await website(
      "JSON.stringify(document.querySelector('h1').getBoundingClientRect())",
      siteOrigin + "/ui",
    );
    await annotate.click();
    await page.waitForFunction(
      () =>
        document
          .querySelector('button[aria-label="批注"]')
          ?.getAttribute("aria-pressed") === "true",
    );
    await application.evaluate(async ({ webContents }) => {
      const site = webContents
        .getAllWebContents()
        .find((c) => c.getURL().endsWith("/ui"));
      const point = await site.executeJavaScript(
        "(()=>{const r=document.querySelector('h1').getBoundingClientRect();return {x:r.x+5,y:r.y+5}})()",
      );
      let picker;
      for (let n = 0; n < 100; n++) {
        picker = webContents
          .getAllWebContents()
          .find(
            (c) =>
              c.getURL().startsWith("data:") &&
              c.getURL().includes("highlight"),
          );
        if (picker && !picker.isLoading()) break;
        await new Promise((r) => setTimeout(r, 10));
      }
      picker.sendInputEvent({
        type: "mouseDown",
        button: "left",
        clickCount: 1,
        ...point,
      });
      picker.sendInputEvent({
        type: "mouseUp",
        button: "left",
        clickCount: 1,
        ...point,
      });
    });
    const feedback = page.getByRole("textbox", { name: "写下你的反馈…" });
    await feedback.fill("让这个标题更清楚");
    assert.notEqual(
      await page
        .getByRole("form", { name: "添加网页批注" })
        .evaluate((node) => getComputedStyle(node).backgroundColor),
      "rgba(0, 0, 0, 0)",
    );
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 0);
    assert.equal(
      await website(
        "JSON.stringify(document.querySelector('h1').getBoundingClientRect())",
        siteOrigin + "/ui",
      ),
      originalLayout,
    );
    if (process.env.OPENFOX_BROWSER_EVIDENCE_DIR) {
      await fs.mkdir(process.env.OPENFOX_BROWSER_EVIDENCE_DIR, {
        recursive: true,
      });
      await page.screenshot({
        path: path.join(
          process.env.OPENFOX_BROWSER_EVIDENCE_DIR,
          "annotation-editor.png",
        ),
      });
    }
    await page.getByRole("button", { name: "添加到聊天", exact: true }).click();
    await page
      .getByLabel("网页批注", { exact: true })
      .getByText("让这个标题更清楚")
      .waitFor();
    await annotate.click();
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 1);
    await website(
      "document.querySelector('#value').value='preserved UI value'",
      siteOrigin + "/ui",
    );
    await page.evaluate(() => {
      const dialog = document.createElement("div");
      dialog.role = "dialog";
      dialog.id = "fixture-dialog";
      dialog.textContent = "Overlay";
      document.body.append(dialog);
    });
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 0);
    await page.locator("#fixture-dialog").evaluate((node) => node.remove());
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 1);
    assert.equal(
      await website(
        "document.querySelector('#value').value",
        siteOrigin + "/ui",
      ),
      "preserved UI value",
    );
    await application.evaluate(({ webContents }) => {
      const contents = webContents
        .getAllWebContents()
        .find((item) => item.getURL().endsWith("/ui"));
      contents.focus();
      contents.sendInputEvent({
        type: "keyDown",
        keyCode: "L",
        modifiers: ["meta"],
      });
      contents.sendInputEvent({
        type: "keyUp",
        keyCode: "L",
        modifiers: ["meta"],
      });
    });
    await page.waitForFunction(
      () => document.activeElement?.getAttribute("aria-label") === "输入网址",
    );
    const evidence = process.env.OPENFOX_BROWSER_EVIDENCE_DIR;
    if (evidence) {
      await fs.mkdir(evidence, { recursive: true });
      await page.screenshot({ path: path.join(evidence, "browser-panel.png") });
      const image = await application.evaluate(async ({ webContents }) =>
        (
          await webContents
            .getAllWebContents()
            .find((item) => item.getURL().endsWith("/ui"))
            .capturePage()
        )
          .toPNG()
          .toString("base64"),
      );
      await fs.writeFile(
        path.join(evidence, "browser-native.png"),
        Buffer.from(image, "base64"),
      );
      const nativeWindowID = await application.evaluate(
        ({ BrowserWindow }) =>
          BrowserWindow.getAllWindows()[0].getMediaSourceId().split(":")[1],
      );
      // Capture only this isolated test window, including its native child view.
      await promisify(execFile)(
        "/usr/sbin/screencapture",
        ["-x", "-l", nativeWindowID, path.join(evidence, "browser-window.png")],
        { timeout: 5000 },
      );
    }
    await application.evaluate(({ BrowserWindow }) =>
      BrowserWindow.getAllWindows()[0].setSize(800, 700),
    );
    await page.waitForTimeout(150);
    assert.equal((await nativeBounds()).length, 1);
    await page.getByRole("button", { name: "打开新标签页" }).click();
    await page.getByRole("button", { name: "浏览产物（0）" }).click();
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 0);
    await page.getByRole("tab", { name: "Website fixture" }).click();
    await page.waitForTimeout(100);
    assert.equal((await nativeBounds()).length, 1);
    assert.equal(
      await website(
        "document.querySelector('#value').value",
        siteOrigin + "/ui",
      ),
      "preserved UI value",
    );
    await page.getByRole("button", { name: "关闭 Website fixture" }).click();
    await page.getByRole("tab", { name: "产物 0 个" }).waitFor();
    await page.getByRole("button", { name: "关闭 产物 0 个" }).click();
    await workspace.waitFor({ state: "detached" });

    // Exercise the production main-process registration/owner/CSRF/binding
    // path, with a private protocol fixture instead of the user's daemon.
    const brokerRoot = await fs.mkdtemp("/tmp/ofx-host-");
    const browserRoot = path.join(brokerRoot, "browser");
    await fs.mkdir(browserRoot, { mode: 0o700 });
    const sockets = new Set();
    let claimSeen = false,
      nativeGrant;
    const broker = net.createServer((socket) => {
      sockets.add(socket);
      socket.once("close", () => sockets.delete(socket));
      let buffer = Buffer.alloc(0);
      const send = (value) => {
        const payload = Buffer.from(JSON.stringify({ version: 1, ...value }));
        const header = Buffer.alloc(4);
        header.writeUInt32BE(payload.length);
        socket.write(Buffer.concat([header, payload]));
      };
      socket.on("data", (chunk) => {
        buffer = Buffer.concat([buffer, chunk]);
        while (
          buffer.length >= 4 &&
          buffer.length >= 4 + buffer.readUInt32BE(0)
        ) {
          const size = buffer.readUInt32BE(0),
            frame = JSON.parse(buffer.subarray(4, size + 4));
          buffer = buffer.subarray(size + 4);
          if (frame.type === "attach") {
            claimSeen =
              frame.attach.claim_secret === "fixture-claim" &&
              frame.attach.host_instance_id === binding.host_instance_id;
            send({ type: "attached", binding });
          } else if (frame.type === "grant") {
            nativeGrant = frame.grant;
            send({ type: "grant-result", id: frame.id });
          } else if (frame.type === "ping")
            send({ type: "pong", id: frame.id });
        }
      });
    });
    broker.listen(path.join(browserRoot, "browser.sock"));
    await once(broker, "listening");
    await fs.chmod(path.join(browserRoot, "browser.sock"), 0o600);
    t.after(async () => {
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => broker.close(resolve));
      await fs.rm(brokerRoot, { recursive: true, force: true });
    });
    await fs.writeFile(
      cliState,
      JSON.stringify({
        registration: {
          registered: true,
          organization_id: orgId,
          device_id: deviceId,
          center_url: centerOrigin,
        },
        daemon: {
          state: "RUNNING",
          runtime: "codex",
          runnable: true,
          socket_path: path.join(brokerRoot, "daemon.sock"),
        },
      }),
    );
    // The previous UI interactions persist asynchronously; navigation must wait
    // for the local save rather than dismissing its beforeunload protection.
    await page
      .getByText("正在保存到本机…", { exact: true })
      .waitFor({ state: "hidden" });
    await page.goto(centerOrigin);
    await page.evaluate(() =>
      window.openfoxDesktop.onBrowserState((state) => {
        window.browserState = state;
      }),
    );
    await application.evaluate(async ({ BrowserWindow }, origin) => {
      await BrowserWindow.getAllWindows()[0].webContents.session.cookies.set({
        url: origin,
        name: "openfox_csrf",
        value: "fixture-csrf",
      });
    }, centerOrigin);
    let controlled = await mount();
    controlled = await command(controlled, {
      action: "layout",
      visible: true,
      bounds: { x: 50, y: 50, width: 500, height: 450 },
    });
    await command(controlled, { action: "allow-origin", url: siteOrigin });
    controlled = await command(controlled, {
      action: "navigate",
      tabId: controlled.activeTab,
      url: siteOrigin + "/controlled",
    });
    await waitState(() =>
      window.browserState?.tabs.some(
        (tab) =>
          tab.url.endsWith("/controlled") &&
          tab.title === "Website fixture" &&
          !tab.loading,
      ),
    );
    const authorization = {
      action: "allow-control",
      clientMessageId: conversationId,
      origins: [siteOrigin],
    };
    await assert.rejects(
      command(controlled, authorization).catch(async (error) => {
        if (!error.message.includes("FORBIDDEN"))
          error.message += JSON.stringify(
            await application.evaluate(({ BrowserWindow }) => {
              const w = BrowserWindow.getAllWindows()[0];
              return {
                visible: w.isVisible(),
                minimized: w.isMinimized(),
                views: w.contentView.children.map((view) => ({
                  visible: view.getVisible(),
                  url: view.webContents?.getURL(),
                  bounds: view.getBounds(),
                })),
              };
            }),
          );
        throw error;
      }),
      /FORBIDDEN/,
    );
    assert.equal(bindingRequests, 0);
    deviceOwner = user;
    controlled = await command(controlled, authorization);
    assert.equal(controlled.control.active, true);
    assert.equal(claimSeen, true);
    assert.equal(bindingRequests, 1);
    assert.equal(nativeGrant.client_message_id, conversationId);
    controlled = await command(controlled, { action: "take-over" });
    assert.equal(controlled.control.active, false);
    await command(controlled, { action: "close", tabId: controlled.activeTab });
    if (process.env.OPENFOX_BROWSER_PUBLIC_SMOKE === "1") {
      for (const [url, expected] of [
        ["https://example.com", "Example Domain"],
        ["https://baidu.com", "百度"],
      ]) {
        controlled = await command(controlled, { action: "open", url });
        await page.waitForFunction(
          (title) =>
            window.browserState?.tabs.some(
              (tab) => tab.title.includes(title) && !tab.loading && !tab.error,
            ),
          expected,
          { timeout: 45000 },
        );
        const loadedURL = await page.evaluate(
          (id) => window.browserState.tabs.find((tab) => tab.id === id).url,
          controlled.activeTab,
        );
        const result = await website(
          "({title:document.title,text:document.body.innerText.slice(0,500)})",
          loadedURL,
        );
        assert.ok(result.title.includes(expected), JSON.stringify(result));
        assert.ok(result.text.length > 20);
        console.log(
          "Public browser smoke:",
          JSON.stringify({ url, loadedURL, title: result.title }),
        );
        if (process.env.OPENFOX_BROWSER_EVIDENCE_DIR) {
          const filename = path.join(
            process.env.OPENFOX_BROWSER_EVIDENCE_DIR,
            new URL(url).hostname + ".png",
          );
          await fs.mkdir(path.dirname(filename), { recursive: true });
          const bytes = await application.evaluate(
            async ({ webContents }, origin) => {
              const contents = webContents
                .getAllWebContents()
                .find((item) => item.getURL().startsWith(origin));
              return (await contents.capturePage()).toPNG().toString("base64");
            },
            loadedURL,
          );
          await fs.writeFile(filename, Buffer.from(bytes, "base64"));
        }
        await command(controlled, {
          action: "close",
          tabId: controlled.activeTab,
        });
      }
    }
    // Losing the trusted UI must not leave hidden pages or grants alive.
    await command(controlled, {
      action: "open",
      url: siteOrigin + "/before-ui-crash",
    });
    await application.evaluate(({ BrowserWindow }) =>
      BrowserWindow.getAllWindows()[0].webContents.forcefullyCrashRenderer(),
    );
    let survivors;
    for (let n = 0; n < 100; n++) {
      survivors = await application.evaluate(
        ({ webContents, BrowserWindow }) =>
          webContents
            .getAllWebContents()
            .filter(
              (c) => c.id !== BrowserWindow.getAllWindows()[0].webContents.id,
            ).length,
      );
      if (survivors === 0) break;
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    assert.equal(
      survivors,
      0,
      "main renderer crash left native browser contents alive",
    );
  },
);
