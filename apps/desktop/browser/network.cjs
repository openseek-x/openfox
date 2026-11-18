const http = require("node:http");
const net = require("node:net");
const { randomBytes, timingSafeEqual } = require("node:crypto");
const { once } = require("node:events");
const { createNetworkPolicy, failure } = require("./policy.cjs");
const { createPublicDNS } = require("./public-dns.cjs");

const MAX_CONNECTIONS = 64;
const CONNECT_TIMEOUT = 10000;
const IDLE_TIMEOUT = 120000;

function forwardedHeaders(headers) {
  const omit = new Set([
    "proxy-authorization",
    "proxy-authenticate",
    "proxy-connection",
    "connection",
    "keep-alive",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
    ...(headers.connection || "")
      .toLowerCase()
      .split(",")
      .map((v) => v.trim()),
  ]);
  return Object.fromEntries(
    Object.entries(headers).filter(([key]) => !omit.has(key.toLowerCase())),
  );
}

async function createBrowserNetwork(options = {}) {
  const username = "openfox-browser";
  const password = randomBytes(32).toString("hex");
  const expected = Buffer.from(
    `Basic ${Buffer.from(`${username}:${password}`).toString("base64")}`,
  );
  const sockets = new Set();
  let disposed = false;
  const authorized = (headers) => {
    const actual = Buffer.from(headers["proxy-authorization"] || "");
    return (
      actual.length === expected.length && timingSafeEqual(actual, expected)
    );
  };
  const track = (socket) => {
    if (disposed || sockets.size >= MAX_CONNECTIONS) {
      socket.destroy();
      throw failure("UNAVAILABLE");
    }
    sockets.add(socket);
    socket.on("error", () => {});
    socket.once("close", () => sockets.delete(socket));
    socket.setTimeout(IDLE_TIMEOUT, () => socket.destroy());
    return socket;
  };
  const dial = async (
    target,
    trackSocket = track,
    deadline = Date.now() + CONNECT_TIMEOUT,
  ) => {
    if (disposed || sockets.size >= MAX_CONNECTIONS)
      throw failure("UNAVAILABLE");
    if (options.dial) {
      const socket = await options.dial(target, trackSocket, deadline);
      if (disposed) {
        socket.destroy();
        throw failure("UNAVAILABLE");
      }
      return socket;
    }
    const socket = trackSocket(
      net.connect({
        host: target.host,
        port: target.port,
        family: target.family,
      }),
    );
    const timer = setTimeout(
      () => socket.destroy(failure("CONNECT_TIMEOUT")),
      Math.max(1, deadline - Date.now()),
    );
    try {
      await once(socket, "connect");
    } finally {
      clearTimeout(timer);
    }
    return socket;
  };
  const publicDNS = createPublicDNS({ dial, track });
  const policy = createNetworkPolicy({
    ...options,
    resolvePublic: publicDNS.resolve,
  });
  const requestFailures = new Map();
  const boundContents = new Set();
  const connect = async (url, resolveOptions) => {
    const targets = await policy.resolve(url, resolveOptions);
    const deadline = Date.now() + CONNECT_TIMEOUT;
    let lastError;
    for (const [i, target] of targets.entries()) {
      const now = Date.now();
      if (now >= deadline || disposed) break;
      const attemptDeadline =
        now + Math.ceil((deadline - now) / (targets.length - i));
      try {
        return { socket: await dial(target, track, attemptDeadline), target };
      } catch (err) {
        lastError = err;
      }
    }
    throw lastError || failure("UNAVAILABLE");
  };
  const statusFor = (error) =>
    ["INVALID_URL", "PROTECTED_ENDPOINT", "NETWORK_DENIED"].includes(error.code)
      ? 403
      : 502;

  const server = http.createServer(
    {
      maxHeaderSize: 32768,
      requestTimeout: IDLE_TIMEOUT,
      headersTimeout: 10000,
    },
    (request, response) => {
      if (!authorized(request.headers)) {
        response.writeHead(407, {
          "Proxy-Authenticate": 'Basic realm="OpenFox browser"',
        });
        response.end();
        return;
      }
      let socket, upstream;
      const cancel = () => {
        socket?.destroy();
        upstream?.destroy();
      };
      response.once("close", cancel);
      void (async () => {
        // HTTPS and WSS use CONNECT; no TLS termination or certificate bypass.
        if (!request.url.startsWith("http://")) throw failure("INVALID_URL");
        const connected = await connect(request.url);
        socket = connected.socket;
        if (request.destroyed || response.destroyed) return socket.destroy();
        const { url } = connected.target;
        const agent = new http.Agent({ keepAlive: false, maxSockets: 1 });
        agent.createConnection = () => socket;
        response.once("close", () => agent.destroy());
        upstream = http.request(
          {
            method: request.method,
            hostname: url.hostname,
            port: url.port || 80,
            path: url.pathname + url.search,
            agent,
            headers: {
              ...forwardedHeaders(request.headers),
              host: url.host,
              connection: "close",
            },
          },
          (result) => {
            response.writeHead(
              result.statusCode,
              forwardedHeaders(result.headers),
            );
            result.on("error", () => response.destroy());
            result.pipe(response);
          },
        );
        upstream.on("error", () => {
          if (!response.headersSent) response.writeHead(502);
          response.end();
        });
        request.on("aborted", cancel);
        request.pipe(upstream);
      })().catch((error) => {
        cancel();
        if (!response.headersSent) response.writeHead(statusFor(error));
        response.end();
      });
    },
  );
  server.on("connection", (socket) => {
    if (disposed || sockets.size >= MAX_CONNECTIONS) return socket.destroy();
    track(socket);
  });
  server.on("connect", (request, client, head) => {
    if (!authorized(request.headers)) {
      client.end(
        'HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm="OpenFox browser"\r\nConnection: close\r\n\r\n',
      );
      return;
    }
    void (async () => {
      if (!/^(?:\[[0-9a-f:.]+\]|[a-z0-9.-]+):[0-9]{1,5}$/i.test(request.url))
        throw failure("INVALID_URL");
      const { socket } = await connect(`https://${request.url}`, {
        tunnel: true,
      });
      if (client.destroyed) return socket.destroy();
      client.once("close", () => socket.destroy());
      socket.once("close", () => client.destroy());
      client.write("HTTP/1.1 200 Connection Established\r\n\r\n");
      if (head.length) socket.write(head);
      client.pipe(socket).pipe(client);
    })().catch((error) =>
      client.end(
        `HTTP/1.1 ${statusFor(error)} Connection Refused\r\nConnection: close\r\n\r\n`,
      ),
    );
  });
  server.on("upgrade", (request, client, head) => {
    if (!authorized(request.headers)) {
      client.end(
        'HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm="OpenFox browser"\r\nConnection: close\r\n\r\n',
      );
      return;
    }
    void (async () => {
      if (
        request.method !== "GET" ||
        request.headers.upgrade?.toLowerCase() !== "websocket" ||
        !/^(?:http|ws):\/\//.test(request.url)
      )
        throw failure("INVALID_URL");
      const { socket, target } = await connect(
        request.url.replace(/^ws:/, "http:"),
      );
      if (client.destroyed) return socket.destroy();
      client.once("close", () => socket.destroy());
      socket.once("close", () => client.destroy());
      const headers = {
        ...forwardedHeaders(request.headers),
        host: target.url.host,
        connection: "Upgrade",
        upgrade: "websocket",
      };
      socket.write(
        `GET ${target.url.pathname + target.url.search} HTTP/1.1\r\n${Object.entries(
          headers,
        )
          .map(([key, value]) => `${key}: ${value}`)
          .join("\r\n")}\r\n\r\n`,
      );
      if (head.length) socket.write(head);
      client.pipe(socket).pipe(client);
    })().catch((error) =>
      client.end(
        `HTTP/1.1 ${statusFor(error)} Connection Refused\r\nConnection: close\r\n\r\n`,
      ),
    );
  });
  server.on("clientError", (_error, socket) => socket.destroy());
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const port = server.address().port;
  policy.blockPort(port);
  const url = `http://127.0.0.1:${port}`;
  return {
    url,
    username,
    password,
    policy,
    failureFor(contentsId, url) {
      const failed = requestFailures.get(contentsId);
      return failed?.url.split("#")[0] === url.split("#")[0]
        ? failed.error
        : null;
    },
    async attach(session) {
      await session.setProxy({
        mode: "fixed_servers",
        proxyRules: url,
        proxyBypassRules: "<-loopback>",
      });
      await session.closeAllConnections();
      session.webRequest.onBeforeRequest(
        { urls: ["<all_urls>"] },
        (details, callback) => {
          if (/^(?:https?|wss?):/.test(details.url)) {
            const target = details.url
              .replace(/^ws:/, "http:")
              .replace(/^wss:/, "https:");
            if (details.resourceType === "mainFrame")
              requestFailures.delete(details.webContentsId);
            void policy.resolve(target).then(
              () => callback({}),
              (error) => {
                if (
                  !disposed &&
                  boundContents.has(details.webContentsId) &&
                  details.resourceType === "mainFrame"
                )
                  requestFailures.set(details.webContentsId, {
                    url: details.url,
                    error,
                  });
                callback({ cancel: true });
              },
            );
          } else
            callback({ cancel: !/^(?:about|data|blob):/.test(details.url) });
        },
      );
    },
    bind(contents) {
      const contentsId = contents.id;
      boundContents.add(contentsId);
      const login = (event, _details, info, callback) => {
        if (info.isProxy && info.host === "127.0.0.1" && info.port === port) {
          event.preventDefault();
          callback(username, password);
        }
      };
      contents.on("login", login);
      contents.setWebRTCIPHandlingPolicy("disable_non_proxied_udp");
      return () => {
        contents.off("login", login);
        boundContents.delete(contentsId);
        requestFailures.delete(contentsId);
      };
    },
    async dispose() {
      if (disposed) return;
      disposed = true;
      publicDNS.dispose();
      requestFailures.clear();
      boundContents.clear();
      const done = new Promise((resolve) => server.close(resolve));
      sockets.forEach((socket) => socket.destroy());
      await done;
    },
  };
}

module.exports = { createBrowserNetwork, forwardedHeaders };
