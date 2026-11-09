import {
  useCallback,
  useEffect,
  useId,
  useRef,
  type CSSProperties,
} from "react";

import type { RunArtifact } from "@openfox/api";
import { useI18n, useT } from "@openfox/i18n";
import { Button, Icon, IconButton, Skeleton, cn } from "@openfox/ui";

import { formatBytes } from "../steps/cards";
import { ArtifactPreview } from "./ArtifactPreview";
import { artifactExpired, artifactPreviewKind } from "./model";
import { ARTIFACT_LIST, type ArtifactPanelState } from "./params";
import { useConversationArtifacts } from "./queries";
import { PanelResizeHandle } from "../../shell/PanelResizeHandle";
import { usePanelWidth } from "../../shell/panelWidth";
import { localKeys } from "../../storage";

// 会话右侧的产物面板：「产物 N 个」列表标签 + 可关闭的文件标签。
// 宽容器里与消息流并排；窄容器（任务全页的侧边会话、小窗口）里覆盖在消息流上。

/** 中心返回的会话产物上限（listConversationArtifacts maxItems）。 */
const MAX_LISTED = 200;

export function ArtifactPanel({
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
  const resize = usePanelWidth({
    storageKey: localKeys.artifactPanelWidth,
    initial: 480,
    min: 280,
    max: 960,
    remaining: 400,
  });
  const containerRef = resize.containerRef;
  const panelRef = useCallback(
    (node: HTMLElement | null) => containerRef(node?.parentElement ?? null),
    [containerRef],
  );
  const query = useConversationArtifacts(orgId, conversationId);
  const artifacts = query.data;
  const byId = new Map((artifacts ?? []).map((item) => [item.id, item]));
  const selected = panel.selected ?? ARTIFACT_LIST;
  const current = selected === ARTIFACT_LIST ? undefined : byId.get(selected);

  // 地址里的产物不在列表里：可能是刚发布、列表还没刷新，先重拉一次；仍然没有（换了会话、链接写错）再静默移除
  const { forget } = panel;
  const { refetch, isFetching } = query;
  const rechecked = useRef(new Set<string>());
  useEffect(() => {
    if (!artifacts || isFetching) return;
    const known = new Set(artifacts.map((item) => item.id));
    const missing = panel.tabs.filter((id) => !known.has(id));
    if (missing.length === 0) return;
    const fresh = missing.filter((id) => !rechecked.current.has(id));
    if (fresh.length > 0) {
      for (const id of fresh) rechecked.current.add(id);
      void refetch();
      return;
    }
    for (const id of missing) forget(id);
  }, [artifacts, isFetching, panel.tabs, forget, refetch]);

  return (
    <aside
      ref={panelRef}
      id={panelId}
      aria-label={t("artifacts.panel.label")}
      style={{ "--artifact-panel-width": `${resize.width}px` } as CSSProperties}
      className="absolute inset-0 z-20 flex min-h-0 min-w-0 flex-col bg-surface-card @[720px]:relative @[720px]:w-[var(--artifact-panel-width)] @[720px]:shrink-0 @[720px]:border-l @[720px]:border-line-default"
    >
      <PanelResizeHandle
        {...resize}
        label={t("artifacts.panel.resize")}
        controls={panelId}
        side="right"
        onCollapse={panel.closePanel}
        className="absolute inset-y-0 -left-1 hidden w-2 @[720px]:block"
      />
      <div className="flex h-11 shrink-0 items-center gap-1 border-b border-line-default px-2">
        <div
          role="tablist"
          aria-label={t("artifacts.panel.tabs")}
          className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto"
        >
          <Tab
            selected={selected === ARTIFACT_LIST}
            onSelect={() => panel.select(ARTIFACT_LIST)}
          >
            {t("artifacts.panel.list", { count: artifacts?.length ?? 0 })}
          </Tab>
          {panel.tabs.map((id) => {
            const artifact = byId.get(id);
            if (!artifact) return null;
            return (
              <Tab
                key={id}
                selected={selected === id}
                onSelect={() => panel.select(id)}
                onClose={() => panel.closeTab(id)}
                closeLabel={t("artifacts.panel.closeTab", {
                  name: artifact.name,
                })}
              >
                {artifact.name}
              </Tab>
            );
          })}
        </div>
        <IconButton
          label={t("artifacts.panel.close")}
          icon={<Icon name="x" size={16} />}
          size="28"
          variant="ghost"
          onClick={panel.closePanel}
        />
      </div>
      <div className="flex min-h-0 flex-1 flex-col">
        {query.isError && !artifacts ? (
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
        ) : current ? (
          <ArtifactPreview key={current.id} orgId={orgId} artifact={current} />
        ) : (
          <ArtifactList
            artifacts={artifacts}
            onOpen={(id) => panel.openArtifact(id)}
          />
        )}
        {!current && artifacts && artifacts.length >= MAX_LISTED && (
          <p className="shrink-0 px-4 pb-3 text-caption text-fg-muted">
            {t("artifacts.list.more", { count: MAX_LISTED })}
          </p>
        )}
      </div>
    </aside>
  );
}

function Tab({
  selected,
  onSelect,
  onClose,
  closeLabel,
  children,
}: {
  selected: boolean;
  onSelect: () => void;
  onClose?: () => void;
  closeLabel?: string;
  children: React.ReactNode;
}) {
  return (
    <div
      className={cn(
        "flex h-7 max-w-[200px] shrink-0 items-center rounded-md text-body-sm",
        selected
          ? "bg-surface-active font-medium text-fg-default"
          : "text-fg-secondary hover:bg-surface-hover",
      )}
    >
      <button
        type="button"
        role="tab"
        aria-selected={selected}
        onClick={onSelect}
        className="min-w-0 truncate px-2"
      >
        {children}
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
