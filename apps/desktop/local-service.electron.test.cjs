const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const { once } = require("node:events");
const { _electron: electron } = require("@playwright/test");

test(
  "isolated real Electron main/preload enforce scoped local service IPC",
  { timeout: 60000 },
  async () => {
    const directory = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-service-electron-"),
    );
    const orgId = "11111111-1111-4111-8111-111111111111";
    const deviceId = "22222222-2222-4222-8222-222222222222";
    let application,
      origin,
      owner = true;
    const requests = [];
    const server = http.createServer((request, response) => {
      if (request.url === "/") {
        response.setHeader("Content-Type", "text/html");
        response.end(
          "<!doctype html><title>Isolated local service test</title><p>IPC fixture only</p>",
        );
        return;
      }
      requests.push(request.url);
      response.setHeader("Content-Type", "application/json");
      if (
        !request.headers.cookie?.includes(
          "openfox_session=isolated-service-test",
        )
      ) {
        response.writeHead(403);
        response.end("{}");
        return;
      }
      response.end(
        JSON.stringify(
          request.url.endsWith("/center")
            ? { center_url: origin }
            : request.url === "/api/v1/me"
              ? { id: "owner" }
              : { id: deviceId, owner_user_id: owner ? "owner" : "different" },
        ),
      );
    });
    try {
      server.listen(0, "127.0.0.1");
      await once(server, "listening");
      origin = `http://127.0.0.1:${server.address().port}`;
      const fixture = path.join(directory, "cli-state.json");
      const operations = path.join(directory, "operations.jsonl");
      const selectedParent = path.join(directory, "new location");
      const cli = path.join(directory, "openfox-fixture");
      await fs.writeFile(
        fixture,
        JSON.stringify({ startup: "login", fail: false }),
      );
      // This fixture replaces only the CLI process; main/preload, IPC and HTTP are real.
      await fs.writeFile(
        cli,
        `#!${process.execPath}
const fs=require('node:fs');
const args=process.argv.slice(2), state=JSON.parse(fs.readFileSync(${JSON.stringify(fixture)},'utf8'));
let result={};
if(args[0]==='computer')result={registered:true,organization_id:${JSON.stringify(orgId)},device_id:${JSON.stringify(deviceId)},center_url:${JSON.stringify(origin)}};
else if(args[0]==='runtime')result={runtime:'codex',runnable:true};
else if(args[1]==='status')result={state:'RUNNING'};
else {
 fs.appendFileSync(${JSON.stringify(operations)},JSON.stringify(args)+'\\n');
 if(state.fail){process.stderr.write('PRIVATE-TOKEN-do-not-expose');process.exit(1)}
 if(args[1]==='move-storage'){state.storage_path=require('node:path').join(args[args.indexOf('--parent')+1],'OpenFox');fs.writeFileSync(${JSON.stringify(fixture)},JSON.stringify(state));}
 if(args[1]==='startup'){state.startup=args[args.indexOf('--mode')+1];fs.writeFileSync(${JSON.stringify(fixture)},JSON.stringify(state));}
 result=args[1]==='logs'?{lines:['2026/11/13 12:00:00 INFO daemon_token_renewed [details redacted]'],next_cursor:null,reset:false}:{supported:true,installed:true,startup:state.startup,keep_alive:true,storage_path:state.storage_path};
}
process.stdout.write(JSON.stringify(result));
`,
        { mode: 0o700 },
      );
      const entry = path.join(directory, "main.cjs");
      await fs.writeFile(
        entry,
        `const electron=require('electron');require('node:os').homedir=()=>${JSON.stringify(directory)};electron.app.setPath('userData',${JSON.stringify(path.join(directory, "profile"))});electron.app.setName('OpenFox Local Service Test');require(${JSON.stringify(path.join(__dirname, "main.cjs"))});`,
      );
      application = await electron.launch({
        executablePath: require("electron"),
        args: [entry],
        env: {
          ...process.env,
          OPENFOX_DESKTOP_WEB_URL: origin,
          OPENFOX_DESKTOP_OPENFOX_BINARY: cli,
        },
      });
      const page = await application.firstWindow();
      await page.waitForURL(origin + "/");
      await page.waitForFunction(() =>
        Boolean(window.openfoxDesktop?.getLocalService),
      );
      await application.evaluate(async ({ BrowserWindow }, url) => {
        await BrowserWindow.getAllWindows()[0].webContents.session.cookies.set({
          url,
          name: "openfox_session",
          value: "isolated-service-test",
          httpOnly: true,
          sameSite: "lax",
        });
      }, origin);
      const target = { orgId, deviceId, centerUrl: origin };
      const invoke = (method, input = target) =>
        page.evaluate(
          async ({ method, input }) => {
            try {
              return { result: await window.openfoxDesktop[method](input) };
            } catch (error) {
              return { error: String(error) };
            }
          },
          { method, input },
        );
      assert.equal((await invoke("getLocalService")).result.startup, "login");
      assert.equal(
        (await invoke("setLocalStartup", { ...target, mode: "manual" })).result
          .startup,
        "manual",
      );
      assert.equal((await invoke("getLocalService")).result.startup, "manual");
      assert.match(
        (await invoke("readLocalLogs")).result.lines[0],
        /details redacted/,
      );
      assert.equal(
        (await invoke("setLocalKeepRunning", { ...target, enabled: false }))
          .result.keep_service_running,
        false,
      );
      assert.equal(
        (await invoke("getLocalService")).result.keep_service_running,
        false,
      );
      assert.ok(
        (await invoke("setLocalAwake", { ...target, minutes: 60 })).result
          .awake_until > Date.now(),
      );
      assert.equal(
        (await invoke("setLocalAwake", { ...target, minutes: 0 })).result
          .awake_until,
        0,
      );
      await application.evaluate(({ dialog }, selectedParent) => {
        dialog.showOpenDialog = async () => ({
          canceled: false,
          filePaths: [selectedParent],
        });
        dialog.showMessageBox = async () => ({ response: 0 });
      }, selectedParent);
      const moved = await invoke("moveLocalStorage", {
        ...target,
        parent: "/renderer-value",
      });
      assert.equal(
        moved.result.storage_path,
        path.join(selectedParent, "OpenFox"),
      );
      assert.equal(
        (await invoke("getLocalService")).result.storage_path,
        moved.result.storage_path,
      );
      const count = (await fs.readFile(operations, "utf8"))
        .trim()
        .split("\n").length;
      assert.match(
        (await invoke("getLocalService", { ...target, deviceId: orgId })).error,
        /local service request failed/,
      );
      owner = false;
      assert.match(
        (await invoke("setLocalStartup", { ...target, mode: "login" })).error,
        /local service request failed/,
      );
      assert.equal(
        (await fs.readFile(operations, "utf8")).trim().split("\n").length,
        count,
      );
      owner = true;
      await fs.writeFile(
        fixture,
        JSON.stringify({ startup: "manual", fail: true }),
      );
      const failed = await invoke("setLocalStartup", {
        ...target,
        mode: "login",
      });
      assert.match(failed.error, /local service request failed/);
      assert.equal(failed.error.includes("PRIVATE-TOKEN"), false);
      assert.equal(
        JSON.parse(await fs.readFile(fixture, "utf8")).startup,
        "manual",
      );
      assert.ok(requests.includes("/api/v1/me"));
    } finally {
      if (application) {
        await Promise.all([
          application.waitForEvent("close"),
          application.evaluate(({ app }) => {
            setImmediate(() => app.exit(0));
          }),
        ]);
      }
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
      await fs.rm(directory, { recursive: true, force: true });
    }
  },
);
