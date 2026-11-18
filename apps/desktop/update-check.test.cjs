const { test } = require("node:test");
const assert = require("node:assert/strict");
const {
  checkForUpdate,
  compareVersions,
  manifestURL,
  parseHostList,
  validateManifest,
} = require("./update-check.cjs");

const CENTER = "https://center.example.com/some/page";
const SHA = "a".repeat(64);
const good = (extra = {}) => ({
  version: "0.2.0",
  download_url: "https://center.example.com/downloads/OpenFox-0.2.0-arm64.dmg",
  notes: "修复若干问题",
  sha256: SHA,
  ...extra,
});
const serve = (body, status = 200) => {
  const seen = [];
  return {
    seen,
    fetchText: async (url, options) => {
      seen.push({ url, options });
      return {
        status,
        text: typeof body === "string" ? body : JSON.stringify(body),
      };
    },
  };
};
const run = (body, extra = {}, status = 200) => {
  const server = serve(body, status);
  return checkForUpdate({
    centerURL: CENTER,
    current: "0.1.0",
    fetchText: server.fetchText,
    ...extra,
  }).then((result) => ({ result, server }));
};

test("语义化版本比较：数值、预发布、非法格式", () => {
  assert.equal(compareVersions("0.10.0", "0.9.0"), 1);
  assert.equal(compareVersions("1.0.0", "1.0.0"), 0);
  assert.equal(compareVersions("1.0.0-rc.1", "1.0.0"), -1);
  assert.equal(compareVersions("1.0.0-rc.2", "1.0.0-rc.10"), -1);
  assert.equal(compareVersions("1.0.0-alpha", "1.0.0-alpha.1"), -1);
  assert.equal(compareVersions("1.0.0+build5", "1.0.0"), 0);
  for (const bad of [
    "1.0",
    "v1.0.0",
    "01.0.0",
    "1.0.0-01",
    "",
    "1.0.0.0",
    null,
    1,
  ]) {
    assert.equal(compareVersions(bad, "1.0.0"), null, String(bad));
  }
});

test("清单地址只取 https 中心服务的 origin", () => {
  assert.equal(
    manifestURL(CENTER),
    "https://center.example.com/desktop/latest.json",
  );
  assert.equal(manifestURL("http://center.example.com"), null);
  assert.equal(manifestURL("https://u:p@center.example.com"), null);
  assert.equal(manifestURL("not a url"), null);
  assert.equal(
    manifestURL("https://1.2.3.4:8443/x"),
    "https://1.2.3.4:8443/desktop/latest.json",
  );
});

test("有新版本：返回已校验清单，请求带 10 秒超时与大小上限，不自动下载", async () => {
  const { result, server } = await run(good());
  assert.equal(result.status, "update_available");
  assert.equal(result.manifest.version, "0.2.0");
  assert.equal(result.manifest.sha256, SHA);
  assert.equal(result.skipped, false);
  assert.equal(result.mandatory, false);
  assert.equal(server.seen.length, 1);
  assert.equal(
    server.seen[0].url,
    "https://center.example.com/desktop/latest.json",
  );
  assert.equal(server.seen[0].options.timeoutMs <= 10_000, true);
  assert.equal(server.seen[0].options.maxBytes <= 64 * 1024, true);
});

test("当前已是最新或更高：无更新", async () => {
  assert.equal(
    (await run(good({ version: "0.1.0" }))).result.status,
    "up_to_date",
  );
  assert.equal(
    (await run(good({ version: "0.0.9" }))).result.status,
    "up_to_date",
  );
});

test("404 视为没有更新信息，其他状态码与网络错误视为失败", async () => {
  assert.equal((await run("", {}, 404)).result.status, "no_update_info");
  assert.equal((await run("", {}, 500)).result.status, "failed");
  assert.equal((await run("", {}, 503)).result.status, "failed");
  // 200 但内容为空不是合法清单
  assert.equal((await run("")).result.status, "invalid_manifest");
  const result = await checkForUpdate({
    centerURL: CENTER,
    current: "0.1.0",
    fetchText: async () => {
      throw new Error("timeout");
    },
  });
  assert.equal(result.status, "failed");
});

test("SPA 回退返回的 HTML 视为没有更新信息，而不是清单错误", async () => {
  const html = "<!doctype html><html><body>app</body></html>";
  assert.equal((await run(html)).result.status, "no_update_info");
  assert.equal((await run("  \n<html>")).result.status, "no_update_info");
  // 以其他字符开头的非 JSON 仍是格式错误
  assert.equal((await run("oops")).result.status, "invalid_manifest");
});

test("超大响应与超时由注入的请求层抛错，结果为失败", async () => {
  for (const message of ["manifest too large", "The operation timed out"]) {
    const result = await checkForUpdate({
      centerURL: CENTER,
      current: "0.1.0",
      fetchText: async () => {
        throw new Error(message);
      },
    });
    assert.equal(result.status, "failed");
  }
});

test("非 https 的中心服务地址不请求", async () => {
  let called = false;
  const result = await checkForUpdate({
    centerURL: "http://center.example.com",
    current: "0.1.0",
    fetchText: async () => {
      called = true;
      return { status: 200, text: "{}" };
    },
  });
  assert.equal(result.status, "unsupported_origin");
  assert.equal(called, false);
});

test("非 https 下载地址被忽略", async () => {
  for (const url of [
    "http://center.example.com/a.dmg",
    "ftp://center.example.com/a.dmg",
    "javascript:alert(1)",
    "file:///etc/passwd",
  ]) {
    const { result } = await run(good({ download_url: url }));
    assert.equal(result.status, "unsafe_manifest", url);
  }
  assert.equal(
    (await run(good({ download_url: "https://u:p@center.example.com/a.dmg" })))
      .result.status,
    "unsafe_manifest",
  );
});

test("跨源下载地址默认被忽略，在显式名单内才放行（含端口、子域不混同）", async () => {
  const cross = good({ download_url: "https://cdn.other.com/a.dmg" });
  assert.equal((await run(cross)).result.status, "unsafe_manifest");
  assert.equal(
    (await run(cross, { allowedHosts: ["cdn.other.com"] })).result.status,
    "update_available",
  );
  // 同主机不同端口属于跨源
  assert.equal(
    (await run(good({ download_url: "https://center.example.com:8443/a.dmg" })))
      .result.status,
    "unsafe_manifest",
  );
  // 名单不做后缀匹配
  assert.equal(
    (
      await run(good({ download_url: "https://evil-cdn.other.com/a.dmg" }), {
        allowedHosts: ["cdn.other.com"],
      })
    ).result.status,
    "unsafe_manifest",
  );
  assert.deepEqual(parseHostList(" A.com, ,b.com "), ["a.com", "b.com"]);
});

test("格式非法的清单被忽略", async () => {
  const cases = [
    "not json",
    "[]",
    "null",
    JSON.stringify(good({ version: "1.0" })),
    JSON.stringify(good({ version: undefined })),
    JSON.stringify(good({ sha256: "xyz" })),
    JSON.stringify(good({ sha256: "a".repeat(63) })),
    JSON.stringify(good({ download_url: 5 })),
    JSON.stringify(good({ download_url: "not a url" })),
    JSON.stringify(good({ notes: 5 })),
    JSON.stringify(good({ min_version: "x" })),
  ];
  for (const body of cases) {
    const { result } = await run(body);
    assert.equal(result.status, "invalid_manifest", body);
  }
});

test("可选字段：notes 截断，sha256 规范化为小写，min_version 强制提示不可跳过", async () => {
  const checked = validateManifest(
    good({ notes: "x".repeat(5000), sha256: "A".repeat(64) }),
    { origin: "https://center.example.com" },
  );
  assert.equal(checked.ok, true);
  assert.equal(checked.manifest.notes.length, 2000);
  assert.equal(checked.manifest.sha256, SHA);
  const { result } = await run(good({ min_version: "0.1.5" }), {
    skippedVersion: "0.2.0",
  });
  assert.equal(result.mandatory, true);
  assert.equal(result.skipped, false);
});

test("跳过此版本：同版本被标记为已跳过，更新的版本仍会提示", async () => {
  assert.equal(
    (await run(good(), { skippedVersion: "0.2.0" })).result.skipped,
    true,
  );
  assert.equal(
    (await run(good({ version: "0.3.0" }), { skippedVersion: "0.2.0" })).result
      .skipped,
    false,
  );
});
