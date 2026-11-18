const { BlockList, isIP } = require("node:net");
const { lookup: systemLookup } = require("node:dns/promises");

const privateAddresses = new BlockList();
for (const [address, prefix] of [
  ["10.0.0.0", 8],
  ["100.64.0.0", 10],
  ["127.0.0.0", 8],
  ["172.16.0.0", 12],
  ["192.168.0.0", 16],
])
  privateAddresses.addSubnet(address, prefix, "ipv4");
privateAddresses.addAddress("::1", "ipv6");
privateAddresses.addSubnet("fc00::", 7, "ipv6");

// IANA special-purpose addresses are not website destinations. In particular,
// link-local/metadata and transition ranges cannot be enabled with a LAN grant.
const reservedAddresses = new BlockList();
for (const [address, prefix] of [
  ["0.0.0.0", 8],
  ["169.254.0.0", 16],
  ["100.100.100.200", 32],
  ["192.0.0.0", 24],
  ["192.0.2.0", 24],
  ["192.88.99.0", 24],
  ["198.18.0.0", 15],
  ["198.51.100.0", 24],
  ["203.0.113.0", 24],
  ["224.0.0.0", 4],
  ["240.0.0.0", 4],
])
  reservedAddresses.addSubnet(address, prefix, "ipv4");
reservedAddresses.addSubnet("2001::", 23, "ipv6");
reservedAddresses.addSubnet("2001:db8::", 32, "ipv6");
reservedAddresses.addSubnet("2002::", 16, "ipv6");
reservedAddresses.addSubnet("3fff::", 20, "ipv6");
reservedAddresses.addAddress("fd00:ec2::254", "ipv6");
const globalIPv6 = new BlockList();
globalIPv6.addSubnet("2000::", 3, "ipv6");
const mappedIPv4 = new BlockList();
mappedIPv4.addSubnet("::ffff:0:0", 96, "ipv6");
const fakeAddresses = new BlockList();
fakeAddresses.addSubnet("198.18.0.0", 15, "ipv4");
fakeAddresses.addSubnet("2001:2::", 48, "ipv6");

function failure(code) {
  return Object.assign(new Error(code), { code });
}

function normalizeURL(input) {
  if (
    typeof input !== "string" ||
    input.length > 16384 ||
    /[\\\x00-\x20\x7f]/.test(input.trim())
  )
    throw failure("INVALID_URL");
  let value = input.trim();
  if (!value || value === "about:blank") return "about:blank";
  if (/^(?:[\\/.]|[a-z]:[\\/])/i.test(value)) throw failure("INVALID_URL");
  if (
    !/^[a-z][a-z0-9+.-]*:/i.test(value) ||
    /^(?:localhost|[a-z0-9-]+(?:\.[a-z0-9-]+)+):\d+(?:[/?#]|$)/i.test(value)
  ) {
    const local =
      /^(?:localhost|127(?:\.\d+){3}|\[::1\])(?::\d+)?(?:[/?#]|$)/i.test(value);
    value = `${local ? "http" : "https"}://${value}`;
  }
  let url;
  try {
    url = new URL(value);
  } catch {
    throw failure("INVALID_URL");
  }
  if (
    !["http:", "https:"].includes(url.protocol) ||
    url.username ||
    url.password
  )
    throw failure("INVALID_URL");
  return url.href;
}

function addressKind(address) {
  const family = isIP(address);
  if (!family || address.includes("%")) return "reserved";
  const type = family === 4 ? "ipv4" : "ipv6";
  if (reservedAddresses.check(address, type)) return "reserved";
  if (privateAddresses.check(address, type)) return "private";
  if (
    family === 6 &&
    !globalIPv6.check(address, type) &&
    !mappedIPv4.check(address, type)
  )
    return "reserved";
  return "public";
}

function createNetworkPolicy({
  allowedOrigins = [],
  blockedPorts = [],
  lookup = systemLookup,
  resolvePublic,
  dnsTimeout = 10000,
} = {}) {
  const allowed = new Set();
  const blocked = new Set(blockedPorts);
  const allowOrigin = (value) => {
    const normalized = normalizeURL(value);
    if (normalized === "about:blank") throw failure("INVALID_URL");
    allowed.add(new URL(normalized).origin);
  };
  allowedOrigins.forEach(allowOrigin);
  return {
    allowOrigin,
    blockPort: (port) => blocked.add(port),
    async resolve(value, { tunnel = false } = {}) {
      const normalized = normalizeURL(value);
      if (normalized === "about:blank") throw failure("INVALID_URL");
      const url = new URL(normalized);
      const hostname = url.hostname.replace(/^\[|\]$/g, "");
      const port = Number(url.port || (url.protocol === "https:" ? 443 : 80));
      let timer;
      let results;
      try {
        results = isIP(hostname)
          ? [{ address: hostname, family: isIP(hostname) }]
          : await Promise.race([
              lookup(hostname, { all: true, verbatim: true }),
              new Promise((_, reject) => {
                timer = setTimeout(
                  () => reject(failure("DNS_TIMEOUT")),
                  dnsTimeout,
                );
              }),
            ]);
      } finally {
        clearTimeout(timer);
      }
      if (!results.length || results.length > 64) throw failure("DNS_FAILED");
      // Synthetic DNS addresses are never destinations, even with a LAN grant.
      // Only names whose answers are entirely synthetic/public can use DoH.
      const synthetic = (address) =>
        fakeAddresses.check(address, isIP(address) === 6 ? "ipv6" : "ipv4");
      if (
        !isIP(hostname) &&
        resolvePublic &&
        results.some((result) => synthetic(result.address)) &&
        results.every(
          (result) =>
            synthetic(result.address) ||
            addressKind(result.address) === "public",
        )
      ) {
        results = await resolvePublic(hostname);
        if (results.some((result) => addressKind(result.address) !== "public"))
          throw failure("NETWORK_DENIED");
      }
      if (!results.length || results.length > 64) throw failure("DNS_FAILED");
      // Check every answer before offering a LAN grant; a mixed answer must not
      // make reserved destinations or protected application ports grantable.
      for (const result of results) {
        const kind = addressKind(result.address);
        if (kind === "reserved") throw failure("NETWORK_DENIED");
        if (kind === "private" && blocked.has(port))
          throw failure("PROTECTED_ENDPOINT");
      }
      for (const result of results) {
        const kind = addressKind(result.address);
        // Chromium tunnels ws:// as CONNECT too, which carries host:port but
        // no scheme. The session request gate separately enforces exact origins.
        const granted =
          allowed.has(url.origin) ||
          (tunnel &&
            allowed.has(new URL(`http://${url.hostname}:${port}`).origin));
        if (kind === "private" && !granted)
          throw Object.assign(failure("NETWORK_DENIED"), { allowLocal: true });
      }
      // Dial only numeric addresses from this validated answer.
      return results.map(({ address }) => ({
        url,
        host: address,
        family: isIP(address),
        port,
      }));
    },
  };
}

module.exports = { addressKind, createNetworkPolicy, failure, normalizeURL };
