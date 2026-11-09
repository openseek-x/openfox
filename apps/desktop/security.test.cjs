// 桌面主进程安全函数单测：node --test（apps/desktop 没有 vitest / jsdom 依赖）。
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { describe, it } = require("node:test");

const {
  INSTALL_TOKEN_MAX_LENGTH,
  activeRunCount,
  buildCsp,
  createSleepGuard,
  injectCspMeta,
  isTrustedSender,
  isWorkDirectoryOrigin,
  isSafeWebLink,
  newWindowAction,
  isValidInstallToken,
  loadSettings,
  parseRegistrationStatus,
  parseWebURL,
  registerFailureReason,
  resolveLocalFile,
  saveSettings,
  trustedSources,
} = require("./security.cjs");

function event({ id = 1, url, parent = null, frame = true } = {}) {
  return {
    sender: { id },
    senderFrame: frame ? { url, parent } : null,
  };
}

describe("桌面授权窗口与外链", () => {
  const web = "http://127.0.0.1:4186";
  const source = `${web}/o/o1/settings/integrations/dingtalk`;
  it("现有 OAuth 流使用独立窗口，普通外链交系统浏览器", () => {
    for (const provider of [
      "feishu",
      "lark",
      "dingtalk",
      "github",
      "gitlab",
      "gitlab_cn",
      "gitee",
      "mcp",
    ]) {
      assert.equal(
        newWindowAction(
          {
            url: "https://provider.example/authorize?state=qa",
            frameName: `openfox-${provider}-oauth`,
          },
          source,
          web,
        ),
        "oauth",
      );
    }
    assert.equal(
      newWindowAction(
        {
          url: "http://127.0.0.1:18108/oauth2/auth",
          frameName: "openfox-feishu-oauth",
        },
        source,
        web,
      ),
      "oauth",
    );
    assert.equal(
      newWindowAction(
        { url: "https://example.com/help", frameName: "_blank" },
        source,
        web,
      ),
      "external",
    );
  });
  it("拒绝非网页协议、嵌入凭据及不可信来源", () => {
    for (const url of [
      "file:///etc/passwd",
      "javascript:alert(1)",
      "data:text/html,x",
      "about:blank",
      "custom://launch",
      "https://user:pass@example.com/",
      "not a url",
    ]) {
      assert.equal(isSafeWebLink(url), false, url);
      assert.equal(
        newWindowAction(
          { url, frameName: "openfox-feishu-oauth" },
          source,
          web,
        ),
        "deny",
      );
    }
    for (const origin of [
      "https://evil.example/",
      "file:///offline.html",
      "http://127.0.0.1:4187/",
    ]) {
      assert.equal(
        newWindowAction(
          {
            url: "https://provider.example",
            frameName: "openfox-feishu-oauth",
          },
          origin,
          web,
        ),
        "deny",
      );
    }
    assert.equal(
      newWindowAction({ url: "https://provider.example" }, source, null),
      "deny",
    );
  });
});

describe("IPC 来源校验", () => {
  const dev = {
    webContentsId: 1,
    sources: trustedSources("http://127.0.0.1:4173/"),
  };
  const local = { webContentsId: 1, sources: trustedSources(null) };

  it("网页模式：同源与本地离线页放行，其他来源拒绝", () => {
    assert.equal(
      isTrustedSender(
        event({ url: "http://127.0.0.1:4173/o/o1/settings" }),
        dev,
      ),
      true,
    );
    for (const url of [
      "http://127.0.0.1:4174/",
      "https://127.0.0.1:4173/",
      "http://localhost:4173/",
      "https://evil.example/",
      "file://server/share/offline.html",
      "",
      "not a url",
    ])
      assert.equal(isTrustedSender(event({ url }), dev), false, url);
    assert.equal(
      isTrustedSender(
        event({ url: "file:///Applications/OpenFox/renderer/offline.html" }),
        dev,
      ),
      true,
    );
  });

  it("未配置网页地址：只放行 file://（无主机），http 与 UNC 主机拒绝", () => {
    assert.equal(
      isTrustedSender(
        event({ url: "file:///Applications/OpenFox/index.html" }),
        local,
      ),
      true,
    );
    assert.equal(
      isTrustedSender(event({ url: "file:///o/o1/chat" }), local),
      true,
    );
    assert.equal(
      isTrustedSender(event({ url: "file://server/share/index.html" }), local),
      false,
    );
    assert.equal(
      isTrustedSender(event({ url: "http://127.0.0.1:4173/" }), local),
      false,
    );
    assert.equal(isTrustedSender(event({ url: "about:blank" }), local), false);
  });

  it("非主窗口、子 frame、frame 已销毁、主窗口不存在一律拒绝", () => {
    const url = "http://127.0.0.1:4173/";
    assert.equal(isTrustedSender(event({ id: 2, url }), dev), false);
    assert.equal(isTrustedSender(event({ url, parent: { url } }), dev), false);
    assert.equal(isTrustedSender(event({ url, frame: false }), dev), false);
    assert.equal(isTrustedSender(event({ url }), null), false);
    assert.equal(isTrustedSender(null, dev), false);
  });
});

describe("安装令牌格式", () => {
  it("要求 ofx_v2_ 前缀、base64url 字符、长度不超过 512", () => {
    assert.equal(isValidInstallToken("ofx_v2_eyJ2IjoyfQ"), true);
    assert.equal(
      isValidInstallToken("ofx_v2_" + "a".repeat(INSTALL_TOKEN_MAX_LENGTH - 7)),
      true,
    );
    assert.equal(
      isValidInstallToken("ofx_v2_" + "a".repeat(INSTALL_TOKEN_MAX_LENGTH - 6)),
      false,
    );
    for (const value of [
      "ofx_v2_",
      "ofx_v1_abc",
      "abc",
      "ofx_v2_abc def",
      "ofx_v2_abc;rm -rf /",
      "ofx_v2_abc\n--force",
      "ofx_v2_abc=",
      "--install-token",
      null,
      undefined,
      42,
      { installToken: "ofx_v2_abc" },
    ])
      assert.equal(isValidInstallToken(value), false, String(value));
  });
});

describe("CSP", () => {
  it("不允许 unsafe-eval，不能联网", () => {
    const csp = buildCsp();
    assert.match(csp, /default-src 'self'/);
    assert.match(csp, /script-src 'self'(;|$)/);
    assert.doesNotMatch(csp, /unsafe-eval/);
    assert.match(csp, /connect-src 'self';/);
  });

  it("网页地址必须是不带账号、查询串和片段的 HTTP(S)", () => {
    assert.equal(parseWebURL(""), null);
    assert.equal(parseWebURL(undefined), null);
    assert.equal(
      parseWebURL(" https://openfox.example.com "),
      "https://openfox.example.com/",
    );
    assert.equal(
      parseWebURL("http://127.0.0.1:18092"),
      "http://127.0.0.1:18092/",
    );
    for (const raw of [
      "ftp://x",
      "file:///etc/passwd",
      "javascript:alert(1)",
      "https://a:b@x",
      "https://x/?next=evil",
      "https://x/#a",
      "not a url",
    ])
      assert.throws(() => parseWebURL(raw), undefined, raw);
  });

  it("meta 插到 head 开头并转义引号", () => {
    const html = injectCspMeta(
      "<html><head><title>x</title></head></html>",
      'a "b"',
    );
    assert.equal(
      html,
      '<html><head><meta http-equiv="Content-Security-Policy" content="a &quot;b&quot;" /><title>x</title></head></html>',
    );
  });
});

describe("本地文件范围", () => {
  const dist = path.resolve("/opt/openfox/web/dist");
  const url = (file) => pathToFileURL(file).href;

  it("dist 内放行，目录外、目录本身与上跳路径拒绝", () => {
    assert.equal(
      resolveLocalFile(url(path.join(dist, "static/js/index.js")), dist),
      path.join(dist, "static/js/index.js"),
    );
    assert.equal(resolveLocalFile(url(dist), dist), null);
    assert.equal(resolveLocalFile(url("/etc/passwd"), dist), null);
    assert.equal(
      resolveLocalFile(url("/opt/openfox/web/dist-evil/a.js"), dist),
      null,
    );
    assert.equal(
      resolveLocalFile("file:///opt/openfox/web/dist/../../key/a.pem", dist),
      null,
    );
    assert.equal(resolveLocalFile("http://x/a.js", dist), null);
  });
});

describe("桌面设置", () => {
  it("缺失或损坏时为默认值；原子写入且权限 0600", async () => {
    const dir = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-desktop-"));
    try {
      const file = path.join(dir, "desktop-settings.json");
      const empty = { prevent_sleep: false, web_url: null };
      assert.deepEqual(await loadSettings(file), empty);
      await fs.writeFile(file, "{broken");
      assert.deepEqual(await loadSettings(file), empty);
      await saveSettings(file, {
        prevent_sleep: true,
        web_url: " http://127.0.0.1:18092 ",
        extra: "x",
      });
      const saved = { prevent_sleep: true, web_url: "http://127.0.0.1:18092" };
      assert.deepEqual(await loadSettings(file), saved);
      assert.deepEqual(JSON.parse(await fs.readFile(file, "utf8")), saved);
      // 切换防止休眠不丢网页地址
      await saveSettings(file, { ...saved, prevent_sleep: false });
      assert.deepEqual(await loadSettings(file), {
        ...saved,
        prevent_sleep: false,
      });
      await fs.writeFile(file, JSON.stringify({ web_url: 42 }));
      assert.deepEqual(await loadSettings(file), empty);
      if (process.platform !== "win32")
        assert.equal((await fs.stat(file)).mode & 0o777, 0o600);
      assert.deepEqual(await fs.readdir(dir), ["desktop-settings.json"]);
    } finally {
      await fs.rm(dir, { recursive: true, force: true });
    }
  });
});

describe("防止休眠", () => {
  function fakeBlocker() {
    const started = new Set();
    let next = 0;
    return {
      calls: [],
      start(type) {
        this.calls.push(["start", type]);
        started.add(++next);
        return next;
      },
      stop(id) {
        this.calls.push(["stop", id]);
        started.delete(id);
      },
      isStarted: (id) => started.has(id),
    };
  }

  it("启用且有活动 Run 才阻止；不重复 start；关闭或无活动 Run 时释放", () => {
    const blocker = fakeBlocker();
    const guard = createSleepGuard(blocker);
    assert.equal(guard.update({ enabled: true, activeRuns: 0 }), false);
    assert.equal(guard.update({ enabled: false, activeRuns: 2 }), false);
    assert.equal(guard.update({ enabled: true, activeRuns: 2 }), true);
    assert.equal(guard.update({ enabled: true, activeRuns: 1 }), true);
    assert.equal(guard.active, true);
    assert.equal(guard.update({ enabled: true, activeRuns: 0 }), false);
    assert.equal(guard.update({ enabled: true, activeRuns: 3 }), true);
    assert.equal(guard.update({ enabled: false, activeRuns: 3 }), false);
    assert.deepEqual(blocker.calls, [
      ["start", "prevent-app-suspension"],
      ["stop", 1],
      ["start", "prevent-app-suspension"],
      ["stop", 2],
    ]);
  });

  it("活动 Run 数：缺失、负数、非整数按 0", () => {
    assert.equal(activeRunCount({ active_runs: 2 }), 2);
    for (const status of [
      {},
      null,
      { active_runs: -1 },
      { active_runs: 1.5 },
      { active_runs: "3" },
    ])
      assert.equal(activeRunCount(status), 0);
  });
});

describe("register 失败原因", () => {
  it("只返回原因代码", () => {
    assert.equal(registerFailureReason(null, ""), null);
    assert.equal(registerFailureReason({ killed: true }, ""), "timeout");
    assert.equal(registerFailureReason({ code: "ENOENT" }, ""), "unavailable");
    assert.equal(
      registerFailureReason(
        { code: "ERR_CHILD_PROCESS_STDIO_MAXBUFFER", killed: true },
        "",
      ),
      "failed",
    );
    assert.equal(registerFailureReason({ code: 2 }), "invalid_token");
    assert.equal(registerFailureReason({ code: 3 }), "already_registered");
    assert.equal(registerFailureReason({ code: 4 }), "token_rejected");
    assert.equal(registerFailureReason({ code: 5 }), "center_unavailable");
    assert.equal(registerFailureReason({ code: 1 }), "failed");
    assert.equal(registerFailureReason({ code: 99 }), "failed");
  });
});

describe("parseRegistrationStatus", () => {
  it("已注册时透传 http(s) 中心地址", () => {
    assert.deepEqual(
      parseRegistrationStatus(
        JSON.stringify({
          registered: true,
          organization_id: "o1",
          device_id: "d1",
          center_url: "https://203.0.113.42",
        }),
      ),
      {
        registered: true,
        organization_id: "o1",
        device_id: "d1",
        center_url: "https://203.0.113.42",
      },
    );
  });

  it("旧版 CLI 没有 center_url 或地址不合法时不传", () => {
    for (const center_url of [
      undefined,
      "",
      "ftp://x",
      "javascript:1",
      "x",
      5,
    ]) {
      const result = parseRegistrationStatus(
        JSON.stringify({
          registered: true,
          organization_id: "o1",
          device_id: "d1",
          center_url,
        }),
      );
      assert.equal("center_url" in result, false, String(center_url));
    }
  });

  it("未注册只返回原因", () => {
    assert.deepEqual(parseRegistrationStatus('{"registered":false}'), {
      registered: false,
      reason: "not_registered",
    });
    assert.deepEqual(
      parseRegistrationStatus('{"registered":false,"reason":"invalid_config"}'),
      { registered: false, reason: "invalid_config" },
    );
  });
});

it("目录解析只使用当前中心的身份，开发代理只接受同一回环主机", () => {
  assert.ok(
    isWorkDirectoryOrigin("https://center.test/o/qa", "https://center.test"),
  );
  assert.ok(
    !isWorkDirectoryOrigin("https://other.test", "https://center.test", true),
  );
  assert.ok(
    !isWorkDirectoryOrigin("openfox://desktop/offline", "https://center.test"),
  );
  assert.ok(
    !isWorkDirectoryOrigin("http://127.0.0.1:4186", "http://127.0.0.1:18096"),
  );
  assert.ok(
    isWorkDirectoryOrigin(
      "http://127.0.0.1:4186/o/qa",
      "http://127.0.0.1:18096",
      true,
    ),
  );
});
