// 自带 CLI 安装与 PATH 配置单测：node --test（全部在临时 HOME 里进行）。
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { describe, it } = require("node:test");

const {
  MARKER,
  ensureOnPath,
  installBundledCli,
  installPaths,
  pathSnippet,
} = require("./cli.cjs");

async function tempHome() {
  return fs.mkdtemp(path.join(os.tmpdir(), "openfox-cli-"));
}

async function writeBundled(dir, content) {
  const file = path.join(dir, "bundle", "openfox");
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, content, { mode: 0o755 });
  return file;
}

describe("安装自带 CLI", () => {
  it("首次安装、同版本跳过、新版本原子替换，权限 755", async () => {
    const home = await tempHome();
    try {
      const { target, record } = installPaths(home);
      const bundled = await writeBundled(home, "v1");
      assert.deepEqual(await installBundledCli({ bundled, home }), {
        path: target,
        state: "installed",
      });
      assert.equal(await fs.readFile(target, "utf8"), "v1");
      assert.equal((await fs.stat(target)).mode & 0o777, 0o755);
      assert.equal((await fs.stat(record)).mode & 0o777, 0o600);

      assert.equal(
        (await installBundledCli({ bundled, home })).state,
        "unchanged",
      );

      await fs.writeFile(bundled, "v2");
      assert.equal(
        (await installBundledCli({ bundled, home })).state,
        "installed",
      );
      assert.equal(await fs.readFile(target, "utf8"), "v2");
      // 没有遗留临时文件
      assert.deepEqual(await fs.readdir(path.dirname(target)), ["openfox"]);
    } finally {
      await fs.rm(home, { recursive: true, force: true });
    }
  });

  it("目标是别人装的文件时不覆盖，改用安装包里的路径", async () => {
    const home = await tempHome();
    try {
      const { target } = installPaths(home);
      await fs.mkdir(path.dirname(target), { recursive: true });
      await fs.writeFile(target, "someone else", { mode: 0o755 });
      const bundled = await writeBundled(home, "v1");
      assert.deepEqual(await installBundledCli({ bundled, home }), {
        path: bundled,
        state: "foreign",
      });
      assert.equal(await fs.readFile(target, "utf8"), "someone else");
    } finally {
      await fs.rm(home, { recursive: true, force: true });
    }
  });

  it("安装包里没有 CLI 时报 bundled_missing", async () => {
    const home = await tempHome();
    try {
      await assert.rejects(
        installBundledCli({ bundled: path.join(home, "missing"), home }),
        (error) => error.code === "bundled_missing",
      );
    } finally {
      await fs.rm(home, { recursive: true, force: true });
    }
  });
});

describe("终端 PATH", () => {
  it("只改已存在的配置并创建 .zprofile；重复执行不重复追加；不写符号链接", async () => {
    const home = await tempHome();
    try {
      const { binDir } = installPaths(home);
      await fs.writeFile(path.join(home, ".zshrc"), "export A=1\n");
      await fs.writeFile(path.join(home, "real-bashrc"), "export B=1\n");
      await fs.symlink(
        path.join(home, "real-bashrc"),
        path.join(home, ".bashrc"),
      );

      assert.deepEqual(await ensureOnPath({ home, binDir }), [
        ".zprofile",
        ".zshrc",
      ]);
      const zshrc = await fs.readFile(path.join(home, ".zshrc"), "utf8");
      assert.ok(zshrc.startsWith("export A=1\n"));
      assert.equal(zshrc.split(MARKER).length, 2);
      assert.equal(
        await fs.readFile(path.join(home, "real-bashrc"), "utf8"),
        "export B=1\n",
      );
      await assert.rejects(fs.stat(path.join(home, ".profile")));

      assert.deepEqual(await ensureOnPath({ home, binDir }), []);
      assert.equal(
        (await fs.readFile(path.join(home, ".zshrc"), "utf8")).split(MARKER)
          .length,
        2,
      );
    } finally {
      await fs.rm(home, { recursive: true, force: true });
    }
  });

  it("脚本只在目录存在且不在 PATH 时加入，并转义特殊字符", () => {
    const snippet = pathSnippet('/Users/a"b/$x/.local/bin');
    assert.match(snippet, /if \[ -d "\/Users\/a\\"b\/\\\$x\/\.local\/bin" \]/);
    assert.match(snippet, /case ":\$PATH:" in/);
    assert.match(
      snippet,
      /export PATH="\/Users\/a\\"b\/\\\$x\/\.local\/bin:\$PATH"/,
    );
  });
});
