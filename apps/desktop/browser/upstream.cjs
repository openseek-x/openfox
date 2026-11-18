const net = require("node:net");
const tls = require("node:tls");
const { once } = require("node:events");
const { failure } = require("./policy.cjs");

function readHandshake(socket, decode) {
  return new Promise((resolve, reject) => {
    let bytes = Buffer.alloc(0);
    const clean = () => {
      socket.off("data", data);
      socket.off("error", error);
      socket.off("close", close);
    };
    const error = (value) => {
      clean();
      reject(value);
    };
    const close = () => error(failure("UPSTREAM_PROXY_UNAVAILABLE"));
    const data = (chunk) => {
      bytes = Buffer.concat([bytes, chunk]);
      if (bytes.length > 32768)
        return error(failure("UPSTREAM_PROXY_UNAVAILABLE"));
      try {
        const length = decode(bytes);
        if (length === null) return;
        socket.pause();
        clean();
        if (bytes.length > length) socket.unshift(bytes.subarray(length));
        resolve();
      } catch (value) {
        error(value);
      }
    };
    socket.on("data", data);
    socket.once("error", error);
    socket.once("close", close);
    socket.resume();
  });
}

function ipv6Bytes(address) {
  // Expand the already validated numeric IP; SOCKS must never resolve a name.
  const normalized = address.replace(
    /(\d+)\.(\d+)\.(\d+)\.(\d+)$/,
    (_, a, b, c, d) =>
      `${((+a << 8) | +b).toString(16)}:${((+c << 8) | +d).toString(16)}`,
  );
  const halves = normalized
    .split("::")
    .map((part) => (part ? part.split(":") : []));
  const words =
    halves.length === 1
      ? halves[0]
      : [
          ...halves[0],
          ...Array(8 - halves[0].length - halves[1].length).fill("0"),
          ...halves[1],
        ];
  const result = Buffer.alloc(16);
  words.forEach((word, index) =>
    result.writeUInt16BE(parseInt(word, 16), index * 2),
  );
  return result;
}

function createUpstreamDial({
  resolveProxy,
  authenticate = () => null,
  protectPort = () => {},
}) {
  return async (target, track, deadline = Date.now() + 10000) => {
    // Resolve proxy selection for the original URL, but tunnel only to the IP
    // already validated by our policy. Never silently try DIRECT after failure.
    let timer;
    let resolved;
    try {
      resolved = await Promise.race([
        resolveProxy(target.url.href),
        new Promise((_, reject) => {
          timer = setTimeout(
            () => reject(failure("CONNECT_TIMEOUT")),
            Math.max(1, deadline - Date.now()),
          );
        }),
      ]);
    } finally {
      clearTimeout(timer);
    }
    const rule = resolved.split(";")[0].trim();
    let proxy;
    if (rule !== "DIRECT") {
      const match =
        /^(PROXY|HTTPS|SOCKS5|SOCKS)\s+(\[[^\]]+\]|[^: ]+):(\d+)$/.exec(rule);
      if (!match) throw failure("UPSTREAM_PROXY_UNSUPPORTED");
      proxy = {
        type: match[1],
        host: match[2].replace(/^\[|\]$/g, ""),
        port: Number(match[3]),
      };
      protectPort(proxy.port);
    }
    const endpoint = proxy || { host: target.host, port: target.port };
    const socket = track(
      proxy?.type === "HTTPS"
        ? tls.connect({
            host: endpoint.host,
            port: endpoint.port,
            ...(net.isIP(endpoint.host) ? {} : { servername: endpoint.host }),
          })
        : net.connect({ host: endpoint.host, port: endpoint.port }),
    );
    const timeout = setTimeout(
      () => socket.destroy(failure("CONNECT_TIMEOUT")),
      Math.max(1, deadline - Date.now()),
    );
    try {
      await once(socket, proxy?.type === "HTTPS" ? "secureConnect" : "connect");
      if (!proxy) return socket;
      const credentials = authenticate({
        isProxy: true,
        host: proxy.host,
        port: proxy.port,
      });
      if (proxy.type === "PROXY" || proxy.type === "HTTPS") {
        const authority = `${target.family === 6 ? `[${target.host}]` : target.host}:${target.port}`;
        const auth = credentials
          ? `Proxy-Authorization: Basic ${Buffer.from(`${credentials.username}:${credentials.password}`).toString("base64")}\r\n`
          : "";
        socket.write(
          `CONNECT ${authority} HTTP/1.1\r\nHost: ${authority}\r\n${auth}\r\n`,
        );
        await readHandshake(socket, (bytes) => {
          const end = bytes.indexOf("\r\n\r\n");
          if (end < 0) return null;
          if (
            !/^HTTP\/1\.[01] 200(?: |\r\n)/.test(
              bytes.toString("latin1", 0, end + 2),
            )
          )
            throw failure("UPSTREAM_PROXY_UNAVAILABLE");
          return end + 4;
        });
      } else {
        socket.write(
          credentials ? Buffer.from([5, 2, 0, 2]) : Buffer.from([5, 1, 0]),
        );
        let method;
        await readHandshake(socket, (bytes) => {
          if (bytes.length < 2) return null;
          if (bytes[0] !== 5 || ![0, 2].includes(bytes[1]))
            throw failure("UPSTREAM_PROXY_UNAVAILABLE");
          method = bytes[1];
          return 2;
        });
        if (method === 2) {
          if (!credentials) throw failure("UPSTREAM_PROXY_UNAVAILABLE");
          const username = Buffer.from(credentials.username),
            password = Buffer.from(credentials.password);
          if (username.length > 255 || password.length > 255)
            throw failure("UPSTREAM_PROXY_UNAVAILABLE");
          socket.write(
            Buffer.concat([
              Buffer.from([1, username.length]),
              username,
              Buffer.from([password.length]),
              password,
            ]),
          );
          await readHandshake(socket, (bytes) => {
            if (bytes.length < 2) return null;
            if (bytes[0] !== 1 || bytes[1] !== 0)
              throw failure("UPSTREAM_PROXY_UNAVAILABLE");
            return 2;
          });
        }
        const port = Buffer.alloc(2);
        port.writeUInt16BE(target.port);
        const address =
          target.family === 6
            ? ipv6Bytes(target.host)
            : Buffer.from(target.host.split(".").map(Number));
        socket.write(
          Buffer.concat([
            Buffer.from([5, 1, 0, target.family === 6 ? 4 : 1]),
            address,
            port,
          ]),
        );
        await readHandshake(socket, (bytes) => {
          if (bytes.length < 5) return null;
          if (bytes[0] !== 5 || bytes[1] !== 0)
            throw failure("UPSTREAM_PROXY_UNAVAILABLE");
          const size =
            bytes[3] === 1
              ? 10
              : bytes[3] === 4
                ? 22
                : bytes[3] === 3
                  ? 7 + bytes[4]
                  : 0;
          if (!size) throw failure("UPSTREAM_PROXY_UNAVAILABLE");
          return bytes.length < size ? null : size;
        });
      }
      return socket;
    } catch (error) {
      socket.destroy();
      throw error;
    } finally {
      clearTimeout(timeout);
    }
  };
}
module.exports = { createUpstreamDial, ipv6Bytes };
