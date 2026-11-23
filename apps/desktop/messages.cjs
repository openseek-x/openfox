// 主进程文案（托盘、退出确认、更新提示）。渲染进程的文案在 packages/i18n，这里只放原生菜单与对话框用的字符串。
// 界面语言跟随系统：zh* 用中文，其余用英文。

const MESSAGES = {
  "zh-CN": {
    "tray.tooltip": "OpenFox",
    "tray.open": "打开 OpenFox",
    "tray.service.running": "本地服务：运行中",
    "tray.service.stopped": "本地服务：已停止",
    "tray.service.not_installed": "本地服务：未安装",
    "tray.service.unknown": "本地服务：状态未知",
    "tray.checkUpdate": "检查更新…",
    "tray.updateAvailable": "发现新版本 {version}…",
    "tray.quit": "退出 OpenFox",
    "quit.message": "确定要退出 OpenFox 吗？",
    "quit.detail.service":
      "本地服务在后台运行。勾选「保持后台服务运行」只退出应用；取消勾选会同时停止本地服务。",
    "quit.detail.runs": "停止后台服务会中断 {count} 个正在运行的任务。",
    "quit.detail.not_installed": "这台电脑尚未安装本地服务，退出应用不受影响。",
    "quit.detail.stopped": "本地服务当前已停止，退出应用不受影响。",
    "quit.detail.unknown":
      "暂时无法获取本地服务状态，退出应用不会停止本地服务。",
    "quit.keepService": "保持后台服务运行",
    "quit.stopFailed.message": "停止本地服务失败",
    "quit.stopFailed.detail":
      "原因：{reason}。应用仍会退出，本地服务可能仍在后台运行。",
    "quit.confirm": "退出",
    "quit.cancel": "取消",
    "update.available.message": "发现新版本 {version}",
    "update.available.detail":
      "当前版本 {current}。\n\n{notes}\n\n下载后请核对 SHA-256：\n{sha256}",
    "update.available.mandatory": "当前版本低于最低要求 {min}，请尽快升级。",
    "update.ready.message": "使用 macOS 安装器安装更新 {version}？",
    "update.ready.detail":
      "更新包已下载。请在系统安装器中确认安装，完成后重新打开 OpenFox。",
    "update.restart": "打开系统安装器",
    "update.installFailed.message": "未能准备桌面更新",
    "update.installFailed.detail":
      "请从可写位置运行已安装的 OpenFox，然后重新检查更新。若仍失败，请联系发布方核对发布的安装包。",
    "update.download": "前往下载",
    "update.skip": "跳过此版本",
    "update.later": "稍后",
    "update.upToDate.message": "已是最新版本",
    "update.upToDate.detail": "当前版本 {current}。",
    "update.noInfo.message": "暂无更新信息",
    "update.noInfo.detail": "当前连接的服务没有发布桌面端更新。",
    "update.failed.message": "检查更新失败",
    "update.failed.detail": "请稍后重试。",
    "update.unsupported.message": "无法检查更新",
    "update.unsupported.detail": "只有 https 地址的服务支持检查更新。",
    "update.rejected.message": "更新信息未通过校验",
    "update.rejected.detail": "更新清单格式不正确或下载地址不受信任，已忽略。",
    "update.ok": "好",
    "restart.manual.title": "请手动重启本地服务",
    "restart.manual.body":
      "OpenFox 已更新自带命令行工具，需要重启本地服务才能生效。请在「本机服务」里手动重启。",
  },
  "en-US": {
    "tray.tooltip": "OpenFox",
    "tray.open": "Open OpenFox",
    "tray.service.running": "Local service: running",
    "tray.service.stopped": "Local service: stopped",
    "tray.service.not_installed": "Local service: not installed",
    "tray.service.unknown": "Local service: unknown",
    "tray.checkUpdate": "Check for Updates…",
    "tray.updateAvailable": "New version {version} available…",
    "tray.quit": "Quit OpenFox",
    "quit.message": "Quit OpenFox?",
    "quit.detail.service":
      'The local service runs in the background. Keep "Keep the background service running" checked to quit only the app; uncheck it to stop the local service too.',
    "quit.detail.runs":
      "Stopping the background service will interrupt {count} running task(s).",
    "quit.detail.not_installed":
      "The local service is not installed on this computer. Quitting is not affected.",
    "quit.detail.stopped":
      "The local service is already stopped. Quitting is not affected.",
    "quit.detail.unknown":
      "The local service status is unavailable. Quitting will not stop the local service.",
    "quit.keepService": "Keep the background service running",
    "quit.stopFailed.message": "Could not stop the local service",
    "quit.stopFailed.detail":
      "Reason: {reason}. The app will still quit and the local service may keep running.",
    "quit.confirm": "Quit",
    "quit.cancel": "Cancel",
    "update.available.message": "Version {version} is available",
    "update.available.detail":
      "Current version {current}.\n\n{notes}\n\nVerify its SHA-256 after downloading:\n{sha256}",
    "update.available.mandatory":
      "Your version is below the minimum required {min}. Please upgrade soon.",
    "update.ready.message": "Open macOS Installer for update {version}?",
    "update.ready.detail":
      "The update is downloaded. Confirm installation in macOS Installer and reopen OpenFox when it finishes.",
    "update.restart": "Open macOS Installer",
    "update.installFailed.message": "Could not prepare the desktop update",
    "update.installFailed.detail":
      "Run the installed app from a writable location and check again. If this persists, ask the publisher to verify the published installer package.",
    "update.download": "Download",
    "update.skip": "Skip This Version",
    "update.later": "Later",
    "update.upToDate.message": "You're up to date",
    "update.upToDate.detail": "Current version {current}.",
    "update.noInfo.message": "No update information",
    "update.noInfo.detail":
      "The connected server does not publish desktop updates.",
    "update.failed.message": "Update check failed",
    "update.failed.detail": "Please try again later.",
    "update.unsupported.message": "Cannot check for updates",
    "update.unsupported.detail":
      "Only servers on an https address support update checks.",
    "update.rejected.message": "Update information rejected",
    "update.rejected.detail":
      "The manifest is malformed or its download address is not trusted, so it was ignored.",
    "update.ok": "OK",
    "restart.manual.title": "Restart the local service manually",
    "restart.manual.body":
      "OpenFox updated its bundled command-line tool. Restart the local service from Local service for it to take effect.",
  },
};

function pickLocale(systemLocale) {
  return /^zh/i.test(String(systemLocale || "")) ? "zh-CN" : "en-US";
}

/** 取文案并替换 {name} 占位符；缺失的键原样返回键名。 */
function message(locale, key, params = {}) {
  const table = MESSAGES[locale] || MESSAGES["en-US"];
  const template = table[key];
  if (typeof template !== "string") return key;
  return template.replace(/\{(\w+)\}/g, (_, name) =>
    name in params ? String(params[name]) : `{${name}}`,
  );
}

module.exports = { MESSAGES, message, pickLocale };
