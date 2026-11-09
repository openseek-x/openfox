const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const http = require("node:http");
const { once } = require("node:events");
const {
  randomBytes,
  createCipheriv,
  publicEncrypt,
  createPublicKey,
  constants,
} = require("node:crypto");
const { _electron: electron } = require("@playwright/test");
const { binding } = require("./work-directory.cjs");

test(
  "isolated Electron main and preload copy local/remote paths without exposing them",
  { timeout: 60000 },
  async () => {
    const directory = await fs.mkdtemp(
      path.join(os.tmpdir(), "openfox-path-electron-"),
    );
    const identifier = "11111111-1111-4111-8111-111111111111";
    const deviceId = "22222222-2222-4222-8222-222222222222";
    const scope = {
      organization_id: identifier,
      device_id: deviceId,
      space_id: identifier,
      conversation_id: identifier,
      agent_id: identifier,
      run_id: identifier,
      generation: 1,
      user_id: identifier,
    };
    const localPath = path.join(directory, "local-workspace");
    const remotePath = "/isolated-remote/真实工作目录";
    const requests = [];
    let forbidden = false;
    let application;
    const server = http.createServer(async (request, response) => {
      if (request.url === "/") {
        response.setHeader("Content-Type", "text/html");
        response.end(
          "<!doctype html><title>Isolated workspace copy test</title><p>IPC fixture only</p>",
        );
        return;
      }
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const raw = Buffer.concat(chunks).toString("utf8");
      requests.push({
        url: request.url,
        method: request.method,
        raw,
        headers: request.headers,
      });
      response.setHeader("Content-Type", "application/json");
      if (
        forbidden ||
        !request.headers.cookie?.includes(
          "openfox_session=isolated-test-session",
        )
      ) {
        response.writeHead(403);
        response.end(JSON.stringify({ error: { code: "FORBIDDEN" } }));
      } else if (request.url.endsWith("/work-directory")) {
        response.end(
          JSON.stringify({ target: scope, supported: true, online: true }),
        );
      } else if (
        request.method === "POST" &&
        request.headers["x-csrf-token"] === "isolated-test-csrf"
      ) {
        const body = JSON.parse(raw);
        const params = body.params;
        const secret = randomBytes(32);
        const nonce = randomBytes(12);
        const cipher = createCipheriv("aes-256-gcm", secret, nonce);
        cipher.setAAD(binding(params));
        const ciphertext = Buffer.concat([
          cipher.update(remotePath, "utf8"),
          cipher.final(),
          cipher.getAuthTag(),
        ]);
        const wrapped = publicEncrypt(
          {
            key: createPublicKey({
              key: Buffer.from(params.public_key, "base64"),
              type: "spki",
              format: "der",
            }),
            padding: constants.RSA_PKCS1_OAEP_PADDING,
            oaepHash: "sha256",
            oaepLabel: binding(params),
          },
          secret,
        );
        response.end(
          JSON.stringify({
            command: {
              id: identifier,
              device_id: deviceId,
              kind: body.kind,
              params,
              status: "SUCCEEDED",
              result: {
                algorithm: "RSA-OAEP-256/A256GCM",
                wrapped_key: wrapped.toString("base64"),
                nonce: nonce.toString("base64"),
                ciphertext: ciphertext.toString("base64"),
              },
            },
          }),
        );
      } else {
        response.writeHead(400);
        response.end(JSON.stringify({ error: { code: "INVALID_REQUEST" } }));
      }
    });
    try {
      server.listen(0, "127.0.0.1");
      await once(server, "listening");
      const origin = `http://127.0.0.1:${server.address().port}`;
      const fixture = path.join(directory, "cli-state.json");
      const cli = path.join(directory, "openfox-fixture");
      await fs.writeFile(
        fixture,
        JSON.stringify({
          registered: true,
          organization_id: identifier,
          device_id: deviceId,
          center_url: origin,
        }),
      );
      await fs.writeFile(
        cli,
        `#!${process.execPath}\nconst state=JSON.parse(require('node:fs').readFileSync(${JSON.stringify(fixture)},'utf8'));process.stdout.write(JSON.stringify(process.argv[3]==='workspace-path'?{ok:true,path:${JSON.stringify(localPath)}}:state));\n`,
        { mode: 0o700 },
      );
      const entry = path.join(directory, "main.cjs");
      await fs.writeFile(
        entry,
        `const electron=require('electron');electron.app.setPath('userData',${JSON.stringify(path.join(directory, "profile"))});electron.app.setName('OpenFox Workspace Copy Test');globalThis.workspaceWrites=[];electron.clipboard.writeText=value=>globalThis.workspaceWrites.push(value);require(${JSON.stringify(path.join(__dirname, "main.cjs"))});\n`,
      );
      application = await electron.launch({
        executablePath: require("electron"),
        args: [entry],
        env: {
          ...process.env,
          HOME: directory,
          OPENFOX_DESKTOP_WEB_URL: origin,
          OPENFOX_DESKTOP_OPENFOX_BINARY: cli,
        },
      });
      const page = await application.firstWindow();
      await page.waitForURL(origin + "/");
      await page.waitForFunction(() =>
        Boolean(window.openfoxDesktop?.copyWorkDirectory),
      );
      await application.evaluate(async ({ BrowserWindow }, url) => {
        const session = BrowserWindow.getAllWindows()[0].webContents.session;
        for (const [name, value] of [
          ["openfox_session", "isolated-test-session"],
          ["openfox_csrf", "isolated-test-csrf"],
        ]) {
          await session.cookies.set({
            url,
            name,
            value,
            httpOnly: name === "openfox_session",
            sameSite: "lax",
          });
        }
      }, origin);
      const input = {
        orgId: identifier,
        conversationId: identifier,
        requestId: identifier,
      };
      const copy = () =>
        page.evaluate(
          (value) => window.openfoxDesktop.copyWorkDirectory(value),
          input,
        );
      assert.deepEqual(await copy(), { ok: true });
      assert.deepEqual(
        await application.evaluate(() => globalThis.workspaceWrites),
        [localPath],
      );
      assert.equal(
        requests.some((request) => request.method === "POST"),
        false,
      );
      await fs.writeFile(
        fixture,
        JSON.stringify({ registered: false, reason: "not_registered" }),
      );
      assert.deepEqual(await copy(), { ok: true });
      assert.deepEqual(
        await application.evaluate(() => globalThis.workspaceWrites),
        [localPath, remotePath],
      );
      assert.equal(
        requests.filter((request) => request.method === "POST").length,
        1,
      );
      assert.equal(JSON.stringify(requests).includes(remotePath), false);
      assert.equal(JSON.stringify(requests).includes(localPath), false);
      forbidden = true;
      assert.deepEqual(await copy(), { ok: false, reason: "forbidden" });
      assert.deepEqual(
        await application.evaluate(() => globalThis.workspaceWrites),
        [localPath, remotePath],
      );
    } finally {
      await application?.close();
      server.closeAllConnections();
      await new Promise((resolve) => server.close(resolve));
      await fs.rm(directory, { recursive: true, force: true });
    }
  },
);
