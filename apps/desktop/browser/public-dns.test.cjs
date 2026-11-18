const test = require("node:test");
const assert = require("node:assert/strict");
const net = require("node:net");
const https = require("node:https");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { execFileSync } = require("node:child_process");
const { once } = require("node:events");
const {
  createPublicDNS,
  decodeAnswers,
  queryDNS,
} = require("./public-dns.cjs");
const { createNetworkPolicy } = require("./policy.cjs");

const answer = (
  name,
  type,
  address = type === 1 ? "8.8.8.8" : "2606:4700:4700::1111",
  TTL = 60,
) => ({
  Status: 0,
  TC: false,
  Question: [{ name, type }],
  Answer: [{ name, type, TTL, data: address }],
});

test("Fake-IP names resolve to pinned public IPs; literals and mixed private answers never use the fallback", async () => {
  let calls = 0;
  let addresses = ["198.18.0.104", "2001:2::58"];
  const policy = createNetworkPolicy({
    allowedOrigins: ["https://fixture.test", "https://198.18.0.104"],
    lookup: async () =>
      addresses.map((address) => ({ address, family: net.isIP(address) })),
    resolvePublic: async () => {
      calls++;
      return [{ address: "8.8.8.8", family: 4 }];
    },
  });
  assert.equal(
    (await policy.resolve("https://fixture.test"))[0].host,
    "8.8.8.8",
  );
  assert.equal(calls, 1);
  await assert.rejects(policy.resolve("https://198.18.0.104"), {
    code: "NETWORK_DENIED",
  });
  addresses = ["198.18.0.104", "127.0.0.1"];
  await assert.rejects(policy.resolve("https://fixture.test"), {
    code: "NETWORK_DENIED",
  });
  addresses = ["198.18.0.104", "169.254.169.254"];
  await assert.rejects(policy.resolve("https://fixture.test"), {
    code: "NETWORK_DENIED",
  });
  addresses = ["8.8.4.4"];
  assert.equal(
    (await policy.resolve("https://fixture.test"))[0].host,
    "8.8.4.4",
  );
  assert.equal(calls, 1);
});

test("public DNS validates both families, CNAME results, malformed responses and unsafe destinations", async () => {
  for (const address of [
    "127.0.0.1",
    "169.254.169.254",
    "198.18.0.1",
    "::ffff:127.0.0.1",
    "2001:2::58",
  ]) {
    const type = net.isIP(address) === 4 ? 1 : 28;
    const data = answer("fixture.test", type, address);
    data.Answer.unshift({
      name: "fixture.test",
      type: 5,
      TTL: 30,
      data: "alias.test",
    });
    assert.throws(() => decodeAnswers("fixture.test", type, data), {
      code: "NETWORK_DENIED",
    });
  }
  for (const data of [
    { ...answer("fixture.test", 1), Status: 3 },
    { ...answer("fixture.test", 1), TC: true },
    answer("another.test", 1),
    answer("fixture.test", 28),
    answer("fixture.test", 1, "not-an-ip"),
    answer("fixture.test", 1, "8.8.8.8", -1),
    { ...answer("fixture.test", 1), Answer: {} },
  ])
    assert.throws(() => decodeAnswers("fixture.test", 1, data), {
      code: "PUBLIC_DNS_FAILED",
    });
  const resolver = createPublicDNS({
    query: async (name, type) =>
      answer(name, type, type === 28 ? "::1" : "8.8.8.8"),
  });
  await assert.rejects(resolver.resolve("fixture.test"), {
    code: "NETWORK_DENIED",
  });
});

test("public resolution deduplicates requests and respects TTL including uncached zero TTL", async () => {
  let calls = 0,
    ttl = 60;
  const resolver = createPublicDNS({
    query: async (name, type) => {
      calls++;
      return answer(name, type, undefined, ttl);
    },
  });
  await Promise.all([
    resolver.resolve("fixture.test"),
    resolver.resolve("fixture.test"),
  ]);
  await resolver.resolve("fixture.test");
  assert.equal(calls, 2);
  ttl = 0;
  await resolver.resolve("zero.test");
  await resolver.resolve("zero.test");
  assert.equal(calls, 6);
  resolver.dispose();
  await assert.rejects(resolver.resolve("fixture.test"), {
    code: "UNAVAILABLE",
  });
});

test("failed DNS never becomes a local grant, including when the origin was previously allowed", async () => {
  const policy = createNetworkPolicy({
    allowedOrigins: ["https://fixture.test"],
    lookup: async () => [{ address: "198.18.0.1", family: 4 }],
    resolvePublic: async () => [{ address: "127.0.0.1", family: 4 }],
  });
  await assert.rejects(
    policy.resolve("https://fixture.test"),
    (error) => error.code === "NETWORK_DENIED" && !error.allowLocal,
  );
  for (const url of [
    "http://169.254.169.254",
    "http://198.18.0.1",
    "http://127.0.0.1:19280",
  ]) {
    await assert.rejects(
      createNetworkPolicy({ blockedPorts: [19280] }).resolve(url),
      (error) => !error.allowLocal,
    );
  }
  await assert.rejects(
    createNetworkPolicy().resolve("http://127.0.0.1:5173"),
    (error) => error.allowLocal === true,
  );
});

test("DNS transport pins its bootstrap, enforces its deadline and closes stalled sockets", async () => {
  let destination, socket;
  await assert.rejects(
    queryDNS("fixture.test", 1, {
      timeout: 20,
      dial: async (target, track) => {
        destination = target;
        socket = track(new net.Socket());
        return new Promise(() => {});
      },
      track: (value) => value,
    }),
    { code: "PUBLIC_DNS_FAILED" },
  );
  assert.equal(destination.host, "1.1.1.1");
  assert.equal(destination.url.hostname, "cloudflare-dns.com");
  assert.equal(socket.destroyed, true);
});

test("DNS transport rejects an untrusted resolver certificate without sending the query", async (t) => {
  const directory = await fs.mkdtemp(
    path.join(os.tmpdir(), "openfox-dns-tls-"),
  );
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const key = path.join(directory, "key.pem"),
    cert = path.join(directory, "cert.pem");
  execFileSync(
    "openssl",
    [
      "req",
      "-x509",
      "-newkey",
      "rsa:2048",
      "-nodes",
      "-keyout",
      key,
      "-out",
      cert,
      "-days",
      "1",
      "-subj",
      "/CN=cloudflare-dns.com",
    ],
    { stdio: "ignore" },
  );
  let hits = 0;
  const server = https.createServer(
    { key: await fs.readFile(key), cert: await fs.readFile(cert) },
    (_req, res) => {
      hits++;
      res.end("{}");
    },
  );
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  await assert.rejects(
    queryDNS("fixture.test", 1, {
      dial: async (_target, track) => {
        const socket = track(net.connect(server.address().port, "127.0.0.1"));
        await once(socket, "connect");
        return socket;
      },
      track: (socket) => socket,
    }),
    { code: "PUBLIC_DNS_FAILED" },
  );
  assert.equal(hits, 0);
});
