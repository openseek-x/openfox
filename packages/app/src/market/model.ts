import type { Locale } from "@openfox/i18n";

// 市场的纯函数与常量：多语言取值、文件类型判定、frontmatter / CSV 解析、.env 遮挡。

/** 市场列表一页的条数 */
export const MARKET_PAGE_SIZE = 30;
/** 推荐区一屏展示的精选包数 */
export const PACKS_PER_PAGE = 3;
/** CSV 预览的行数上限 */
export const CSV_MAX_ROWS = 100;
/** 文本过大时只渲染的行数（服务端超过 1 MB 会截断） */
export const LARGE_MAX_LINES = 500;
export const LARGE_TEXT_BYTES = 1024 * 1024;
/** 图片预览的大小上限，超过按二进制处理 */
export const IMAGE_MAX_BYTES = 10 * 1024 * 1024;
/** 搜索输入的防抖 */
export const SEARCH_DEBOUNCE_MS = 300;

export type MarketSort = "default" | "downloads" | "newest" | "name";
export const MARKET_SORTS: readonly MarketSort[] = [
  "default",
  "downloads",
  "newest",
  "name",
];

/** 多语言字段：按界面语言取，缺失时取另一种，再缺失取任意一个。 */
export function pickText(
  text: Record<string, string> | null | undefined,
  locale: Locale,
): string {
  if (!text) return "";
  const order =
    locale === "en-US" ? ["en", "zh-Hans", "zh"] : ["zh-Hans", "zh", "en"];
  for (const key of order) {
    const value = text[key];
    if (value && value.trim()) return value;
  }
  return Object.values(text).find((value) => value && value.trim()) ?? "";
}

/** 首字母块的字符：名称第一个可见字符，大写。 */
export function initialOf(name: string): string {
  const first = Array.from(name.trim())[0];
  return first ? first.toUpperCase() : "?";
}

/** 下载量等计数的简写：1234 → 1.2k。 */
export function compactCount(value: number): string {
  if (value < 1000) return String(value);
  if (value < 10000) return `${(value / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  if (value < 1_000_000) return `${Math.round(value / 1000)}k`;
  return `${(value / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`;
}

export type PreviewKind =
  "markdown" | "code" | "table" | "image" | "binary" | "unknown";

const MARKDOWN_EXT = new Set(["md", "mdx", "markdown", "rst", "adoc", "org"]);
const TABLE_EXT = new Set(["csv", "tsv"]);
const IMAGE_EXT = new Set([
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "avif",
  "bmp",
  "ico",
  "svg",
]);
const BINARY_EXT = new Set([
  "zip",
  "tar",
  "gz",
  "tgz",
  "7z",
  "rar",
  "jar",
  "war",
  "class",
  "pdf",
  "doc",
  "docx",
  "xls",
  "xlsx",
  "ppt",
  "pptx",
  "exe",
  "dll",
  "so",
  "dylib",
  "wasm",
  "bin",
  "mp3",
  "mp4",
  "mov",
  "ttf",
  "otf",
  "woff",
  "woff2",
]);

/** 后缀 → 语言标签（代码与配置共用）。未列出的代码后缀显示大写后缀。 */
const LANGUAGES: Record<string, string> = {
  java: "Java",
  kt: "Kotlin",
  kts: "Kotlin",
  scala: "Scala",
  groovy: "Groovy",
  py: "Python",
  pyi: "Python",
  ipynb: "JSON",
  js: "JavaScript",
  mjs: "JavaScript",
  cjs: "JavaScript",
  jsx: "JavaScript",
  ts: "TypeScript",
  tsx: "TypeScript",
  go: "Go",
  rs: "Rust",
  c: "C",
  h: "C",
  cpp: "C++",
  hpp: "C++",
  cc: "C++",
  cs: "C#",
  swift: "Swift",
  m: "Objective-C",
  mm: "Objective-C",
  rb: "Ruby",
  php: "PHP",
  lua: "Lua",
  r: "R",
  dart: "Dart",
  pl: "Perl",
  ex: "Elixir",
  exs: "Elixir",
  erl: "Erlang",
  hs: "Haskell",
  clj: "Clojure",
  sh: "Shell",
  bash: "Shell",
  zsh: "Shell",
  fish: "Shell",
  ps1: "PowerShell",
  bat: "Batch",
  cmd: "Batch",
  sql: "SQL",
  graphql: "GraphQL",
  proto: "Protobuf",
  vue: "Vue",
  svelte: "Svelte",
  astro: "Astro",
  html: "HTML",
  htm: "HTML",
  css: "CSS",
  scss: "SCSS",
  sass: "Sass",
  less: "Less",
  yaml: "YAML",
  yml: "YAML",
  json: "JSON",
  jsonc: "JSON",
  json5: "JSON5",
  toml: "TOML",
  ini: "INI",
  cfg: "INI",
  conf: "Config",
  properties: "Properties",
  xml: "XML",
  plist: "Plist",
  env: "Env",
  gradle: "Gradle",
  lock: "Lock",
  editorconfig: "EditorConfig",
  md: "Markdown",
  mdx: "MDX",
  markdown: "Markdown",
  rst: "reStructuredText",
  adoc: "AsciiDoc",
  org: "Org",
  txt: "Text",
  log: "Log",
  csv: "CSV",
  tsv: "TSV",
  svg: "SVG",
};
const SPECIAL_NAMES: Record<string, string> = {
  dockerfile: "Dockerfile",
  makefile: "Makefile",
  procfile: "Procfile",
  ".gitignore": "Git",
  ".npmrc": "npm",
  license: "Text",
  changelog: "Text",
  notice: "Text",
};

function baseName(path: string): string {
  return path.slice(path.lastIndexOf("/") + 1);
}

function extOf(path: string): string {
  const name = baseName(path).toLowerCase();
  const dot = name.lastIndexOf(".");
  return dot <= 0 ? "" : name.slice(dot + 1);
}

/** .env、.env.local 等：值需要遮挡。 */
export function isEnvFile(path: string): boolean {
  const name = baseName(path).toLowerCase();
  return name === ".env" || name.startsWith(".env.") || extOf(path) === "env";
}

/** 按路径判定预览模式；未知后缀交给服务端返回的 binary 标记（kind = unknown）。 */
export function previewKind(path: string): PreviewKind {
  const ext = extOf(path);
  const name = baseName(path).toLowerCase();
  if (MARKDOWN_EXT.has(ext)) return "markdown";
  if (TABLE_EXT.has(ext)) return "table";
  if (IMAGE_EXT.has(ext)) return "image";
  if (BINARY_EXT.has(ext)) return "binary";
  if (isEnvFile(path) || name in SPECIAL_NAMES || ext in LANGUAGES)
    return "code";
  return "unknown";
}

/** 语言标签：特殊文件名优先，其次后缀映射，再退回大写后缀，无后缀显示 Text。 */
export function languageLabel(path: string): string {
  const name = baseName(path).toLowerCase();
  if (isEnvFile(path)) return "Env";
  const special = SPECIAL_NAMES[name];
  if (special) return special;
  const ext = extOf(path);
  if (!ext) return "Text";
  return LANGUAGES[ext] ?? ext.toUpperCase();
}

export function isSvg(path: string): boolean {
  return extOf(path) === "svg";
}

export function lineCount(text: string): number {
  if (!text) return 0;
  const lines = text.split("\n").length;
  return text.endsWith("\n") ? lines - 1 : lines;
}

export interface Frontmatter {
  entries: Array<[string, string]>;
  body: string;
}

/** 拆出 Markdown 开头的 frontmatter（--- 包住的 key: value 行）；没有或解析不出键值时 entries 为空。 */
export function splitFrontmatter(source: string): Frontmatter {
  const text = source.replace(/^﻿/, "").replace(/\r\n/g, "\n");
  const match = /^---\n([\s\S]*?)\n---[ \t]*(?:\n|$)/.exec(text);
  if (!match) return { entries: [], body: text };
  const entries: Array<[string, string]> = [];
  for (const line of match[1]!.split("\n")) {
    const pair = /^([A-Za-z0-9_.-]+):\s*(.*)$/.exec(line);
    if (!pair) continue;
    let value = pair[2]!.trim();
    if (
      value.length >= 2 &&
      ((value.startsWith('"') && value.endsWith('"')) ||
        (value.startsWith("'") && value.endsWith("'")))
    )
      value = value.slice(1, -1);
    entries.push([pair[1]!, value]);
  }
  if (entries.length === 0) return { entries: [], body: text };
  return { entries, body: text.slice(match[0].length) };
}

/** 解析 CSV / TSV（支持引号与转义引号）；引号不闭合时返回 null，由调用方退回 Code。 */
export function parseCsv(text: string, delimiter: string): string[][] | null {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  const source = text.replace(/^﻿/, "");
  for (let i = 0; i < source.length; i++) {
    const ch = source[i]!;
    if (quoted) {
      if (ch === '"') {
        if (source[i + 1] === '"') {
          field += '"';
          i++;
        } else quoted = false;
      } else field += ch;
    } else if (ch === '"' && field === "") quoted = true;
    else if (ch === delimiter) {
      row.push(field);
      field = "";
    } else if (ch === "\n" || ch === "\r") {
      if (ch === "\r" && source[i + 1] === "\n") i++;
      row.push(field);
      rows.push(row);
      row = [];
      field = "";
    } else field += ch;
  }
  if (quoted) return null;
  if (field !== "" || row.length > 0) {
    row.push(field);
    rows.push(row);
  }
  return rows.length > 0 ? rows : null;
}

/** .env 一行：KEY=VALUE 拆成 [前缀含等号, 值]；注释与空行没有值。 */
export function splitEnvLine(line: string): [string, string] | null {
  const match = /^(\s*(?:export\s+)?[A-Za-z_][A-Za-z0-9_.-]*\s*=)(.*)$/.exec(
    line,
  );
  if (!match || !match[2]!.trim()) return null;
  return [match[1]!, match[2]!];
}
