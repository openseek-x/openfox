import type { RunArtifact } from "@openfox/api";

import { IMAGE_MAX_BYTES, LARGE_TEXT_BYTES } from "../../market/model";
import { artifactAvailable } from "../steps/cards";

// 阶段 10d · 会话产物的预览规则。
// 中心只把可预览白名单里的 MIME 原样保存（text/markdown、text/csv、text/plain、image/png|jpeg|gif|webp），
// 其余一律 application/octet-stream。仅对已验证的 text/plain .html/.htm 选择隔离静态预览。

export type ArtifactPreviewKind =
  | "office"
  | "html"
  | "markdown"
  | "table"
  | "code"
  | "image"
  | "pdf"
  | "binary";

/** 文本类产物（Markdown / CSV / 文本代码）预览时取回的字节上限；超过只能下载。 */
export const ARTIFACT_TEXT_MAX_BYTES = 5 * 1024 * 1024;
/** 图片预览上限（与市场一致：10 MB），超过只能下载。 */
export const ARTIFACT_IMAGE_MAX_BYTES = IMAGE_MAX_BYTES;
/** 文本超过它时按大文件模式只显示前 500 行（与市场一致）。 */
export const ARTIFACT_LARGE_TEXT_BYTES = LARGE_TEXT_BYTES;

const OFFICE_TYPES = new Set([
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  "application/vnd.openxmlformats-officedocument.presentationml.presentation",
]);
const IMAGE_TYPES = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
]);

export function artifactPreviewKind(
  artifact: Pick<RunArtifact, "mime_type" | "name">,
): ArtifactPreviewKind {
  const mime = artifact.mime_type.split(";")[0]!.trim().toLowerCase();
  if (mime === "text/markdown") return "markdown";
  if (mime === "text/csv") return "table";
  if (mime === "text/plain")
    return /\.html?$/i.test(artifact.name) ? "html" : "code";
  if (OFFICE_TYPES.has(mime)) return "office";
  if (mime === "application/pdf") return "pdf";
  if (IMAGE_TYPES.has(mime)) return "image";
  return "binary";
}

/** 是否在面板里预览（类型可预览且不超过对应上限）。 */
export function artifactPreviewable(artifact: RunArtifact): boolean {
  const kind = artifactPreviewKind(artifact);
  if (kind === "binary") return false;
  const limit =
    kind === "image" || kind === "pdf" || kind === "office"
      ? ARTIFACT_IMAGE_MAX_BYTES
      : ARTIFACT_TEXT_MAX_BYTES;
  return artifact.size_bytes <= limit;
}

/** 已失效（过保留期、已删除或不可用）。 */
export function artifactExpired(artifact: RunArtifact, now = Date.now()) {
  return !artifactAvailable(artifact, now);
}
