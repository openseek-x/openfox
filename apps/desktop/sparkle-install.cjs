// Sparkle accepts HTTP(S) enclosures. Serve the already verified DMG on loopback
// so its installer can consume it without a second external download or proxy.
const http = require("node:http");
const { createReadStream } = require("node:fs");
const { pipeline } = require("node:stream");
const { randomUUID } = require("node:crypto");
const { execFile } = require("node:child_process");
const { promisify } = require("node:util");
const path = require("node:path");
const run = promisify(execFile);

async function installWithSparkle(bundle, { file, size, version, signature }) {
  const prefix = "/" + randomUUID();
  const feed = prefix + "/appcast.xml";
  const dmg = prefix + "/OpenFox.dmg";
  // version and signature have already passed strict syntax and signature checks.
  const xml = `<rss version="2.0" xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle"><channel><title>OpenFox</title><item><sparkle:version>${version}</sparkle:version><enclosure url="OpenFox.dmg" length="${size}" type="application/octet-stream" sparkle:edSignature="${signature}"/></item></channel></rss>`;
  const server = http.createServer((req, res) => {
    if (req.method === "GET" && req.url === feed) {
      res.writeHead(200, { "Content-Type": "application/xml" }).end(xml);
    } else if (req.method === "GET" && req.url === dmg) {
      res.writeHead(200, {
        "Content-Type": "application/octet-stream",
        "Content-Length": size,
      });
      pipeline(createReadStream(file), res, () => {});
    } else {
      res.writeHead(404).end();
    }
  });
  try {
    await new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(0, "127.0.0.1", resolve);
    });
    const helper = path.join(
      bundle,
      "Contents/Helpers/OpenFox Update.app/Contents/MacOS/OpenFoxUpdate",
    );
    await run(helper, [
      bundle,
      "--feed-url",
      `http://127.0.0.1:${server.address().port}${feed}`,
      "--check-immediately",
      "--grant-automatic-checks",
      "--user-agent-name",
      "OpenFox",
    ]);
  } finally {
    server.close();
    server.closeAllConnections();
  }
}

module.exports = { installWithSparkle };
