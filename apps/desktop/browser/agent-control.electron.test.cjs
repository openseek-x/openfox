const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const os = require("node:os");
const net = require("node:net");
const http = require("node:http");
const { once } = require("node:events");
const { _electron } = require("@playwright/test");

test(
  "BrowserHost authorizes one Run, serves native tools through the socket and revokes on human input",
  { timeout: 45000 },
  async (t) => {
    const root = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-agent-browser-"),
    );
    t.after(() => fs.rm(root, { recursive: true, force: true }));
    const browserRoot = path.join(root, "browser");
    await fs.mkdir(browserRoot, { mode: 0o700 });
    const socketPath = path.join(browserRoot, "browser.sock");
    const ids = {
      org: "10000000-0000-4000-8000-000000000001",
      conversation: "10000000-0000-4000-8000-000000000002",
      device: "10000000-0000-4000-8000-000000000003",
      owner: "10000000-0000-4000-8000-000000000004",
      binding: "10000000-0000-4000-8000-000000000005",
      run: "10000000-0000-4000-8000-000000000006",
    };
    const site = http.createServer((_req, res) => {
      res.setHeader("Content-Type", "text/html");
      res.end(
        "<!doctype html><title>Agent fixture</title><style>body{background:#dcf2e8}</style><button onclick=\"this.textContent='Clicked';document.body.style.background='#b3c9ff'\">Click me</button><input aria-label=\"Name\">",
      );
    });
    site.listen(0, "127.0.0.1");
    await once(site, "listening");
    const origin = `http://127.0.0.1:${site.address().port}`;
    t.after(
      () =>
        new Promise((resolve) => {
          site.closeAllConnections();
          site.close(resolve);
        }),
    );
    let socket,
      grant,
      sequence = 0,
      attached = 0;
    const replies = new Map();
    const send = (value) => {
      const payload = Buffer.from(JSON.stringify({ version: 1, ...value }));
      const length = Buffer.alloc(4);
      length.writeUInt32BE(payload.length);
      socket.write(Buffer.concat([length, payload]));
    };
    const server = net.createServer((client) => {
      socket = client;
      let buffer = Buffer.alloc(0);
      client.on("data", (chunk) => {
        buffer = Buffer.concat([buffer, chunk]);
        while (
          buffer.length >= 4 &&
          buffer.length >= 4 + buffer.readUInt32BE(0)
        ) {
          const size = buffer.readUInt32BE(0),
            frame = JSON.parse(buffer.subarray(4, size + 4));
          buffer = buffer.subarray(size + 4);
          if (frame.type === "attach") {
            attached++;
            send({
              type: "attached",
              binding: {
                id: ids.binding,
                organization_id: ids.org,
                conversation_id: ids.conversation,
                device_id: ids.device,
                owner_user_id: ids.owner,
                host_instance_id: frame.attach.host_instance_id,
                host_epoch: frame.attach.host_epoch,
                revision: frame.attach.revision,
                lease_expires_at: new Date(Date.now() + 30000).toISOString(),
              },
            });
          } else if (frame.type === "grant") {
            grant = frame.grant;
            send({ type: "grant-result", id: frame.id });
          } else if (frame.type === "ping")
            send({ type: "pong", id: frame.id });
          else if (frame.type === "result") {
            const waiter = replies.get(frame.id);
            if (waiter) {
              replies.delete(frame.id);
              clearTimeout(waiter.timer);
              waiter.resolve(frame.result);
            }
          }
        }
      });
    });
    server.listen(socketPath);
    await once(server, "listening");
    await fs.chmod(socketPath, 0o600);
    t.after(
      () =>
        new Promise((resolve) => {
          socket?.destroy();
          server.close(resolve);
        }),
    );
    const call = (tool, args, extra = {}) =>
      new Promise((resolve, reject) => {
        const id = String(++sequence);
        replies.set(id, {
          resolve,
          timer: setTimeout(() => {
            replies.delete(id);
            reject(new Error("tool timed out"));
          }, 5000),
        });
        send({
          type: "execute",
          id,
          request: {
            id: `call-${id}`,
            tool: `openfox_browser_${tool}`,
            arguments: args,
            run_id: ids.run,
            generation: 1,
            control_epoch: grant.epoch,
            read_only: false,
            origins: [origin],
            lease_expires_at: new Date(Date.now() + 30000).toISOString(),
            ...extra,
          },
        });
      });
    const entry = path.join(root, "main.cjs");
    await fs.writeFile(
      entry,
      `
    const {app,BrowserWindow,shell}=require('electron');
    const openedExternal=[];
    shell.openExternal=async(url)=>{openedExternal.push(url)};
    const {createBrowserHost}=require(${JSON.stringify(path.join(__dirname, "host.cjs"))});
    app.setName('OpenFox Agent Browser Test'); app.setPath('userData',${JSON.stringify(root)});
    app.whenReady().then(async()=>{
      const window=new BrowserWindow({width:900,height:700,webPreferences:{sandbox:true}});
      await window.loadURL('data:text/html,<title>OpenFox Agent Browser Test</title>');
      const host=createBrowserHost({window,authorize:async()=>({center:'https://center.example.test',userId:${JSON.stringify(ids.owner)},deviceId:${JSON.stringify(ids.device)}}),networkOptions:()=>({allowedOrigins:[${JSON.stringify(origin)}]}),
        connectControl:async(_input,_identity,control)=>({root:${JSON.stringify(browserRoot)},socketPath:${JSON.stringify(socketPath)},claim:{organization_id:${JSON.stringify(ids.org)},binding_id:${JSON.stringify(ids.binding)},claim_secret:'fixture-claim',host_instance_id:control.hostInstanceID,host_epoch:control.hostEpoch,revision:1}})});
      const mounted=await host.mount({orgId:${JSON.stringify(ids.org)},conversationId:${JSON.stringify(ids.conversation)}});
      let state=await host.command({lease:mounted.lease,action:'open',url:${JSON.stringify(origin + "/review/panel-b?case=ope23#sample")}});
      state=await host.command({lease:state.lease,action:'layout',visible:true,bounds:{x:0,y:0,width:900,height:650}});
      globalThis.fixture={window,host,state,openedExternal};
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
      await new Promise((r) => setTimeout(r, 20));
    }
    const command = (input) =>
      application.evaluate(async (_electron, input) => {
        const f = globalThis.fixture;
        f.state = await f.host.command({ lease: f.state.lease, ...input });
        return f.state;
      }, input);
    await application.evaluate(async ({ webContents }, origin) => {
      const contents = webContents
        .getAllWebContents()
        .find((c) => c.getURL().startsWith(origin));
      if (contents?.isLoading())
        await new Promise((r) => contents.once("did-stop-loading", r));
    }, origin);
    const state = await command({
      action: "allow-control",
      runId: ids.run,
      generation: 1,
      origins: [origin],
    });
    assert.equal(state.control.active, true);
    assert.equal(attached, 1);
    const listed = await call("tabs", { action: "list" });
    assert.equal(listed.ok, true);
    const tab = JSON.parse(listed.text).tabs[0].tab_id;
    const snapshot = async () => {
      const reply = await call("observe", { action: "snapshot", tab_id: tab });
      assert.equal(reply.ok, true, reply.text);
      return JSON.parse(reply.text);
    };
    const page = await snapshot();
    const input = page.nodes.find((n) => n.name === "Name");
    const fill = {
      action: "fill",
      tab_id: tab,
      frame_id: page.frame_id,
      document_generation: page.document_generation,
      ref: input.ref,
      text: "shared browser",
    };
    assert.equal(
      (await call("act", fill, { read_only: true })).text,
      "FORBIDDEN",
    );
    assert.equal(
      (await call("act", { ...fill, script: "alert(1)" })).text,
      "INVALID_REQUEST",
    );
    assert.equal(
      (await call("act", fill, { run_id: ids.owner })).text,
      "FORBIDDEN",
    );
    assert.equal((await call("act", fill)).ok, true);
    const afterFill = await snapshot();
    assert.equal(
      afterFill.nodes.find((n) => n.name === "Name").value,
      "shared browser",
    );
    const button = afterFill.nodes.find((n) => n.name === "Click me");
    const click = await call("act", {
      action: "click",
      tab_id: tab,
      frame_id: afterFill.frame_id,
      document_generation: afterFill.document_generation,
      ref: button.ref,
    });
    assert.equal(click.ok, true, click.text);
    assert.ok((await snapshot()).nodes.some((n) => n.name === "Clicked"));
    const screenshot = await call("observe", {
      action: "screenshot",
      tab_id: tab,
    });
    assert.equal(screenshot.ok, true, screenshot.text);
    assert.equal(screenshot.image.mime_type, "image/png");
    const pixel = await application.evaluate(({ nativeImage }, data) => {
      const image = nativeImage.createFromBuffer(Buffer.from(data, "base64"));
      const { width, height } = image.getSize();
      const offset =
        (Math.floor(height / 2) * width + Math.floor(width / 2)) * 4;
      return [...image.toBitmap().subarray(offset, offset + 3)];
    }, screenshot.image.data);
    assert.deepEqual(
      pixel,
      [255, 201, 179],
      "screenshot must reflect the completed click",
    );
    // Agent screenshot focus must not lose the grant through a layout/hide loop.
    assert.equal((await call("tabs", { action: "list" })).ok, true);
    // 瞬时菜单遮挡（occluded: true）隐藏原生视图但保留活动共用，且系统外开与恢复布局后同一运行仍可操作
    const occludedState = await command({
      action: "layout",
      visible: false,
      occluded: true,
    });
    assert.equal(occludedState.control.active, true);
    assert.equal(
      await application.evaluate(
        () =>
          globalThis.fixture.window.contentView.children.filter((v) =>
            v.getVisible(),
          ).length,
      ),
      0,
    );
    assert.equal((await call("tabs", { action: "list" })).ok, true);
    const externalState = await command({
      action: "open-external",
      tabId: tab,
    });
    assert.equal(externalState.control.active, true);
    assert.deepEqual(
      await application.evaluate(() => globalThis.fixture.openedExternal),
      [origin + "/review/panel-b?case=ope23#sample"],
    );
    const restoredState = await command({
      action: "layout",
      visible: true,
      bounds: { x: 0, y: 0, width: 900, height: 650 },
    });
    assert.equal(restoredState.control.active, true);
    assert.equal(
      (await call("observe", { action: "snapshot", tab_id: tab })).ok,
      true,
    );
    // 非瞬时隐藏（无 occluded: true，如打开模态框或视口坍缩）仍立即撤权
    const hiddenLayoutState = await command({
      action: "layout",
      visible: false,
    });
    assert.equal(hiddenLayoutState.control.active, false);
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await command({
      action: "layout",
      visible: true,
      bounds: { x: 0, y: 0, width: 900, height: 650 },
    });
    await command({
      action: "allow-control",
      runId: ids.run,
      generation: 1,
      origins: [origin],
    });
    await application.evaluate(({ webContents }, origin) => {
      const contents = webContents
        .getAllWebContents()
        .find((c) => c.getURL().startsWith(origin));
      contents.sendInputEvent({ type: "keyDown", keyCode: "X" });
    }, origin);
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await command({
      action: "allow-control",
      runId: ids.run,
      generation: 1,
      origins: [origin],
    });
    assert.equal(attached, 1, "explicit re-grant can reuse a live binding");
    assert.equal((await call("act", fill)).text, "STALE_TARGET");
    send({ type: "revoked", grant: { epoch: grant.epoch + 1 } });
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await command({
      action: "allow-control",
      runId: ids.run,
      generation: 1,
      origins: [origin],
    });
    await application.evaluate(() => globalThis.fixture.window.hide());
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await application.evaluate(() => globalThis.fixture.window.show());
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await command({
      action: "allow-control",
      runId: ids.run,
      generation: 1,
      origins: [origin],
    });
    await application.evaluate(() => globalThis.fixture.host.suspend());
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    assert.equal(
      await application.evaluate(
        () =>
          globalThis.fixture.window.contentView.children.filter((v) =>
            v.getVisible(),
          ).length,
      ),
      0,
    );
    await application.evaluate(() => globalThis.fixture.host.resume());
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await command({
      action: "allow-control",
      runId: ids.run,
      generation: 1,
      origins: [origin],
    });
    await application.evaluate(({ webContents }) => {
      webContents
        .getAllWebContents()
        .find((c) => c.getURL().startsWith("http:"))
        .forcefullyCrashRenderer();
    });
    let crashed;
    for (let n = 0; n < 100; n++) {
      crashed = await command({
        action: "layout",
        visible: true,
        bounds: { x: 0, y: 0, width: 900, height: 650 },
      });
      if (crashed.tabs.some((tab) => tab.error === "PAGE_CRASHED")) break;
      await new Promise((resolve) => setTimeout(resolve, 20));
    }
    assert.ok(crashed.tabs.some((tab) => tab.error === "PAGE_CRASHED"));
    assert.equal(
      (await call("tabs", { action: "list" })).text,
      "CONTROL_REVOKED",
    );
    await application.evaluate(async () => {
      await globalThis.fixture.host.dispose();
    });
    const remaining = await application.evaluate(
      ({ webContents }) =>
        webContents
          .getAllWebContents()
          .filter((c) => c.id !== globalThis.fixture.window.webContents.id)
          .length,
    );
    assert.equal(remaining, 0);
    if (process.env.OPENFOX_BROWSER_EVIDENCE_DIR) {
      await fs.mkdir(process.env.OPENFOX_BROWSER_EVIDENCE_DIR, {
        recursive: true,
      });
      await fs.writeFile(
        path.join(
          process.env.OPENFOX_BROWSER_EVIDENCE_DIR,
          "agent-control.png",
        ),
        Buffer.from(screenshot.image.data, "base64"),
      );
      await fs.writeFile(
        path.join(
          process.env.OPENFOX_BROWSER_EVIDENCE_DIR,
          "agent-control-result.json",
        ),
        JSON.stringify(
          {
            bindingClaims: attached,
            readonlyRejected: true,
            wrongRunRejected: true,
            nativeClick: true,
            screenshot: true,
            userTakeover: true,
            regrantStaleRef: true,
            runEndRevoke: true,
            hiddenWindowRevoked: true,
            remaining,
          },
          null,
          2,
        ),
      );
    }
  },
);
