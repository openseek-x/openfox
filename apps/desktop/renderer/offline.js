// 离线页：网页地址未配置、格式不对或加载失败时由主进程显示。参数来自主进程 loadFile 的 query，
// 只用 textContent 写入页面，不解析成 HTML。
(function () {
  const params = new URLSearchParams(window.location.search);
  const reason = params.get("reason") || "unreachable";
  const url = params.get("url") || "";
  const settingsPath = params.get("settings") || "desktop-settings.json";
  const zh = !/^en/i.test(navigator.language || "");

  const text = zh
    ? {
        title: {
          invalid_proxy: "无法应用网络代理设置",
          not_configured: "还没有配置 OpenFox 网页地址",
          invalid_url: "OpenFox 网页地址格式不对",
          unreachable: "无法连接到 OpenFox",
        },
        message: {
          invalid_proxy:
            "代理配置损坏或无法读取。请修复下面的文件，或移走该文件以恢复系统代理，然后重试。",
          not_configured:
            "桌面端会加载服务端提供的 OpenFox 网页，请先配置网页地址。",
          invalid_url:
            "网页地址必须是 http:// 或 https:// 开头，不能带账号密码、查询串或 # 片段。",
          unreachable:
            "网页加载失败，请确认网络可用、服务器可以访问，然后重试。",
        },
        hint: "在环境变量 OPENFOX_DESKTOP_WEB_URL 或下面的设置文件里配置 web_url：",
        retry: "重试",
        retrying: "正在重试…",
      }
    : {
        title: {
          invalid_proxy: "Cannot apply network proxy settings",
          not_configured: "OpenFox web URL is not configured",
          invalid_url: "OpenFox web URL is invalid",
          unreachable: "Cannot reach OpenFox",
        },
        message: {
          invalid_proxy:
            "Repair the proxy configuration file below or move it aside to restore the system proxy, then retry.",
          not_configured:
            "The desktop app loads the OpenFox web app from your server. Configure its URL first.",
          invalid_url:
            "The URL must start with http:// or https:// and must not include credentials, a query or a fragment.",
          unreachable:
            "The page failed to load. Check your network and that the server is reachable, then retry.",
        },
        hint: "Set OPENFOX_DESKTOP_WEB_URL, or web_url in this settings file:",
        retry: "Retry",
        retrying: "Retrying…",
      };

  const key = text.title[reason] ? reason : "unreachable";
  document.documentElement.lang = zh ? "zh-CN" : "en";
  document.getElementById("title").textContent = text.title[key];
  document.getElementById("message").textContent = text.message[key];
  const urlLine = document.getElementById("url");
  if (url) {
    urlLine.textContent = url;
    urlLine.hidden = false;
  }
  // 地址已配置、只是连不上时不提示去改配置（安装包自带默认地址，提示改配置会误导）
  if (key === "unreachable") document.getElementById("hint-box").hidden = true;
  document.getElementById("hint").textContent =
    key === "invalid_proxy" ? "" : text.hint;
  if (key === "invalid_proxy") document.getElementById("example").hidden = true;
  document.getElementById("settings").textContent = settingsPath;
  document.getElementById("example").textContent =
    '{ "web_url": "https://openfox.example.com" }';

  const retry = document.getElementById("retry");
  retry.textContent = text.retry;
  const bridge = window.openfoxDesktop;
  if (!bridge || typeof bridge.retryRemote !== "function") {
    retry.hidden = true;
    return;
  }
  retry.addEventListener("click", () => {
    retry.disabled = true;
    retry.textContent = text.retrying;
    bridge.retryRemote().catch(() => {
      retry.disabled = false;
      retry.textContent = text.retry;
    });
  });
})();
