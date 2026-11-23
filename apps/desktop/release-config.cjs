// macOS arm64 release package for the system Installer.
const { execFileSync } = require("node:child_process");
const { createHash } = require("node:crypto");
const { createReadStream } = require("node:fs");
const fs = require("node:fs/promises");
const path = require("node:path");
const { build, version } = require("./package.json");
const { manifestURL } = require("./update-check.cjs");

const feed = manifestURL(process.env.OPENFOX_DESKTOP_DEFAULT_WEB_URL);

if (!feed)
  throw new Error(
    "Desktop releases require an HTTPS OPENFOX_DESKTOP_DEFAULT_WEB_URL",
  );
if (!/^\d+\.\d+\.\d+$/.test(version))
  throw new Error(
    "Desktop releases require a numeric major.minor.patch version",
  );

module.exports = {
  ...build,
  extraMetadata: { openfoxAutoUpdate: true },
  directories: { ...build.directories, output: "dist/release" },
  mac: { ...build.mac, target: ["dir"], notarize: false },
  async afterAllArtifactBuild({ outDir }) {
    const filename = `OpenFox-${version}-arm64.pkg`;
    const pkg = path.join(outDir, filename);
    const bundle = path.join(outDir, "mac-arm64", "OpenFox.app");
    execFileSync("/usr/bin/codesign", [
      "--verify",
      "--deep",
      "--strict",
      bundle,
    ]);
    execFileSync("/usr/bin/productbuild", [
      "--component",
      bundle,
      "/Applications",
      pkg,
    ]);
    const hash = createHash("sha256");
    for await (const bytes of createReadStream(pkg)) hash.update(bytes);
    const manifest = {
      version,
      download_url: new URL(filename, feed).href,
      sha256: hash.digest("hex"),
    };
    const output = path.join(outDir, "latest.json");
    await fs.writeFile(output, JSON.stringify(manifest, null, 2) + "\n");
    return [pkg, output];
  },
};
