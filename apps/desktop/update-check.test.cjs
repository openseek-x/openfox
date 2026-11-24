const { test } = require("node:test");
const assert = require("node:assert/strict");
const {
  checkForUpdate,
  compareVersions,
  manifestURL,
} = require("./update-check.cjs");
const CENTER = "https://center.example.com/some/page";
const SHA = "a".repeat(64);
const repo = "https://github.com/openseek-x/openfox";
const asset = (suffix) => {
  const name = `OpenFox-0.2.0-arm64.${suffix}`;
  return {
    name,
    browser_download_url: `${repo}/releases/download/v0.2.0/${name}`,
    state: "uploaded",
    digest: `sha256:${SHA}`,
  };
};
const release = () => ({
  tag_name: "v0.2.0",
  draft: false,
  prerelease: false,
  body: "修复若干问题",
  assets: [asset("pkg"), asset("dmg")],
});
const run = (body, options = {}, status = 200) =>
  checkForUpdate({
    current: "0.1.3",
    systemInstaller: true,
    ...options,
    fetchText: async (url, limits) => {
      assert.equal(
        url,
        "https://api.github.com/repos/openseek-x/openfox/releases/latest",
      );
      assert.equal(limits.timeoutMs, 10000);
      assert.equal(limits.maxBytes, 128 * 1024);
      return { status, text: JSON.stringify(body) };
    },
  });
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

test("系统安装器选择 PKG，手动下载选择同一标签的 DMG", async () => {
  for (const [systemInstaller, ext] of [
    [true, "pkg"],
    [false, "dmg"],
  ]) {
    const result = await run(release(), {
      systemInstaller,
      skippedVersion: "0.2.0",
    });
    assert.equal(result.status, "update_available");
    assert.equal(result.manifest.download_url, asset(ext).browser_download_url);
    assert.equal(result.manifest.sha256, SHA);
    assert.equal(result.skipped, true);
  }
});

test("等于或高于 GitHub 最新正式标签时不下载", async () => {
  for (const current of ["0.2.0", "1.0.0"])
    assert.deepEqual(await run(release(), { current }), {
      status: "up_to_date",
      version: "0.2.0",
    });
});

test("拒绝草稿、预发布、非正式标签、缺失附件或缺失摘要", async () => {
  for (const fields of [
    { draft: true },
    { prerelease: true },
    { tag_name: "v0.2.0-r2" },
    { tag_name: "v00.2.0" },
    { assets: [] },
    { assets: [{ ...asset("pkg"), digest: null }] },
    { assets: [{ ...asset("pkg"), state: "starter" }] },
  ]) {
    assert.equal(
      (await run({ ...release(), ...fields })).status,
      "invalid_manifest",
    );
  }
});

test("下载 URL 必须精确指向该仓库与标签", async () => {
  for (const from of [
    repo.replace("openfox", "other"),
    repo + "/extra",
    "https://another.test",
  ]) {
    const pkg = asset("pkg");
    pkg.browser_download_url = pkg.browser_download_url.replace(repo, from);
    assert.equal(
      (await run({ ...release(), assets: [pkg] })).status,
      "unsafe_manifest",
    );
  }
});
test("没有 Release、限流、非法 JSON 及超时", async () => {
  assert.equal((await run({}, {}, 404)).status, "no_update_info");
  assert.equal((await run({}, {}, 403)).status, "failed");
  assert.equal(
    (
      await checkForUpdate({
        fetchText: async () => ({ status: 200, text: "<html>" }),
      })
    ).status,
    "invalid_manifest",
  );
  assert.equal(
    (
      await checkForUpdate({
        fetchText: async () => {
          throw new Error("timeout");
        },
      })
    ).status,
    "failed",
  );
});
