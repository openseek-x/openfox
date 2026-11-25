import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { useLocation, useParams } from "react-router-dom";

import { type DocNodeDetail } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Drawer,
  Icon,
  IconButton,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  Skeleton,
} from "@openfox/ui";

import { keys, useApi } from "../context";
import { usePanelWidth } from "../shell/panelWidth";
import { localKeys, readLocal, writeLocal } from "../storage";
import { DocActionsProvider } from "./DocActions";
import { DocEditor } from "./DocEditor";
import { DocNodeMenuItems } from "./DocMenus";
import {
  docPageWidths,
  useDocPageWidth,
  type DocPageWidth,
} from "./pagePreferences";
import { DocsNav } from "./DocsNav";
import { FileView } from "./FileView";
import { FolderView } from "./FolderView";
import {
  DOC_NAV_WIDTH,
  locationOf,
  rootLocation,
  type DocLocation,
} from "./model";
import { DocBreadcrumb, downloadUrl } from "./parts";
import { SheetEditor } from "./SheetEditor";
import { useDocNode, useDocSets } from "./queries";
import { DocHistoryDialog } from "./DocHistoryDialog";

// 阶段 5b · 文档库页面（Figma 3.1–3.8）：二级导航 + 内容区。
// 地址 /docs 为我的文档根，/docs/:nodeId 打开文件夹、文档、表格或文件。

const MIN_READER_WIDTH = 560;

export function DocsPage() {
  const t = useT();
  const api = useApi();
  const me = useQuery({ queryKey: keys.me, queryFn: api.me.get });
  const [pageWidth, setPageWidth] = useDocPageWidth(me.data?.id ?? "");
  const locationKey = useLocation().key;
  const { orgId = "", spaceId = "", nodeId } = useParams();
  const detail = useDocNode(orgId, spaceId, nodeId ?? "");
  const sets = useDocSets(orgId, spaceId);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [openingHistory, setOpeningHistory] = useState(false);
  const flushRef = useRef<{ nodeId?: string; flush: () => Promise<boolean> }>({
    flush: async () => true,
  });
  const onFlushReady = useCallback(
    (flush: () => Promise<boolean>) => {
      flushRef.current = { nodeId, flush };
    },
    [nodeId],
  );
  const flushCurrent = () =>
    flushRef.current.nodeId === nodeId
      ? flushRef.current.flush()
      : Promise.resolve(true);
  useEffect(() => {
    setHistoryOpen(false);
  }, [nodeId]);
  const openHistory = async () => {
    setOpeningHistory(true);
    try {
      if (await flushCurrent()) setHistoryOpen(true);
    } finally {
      setOpeningHistory(false);
    }
  };
  const nav = usePanelWidth({
    storageKey: localKeys.docsNavWidth,
    initial: DOC_NAV_WIDTH.default,
    min: DOC_NAV_WIDTH.min,
    max: DOC_NAV_WIDTH.max,
    remaining: MIN_READER_WIDTH,
  });
  const [navCollapsed, setNavCollapsed] = useState(
    () => readLocal(localKeys.docsNavCollapsed) === "1",
  );
  useEffect(() => {
    writeLocal(localKeys.docsNavCollapsed, navCollapsed ? "1" : null);
  }, [navCollapsed]);

  // 连最小目录宽度都无法容纳时，优先保留正文。
  const narrow =
    nav.availableWidth > 0 &&
    nav.availableWidth < DOC_NAV_WIDTH.min + MIN_READER_WIDTH;
  const [treeOpen, setTreeOpen] = useState(false);
  useEffect(() => setTreeOpen(false), [locationKey, narrow]);
  const treeHidden = narrow || navCollapsed;
  const tree = (
    <DocsNav
      orgId={orgId}
      spaceId={spaceId}
      selectedId={nodeId}
      width={
        narrow ? Math.min(DOC_NAV_WIDTH.default, nav.availableWidth) : nav.width
      }
      maxWidth={narrow ? DOC_NAV_WIDTH.min : nav.max}
      onWidthChange={nav.onWidthChange}
    />
  );

  const current: DocNodeDetail | undefined = nodeId ? detail.data : undefined;
  const location = current ? locationOf(current) : rootLocation();
  const docSet =
    current?.container.kind === "set"
      ? sets.data?.find((set) => set.id === current.container.set_id)
      : undefined;

  return (
    <DocActionsProvider
      orgId={orgId}
      spaceId={spaceId}
      current={current}
      beforeCurrentChange={() => flushCurrent()}
    >
      <div ref={nav.containerRef} className="flex h-full min-h-0">
        {narrow ? (
          <Drawer
            open={treeOpen}
            onClose={() => setTreeOpen(false)}
            title={t("docs.title")}
            side="left"
            width="min(302px, calc(100vw - 24px))"
          >
            <div className="h-full [&>aside]:max-w-full">{tree}</div>
          </Drawer>
        ) : (
          !navCollapsed && tree
        )}
        <section className="flex min-w-0 flex-1 flex-col">
          <header className="flex h-11 shrink-0 items-center gap-2 border-b border-line-subtle px-3">
            <IconButton
              label={treeHidden ? t("docs.nav.expand") : t("docs.nav.collapse")}
              icon={
                <Icon name={treeHidden ? "panel-left" : "panel-left-close"} />
              }
              size="28"
              variant="ghost"
              aria-expanded={narrow ? treeOpen : !navCollapsed}
              aria-haspopup={narrow ? "dialog" : undefined}
              onClick={() =>
                narrow ? setTreeOpen(true) : setNavCollapsed((value) => !value)
              }
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
                pageWidth={pageWidth}
                onPageWidth={setPageWidth}
                beforeDownload={() => flushCurrent()}
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
                treeHidden={treeHidden}
                pending={Boolean(nodeId) && detail.isPending}
                failed={Boolean(nodeId) && detail.isError}
                location={location}
                onFlushReady={onFlushReady}
                pageWidth={pageWidth}
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
  treeHidden,
  failed,
  location,
  onFlushReady,
  pageWidth,
}: {
  orgId: string;
  spaceId: string;
  nodeId: string | undefined;
  current: DocNodeDetail | undefined;
  pending: boolean;
  treeHidden: boolean;
  failed: boolean;
  location: DocLocation;
  onFlushReady: (flush: () => Promise<boolean>) => void;
  pageWidth: DocPageWidth;
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
    return (
      <FolderView
        orgId={orgId}
        spaceId={spaceId}
        location={location}
        showPersonalList={treeHidden}
      />
    );
  if (current.kind === "doc")
    return (
      <DocEditor
        key={current.id}
        orgId={orgId}
        spaceId={spaceId}
        node={current}
        onFlushReady={onFlushReady}
        pageWidth={pageWidth}
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

/** 顶栏复用树节点操作，并保留下载、历史与个人阅读设置。 */
function NodeTools({
  orgId,
  spaceId,
  node,
  onHistory,
  openingHistory,
  pageWidth,
  onPageWidth,
  beforeDownload,
}: {
  orgId: string;
  spaceId: string;
  node: DocNodeDetail;
  onHistory: () => void;
  openingHistory: boolean;
  pageWidth: DocPageWidth;
  onPageWidth: (width: DocPageWidth) => void;
  beforeDownload: () => Promise<boolean>;
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
          <DocNodeMenuItems node={node} />
          <MenuSeparator />
          <MenuItem
            icon={<Icon name="download" />}
            onSelect={() => {
              void beforeDownload().then((saved) => {
                if (saved)
                  downloadUrl(
                    api.docNodes.downloadUrl(orgId, spaceId, node.id),
                  );
              });
            }}
          >
            {t("docs.editor.download")}
          </MenuItem>
          {(node.kind === "doc" || node.kind === "sheet") && (
            <MenuItem
              icon={<Icon name="history" />}
              disabled={openingHistory}
              onSelect={onHistory}
            >
              {t("docs.history.title")}
            </MenuItem>
          )}
          {node.kind === "doc" && (
            <>
              <MenuSeparator />
              <MenuLabel>{t("docs.pageWidth.title")}</MenuLabel>
              {(Object.keys(docPageWidths) as DocPageWidth[]).map((width) => (
                <MenuItem
                  key={width}
                  selected={pageWidth === width}
                  onSelect={() => onPageWidth(width)}
                >
                  {t(`docs.pageWidth.${width}`)}
                </MenuItem>
              ))}
            </>
          )}
        </Menu>
      )}
    </div>
  );
}
