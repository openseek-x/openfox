const { contextBridge, ipcRenderer } = require("electron");

contextBridge.exposeInMainWorld("openfoxDesktop", {
  browserMount: (input) => ipcRenderer.invoke("openfox:browser-mount", input),
  browserCommand: (input) =>
    ipcRenderer.invoke("openfox:browser-command", input),
  onBrowserState: (callback) => {
    const listener = (_event, state) => callback(state);
    ipcRenderer.on("openfox:browser-state", listener);
    return () => ipcRenderer.removeListener("openfox:browser-state", listener);
  },
  onBrowserFocusAddress: (callback) => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on("openfox:browser-focus-address", listener);
    return () =>
      ipcRenderer.removeListener("openfox:browser-focus-address", listener);
  },
  onBrowserToggleSidebar: (callback) => {
    const listener = (_event, value) => callback(value);
    ipcRenderer.on("openfox:browser-toggle-sidebar", listener);
    return () =>
      ipcRenderer.removeListener("openfox:browser-toggle-sidebar", listener);
  },
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
  getLocalService: (input) =>
    ipcRenderer.invoke("openfox:local-service-service-status", input),
  setLocalKeepRunning: (input) =>
    ipcRenderer.invoke("openfox:local-service-keep-running", input),
  setLocalAwake: (input) =>
    ipcRenderer.invoke("openfox:local-service-timed-awake", input),
  moveLocalStorage: (input) =>
    ipcRenderer.invoke("openfox:local-service-move-storage", input),
  openLocalStorage: (input) =>
    ipcRenderer.invoke("openfox:local-service-open-storage", input),
  getMachineProxy: (input) =>
    ipcRenderer.invoke("openfox:local-service-proxy-status", input),
  saveMachineProxy: (input) =>
    ipcRenderer.invoke("openfox:local-service-proxy-save", input),
  setLocalStartup: (input) =>
    ipcRenderer.invoke("openfox:local-service-startup", input),
  readLocalLogs: (input) =>
    ipcRenderer.invoke("openfox:local-service-logs", input),
  localDaemonAction: (input) =>
    ipcRenderer.invoke("openfox:local-service-action", input),
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
  // 阶段 12h：桌面偏好、开机自启、更新检查与关于页。参数都在主进程校验，
  // 渲染进程不能传 URL 或路径（下载地址只取主进程里已校验的清单）。
  getDesktopPrefs: () => ipcRenderer.invoke("openfox:desktop-prefs"),
  setKeepInTray: (enabled) =>
    ipcRenderer.invoke("openfox:set-keep-in-tray", {
      enabled: enabled === true,
    }),
  setLoginItem: (enabled) =>
    ipcRenderer.invoke("openfox:set-login-item", { enabled: enabled === true }),
  getAbout: () => ipcRenderer.invoke("openfox:about"),
  checkForUpdate: () => ipcRenderer.invoke("openfox:check-update"),
  openUpdateDownload: () => ipcRenderer.invoke("openfox:open-update-download"),
  skipUpdate: () => ipcRenderer.invoke("openfox:skip-update"),
  // 网页按平台调整标题栏（macOS 左侧给红黄绿按钮留位）
  platform: process.platform,
  // 离线页「重试」：主进程重新读设置并加载网页
  retryRemote: () => ipcRenderer.invoke("openfox:retry-remote"),
});
