import type { DocNodeDetail } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon } from "@openfox/ui";

import { formatDateTime } from "../automations/parts";
import { useApi } from "../context";
import { downloadUrl, NodeIcon } from "./parts";

// 文件节点：显示文件信息与「下载」；图片直接预览。

/** 文件大小：B / KB / MB / GB，保留一位小数（整数字节不带小数）。 */
export function formatBytes(bytes: number | null): string {
  if (bytes === null || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} B`;
  const units = ["KB", "MB", "GB"];
  let value = bytes / 1024;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(1)} ${units[unit]}`;
}

export function FileView({
  orgId,
  spaceId,
  node,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
}) {
  const t = useT();
  const api = useApi();
  const { locale } = useI18n();
  const url = api.docNodes.downloadUrl(orgId, spaceId, node.id);
  const isImage = node.content_type?.startsWith("image/") ?? false;
  const title = node.title || t("docs.untitled");
  return (
    <div className="mx-auto flex w-full max-w-[768px] flex-col gap-6 p-6">
      {isImage && (
        <img
          src={url}
          alt={t("docs.editor.imagePreview")}
          className="max-h-[480px] self-center rounded-xl object-contain"
        />
      )}
      <div className="flex items-center gap-3 rounded-xl border border-line-default p-4">
        <NodeIcon node={node} size={24} />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <h1 className="truncate text-body font-medium">{title}</h1>
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-caption text-fg-muted">
            <dt>{t("docs.file.kind")}</dt>
            <dd className="truncate">
              {node.content_type ?? t("docs.file.file")}
            </dd>
            <dt>{t("docs.file.size")}</dt>
            <dd>{formatBytes(node.size_bytes)}</dd>
            <dt>{t("docs.file.updatedAt")}</dt>
            <dd>{formatDateTime(locale, node.updated_at)}</dd>
          </dl>
        </div>
        <Button
          variant="outline"
          size="32"
          leading={<Icon name="download" />}
          onClick={() => downloadUrl(url)}
        >
          {t("docs.file.download")}
        </Button>
      </div>
    </div>
  );
}
