const test = require("node:test"),
  assert = require("node:assert/strict");
const fs = require("node:fs/promises"),
  os = require("node:os"),
  path = require("node:path"),
  http = require("node:http");
const { once } = require("node:events");
const { _electron } = require("@playwright/test");
const ts = require("typescript");

test(
  "Chromium preserves real File bytes and frozen requests across renderer replacement",
  { timeout: 30000 },
  async (t) => {
    const root = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-draft-storage-"),
    );
    t.after(() => fs.rm(root, { recursive: true, force: true }));
    const server = http.createServer((_req, res) => {
      res.setHeader("Content-Type", "text/html");
      res.end("<!doctype html><title>Draft storage test</title>");
    });
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    t.after(
      () =>
        new Promise((resolve) => {
          server.closeAllConnections();
          server.close(resolve);
        }),
    );
    const url = `http://127.0.0.1:${server.address().port}`;
    const source = await fs.readFile(
      path.join(
        __dirname,
        "../../packages/app/src/conversations/workspaceStorage.ts",
      ),
      "utf8",
    );
    const moduleURL =
      "data:text/javascript;base64," +
      Buffer.from(
        ts.transpileModule(source, {
          compilerOptions: {
            target: ts.ScriptTarget.ES2022,
            module: ts.ModuleKind.ESNext,
          },
        }).outputText,
      ).toString("base64");
    const entry = path.join(root, "main.cjs");
    await fs.writeFile(
      entry,
      `const {app,BrowserWindow}=require('electron');app.setPath('userData',${JSON.stringify(root)});app.whenReady().then(async()=>{globalThis.create=async()=>{const w=new BrowserWindow({show:false,webPreferences:{sandbox:true,contextIsolation:true}});await w.loadURL(${JSON.stringify(url)});globalThis.window=w;};await globalThis.create();});`,
    );
    const app = await _electron.launch({
      executablePath: require("../../apps/desktop/node_modules/electron"),
      args: [entry],
    });
    t.after(async () => {
      const timer = setTimeout(() => app.process().kill("SIGKILL"), 3000);
      try {
        await app.close();
      } finally {
        clearTimeout(timer);
      }
    });
    for (let i = 0; i < 100; i++) {
      if (await app.evaluate(() => Boolean(globalThis.window))) break;
      await new Promise((r) => setTimeout(r, 20));
    }
    const execute = (code) =>
      app.evaluate(
        (_e, code) => globalThis.window.webContents.executeJavaScript(code),
        code,
      );
    const load = `const m=await import(${JSON.stringify(moduleURL)});const store=new m.WorkspaceStore(JSON.stringify(['u1','o1']),await m.openWorkspaceDB());await store.hydrate();`;
    await execute(
      `(async()=>{${load} const file=new File(['中文附件\\nbytes'], '草稿.txt', {type:'text/plain',lastModified:1234});store.write('draft:c1',{body:'还没发送',files:[file]});store.write('send:c1',{clientId:'original-key',request:{body:'冻结输入',client_message_id:'original-key'},uploaded:['uploaded-id'],files:[file]});await store.close();})()`,
    );
    await app.evaluate(async () => {
      const old = globalThis.window;
      await globalThis.create();
      old.destroy();
    });
    const result = await execute(
      `(async()=>{${load} const draft=store.read('draft:c1'),pending=store.read('send:c1');const file=draft.files[0];const result={body:draft.body,file:{name:file.name,type:file.type,lastModified:file.lastModified,text:await file.text(),isFile:file instanceof File},pending:{clientId:pending.clientId,request:pending.request,uploaded:pending.uploaded,fileText:await pending.files[0].text()}};await store.close();return result;})()`,
    );
    assert.deepEqual(result, {
      body: "还没发送",
      file: {
        name: "草稿.txt",
        type: "text/plain",
        lastModified: 1234,
        text: "中文附件\nbytes",
        isFile: true,
      },
      pending: {
        clientId: "original-key",
        request: { body: "冻结输入", client_message_id: "original-key" },
        uploaded: ["uploaded-id"],
        fileText: "中文附件\nbytes",
      },
    });
  },
);
