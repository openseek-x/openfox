import {
  ATTACHMENT_MAX_BYTES,
  isApiError,
  type Attachment,
} from "@openfox/api";
import type { Translate } from "@openfox/i18n";

// 附件的纯逻辑：大小格式化、客户端预检查、错误文案。类型白名单与内容嗅探只在服务端判断。

/** 1536 → "1.5 KB"。 */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 ? Math.round(value) : Math.round(value * 10) / 10} ${units[unit]}`;
}

/** 上传前的本地检查：超过单文件上限直接拒绝，省一次请求。 */
export function precheckFile(file: File): "tooLarge" | null {
  return file.size > ATTACHMENT_MAX_BYTES ? "tooLarge" : null;
}

/** 粘贴的图片通常叫 image.png；统一补上时间，避免多张同名。 */
export function namedPastedFile(file: File, label: string, now = new Date()) {
  if (file.name && file.name !== "image.png") return file;
  const stamp = now.toISOString().replace(/[-:T]/g, "").slice(0, 14);
  const ext = file.type.split("/")[1] || "png";
  return new File([file], `${label}-${stamp}.${ext}`, { type: file.type });
}

/** 从剪贴板或拖拽的文件列表里取出文件。 */
export function filesOf(list: FileList | null | undefined): File[] {
  return list ? Array.from(list) : [];
}

export function attachmentErrorMessage(
  t: Translate,
  error: unknown,
  name: string,
): string {
  if (!isApiError(error)) return t("attachments.errors.generic");
  switch (error.code) {
    case "ATTACHMENT_TOO_LARGE":
      return t("attachments.errors.tooLarge", { name });
    case "ATTACHMENT_TYPE_NOT_ALLOWED":
      return t("attachments.errors.typeNotAllowed", { name });
    case "ATTACHMENT_LIMIT":
      return t("attachments.errors.taskLimit");
    case "ATTACHMENT_PENDING_LIMIT":
      return t("attachments.errors.pendingLimit");
    case "STORAGE_UNAVAILABLE":
      return t("attachments.errors.storage");
    default:
      if (error.status === 403) return t("attachments.errors.forbidden");
      if (error.status === 404) return t("attachments.errors.notFound");
      return t("attachments.errors.generic");
  }
}

export type AttachmentKind =
  | "word"
  | "excel"
  | "powerpoint"
  | "pdf"
  | "image"
  | "archive"
  | "text"
  | "file";

/** 按扩展名分类，决定卡片左侧的类型徽标：Word 蓝 W、Excel 绿 X、PDF 红 PDF。 */
export function attachmentKind(name: string): AttachmentKind {
  const ext = name.slice(name.lastIndexOf(".") + 1).toLowerCase();
  if (name.indexOf(".") < 0) return "file";
  if (["doc", "docx"].includes(ext)) return "word";
  if (["xls", "xlsx", "csv", "tsv"].includes(ext)) return "excel";
  if (["ppt", "pptx"].includes(ext)) return "powerpoint";
  if (ext === "pdf") return "pdf";
  if (["png", "jpg", "jpeg", "gif", "webp"].includes(ext)) return "image";
  if (ext === "zip") return "archive";
  if (
    [
      "txt",
      "md",
      "json",
      "log",
      "yaml",
      "yml",
      "toml",
      "ini",
      "xml",
      "html",
      "htm",
      "svg",
    ].includes(ext)
  )
    return "text";
  return "file";
}

/** 徽标上的字母与配色；没有品牌色的类型用中性底色加文件图标（badge 为空）。 */
export const KIND_BADGE: Record<
  AttachmentKind,
  { badge: string; className: string }
> = {
  word: { badge: "W", className: "bg-[#2b579a] text-white" },
  excel: { badge: "X", className: "bg-[#217346] text-white" },
  powerpoint: { badge: "P", className: "bg-[#c43e1c] text-white" },
  pdf: { badge: "PDF", className: "bg-[#d93831] text-white" },
  image: { badge: "", className: "bg-surface-muted text-fg-muted" },
  archive: { badge: "ZIP", className: "bg-[#6b7280] text-white" },
  text: { badge: "", className: "bg-surface-muted text-fg-muted" },
  file: { badge: "", className: "bg-surface-muted text-fg-muted" },
};

/** 评论里的附件链接：用绝对地址，评论按 Markdown 链接渲染。 */
export function attachmentMarkdown(a: Attachment, url: string): string {
  const absolute = new URL(url, window.location.origin).href;
  const label = a.name.replace(/[[\]]/g, "");
  return `[${label}](${absolute})`;
}

/** 评论正文里含有附件链接时才按 Markdown 链接渲染，其余评论保持纯文本。 */
export function hasAttachmentLink(body: string): boolean {
  return /\]\([^)\s]*\/attachments\/[0-9a-f-]{36}[^)\s]*\)/i.test(body);
}
