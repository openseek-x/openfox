// OpenFox's bounded public-API adapter. No SDK implementation is copied here.
import https from 'node:https';
import {pathToFileURL, fileURLToPath} from 'node:url';
import {rmSync} from 'node:fs';
import {dirname, basename, resolve} from 'node:path';

// The Go parent creates this private directory and passes no other writable
// path. EOF also runs when that parent is SIGKILLed, when Go's defer cannot run.
const helperHome = dirname(fileURLToPath(import.meta.url));
process.on('exit', () => {
  if (helperHome === resolve(process.env.HOME ?? '') && basename(helperHome).startsWith('openfox-qq-helper-')) {
    try { rmSync(helperHome, {recursive: true, force: true}); } catch { /* parent also cleans on normal exit */ }
  }
});
process.stdout.on('error', () => process.exit(2));

const sdkRoot = process.argv[2];
const requests = new Set();
const controller = new AbortController();
let finished = false;
let stop;
let requestCount = 0;
let bytesWritten = 0;
const maxOutput = 1 << 20;
const sessionTimer = setTimeout(() => finish('failure'), 600_000);

function emit(value, done) {
  const line = JSON.stringify(value) + '\n';
  bytesWritten += Buffer.byteLength(line);
  if (bytesWritten > maxOutput) process.exit(3);
  process.stdout.write(line, done);
}
function finish(kind, extra = {}) {
  if (finished) return;
  finished = true;
  clearTimeout(sessionTimer);
  controller.abort();
  stop?.();
  for (const req of requests) req.destroy();
  emit({kind, ...extra}, () => process.exit(kind === 'success' ? 0 : 2));
}
process.stdin.resume();
process.stdin.on('end', () => finish('cancelled'));
process.stdin.on('error', () => finish('cancelled'));
// Only EOF is a control signal; never interpret a model/user-supplied command.
process.stdin.on('data', () => finish('failure'));
process.on('SIGTERM', () => finish('cancelled'));
process.on('SIGINT', () => finish('cancelled'));
process.on('uncaughtException', () => finish('failure'));
process.on('unhandledRejection', () => finish('failure'));

// The fixed SDK uses node:https. Bound that transport while leaving all QR
// protocol details to its public startQrConnect implementation. No HTTP proxy,
// alternate host, redirect, TLS override or caller header is accepted here.
const request = https.request.bind(https);
https.request = function (options, callback) {
  if (finished || ++requestCount > 360 || !options || options.hostname !== 'q.qq.com' ||
      (options.port !== undefined && String(options.port) !== '443') ||
      options.method !== 'POST' || options.agent || options.createConnection ||
      options.rejectUnauthorized === false || options.servername || options.auth) {
    finish('failure');
    throw new Error('connector request rejected');
  }
  const req = request({...options, timeout: 10_000, maxHeaderSize: 65536}, response => {
    let size = 0;
    response.on('data', chunk => {
      size += chunk.length;
      if (size > 1 << 20) { response.destroy(); finish('failure'); }
    });
    response.on('error', () => finish('failure'));
    callback(response);
  });
  requests.add(req);
  req.once('close', () => requests.delete(req));
  const end = req.end.bind(req);
  req.end = function (body, ...args) {
    if (body !== undefined && Buffer.byteLength(body) > 4096) {
      req.destroy(); finish('failure'); return req;
    }
    return end(body, ...args);
  };
  return req;
};

try {
  const {startQrConnect} = await import(pathToFileURL(sdkRoot + '/connector/dist/esm/index.js').href);
  stop = startQrConnect({
    onQrDisplayed(url) {
      if (finished) return;
      const parsed = new URL(url);
      if (Buffer.byteLength(url) > 8192 || parsed.origin !== 'https://q.qq.com' ||
          parsed.pathname !== '/qqbot/openclaw/connect.html' || parsed.username || parsed.password || parsed.hash) {
        finish('failure'); return;
      }
      emit({kind: 'qr', url});
    },
    onQrExpired() { if (!finished) emit({kind: 'expired'}); },
    onSuccess(credentials) {
      if (finished) return;
      if (!Array.isArray(credentials) || credentials.length !== 1 ||
          typeof credentials[0].appId !== 'string' || typeof credentials[0].appSecret !== 'string') {
        finish('failure'); return;
      }
      // Optional userOpenid has no established cross-scope identity meaning.
      finish('success', {app_id: credentials[0].appId, app_secret: credentials[0].appSecret});
    },
    onFailure() { finish('failure'); }
  }, {displayQrCodeToConsole: false, signal: controller.signal, source: 'openfox'});
} catch {
  finish('failure');
}
