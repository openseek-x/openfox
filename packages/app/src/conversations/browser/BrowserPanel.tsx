import {
  useStoredValue,
  rebindKey,
  type Rebind,
} from "../comments/resolutionState";
import { workspaceStore } from "../workspaceStorage";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useT } from "@openfox/i18n";
import {
  Button,
  Icon,
  IconButton,
  TextInput,
  Menu,
  MenuItem,
  MenuLabel,
  MenuSeparator,
  cn,
} from "@openfox/ui";
import { BrowserAnnotationEditor } from "./BrowserAnnotationEditor";
import { desktopBridge, type BrowserAnnotationMode } from "../../desktop";
import { isMacPlatform } from "../../me/shortcuts";
import { copyText } from "../../tasks/parts";
import { useDraftBrowserContexts } from "../drafts";
import type { BrowserSession } from "./useBrowserSession";
import { useConversation } from "../queries";
import { useRun } from "../steps/queries";
import { activeRunId, isActiveRunState } from "../model";

export function BrowserPanel({ session }: { session: BrowserSession }) {
  const t = useT();
  const { state, error, command, retry, disarm } = session;
  const conversation = useConversation(session.orgId, session.conversationId);
  const activeRun = activeRunId(conversation.data);
  const run = useRun(session.orgId, state?.control?.runId || activeRun || "");
  const control = state?.control;
  const binding = useStoredValue<Rebind>(rebindKey(session.conversationId));
  const annotation = state?.annotation;
  const selection = annotation?.selection;
  const draftBrowsers = useDraftBrowserContexts(session.conversationId);
  const pendingAnnotations = draftBrowsers.reduce(
    (sum, item) => sum + item.comments.length,
    0,
  );
  const [address, setAddress] = useState("");
  const [addressFocused, setAddressFocused] = useState(false);
  const [urlCopied, setUrlCopied] = useState(false);
  const [externalError, setExternalError] = useState(false);
  const viewport = useRef<HTMLDivElement>(null);
  const addressInput = useRef<HTMLInputElement>(null);
  const lastLayout = useRef<{
    visible: boolean;
    occluded: boolean;
    x: number;
    y: number;
    width: number;
    height: number;
  } | null>(null);
  const active = state?.tabs.find((tab) => tab.id === state.activeTab);
  const hasRealUrl = Boolean(active?.url && active.url !== "about:blank");
  const controlBannerVisible = Boolean(control?.active || session.armed);
  let currentOrigin: string | null = null;
  try {
    if (active?.url && /^https?:/.test(active.url))
      currentOrigin = new URL(active.url).origin;
  } catch {
    /* A failed address has no grantable origin. */
  }
  const availableOrigins = [
    ...new Set(
      (state?.tabs || []).flatMap((tab) => {
        try {
          return /^https?:/.test(tab.url) ? [new URL(tab.url).origin] : [];
        } catch {
          return [];
        }
      }),
    ),
  ];
  const controlStatus = control?.active
    ? t(
        control.runId
          ? "artifacts.browser.agentControl"
          : "artifacts.browser.waitingRun",
      )
    : session.armed
      ? t("artifacts.browser.nextMessage")
      : t("artifacts.browser.userControl");
  const controlLabel =
    control?.active || session.armed
      ? t("artifacts.browser.takeOver")
      : t(
          control?.pending
            ? "artifacts.browser.connecting"
            : control?.error === "BINDING_CONFLICT"
              ? "artifacts.browser.replaceBinding"
              : "artifacts.browser.allowControl",
        );
  const controlDisabled =
    !currentOrigin ||
    annotation?.enabled ||
    control?.pending ||
    Boolean(activeRun && (!run.data || run.data.state !== "RUNNING"));
  const changeControl = () => {
    if (control?.active || session.armed) {
      disarm();
      void command({ action: "take-over" });
    } else if (currentOrigin) {
      if (activeRun && run.data?.id === activeRun) {
        void command({
          action: "allow-control",
          runId: activeRun,
          generation: run.data.generation,
          origins: availableOrigins,
          replace: control?.error === "BINDING_CONFLICT",
        });
      } else
        session.arm(availableOrigins, control?.error === "BINDING_CONFLICT");
    }
  };
  const downloads = (
    <div className="max-h-64 overflow-y-auto">
      {!state?.downloads.length && (
        <p className="px-2.5 py-3 text-body-sm text-fg-muted">
          {t("artifacts.browser.noDownloads")}
        </p>
      )}
      {state?.downloads.map((file) => (
        <MenuItem
          key={file.id}
          icon={<Icon name="file-text" size={16} />}
          description={t(
            file.state === "completed"
              ? "artifacts.browser.downloadComplete"
              : file.state === "progressing"
                ? "artifacts.browser.downloading"
                : "artifacts.browser.downloadFailed",
          )}
          disabled={file.state !== "completed"}
          trailing={
            file.state === "completed" ? (
              <Icon name="folder" size={14} />
            ) : undefined
          }
          onSelect={() =>
            void command({
              action: "reveal-download",
              downloadId: file.id,
            })
          }
        >
          {file.filename}
        </MenuItem>
      ))}
    </div>
  );
  useLayoutEffect(() => {
    if (addressFocused) addressInput.current?.select();
  }, [addressFocused]);
  useEffect(() => () => disarm(), [disarm]);
  useEffect(() => {
    if (
      control?.active &&
      control.runId &&
      run.data?.id === control.runId &&
      !isActiveRunState(run.data.state)
    )
      void command({ action: "take-over" });
  }, [control?.active, control?.runId, run.data?.id, run.data?.state, command]);
  useEffect(
    () =>
      desktopBridge()?.onBrowserFocusAddress?.((event) => {
        if (event.lease === state?.lease) {
          addressInput.current?.focus();
          addressInput.current?.select();
        }
      }),
    [state?.lease],
  );
  useEffect(() => {
    setAddress(active?.url === "about:blank" ? "" : active?.url || "");
    setExternalError(false);
  }, [active?.id, active?.url]);
  useEffect(() => {
    if (!urlCopied) return;
    const timer = window.setTimeout(() => setUrlCopied(false), 1600);
    return () => window.clearTimeout(timer);
  }, [urlCopied]);
  const copyCurrentUrl = async () => {
    if (!hasRealUrl || !active?.url) return;
    if (await copyText(active.url)) setUrlCopied(true);
  };
  const openExternal = async () => {
    if (!active || !currentOrigin || !state?.lease) return;
    setExternalError(false);
    try {
      await desktopBridge()?.browserCommand?.({
        lease: state.lease,
        action: "open-external",
        tabId: active.id,
      });
    } catch {
      setExternalError(true);
    }
  };
  useEffect(() => {
    if (active?.url === "about:blank") addressInput.current?.focus();
  }, [active?.id, active?.url]);
  useEffect(() => {
    const element = viewport.current;
    if (!element || !state?.lease) return;
    lastLayout.current = null;
    let frame = 0;
    const layout = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const rect = element.getBoundingClientRect();
        const isShown = (node: Element) =>
          node.getClientRects().length > 0 &&
          getComputedStyle(node).visibility !== "hidden";
        // 模态弹窗与设置抽屉表示离开当前浏览上下文，隐藏原生视图并结束共用；
        // 下拉菜单（「更多」「下载」）与列表仅瞬时遮挡视口，隐藏原生视图以免遮盖菜单但不撤权。
        const modalOccluded = [
          ...document.querySelectorAll(
            '[role="dialog"], [role="alertdialog"], [data-openfox-settings-drawer]',
          ),
        ].some(isShown);
        const menuOccluded = [
          ...document.querySelectorAll('[role="menu"], [role="listbox"]'),
        ].some(isShown);
        const hasBounds = rect.width >= 1 && rect.height >= 1;
        const visible =
          !document.hidden && !modalOccluded && !menuOccluded && hasBounds;
        // 窗口真正 hide/minimize 由主进程窗口事件撤权；此处允许瞬时菜单与外部应用覆盖保留共用
        const occluded =
          !visible &&
          hasBounds &&
          !modalOccluded &&
          (menuOccluded || document.hidden);
        const next = {
          visible,
          occluded,
          x: Math.round(rect.x),
          y: Math.round(rect.y),
          width: Math.round(rect.width),
          height: Math.round(rect.height),
        };
        const prev = lastLayout.current;
        // 仅在可见性、遮挡类型或像素边界真实变化时下发原生布局，避免流式输出触发无意义 IPC
        if (
          prev &&
          prev.visible === next.visible &&
          prev.occluded === next.occluded &&
          (!next.visible ||
            (prev.x === next.x &&
              prev.y === next.y &&
              prev.width === next.width &&
              prev.height === next.height))
        )
          return;
        lastLayout.current = next;
        void desktopBridge()
          ?.browserCommand?.({
            lease: state.lease,
            action: "layout",
            visible,
            ...(occluded ? { occluded: true } : {}),
            ...(visible
              ? {
                  bounds: {
                    x: next.x,
                    y: next.y,
                    width: next.width,
                    height: next.height,
                  },
                }
              : {}),
          })
          .catch(() => {});
      });
    };
    const observer = new ResizeObserver(layout);
    observer.observe(element);
    const overlays = new MutationObserver(layout);
    overlays.observe(document.body, { childList: true, subtree: true });
    window.addEventListener("resize", layout);
    document.addEventListener("visibilitychange", layout);
    layout();
    return () => {
      cancelAnimationFrame(frame);
      lastLayout.current = null;
      void desktopBridge()
        ?.browserCommand?.({ lease: state.lease, action: "hide" })
        .catch(() => {});
      observer.disconnect();
      overlays.disconnect();
      window.removeEventListener("resize", layout);
      document.removeEventListener("visibilitychange", layout);
    };
  }, [state?.lease]);
  const annotationMode: BrowserAnnotationMode = annotation?.mode ?? "element";
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (
        event.key === "Escape" &&
        annotation?.enabled &&
        !selection &&
        !addressFocused
      ) {
        event.preventDefault();
        void command({
          action: "annotate",
          enabled: false,
          mode: annotationMode,
        });
        return;
      }
      if (!(event.metaKey || event.ctrlKey)) return;
      if (event.key.toLowerCase() === "l") {
        event.preventDefault();
        addressInput.current?.focus();
        addressInput.current?.select();
      }
      if (event.key.toLowerCase() === "t") {
        event.preventDefault();
        void command({ action: "open" });
      }
      if (event.key.toLowerCase() === "w" && active) {
        event.preventDefault();
        void command({ action: "close", tabId: active.id });
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [
    command,
    active,
    annotation?.enabled,
    annotationMode,
    selection,
    addressFocused,
  ]);
  const annotationHint =
    annotationMode === "text"
      ? t("artifacts.browser.annotateHintText")
      : annotationMode === "region"
        ? t("artifacts.browser.annotateHintRegion")
        : t("artifacts.browser.annotateHintElement");
  const showBottomDock =
    controlBannerVisible ||
    Boolean(annotation?.enabled && !selection) ||
    Boolean(binding && !selection);
  return (
    <div
      className="flex min-h-0 flex-1 flex-col"
      aria-label={t("artifacts.browser.label")}
    >
      <form
        aria-label={t("artifacts.browser.navigation")}
        className="@container/browser relative flex h-11 min-w-0 shrink-0 items-center gap-1.5 border-b border-line-subtle bg-surface-canvas px-2"
        onSubmit={(event) => {
          event.preventDefault();
          void command(
            active
              ? { action: "navigate", tabId: active.id, url: address }
              : { action: "open", url: address },
          );
          addressInput.current?.blur();
        }}
      >
        <div className="flex shrink-0 items-center gap-0.5">
          <IconButton
            label={t("artifacts.browser.back")}
            icon={<Icon name="arrow-left" size={15} />}
            size="28"
            variant="ghost"
            disabled={!active?.canBack}
            onClick={() => void command({ action: "back", tabId: active?.id })}
          />
          <IconButton
            label={t("artifacts.browser.forward")}
            icon={<Icon name="arrow-right" size={15} />}
            size="28"
            variant="ghost"
            disabled={!active?.canForward}
            onClick={() =>
              void command({ action: "forward", tabId: active?.id })
            }
          />
          <IconButton
            label={t(
              active?.loading
                ? "artifacts.browser.stop"
                : "artifacts.browser.reload",
            )}
            icon={
              <Icon name={active?.loading ? "x" : "rotate-ccw"} size={14} />
            }
            size="28"
            variant="ghost"
            disabled={!active}
            onClick={() =>
              void command({
                action: active?.loading ? "stop" : "reload",
                tabId: active?.id,
              })
            }
          />
        </div>
        <div className="relative flex min-w-0 flex-1 items-center">
          <TextInput
            ref={addressInput}
            aria-label={t("artifacts.browser.address")}
            placeholder={t("artifacts.browser.address")}
            value={
              addressFocused
                ? address
                : currentOrigin
                  ? new URL(currentOrigin).host
                  : address
            }
            disabled={!state}
            onChange={(event) => setAddress(event.target.value)}
            onFocus={() => setAddressFocused(true)}
            onBlur={() => setAddressFocused(false)}
            onKeyDown={(event) => {
              if (event.key === "Escape") {
                event.stopPropagation();
                setAddress(
                  active?.url === "about:blank" ? "" : active?.url || "",
                );
                event.currentTarget.blur();
              }
            }}
            className={cn(
              "h-8 min-w-0 flex-1 rounded-lg! border-line-subtle! bg-surface-card! px-2.5 text-body-sm shadow-card",
              !addressFocused && "[&_input]:text-center",
              (addressFocused ? Boolean(address) : hasRealUrl) && "pr-8",
            )}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
          />
          {addressFocused && address ? (
            <button
              type="button"
              aria-label={t("artifacts.browser.clearAddress")}
              title={t("artifacts.browser.clearAddress")}
              onMouseDown={(event) => event.preventDefault()}
              onClick={() => {
                setAddress("");
                addressInput.current?.focus();
              }}
              className="absolute right-1.5 flex size-5 items-center justify-center rounded text-fg-muted hover:bg-surface-hover hover:text-fg-default"
            >
              <Icon name="x" size={12} />
            </button>
          ) : !addressFocused && hasRealUrl ? (
            <button
              type="button"
              aria-label={t(
                urlCopied
                  ? "artifacts.browser.urlCopied"
                  : "artifacts.browser.copyUrl",
              )}
              title={t(
                urlCopied
                  ? "artifacts.browser.urlCopied"
                  : "artifacts.browser.copyUrl",
              )}
              onClick={() => void copyCurrentUrl()}
              className={cn(
                "absolute right-1.5 hidden size-6 items-center justify-center rounded-md text-fg-muted transition-colors hover:bg-surface-hover hover:text-fg-default @[360px]/browser:flex",
                urlCopied && "text-accent!",
              )}
            >
              <Icon name={urlCopied ? "check" : "copy"} size={13} />
            </button>
          ) : null}
        </div>
        <button
          type="submit"
          className="sr-only"
          tabIndex={-1}
          disabled={!state}
        >
          {t("artifacts.browser.go")}
        </button>
        <div className="flex shrink-0 items-center gap-0.5">
          <div className="relative shrink-0">
            <IconButton
              label={t("artifacts.browser.annotate")}
              title={t(
                annotation?.enabled
                  ? "artifacts.browser.stopSelecting"
                  : "artifacts.browser.selectElement",
              )}
              icon={<Icon name="hifox-scan-eye" size={16} />}
              size="28"
              variant="ghost"
              active={annotation?.enabled ?? false}
              aria-pressed={annotation?.enabled ?? false}
              className={cn(
                "rounded-lg!",
                annotation?.enabled && "bg-accent-soft! text-accent!",
              )}
              disabled={!currentOrigin}
              onClick={() =>
                void command({
                  action: "annotate",
                  enabled: !annotation?.enabled,
                  mode: annotation?.enabled ? annotationMode : "element",
                })
              }
            />
            {pendingAnnotations > 0 && (
              <span
                aria-hidden="true"
                className="pointer-events-none absolute -top-0.5 -right-0.5 flex h-3.5 min-w-3.5 items-center justify-center rounded-full bg-accent px-1 text-[10px] font-medium leading-none text-fg-inverse"
              >
                {pendingAnnotations > 99 ? "99+" : pendingAnnotations}
              </span>
            )}
          </div>
          {!controlBannerVisible && (
            <div className="hidden @[400px]/browser:flex">
              <IconButton
                label={controlLabel}
                title={controlLabel}
                icon={<Icon name="bot" size={16} />}
                size="28"
                variant="ghost"
                disabled={controlDisabled}
                onClick={changeControl}
              />
            </div>
          )}
          <div className="hidden @[440px]/browser:flex">
            <Menu
              label={t("artifacts.browser.downloads")}
              placement="bottom-end"
              className="w-72 max-w-[calc(100vw-16px)]"
              trigger={({ ref, open, toggle }) => (
                <IconButton
                  ref={ref}
                  label={t("artifacts.browser.downloads")}
                  icon={<Icon name="download" size={15} />}
                  size="28"
                  variant="ghost"
                  active={open}
                  aria-expanded={open}
                  aria-haspopup="menu"
                  onClick={toggle}
                />
              )}
            >
              <MenuLabel>{t("artifacts.browser.downloads")}</MenuLabel>
              {downloads}
            </Menu>
          </div>
          <Menu
            label={t("artifacts.browser.more")}
            placement="bottom-end"
            className="w-64 max-w-[calc(100vw-16px)]"
            trigger={({ ref, open, toggle }) => (
              <IconButton
                ref={ref}
                label={t("artifacts.browser.more")}
                icon={<Icon name="more" size={16} />}
                size="28"
                variant="ghost"
                active={open}
                aria-expanded={open}
                aria-haspopup="menu"
                onClick={toggle}
              />
            )}
          >
            <MenuItem
              icon={<Icon name={urlCopied ? "check" : "copy"} size={16} />}
              disabled={!hasRealUrl}
              onSelect={() => void copyCurrentUrl()}
            >
              {t("artifacts.browser.copyUrl")}
            </MenuItem>
            <MenuItem
              icon={<Icon name="external-link" size={16} />}
              disabled={!currentOrigin}
              onSelect={() => void openExternal()}
            >
              {t("artifacts.browser.openExternal")}
            </MenuItem>
            <MenuSeparator />
            <MenuLabel>{controlStatus}</MenuLabel>
            <MenuItem
              icon={
                <Icon
                  name={controlBannerVisible ? "shield" : "bot"}
                  size={16}
                />
              }
              disabled={controlBannerVisible ? false : controlDisabled}
              onSelect={changeControl}
            >
              {controlLabel}
            </MenuItem>
            <p className="px-2.5 pb-2 text-caption text-fg-muted">
              {t("artifacts.browser.privateSession")}
            </p>
            <MenuSeparator />
            <MenuLabel>{t("artifacts.browser.downloads")}</MenuLabel>
            {downloads}
            <MenuSeparator />
            <MenuItem
              icon={<Icon name="trash-2" size={16} />}
              disabled={!state}
              onSelect={() => {
                if (window.confirm(t("artifacts.browser.clearConfirm")))
                  void command({ action: "clear" });
              }}
            >
              {t("artifacts.browser.clear")}
            </MenuItem>
          </Menu>
        </div>
        {active?.loading && (
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-x-0 bottom-0 h-[1.5px] overflow-hidden bg-accent/15"
          >
            <div className="h-full w-1/2 animate-pulse bg-accent" />
          </div>
        )}
      </form>
      {!controlBannerVisible && (
        <span role="status" className="sr-only">
          {controlStatus}
        </span>
      )}
      {externalError && (
        <p
          role="alert"
          className="shrink-0 border-b border-line-subtle bg-danger-soft/40 px-3 py-1.5 text-caption text-danger"
        >
          {t("artifacts.browser.openExternalFailed")}
        </p>
      )}
      {annotation?.error && (
        <p role="alert" className="shrink-0 px-3 py-2 text-body-sm text-danger">
          {t(
            annotation.error === "stale"
              ? "artifacts.browser.staleAnnotation"
              : "artifacts.browser.selectionFailed",
          )}
        </p>
      )}
      {selection && !selection.image && (
        <BrowserAnnotationEditor
          key={selection.id}
          selection={selection}
          session={session}
        />
      )}
      {control?.error && (
        <p
          role="alert"
          className="shrink-0 border-b border-line-default px-3 py-2 text-body-sm text-danger"
        >
          {t(
            control.error === "BINDING_CONFLICT"
              ? "artifacts.browser.bindingConflict"
              : control.error === "ORIGIN_NOT_ALLOWED"
                ? "artifacts.browser.originDenied"
                : "artifacts.browser.controlFailed",
          )}
        </p>
      )}
      {error && (
        <div
          role="alert"
          className="flex shrink-0 items-center justify-between gap-2 border-b border-line-default p-2 text-body-sm text-danger"
        >
          <span>
            {t(
              error === "limit"
                ? "artifacts.browser.limit"
                : error === "reset"
                  ? "artifacts.browser.reset"
                  : "artifacts.browser.failed",
            )}
          </span>
          <Button size="28" variant="ghost" onClick={retry}>
            {t("common.retry")}
          </Button>
        </div>
      )}
      <div ref={viewport} className="relative min-h-0 flex-1 bg-surface-card">
        {selection?.image && (
          <BrowserAnnotationEditor
            key={selection.id}
            selection={selection}
            session={session}
          />
        )}
        {active?.error ? (
          <div className="flex flex-col items-center gap-3 p-6 text-center text-body-sm">
            <Icon name="globe" />
            <p>
              {t(
                active.error === "NETWORK_DENIED"
                  ? "artifacts.browser.networkDenied"
                  : active.error === "PROTECTED_ENDPOINT"
                    ? "artifacts.browser.protectedEndpoint"
                    : active.error === "PUBLIC_DNS_FAILED"
                      ? "artifacts.browser.publicDnsFailed"
                      : active.error === "DNS_FAILED"
                        ? "artifacts.browser.dnsFailed"
                        : "artifacts.browser.loadFailed",
              )}
            </p>
            <Button
              size="28"
              variant="outline"
              onClick={() =>
                void command({ action: "reload", tabId: active.id })
              }
            >
              {t("common.retry")}
            </Button>
            {active.error === "NETWORK_DENIED" && active.canAllowLocal && (
              <Button
                size="28"
                variant="ghost"
                onClick={() =>
                  void command({
                    action: "allow-origin",
                    url: active.url,
                  }).then(() => command({ action: "reload", tabId: active.id }))
                }
              >
                {t("artifacts.browser.allowLocal")}
              </Button>
            )}
          </div>
        ) : (
          (!active || active.url === "about:blank") && (
            <div className="flex h-full flex-col items-center justify-center gap-4 bg-surface-canvas/30 p-8 text-center">
              <div className="flex size-12 items-center justify-center rounded-2xl border border-line-subtle bg-surface-card text-fg-muted shadow-card">
                <Icon name="globe" size={20} />
              </div>
              <div className="space-y-1">
                <p className="text-body font-medium text-fg-default">
                  {t("artifacts.browser.empty")}
                </p>
                <p className="text-caption text-fg-muted">
                  {t("artifacts.browser.emptyHint", {
                    focus: isMacPlatform() ? "⌘L" : "Ctrl+L",
                    newTab: isMacPlatform() ? "⌘T" : "Ctrl+T",
                  })}
                </p>
              </div>
              <Button
                size="28"
                variant="outline"
                disabled={!state}
                onClick={() => addressInput.current?.focus()}
              >
                {t("artifacts.browser.focusAddress")}
              </Button>
            </div>
          )
        )}
      </div>
      {showBottomDock && (
        <div className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-t border-line-subtle bg-surface-canvas px-3 py-1.5 text-caption">
          {annotation?.enabled && !selection && (
            <div className="flex min-w-0 flex-1 flex-wrap items-center justify-between gap-2">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <div
                  role="group"
                  aria-label={t("artifacts.browser.annotationMode")}
                  className="inline-flex shrink-0 items-center gap-0.5 rounded-lg border border-line-subtle bg-surface-muted p-0.5"
                >
                  {(
                    [
                      ["element", t("artifacts.browser.element")],
                      ["text", t("artifacts.browser.text")],
                      ["region", t("artifacts.browser.region")],
                    ] as const
                  ).map(([mode, label]) => (
                    <button
                      key={mode}
                      type="button"
                      aria-pressed={annotationMode === mode}
                      onClick={() =>
                        void command({
                          action: "annotate",
                          enabled: true,
                          mode,
                        })
                      }
                      className={cn(
                        "rounded-md px-2 py-0.5 text-caption transition-colors",
                        annotationMode === mode
                          ? "bg-surface-card font-medium text-accent shadow-card"
                          : "text-fg-secondary hover:text-fg-default",
                      )}
                    >
                      {label}
                    </button>
                  ))}
                </div>
                <span className="truncate text-fg-muted">{annotationHint}</span>
              </div>
              <div className="flex shrink-0 items-center gap-1.5">
                <kbd className="hidden rounded border border-line-subtle bg-surface-card px-1 py-px font-mono text-[10px] text-fg-muted sm:inline">
                  Esc
                </kbd>
                <Button
                  size="28"
                  variant="ghost"
                  className="h-6 px-2 text-caption"
                  onClick={() =>
                    void command({
                      action: "annotate",
                      enabled: false,
                      mode: annotationMode,
                    })
                  }
                >
                  {t("artifacts.browser.exitAnnotate")}
                </Button>
              </div>
            </div>
          )}
          {controlBannerVisible && (
            <div
              role="status"
              className="flex min-w-0 items-center gap-2 rounded-lg border border-accent/25 bg-accent-soft/50 px-2.5 py-1"
            >
              <span
                aria-hidden="true"
                className="size-1.5 shrink-0 rounded-full bg-accent"
              />
              <span className="truncate font-medium text-fg-default">
                {controlStatus}
              </span>
              <Button
                size="28"
                variant="outline"
                className="h-6 shrink-0 px-2 text-caption"
                onClick={changeControl}
              >
                {controlLabel}
              </Button>
            </div>
          )}
          {binding && !selection && (
            <div
              role="status"
              className="flex min-w-0 flex-1 items-center justify-between gap-2"
            >
              <span className="truncate text-fg-secondary">
                {t("artifacts.resolution.rebindHint")}
              </span>
              <Button
                size="28"
                variant="ghost"
                className="h-6 shrink-0 px-2 text-caption"
                onClick={() => {
                  workspaceStore().write(
                    rebindKey(session.conversationId),
                    undefined,
                  );
                  void command({
                    action: "annotate",
                    enabled: false,
                    mode: annotationMode,
                  });
                }}
              >
                {t("artifacts.resolution.cancelRebind")}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
