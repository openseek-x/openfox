import { useCallback, useEffect, useRef, useState } from "react";
import { useParams } from "react-router-dom";

import { type DocNodeDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Icon, IconButton, Menu, MenuItem, Skeleton } from "@openfox/ui";

import { useApi } from "../context";
import { localKeys, readLocal, writeLocal } from "../storage";
import { DocActionsProvider } from "./DocActions";
import { DocEditor } from "./DocEditor";
import { DocsNav } from "./DocsNav";
import { FileView } from "./FileView";
import { FolderView } from "./FolderView";
import {
  clampNavWidth,
  DOC_NAV_WIDTH,
  locationOf,
  rootLocation,
  type DocLocation,
} from "./model";
import { DocBreadcrumb, downloadUrl } from "./parts";
import { SheetEditor } from "./SheetEditor";
import { useDocNode, useDocSets } from "./queries";
import { DocHistoryDialog } from "./DocHistoryDialog";

// 文档库页面：二级导航 + 内容区。
// 地址 /docs 为我的文档根，/docs/:nodeId 打开文件夹、文档、表格或文件。

function readNavWidth(): number {
  const stored = Number(readLocal(localKeys.docsNavWidth));
  return stored ? clampNavWidth(stored) : DOC_NAV_WIDTH.default;
}

export function DocsPage() {
  const t = useT();
  const { orgId = "", spaceId = "", nodeId } = useParams();
  const detail = useDocNode(orgId, spaceId, nodeId ?? "");
  const sets = useDocSets(orgId, spaceId);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [openingHistory, setOpeningHistory] = useState(false);
  const flushRef = useRef<() => Promise<boolean>>(async () => true);
  const onFlushReady = useCallback((flush: () => Promise<boolean>) => {
    flushRef.current = flush;
  }, []);
  useEffect(() => {
    setHistoryOpen(false);
  }, [nodeId]);
  const openHistory = async () => {
    setOpeningHistory(true);
    try {
      if (await flushRef.current()) setHistoryOpen(true);
    } finally {
      setOpeningHistory(false);
    }
  };
  const [navWidth, setNavWidth] = useState(readNavWidth);
  const [navCollapsed, setNavCollapsed] = useState(
    () => readLocal(localKeys.docsNavCollapsed) === "1",
  );
  useEffect(() => {
    writeLocal(localKeys.docsNavWidth, String(navWidth));
  }, [navWidth]);
  useEffect(() => {
    writeLocal(localKeys.docsNavCollapsed, navCollapsed ? "1" : null);
  }, [navCollapsed]);

  const current: DocNodeDetail | undefined = nodeId ? detail.data : undefined;
  const location = current ? locationOf(current) : rootLocation();
  const docSet =
    current?.container.kind === "set"
      ? sets.data?.find((set) => set.id === current.container.set_id)
      : undefined;

  return (
    <DocActionsProvider orgId={orgId} spaceId={spaceId} current={current}>
      <div className="flex h-full min-h-0">
        {!navCollapsed && (
          <DocsNav
            orgId={orgId}
            spaceId={spaceId}
            selectedId={nodeId}
            width={navWidth}
            onWidthChange={setNavWidth}
          />
        )}
        <section className="flex min-w-0 flex-1 flex-col">
          <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-default px-3">
            <IconButton
              label={
                navCollapsed ? t("docs.nav.expand") : t("docs.nav.collapse")
              }
              icon={
                <Icon name={navCollapsed ? "panel-left" : "panel-left-close"} />
              }
              size="28"
              variant="ghost"
              onClick={() => setNavCollapsed((value) => !value)}
            />
            <div className="min-w-0 flex-1">
              {!nodeId ? (
                <h1 className="truncate text-body font-medium">
                  {t("docs.nav.groupPersonal")}
                </h1>
              ) : current ? (
                <DocBreadcrumb
                  orgId={orgId}
                  spaceId={spaceId}
                  node={current}
                  docSet={docSet}
                />
              ) : null}
            </div>
            {current && (
              <NodeTools
                orgId={orgId}
                spaceId={spaceId}
                node={current}
                onHistory={openHistory}
                openingHistory={openingHistory}
              />
            )}
          </header>
          <div className="min-h-0 flex-1 overflow-auto">
            {!historyOpen && (
              <DocContent
                orgId={orgId}
                spaceId={spaceId}
                nodeId={nodeId}
                current={current}
                pending={Boolean(nodeId) && detail.isPending}
                failed={Boolean(nodeId) && detail.isError}
                location={location}
                onFlushReady={onFlushReady}
              />
            )}
          </div>
        </section>
        {historyOpen && current && (
          <DocHistoryDialog
            orgId={orgId}
            spaceId={spaceId}
            node={current}
            onClose={() => setHistoryOpen(false)}
          />
        )}
      </div>
    </DocActionsProvider>
  );
}

function DocContent({
  orgId,
  spaceId,
  nodeId,
  current,
  pending,
  failed,
  location,
  onFlushReady,
}: {
  orgId: string;
  spaceId: string;
  nodeId: string | undefined;
  current: DocNodeDetail | undefined;
  pending: boolean;
  failed: boolean;
  location: DocLocation;
  onFlushReady: (flush: () => Promise<boolean>) => void;
}) {
  const t = useT();
  if (pending)
    return (
      <div
        aria-busy="true"
        className="mx-auto flex w-full max-w-[768px] flex-col gap-3 p-6"
      >
        <Skeleton className="h-8 w-1/2" />
        <Skeleton className="h-4 w-full" />
        <Skeleton className="h-4 w-5/6" />
      </div>
    );
  if (failed)
    return (
      <div className="flex flex-1 items-center justify-center p-6 text-body text-danger">
        <p role="alert">{t("docs.loadFailed")}</p>
      </div>
    );
  if (!nodeId || !current || current.kind === "folder")
    return <FolderView orgId={orgId} spaceId={spaceId} location={location} />;
  if (current.kind === "doc")
    return (
      <DocEditor
        key={current.id}
        orgId={orgId}
        spaceId={spaceId}
        node={current}
        onFlushReady={onFlushReady}
      />
    );
  if (current.kind === "sheet")
    return (
      <SheetEditor
        key={current.id}
        orgId={orgId}
        spaceId={spaceId}
        node={current}
        onFlushReady={onFlushReady}
      />
    );
  return <FileView orgId={orgId} spaceId={spaceId} node={current} />;
}

/** 顶栏右侧：只读徽章与「更多操作」，只提供下载。 */
function NodeTools({
  orgId,
  spaceId,
  node,
  onHistory,
  openingHistory,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  onHistory: () => void;
  openingHistory: boolean;
}) {
  const t = useT();
  const api = useApi();
  return (
    <div className="flex shrink-0 items-center gap-2">
      {!node.can_edit && (
        <Badge tone="muted">{t("docs.editor.viewOnly")}</Badge>
      )}
      {(node.kind === "doc" || node.kind === "sheet") && (
        <IconButton
          label={t("docs.history.title")}
          icon={<Icon name="history" />}
          size="28"
          variant="ghost"
          disabled={openingHistory}
          onClick={onHistory}
        />
      )}
      {node.kind !== "folder" && (
        <Menu
          placement="bottom-end"
          label={t("docs.moreActions")}
          className="w-44"
          trigger={(props) => (
            <IconButton
              ref={props.ref}
              label={t("docs.moreActions")}
              icon={<Icon name="ellipsis" />}
              size="28"
              variant="ghost"
              active={props.open}
              onClick={props.toggle}
            />
          )}
        >
          <MenuItem
            icon={<Icon name="download" />}
            onSelect={() =>
              downloadUrl(api.docNodes.downloadUrl(orgId, spaceId, node.id))
            }
          >
            {t("docs.editor.download")}
          </MenuItem>
        </Menu>
      )}
    </div>
  );
}
