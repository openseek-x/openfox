const https = require("node:https");
const tls = require("node:tls");
const { isIP } = require("node:net");
const { addressKind, failure } = require("./policy.cjs");

// A fixed numeric bootstrap avoids resolving the resolver through Fake-IP DNS.
// HTTPS still verifies cloudflare-dns.com's certificate. The dialer retains the
// user's proxy routing/authentication and never falls through to DIRECT.
function queryDNS(hostname, type, { dial, track, timeout = 8000 }) {
  const url = new URL("https://cloudflare-dns.com/dns-query");
  url.search = new URLSearchParams({ name: hostname, type: String(type) });
  return new Promise((resolve, reject) => {
    let done = false,
      request;
    const sockets = new Set();
    const agent = new https.Agent({ keepAlive: false, maxSockets: 1 });
    const finish = (error, value) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      request?.destroy();
      agent.destroy();
      sockets.forEach((socket) => socket.destroy());
      if (error) reject(failure("PUBLIC_DNS_FAILED"));
      else resolve(value);
    };
    const timer = setTimeout(() => finish(true), timeout);
    agent.createConnection = (_options, callback) => {
      void dial({ url, host: "1.1.1.1", family: 4, port: 443 }, (socket) => {
        if (done) {
          socket.destroy();
          throw failure("PUBLIC_DNS_FAILED");
        }
        sockets.add(socket);
        return track(socket);
      }).then(
        (socket) => {
          if (done) return socket.destroy();
          const secure = tls.connect({
            socket,
            servername: url.hostname,
            rejectUnauthorized: true,
          });
          sockets.add(secure);
          callback(null, secure);
        },
        (error) => callback(error),
      );
    };
    request = https.get(
      url,
      {
        agent,
        headers: { accept: "application/dns-json" },
      },
      (response) => {
        if (response.statusCode !== 200) return finish(true);
        const chunks = [];
        let size = 0;
        response.on("data", (chunk) => {
          size += chunk.length;
          if (size > 32768) finish(true);
          else chunks.push(chunk);
        });
        response.on("error", () => finish(true));
        response.on("end", () => {
          try {
            finish(null, JSON.parse(Buffer.concat(chunks).toString("utf8")));
          } catch {
            finish(true);
          }
        });
      },
    );
    request.on("error", () => finish(true));
  });
}

function decodeAnswers(hostname, type, data) {
  const name = (value) =>
    typeof value === "string" ? value.toLowerCase().replace(/\.$/, "") : "";
  if (
    data?.Status !== 0 ||
    data.TC !== false ||
    data.Question?.length !== 1 ||
    data.Question[0].type !== type ||
    name(data.Question[0].name) !== name(hostname) ||
    (data.Answer !== undefined && !Array.isArray(data.Answer)) ||
    (data.Answer?.length || 0) > 64
  )
    throw failure("PUBLIC_DNS_FAILED");
  let ttl = 60;
  const addresses = [];
  for (const record of data.Answer || []) {
    if (!Number.isInteger(record.TTL) || record.TTL < 0)
      throw failure("PUBLIC_DNS_FAILED");
    ttl = Math.min(ttl, record.TTL);
    if (record.type !== 1 && record.type !== 28) continue;
    const family = isIP(record.data);
    if (family !== (record.type === 1 ? 4 : 6))
      throw failure("PUBLIC_DNS_FAILED");
    if (addressKind(record.data) !== "public") throw failure("NETWORK_DENIED");
    if (record.type === type) addresses.push({ address: record.data, family });
  }
  return { addresses, ttl };
}

function createPublicDNS(options) {
  const cache = new Map(),
    pending = new Map();
  const query =
    options.query || ((hostname, type) => queryDNS(hostname, type, options));
  let disposed = false;
  return {
    async resolve(hostname) {
      if (disposed) throw failure("UNAVAILABLE");
      const cached = cache.get(hostname);
      if (cached?.expires > Date.now()) return cached.addresses;
      cache.delete(hostname);
      if (pending.has(hostname)) return pending.get(hostname);
      if (pending.size >= 16) throw failure("PUBLIC_DNS_FAILED");
      const job = (async () => {
        const answers = await Promise.all(
          [1, 28].map(async (type) =>
            decodeAnswers(hostname, type, await query(hostname, type)),
          ),
        );
        const addresses = answers.flatMap((answer) => answer.addresses);
        if (!addresses.length || addresses.length > 64)
          throw failure("PUBLIC_DNS_FAILED");
        if (disposed) throw failure("UNAVAILABLE");
        if (cache.size >= 256) cache.delete(cache.keys().next().value);
        cache.set(hostname, {
          addresses,
          expires:
            Date.now() +
            Math.min(...answers.map((answer) => answer.ttl)) * 1000,
        });
        return addresses;
      })();
      pending.set(hostname, job);
      try {
        return await job;
      } finally {
        pending.delete(hostname);
      }
    },
    dispose() {
      disposed = true;
      cache.clear();
    },
  };
}

module.exports = { createPublicDNS, queryDNS, decodeAnswers };
