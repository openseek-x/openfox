const test = require("node:test");
const assert = require("node:assert/strict");
const http = require("node:http");
const net = require("node:net");
const { once } = require("node:events");
const { createBrowserNetwork } = require("./network.cjs");

async function listen(server) {
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return `http://127.0.0.1:${server.address().port}`;
}
function request(proxy, target, auth = true) {
  return new Promise((resolve, reject) => {
    const call = http.get(
      proxy.url,
      {
        path: target,
        headers: auth
          ? {
              "Proxy-Authorization": `Basic ${Buffer.from(`${proxy.username}:${proxy.password}`).toString("base64")}`,
            }
          : {},
      },
      (res) => {
        let body = "";
        res.on("data", (chunk) => (body += chunk));
        res.on("end", () => resolve({ status: res.statusCode, body }));
      },
    );
    call.on("error", reject);
  });
}

test("proxy authenticates, pins DNS, strips its credentials and blocks private access", async (t) => {
  let hits = 0;
  const server = http.createServer((req, res) => {
    hits++;
    res.end(
      JSON.stringify({
        host: req.headers.host,
        auth: req.headers["proxy-authorization"] || null,
      }),
    );
  });
  const origin = await listen(server);
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  const alias = origin.replace("127.0.0.1", "fixture.test");
  let resolutions = 0;
  const proxy = await createBrowserNetwork({
    allowedOrigins: [alias],
    lookup: async () => {
      resolutions++;
      return [{ address: "127.0.0.1", family: 4 }];
    },
  });
  t.after(() => proxy.dispose());
  assert.equal((await request(proxy, alias, false)).status, 407);
  assert.equal(hits, 0);
  const response = await request(proxy, alias + "/test");
  assert.equal(response.status, 200);
  assert.deepEqual(JSON.parse(response.body), {
    host: new URL(alias).host,
    auth: null,
  });
  assert.equal(resolutions, 1);
  assert.equal((await request(proxy, origin)).status, 403);
  assert.equal(hits, 1);
  proxy.policy.allowOrigin(proxy.url);
  assert.equal((await request(proxy, proxy.url)).status, 403);
});

test("CONNECT requires a grant, preserves the tunnel and disposal closes it", async (t) => {
  const server = net.createServer((socket) => socket.pipe(socket));
  const origin = await listen(server);
  t.after(() => server.close());
  const proxy = await createBrowserNetwork({
    allowedOrigins: [origin.replace("http:", "https:")],
  });
  t.after(() => proxy.dispose());
  const socket = net.connect(Number(new URL(proxy.url).port), "127.0.0.1");
  socket.on("error", () => {});
  t.after(() => socket.destroy());
  await once(socket, "connect");
  socket.write(
    `CONNECT ${new URL(origin).host} HTTP/1.1\r\nHost: ${new URL(origin).host}\r\nProxy-Authorization: Basic ${Buffer.from(`${proxy.username}:${proxy.password}`).toString("base64")}\r\n\r\n`,
  );
  assert.match(
    (await once(socket, "data"))[0].toString(),
    /200 Connection Established/,
  );
  socket.write("opaque-tls-placeholder");
  assert.equal(
    (await once(socket, "data"))[0].toString(),
    "opaque-tls-placeholder",
  );
  const closed = once(socket, "close");
  await proxy.dispose();
  await closed;
});

test("a proxy cannot reach a protected endpoint even with an explicit origin grant", async (t) => {
  let hits = 0;
  const server = http.createServer((_req, res) => {
    hits++;
    res.end("protected");
  });
  const origin = await listen(server);
  t.after(() => server.close());
  const proxy = await createBrowserNetwork({
    allowedOrigins: [origin],
    blockedPorts: [server.address().port],
  });
  t.after(() => proxy.dispose());
  assert.equal((await request(proxy, origin)).status, 403);
  assert.equal(hits, 0);
});

test("late DNS failure after a tab closes cannot retain a stale failure record", async (t) => {
  const { EventEmitter } = require("node:events");
  let rejectLookup, gate;
  const network = await createBrowserNetwork({
    lookup: () =>
      new Promise((_resolve, reject) => {
        rejectLookup = reject;
      }),
  });
  t.after(() => network.dispose());
  await network.attach({
    setProxy: async () => {},
    closeAllConnections: async () => {},
    webRequest: {
      onBeforeRequest: (_filter, callback) => {
        gate = callback;
      },
    },
  });
  const contents = Object.assign(new EventEmitter(), {
    id: 123,
    setWebRTCIPHandlingPolicy() {},
  });
  const unbind = network.bind(contents);
  const url = "https://fixture.test/";
  const result = new Promise((resolve) =>
    gate({ url, resourceType: "mainFrame", webContentsId: 123 }, resolve),
  );
  unbind();
  rejectLookup(new Error("DNS failure"));
  assert.deepEqual(await result, { cancel: true });
  assert.equal(network.failureFor(123, url), null);
});

test("retries a second validated address without resolving the hostname again", async (t) => {
  const server = http.createServer((_req, res) => res.end("second address"));
  const origin = await listen(server);
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  const alias = origin.replace("127.0.0.1", "fixture.test");
  let resolutions = 0;
  const proxy = await createBrowserNetwork({
    allowedOrigins: [alias],
    lookup: async () => {
      resolutions++;
      return [
        { address: "127.0.0.2", family: 4 },
        { address: "127.0.0.1", family: 4 },
      ];
    },
  });
  t.after(() => proxy.dispose());
  assert.deepEqual(await request(proxy, alias), {
    status: 200,
    body: "second address",
  });
  assert.equal(resolutions, 1);
});
