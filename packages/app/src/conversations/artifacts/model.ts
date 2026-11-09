import type { RunArtifact } from "@openfox/api";

import { IMAGE_MAX_BYTES, LARGE_TEXT_BYTES } from "../../market/model";
import { artifactAvailable } from "../steps/cards";

// 会话产物的预览规则。
// 中心只把可预览白名单里的 MIME 原样保存（text/markdown、text/csv、text/plain、image/png|jpeg|gif|webp），
// 其余一律 application/octet-stream；这里只按 MIME 判断，不按扩展名猜（与电脑端「两者不符取保守」一致）。

export type ArtifactPreviewKind =
  "markdown" | "table" | "code" | "image" | "binary";

/** 文本类产物（Markdown / CSV / 文本代码）预览时取回的字节上限；超过只能下载。 */
export const ARTIFACT_TEXT_MAX_BYTES = 5 * 1024 * 1024;
/** 图片预览上限（与市场一致：10 MB），超过只能下载。 */
export const ARTIFACT_IMAGE_MAX_BYTES = IMAGE_MAX_BYTES;
/** 文本超过它时按大文件模式只显示前 500 行（与市场一致）。 */
export const ARTIFACT_LARGE_TEXT_BYTES = LARGE_TEXT_BYTES;

const IMAGE_TYPES = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
]);

export function artifactPreviewKind(
  artifact: Pick<RunArtifact, "mime_type">,
): ArtifactPreviewKind {
  const mime = artifact.mime_type.split(";")[0]!.trim().toLowerCase();
  if (mime === "text/markdown") return "markdown";
  if (mime === "text/csv") return "table";
  if (mime === "text/plain") return "code";
  if (IMAGE_TYPES.has(mime)) return "image";
  return "binary";
}

/** 是否在面板里预览（类型可预览且不超过对应上限）。 */
export function artifactPreviewable(artifact: RunArtifact): boolean {
  const kind = artifactPreviewKind(artifact);
  if (kind === "binary") return false;
  const limit =
    kind === "image" ? ARTIFACT_IMAGE_MAX_BYTES : ARTIFACT_TEXT_MAX_BYTES;
  return artifact.size_bytes <= limit;
}

/** 已失效（过保留期、已删除或不可用）。 */
export function artifactExpired(artifact: RunArtifact, now = Date.now()) {
  return !artifactAvailable(artifact, now);
}
