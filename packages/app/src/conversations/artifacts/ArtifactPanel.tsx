import { useApi, keys } from "../../context";
import { NativeCanvas } from "../canvas/NativeCanvas";
import { DocumentPicker } from "../canvas/DocumentPicker";
import { documentTab } from "./params";
import { proposalKey } from "../proposals/WorkspaceProposals";
import { ProposalPreview } from "../proposals/ProposalPreview";
import { isProposalTab } from "./params";
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
import {
  currentTabKey,
  isCurrentTab,
  ARTIFACT_LIST,
  BROWSER_PANEL,
  NEW_PANEL,
  browserTabKey,
  isBrowserTab,
  isArtifactTab,
  type ArtifactPanelState,
} from "./params";
import { desktopBridge } from "../../desktop";
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

// 会话右侧工作区：按需打开的文件、网页和产物列表共用动态标签栏。
// 宽容器里与消息流并排；窄容器（任务全页的侧边会话、小窗口）里覆盖在消息流上。

/** 中心返回的会话产物上限（listConversationArtifacts maxItems）。 */
const MAX_LISTED = 200;

function ArtifactPanelContent({
  orgId,
  conversationId,
  panel,
}: {
  orgId: string;
  conversationId: string;
  panel: ArtifactPanelState;
}) {
  const t = useT();
  const panelId = useId();
  const [expanded, setExpanded] = useViewState("expanded", false);
  const resize = usePanelWidth({
    storageKey: localKeys.artifactPanelWidth,
    initial: 960,
    min: 280,
    max: 1280,
    remaining: 360,
  });
  const containerRef = resize.containerRef;
  const panelRef = useCallback(
    (node: HTMLElement | null) => containerRef(node?.parentElement ?? null),
    [containerRef],
  );
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

  return (
    <aside
      ref={panelRef}
      id={panelId}
      aria-label={t("artifacts.panel.label")}
      style={{ "--artifact-panel-width": `${resize.width}px` } as CSSProperties}
      className={cn(
        "absolute inset-0 z-20 flex min-h-0 min-w-0 flex-col bg-surface-card",
        !expanded &&
          "@[720px]:relative @[720px]:w-[var(--artifact-panel-width)] @[720px]:shrink-0 @[720px]:border-l @[720px]:border-line-default",
      )}
    >
      <PanelResizeHandle
        {...resize}
        label={t("artifacts.panel.resize")}
        controls={panelId}
        side="right"
        onCollapse={panel.closePanel}
        className={cn(
          "absolute inset-y-0 -left-1 hidden w-2",
          !expanded && "@[720px]:block",
        )}
      />
      <div className="@container/tabs flex h-11 shrink-0 items-center gap-1 border-b border-line-default px-2">
        <div
          role="tablist"
          aria-label={t("artifacts.panel.tabs")}
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
                    void browser.command({ action: "focus", tabId: webTab.id });
                }}
                onClose={() => {
                  if (webTab)
                    void browser.command({ action: "close", tabId: webTab.id });
                  else panel.closeTab(id);
                }}
                onReorder={panel.reorder}
                icon={
                  webTab
                    ? "globe"
                    : id === ARTIFACT_LIST
                      ? "folder"
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
          icon={<Icon name="plus" size={16} />}
          size="28"
          variant="ghost"
          onClick={() => panel.select(NEW_PANEL)}
        />
        <div className="min-w-0 flex-1" />
        <IconButton
          label={t(
            expanded ? "artifacts.panel.restore" : "artifacts.panel.expand",
          )}
          icon={
            <Icon name={expanded ? "minimize-2" : "maximize-2"} size={16} />
          }
          size="28"
          variant="ghost"
          active={expanded}
          onClick={() => setExpanded((value) => !value)}
        />
        <IconButton
          label={t("artifacts.panel.close")}
          icon={<Icon name="panel-left" size={16} className="rotate-180" />}
          size="28"
          variant="ghost"
          onClick={panel.closePanel}
        />
      </div>
      <div className="flex min-h-0 flex-1 flex-col">
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
        ) : selected === NEW_PANEL ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-3 p-6">
            <p className="mb-2 text-body text-fg-muted">
              {t("artifacts.workspace.choose")}
            </p>
            <DocumentPicker
              orgId={orgId}
              conversationId={conversationId}
              onOpen={panel.select}
            />
            {browserAvailable && (
              <Button variant="outline" onClick={openBrowser}>
                <Icon name="globe" size={16} />
                {t("artifacts.workspace.openBrowser")}
              </Button>
            )}
            <Button
              variant="outline"
              onClick={() => panel.select(ARTIFACT_LIST)}
            >
              <Icon name="folder" size={16} />
              {t("artifacts.workspace.openArtifacts", {
                count: visible.length,
              })}
            </Button>
          </div>
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
  icon: "globe" | "folder" | "plus" | "file-text";
  onReorder: (id: string, before: string) => void;
}) {
  const tabRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const tab = tabRef.current;
    if (!selected || !tab) return;
    const reveal = () =>
      tab.scrollIntoView?.({ block: "nearest", inline: "nearest" });
    reveal();
    // Restoring the workspace can resize the strip after the tabs first mount.
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
        if (event.dataTransfer.types.includes("application/openfox-panel-tab"))
          event.preventDefault();
      }}
      onDrop={(event) => {
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
        "flex h-8 w-44 max-w-[min(240px,calc(100cqw-100px))] shrink-0 items-center rounded-lg border text-body-sm",
        selected
          ? "border-line-default bg-surface-active text-fg-default"
          : "border-transparent text-fg-secondary hover:bg-surface-hover",
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
        className="flex h-full min-w-0 flex-1 items-center gap-2 px-2.5"
      >
        <Icon name={icon} size={14} className="shrink-0" />
        <span className="truncate">{children}</span>
        {badge && (
          <span aria-hidden className="shrink-0 text-[10px] text-fg-muted">
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
          className="mr-1 flex size-5 shrink-0 items-center justify-center rounded text-fg-muted hover:bg-surface-muted hover:text-fg-default"
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
