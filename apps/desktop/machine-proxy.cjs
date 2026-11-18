const { createHash } = require("node:crypto");
const { normalize } = require("./proxy.cjs");

const modes = ["DISABLED", "FOLLOW_DESKTOP", "CUSTOM"];
const digest = /^[0-9a-f]{64}$/;
const revision = (v) => Number.isSafeInteger(v) && v >= 0;

function desktopSnapshot(source) {
  const config = normalize(source);
  if (config.mode === "system") throw Error("system_proxy_unsupported");
  if (config.mode === "manual") {
    if (config.protocol === "socks5") throw Error("socks_proxy_unsupported");
    if (source.authenticated) throw Error("authenticated_proxy_unsupported");
    if (config.bypass) throw Error("bypass_proxy_unsupported");
  }
  const effective =
    config.mode === "direct"
      ? { mode: "direct" }
      : {
          mode: "proxy",
          protocol: config.protocol,
          host: config.host,
          port: config.port,
        };
  return {
    effective,
    source_fingerprint: createHash("sha256")
      .update(JSON.stringify({ config, authenticated: !!source.authenticated }))
      .digest("hex"),
  };
}

function effectiveProxy(value) {
  if (
    !value ||
    Object.keys(value).some(
      (k) => !["mode", "protocol", "host", "port"].includes(k),
    )
  )
    throw Error("machine_proxy_invalid");
  if (value.mode === "direct" && Object.keys(value).length === 1)
    return { mode: "direct" };
  const config = normalize({
    mode: "manual",
    protocol: value.protocol,
    host: value.host,
    port: value.port,
  });
  if (value.mode !== "proxy" || config.protocol === "socks5")
    throw Error("machine_proxy_invalid");
  return {
    mode: "proxy",
    protocol: config.protocol,
    host: config.host,
    port: config.port,
  };
}

function saveRequest(input, source, followUpdate = false) {
  if (
    !revision(input.expected_revision) ||
    !digest.test(input.scope_token) ||
    !modes.includes(input.mode)
  )
    throw Error("machine_proxy_invalid");
  const body = {
    expected_revision: input.expected_revision,
    scope_token: input.scope_token,
    mode: input.mode,
  };
  if (input.mode === "CUSTOM") {
    body.effective = effectiveProxy(input.effective);
    if (body.effective.mode !== "proxy") throw Error("machine_proxy_invalid");
  }
  if (input.mode === "FOLLOW_DESKTOP")
    Object.assign(body, desktopSnapshot(source));
  if (followUpdate) body.follow_update = true;
  return JSON.stringify(body);
}

function readView(raw) {
  if (typeof raw !== "string" || Buffer.byteLength(raw) > 16384)
    throw Error("machine_proxy_unavailable");
  const v = JSON.parse(raw);
  if (
    !revision(v.revision) ||
    !digest.test(v.scope_token) ||
    !modes.includes(v.mode)
  )
    throw Error("machine_proxy_unavailable");
  const result = {
    revision: v.revision,
    scope_token: v.scope_token,
    mode: v.mode,
  };
  if (v.problem !== undefined) {
    if (
      !["machine_proxy_invalid", "machine_proxy_registration_changed"].includes(
        v.problem,
      )
    )
      throw Error("machine_proxy_unavailable");
    result.problem = v.problem;
  }
  if (v.effective !== undefined) result.effective = effectiveProxy(v.effective);
  if (v.mode !== "DISABLED" && !result.effective)
    throw Error("machine_proxy_unavailable");
  if (v.source_fingerprint !== undefined) {
    if (!digest.test(v.source_fingerprint))
      throw Error("machine_proxy_unavailable");
    result.source_fingerprint = v.source_fingerprint;
  }
  if (v.updated_at !== undefined) {
    if (
      typeof v.updated_at !== "string" ||
      v.updated_at.length > 40 ||
      !Number.isFinite(Date.parse(v.updated_at))
    )
      throw Error("machine_proxy_unavailable");
    result.updated_at = v.updated_at;
  }
  if (v.runtime) {
    result.runtime = {};
    for (const key of ["loaded_revision", "connection_revision"]) {
      if (v.runtime[key] !== undefined) {
        if (!revision(v.runtime[key])) throw Error("machine_proxy_unavailable");
        result.runtime[key] = v.runtime[key];
      }
    }
    if (v.runtime.problem) result.runtime.problem = "machine_proxy_unavailable";
  }
  return result;
}

function sourceState(view, source) {
  try {
    const snapshot = desktopSnapshot(source);
    return {
      ...view,
      desktop_supported: true,
      source_state:
        view.mode !== "FOLLOW_DESKTOP"
          ? "not_following"
          : snapshot.source_fingerprint === view.source_fingerprint
            ? "synced"
            : "outdated",
    };
  } catch {
    return {
      ...view,
      desktop_supported: false,
      source_state:
        view.mode === "FOLLOW_DESKTOP" ? "unsupported" : "not_following",
    };
  }
}

module.exports = {
  desktopSnapshot,
  effectiveProxy,
  saveRequest,
  readView,
  sourceState,
};
