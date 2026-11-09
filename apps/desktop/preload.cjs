const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("openfoxDesktop", {
  copyWorkDirectory: (input) =>
    ipcRenderer.invoke("openfox:copy-work-directory", input),
  cancelWorkDirectoryCopy: (requestId) =>
    ipcRenderer.invoke("openfox:cancel-work-directory-copy", requestId),
  getProxy: () => ipcRenderer.invoke("openfox:get-proxy"),
  saveProxy: (config) => ipcRenderer.invoke("openfox:save-proxy", config),
  testProxy: (config) => ipcRenderer.invoke("openfox:test-proxy", config),
  accounts: () => ipcRenderer.invoke("openfox:accounts"),
  nameAccount: (name) => ipcRenderer.invoke("openfox:account-name", name),
  selectAccount: (id) => ipcRenderer.invoke("openfox:select-account", id),
  resolveWorkDirectory: (input) =>
    ipcRenderer.invoke("openfox:resolve-work-directory", input),
  getLocalStatus: () => ipcRenderer.invoke("openfox:local-status"),
  daemonAction: (action) => ipcRenderer.invoke("openfox:daemon-action", action),
  pickDirectory: (defaultPath) =>
    ipcRenderer.invoke("openfox:pick-directory", defaultPath),
  // 只传令牌字符串；主进程校验格式后用 execFile 调 CLI
  // options.force：本机配置还在但服务端已删除这台电脑时替换旧注册
  registerComputer: (installToken, options) =>
    ipcRenderer.invoke("openfox:register-computer", {
      installToken: String(installToken),
      force: Boolean(options && options.force === true),
    }),
  setPreventSleep: (enabled) =>
    ipcRenderer.invoke("openfox:prevent-sleep", { enabled: enabled === true }),
  // 网页按平台调整标题栏（macOS 左侧给红黄绿按钮留位）
  platform: process.platform,
  // 离线页「重试」：主进程重新读设置并加载网页
  retryRemote: () => ipcRenderer.invoke("openfox:retry-remote"),
});
