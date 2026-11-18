// 代码高亮的语言识别（纯函数，不加载高亮库）。返回 highlight.js 的语言名或别名；
// 是否真的支持由 highlight.ts 里的 lowlight 判断，不支持的语言按纯文本显示。

/** 后缀与 highlight.js 语言名不一致的映射；其余后缀（ts、py、go、rs、yaml…）本身就是别名，原样返回。 */
const EXT_LANGUAGES: Record<string, string> = {
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  pyi: "python",
  ipynb: "json",
  jsonc: "json",
  json5: "json",
  zsh: "bash",
  fish: "bash",
  kts: "kotlin",
  hpp: "cpp",
  cc: "cpp",
  hh: "cpp",
  vue: "xml",
  svelte: "xml",
  astro: "xml",
  htm: "html",
  svg: "xml",
  plist: "xml",
  cfg: "ini",
  conf: "ini",
  properties: "ini",
  editorconfig: "ini",
  env: "bash",
  mdx: "markdown",
  txt: "plaintext",
  log: "plaintext",
};

/** 特殊文件名（不区分大小写）。 */
const NAME_LANGUAGES: Record<string, string> = {
  makefile: "makefile",
  gnumakefile: "makefile",
  dockerfile: "dockerfile",
  ".gitignore": "bash",
  ".npmrc": "ini",
  ".editorconfig": "ini",
  license: "plaintext",
  changelog: "plaintext",
  notice: "plaintext",
};

/** 按文件路径推断语言；无法推断时返回 null。 */
export function languageForPath(path: string): string | null {
  const name = path.slice(path.lastIndexOf("/") + 1).toLowerCase();
  if (NAME_LANGUAGES[name]) return NAME_LANGUAGES[name];
  if (name === ".env" || name.startsWith(".env.")) return "bash";
  const dot = name.lastIndexOf(".");
  if (dot <= 0) return null;
  const ext = name.slice(dot + 1);
  return EXT_LANGUAGES[ext] ?? ext;
}

/** Markdown 围栏上的语言标记（```ts、```Python {title} 等）；没有时返回 null。 */
export function languageForFence(
  info: string | null | undefined,
): string | null {
  const word = (info ?? "").trim().split(/\s+/)[0]?.toLowerCase() ?? "";
  if (!word || !/^[a-z0-9_+#.-]{1,32}$/.test(word)) return null;
  return EXT_LANGUAGES[word] ?? word;
}

/** 代码块顶栏显示的语言名（键为 languageForFence 的结果或常见别名）。 */
const LANGUAGE_LABELS: Record<string, string> = {
  typescript: "TypeScript",
  ts: "TypeScript",
  javascript: "JavaScript",
  js: "JavaScript",
  go: "Go",
  golang: "Go",
  python: "Python",
  py: "Python",
  rust: "Rust",
  rs: "Rust",
  java: "Java",
  kotlin: "Kotlin",
  kt: "Kotlin",
  swift: "Swift",
  c: "C",
  cpp: "C++",
  "c++": "C++",
  csharp: "C#",
  cs: "C#",
  "c#": "C#",
  ruby: "Ruby",
  rb: "Ruby",
  php: "PHP",
  bash: "Shell",
  sh: "Shell",
  shell: "Shell",
  console: "Shell",
  powershell: "PowerShell",
  ps1: "PowerShell",
  sql: "SQL",
  json: "JSON",
  yaml: "YAML",
  yml: "YAML",
  toml: "TOML",
  ini: "INI",
  xml: "XML",
  html: "HTML",
  css: "CSS",
  scss: "SCSS",
  markdown: "Markdown",
  md: "Markdown",
  dockerfile: "Dockerfile",
  makefile: "Makefile",
  diff: "Diff",
  graphql: "GraphQL",
  proto: "Protobuf",
  protobuf: "Protobuf",
  lua: "Lua",
  dart: "Dart",
};

/** 围栏语言的显示名：常见语言用规范写法，其余原样显示；没有语言时返回 null。 */
export function languageLabel(info: string | null | undefined): string | null {
  const language = languageForFence(info);
  if (!language) return null;
  return LANGUAGE_LABELS[language] ?? info!.trim().split(/\s+/)[0]!;
}
