const fs = require("node:fs/promises");
const path = require("node:path");
const { randomUUID } = require("node:crypto");

function normalize(input) {
  if (input?.mode === "system" || input?.mode === "direct")
    return { mode: input.mode };
  if (
    input?.mode !== "manual" ||
    !["http", "https", "socks5"].includes(input.protocol) ||
    typeof input.host !== "string" ||
    !/^(?:[a-zA-Z0-9.-]{1,253}|\[[a-fA-F0-9:]{2,45}\])$/.test(input.host) ||
    !Number.isInteger(input.port) ||
    input.port < 1 ||
    input.port > 65535
  )
    throw Error("invalid proxy");
  const bypass = String(input.bypass || "").trim();
  if (bypass.length > 2048 || /[^\w.:*/\[\],;\s-]/.test(bypass))
    throw Error("invalid bypass rules");
  return {
    mode: input.mode,
    protocol: input.protocol,
    host: input.host,
    port: input.port,
    bypass,
  };
}

function proxyRules(config) {
  if (config.mode !== "manual") return { mode: config.mode };
  return {
    mode: "fixed_servers",
    proxyRules: config.protocol + "://" + config.host + ":" + config.port,
    proxyBypassRules: [
      "localhost",
      "127.0.0.1",
      "[::1]",
      ...config.bypass
        .split(/[;,\n]/)
        .map((v) => v.trim())
        .filter(Boolean),
    ].join(";"),
  };
}

function createProxy(file, encryption) {
  let current = { mode: "system" };
  let credentials;
  return {
    async load() {
      try {
        const saved = JSON.parse(await fs.readFile(file, "utf8"));
        current = normalize(saved.config);
        credentials = saved.credentials;
      } catch (err) {
        if (err.code !== "ENOENT") throw err;
        current = { mode: "system" };
        credentials = undefined;
      }
    },
    view() {
      return {
        ...current,
        username: credentials?.username || "",
        authenticated: !!credentials,
      };
    },
    config: () => proxyRules(current),
    authenticate(info) {
      if (
        !credentials ||
        !info.isProxy ||
        current.mode !== "manual" ||
        current.host.toLowerCase() !== info.host.toLowerCase() ||
        Number(info.port) !== current.port
      )
        return null;
      return {
        username: credentials.username,
        password: encryption.decryptString(
          Buffer.from(credentials.secret, "base64"),
        ),
      };
    },
    async save(input) {
      const config = normalize(input);
      let next;
      if (config.mode === "manual" && input.authenticated) {
        if (
          typeof input.username !== "string" ||
          !input.username ||
          input.username.length > 256
        )
          throw Error("invalid username");
        if (
          input.password &&
          typeof input.password === "string" &&
          input.password.length <= 2048 &&
          encryption.isEncryptionAvailable() &&
          (process.platform !== "linux" ||
            encryption.getSelectedStorageBackend?.() !== "basic_text")
        ) {
          next = {
            username: input.username,
            secret: encryption.encryptString(input.password).toString("base64"),
          };
        } else if (
          !input.password &&
          config.host === current.host &&
          config.port === current.port &&
          config.protocol === current.protocol &&
          input.username === credentials?.username
        )
          next = credentials;
        else throw Error("password unavailable");
      }
      const tmp = path.join(path.dirname(file), randomUUID() + ".tmp");
      await fs.writeFile(tmp, JSON.stringify({ config, credentials: next }), {
        mode: 0o600,
      });
      await fs.rename(tmp, file);
      current = config;
      credentials = next;
    },
  };
}
module.exports = { createProxy, normalize, proxyRules };
