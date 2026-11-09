// 表格（.csv）的解析与生成。只处理引号、逗号与换行，不依赖第三方库。

/** 解析 CSV 文本为二维数组。支持双引号包裹（内部 "" 表示引号）、字段内的逗号与换行、CRLF / LF。 */
export function parseCsv(text: string): string[][] {
  const source = text.startsWith("﻿") ? text.slice(1) : text;
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  let i = 0;
  while (i < source.length) {
    const ch = source[i]!;
    if (quoted) {
      if (ch === '"') {
        if (source[i + 1] === '"') {
          field += '"';
          i += 2;
          continue;
        }
        quoted = false;
        i++;
        continue;
      }
      field += ch;
      i++;
      continue;
    }
    if (ch === '"') {
      quoted = true;
      i++;
    } else if (ch === ",") {
      row.push(field);
      field = "";
      i++;
    } else if (ch === "\r" || ch === "\n") {
      row.push(field);
      rows.push(row);
      row = [];
      field = "";
      // CRLF 视为一个换行
      i += ch === "\r" && source[i + 1] === "\n" ? 2 : 1;
    } else {
      field += ch;
      i++;
    }
  }
  // 末尾没有换行时，最后一行仍需收尾；空文本不产生任何行
  if (field !== "" || row.length > 0) {
    row.push(field);
    rows.push(row);
  }
  return rows;
}

/** 字段含逗号、引号或换行时加引号，内部引号加倍。 */
function escapeField(value: string): string {
  return /[",\r\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value;
}

/** 生成 CSV 文本，行之间以 LF 分隔，末尾不带换行。 */
export function serializeCsv(rows: readonly (readonly string[])[]): string {
  return rows.map((row) => row.map(escapeField).join(",")).join("\n");
}
