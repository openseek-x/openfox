import { useNavigate } from "react-router-dom";

import type { DocNode } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon } from "@openfox/ui";

import { formatDateTime } from "../automations/parts";
import { useDocActions } from "./DocActions";
import { DocRowMenu, QuickAddMenu } from "./DocMenus";
import type { DocLocation } from "./model";
import { docsPath } from "./model";
import { NodeIcon } from "./parts";
import { useDocChildren, useRecentPersonalFiles } from "./queries";

// 阶段 5b · 目录内容（Figma 3.1、3.5）：名称（N 项）/ 更新人 / 更新时间的表格，
// 行 hover 显示「···」，空态与「快速添加」在内容区右上。

export function FolderView({
  orgId,
  spaceId,
  location,
  showPersonalList,
}: {
  orgId: string;
  spaceId: string;
  location: DocLocation;
  showPersonalList: boolean;
}) {
  const t = useT();
  const nodes = useDocChildren(
    orgId,
    spaceId,
    location.container,
    location.parentId,
  );
  const isPersonalRoot =
    location.container.kind === "personal" && location.parentId === null;
  const recent = useRecentPersonalFiles(orgId, spaceId, isPersonalRoot);
  const visible = isPersonalRoot ? recent : nodes;
  const list = visible.data ?? [];
  const folders =
    isPersonalRoot && showPersonalList
      ? nodes.data?.filter((n) => n.kind === "folder")
      : undefined;

  return (
    <div className="mx-auto flex w-full max-w-[896px] flex-col gap-4 p-6">
      <div className="flex flex-wrap items-center justify-end gap-3">
        {isPersonalRoot && (
          <h2 className="mr-auto flex items-center gap-2 text-title font-bold">
            {t("docs.recentlyModified")}
          </h2>
        )}
        <QuickAddMenu
          location={location}
          label={t("docs.quickAdd")}
          placement="bottom-end"
          trigger={(props) => (
            <Button
              ref={props.ref}
              variant="outline"
              size="32"
              aria-haspopup="menu"
              aria-expanded={props.open}
              leading={<Icon name="plus" />}
              onClick={props.toggle}
            >
              {t("docs.quickAdd")}
            </Button>
          )}
        />
      </div>
      {visible.isPending || (isPersonalRoot && nodes.isPending) ? (
        <div aria-busy="true" className="flex flex-col gap-2">
          {[0, 1, 2].map((index) => (
            <div
              key={index}
              className="h-9 animate-pulse rounded-lg bg-surface-muted"
            />
          ))}
        </div>
      ) : visible.isError || nodes.isError ? (
        <p role="alert" className="py-8 text-center text-body text-danger">
          {t("docs.loadFailed")}
        </p>
      ) : list.length === 0 ? (
        isPersonalRoot && nodes.data?.length ? (
          <p className="text-body-sm text-fg-muted">
            {t("docs.noRecentFiles")}
          </p>
        ) : (
          <EmptyFolder location={location} />
        )
      ) : (
        <>
          <div className="@container">
            <NodeTable orgId={orgId} spaceId={spaceId} nodes={list} />
          </div>
          {!isPersonalRoot && (
            <p className="py-4 text-center text-caption text-fg-muted">
              {t("docs.endOfList")}
            </p>
          )}
        </>
      )}
      {folders && folders.length > 0 && (
        <div className="@container">
          <NodeTable orgId={orgId} spaceId={spaceId} nodes={folders} />
        </div>
      )}
    </div>
  );
}

function NodeTable({
  orgId,
  spaceId,
  nodes,
}: {
  orgId: string;
  spaceId: string;
  nodes: readonly DocNode[];
}) {
  const t = useT();
  const { locale } = useI18n();
  const navigate = useNavigate();
  return (
    <table className="w-full table-fixed border-collapse text-body">
      <colgroup>
        <col />
        <col className="hidden w-40 @xl:table-column" />
        <col className="hidden w-32 @xl:table-column" />
        <col className="w-10" />
      </colgroup>
      <thead>
        <tr className="border-b border-line-default text-left text-caption text-fg-muted">
          <th className="h-9 px-2 font-medium">
            {t("docs.itemCount", { count: nodes.length })}
          </th>
          <th className="hidden h-9 px-2 font-medium @xl:table-cell">
            {t("docs.columns.updatedBy")}
          </th>
          <th className="hidden h-9 px-2 font-medium @xl:table-cell">
            {t("docs.columns.updatedAt")}
          </th>
          <th />
        </tr>
      </thead>
      <tbody>
        {nodes.map((node) => (
          <tr
            key={node.id}
            className="group border-b border-line-soft hover:bg-surface-hover"
          >
            <td className="h-9 px-2">
              <button
                type="button"
                onClick={() => navigate(docsPath(orgId, spaceId, node.id))}
                className="flex w-full min-w-0 items-center gap-2 text-left"
              >
                <NodeIcon node={node} />
                <span className="truncate">
                  {node.title || t("docs.untitled")}
                </span>
              </button>
              <span className="block truncate pb-1 pl-6 text-caption text-fg-muted @xl:hidden">
                {[node.updated_by_name, formatDateTime(locale, node.updated_at)]
                  .filter(Boolean)
                  .join(" · ")}
              </span>
            </td>
            <td className="hidden truncate px-2 text-fg-secondary @xl:table-cell">
              {node.updated_by_name}
            </td>
            <td className="hidden truncate px-2 text-fg-secondary @xl:table-cell">
              {formatDateTime(locale, node.updated_at)}
            </td>
            <td className="px-1 @xl:of-row-actions">
              <DocRowMenu node={node} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function EmptyFolder({ location }: { location: DocLocation }) {
  const t = useT();
  const actions = useDocActions();
  return (
    <div className="flex flex-col items-start gap-4 py-16">
      <Icon name="hifox-empty-library" size={90} />
      <div className="flex flex-col gap-2">
        <h2 className="text-body font-medium">{t("docs.emptyTitle")}</h2>
        <p className="max-w-md text-body-sm text-fg-muted">
          {t("docs.emptyDesc")}
        </p>
      </div>
      <div className="flex gap-2">
        <Button
          variant="primary"
          size="32"
          leading={<Icon name="plus" />}
          onClick={() => actions.createNode(location, "doc")}
        >
          {t("docs.emptyNewDoc")}
        </Button>
        <Button
          variant="outline"
          size="32"
          leading={<Icon name="upload" />}
          disabled={actions.uploading}
          onClick={() => actions.requestUpload(location, "file")}
        >
          {actions.uploading
            ? t("docs.create.uploading")
            : t("docs.emptyUpload")}
        </Button>
      </div>
    </div>
  );
}
