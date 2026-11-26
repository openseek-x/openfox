// 页面引用：用户主动勾选后，把当前页的标题、地址与选中文本以文本形式拼进消息正文（决策 D4）。
// 地址只保留来源与路径，去掉查询串、片段和账号信息，避免把链接里的凭据带给 Agent。

export interface PageReference {
  title: string;
  url: string;
  selection: string;
}

export const MAX_SELECTION_CHARS = 8000;
const MAX_TITLE_CHARS = 200;

/** 只接受 http(s) 页面，返回不含查询串与片段的地址；其他协议（chrome://、file:// 等）返回 null。 */
export function sanitizePageUrl(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") return null;
  return `${url.origin}${url.pathname}`;
}

function clip(text: string, max: number): string {
  const chars = Array.from(text);
  return chars.length > max
    ? `${chars.slice(0, max).join("")}…（已截断）`
    : text;
}

/** 生成最终发送的正文；没有引用或页面不可引用时，正文就是用户输入本身。 */
export function buildMessageBody(
  text: string,
  reference: PageReference | null,
): string {
  const message = text.trim();
  if (!reference) return message;
  const url = sanitizePageUrl(reference.url);
  if (!url) return message;
  const title = clip(
    reference.title.replace(/\s+/g, " ").trim(),
    MAX_TITLE_CHARS,
  );
  const lines = [
    message,
    "",
    "---",
    "引用的页面：",
    `标题：${title}`,
    `地址：${url}`,
  ];
  const selection = reference.selection.trim();
  if (selection) {
    lines.push("选中文本：");
    for (const line of clip(selection, MAX_SELECTION_CHARS).split(/\r?\n/)) {
      lines.push(`> ${line}`);
    }
  }
  return lines.join("\n");
}

/** 仅识别扩展生成的完整引用后缀。其他文字按原样显示。 */
export function splitPageReference(body: string): {
  text: string;
  reference: PageReference | null;
} {
  const marker = "\n\n---\n引用的页面：\n";
  const start = body.lastIndexOf(marker);
  const plain = { text: body, reference: null };
  if (start < 0) return plain;
  const [titleLine, urlLine, selectionLabel, ...lines] = body
    .slice(start + marker.length)
    .split("\n");
  if (!titleLine?.startsWith("标题：") || !urlLine?.startsWith("地址："))
    return plain;
  const url = urlLine.slice("地址：".length);
  if (sanitizePageUrl(url) !== url) return plain;
  if (
    selectionLabel !== undefined &&
    (selectionLabel !== "选中文本：" ||
      !lines.length ||
      lines.some((line) => !line.startsWith("> ")))
  )
    return plain;
  return {
    text: body.slice(0, start),
    reference: {
      title: titleLine.slice("标题：".length),
      url,
      selection: lines.map((line) => line.slice(2)).join("\n"),
    },
  };
}
