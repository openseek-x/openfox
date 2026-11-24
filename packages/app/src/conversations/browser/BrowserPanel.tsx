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
import { desktopBridge } from "../../desktop";
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
  const [address, setAddress] = useState("");
  const [addressFocused, setAddressFocused] = useState(false);
  const viewport = useRef<HTMLDivElement>(null);
  const addressInput = useRef<HTMLInputElement>(null);
  const active = state?.tabs.find((tab) => tab.id === state.activeTab);
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
  }, [active?.id, active?.url]);
  useEffect(() => {
    if (active?.url === "about:blank") addressInput.current?.focus();
  }, [active?.id]);
  useEffect(() => {
    const element = viewport.current;
    if (!element || !state?.lease) return;
    let frame = 0;
    const layout = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const rect = element.getBoundingClientRect();
        const occluded = [
          ...document.querySelectorAll(
            '[role="dialog"], [role="alertdialog"], [role="menu"], [role="listbox"], [data-openfox-settings-drawer]',
          ),
        ].some(
          (node) =>
            node.getClientRects().length > 0 &&
            getComputedStyle(node).visibility !== "hidden",
        );
        const visible =
          !document.hidden && !occluded && rect.width >= 1 && rect.height >= 1;
        void desktopBridge()
          ?.browserCommand?.({
            lease: state.lease,
            action: "layout",
            visible,
            ...(visible
              ? {
                  bounds: {
                    x: rect.x,
                    y: rect.y,
                    width: rect.width,
                    height: rect.height,
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
      void desktopBridge()
        ?.browserCommand?.({ lease: state.lease, action: "hide" })
        .catch(() => {});
      observer.disconnect();
      overlays.disconnect();
      window.removeEventListener("resize", layout);
      document.removeEventListener("visibilitychange", layout);
    };
  }, [state?.lease]);
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
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
  }, [command, active?.id]);
  return (
    <div
      className="flex min-h-0 flex-1 flex-col"
      aria-label={t("artifacts.browser.label")}
    >
      <form
        aria-label={t("artifacts.browser.navigation")}
        className="@container/browser flex h-[52px] min-w-0 shrink-0 items-center gap-1.5 border-b border-line-default px-2"
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
        <div className="flex shrink-0 items-center rounded-full border border-line-default bg-surface-muted p-0.5">
          <IconButton
            label={t("artifacts.browser.back")}
            icon={<Icon name="arrow-left" size={16} />}
            size="28"
            className="rounded-full!"
            disabled={!active?.canBack}
            onClick={() => void command({ action: "back", tabId: active?.id })}
          />
          <IconButton
            label={t("artifacts.browser.forward")}
            icon={<Icon name="arrow-right" size={16} />}
            size="28"
            className="rounded-full!"
            disabled={!active?.canForward}
            onClick={() =>
              void command({ action: "forward", tabId: active?.id })
            }
          />
          <span
            className="mx-1 h-3.5 w-px bg-line-default"
            aria-hidden="true"
          />
          <IconButton
            label={t(
              active?.loading
                ? "artifacts.browser.stop"
                : "artifacts.browser.reload",
            )}
            icon={
              <Icon name={active?.loading ? "x" : "rotate-ccw"} size={16} />
            }
            size="28"
            className="rounded-full!"
            disabled={!active}
            onClick={() =>
              void command({
                action: active?.loading ? "stop" : "reload",
                tabId: active?.id,
              })
            }
          />
        </div>
        <IconButton
          label={t("artifacts.browser.annotate")}
          title={t(
            annotation?.enabled
              ? "artifacts.browser.stopSelecting"
              : "artifacts.browser.selectElement",
          )}
          icon={<Icon name="hifox-scan-eye" size={18} />}
          size="32"
          variant="round"
          active={annotation?.enabled ?? false}
          aria-pressed={annotation?.enabled ?? false}
          className={
            annotation?.enabled
              ? "border-accent! bg-accent-soft! text-accent!"
              : "bg-surface-muted"
          }
          disabled={!currentOrigin}
          onClick={() =>
            void command({
              action: "annotate",
              enabled: !annotation?.enabled,
              mode: "element",
            })
          }
        />
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
            "h-[34px] min-w-0 flex-1 rounded-full! bg-surface-muted px-3 text-body-sm",
            !addressFocused && "[&_input]:text-center",
          )}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
        />
        <button
          type="submit"
          className="sr-only"
          tabIndex={-1}
          disabled={!state}
        >
          {t("artifacts.browser.go")}
        </button>
        <div className="hidden shrink-0 items-center rounded-full border border-line-default bg-surface-muted p-0.5 @[400px]/browser:flex">
          <IconButton
            label={controlLabel}
            title={
              control?.active || session.armed
                ? `${controlStatus} · ${controlLabel}`
                : controlLabel
            }
            icon={
              <Icon
                name={control?.active || session.armed ? "shield" : "bot"}
                size={17}
              />
            }
            size="28"
            className={cn(
              "rounded-full",
              (control?.active || session.armed) &&
                "text-accent! bg-surface-selected!",
            )}
            active={Boolean(control?.active || session.armed)}
            disabled={
              control?.active || session.armed ? false : controlDisabled
            }
            onClick={changeControl}
          />
          <Menu
            label={t("artifacts.browser.downloads")}
            placement="bottom-end"
            className="w-72 max-w-[calc(100vw-16px)]"
            trigger={({ ref, open, toggle }) => (
              <IconButton
                ref={ref}
                label={t("artifacts.browser.downloads")}
                icon={<Icon name="download" size={17} />}
                size="28"
                className="relative rounded-full"
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
              icon={<Icon name="more" size={18} />}
              size="32"
              variant="round"
              className="bg-surface-muted"
              active={open}
              aria-expanded={open}
              aria-haspopup="menu"
              onClick={toggle}
            />
          )}
        >
          <MenuLabel>{controlStatus}</MenuLabel>
          <MenuItem
            icon={
              <Icon
                name={control?.active || session.armed ? "shield" : "bot"}
                size={16}
              />
            }
            disabled={
              control?.active || session.armed ? false : controlDisabled
            }
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
      </form>
      <span role="status" className="sr-only">
        {controlStatus}
      </span>
      {binding && !selection && (
        <div className="shrink-0 px-3 py-2 text-caption" role="status">
          {t("artifacts.resolution.rebindHint")}{" "}
          <button
            type="button"
            onClick={() => {
              workspaceStore().write(
                rebindKey(session.conversationId),
                undefined,
              );
              void command({ action: "annotate", enabled: false });
            }}
          >
            {t("artifacts.resolution.cancelRebind")}
          </button>
        </div>
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
            <div className="flex flex-col items-center gap-2 p-8 text-center text-fg-muted">
              <Icon name="globe" size={24} />
              <p className="text-body-sm">{t("artifacts.browser.empty")}</p>
            </div>
          )
        )}
      </div>
    </div>
  );
}
