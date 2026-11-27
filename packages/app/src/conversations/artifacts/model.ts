import type { RunArtifact } from "@openfox/api";

import { IMAGE_MAX_BYTES, LARGE_TEXT_BYTES } from "../../market/model";
import { artifactAvailable } from "../steps/cards";

// 阶段 10d · 会话产物的预览规则。
// 中心只把可预览白名单里的 MIME 原样保存（text/markdown、text/csv、text/plain、image/png|jpeg|gif|webp、
// video/webm|mp4），其余一律 application/octet-stream。仅对已验证的 text/plain .html/.htm 选择隔离静态预览。

export type ArtifactPreviewKind =
  | "office"
  | "html"
  | "markdown"
  | "table"
  | "code"
  | "image"
  | "pdf"
  | "video"
  | "binary";

/** 文本类产物（Markdown / CSV / 文本代码）预览时取回的字节上限；超过只能下载。 */
export const ARTIFACT_TEXT_MAX_BYTES = 5 * 1024 * 1024;
/** 图片预览上限（与市场一致：10 MB），超过只能下载。 */
export const ARTIFACT_IMAGE_MAX_BYTES = IMAGE_MAX_BYTES;
/** HTML 报告（测试报告常内嵌步骤截图）的渲染上限：10 MB，超过只能下载。 */
export const ARTIFACT_HTML_MAX_BYTES = 10 * 1024 * 1024;
/** 视频（测试录像）在面板内播放的上限：与单个产物上限一致（20 MB）；按 HTTP Range 分段播放，可拖动。 */
export const ARTIFACT_VIDEO_MAX_BYTES = 20 * 1024 * 1024;
/** 文本超过它时按大文件模式只显示前 500 行（与市场一致）。 */
export const ARTIFACT_LARGE_TEXT_BYTES = LARGE_TEXT_BYTES;

const OFFICE_TYPES = new Set([
  "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  "application/vnd.openxmlformats-officedocument.presentationml.presentation",
]);
const VIDEO_TYPES = new Set(["video/webm", "video/mp4"]);
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
  if (VIDEO_TYPES.has(mime)) return "video";
  return "binary";
}

/** 是否在面板里预览（类型可预览且不超过对应上限）。 */
export function artifactPreviewable(artifact: RunArtifact): boolean {
  const kind = artifactPreviewKind(artifact);
  if (kind === "binary") return false;
  return artifact.size_bytes <= artifactFetchLimit(kind);
}

/** 按预览类型取回内容的字节上限（同时是是否在面板内预览的阈值）。 */
export function artifactFetchLimit(kind: ArtifactPreviewKind): number {
  if (kind === "image" || kind === "pdf" || kind === "office")
    return ARTIFACT_IMAGE_MAX_BYTES;
  if (kind === "html") return ARTIFACT_HTML_MAX_BYTES;
  if (kind === "video") return ARTIFACT_VIDEO_MAX_BYTES;
  return ARTIFACT_TEXT_MAX_BYTES;
}

/** 是否支持批注：后端评审只接受不超过 5 MB 的文本类产物，更大的 HTML 报告只能查看。 */
export function artifactAnnotatable(artifact: RunArtifact): boolean {
  const kind = artifactPreviewKind(artifact);
  if (kind === "video" || kind === "binary") return false;
  if (kind === "html") return artifact.size_bytes <= ARTIFACT_TEXT_MAX_BYTES;
  return true;
}

/** 已失效（过保留期、已删除或不可用）。 */
export function artifactExpired(artifact: RunArtifact, now = Date.now()) {
  return !artifactAvailable(artifact, now);
}
