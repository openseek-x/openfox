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
import { useDocChildren } from "./queries";

// 目录内容：名称（N 项）/ 更新人 / 更新时间的表格，
// 行 hover 显示「···」，空态与「快速添加」在内容区右上。

export function FolderView({
  orgId,
  spaceId,
  location,
}: {
  orgId: string;
  spaceId: string;
  location: DocLocation;
}) {
  const t = useT();
  const nodes = useDocChildren(
    orgId,
    spaceId,
    location.container,
    location.parentId,
  );
  const list = nodes.data ?? [];
  const isPersonalRoot =
    location.container.kind === "personal" && location.parentId === null;

  return (
    <div className="mx-auto flex w-full max-w-[896px] flex-col gap-4 p-6">
      <div className="flex flex-wrap items-center justify-end gap-3">
        {isPersonalRoot && (
          <h2 className="mr-auto flex items-center gap-2 text-title font-bold">
            <Icon name="folder" className="text-fg-muted" />
            {t("docs.nav.groupPersonal")}
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
      {nodes.isPending ? (
        <div aria-busy="true" className="flex flex-col gap-2">
          {[0, 1, 2].map((index) => (
            <div
              key={index}
              className="h-9 animate-pulse rounded-lg bg-surface-muted"
            />
          ))}
        </div>
      ) : nodes.isError ? (
        <p role="alert" className="py-8 text-center text-body text-danger">
          {t("docs.loadFailed")}
        </p>
      ) : list.length === 0 ? (
        <EmptyFolder location={location} />
      ) : !isPersonalRoot ? (
        <>
          <div className="overflow-x-auto">
            <NodeTable orgId={orgId} spaceId={spaceId} nodes={list} />
          </div>
          <p className="py-4 text-center text-caption text-fg-muted">
            {t("docs.endOfList")}
          </p>
        </>
      ) : null}
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
    <table className="w-full min-w-[640px] table-fixed border-collapse text-body">
      <colgroup>
        <col />
        <col className="w-40" />
        <col className="w-32" />
        <col className="w-10" />
      </colgroup>
      <thead>
        <tr className="border-b border-line-default text-left text-caption text-fg-muted">
          <th className="h-9 px-2 font-medium">
            {t("docs.itemCount", { count: nodes.length })}
          </th>
          <th className="h-9 px-2 font-medium">
            {t("docs.columns.updatedBy")}
          </th>
          <th className="h-9 px-2 font-medium">
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
            </td>
            <td className="truncate px-2 text-fg-secondary">
              {node.updated_by_name}
            </td>
            <td className="truncate px-2 text-fg-secondary">
              {formatDateTime(locale, node.updated_at)}
            </td>
            <td className="px-1 opacity-0 group-hover:opacity-100 focus-within:opacity-100">
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
      <Icon name="openfox-empty-library" size={90} />
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
