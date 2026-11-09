import { useState, type ReactNode } from "react";
import { useT } from "@openfox/i18n";
import { Button, Dialog, Icon, TextInput } from "@openfox/ui";
import { AgentToolsProviderCLI } from "./AgentToolsProviderCLI";
import { toolCatalog, type ToolCatalogEntry } from "./tool-catalog";
import {
  AgentToolsEditor,
  type AgentToolConnection,
  type AgentToolState,
} from "./AgentToolsEditor";

export function ToolIcon({
  entry,
  name,
  size = 36,
}: {
  entry?: ToolCatalogEntry;
  name: string;
  size?: number;
}) {
  const [failed, setFailed] = useState(false);
  const image =
    entry &&
    "iconUrl" in entry &&
    typeof entry.iconUrl === "string" &&
    entry.iconUrl.startsWith("/") &&
    !entry.iconUrl.startsWith("//")
      ? entry.iconUrl
      : null;
  const colors = [
    "bg-info-soft text-link",
    "bg-success-soft text-success",
    "bg-surface-muted text-fg-secondary",
    "bg-warning-soft text-warning",
  ];
  const hash = Array.from(entry?.id ?? name).reduce(
    (a, c) => (Math.imul(a, 31) + c.charCodeAt(0)) >>> 0,
    0,
  );
  return (
    <span
      aria-hidden="true"
      className={`inline-flex shrink-0 items-center justify-center rounded-md border border-line-default font-medium ${image && !failed ? "bg-surface-card" : colors[hash % colors.length]}`}
      style={{ height: size, width: size }}
    >
      {image && !failed ? (
        <img
          className="h-3/4 w-3/4 object-contain"
          src={image}
          onError={() => setFailed(true)}
          alt=""
        />
      ) : (
        Array.from(name)[0]?.toUpperCase()
      )}
    </span>
  );
}
const badge =
  "w-fit rounded bg-surface-muted px-1.5 py-0.5 text-caption text-fg-secondary";
interface ToolCatalogItem {
  entry: ToolCatalogEntry;
  connection?: AgentToolConnection;
}

export function AgentToolsCatalog({
  state,
  initialConnection,
  onClose,
}: {
  state: AgentToolState;
  initialConnection?: AgentToolConnection;
  onClose: () => void;
}) {
  const t = useT(),
    [page, setPage] = useState<"catalog" | "detail" | "form">(
      initialConnection ? "form" : "catalog",
    );
  const [item, setItem] = useState<ToolCatalogItem | undefined>(() =>
    initialConnection
      ? {
          entry: toolCatalog.find(
            (v) => v.id === initialConnection.catalogId,
          ) ?? {
            id: initialConnection.catalogId,
            name: initialConnection.name,
            category: "",
            description: "",
            kind: "MCP",
          },
          connection: initialConnection,
        }
      : undefined,
  );
  const [query, setQuery] = useState(""),
    [category, setCategory] = useState("all"),
    [busy, setBusy] = useState(false);
  const custom = page === "form" && (!item || item.entry.id === "custom");
  function back() {
    if (busy) return;
    if (initialConnection || page === "catalog") {
      onClose();
      return;
    }
    setPage(
      page === "form" && item && item.entry.id !== "custom"
        ? "detail"
        : "catalog",
    );
  }
  const title =
    page === "catalog"
      ? t("agents.tools.add")
      : initialConnection
        ? t("agents.tools.editTitle", { name: initialConnection.name })
        : custom
          ? t("agents.tools.custom")
          : item?.entry.name;
  const validConnection = (c: AgentToolConnection) =>
    c.status.toLowerCase() === "connected";
  const bound = (c: AgentToolConnection) =>
    validConnection(c) || c.status.toLowerCase() === "device";
  const categories = [
    ...new Set(
      toolCatalog.filter((e) => e.id !== "custom").map((e) => e.category),
    ),
  ];
  const entries: ToolCatalogItem[] = toolCatalog
    .filter((e) => e.id !== "custom")
    .map((entry) => ({
      entry,
      connection: state.connections.find((c) => c.catalogId === entry.id),
    }));
  for (const c of state.connections.filter((c) => c.catalogId === "custom")) {
    entries.push({
      entry: {
        id: "custom",
        name: c.name,
        kind: "MCP",
        description: "",
        category: t("agents.tools.other"),
      },
      connection: c,
    });
  }
  const needle = query.trim().toLocaleLowerCase();
  const filtered = entries.filter(
    ({ entry, connection }) =>
      (category === "all" ||
        (category === "connected"
          ? !!connection && bound(connection)
          : entry.category === category)) &&
      [entry.name, entry.id, entry.description].some((v) =>
        v.toLocaleLowerCase().includes(needle),
      ),
  );
  const groups = [...new Set(filtered.map(({ entry }) => entry.category))];
  let content: ReactNode;
  if (page === "catalog")
    content = (
      <div className="@container flex-1 space-y-6 overflow-auto px-6 pt-5 pb-8">
        <div
          className="flex flex-wrap gap-1.5"
          aria-label={t("agents.tools.add")}
        >
          {[
            ["all", t("agents.tools.all")],
            ["connected", t("agents.tools.connected")],
            ...categories.map((v) => [v, v]),
          ].map(([id, label]) => (
            <button
              key={id}
              type="button"
              aria-pressed={id === category}
              className={`rounded-full px-2.5 py-1 text-body-sm ${id === category ? "bg-surface-selected font-medium text-fg-default" : "text-fg-secondary hover:bg-surface-hover"}`}
              onClick={() => setCategory(id!)}
            >
              {label}
            </button>
          ))}
        </div>
        {groups.map((group) => (
          <section key={group} className="space-y-3">
            <h3 className="text-title font-semibold">{group}</h3>
            <div className="grid grid-cols-1 gap-3 @[520px]:grid-cols-2 @[820px]:grid-cols-3 @[1120px]:grid-cols-4">
              {filtered
                .filter((v) => v.entry.category === group)
                .map((v) => (
                  <button
                    type="button"
                    key={v.connection?.id ?? v.entry.id}
                    className="flex min-h-32 flex-col gap-2 rounded-xl border border-line-default bg-surface-card p-3.5 text-left hover:bg-surface-hover"
                    onClick={() => {
                      setItem(v);
                      setPage(v.entry.id === "custom" ? "form" : "detail");
                    }}
                  >
                    <span className="flex w-full items-center gap-2.5">
                      <ToolIcon entry={v.entry} name={v.entry.name} />
                      <span className="min-w-0 flex-1">
                        <span className="block truncate text-body font-medium">
                          {v.entry.name}
                        </span>
                        {v.entry.id !== "custom" && (
                          <span className="block truncate font-mono text-caption text-fg-muted">
                            {v.entry.id}
                          </span>
                        )}
                      </span>
                      {v.connection && (
                        <span
                          className={
                            validConnection(v.connection)
                              ? "text-caption text-success"
                              : "text-caption text-fg-muted"
                          }
                        >
                          {t(
                            validConnection(v.connection)
                              ? "agents.tools.connected"
                              : v.connection.status.toLowerCase() === "device"
                                ? "agents.tools.configuredShort"
                                : v.connection.status.toLowerCase() === "failed"
                                  ? "agents.tools.failed"
                                  : "agents.tools.awaiting",
                          )}
                        </span>
                      )}
                    </span>
                    {v.entry.description && (
                      <span className="line-clamp-2 flex-1 text-body-sm text-fg-secondary">
                        {v.entry.description}
                      </span>
                    )}
                    <span className={badge}>{v.entry.kind}</span>
                  </button>
                ))}
            </div>
          </section>
        ))}
        {groups.length === 0 && (
          <p className="grid min-h-64 place-content-center text-body text-fg-muted">
            {t(
              category === "connected" && !needle
                ? "agents.tools.emptyConnected"
                : "agents.tools.emptySearch",
            )}
          </p>
        )}
      </div>
    );
  else if (page === "detail" && item) {
    const { entry, connection } = item;
    const external = entry.website?.startsWith("https://")
      ? entry.website
      : undefined;
    content = (
      <div className="flex min-h-0 flex-1 flex-col">
        <div className="flex-1 space-y-5 overflow-y-auto p-6">
          <div className="flex items-center gap-4">
            <ToolIcon entry={entry} name={entry.name} size={56} />
            <div>
              <h3 className="text-xl font-semibold">{entry.name}</h3>
              <p className="mt-0.5 font-mono text-caption text-fg-muted">
                {entry.id}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <span className={badge}>{entry.kind}</span>
            <span className={badge}>{entry.category}</span>
          </div>
          <p className="text-body text-fg-secondary">{entry.description}</p>
          {(["useCases", "prompts"] as const).map((kind) =>
            entry[kind]?.length ? (
              <section key={kind} className="mt-7 space-y-3.5">
                <h4 className="text-caption text-fg-muted">
                  {t(`agents.tools.${kind}`)}
                </h4>
                <ul className="space-y-3">
                  {entry[kind]?.map((line) => (
                    <li
                      key={line}
                      className={`flex items-start gap-2.5 text-[15px] leading-6 text-fg-secondary ${kind === "prompts" ? "italic" : ""}`}
                    >
                      <Icon
                        name={kind === "prompts" ? "message-circle" : "check"}
                        size={14}
                        className="mt-1 shrink-0 text-fg-muted"
                      />
                      <span>{line}</span>
                    </li>
                  ))}
                </ul>
              </section>
            ) : null,
          )}
          {external && (
            <a
              href={external}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 text-body-sm text-link"
            >
              {t("agents.tools.source")}
              <Icon name="external-link" size={14} />
            </a>
          )}
          {entry.kind === "CLI" && !state.canUseDevice && (
            <p className="text-body-sm text-fg-muted">
              {t("agents.tools.cliOwner")}
            </p>
          )}
        </div>
        <div className="flex justify-end border-t border-line-default px-6 py-3">
          <Button
            disabled={
              !state.canManage ||
              (!!connection &&
                validConnection(connection) &&
                entry.kind !== "CLI")
            }
            onClick={() => setPage("form")}
          >
            {t(
              connection
                ? validConnection(connection) && entry.kind !== "CLI"
                  ? "agents.tools.connected"
                  : "agents.tools.configure"
                : "agents.tools.connect",
            )}
          </Button>
        </div>
      </div>
    );
  } else if (item?.entry.kind === "CLI") {
    content = (
      <AgentToolsProviderCLI
        entry={item.entry}
        connection={item.connection}
        state={state}
        onClose={onClose}
        busy={busy}
        setBusy={setBusy}
      />
    );
  } else
    content = (
      <AgentToolsEditor
        key={item?.connection?.id ?? item?.entry.id ?? "custom"}
        state={state}
        entry={item?.entry.id === "custom" ? undefined : item?.entry}
        connection={item?.connection}
        onClose={onClose}
        busy={busy}
        setBusy={setBusy}
      />
    );
  return (
    <Dialog
      hideHeader
      open
      title={title}
      onClose={back}
      width={
        page === "catalog" ? 1200 : page === "detail" ? 768 : custom ? 576 : 640
      }
      className={`gap-0 overflow-hidden p-0 [&>div:last-child]:flex [&>div:last-child]:flex-1 [&>div:last-child]:flex-col [&>div:last-child]:overflow-hidden ${page === "catalog" || page === "detail" ? "h-[min(736px,calc(100dvh-2rem))]" : "h-[min(600px,calc(100dvh-2rem))]"}`}
    >
      <div className="flex h-12 shrink-0 items-center gap-3 border-b border-line-default px-4">
        {page !== "catalog" && !initialConnection && (
          <Button
            variant="ghost"
            size="28"
            disabled={busy}
            aria-label={t(
              page === "form" && !custom
                ? "agents.tools.backDetail"
                : "agents.tools.backCatalog",
            )}
            onClick={back}
          >
            <Icon name="arrow-left" size={16} />
          </Button>
        )}
        <span className="min-w-0 truncate text-body font-medium">{title}</span>
        {page === "catalog" && (
          <>
            <TextInput
              data-autofocus
              aria-label={t("agents.tools.search")}
              placeholder={t("agents.tools.placeholder")}
              leading={<Icon name="search" size={15} />}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="ml-auto h-8 w-64 min-w-12 rounded-full"
            />
            <Button
              size="28"
              variant="outline"
              leading={<Icon name="plus" size={14} />}
              onClick={() => {
                setItem(undefined);
                setPage("form");
              }}
            >
              {t("agents.tools.custom")}
            </Button>
          </>
        )}
        <Button
          variant="ghost"
          size="28"
          className={page === "catalog" ? undefined : "ml-auto"}
          aria-label={t("common.close")}
          disabled={busy}
          onClick={onClose}
        >
          <Icon name="x" size={16} />
        </Button>
      </div>
      {content}
    </Dialog>
  );
}
