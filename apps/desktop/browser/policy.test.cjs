const test = require("node:test");
const assert = require("node:assert/strict");
const {
  addressKind,
  createNetworkPolicy,
  normalizeURL,
} = require("./policy.cjs");

test("address entry normalizes websites and local development origins", () => {
  for (const [input, expected] of [
    ["example.com", "https://example.com/"],
    ["example.com:8080/a", "https://example.com:8080/a"],
    ["localhost:5173", "http://localhost:5173/"],
    ["127.0.0.1:5173", "http://127.0.0.1:5173/"],
    ["[::1]:5173", "http://[::1]:5173/"],
    [
      " https://example.com/path?q=1#part ",
      "https://example.com/path?q=1#part",
    ],
    ["", "about:blank"],
  ])
    assert.equal(normalizeURL(input), expected);
  for (const input of [
    "file:///etc/passwd",
    "javascript:alert(1)",
    "data:text/html,x",
    "ftp://example.com",
    "https://u:p@example.com",
    "//example.com",
    "https://example.com\\@localhost",
    "https://exa\nmple.com",
    "/tmp/page.html",
  ])
    assert.throws(() => normalizeURL(input), { code: "INVALID_URL" });
});

test("reserved, metadata, IPv6 transition and mapped addresses cannot evade policy", () => {
  for (const ip of [
    "169.254.169.254",
    "100.100.100.200",
    "::ffff:100.100.100.200",
    "fd00:ec2::254",
    "0.0.0.0",
    "224.0.0.1",
    "198.18.1.1",
    "::",
    "fe80::1",
    "64:ff9b::7f00:1",
    "2002:7f00:1::",
    "::ffff:169.254.169.254",
  ])
    assert.equal(addressKind(ip), "reserved", ip);
  for (const ip of [
    "127.0.0.1",
    "10.1.2.3",
    "100.64.1.1",
    "172.16.0.1",
    "192.168.1.1",
    "::1",
    "fd00::1",
    "::ffff:127.0.0.1",
    "::ffff:7f00:1",
  ])
    assert.equal(addressKind(ip), "private", ip);
  for (const ip of ["8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"])
    assert.equal(addressKind(ip), "public", ip);
});

test("private grants are exact origins and never allow protected or reserved endpoints", async () => {
  const policy = createNetworkPolicy({
    allowedOrigins: [
      "http://127.0.0.1:5173",
      "http://169.254.169.254",
      "http://100.100.100.200",
      "http://[fd00:ec2::254]",
      "http://127.0.0.1:19280",
    ],
    blockedPorts: [19280],
  });
  assert.equal(
    (await policy.resolve("http://127.0.0.1:5173/page"))[0].host,
    "127.0.0.1",
  );
  for (const url of [
    "http://127.0.0.1:5174",
    "https://127.0.0.1:5173",
    "http://169.254.169.254",
    "http://100.100.100.200",
    "http://[fd00:ec2::254]",
  ])
    await assert.rejects(policy.resolve(url), { code: "NETWORK_DENIED" });
  await assert.rejects(policy.resolve("http://127.0.0.1:19280"), {
    code: "PROTECTED_ENDPOINT",
  });
});

test("each connection resolves once, rejects rebinding and mixed private answers", async () => {
  let calls = 0;
  const policy = createNetworkPolicy({
    lookup: async () => [
      { address: ++calls === 1 ? "8.8.8.8" : "127.0.0.1", family: 4 },
    ],
  });
  assert.equal(
    (await policy.resolve("https://fixture.test"))[0].host,
    "8.8.8.8",
  );
  assert.equal(calls, 1);
  await assert.rejects(policy.resolve("https://fixture.test"), {
    code: "NETWORK_DENIED",
  });
  const mixed = createNetworkPolicy({
    lookup: async () => [
      { address: "8.8.8.8", family: 4 },
      { address: "::ffff:127.0.0.1", family: 6 },
    ],
  });
  await assert.rejects(mixed.resolve("https://fixture.test"), {
    code: "NETWORK_DENIED",
  });
});

test("DNS failure and stalled resolution are bounded", async () => {
  await assert.rejects(
    createNetworkPolicy({ lookup: async () => [] }).resolve(
      "https://fixture.test",
    ),
    { code: "DNS_FAILED" },
  );
  await assert.rejects(
    createNetworkPolicy({
      lookup: () => new Promise(() => {}),
      dnsTimeout: 10,
    }).resolve("https://fixture.test"),
    { code: "DNS_TIMEOUT" },
  );
});
