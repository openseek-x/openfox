// 菜单栏托盘：菜单模板构建与本地服务状态映射（纯函数，不依赖 electron，便于单测）。

const { message } = require("./messages.cjs");

/**
 * 本地服务状态（只读显示）：
 * installed = launchd 的 LaunchAgent 文件是否存在；daemonState = openfox daemon status 的 state。
 * 返回 running / stopped / not_installed / unknown。
 */
function serviceStatus({ installed, daemonState }) {
  if (!installed) return "not_installed";
  if (daemonState === "RUNNING" || daemonState === "STARTING") return "running";
  if (daemonState === "STOPPED" || daemonState === "STOPPING") return "stopped";
  return "unknown";
}

/**
 * 构建托盘菜单模板（Electron Menu.buildFromTemplate 的入参）。
 * handlers：{ open, checkUpdate, quit }；updateVersion 非空时检查更新项改为「发现新版本」。
 */
function buildTrayMenu({ locale, service, updateVersion, handlers }) {
  const t = (key, params) => message(locale, key, params);
  return [
    { label: t("tray.open"), click: handlers.open },
    { type: "separator" },
    { label: t(`tray.service.${service}`), enabled: false },
    { type: "separator" },
    {
      label: updateVersion
        ? t("tray.updateAvailable", { version: updateVersion })
        : t("tray.checkUpdate"),
      click: handlers.checkUpdate,
    },
    { type: "separator" },
    { label: t("tray.quit"), click: handlers.quit },
  ];
}

module.exports = { buildTrayMenu, serviceStatus };
