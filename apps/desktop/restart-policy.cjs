// 升级后自带 CLI 与本地服务的重启策略（纯函数）。
//
// 背景：launchd 的 LaunchAgent 记录的是安装时的 openfox 可执行文件路径。安装自带 CLI 用的是
// 「临时文件 + rename」的原子替换，已运行的 daemon 继续用旧文件的 inode，新版本要重启服务才生效。
// 因此：CLI 被覆盖（state=installed）且本地服务正在运行时，才需要重启；
// 重启会终止本机运行中的 Run，所以必须确认没有运行中的 Run。

/**
 * @param {{ cliState: string, daemon: { state: string, active_runs?: number } | null, pending: boolean }} input
 *   cliState：本次启动的 CLI 安装结果；pending：此前已决定重启但被推迟。
 * 返回：
 *   none     不需要重启（CLI 没变且没有待办，或服务没在运行）
 *   restart  现在重启（服务运行中且没有运行中的 Run）
 *   defer    有运行中的 Run，推迟到空闲
 *   manual   拿不到可靠的服务状态，不动服务，提示用户手动重启
 */
function decideRestart({ cliState, daemon, pending }) {
  const needed = pending || cliState === "installed";
  if (!needed) return "none";
  if (!daemon || daemon.state === "UNAVAILABLE") return "manual";
  // 服务没在运行：下次启动自然用新文件
  if (daemon.state === "STOPPED") return "none";
  if (daemon.state !== "RUNNING") return "defer";
  // active_runs 缺失说明 daemon 没上报，不能当作 0
  if (!Number.isInteger(daemon.active_runs) || daemon.active_runs < 0) {
    return "manual";
  }
  return daemon.active_runs > 0 ? "defer" : "restart";
}

module.exports = { decideRestart };
