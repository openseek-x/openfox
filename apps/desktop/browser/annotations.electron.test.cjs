const test = require("node:test"),
  assert = require("node:assert/strict");
const fs = require("node:fs/promises"),
  os = require("node:os"),
  path = require("node:path"),
  http = require("node:http");
const { once } = require("node:events");
const { _electron } = require("@playwright/test");
test(
  "native annotation selection preserves layout, intercepts clicks, captures regions and rejects stale refs",
  { timeout: 45000 },
  async (t) => {
    const root = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-annotations-"),
    );
    t.after(() => fs.rm(root, { recursive: true, force: true }));
    let origin;
    const server = http.createServer((req, res) => {
      res.setHeader("Content-Type", "text/html; charset=utf-8");
      res.end(
        req.url === "/frame"
          ? '<style>body{margin:0}</style><button id="child">Child selection</button>'
          : `<!doctype html><title>Annotations</title><style>body{margin:0;background:#fff}button{position:absolute;left:40px;top:40px;width:200px;height:40px}p{position:absolute;left:40px;top:110px;font:20px monospace;margin:0}input{position:absolute;top:200px;left:40px}iframe{position:absolute;left:40px;top:300px;width:400px;height:100px;border:0}</style><button id="target" onclick="window.clicks++">Select this button</button><p id="text">Hello annotation world</p><input type="password" value="SECRET"><iframe src="${origin?.replace("127.0.0.1", "localhost")}/frame"></iframe><script>window.clicks=0</script>`,
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
    // Keep native OS cursor events out of the deterministic input checks.
    // Only show the fixture when explicitly capturing visual evidence.
    const entry = path.join(root, "main.cjs");
    await fs.writeFile(
      entry,
      `
    const {app,BrowserWindow,WebContentsView}=require('electron');const {createAnnotations}=require(${JSON.stringify(path.join(__dirname, "annotations.cjs"))});
    app.setPath('userData',${JSON.stringify(root)});app.setName('OpenFox Annotation Test');app.whenReady().then(async()=>{
      const window=new BrowserWindow({width:900,height:700,show:${Boolean(process.env.OPENFOX_BROWSER_EVIDENCE_DIR)},webPreferences:{sandbox:true}});await window.loadURL('data:text/html,<title>OpenFox Annotation Test</title>');
      const view=new WebContentsView({webPreferences:{sandbox:true,contextIsolation:true}});window.contentView.addChildView(view);view.setBounds({x:0,y:0,width:850,height:600});await view.webContents.loadURL(${JSON.stringify(origin)});
      const tab={id:'11111111-1111-4111-8111-111111111111',title:'Annotations',surface:{view,contents:view.webContents}};
      const annotations=createAnnotations({window,active:()=>tab,changed:()=>{view.setVisible(!annotations.frozen());annotations.layout({x:0,y:0,width:850,height:600},true)}});
      globalThis.fixture={window,view,tab,annotations};
    });
  `,
    );
    const app = await _electron.launch({
      executablePath: require("electron"),
      args: [entry],
    });
    t.after(async () => {
      const timer = setTimeout(() => app.process().kill("SIGKILL"), 5000);
      try {
        await app.close();
      } finally {
        clearTimeout(timer);
      }
    });
    for (let n = 0; n < 100; n++) {
      if (await app.evaluate(() => Boolean(globalThis.fixture))) break;
      await new Promise((r) => setTimeout(r, 20));
    }
    const command = (input) =>
      app.evaluate(async (_e, input) => {
        await globalThis.fixture.annotations.command(input);
        return globalThis.fixture.annotations.state();
      }, input);
    const state = () =>
      app.evaluate(() => globalThis.fixture.annotations.state());
    const page = (expression) =>
      app.evaluate(
        (_e, expression) =>
          globalThis.fixture.view.webContents.executeJavaScript(expression),
        expression,
      );
    const mouse = (input) =>
      app.evaluate((_e, input) => {
        const f = globalThis.fixture;
        f.window.contentView.children
          .find((v) => v !== f.view && v.webContents !== f.window.webContents)
          .webContents.sendInputEvent(input);
      }, input);
    const highlight = () =>
      app.evaluate(async () => {
        const f = globalThis.fixture;
        const c = f.window.contentView.children.find(
          (v) => v !== f.view && v.webContents !== f.window.webContents,
        ).webContents.debugger;
        if (!c.isAttached()) {
          c.attach("1.3");
          await c.sendCommand("DOM.enable");
          await c.sendCommand("CSS.enable");
        }
        const { root } = await c.sendCommand("DOM.getDocument");
        const { nodeId } = await c.sendCommand("DOM.querySelector", {
          nodeId: root.nodeId,
          selector: "#highlight",
        });
        if (!nodeId) return null;
        const { computedStyle } = await c.sendCommand(
          "CSS.getComputedStyleForNode",
          { nodeId },
        );
        const s = Object.fromEntries(
          computedStyle.map(({ name, value }) => [name, value]),
        );
        return s.display === "none"
          ? null
          : {
              x: parseFloat(s.left),
              y: parseFloat(s.top),
              width: parseFloat(s.width),
              height: parseFloat(s.height),
            };
      });
    const waitFor = async (check, label) => {
      for (let n = 0; n < 150; n++) {
        if (await check()) return;
        await new Promise((resolve) => setTimeout(resolve, 20));
      }
      throw Error(label);
    };
    await command({ action: "annotate", enabled: true, mode: "element" });
    await app.evaluate(async () => {
      const f = globalThis.fixture;
      const c = f.window.contentView.children.find(
        (v) => v !== f.view && v.webContents !== f.window.webContents,
      ).webContents;
      while (c.isLoading()) await new Promise((r) => setTimeout(r, 10));
    });
    await mouse({ type: "mouseMove", x: 80, y: 60 });
    await waitFor(
      async () => (await highlight())?.y === 40,
      "hover should show the button without clicking",
    );
    assert.equal((await state()).selection, null);
    const evidence = process.env.OPENFOX_BROWSER_EVIDENCE_DIR;
    const capture = async (name) => {
      if (!evidence) return;
      await fs.mkdir(evidence, { recursive: true });
      const png = await app.evaluate(async ({ desktopCapturer }) => {
        const f = globalThis.fixture;
        const sources = await desktopCapturer.getSources({
          types: ["window"],
          thumbnailSize: { width: 1800, height: 1400 },
        });
        return sources
          .find((source) => source.id === f.window.getMediaSourceId())
          .thumbnail.toPNG()
          .toString("base64");
      });
      await fs.writeFile(path.join(evidence, name), Buffer.from(png, "base64"));
    };
    await capture("hover-button.png");

    // Hold one hit test while the mouse moves again. The last position must win
    // without requiring an additional movement after the old read completes.
    await app.evaluate(() => {
      const c = globalThis.fixture.view.webContents.debugger;
      const send = c.sendCommand.bind(c);
      globalThis.restoreSend = () => {
        c.sendCommand = send;
      };
      globalThis.readStarted = false;
      let delayed = false;
      c.sendCommand = async (...args) => {
        if (args[0] === "DOM.getNodeForLocation" && !delayed) {
          delayed = true;
          globalThis.readStarted = true;
          await new Promise((resolve) => {
            globalThis.releaseRead = resolve;
          });
        }
        return send(...args);
      };
    });
    await mouse({ type: "mouseMove", x: 90, y: 60 });
    await waitFor(
      () => app.evaluate(() => globalThis.readStarted),
      "delayed hover read should start",
    );
    await mouse({ type: "mouseMove", x: 80, y: 120 });
    await app.evaluate(() => globalThis.releaseRead());
    await waitFor(
      async () => (await highlight())?.y === 110,
      "latest mouse position should replace the delayed hover",
    );
    await app.evaluate(() => globalThis.restoreSend());
    await capture("hover-paragraph.png");
    await mouse({ type: "mouseLeave", x: -1, y: 120 });
    await waitFor(
      async () => (await highlight()) === null,
      "leaving the page should clear hover",
    );
    await mouse({ type: "mouseMove", x: 80, y: 60 });
    await waitFor(
      async () => (await highlight())?.y === 40,
      "hover resumes on re-entry",
    );
    await command({ action: "annotate", enabled: false, mode: "element" });
    await command({ action: "annotate", enabled: true, mode: "element" });
    await waitFor(
      async () => (await highlight()) === null,
      "re-enabling must not restore an old hover",
    );
    // Scrolling the document changes viewport coordinates; the next move uses
    // the new element rect rather than a cached rectangle.
    await page("document.body.style.minHeight='1400px'");
    await mouse({
      type: "mouseWheel",
      x: 80,
      y: 45,
      deltaY: -30,
      hasPreciseScrollingDeltas: true,
    });
    await waitFor(
      async () => (await page("scrollY")) > 0,
      "annotation overlay forwards native wheel scrolling",
    );
    await waitFor(
      async () =>
        Math.abs((await highlight())?.y - (40 - (await page("scrollY")))) < 1,
      "outline refreshes after wheel without mouse movement",
    );
    // Native hit testing targets nested scrollers, without moving the document.
    await page(
      "scrollTo(0,0);const scroller=document.createElement('div');scroller.id='nested';scroller.style='position:absolute;left:500px;top:40px;width:200px;height:100px;overflow:auto';scroller.innerHTML='<div style=height:500px>Nested</div>';document.body.append(scroller)",
    );
    await page(
      "new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))",
    );
    await new Promise((resolve) => setTimeout(resolve, 350));
    await mouse({
      type: "mouseWheel",
      x: 550,
      y: 80,
      deltaY: -70,
      hasPreciseScrollingDeltas: true,
    });
    await waitFor(
      async () =>
        (await page("document.querySelector('#nested').scrollTop")) > 0,
      "nested scroll container receives wheel",
    );
    assert.equal(await page("scrollY"), 0);
    await page("document.querySelector('#nested').remove();scrollTo(0,30)");
    await mouse({ type: "mouseMove", x: 80, y: 30 });
    await waitFor(
      async () => (await highlight())?.y === 10,
      "hover follows scrolled geometry",
    );
    await page("scrollTo(0,0);document.body.style.minHeight=''");
    await command({ action: "annotate", enabled: false, mode: "element" });
    const baseline = await page(
      "JSON.stringify(document.querySelector('#target').getBoundingClientRect())",
    );
    assert.equal((await state()).enabled, false);
    const pick = async (mode, a, b = a) => {
      await command({ action: "annotation-cancel" });
      await command({ action: "annotate", enabled: true, mode });
      await app.evaluate(
        async (_e, { a, b }) => {
          const f = globalThis.fixture;
          const overlay = f.window.contentView.children.find(
            (v) => v !== f.view,
          );
          for (let i = 0; i < 100 && overlay.webContents.isLoading(); i++)
            await new Promise((r) => setTimeout(r, 10));
          overlay.webContents.sendInputEvent({
            type: "mouseDown",
            button: "left",
            clickCount: 1,
            ...a,
          });
          overlay.webContents.sendInputEvent({ type: "mouseMove", ...b });
          overlay.webContents.sendInputEvent({
            type: "mouseUp",
            button: "left",
            clickCount: 1,
            ...b,
          });
        },
        { a, b },
      );
      for (let n = 0; n < 100; n++) {
        const value = await state();
        if (value.selection || value.error) return value;
        await new Promise((r) => setTimeout(r, 20));
      }
      throw new Error("selection timeout");
    };
    let selected = await pick("element", { x: 80, y: 60 });
    assert.equal(selected.error, null);
    assert.equal(
      selected.selection.anchor.snapshot.quote,
      "Select this button",
    );
    await mouse({ type: "mouseMove", x: 80, y: 120 });
    assert.deepEqual(
      await highlight(),
      selected.selection.anchor.visual.rect,
      "click locks the chosen element",
    );
    const live = selected.selection.anchor.live;
    assert.equal(await page("window.clicks"), 0);
    assert.equal(
      await page(
        "JSON.stringify(document.querySelector('#target').getBoundingClientRect())",
      ),
      baseline,
    );
    assert.ok(selected.selection.image.startsWith("data:image/"));
    assert.equal(
      await app.evaluate(() => globalThis.fixture.view.getVisible()),
      true,
    );
    await command({
      action: "annotation-ready",
      selectionId: selected.selection.id,
    });
    assert.equal(
      await app.evaluate(() => globalThis.fixture.view.getVisible()),
      false,
    );
    await command({ action: "annotation-cancel" });
    assert.equal(
      await app.evaluate(() => globalThis.fixture.view.getVisible()),
      true,
    );
    selected = await pick("text", { x: 41, y: 120 }, { x: 165, y: 120 });
    assert.equal(selected.error, null);
    assert.match(selected.selection.anchor.snapshot.quote, /Hello/);
    selected = await pick("region", { x: 20, y: 20 }, { x: 280, y: 170 });
    assert.equal(selected.error, null);
    assert.ok(
      selected.selection.screenshot.startsWith("data:image/png;base64,"),
    );
    const regionLive = selected.selection.anchor.live;
    await command({ action: "annotation-cancel" });
    await command({ action: "annotation-locate", live: regionLive });
    assert.equal((await state()).error, null);
    await page("document.querySelector('#text').style.color='red'");
    await command({ action: "annotation-locate", live: regionLive });
    assert.equal((await state()).error, "stale");
    await page("document.querySelector('#text').style.color='black'");
    selected = await pick("element", { x: 60, y: 210 });
    assert.ok(!selected.selection);
    assert.equal(selected.error, "selection");
    selected = await pick("region", { x: 20, y: 190 }, { x: 280, y: 240 });
    assert.ok(!selected.selection);
    selected = await pick("element", { x: 80, y: 310 });
    assert.equal(selected.error, null);
    assert.equal(selected.selection.anchor.snapshot.quote, "Child selection");
    await command({ action: "annotation-cancel" });
    const childLive = selected.selection.anchor.live;
    await page(
      "new Promise(resolve=>{const frame=document.querySelector('iframe');frame.onload=()=>resolve(true);frame.src=frame.src;})",
    );
    await command({ action: "annotation-locate", live: childLive });
    assert.equal(
      (await state()).error,
      "stale",
      "reloaded child frame must invalidate its old reference",
    );
    selected = await pick("element", { x: 80, y: 310 });
    assert.equal(selected.error, null);
    assert.notEqual(selected.selection.anchor.live.ref, childLive.ref);
    const rebound = selected.selection.anchor.live;
    await command({ action: "annotation-cancel" });
    await command({ action: "annotation-locate", live: rebound });
    assert.equal(
      (await state()).error,
      null,
      "explicit new child selection is locatable",
    );
    for (const generation of [undefined, 0, live.document_generation + 1]) {
      await command({
        action: "annotation-locate",
        live: { ...live, document_generation: generation },
      });
      assert.equal(
        (await state()).error,
        "stale",
        "missing/stale page epochs cannot revive live targets",
      );
    }
    await command({ action: "annotation-locate", live });
    assert.equal((await state()).error, null);
    await page("document.querySelector('#target').textContent='Changed'");
    await command({ action: "annotation-locate", live });
    assert.equal((await state()).error, "stale");
    await app.evaluate(async () => {
      await globalThis.fixture.annotations.close();
    });
    assert.equal(
      await app.evaluate(() =>
        globalThis.fixture.view.webContents.debugger.isAttached(),
      ),
      false,
    );
  },
);
