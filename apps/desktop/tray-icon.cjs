// 托盘图标（占位）：用代码生成的中性几何图形（圆环加圆点），黑色带透明度，
// 配合 setTemplateImage(true) 自动适配 macOS 明暗菜单栏。不是品牌图标，正式图标由用户提供后替换。

const zlib = require("node:zlib");

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

function crc32(buffer) {
  let c = 0xffffffff;
  for (const byte of buffer) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const head = Buffer.alloc(8);
  head.writeUInt32BE(data.length, 0);
  head.write(type, 4, "ascii");
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([head.subarray(4), data])), 0);
  return Buffer.concat([head, data, crc]);
}

/** 把 size x size 的 alpha 通道编码成 RGBA PNG（RGB 固定为黑色）。 */
function encodePng(size, alphaAt) {
  const rows = [];
  for (let y = 0; y < size; y++) {
    const row = Buffer.alloc(1 + size * 4);
    for (let x = 0; x < size; x++) row[1 + x * 4 + 3] = alphaAt(x, y);
    rows.push(row);
  }
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header[8] = 8; // 位深
  header[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", zlib.deflateSync(Buffer.concat(rows))),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

/** 圆环 + 中心圆点，边缘按距离做简单抗锯齿。 */
function shape(size) {
  const center = (size - 1) / 2;
  const outer = size * 0.44;
  const inner = size * 0.32;
  const dot = size * 0.14;
  const coverage = (distance, from, to) =>
    Math.max(0, Math.min(1, Math.min(distance - from, to - distance) + 0.5));
  return (x, y) => {
    const d = Math.hypot(x - center, y - center);
    const ring = coverage(d, inner, outer);
    const point = Math.max(0, Math.min(1, dot - d + 0.5));
    return Math.round(255 * Math.max(ring, point));
  };
}

/** 返回 1x（16px）与 2x（32px）两张 PNG。 */
function trayIconPngs() {
  return { x1: encodePng(16, shape(16)), x2: encodePng(32, shape(32)) };
}

module.exports = { encodePng, trayIconPngs };
