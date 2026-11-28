import { useApi, keys } from "../../context";
import { NativeCanvas } from "../canvas/NativeCanvas";
import { DocumentPicker } from "../canvas/DocumentPicker";
import { WorkspaceComments } from "../comments/WorkspaceComments";
import {
  proposalKey,
  WorkspaceProposals,
} from "../proposals/WorkspaceProposals";
import { ProposalPreview } from "../proposals/ProposalPreview";
import { useQueries, useQueryClient } from "@tanstack/react-query";
import { PreviewStateKey, useViewState } from "./viewState";
import { AppErrorBoundary } from "../../ErrorBoundary";
import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
} from "react";

import type { RunArtifact } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, IconButton, Skeleton, cn } from "@openfox/ui";

import { formatBytes } from "../steps/cards";
import { ArtifactPreview } from "./ArtifactPreview";
import { artifactExpired, artifactPreviewKind } from "./model";
import { WorkspaceToolbarTarget } from "./PreviewNavigation";
import {
  currentTabKey,
  isCurrentTab,
  ARTIFACT_LIST,
  BROWSER_PANEL,
  COMMENTS_PANEL,
  PROPOSALS_PANEL,
  NEW_PANEL,
  browserTabKey,
  documentTab,
  isBrowserTab,
  isArtifactTab,
  isProposalTab,
  type ArtifactPanelState,
} from "./params";
import { desktopBridge } from "../../desktop";
import { isMacPlatform } from "../../me/shortcuts";
import { BrowserPanel } from "../browser/BrowserPanel";
import { useBrowserSession } from "../browser/useBrowserSession";
import {
  useConversationArtifacts,
  useResolvedArtifact,
  resolvedArtifactKey,
} from "./queries";
import { PanelResizeHandle } from "../../shell/PanelResizeHandle";
import { usePanelWidth } from "../../shell/panelWidth";
import { localKeys } from "../../storage";

// 会话右侧工作区：按需打开的文件、网页、评论、提案和产物列表共用动态标签栏。
// 宽容器里与消息流并排；窄容器（任务全页的侧边会话、小窗口）里覆盖在消息流上。

/** 中心返回的会话产物上限（listConversationArtifacts maxItems）。 */
const MAX_LISTED = 200;

function ArtifactPanelContent({
  orgId,
  conversationId,
  panel,
  reviewDisabled = false,
}: {
  orgId: string;
  conversationId: string;
  panel: ArtifactPanelState;
  reviewDisabled?: boolean;
}) {
  const t = useT();
  const panelId = useId();
  const [expanded, setExpanded] = useViewState("expanded", false);
  const resize = usePanelWidth({
    storageKey: localKeys.artifactPanelWidth,
    initial: 640,
    min: 280,
    max: 1280,
    remaining: 360,
  });
  const containerRef = resize.containerRef;
  const [asideEl, setAsideEl] = useState<HTMLElement | null>(null);
  const [toolbarSlot, setToolbarSlot] = useState<HTMLDivElement | null>(null);
  const [panelWidth, setPanelWidth] = useState(0);
  const panelRef = useCallback(
    (node: HTMLElement | null) => {
      setAsideEl(node);
      containerRef(node?.parentElement ?? null);
    },
    [containerRef],
  );
  useEffect(() => {
    if (!asideEl) return;
    const sync = () => setPanelWidth(asideEl.clientWidth);
    sync();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(sync);
    observer.observe(asideEl);
    return () => observer.disconnect();
  }, [asideEl]);
  const wideHeader = expanded || panelWidth === 0 || panelWidth >= 460;
  const shortcut = isMacPlatform() ? "⌘⇧\\" : "Ctrl+Shift+\\";
  const queryClient = useQueryClient();
  const api = useApi();
  const docs = panel.tabs.flatMap((id) => {
    const ref = documentTab(id);
    return ref ? [{ id, ...ref }] : [];
  });
  const docQueries = useQueries({
    queries: docs.map((ref) => ({
      queryKey: keys.docNode(orgId, ref.spaceId, ref.nodeId),
      queryFn: () => api.docNodes.get(orgId, ref.spaceId, ref.nodeId),
      refetchOnWindowFocus: false,
      retry: false,
    })),
  });
  const docNames = new Map(
    docs.map((ref, i) => [ref.id, docQueries[i]?.data?.title]),
  );
  const proposalIds = panel.tabs.filter(isProposalTab);
  const proposalQueries = useQueries({
    queries: proposalIds.map((id) => ({
      queryKey: proposalKey(orgId, conversationId, id.slice(9)),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        api.workspaceProposals.get(orgId, conversationId, id.slice(9), signal),
      retry: false,
    })),
  });
  const proposalNames = new Map(
    proposalIds.map((id, index) => [
      id,
      proposalQueries[index]?.data?.base.name,
    ]),
  );
  const query = useConversationArtifacts(orgId, conversationId);
  const artifacts = query.data;
  const byId = new Map((artifacts ?? []).map((item) => [item.id, item]));
  const selected = panel.selected ?? NEW_PANEL;
  const selectedDoc = documentTab(selected);
  const browserAvailable =
    desktopBridge()?.platform === "darwin" &&
    Boolean(desktopBridge()?.browserMount);
  const browserSelected = selected === BROWSER_PANEL || isBrowserTab(selected);
  const [browserStarted, setBrowserStarted] = useState(browserSelected);
  useEffect(() => {
    if (browserSelected) setBrowserStarted(true);
  }, [browserSelected]);
  const browser = useBrowserSession(
    orgId,
    conversationId,
    browserAvailable && (browserStarted || browserSelected),
  );
  const lastBrowserActive = useRef<string | null>(null);
  const lastBrowserTabs = useRef(new Set<string>());
  const { syncBrowserTabs, select, tabs, closePanel } = panel;
  useEffect(() => {
    const state = browser.state;
    if (!state) return;
    syncBrowserTabs(state.tabs.map((tab) => tab.id));
    if (
      state.activeTab &&
      (selected === BROWSER_PANEL ||
        (lastBrowserActive.current !== state.activeTab &&
          (isBrowserTab(selected) ||
            !lastBrowserTabs.current.has(state.activeTab))))
    )
      select(browserTabKey(state.activeTab));
    else if (!state.activeTab && isBrowserTab(selected)) {
      const remaining = tabs.filter((id) => !isBrowserTab(id));
      if (remaining.length) select(remaining.at(-1)!);
      else closePanel();
    }
    lastBrowserActive.current = state.activeTab;
    lastBrowserTabs.current = new Set(state.tabs.map((tab) => tab.id));
  }, [browser.state, selected, syncBrowserTabs, select, tabs, closePanel]);
  const openBrowser = () => {
    if (browser.state) void browser.command({ action: "open" });
    else select(BROWSER_PANEL);
  };
  const visible = (artifacts ?? []).filter((a) => a.id === a.head_artifact_id);
  const heads = new Map(visible.map((a) => [currentTabKey(a.content_id), a]));
  const lookup = (id: string) =>
    isCurrentTab(id) ? heads.get(id) : byId.get(id);
  const listed = lookup(selected);
  const resolved = useResolvedArtifact(
    orgId,
    conversationId,
    selected,
    isArtifactTab(selected) && Boolean(artifacts) && !listed,
  );
  const current =
    listed ?? (isArtifactTab(selected) ? resolved.data : undefined);
  const pinned = !isCurrentTab(selected);
  const missing = isArtifactTab(selected) && !current;

  const hasInlineToolbar =
    wideHeader && (Boolean(current) || Boolean(selectedDoc));

  return (
    <WorkspaceToolbarTarget.Provider value={wideHeader ? toolbarSlot : null}>
      <aside
        ref={panelRef}
        id={panelId}
        aria-label={t("artifacts.panel.label")}
        style={
          { "--artifact-panel-width": `${resize.width}px` } as CSSProperties
        }
        className={cn(
          "absolute inset-0 z-20 flex min-h-0 min-w-0 flex-col bg-surface-canvas",
          !expanded &&
            "@[720px]:relative @[720px]:w-[var(--artifact-panel-width)] @[720px]:shrink-0 @[720px]:border-l @[720px]:border-line-subtle",
        )}
      >
        <PanelResizeHandle
          {...resize}
          label={t("artifacts.panel.resize")}
          controls={panelId}
          side="right"
          onCollapse={panel.closePanel}
          onReset={resize.reset}
          className={cn(
            "absolute inset-y-0 -left-1 hidden w-2",
            !expanded && "@[720px]:block",
          )}
        />
        <div className="@container/tabs flex h-11 shrink-0 items-center gap-1 border-b border-line-subtle bg-surface-canvas px-2">
          <div
            role="tablist"
            aria-label={t("artifacts.panel.tabs")}
            onWheel={(event) => {
              if (Math.abs(event.deltaY) > Math.abs(event.deltaX))
                event.currentTarget.scrollLeft += event.deltaY;
            }}
            className="flex min-w-0 items-center gap-1 overflow-x-auto"
          >
            {panel.tabs.map((id) => {
              const artifact =
                lookup(id) ??
                (id === selected
                  ? current
                  : queryClient.getQueryData<RunArtifact>(
                      resolvedArtifactKey(orgId, conversationId, id),
                    ));
              const webTab = browser.state?.tabs.find(
                (tab) => browserTabKey(tab.id) === id,
              );
              const title =
                id === ARTIFACT_LIST
                  ? t("artifacts.panel.list", { count: visible.length })
                  : id === COMMENTS_PANEL
                    ? t("artifacts.comments.tabTitle")
                    : id === PROPOSALS_PANEL
                      ? t("artifacts.proposals.tabTitle")
                      : id === NEW_PANEL
                        ? t("artifacts.workspace.newTab")
                        : webTab
                          ? webTab.title ||
                            (webTab.url === "about:blank"
                              ? t("artifacts.browser.newTab")
                              : webTab.url)
                          : documentTab(id)
                            ? (docNames.get(id) ?? t("docs.untitled"))
                            : isProposalTab(id)
                              ? `${proposalNames.get(id) ?? t("artifacts.version.file")} · ${t("artifacts.proposals.review")}`
                              : (artifact?.name ??
                                (isArtifactTab(id)
                                  ? t("artifacts.version.file")
                                  : undefined));
              if (!title) return null;
              return (
                <Tab
                  key={id}
                  id={id}
                  label={title}
                  badge={
                    artifact
                      ? isCurrentTab(id)
                        ? t("artifacts.version.current")
                        : `v${artifact.content_revision}`
                      : undefined
                  }
                  description={
                    artifact
                      ? t(
                          isCurrentTab(id)
                            ? "artifacts.version.following"
                            : "artifacts.version.fixed",
                          { version: artifact.content_revision },
                        )
                      : undefined
                  }
                  selected={selected === id}
                  onSelect={() => {
                    panel.select(id);
                    if (webTab)
                      void browser.command({
                        action: "focus",
                        tabId: webTab.id,
                      });
                  }}
                  onClose={() => {
                    if (webTab)
                      void browser.command({
                        action: "close",
                        tabId: webTab.id,
                      });
                    else panel.closeTab(id);
                  }}
                  onReorder={panel.reorder}
                  icon={
                    webTab
                      ? "globe"
                      : id === ARTIFACT_LIST
                        ? "folder"
                        : id === COMMENTS_PANEL
                          ? "message-circle"
                          : id === PROPOSALS_PANEL || isProposalTab(id)
                            ? "layers"
                            : id === NEW_PANEL
                              ? "plus"
                              : "file-text"
                  }
                  closeLabel={t("artifacts.panel.closeTab", {
                    name: title,
                  })}
                >
                  {title}
                </Tab>
              );
            })}
          </div>
          <IconButton
            label={t("artifacts.workspace.addTab")}
            icon={<Icon name="plus" size={15} />}
            size="28"
            variant="ghost"
            onClick={() => panel.select(NEW_PANEL)}
          />
          <div className="min-w-0 flex-1" />
          <div
            ref={setToolbarSlot}
            className="flex shrink-0 items-center gap-1 empty:hidden"
          />
          {hasInlineToolbar && (
            <span
              aria-hidden="true"
              className="mx-0.5 h-3.5 w-px shrink-0 bg-line-default"
            />
          )}
          <IconButton
            label={t(
              expanded ? "artifacts.panel.restore" : "artifacts.panel.expand",
            )}
            icon={
              <Icon name={expanded ? "minimize-2" : "maximize-2"} size={15} />
            }
            size="28"
            variant="ghost"
            active={expanded}
            onClick={() => setExpanded((value) => !value)}
          />
          <IconButton
            label={t("artifacts.panel.close")}
            title={`${t("artifacts.panel.close")} (${shortcut})`}
            icon={<Icon name="panel-left" size={15} className="rotate-180" />}
            size="28"
            variant="ghost"
            onClick={panel.closePanel}
          />
        </div>
        <div className="flex min-h-0 flex-1 flex-col bg-surface-card">
          {browserSelected ? (
            <BrowserPanel session={browser} />
          ) : selectedDoc ? (
            <NativeCanvas
              key={selected}
              orgId={orgId}
              conversationId={conversationId}
              {...selectedDoc}
            />
          ) : isProposalTab(selected) ? (
            <ProposalPreview
              key={selected}
              orgId={orgId}
              conversationId={conversationId}
              id={selected.slice(9)}
            />
          ) : selected === COMMENTS_PANEL ? (
            <WorkspaceComments
              orgId={orgId}
              conversationId={conversationId}
              disabled={reviewDisabled}
              embedded
            />
          ) : selected === PROPOSALS_PANEL ? (
            <WorkspaceProposals
              orgId={orgId}
              conversationId={conversationId}
              onOpen={() => {}}
              embedded
            />
          ) : selected === NEW_PANEL ? (
            <WorkspaceLauncher
              orgId={orgId}
              conversationId={conversationId}
              browserAvailable={browserAvailable}
              visible={visible}
              onOpenBrowser={openBrowser}
              onSelect={panel.select}
            />
          ) : query.isError && !artifacts ? (
            <div role="alert" className="flex flex-col items-start gap-2 p-4">
              <p className="text-body text-danger">
                {t("artifacts.list.loadFailed")}
              </p>
              <Button
                variant="outline"
                size="28"
                onClick={() => void query.refetch()}
              >
                {t("common.retry")}
              </Button>
            </div>
          ) : !artifacts ? (
            <div aria-busy="true" className="flex flex-col gap-2 p-4">
              <Skeleton className="h-10 w-full" />
              <Skeleton className="h-10 w-full" />
            </div>
          ) : missing ? (
            <div
              className="flex flex-col items-start gap-2 p-4"
              role={resolved.isError ? "alert" : "status"}
            >
              <p className="text-body-sm text-fg-muted">
                {t(
                  resolved.isError
                    ? "artifacts.version.unavailable"
                    : "artifacts.review.loading",
                )}
              </p>
              {resolved.isError && (
                <Button
                  size="28"
                  variant="outline"
                  onClick={() => void resolved.refetch()}
                >
                  {t("common.retry")}
                </Button>
              )}
            </div>
          ) : current ? (
            <PreviewStateKey.Provider
              key={`${selected}:${current.id}`}
              value={`${conversationId}:${selected}:${current.id}:${current.sha256}`}
            >
              <ArtifactPreview
                key={current.id}
                orgId={orgId}
                conversationId={conversationId}
                artifact={current}
                versionMode={pinned ? "fixed" : "current"}
                onOpenVersion={() =>
                  panel.select(
                    pinned ? currentTabKey(current.content_id) : current.id,
                  )
                }
              />
            </PreviewStateKey.Provider>
          ) : (
            <ArtifactList
              artifacts={visible}
              onOpen={(id) => {
                const file = byId.get(id);
                if (file) panel.select(currentTabKey(file.content_id));
              }}
            />
          )}
          {selected === ARTIFACT_LIST &&
            !current &&
            artifacts &&
            artifacts.length >= MAX_LISTED && (
              <p className="shrink-0 px-4 pb-3 text-caption text-fg-muted">
                {t("artifacts.list.more", { count: MAX_LISTED })}
              </p>
            )}
        </div>
      </aside>
    </WorkspaceToolbarTarget.Provider>
  );
}

/** 工作区新标签启动页：居中搜索、均衡快捷磁贴与最近产物卡片列表。 */
function WorkspaceLauncher({
  orgId,
  conversationId,
  browserAvailable,
  visible,
  onOpenBrowser,
  onSelect,
}: {
  orgId: string;
  conversationId: string;
  browserAvailable: boolean;
  visible: readonly RunArtifact[];
  onOpenBrowser: () => void;
  onSelect: (id: string) => void;
}) {
  const t = useT();
  const { formatRelative } = useI18n();
  const recent = [...visible]
    .sort((a, b) => (a.created_at < b.created_at ? 1 : -1))
    .slice(0, 5);
  const shortcut = isMacPlatform() ? "⌘⇧\\" : "Ctrl+Shift+\\";
  const tileClass =
    "group flex flex-col items-start gap-2.5 rounded-xl border border-line-subtle bg-surface-card p-3 text-left shadow-card transition-colors hover:border-line-strong hover:bg-surface-hover/50";
  return (
    <div className="@container/launcher flex min-h-0 flex-1 flex-col overflow-y-auto bg-surface-canvas/40 px-5 py-8">
      <div className="mx-auto flex w-full max-w-md flex-col gap-5">
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2">
            <span className="flex size-7 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary">
              <Icon name="layers" size={15} />
            </span>
            <p className="text-body font-semibold text-fg-default">
              {t("artifacts.workspace.choose")}
            </p>
          </div>
          <kbd className="shrink-0 rounded-md border border-line-subtle bg-surface-card px-1.5 py-0.5 font-mono text-[11px] text-fg-muted">
            {shortcut}
          </kbd>
        </div>
        <DocumentPicker
          orgId={orgId}
          conversationId={conversationId}
          onOpen={onSelect}
        />
        <div
          className={cn(
            "grid gap-2.5",
            browserAvailable
              ? "grid-cols-1 @[340px]/launcher:grid-cols-2"
              : "grid-cols-1 @[360px]/launcher:grid-cols-3",
          )}
        >
          {browserAvailable && (
            <button type="button" className={tileClass} onClick={onOpenBrowser}>
              <span className="flex size-8 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary transition-colors group-hover:bg-accent-soft group-hover:text-accent">
                <Icon name="globe" size={16} />
              </span>
              <span className="text-body-sm font-medium text-fg-default">
                {t("artifacts.workspace.openBrowser")}
              </span>
            </button>
          )}
          <button
            type="button"
            className={tileClass}
            onClick={() => onSelect(ARTIFACT_LIST)}
          >
            <span className="flex size-8 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary transition-colors group-hover:bg-accent-soft group-hover:text-accent">
              <Icon name="folder" size={16} />
            </span>
            <span className="text-body-sm font-medium text-fg-default">
              {t("artifacts.workspace.openArtifacts", {
                count: visible.length,
              })}
            </span>
          </button>
          <button
            type="button"
            className={tileClass}
            onClick={() => onSelect(COMMENTS_PANEL)}
          >
            <span className="flex size-8 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary transition-colors group-hover:bg-accent-soft group-hover:text-accent">
              <Icon name="message-circle" size={16} />
            </span>
            <span className="text-body-sm font-medium text-fg-default">
              {t("artifacts.workspace.openComments")}
            </span>
          </button>
          <button
            type="button"
            className={tileClass}
            onClick={() => onSelect(PROPOSALS_PANEL)}
          >
            <span className="flex size-8 items-center justify-center rounded-lg bg-surface-muted text-fg-secondary transition-colors group-hover:bg-accent-soft group-hover:text-accent">
              <Icon name="layers" size={16} />
            </span>
            <span className="text-body-sm font-medium text-fg-default">
              {t("artifacts.workspace.openProposals")}
            </span>
          </button>
        </div>
        {recent.length > 0 && (
          <div className="space-y-2">
            <p className="px-0.5 text-caption font-medium text-fg-muted">
              {t("artifacts.workspace.recentArtifacts")}
            </p>
            <ul className="overflow-hidden rounded-xl border border-line-subtle bg-surface-card shadow-card divide-y divide-line-subtle">
              {recent.map((item) => (
                <li key={item.id}>
                  <button
                    type="button"
                    aria-label={item.name}
                    onClick={() => onSelect(currentTabKey(item.content_id))}
                    className="group flex w-full min-w-0 items-center gap-2.5 px-3 py-2.5 text-left transition-colors hover:bg-surface-hover/60"
                  >
                    <span className="flex size-7 shrink-0 items-center justify-center rounded-lg bg-surface-muted text-fg-muted group-hover:text-fg-default">
                      <Icon
                        name={
                          artifactPreviewKind(item) === "image"
                            ? "image"
                            : "file-text"
                        }
                        size={14}
                      />
                    </span>
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate text-body-sm font-medium text-fg-default">
                        {item.name}
                      </span>
                      <span className="truncate text-caption text-fg-muted">
                        {formatBytes(item.size_bytes)} ·{" "}
                        {formatRelative(item.created_at)}
                      </span>
                    </span>
                    <Icon
                      name="chevron-right"
                      size={14}
                      className="shrink-0 text-fg-subtle opacity-0 transition-opacity group-hover:opacity-100"
                    />
                  </button>
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </div>
  );
}

function Tab({
  id,
  label,
  badge,
  description,
  selected,
  onSelect,
  onClose,
  closeLabel,
  children,
  icon,
  onReorder,
}: {
  id: string;
  label: string;
  badge?: string;
  description?: string;
  selected: boolean;
  onSelect: () => void;
  onClose?: () => void;
  closeLabel?: string;
  children: React.ReactNode;
  icon: "globe" | "folder" | "plus" | "file-text" | "message-circle" | "layers";
  onReorder: (id: string, before: string) => void;
}) {
  const tabRef = useRef<HTMLDivElement>(null);
  const [dragOver, setDragOver] = useState(false);
  useEffect(() => {
    const tab = tabRef.current;
    if (!selected || !tab) return;
    const reveal = () =>
      tab.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    reveal();
    // 恢复工作区或缩放宽度时确保当前选中标签滚动进可视区域。
    if (!tab.parentElement || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(reveal);
    observer.observe(tab.parentElement);
    return () => observer.disconnect();
  }, [selected]);
  return (
    <div
      ref={tabRef}
      draggable
      onDragStart={(event) => {
        event.dataTransfer.setData("application/openfox-panel-tab", id);
        event.dataTransfer.effectAllowed = "move";
      }}
      onDragOver={(event) => {
        if (
          event.dataTransfer.types.includes("application/openfox-panel-tab")
        ) {
          event.preventDefault();
          setDragOver(true);
        }
      }}
      onDragLeave={() => setDragOver(false)}
      onDrop={(event) => {
        setDragOver(false);
        const source = event.dataTransfer.getData(
          "application/openfox-panel-tab",
        );
        if (source) {
          event.preventDefault();
          onReorder(source, id);
        }
      }}
      onAuxClick={(event) => {
        if (event.button === 1) {
          event.preventDefault();
          onClose?.();
        }
      }}
      className={cn(
        "group flex h-[30px] min-w-[96px] max-w-[min(184px,calc(100cqw-100px))] shrink-0 items-center rounded-lg border text-body-sm transition-colors",
        selected
          ? "border-line-subtle bg-surface-card text-fg-default shadow-card"
          : "border-transparent text-fg-muted hover:bg-surface-hover/70 hover:text-fg-default",
        dragOver && "border-l-2 border-l-accent",
      )}
    >
      <button
        type="button"
        role="tab"
        aria-selected={selected}
        aria-label={label}
        aria-description={description}
        title={description ? `${label} · ${description}` : label}
        onClick={onSelect}
        className="flex h-full min-w-0 flex-1 items-center gap-1.5 px-2"
      >
        <Icon
          name={icon}
          size={13}
          className={cn(
            "shrink-0",
            selected ? "text-fg-secondary" : "text-fg-muted",
          )}
        />
        <span className="truncate">{children}</span>
        {badge && (
          <span
            aria-hidden
            className="shrink-0 rounded bg-surface-muted px-1 font-mono text-[10px] leading-4 text-fg-muted"
          >
            {badge}
          </span>
        )}
      </button>
      {onClose && (
        <button
          type="button"
          aria-label={closeLabel}
          title={closeLabel}
          onClick={onClose}
          className={cn(
            "mr-1 flex size-5 shrink-0 items-center justify-center rounded text-fg-muted transition-opacity hover:bg-surface-muted hover:text-fg-default",
            selected
              ? "opacity-75 hover:opacity-100"
              : "opacity-45 group-hover:opacity-100 focus-visible:opacity-100",
          )}
        >
          <Icon name="x" size={12} />
        </button>
      )}
    </div>
  );
}

function ArtifactList({
  artifacts,
  onOpen,
}: {
  artifacts: readonly RunArtifact[];
  onOpen: (id: string) => void;
}) {
  const t = useT();
  const { formatRelative } = useI18n();
  if (artifacts.length === 0)
    return (
      <p className="p-6 text-body-sm text-fg-muted">
        {t("artifacts.list.empty")}
      </p>
    );
  return (
    <ul className="flex min-h-0 flex-1 flex-col overflow-y-auto p-2">
      {artifacts.map((artifact) => {
        const expired = artifactExpired(artifact);
        return (
          <li key={artifact.id}>
            <button
              type="button"
              aria-label={t("artifacts.list.open", { name: artifact.name })}
              onClick={() => onOpen(artifact.id)}
              className="flex w-full min-w-0 items-center gap-3 rounded-md px-2 py-2 text-left hover:bg-surface-hover"
            >
              <Icon
                name={
                  artifactPreviewKind(artifact) === "image"
                    ? "image"
                    : "file-text"
                }
                className="shrink-0 text-fg-muted"
              />
              <span className="flex min-w-0 flex-1 flex-col">
                <span
                  className={cn(
                    "truncate text-body-sm font-medium",
                    expired && "text-fg-muted line-through",
                  )}
                >
                  {artifact.name}
                </span>
                <span className="truncate text-caption text-fg-muted">
                  {expired
                    ? t("artifacts.preview.expired")
                    : `${formatBytes(artifact.size_bytes)} · ${formatRelative(artifact.created_at)}`}
                </span>
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}

export function ArtifactPanel(
  props: Parameters<typeof ArtifactPanelContent>[0],
) {
  return (
    <AppErrorBoundary key={props.conversationId}>
      <PreviewStateKey.Provider value={`panel:${props.conversationId}`}>
        <ArtifactPanelContent key={props.conversationId} {...props} />
      </PreviewStateKey.Provider>
    </AppErrorBoundary>
  );
}
