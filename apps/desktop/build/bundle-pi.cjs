// 打包时将校验过的上游发行目录写入 app，再由桌面签名流程处理。
const { execFileSync } = require("node:child_process");
const { createHash } = require("node:crypto");
const { createReadStream } = require("node:fs");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { Arch } = require("electron-builder");

const release = "https://github.com/earendil-works/pi/releases/download/v1.5.0";
const archiveName = "pi-darwin-arm64.tar.gz";
const sha256 =
  "a9bf0d82d58d2a7d337f4ae4171b5fc9ebffe42509527a831364c4b65adf2cdf";

module.exports = async function bundlePi(context) {
  if (context.electronPlatformName !== "darwin" || context.arch !== Arch.arm64)
    throw new Error("Bundled Pi requires the macOS arm64 desktop target");
  const resources = path.join(
    context.appOutDir,
    context.packager.appInfo.productFilename + ".app",
    "Contents",
    "Resources",
  );
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "openfox-pi-"));
  try {
    const archive = path.join(tmp, archiveName);
    if (process.env.OPENFOX_PI_ARCHIVE) {
      await fs.copyFile(process.env.OPENFOX_PI_ARCHIVE, archive);
    } else {
      execFileSync("curl", [
        "--fail",
        "--silent",
        "--show-error",
        "--location",
        "--proto",
        "=https",
        "--retry",
        "2",
        "--max-time",
        "240",
        "--output",
        archive,
        release + "/" + archiveName,
      ]);
    }
    const hash = createHash("sha256");
    for await (const chunk of createReadStream(archive)) hash.update(chunk);
    if (hash.digest("hex") !== sha256)
      throw new Error("Bundled Pi archive SHA-256 mismatch");
    execFileSync("tar", ["-xzf", archive, "-C", resources]);
    await fs.copyFile(
      path.join(__dirname, "pi-LICENSE"),
      path.join(resources, "pi", "LICENSE"),
    );
  } finally {
    await fs.rm(tmp, { recursive: true, force: true });
  }
};
