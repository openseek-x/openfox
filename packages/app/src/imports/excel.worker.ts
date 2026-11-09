import { read, utils } from "xlsx";

const maxBytes = 10 * 1024 * 1024;
const maxInflated = 64 * 1024 * 1024;

// Check compressed input before giving the workbook to the spreadsheet parser.
// Stream each ZIP entry with a byte ceiling so forged size fields cannot hide a bomb.
async function checkZip(data: Uint8Array<ArrayBuffer>) {
  const v = new DataView(data.buffer);
  let end = data.length - 22;
  while (
    end >= Math.max(0, data.length - 65557) &&
    v.getUint32(end, true) !== 0x06054b50
  )
    end--;
  if (
    end < 0 ||
    end < data.length - 65557 ||
    v.getUint32(end, true) !== 0x06054b50
  )
    throw Error();
  const count = v.getUint16(end + 10, true);
  if (count > 2048 || v.getUint32(end + 4, true) !== 0) throw Error();
  let pos = v.getUint32(end + 16, true),
    total = 0;
  for (let i = 0; i < count; i++) {
    if (v.getUint32(pos, true) !== 0x02014b50) throw Error();
    const method = v.getUint16(pos + 10, true),
      compressed = v.getUint32(pos + 20, true),
      size = v.getUint32(pos + 24, true),
      local = v.getUint32(pos + 42, true);
    if (
      (total += size) > maxInflated ||
      v.getUint32(local, true) !== 0x04034b50 ||
      (method !== 0 && method !== 8)
    )
      throw Error();
    const start =
      local +
      30 +
      v.getUint16(local + 26, true) +
      v.getUint16(local + 28, true);
    if (
      start + compressed > data.length ||
      (method === 0 && compressed !== size)
    )
      throw Error();
    if (method === 8) {
      const stream = new Blob([data.subarray(start, start + compressed)])
        .stream()
        .pipeThrough(new DecompressionStream("deflate-raw"));
      const r = stream.getReader();
      let actual = 0;
      try {
        for (;;) {
          const part = await r.read();
          if (part.done) break;
          actual += part.value.length;
          if (actual > size) {
            await r.cancel();
            throw Error();
          }
        }
      } finally {
        r.releaseLock();
      }
      if (actual !== size) throw Error();
    }
    pos +=
      46 +
      v.getUint16(pos + 28, true) +
      v.getUint16(pos + 30, true) +
      v.getUint16(pos + 32, true);
  }
  if (pos !== end) throw Error();
}
self.onmessage = async ({ data }: { data: { file: File; sheet?: string } }) => {
  try {
    if (data.file.size > maxBytes) throw Error();
    const bytes = new Uint8Array(await data.file.arrayBuffer());
    if (bytes[0] === 0x50 && bytes[1] === 0x4b) await checkZip(bytes);
    else if (
      !(bytes[0] === 0xd0 && bytes[1] === 0xcf) &&
      !/^\s*(?:<!doctype|<html|<table|<\?xml)/i.test(
        new TextDecoder().decode(bytes.subarray(0, 512)),
      )
    )
      throw Error();
    const book = read(bytes, {
      type: "array",
      bookSheets: data.sheet === undefined,
      sheetRows: 5002,
      sheets: data.sheet,
      cellHTML: false,
      cellFormula: false,
      cellText: true,
      bookVBA: false,
    });
    if (!data.sheet) {
      if (!book.SheetNames.length || book.SheetNames.length > 128)
        throw Error();
      self.postMessage({ sheets: book.SheetNames });
      return;
    }
    const sheet = book.Sheets[data.sheet];
    if (!sheet?.["!ref"]) throw Error();
    const range = utils.decode_range(sheet["!fullref"] || sheet["!ref"]);
    if (range.e.r - range.s.r > 5000 || range.e.c - range.s.c >= 300)
      throw Error();
    const rows: string[] = [];
    let total = 0;
    const ids = new Set<number>();
    for (let row = range.s.r; row <= range.e.r; row++) {
      const values: string[] = [];
      for (let col = range.s.c; col <= range.e.c; col++) {
        const cell = sheet[utils.encode_cell({ r: row, c: col })];
        if (
          cell?.t === "n" &&
          ids.has(col) &&
          (!Number.isSafeInteger(cell.v) || Math.abs(cell.v) >= 1e15)
        ) {
          self.postMessage({ error: "precision" });
          return;
        }
        const s = cell ? String(cell.w ?? cell.v ?? "") : "";
        if (
          row === range.s.r &&
          /^(?:(?:需求|缺陷|任务|工作项)\s*)?id$/i.test(s.trim())
        )
          ids.add(col);
        values.push('"' + s.replaceAll('"', '""') + '"');
      }
      const line = values.join(",");
      total += line.length + 1;
      if (total > maxBytes) throw Error();
      rows.push(line);
    }
    const csv = new Blob([rows.join("\n")], { type: "text/csv" });
    if (csv.size > maxBytes) throw Error();
    self.postMessage({ csv });
  } catch {
    self.postMessage({ error: "invalid" });
  }
};
