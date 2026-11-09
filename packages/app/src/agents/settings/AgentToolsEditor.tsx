import { useState } from "react";
import { isApiError } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { copyText } from "../../tasks/parts";
import { Button, Icon, TextInput, Switch } from "@openfox/ui";
import type { ToolCatalogEntry } from "./tool-catalog";
import type { useAgentToolConnections } from "./tool-connections";
import {
  emptyToolDraft,
  draftToJson,
  parseToolJson,
  toMap,
  validToolName,
  validToolUrl,
  type AgentToolDraft,
  type CreateAgentToolInput,
} from "./agentToolsModel";

export type AgentToolState = ReturnType<typeof useAgentToolConnections>;
export type AgentToolConnection = AgentToolState["connections"][number];

function PairEditor({
  title,
  add,
  remove,
  pairs,
  onChange,
  keepExisting,
}: {
  title: string;
  add: string;
  remove: string;
  pairs: AgentToolDraft["env"];
  onChange: (value: AgentToolDraft["env"]) => void;
  keepExisting?: Set<string>;
}) {
  const t = useT();
  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <span className="font-medium">{title}</span>
        <Button
          size="28"
          variant="ghost"
          leading={<Icon name="plus" size={14} />}
          onClick={() => onChange([...pairs, { key: "", value: "" }])}
        >
          {add}
        </Button>
      </div>
      {pairs.map((pair, index) => (
        <div key={index} className="flex items-center gap-2">
          <TextInput
            aria-label={title + " " + t("agents.tools.key") + " " + (index + 1)}
            placeholder={t("agents.tools.key")}
            value={pair.key}
            className="min-w-0 flex-1"
            onChange={(e) =>
              onChange(
                pairs.map((v, i) =>
                  i === index ? { ...v, key: e.target.value } : v,
                ),
              )
            }
          />
          <TextInput
            aria-label={
              title + " " + t("agents.tools.value") + " " + (index + 1)
            }
            placeholder={
              keepExisting?.has(pair.key)
                ? t("agents.tools.secretKept")
                : t("agents.tools.value")
            }
            type="password"
            autoComplete="new-password"
            value={pair.value}
            className="min-w-0 flex-1"
            onChange={(e) =>
              onChange(
                pairs.map((v, i) =>
                  i === index ? { ...v, value: e.target.value } : v,
                ),
              )
            }
          />
          <Button
            size="28"
            variant="ghost"
            aria-label={remove + " " + (index + 1)}
            onClick={() => onChange(pairs.filter((_, i) => i !== index))}
          >
            <Icon name="x" size={14} />
          </Button>
        </div>
      ))}
    </div>
  );
}
function Segments<T extends string>({
  title,
  options,
  value,
  onChange,
  disabled,
}: {
  title: string;
  options: readonly { id: T; label: string }[];
  value: T;
  onChange: (v: T) => void;
  disabled?: boolean;
}) {
  return (
    <div role="group" aria-label={title} className="space-y-2">
      <div className="font-medium">{title}</div>
      <div className="inline-flex rounded-md bg-surface-muted p-1">
        {options.map((item) => (
          <button
            key={item.id}
            type="button"
            aria-pressed={item.id === value}
            disabled={disabled}
            className={`rounded px-3 py-1 text-body-sm ${item.id === value ? "bg-surface-card text-fg-default shadow-sm" : "text-fg-secondary"}`}
            onClick={() => onChange(item.id)}
          >
            {item.label}
          </button>
        ))}
      </div>
    </div>
  );
}

export function AgentToolsEditor({
  state,
  entry,
  connection,
  onClose,
  busy,
  setBusy,
}: {
  state: AgentToolState;
  entry?: ToolCatalogEntry;
  connection?: AgentToolConnection;
  onClose: () => void;
  busy: boolean;
  setBusy: (busy: boolean) => void;
}) {
  const t = useT(),
    old = connection?.config;
  const [name, setName] = useState(connection?.name ?? entry?.id ?? "");
  const [draft, setDraft] = useState<AgentToolDraft>(() => ({
    ...emptyToolDraft(),
    transport: old?.transport ?? "http",
    url: old?.url ?? entry?.endpoint ?? "",
    command: old?.command ?? "",
    args: old?.args ?? [],
    headers: old
      ? Object.keys(old.headers ?? {}).map((key) => ({ key, value: "" }))
      : Object.entries(entry?.initialHeaders ?? {}).map(([key, value]) => ({
          key,
          value,
        })),
    env: Object.keys(old?.env ?? {}).map((key) => ({ key, value: "" })),
  }));
  const [copyStatus, setCopyStatus] = useState("");
  const [localOnly, setLocalOnly] = useState(!!connection?.localOnly);
  const [auth, setAuth] = useState<CreateAgentToolInput["authMode"]>(
      old?.authMode ?? entry?.authMode ?? "none",
    ),
    [credential, setCredential] = useState(""),
    [authHeader, setAuthHeader] = useState(
      old?.authHeader ?? entry?.authHeader ?? "Authorization",
    ),
    [clientId, setClientId] = useState(""),
    [clientSecret, setClientSecret] = useState("");
  const [tab, setTab] = useState<"form" | "json">("form"),
    [json, setJson] = useState(""),
    [extras, setExtras] = useState<Record<string, unknown>>({});
  const [advanced, setAdvanced] = useState(
      Object.keys(old?.headers ?? entry?.initialHeaders ?? {}).length > 0 ||
        !!entry?.authHeader,
    ),
    [error, setError] = useState("");
  const update = (next: Partial<AgentToolDraft>) => {
    setDraft((prev) => ({ ...prev, ...next }));
    setError("");
  };
  const parsed = tab === "json" ? parseToolJson(json, name) : null;
  const effective = parsed && "draft" in parsed ? parsed.draft : draft;
  const serverName =
    name.trim() || (parsed && "draft" in parsed ? parsed.name : "");
  const stdio = effective.transport === "stdio";
  const authMode = stdio || tab === "json" ? "none" : auth;
  const reauthorizing = !!connection && old?.authMode === "oauth";
  const unchanged =
    !!old &&
    localOnly === !!connection?.localOnly &&
    authMode === (old.authMode ?? "none") &&
    (authMode !== "api_key" ||
      authHeader.trim().toLowerCase() ===
        (old.authHeader || "Authorization").toLowerCase()) &&
    (stdio
      ? effective.command.trim() === (old.command ?? "").trim() &&
        JSON.stringify(effective.args.filter((a) => a.trim())) ===
          JSON.stringify(old.args ?? [])
      : effective.url.trim() === (old.url ?? "").trim());
  const secretAvailable =
    unchanged &&
    old?.authMode === "api_key" &&
    authHeader === (old.authHeader || "Authorization");
  const scope = stdio || localOnly ? "DEVICE_OWNER" : "PUBLIC";
  const supported =
    state.supportedTransports.includes(effective.transport) &&
    (scope === "PUBLIC" || state.canUseDevice);
  const urlIsValid = validToolUrl(effective.url, {
    deviceOnly: localOnly,
    oauth: authMode === "oauth",
    savedUrl: old?.url,
    example: entry?.endpointExample,
  });
  const requiredHeadersReady =
    authMode !== "none" ||
    !entry?.initialHeaders ||
    Object.keys(entry.initialHeaders).every((key) =>
      effective.headers.some(
        (pair) =>
          pair.key.toLowerCase() === key.toLowerCase() &&
          (pair.value.trim() || (unchanged && old?.headers?.[pair.key])),
      ),
    );
  const handler = authMode === "oauth" ? state.startOAuth : state.submit;
  const valid =
    !busy &&
    state.canManage &&
    supported &&
    requiredHeadersReady &&
    (!parsed || !("error" in parsed)) &&
    validToolName(serverName) &&
    (stdio
      ? !!effective.command.trim() &&
        (unchanged || !effective.args.includes("••••••"))
      : urlIsValid) &&
    (authMode !== "api_key" ||
      ((!!credential.trim() || secretAvailable) && !!authHeader.trim())) &&
    (authMode !== "oauth" || (state.supportsOAuth && scope === "PUBLIC"));
  function switchTab(next: "form" | "json") {
    if (next === tab) return;
    if (next === "json") setJson(draftToJson(draft, extras));
    else if (parsed && "draft" in parsed) {
      setDraft(parsed.draft);
      setExtras(parsed.extras);
      if (!name.trim() && parsed.name) setName(parsed.name);
    }
    setTab(next);
    setError("");
  }
  async function submit() {
    if (!valid) return;
    function secrets(
      rows: AgentToolDraft["env"],
      previous?: Record<string, string>,
    ) {
      const values = toMap(rows);
      for (const key of Object.keys(values)) {
        if (values[key] === "" && unchanged && previous?.[key])
          values[key] = previous[key]!;
      }
      return values;
    }
    const input: CreateAgentToolInput = {
      catalogId: connection?.catalogId ?? entry?.id ?? "custom",
      name: serverName,
      transport: effective.transport,
      authMode,
      scope,
      ...(connection ? { id: connection.id } : {}),
      ...(stdio
        ? {
            command: effective.command.trim(),
            args: effective.args.filter((a) => a.trim()),
            env: secrets(effective.env, old?.env),
          }
        : {
            url: effective.url.trim(),
            headers: secrets(effective.headers, old?.headers),
          }),
      ...(authMode === "api_key"
        ? {
            authHeader: authHeader.trim(),
            ...(credential.trim()
              ? { credential: credential.trim() }
              : secretAvailable && old?.credential
                ? { credential: old.credential }
                : {}),
          }
        : {}),
      ...(authMode === "oauth"
        ? {
            ...(clientId.trim() ? { oauthClientId: clientId.trim() } : {}),
            ...(clientSecret.trim()
              ? { oauthClientSecret: clientSecret.trim() }
              : {}),
          }
        : {}),
    };
    setBusy(true);
    setError("");
    try {
      await handler(input);
      onClose();
    } catch (e) {
      setError(
        isApiError(e, "MCP_ADDRESS_BLOCKED")
          ? t("agents.tools.addressBlocked")
          : e instanceof Error
            ? e.message
            : t("agents.tools.failed"),
      );
    } finally {
      setBusy(false);
    }
  }
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      className="flex min-h-0 flex-1 flex-col text-body"
    >
      <fieldset
        disabled={busy}
        className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5"
      >
        <div>
          <h3 className="text-title font-semibold">
            {entry?.name ?? "Custom MCP"}{" "}
            {entry && (
              <span className="ml-1 rounded bg-surface-muted px-1.5 py-0.5 align-middle text-caption font-normal">
                MCP
              </span>
            )}
          </h3>
          <p className="mt-1 text-body-sm text-fg-muted">
            {entry?.description ?? t("agents.tools.server")}
          </p>
        </div>
        <label className="block space-y-1.5">
          <span className="font-medium">{t("agents.tools.name")}</span>
          <TextInput
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="playwright"
            aria-label={t("agents.tools.name")}
            invalid={!!name && !validToolName(name)}
            maxLength={64}
          />
          <span className="block text-caption text-fg-muted">
            {t("agents.tools.nameHint")}
          </span>
        </label>
        {!connection && (
          <div
            role="tablist"
            className="flex gap-4 border-b border-line-default"
          >
            {(["form", "json"] as const).map((id) => (
              <button
                key={id}
                type="button"
                role="tab"
                aria-selected={tab === id}
                className={`border-b-2 py-1.5 ${tab === id ? "border-fg-default font-medium" : "border-transparent text-fg-muted"}`}
                onClick={() => switchTab(id)}
              >
                {t(`agents.tools.${id}`)}
              </button>
            ))}
          </div>
        )}
        {tab === "json" ? (
          <label className="block space-y-1.5">
            <span className="font-medium">
              {t("agents.tools.serverConfig")}
            </span>
            <textarea
              spellCheck={false}
              aria-label={t("agents.tools.serverConfig")}
              value={json}
              onChange={(e) => setJson(e.target.value)}
              className="min-h-36 w-full resize-y rounded-lg border border-line-default bg-surface-muted p-3 font-mono text-body-sm"
            />
            <span className="block text-caption text-fg-muted">
              {t("agents.tools.jsonHint")}
            </span>
            {parsed && "error" in parsed && (
              <span role="alert" className="block text-caption text-danger">
                {t(`agents.tools.${parsed.error}`)}
              </span>
            )}
            {parsed && "count" in parsed && parsed.count > 1 && (
              <span className="block text-caption">
                {t("agents.tools.multiple", { count: parsed.count })}
              </span>
            )}
          </label>
        ) : (
          <>
            <Segments
              title={t("agents.tools.transport")}
              options={[
                { id: "stdio", label: "STDIO" },
                { id: "http", label: "Streamable HTTP" },
              ]}
              value={draft.transport}
              disabled={!!connection || !!entry?.endpoint}
              onChange={(transport) => update({ transport })}
            />
            <p className="-mt-2 text-caption text-fg-muted">
              {t(stdio ? "agents.tools.stdioHint" : "agents.tools.httpHint")}
            </p>
            {stdio ? (
              <>
                <label className="block space-y-1.5">
                  <span>{t("agents.tools.command")}</span>
                  <TextInput
                    aria-label={t("agents.tools.command")}
                    value={draft.command}
                    placeholder="npx"
                    onChange={(e) => update({ command: e.target.value })}
                  />
                </label>
                <div className="space-y-2">
                  <div className="flex items-center justify-between">
                    <span>{t("agents.tools.args")}</span>
                    <Button
                      size="28"
                      variant="ghost"
                      onClick={() => update({ args: [...draft.args, ""] })}
                      leading={<Icon name="plus" size={14} />}
                    >
                      {t("agents.tools.addArg")}
                    </Button>
                  </div>
                  {draft.args.map((arg, index) => (
                    <div key={index} className="flex gap-2">
                      <TextInput
                        aria-label={t("agents.tools.arg", { index: index + 1 })}
                        className="flex-1"
                        value={arg}
                        placeholder="--flag"
                        onChange={(e) =>
                          update({
                            args: draft.args.map((v, i) =>
                              i === index ? e.target.value : v,
                            ),
                          })
                        }
                      />
                      <Button
                        variant="ghost"
                        size="28"
                        aria-label={t("agents.tools.removeArg")}
                        onClick={() =>
                          update({
                            args: draft.args.filter((_, i) => i !== index),
                          })
                        }
                      >
                        <Icon name="x" size={14} />
                      </Button>
                    </div>
                  ))}
                </div>
                <PairEditor
                  title={t("agents.tools.env")}
                  add={t("agents.tools.addEnv")}
                  remove={t("agents.tools.removeEnv")}
                  pairs={draft.env}
                  keepExisting={
                    unchanged ? new Set(Object.keys(old?.env ?? {})) : undefined
                  }
                  onChange={(env) => update({ env })}
                />
                <p className="text-caption text-fg-muted">
                  {t("agents.tools.stdioNotice")}
                </p>
              </>
            ) : (
              <>
                <label className="block space-y-1.5">
                  <span>{t("agents.tools.url")}</span>
                  <TextInput
                    aria-label={t("agents.tools.url")}
                    value={draft.url}
                    placeholder={
                      entry?.endpointExample ?? "https://example.com/mcp"
                    }
                    autoComplete="off"
                    spellCheck={false}
                    invalid={!!draft.url && !urlIsValid}
                    onChange={(e) => update({ url: e.target.value })}
                  />
                  {entry && !entry.endpoint && !draft.url && (
                    <span className="text-caption text-fg-muted">
                      {t("agents.tools.urlRequired")}
                    </span>
                  )}
                  {!!draft.url && !urlIsValid && (
                    <span className="text-caption text-danger">
                      {t("agents.tools.invalidUrl")}
                    </span>
                  )}
                </label>
                <Segments
                  title={t("agents.tools.auth")}
                  options={[
                    { id: "none", label: t("agents.tools.none") },
                    { id: "api_key", label: "API Key" },
                    { id: "oauth", label: "OAuth" },
                  ]}
                  disabled={reauthorizing}
                  value={auth}
                  onChange={setAuth}
                />
                <p className="-mt-2 text-caption text-fg-muted">
                  {t(
                    auth === "none"
                      ? "agents.tools.noneHint"
                      : auth === "oauth"
                        ? "agents.tools.oauthHint"
                        : "agents.tools.apiHint",
                  )}
                </p>
                {auth === "api_key" && (
                  <label className="block space-y-1.5">
                    <span>{t("agents.tools.credential")}</span>
                    <TextInput
                      aria-label={t("agents.tools.credential")}
                      autoComplete="new-password"
                      type="password"
                      value={credential}
                      placeholder={t(
                        secretAvailable
                          ? "agents.tools.secretKept"
                          : "agents.tools.credentialPlaceholder",
                      )}
                      onChange={(e) => setCredential(e.target.value)}
                    />
                  </label>
                )}
                {auth === "oauth" && (
                  <>
                    <label className="block space-y-1.5">
                      <span>{t("agents.tools.clientId")}</span>
                      <TextInput
                        value={clientId}
                        onChange={(e) => setClientId(e.target.value)}
                      />
                    </label>
                    <label className="block space-y-1.5">
                      <span>{t("agents.tools.clientSecret")}</span>
                      <TextInput
                        autoComplete="new-password"
                        type="password"
                        value={clientSecret}
                        onChange={(e) => setClientSecret(e.target.value)}
                      />
                    </label>
                    {state.oauthRedirectUri && (
                      <label className="block space-y-1.5">
                        <span>{t("agents.tools.redirect")}</span>
                        <TextInput
                          readOnly
                          value={state.oauthRedirectUri}
                          onFocus={(e) => e.target.select()}
                          trailing={
                            <button
                              type="button"
                              aria-label={t("agents.tools.copiedRedirect")}
                              onClick={async () => {
                                const ok = await copyText(
                                  state.oauthRedirectUri!,
                                );
                                setCopyStatus(
                                  t(
                                    ok
                                      ? "agents.tools.redirectCopied"
                                      : "agents.tools.copyFailed",
                                  ),
                                );
                              }}
                            >
                              <Icon name="copy" size={14} />
                            </button>
                          }
                        />
                        {copyStatus && (
                          <span
                            role="status"
                            className="text-caption text-fg-muted"
                          >
                            {copyStatus}
                          </span>
                        )}
                      </label>
                    )}
                  </>
                )}
                <button
                  type="button"
                  aria-expanded={advanced}
                  className="flex items-center gap-2 text-body-sm"
                  onClick={() => setAdvanced(!advanced)}
                >
                  <Icon
                    name={advanced ? "chevron-down" : "chevron-right"}
                    size={14}
                  />
                  {t("agents.tools.advanced")}
                </button>
                {advanced && (
                  <>
                    {state.canUseDevice && (
                      <div className="space-y-1.5">
                        <label className="flex items-center gap-2">
                          <Switch
                            label={t("agents.tools.privateScope")}
                            checked={localOnly}
                            disabled={!!connection}
                            onCheckedChange={setLocalOnly}
                          />
                          <span>{t("agents.tools.privateScope")}</span>
                        </label>
                        <p className="text-caption text-fg-muted">
                          {t("agents.tools.privateHint")}
                        </p>
                      </div>
                    )}
                    {auth === "api_key" && (
                      <label className="block space-y-1.5">
                        <span>{t("agents.tools.authHeader")}</span>
                        <TextInput
                          value={authHeader}
                          onChange={(e) => setAuthHeader(e.target.value)}
                        />
                      </label>
                    )}
                    <PairEditor
                      title={t("agents.tools.headers")}
                      add={t("agents.tools.addHeader")}
                      remove={t("agents.tools.removeHeader")}
                      pairs={draft.headers}
                      keepExisting={
                        unchanged
                          ? new Set(Object.keys(old?.headers ?? {}))
                          : undefined
                      }
                      onChange={(headers) => update({ headers })}
                    />
                  </>
                )}
              </>
            )}
          </>
        )}
        {old &&
          !unchanged &&
          (old.authMode === "api_key" ||
            Object.keys(old.env ?? {}).length > 0 ||
            Object.keys(old.headers ?? {}).length > 0) && (
            <p role="status" className="text-caption text-fg-muted">
              {t("agents.tools.changedTarget")}
            </p>
          )}
        {authMode === "oauth" && scope === "DEVICE_OWNER" && (
          <p role="status" className="text-caption text-fg-muted">
            {t("agents.tools.privateOAuth")}
          </p>
        )}
        {(!supported || (authMode === "oauth" && !state.supportsOAuth)) && (
          <p role="status" className="text-body-sm text-fg-muted">
            {t(
              authMode === "oauth"
                ? "agents.tools.unavailableOAuth"
                : "agents.tools.unsupported",
            )}
          </p>
        )}
        {error && (
          <p role="alert" className="text-body-sm text-danger">
            {error}
          </p>
        )}
      </fieldset>
      <div className="flex shrink-0 justify-end gap-2 border-t border-line-default px-6 py-3">
        <Button variant="outline" disabled={busy} onClick={onClose}>
          {t("common.cancel")}
        </Button>
        <Button type="submit" loading={busy} disabled={!valid}>
          {t(
            reauthorizing
              ? "agents.tools.reauthorize"
              : connection
                ? "common.save"
                : authMode === "oauth"
                  ? "agents.tools.authorize"
                  : "agents.tools.connect",
          )}
        </Button>
      </div>
    </form>
  );
}
