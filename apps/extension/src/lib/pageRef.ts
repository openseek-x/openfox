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
