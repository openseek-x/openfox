import { Link } from "react-router-dom";

import type {
  DocContainer,
  DocNode,
  DocNodeDetail,
  DocSet,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Icon, cn, type IconName } from "@openfox/ui";

import { ViewIcon } from "../views/ViewIcon";

// 文档库的共用小组件：节点图标、文档集图标、面包屑。

/** 节点图标：文件夹、文档、表格；文件按内容类型区分图片。 */
export function NodeIcon({
  node,
  size = 16,
  className,
}: {
  node: Pick<DocNode, "kind" | "content_type">;
  size?: number;
  className?: string;
}) {
  const name: IconName =
    node.kind === "folder"
      ? "folder"
      : node.kind === "doc"
        ? "file-text"
        : node.kind === "sheet"
          ? "file-spreadsheet"
          : node.content_type?.startsWith("image/")
            ? "image"
            : "openfox-file-down";
  return (
    <Icon
      name={name}
      size={size}
      className={cn("shrink-0 text-fg-secondary", className)}
    />
  );
}

/** 文档集图标：未设置时为 library-big。 */
export function DocSetIcon({
  docSet,
  size = 16,
}: {
  docSet: DocSet;
  size?: number;
}) {
  return (
    <ViewIcon
      view={{
        icon_kind: docSet.icon_kind ?? "icon",
        icon: docSet.icon ?? "library-big",
        icon_url: docSet.icon_url,
        name: docSet.name,
      }}
      size={size}
    />
  );
}

/** 容器名称：「我的文档」或文档集名称。 */
export function containerName(
  container: DocContainer,
  sets: readonly DocSet[] | undefined,
  personalLabel: string,
): string {
  if (container.kind === "personal") return personalLabel;
  return sets?.find((set) => set.id === container.set_id)?.name ?? "";
}

/**
 * 面包屑：我的文档（或文档集）→ 各级祖先 → 当前节点。
 * 祖先可点击回到对应节点；文档集本身没有独立页面，首段只显示图标与名称。
 */
export function DocBreadcrumb({
  orgId,
  spaceId,
  node,
  docSet,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  /** 节点所在的文档集；我的文档时不传 */
  docSet?: DocSet;
}) {
  const t = useT();
  const base = `/o/${encodeURIComponent(orgId)}/spaces/${encodeURIComponent(spaceId)}/docs`;
  const rootLabel = t("docs.nav.groupPersonal");
  return (
    <nav
      aria-label={t("docs.title")}
      className="flex min-w-0 items-center gap-1.5 text-body text-fg-secondary"
    >
      {node.container.kind === "set" ? (
        <span className="flex min-w-0 items-center gap-2">
          {docSet && <DocSetIcon docSet={docSet} />}
          <span className="truncate">{docSet?.name}</span>
        </span>
      ) : (
        <Link to={base} className="truncate hover:text-fg-default">
          {rootLabel}
        </Link>
      )}
      {node.breadcrumb.map((crumb) => (
        <span key={crumb.id} className="flex min-w-0 items-center gap-1.5">
          <Icon
            name="chevron-right"
            size={12}
            className="shrink-0 text-fg-muted"
          />
          <Link
            to={`${base}/${encodeURIComponent(crumb.id)}`}
            className="truncate hover:text-fg-default"
          >
            {crumb.title || t("docs.untitled")}
          </Link>
        </span>
      ))}
      <Icon name="chevron-right" size={12} className="shrink-0 text-fg-muted" />
      <span className="truncate font-medium text-fg-default">
        {node.title || t("docs.untitled")}
      </span>
    </nav>
  );
}

/** 通过隐藏链接触发下载；服务端以 Content-Disposition: attachment 返回文件。 */
export function downloadUrl(url: string) {
  const link = document.createElement("a");
  link.href = url;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
}
