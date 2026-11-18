// 退出确认流程（纯逻辑，依赖由调用方注入，便于单测）。
//
// 对齐 HiFox：用户主动退出（菜单、Cmd+Q、托盘「退出」）都弹确认；弹窗里有「保持后台服务运行」复选框，
// 默认勾选（沿用上次选择）。勾选＝只退出应用，本地服务继续运行；取消勾选＝先停止本地服务再退出。
// 系统关机/注销触发的退出不弹窗、不停止服务。

const { message } = require("./messages.cjs");
const { serviceStatus } = require("./tray.cjs");

/**
 * @param {object} deps
 * @param {() => Promise<{ installed: boolean, daemon: { state: string, active_runs?: number } | null }>} deps.getService
 * @param {() => boolean} deps.getKeepService   上次选择（默认 true）
 * @param {(value: boolean) => Promise<unknown>} deps.setKeepService
 * @param {(options: object) => Promise<{ response: number, checkboxChecked: boolean }>} deps.showDialog
 * @param {() => Promise<unknown>} deps.stopService  openfox daemon stop
 * @param {(title: string, detail: string) => Promise<unknown>} deps.showError
 * @param {() => boolean} deps.isShuttingDown
 * @param {string} deps.locale
 * @returns {() => Promise<boolean>} 返回 true 表示可以退出应用
 */
function createQuitFlow(deps) {
  const t = (key, params) => message(deps.locale, key, params);
  return async function confirmQuit() {
    if (deps.isShuttingDown()) return true;
    let service;
    try {
      service = await deps.getService();
    } catch {
      service = { installed: false, daemon: null };
    }
    const status = serviceStatus({
      installed: service.installed,
      daemonState: service.daemon ? service.daemon.state : "UNAVAILABLE",
    });
    // 只有服务确实在运行时才有「停止」可言；其余情况不显示复选框，在正文里说明
    const canStop = status === "running";
    const runs =
      canStop && Number.isInteger(service.daemon.active_runs)
        ? Math.max(0, service.daemon.active_runs)
        : 0;
    const keepDefault = deps.getKeepService();
    const lines = canStop
      ? [t("quit.detail.service")]
      : [t(`quit.detail.${status}`)];
    if (canStop && runs > 0) lines.push(t("quit.detail.runs", { count: runs }));
    const options = {
      type: "question",
      message: t("quit.message"),
      detail: lines.join("\n\n"),
      buttons: [t("quit.confirm"), t("quit.cancel")],
      defaultId: 0,
      cancelId: 1,
      ...(canStop
        ? { checkboxLabel: t("quit.keepService"), checkboxChecked: keepDefault }
        : {}),
    };
    const result = await deps.showDialog(options);
    if (result.response !== 0) return false;
    if (!canStop) return true;
    const keep = result.checkboxChecked === true;
    if (keep !== keepDefault) await deps.setKeepService(keep).catch(() => {});
    if (!keep) {
      try {
        await deps.stopService();
      } catch (error) {
        // 停止失败不阻止退出，但要告诉用户服务可能还在运行
        const reason =
          error && typeof error.message === "string" && error.message
            ? error.message
            : "unknown";
        await deps
          .showError(
            t("quit.stopFailed.message"),
            t("quit.stopFailed.detail", { reason }),
          )
          .catch(() => {});
      }
    }
    return true;
  };
}

module.exports = { createQuitFlow };
