import { useState } from "react";
import { isApiError, type SpaceAgent } from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  ConfirmDialog,
  Icon,
  Menu,
  MenuItem,
  Switch,
} from "@openfox/ui";
import { runtimeLabel } from "../model";
import { AgentToolsCatalog, ToolIcon } from "./AgentToolsCatalog";
import type { AgentToolConnection } from "./AgentToolsEditor";
import { toolCatalog } from "./tool-catalog";
import { useAgentToolConnections } from "./tool-connections";

export function AgentTools({
  orgId,
  agent,
}: {
  orgId: string;
  agent: SpaceAgent;
}) {
  const t = useT(),
    state = useAgentToolConnections(orgId, agent);
  const [modal, setModal] = useState<"catalog" | AgentToolConnection | null>(
    null,
  );
  const [removing, setRemoving] = useState<AgentToolConnection | null>(null),
    [busy, setBusy] = useState(""),
    [error, setError] = useState("");
  const runtime = runtimeLabel(agent.config?.runtime) || "Runtime";
  const readonly = !state.canManage;
  async function run(key: string, fn: () => Promise<void>) {
    if (busy) return;
    setBusy(key);
    setError("");
    try {
      await fn();
    } catch (e) {
      setError(
        isApiError(e, "MCP_ADDRESS_BLOCKED")
          ? t("agents.tools.addressBlocked")
          : e instanceof Error
            ? e.message
            : t("agents.tools.enableFailed"),
      );
    } finally {
      setBusy("");
    }
  }
  const inventoryText = {
    idle: t("agents.tools.inventoryIdle"),
    ready: t("agents.tools.inventoryEmpty", { runtime }),
    loading: t("agents.tools.inventoryLoading"),
    unsupported: t("agents.tools.inventoryUnsupported", { runtime }),
    unavailable: t("agents.tools.inventoryUnavailable"),
    failed: t("agents.tools.inventoryFailed"),
  }[state.inventoryState];
  return (
    <div className="flex flex-col gap-4">
      {state.connections.length > 0 && (
        <div className="-mx-4 divide-y divide-line-default border-b border-line-default">
          {state.connections.map((c) => {
            const entry = toolCatalog.find((v) => v.id === c.catalogId),
              status = c.status.toLowerCase(),
              failed = status === "failed",
              pending = status === "authorizing" || status === "expired";
            const edit =
              !!c.config && !readonly && (!c.localOnly || state.canUseDevice);
            return (
              <section
                key={c.id}
                className="space-y-2 px-4 pb-3 pt-2 first:pt-0"
              >
                <div className="flex min-h-8 items-center gap-2">
                  <ToolIcon entry={entry} name={c.name} size={24} />
                  <span className="min-w-0 truncate text-body font-medium">
                    {c.name}
                  </span>
                  {c.localOnly && (
                    <span className="rounded border border-line-default px-1.5 text-caption text-fg-muted">
                      {t("agents.tools.localOnly")}
                    </span>
                  )}
                  {status === "device" && (
                    <span className="text-caption text-fg-muted">
                      {t("agents.tools.configured")}
                    </span>
                  )}
                  {(failed || pending) && (
                    <span
                      className={
                        failed
                          ? "text-caption text-danger"
                          : "text-caption text-fg-muted"
                      }
                    >
                      {t(
                        failed
                          ? "agents.tools.failed"
                          : "agents.tools.awaiting",
                      )}
                    </span>
                  )}
                  {!readonly && (
                    <Menu
                      label={c.name}
                      placement="bottom-end"
                      trigger={({ ref, open, toggle }) => (
                        <button
                          type="button"
                          ref={ref}
                          aria-label={c.name}
                          aria-expanded={open}
                          onClick={toggle}
                          className="ml-auto grid size-7 place-items-center rounded hover:bg-surface-hover"
                        >
                          <Icon name="ellipsis" size={16} />
                        </button>
                      )}
                    >
                      {edit && (
                        <MenuItem
                          icon={<Icon name="pencil" size={16} />}
                          disabled={!!busy}
                          onSelect={() => setModal(c)}
                        >
                          {t(
                            entry?.kind === "CLI"
                              ? "agents.tools.configure"
                              : pending
                                ? "agents.tools.reauthorize"
                                : "agents.tools.edit",
                          )}
                        </MenuItem>
                      )}
                      {!c.localOnly && c.transport !== "stdio" && !pending && (
                        <MenuItem
                          icon={<Icon name="refresh-cw" size={16} />}
                          disabled={!!busy}
                          onSelect={() =>
                            void run(c.id, () => state.refresh(c.id))
                          }
                        >
                          {t("agents.tools.refresh")}
                        </MenuItem>
                      )}
                      <MenuItem
                        danger
                        disabled={
                          !!busy || !!(c.localOnly && !state.canUseDevice)
                        }
                        icon={<Icon name="unplug" size={16} />}
                        onSelect={() => setRemoving(c)}
                      >
                        {t("agents.tools.disconnect")}
                      </MenuItem>
                    </Menu>
                  )}
                </div>
                {c.detail && (
                  <p
                    role={failed ? "alert" : undefined}
                    className={
                      failed
                        ? "text-caption text-danger"
                        : "text-caption text-fg-muted"
                    }
                  >
                    {c.detail}
                  </p>
                )}
                {c.transport !== "stdio" &&
                  !failed &&
                  (
                    [
                      ["read", c.readTools],
                      ["write", c.writeTools],
                      ["resources", c.resources],
                    ] as const
                  ).map(
                    ([key, items]) =>
                      !!items?.length && (
                        <details
                          key={key}
                          className="rounded-lg border border-line-default"
                        >
                          <summary className="cursor-pointer px-3 py-2 text-body-sm font-medium">
                            {t(`agents.tools.${key}`)}{" "}
                            <span className="ml-1 rounded bg-surface-muted px-1.5 py-0.5 text-caption text-fg-muted">
                              {t("agents.tools.readCount", {
                                count: items.length,
                              })}
                            </span>
                          </summary>
                          <ul className="max-h-72 overflow-y-auto border-t border-line-default py-1 pl-8 text-caption text-fg-secondary">
                            {items.map((item, i) => (
                              <li key={i} className="break-all py-1">
                                {item}
                              </li>
                            ))}
                          </ul>
                        </details>
                      ),
                  )}
              </section>
            );
          })}
        </div>
      )}
      <section className="space-y-2">
        <div className="flex items-center gap-2">
          <span className="grid size-6 place-content-center rounded bg-surface-muted text-fg-secondary">
            <Icon name="server" size={14} />
          </span>
          <h4 className="min-w-0 flex-1 truncate text-body font-medium">
            {t("agents.tools.inherited", { runtime })}
          </h4>
          <Button
            variant="ghost"
            size="28"
            disabled={state.inventoryState === "loading" || !state.canUseDevice}
            aria-label={t("agents.tools.refreshInventory")}
            title={t("agents.tools.refreshInventory")}
            onClick={state.refreshInventory}
          >
            <Icon
              name="refresh-cw"
              size={14}
              className={
                state.inventoryState === "loading"
                  ? "animate-spin motion-reduce:animate-none"
                  : ""
              }
            />
          </Button>
        </div>
        {state.inventory.length > 0 &&
          ["unavailable", "failed"].includes(state.inventoryState) && (
            <p className="py-1 text-caption text-fg-muted" role="status">
              {inventoryText}
            </p>
          )}
        {state.inventory.length > 0 ? (
          <ul className="space-y-2">
            {state.inventory.map((row) => {
              const overridden = state.connections.some(
                (c) => c.name === row.name,
              );
              return (
                <li
                  key={row.name}
                  className="flex min-h-10 items-center gap-2 rounded-lg border border-line-default px-3 py-2"
                >
                  <ToolIcon name={row.name} size={24} />
                  <span className="min-w-0 truncate text-body font-medium">
                    {row.name}
                  </span>
                  <span className="min-w-0 truncate text-caption text-fg-muted">
                    {row.transport.toUpperCase()}
                    {row.source && ` · ${row.source}`}
                  </span>
                  {(overridden || !row.enabled) && (
                    <span className="ml-auto rounded border border-line-default px-1.5 text-caption text-fg-muted">
                      {t(
                        overridden
                          ? "agents.tools.overridden"
                          : "agents.tools.disabled",
                      )}
                    </span>
                  )}
                  {state.setInherited &&
                    state.canUseDevice &&
                    !readonly &&
                    !overridden &&
                    row.enabled && (
                      <div className="ml-auto">
                        <Switch
                          label={t("agents.tools.enableTool", {
                            name: row.name,
                          })}
                          checked={!!row.active}
                          disabled={!!busy}
                          onCheckedChange={(v) =>
                            void run(row.name, () =>
                              state.setInherited!(row.name, v),
                            )
                          }
                        />
                      </div>
                    )}
                </li>
              );
            })}
          </ul>
        ) : (
          <p className="py-1 text-caption text-fg-muted" role="status">
            {inventoryText}
          </p>
        )}
      </section>
      {error && (
        <p role="alert" className="text-caption text-danger">
          {error}
        </p>
      )}
      {!readonly && (
        <div className="-mx-4 -mb-2 border-t border-line-default px-4 pt-3">
          <Button
            variant="outline"
            size="32"
            leading={<Icon name="plus" size={16} />}
            onClick={() => setModal("catalog")}
          >
            {t("agents.tools.add")}
          </Button>
        </div>
      )}
      {modal && state.canManage && (
        <AgentToolsCatalog
          state={state}
          initialConnection={modal === "catalog" ? undefined : modal}
          onClose={() => setModal(null)}
        />
      )}
      {removing && (
        <ConfirmDialog
          open
          danger
          title={t("agents.tools.disconnectTitle", { name: removing.name })}
          description={t("agents.tools.disconnectBody")}
          cancelLabel={t("common.cancel")}
          confirmLabel={t("agents.tools.disconnect")}
          loading={busy === removing.id}
          onCancel={() => {
            if (!busy) setRemoving(null);
          }}
          onConfirm={() =>
            void run(removing.id, async () => {
              await state.disconnect(removing.id);
              setRemoving(null);
            })
          }
        />
      )}
    </div>
  );
}
