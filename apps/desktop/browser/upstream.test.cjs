const test = require("node:test");
const assert = require("node:assert/strict");
const http = require("node:http");
const net = require("node:net");
const { once } = require("node:events");
const { createUpstreamDial, ipv6Bytes } = require("./upstream.cjs");

test("upstream HTTP CONNECT uses the validated numeric destination and authentication", async (t) => {
  const site = net.createServer((socket) => socket.pipe(socket));
  site.listen(0, "127.0.0.1");
  await once(site, "listening");
  t.after(() => site.close());
  let destination, auth;
  const proxy = http.createServer();
  const sockets = new Set();
  proxy.on("connection", (socket) => {
    sockets.add(socket);
    socket.once("close", () => sockets.delete(socket));
  });
  proxy.on("connect", (req, client) => {
    destination = req.url;
    auth = req.headers["proxy-authorization"];
    const socket = net.connect(site.address().port, "127.0.0.1", () => {
      client.write("HTTP/1.1 200 OK\r\n\r\n");
      client.pipe(socket).pipe(client);
    });
    client.once("close", () => socket.destroy());
  });
  proxy.listen(0, "127.0.0.1");
  await once(proxy, "listening");
  t.after(() => {
    sockets.forEach((socket) => socket.destroy());
    proxy.close();
  });
  const dial = createUpstreamDial({
    resolveProxy: async () => `PROXY 127.0.0.1:${proxy.address().port}; DIRECT`,
    authenticate: () => ({ username: "fixture", password: "test-only" }),
  });
  const socket = await dial(
    {
      url: new URL("https://does-not-resolve.invalid/"),
      host: "127.0.0.1",
      port: site.address().port,
      family: 4,
    },
    (socket) => socket,
  );
  t.after(() => socket.destroy());
  assert.equal(destination, `127.0.0.1:${site.address().port}`);
  assert.equal(
    auth,
    "Basic " + Buffer.from("fixture:test-only").toString("base64"),
  );
  const response = once(socket, "data");
  socket.resume();
  socket.write("pinned destination");
  assert.equal((await response)[0].toString(), "pinned destination");
});

test("a rejected upstream tunnel does not fall through to DIRECT", async (t) => {
  const proxy = http.createServer();
  proxy.on("connect", (_req, socket) =>
    socket.end("HTTP/1.1 403 Forbidden\r\n\r\n"),
  );
  proxy.listen(0, "127.0.0.1");
  await once(proxy, "listening");
  t.after(() => proxy.close());
  const dial = createUpstreamDial({
    resolveProxy: async () => `PROXY 127.0.0.1:${proxy.address().port}; DIRECT`,
  });
  await assert.rejects(
    dial(
      {
        url: new URL("https://fixture.test/"),
        host: "127.0.0.1",
        port: 80,
        family: 4,
      },
      (socket) => socket,
    ),
    { code: "UPSTREAM_PROXY_UNAVAILABLE" },
  );
});

test("SOCKS5 sends a numeric IP and preserves data after its handshake", async (t) => {
  let connectRequest;
  const server = net.createServer((socket) => {
    socket.once("data", (hello) => {
      assert.deepEqual(hello, Buffer.from([5, 1, 0]));
      socket.write(Buffer.from([5, 0]));
      socket.once("data", (request) => {
        connectRequest = request;
        socket.write(Buffer.from([5, 0, 0, 1, 0, 0, 0, 0, 0, 0]));
        socket.pipe(socket);
      });
    });
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => server.close());
  const dial = createUpstreamDial({
    resolveProxy: async () => `SOCKS5 127.0.0.1:${server.address().port}`,
  });
  const socket = await dial(
    {
      url: new URL("https://fixture.test/"),
      host: "8.8.8.8",
      family: 4,
      port: 443,
    },
    (socket) => socket,
  );
  t.after(() => socket.destroy());
  assert.deepEqual(
    connectRequest,
    Buffer.from([5, 1, 0, 1, 8, 8, 8, 8, 1, 187]),
  );
  const response = once(socket, "data");
  socket.resume();
  socket.write("opaque website data");
  assert.equal((await response)[0].toString(), "opaque website data");
  assert.equal(
    ipv6Bytes("::ffff:127.0.0.1").toString("hex"),
    "00000000000000000000ffff7f000001",
  );
  assert.equal(
    ipv6Bytes("2606:4700:4700::1111").toString("hex"),
    "26064700470000000000000000001111",
  );
});
