// 开机自启适配层：包一层 Electron 的 app.get/setLoginItemSettings，便于注入假对象单测。
// 状态以系统实际值为准：写入后重新读取，读回值与期望不一致就视为失败。
// 非 macOS、未打包的开发运行下不注册登录项（避免把开发用的 electron 可执行文件写进系统）。

/**
 * @param {{ app: { getLoginItemSettings(): object, setLoginItemSettings(o: object): void },
 *    platform: string, packaged: boolean }} deps
 */
function createLoginItem({ app, platform, packaged }) {
  function unsupportedReason() {
    if (platform !== "darwin") return "unsupported_platform";
    if (!packaged) return "not_packaged";
    return null;
  }

  function read() {
    const reason = unsupportedReason();
    if (reason) return { supported: false, enabled: false, reason };
    let settings;
    try {
      settings = app.getLoginItemSettings();
    } catch {
      return { supported: true, enabled: false, reason: "read_failed" };
    }
    if (settings && settings.status === "requires-approval") {
      return { supported: true, enabled: false, reason: "requires_approval" };
    }
    return {
      supported: true,
      enabled: Boolean(settings && settings.openAtLogin),
    };
  }

  return {
    get: read,
    /** 返回 { ok, state, reason? }；state 始终是系统实际值。 */
    set(enabled) {
      if (typeof enabled !== "boolean") {
        return { ok: false, state: read(), reason: "invalid" };
      }
      const blocked = unsupportedReason();
      if (blocked) {
        return {
          ok: false,
          state: { supported: false, enabled: false, reason: blocked },
          reason: blocked,
        };
      }
      try {
        app.setLoginItemSettings({ openAtLogin: enabled });
      } catch {
        return { ok: false, state: read(), reason: "rejected" };
      }
      const state = read();
      if (state.enabled !== enabled) {
        return { ok: false, state, reason: state.reason || "rejected" };
      }
      return { ok: true, state };
    },
  };
}

module.exports = { createLoginItem };
