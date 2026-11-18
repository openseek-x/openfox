// Isolated Electron fixture. Never used by the desktop application's entry point.
const { app, BrowserWindow, session } = require("electron");
const fs = require("node:fs");
const path = require("node:path");
const { createBrowserNetwork } = require("./network.cjs");
const { createBrowserSurface } = require("./surface.cjs");
const config = JSON.parse(process.env.OPENFOX_BROWSER_TEST_CONFIG);
app.setPath("userData", path.join(config.directory, "profile"));
app.setName("OpenFox Browser Verification");

void app.whenReady().then(async () => {
  const network = await createBrowserNetwork({
    allowedOrigins: [config.origin, config.secureOrigin],
  });
  const browserSession = session.fromPartition("browser-verification");
  await network.attach(browserSession);
  // Only the locally generated fixture certificate is trusted, only in this
  // temporary test partition. Product code retains Chromium verification.
  browserSession.setCertificateVerifyProc((request, callback) =>
    callback(
      request.hostname === "127.0.0.1" &&
        request.certificate.data.trim() === config.certificate.trim()
        ? 0
        : -3,
    ),
  );
  await session.defaultSession.cookies.set({
    url: config.origin,
    name: "main_session",
    value: "must-stay-in-main",
  });
  const window = new BrowserWindow({
    width: 1040,
    height: 780,
    useContentSize: true,
    show: true,
    webPreferences: {
      sandbox: true,
      nodeIntegration: false,
      contextIsolation: true,
    },
  });
  await window.loadURL(
    "data:text/html;charset=utf-8," +
      encodeURIComponent(`<!doctype html><meta charset="utf-8"><style>
body{margin:0;background:#202124;color:#eee;font:15px system-ui}header{height:58px;display:flex;align-items:center;padding:0 24px;border-bottom:1px solid #45464b}aside{padding:24px;width:275px}small{color:#bbb}.surface{position:absolute;left:340px;top:80px;width:660px;height:600px}.surface img{width:100%;height:100%}#overlay{display:none;position:absolute;left:500px;top:230px;width:330px;padding:20px;border-radius:10px;background:#30323a;box-shadow:0 8px 40px #0009;box-sizing:border-box;border:1px solid #747887}textarea{box-sizing:border-box;width:100%;height:90px;margin:14px 0;background:#202124;color:white;border:1px solid #656770;padding:10px}button{padding:8px 14px;float:right}#snapshot{display:none}
</style><header>OpenFox · 内置浏览器技术验证</header><aside><h2>本机浏览器</h2><p>独立网站会话</p><small>仅用于阶段 1 验证。不是正式产品界面。</small></aside><div class="surface"><img id="snapshot"></div><div id="overlay"><b>添加批注</b><textarea>保留这段网页内容的原始样式</textarea><button>添加到对话</button></div>`),
  );
  const surface = createBrowserSurface({
    window,
    session: browserSession,
    network,
  });
  surface.place({ x: 340, y: 80, width: 660, height: 600 });
  surface.show(true);
  await surface.navigate(config.origin);
  globalThis.browserFixture = {
    window,
    surface,
    network,
    browserSession,
    async freeze() {
      const capture = await surface.capture();
      await window.webContents.executeJavaScript(
        `(async () => {const image=document.getElementById('snapshot');image.src=${JSON.stringify(capture.image.toDataURL())};await image.decode();image.style.display='block';document.getElementById('overlay').style.display='block';})()`,
      );
      surface.show(false);
      return capture.generation;
    },
    async resume() {
      surface.show(true);
      await window.webContents.executeJavaScript(
        "document.getElementById('snapshot').style.display='none';document.getElementById('overlay').style.display='none'",
      );
    },
    async save(filename, wholeWindow = false) {
      const image = wholeWindow
        ? await window.webContents.capturePage()
        : (await surface.capture()).image;
      fs.writeFileSync(path.join(config.evidence, filename), image.toPNG());
      return {
        width: image.getSize().width,
        height: image.getSize().height,
        bytes: image.toPNG().length,
      };
    },
  };
});
app.on("window-all-closed", () => app.quit());
