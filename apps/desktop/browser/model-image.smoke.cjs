// Explicit live-provider smoke test, excluded from ordinary tests. The fixture
// image contains no user data; it is returned only as a typed tool image.
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { spawn, execFileSync } = require("node:child_process");
const { createInterface } = require("node:readline");
const { createHash } = require("node:crypto");
const assert = require("node:assert/strict");
const http = require("node:http");
const { once } = require("node:events");
const { randomBytes } = require("node:crypto");

async function main() {
  const evidence = process.env.OPENFOX_BROWSER_EVIDENCE_DIR;
  if (!evidence) throw new Error("OPENFOX_BROWSER_EVIDENCE_DIR is required");
  const screenshot = await fs.readFile(path.join(evidence, "browser-page.png"));
  const fixture = JSON.parse(
    await fs.readFile(path.join(evidence, "electron-result.json"), "utf8"),
  );
  const cli = process.env.OPENFOX_CODEX_BIN || "codex";
  const inputMode = process.env.OPENFOX_BROWSER_VERIFY_INPUT || "mcp-image";
  assert.ok(["mcp-image", "dynamic-image", "user-image"].includes(inputMode));
  const inputControl = inputMode === "user-image";
  const mcpMode = inputMode === "mcp-image";
  const directory = await fs.mkdtemp(
    path.join(os.tmpdir(), "openfox-image-protocol-"),
  );
  const pending = new Map();
  let sequence = 0,
    calls = 0,
    finalText = "",
    model;
  let mcp;
  let mcpURL;
  if (mcpMode) {
    const route = "/" + randomBytes(24).toString("hex");
    mcp = http.createServer(async (req, res) => {
      if (req.url !== route || req.headers.origin || req.method !== "POST") {
        res.writeHead(404);
        res.end();
        return;
      }
      let body = "";
      for await (const chunk of req) {
        body += chunk;
        if (body.length > 1024 * 1024) {
          req.destroy();
          return;
        }
      }
      const message = JSON.parse(body);
      process.stdout.write(
        JSON.stringify({ fixtureMcpMethod: message.method }) + "\n",
      );
      if (message.id === undefined) {
        res.writeHead(202);
        res.end();
        return;
      }
      let result;
      if (message.method === "initialize")
        result = {
          protocolVersion: "2025-03-26",
          capabilities: { tools: {} },
          serverInfo: { name: "openfox-browser-fixture", version: "1" },
        };
      else if (message.method === "tools/list")
        result = {
          tools: [
            {
              name: "browser_fixture_screenshot",
              description:
                "Returns the screenshot as an MCP image. Inspect its visual contents.",
              inputSchema: { type: "object", properties: {} },
              annotations: {
                readOnlyHint: true,
                destructiveHint: false,
                openWorldHint: false,
              },
            },
          ],
        };
      else if (
        message.method === "tools/call" &&
        message.params?.name === "browser_fixture_screenshot"
      ) {
        calls++;
        result = {
          content: [
            {
              type: "text",
              text: "Screenshot captured. Inspect the attached image.",
            },
            {
              type: "image",
              data: screenshot.toString("base64"),
              mimeType: "image/png",
            },
          ],
          isError: false,
        };
        process.stdout.write("MCP image tool result delivered.\n");
      } else result = {};
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ jsonrpc: "2.0", id: message.id, result }));
    });
    mcp.listen(0, "127.0.0.1");
    await once(mcp, "listening");
    mcpURL = `http://127.0.0.1:${mcp.address().port}${route}`;
  }
  const child = spawn(
    cli,
    [
      ...(mcpMode
        ? [
            "-c",
            `mcp_servers.openfox_browser_fixture.url=${JSON.stringify(mcpURL)}`,
          ]
        : []),
      "app-server",
    ],
    {
      cwd: directory,
      env: {
        ...process.env,
        NO_PROXY: [process.env.NO_PROXY, "127.0.0.1,localhost,::1"]
          .filter(Boolean)
          .join(","),
        no_proxy: [process.env.no_proxy, "127.0.0.1,localhost,::1"]
          .filter(Boolean)
          .join(","),
      },
      stdio: ["pipe", "pipe", "pipe"],
    },
  );
  child.stderr.resume(); // Provider/config diagnostics may contain sensitive data.
  let complete, fail;
  const completed = new Promise((resolve, reject) => {
    complete = resolve;
    fail = reject;
  });
  // Attach a rejection observer before the handshake completes.
  void completed.catch(() => {});
  const send = (message) => child.stdin.write(JSON.stringify(message) + "\n");
  const request = (method, params) =>
    new Promise((resolve, reject) => {
      const id = ++sequence;
      pending.set(id, { resolve, reject });
      send({ id, method, params });
    });
  const failAll = (error) => {
    for (const value of pending.values()) value.reject(error);
    pending.clear();
    fail(error);
  };
  const lines = createInterface({ input: child.stdout });
  lines.on("line", (line) => {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      return;
    }
    if (message.id !== undefined && !message.method) {
      const waiter = pending.get(message.id);
      if (waiter) {
        pending.delete(message.id);
        message.error
          ? waiter.reject(
              new Error(
                `RPC failed (${message.error.code}): ${String(message.error.message).slice(0, 300)}`,
              ),
            )
          : waiter.resolve(message.result);
      }
      return;
    }
    if (message.method === "item/tool/call") {
      if (
        message.params.tool !== "browser_fixture_screenshot" ||
        ++calls !== 1
      ) {
        send({
          id: message.id,
          result: {
            success: false,
            contentItems: [
              {
                type: "inputText",
                text: "Only the single screenshot fixture call is supported.",
              },
            ],
          },
        });
        return;
      }
      send({
        id: message.id,
        result: {
          success: true,
          contentItems: [
            {
              type: "inputText",
              text: "Screenshot captured. Inspect the attached image.",
            },
            {
              type: "inputImage",
              imageUrl:
                "data:image/png;base64," + screenshot.toString("base64"),
            },
          ],
        },
      });
      process.stdout.write("Typed image tool result delivered.\n");
    } else if (message.id !== undefined) {
      send({
        id: message.id,
        error: {
          code: -32601,
          message: "Unsupported in isolated verification",
        },
      });
    } else if (
      message.method === "item/completed" &&
      message.params?.item?.type === "agentMessage"
    ) {
      finalText = message.params.item.text || finalText;
    } else if (message.method === "turn/completed") {
      message.params.turn.status === "completed"
        ? complete()
        : fail(
            new Error(
              "Model turn did not complete: " + message.params.turn.status,
            ),
          );
    }
  });
  child.once("error", failAll);
  child.once("exit", () =>
    failAll(new Error("App server exited before verification finished")),
  );
  const timer = setTimeout(() => {
    failAll(new Error("Model verification timeout (120s)"));
    child.kill();
  }, 120000);
  const started = Date.now();
  try {
    await request("initialize", {
      clientInfo: {
        name: "openfox_browser_verification",
        title: "OpenFox browser verification",
        version: "1",
      },
      capabilities: { experimentalApi: true, requestAttestation: false },
    });
    send({ method: "initialized" });
    const thread = await request("thread/start", {
      cwd: directory,
      approvalPolicy: "never",
      sandbox: "read-only",
      ephemeral: true,
      ...(process.env.OPENFOX_BROWSER_VERIFY_MODEL
        ? { model: process.env.OPENFOX_BROWSER_VERIFY_MODEL }
        : {}),
      baseInstructions: inputControl
        ? "Describe the attached image. Do not call tools, read files, access the network or delegate work."
        : mcpMode
          ? "You verify a screenshot. Discover and invoke the MCP tool browser_fixture_screenshot from openfox_browser_fixture, exactly once. Tool discovery is allowed. Do not invoke unrelated tools, read files, access the network or delegate work. Answer only from the image returned by the screenshot tool."
          : "You verify a screenshot from a fixture. Use only browser_fixture_screenshot, exactly once. Do not invoke other tools, read files, access the network or delegate work. Answer only from the image returned by that tool.",
      dynamicTools: mcpMode
        ? []
        : [
            {
              type: "function",
              name: "browser_fixture_screenshot",
              description:
                "Returns the only screenshot to inspect as a native image tool result.",
              inputSchema: {
                type: "object",
                properties: {},
                additionalProperties: false,
              },
            },
          ],
    });
    model = thread.model;
    process.stdout.write(
      JSON.stringify({ model, stage: "thread-started" }) + "\n",
    );
    if (mcpMode) {
      const inventory = await request("mcpServerStatus/list", {
        threadId: thread.thread.id,
        serverName: "openfox_browser_fixture",
      });
      process.stdout.write(
        JSON.stringify({
          fixtureInventory: inventory.data.map(
            ({ name, tools, authStatus }) => ({
              name,
              tools: Object.keys(tools || {}),
              authStatus,
            }),
          ),
        }) + "\n",
      );
    }
    await request("turn/start", {
      threadId: thread.thread.id,
      effort: "low",
      input: [
        {
          type: "text",
          text:
            (inputControl
              ? "Inspect the attached image. "
              : "Call browser_fixture_screenshot. ") +
            "Read the visual code and the left and right colored shapes. Return JSON with visual_code, left and right; describe shapes and colors in English. Do not infer missing information.",
          text_elements: [],
        },
        ...(inputControl
          ? [
              {
                type: "image",
                url: "data:image/png;base64," + screenshot.toString("base64"),
              },
            ]
          : []),
      ],
      outputSchema: {
        type: "object",
        properties: {
          visual_code: { type: "string" },
          left: { type: "string" },
          right: { type: "string" },
        },
        required: ["visual_code", "left", "right"],
        additionalProperties: false,
      },
    });
    await completed;
    const result = JSON.parse(finalText);
    assert.equal(calls, inputControl ? 0 : 1);
    assert.equal(result.visual_code.toUpperCase(), fixture.visualCode);
    assert.match(result.left.toLowerCase(), /red/);
    assert.match(result.left.toLowerCase(), /circle/);
    assert.match(result.right.toLowerCase(), /blue/);
    assert.match(result.right.toLowerCase(), /square/);
    const record = {
      verifiedAt: new Date().toISOString(),
      cliVersion: execFileSync(cli, ["--version"], { encoding: "utf8" }).trim(),
      model,
      inputMode,
      elapsedMs: Date.now() - started,
      transport: inputControl
        ? "Control: user image (does not satisfy tool image acceptance)"
        : mcpMode
          ? "Codex app-server MCP native image (protocol POC; production bridge remains stage 3)"
          : "Codex app-server dynamicTools inputImage (protocol POC; production Go bridge remains stage 3)",
      screenshotSHA256: createHash("sha256").update(screenshot).digest("hex"),
      toolCalls: calls,
      imageOnlyExpectedValues: true,
      response: result,
      passed: true,
    };
    await fs.writeFile(
      path.join(
        evidence,
        inputControl ? "model-input-control.json" : "model-image-result.json",
      ),
      JSON.stringify(record, null, 2) + "\n",
    );
    process.stdout.write(
      JSON.stringify({
        passed: true,
        model,
        elapsedMs: record.elapsedMs,
        response: result,
      }) + "\n",
    );
  } catch (error) {
    await fs.writeFile(
      path.join(evidence, `model-${inputMode}-failure.json`),
      JSON.stringify(
        {
          verifiedAt: new Date().toISOString(),
          model,
          inputMode,
          toolCalls: calls,
          response: finalText,
          error: error.message,
          screenshotSHA256: createHash("sha256")
            .update(screenshot)
            .digest("hex"),
          passed: false,
        },
        null,
        2,
      ) + "\n",
    );
    throw error;
  } finally {
    clearTimeout(timer);
    lines.close();
    child.kill();
    if (mcp) {
      mcp.closeAllConnections();
      mcp.close();
    }
    await fs.rm(directory, { recursive: true, force: true });
  }
}
main().catch((error) => {
  process.stderr.write(error.message + "\n");
  process.exitCode = 1;
});
