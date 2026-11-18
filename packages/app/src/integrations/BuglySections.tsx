import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  isApiError,
  type BuglyBinding,
  type BuglyBindingAction,
  type BuglyConnectionAction,
  type IntegrationDetail,
  type TaskPriority,
} from "@openfox/api";
import { useT, type Translate } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  ConfirmDialog,
  Dialog,
  Field,
  Select,
  TextInput,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import { useOrganizationSettings } from "../organization/queries";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";

const configurationKey = (orgId: string) => [
  ...keys.integrations(orgId),
  "bugly-configuration",
];

function errorMessage(t: Translate, error: unknown) {
  if (isApiError(error)) {
    if (error.code === "VERSION_CONFLICT")
      return t("integrations.bugly.conflict");
    if (error.status === 403) return t("integrations.bugly.forbidden");
    if (error.code === "BUGLY_INACTIVE")
      return t("integrations.bugly.inactive");
    if (
      error.code === "INTEGRATION_NOT_CONFIGURED" ||
      error.code === "SECRETS_UNAVAILABLE"
    )
      return t("integrations.bugly.notConfigured");
    if (error.status === 400) return t("integrations.bugly.invalid");
    if (error.status === 404) return t("integrations.bugly.unavailable");
  }
  return t("integrations.bugly.saveFailed");
}

export function BuglySections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  return (
    <>
      <Badge>{t("integrations.bugly.capability")}</Badge>
      <Callout tone="info">{t("integrations.bugly.scope")}</Callout>
      {!detail.can_manage ? (
        <Callout tone="info">
          {t("integrations.readOnly", { name: detail.provider.name })}
        </Callout>
      ) : !detail.provider.configured ? (
        <Callout tone="warning">
          {t("integrations.bugly.notConfigured")}
        </Callout>
      ) : (
        <BuglyManager key={orgId} orgId={orgId} />
      )}
    </>
  );
}

type PendingAction =
  | { kind: "connection"; action: BuglyConnectionAction; version: number }
  | { kind: "binding"; action: BuglyBindingAction; binding: BuglyBinding };

function BuglyManager({ orgId }: { orgId: string }) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient();
  const config = useQuery({
    queryKey: configurationKey(orgId),
    queryFn: ({ signal }) => api.bugly.configuration(orgId, signal),
  });
  const connection = config.data?.connection;
  const scope = connection ? `${connection.id}:${connection.version}` : "";
  const currentScope = useRef(scope);
  currentScope.current = scope;
  const secretRequest = useRef<AbortController | null>(null);
  const [secret, setSecret] = useState<{ scope: string; url: string } | null>(
    null,
  );
  const [secretBusy, setSecretBusy] = useState(false),
    [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>();
  const [pending, setPending] = useState<PendingAction | null>(null),
    [conflict, setConflict] = useState(false);
  const [editing, setEditing] = useState<BuglyBinding | "new" | null>(null);
  function clearSecret() {
    secretRequest.current?.abort();
    secretRequest.current = null;
    setSecret(null);
    setSecretBusy(false);
    setCopied(false);
    setCopyFailed(false);
  }
  useEffect(() => {
    clearSecret();
    return () => {
      secretRequest.current?.abort();
      secretRequest.current = null;
    };
  }, [scope]);
  const refresh = () =>
    cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
  async function readSecret() {
    if (!connection || secretBusy || busy) return;
    clearSecret();
    setError(undefined);
    setSecretBusy(true);
    const controller = new AbortController();
    secretRequest.current = controller;
    const requestedScope = scope;
    try {
      const result = await api.bugly.secret(
        orgId,
        connection.id,
        connection.version,
        controller.signal,
      );
      if (
        !controller.signal.aborted &&
        secretRequest.current === controller &&
        currentScope.current === requestedScope &&
        result.version === connection.version
      ) {
        setSecret({ scope: requestedScope, url: result.url });
      }
    } catch (e) {
      if (!controller.signal.aborted && secretRequest.current === controller) {
        setError(e);
        if (isApiError(e) && e.status === 409) void refresh();
      }
    } finally {
      if (secretRequest.current === controller) {
        secretRequest.current = null;
        setSecretBusy(false);
      }
    }
  }
  async function run(action: () => Promise<unknown>) {
    if (busy) return;
    clearSecret();
    setBusy(true);
    setError(undefined);
    try {
      await action();
      setPending(null);
      await refresh();
    } catch (e) {
      setError(e);
      if (isApiError(e) && e.status === 409) {
        setConflict(true);
        await refresh();
      }
    } finally {
      setBusy(false);
    }
  }
  function ask(action: PendingAction) {
    clearSecret();
    setError(undefined);
    setConflict(false);
    setPending(action);
  }
  const actionLabel =
    pending?.kind === "connection" &&
    pending.action === "enable" &&
    connection?.status === "DISCONNECTED"
      ? "reconnect"
      : pending?.action;
  const help =
    pending?.kind === "binding"
      ? pending.action === "remove"
        ? "removeHelp"
        : pending.action === "resume"
          ? "resumeHelp"
          : "pauseHelp"
      : actionLabel === "reconnect"
        ? "reconnectHelp"
        : pending?.action === "rotate"
          ? "rotateHelp"
          : pending?.action === "disconnect"
            ? "disconnectHelp"
            : pending?.action === "disable"
              ? "disableHelp"
              : "enableHelp";
  const unavailable = busy || config.isFetching || config.isError;
  return (
    <>
      <Callout tone="warning">{t("integrations.bugly.authority")}</Callout>
      {error !== undefined && !pending && (
        <Callout tone="danger">{errorMessage(t, error)}</Callout>
      )}
      {config.isError && (
        <Callout tone="danger">{t("integrations.bugly.loadFailed")}</Callout>
      )}
      <SettingsSection
        title={t("integrations.bugly.title")}
        action={
          <Button
            variant="ghost"
            disabled={config.isFetching || busy}
            onClick={() => {
              clearSecret();
              void refresh();
            }}
          >
            {t("integrations.bugly.refresh")}
          </Button>
        }
      >
        {config.isPending ? (
          <p>{t("integrations.bugly.loading")}</p>
        ) : (
          config.data && (
            <SettingsCard>
              <div className="flex flex-col gap-3 p-4">
                {!connection ? (
                  <>
                    <p className="text-body-sm text-fg-muted">
                      {t("integrations.bugly.createdDisabled")}
                    </p>
                    <Button
                      disabled={unavailable}
                      onClick={() => void run(() => api.bugly.create(orgId))}
                    >
                      {t("integrations.bugly.create")}
                    </Button>
                  </>
                ) : (
                  <>
                    <p>{t(`integrations.bugly.states.${connection.status}`)}</p>
                    {connection.status === "NEEDS_ATTENTION" && (
                      <p className="text-body-sm text-fg-muted">
                        {t("integrations.bugly.createdDisabled")}
                      </p>
                    )}
                    <div className="flex flex-wrap gap-2">
                      <Button
                        disabled={unavailable}
                        onClick={() =>
                          ask({
                            kind: "connection",
                            action:
                              connection.status === "ACTIVE"
                                ? "disable"
                                : "enable",
                            version: connection.version,
                          })
                        }
                      >
                        {t(
                          connection.status === "ACTIVE"
                            ? "integrations.bugly.disable"
                            : connection.status === "DISCONNECTED"
                              ? "integrations.bugly.reconnect"
                              : "integrations.bugly.enable",
                        )}
                      </Button>
                      {connection.status !== "DISCONNECTED" && (
                        <>
                          <Button
                            variant="outline"
                            disabled={unavailable}
                            onClick={() =>
                              ask({
                                kind: "connection",
                                action: "rotate",
                                version: connection.version,
                              })
                            }
                          >
                            {t("integrations.bugly.rotate")}
                          </Button>
                          <Button
                            variant="outline"
                            disabled={unavailable}
                            onClick={() =>
                              ask({
                                kind: "connection",
                                action: "disconnect",
                                version: connection.version,
                              })
                            }
                          >
                            {t("integrations.bugly.disconnect")}
                          </Button>
                          <Button
                            variant="outline"
                            disabled={unavailable || secretBusy}
                            onClick={() =>
                              secret?.scope === scope
                                ? clearSecret()
                                : void readSecret()
                            }
                          >
                            {t(
                              secret?.scope === scope
                                ? "integrations.bugly.hideSecret"
                                : "integrations.bugly.showSecret",
                            )}
                          </Button>
                        </>
                      )}
                    </div>
                    {secret?.scope === scope && (
                      <Field
                        label={t("integrations.bugly.secret")}
                        error={
                          copyFailed
                            ? t("integrations.bugly.copyFailed")
                            : undefined
                        }
                      >
                        <TextInput
                          readOnly
                          value={secret.url}
                          autoComplete="off"
                          spellCheck={false}
                        />
                        <Button
                          variant="outline"
                          onClick={async () => {
                            try {
                              await navigator.clipboard.writeText(secret.url);
                              setCopied(true);
                              setCopyFailed(false);
                            } catch {
                              setCopyFailed(true);
                            }
                          }}
                        >
                          {t(
                            copied
                              ? "integrations.bugly.copied"
                              : "integrations.bugly.copySecret",
                          )}
                        </Button>
                      </Field>
                    )}
                  </>
                )}
              </div>
            </SettingsCard>
          )
        )}
      </SettingsSection>
      {config.data && (
        <>
          <SettingsSection
            title={t("integrations.bugly.bindings")}
            description={t("integrations.bugly.triage")}
            action={
              connection && connection.status !== "DISCONNECTED" ? (
                <Button
                  variant="outline"
                  disabled={unavailable}
                  onClick={() => {
                    clearSecret();
                    setEditing("new");
                  }}
                >
                  {t("integrations.bugly.addBinding")}
                </Button>
              ) : undefined
            }
          >
            {!config.data.bindings.length ? (
              <p className="text-body-sm text-fg-muted">
                {t("integrations.bugly.noBindings")}
              </p>
            ) : (
              config.data.bindings.map((binding) => (
                <SettingsCard key={binding.id}>
                  <div className="flex flex-col gap-2 p-4">
                    <h3 className="text-body font-medium">
                      {binding.project_name || binding.product_id}
                    </h3>
                    <p className="break-all text-body-sm">
                      {t("integrations.bugly.product")}: {binding.product_id}
                    </p>
                    <p>{t(`integrations.bugly.states.${binding.status}`)}</p>
                    {binding.status_reason && (
                      <p className="text-body-sm text-fg-muted">
                        {t(
                          binding.status_reason === "rollback_disabled"
                            ? "integrations.bugly.rollbackDisabled"
                            : binding.status_reason === "authorization_lost"
                              ? "integrations.bugly.details.authorization_lost"
                              : binding.status_reason ===
                                  "task_type_unavailable"
                                ? "integrations.bugly.details.task_type_unavailable"
                                : "integrations.bugly.unavailable",
                        )}
                      </p>
                    )}
                    <div className="flex flex-wrap gap-2">
                      <Button
                        variant="outline"
                        disabled={unavailable}
                        onClick={() => {
                          clearSecret();
                          setEditing(binding);
                        }}
                      >
                        {t("integrations.bugly.editBinding")}
                      </Button>
                      <Button
                        variant="outline"
                        disabled={
                          unavailable ||
                          (binding.status !== "ACTIVE" &&
                            connection?.status === "DISCONNECTED")
                        }
                        onClick={() =>
                          ask({
                            kind: "binding",
                            action:
                              binding.status === "ACTIVE" ? "pause" : "resume",
                            binding,
                          })
                        }
                      >
                        {t(
                          binding.status === "ACTIVE"
                            ? "integrations.bugly.pause"
                            : "integrations.bugly.resume",
                        )}
                      </Button>
                      <Button
                        variant="outline"
                        disabled={unavailable}
                        onClick={() =>
                          ask({ kind: "binding", action: "remove", binding })
                        }
                      >
                        {t("integrations.bugly.remove")}
                      </Button>
                    </div>
                  </div>
                </SettingsCard>
              ))
            )}
          </SettingsSection>
          <SettingsSection
            title={t("integrations.bugly.deliveries")}
            description={t("integrations.bugly.deliveriesHelp")}
          >
            {!config.data.deliveries.length ? (
              <p className="text-body-sm text-fg-muted">
                {t("integrations.bugly.noDeliveries")}
              </p>
            ) : (
              <ul className="flex flex-col gap-2">
                {config.data.deliveries.map((item) => (
                  <li
                    key={item.id}
                    className="flex flex-wrap gap-2 text-body-sm"
                  >
                    <time dateTime={item.received_at}>
                      {new Date(item.received_at).toLocaleString()}
                    </time>
                    <span>
                      {t(`integrations.bugly.receipt.${item.status}`)}
                    </span>
                    <span>
                      {t(`integrations.bugly.details.${item.detail}`)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </SettingsSection>
        </>
      )}
      {pending && connection && (
        <ConfirmDialog
          open
          title={t(`integrations.bugly.${actionLabel!}`)}
          description={t(`integrations.bugly.${help}`)}
          confirmLabel={t(`integrations.bugly.${actionLabel!}`)}
          danger={
            pending.action === "disconnect" || pending.action === "remove"
          }
          loading={busy}
          confirmDisabled={conflict || config.isError}
          onCancel={() => {
            if (!busy) {
              setPending(null);
              setError(undefined);
            }
          }}
          onConfirm={() =>
            void run(() =>
              pending.kind === "connection"
                ? api.bugly.change(
                    orgId,
                    connection.id,
                    pending.action,
                    pending.version,
                  )
                : api.bugly.changeBinding(
                    orgId,
                    connection.id,
                    pending.binding.id,
                    pending.action,
                    pending.binding.version,
                  ),
            )
          }
        >
          {error !== undefined && (
            <Callout tone="danger">{errorMessage(t, error)}</Callout>
          )}
        </ConfirmDialog>
      )}
      {editing && connection && (
        <BindingDialog
          key={`${connection.id}:${editing === "new" ? "new" : editing.id}`}
          orgId={orgId}
          connectionId={connection.id}
          connectionVersion={connection.version}
          binding={editing === "new" ? undefined : editing}
          refresh={refresh}
          onClose={() => setEditing(null)}
        />
      )}
    </>
  );
}

const priorities: TaskPriority[] = [
  "NONE",
  "LOWEST",
  "LOW",
  "NORMAL",
  "HIGH",
  "URGENT",
];
function BindingDialog({
  orgId,
  connectionId,
  connectionVersion,
  binding,
  refresh,
  onClose,
}: {
  orgId: string;
  connectionId: string;
  connectionVersion: number;
  binding?: BuglyBinding;
  refresh: () => Promise<unknown>;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT();
  // Keep the reviewed version for this dialog. A refresh must not silently
  // authorize a stale draft against a newly rotated/reconnected connection.
  const [reviewedVersion] = useState(connectionVersion);
  const [product, setProduct] = useState(binding?.product_id ?? ""),
    [project, setProject] = useState(binding?.project_name ?? "");
  const [space, setSpace] = useState(binding?.space_id ?? ""),
    [type, setType] = useState(binding?.task_type_id ?? "");
  const [mapping, setMapping] = useState(() =>
    Object.entries(binding?.priority_mapping ?? {}).map(
      ([external, priority]) => ({ external, priority }),
    ),
  );
  const [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(),
    [conflict, setConflict] = useState(false);
  const spaces = useQuery({
    queryKey: [...configurationKey(orgId), "spaces"],
    queryFn: () => api.spaces.list(orgId),
  });
  const types = useQuery({
    queryKey: [...configurationKey(orgId), "types"],
    queryFn: () => api.orgTaskTypes.list(orgId),
  });
  const settings = useOrganizationSettings(orgId);
  const candidates =
    spaces.data?.filter(
      (item) => item.can_manage && item.status === "active",
    ) ?? [];
  const taskTypes =
    types.data?.find((item) => item.space_id === space)?.task_types ?? [];
  const failed = spaces.isError || types.isError || settings.isError;
  const ready = spaces.isSuccess && types.isSuccess && settings.isSuccess;
  const mappingValid =
    mapping.length <= 20 &&
    mapping.every(
      (item) =>
        item.external.trim().length > 0 &&
        Array.from(item.external).length <= 64 &&
        (item.priority !== "LOWEST" || settings.data?.priority_levels === 5),
    ) &&
    new Set(mapping.map((item) => item.external)).size === mapping.length;
  const valid =
    ready &&
    !failed &&
    candidates.some((item) => item.id === space) &&
    taskTypes.some((item) => item.id === type) &&
    product.trim().length > 0 &&
    Array.from(product).length <= 255 &&
    Array.from(project).length <= 200 &&
    mappingValid;
  async function save() {
    if (!valid || busy || conflict) return;
    setBusy(true);
    setError(undefined);
    const fields = {
      project_name: project,
      task_type_id: type,
      priority_mapping: Object.fromEntries(
        mapping.map((item) => [item.external, item.priority]),
      ),
    };
    try {
      if (binding)
        await api.bugly.updateBinding(orgId, connectionId, binding.id, {
          ...fields,
          version: binding.version,
        });
      else
        await api.bugly.createBinding(orgId, connectionId, {
          ...fields,
          connection_version: reviewedVersion,
          product_id: product,
          space_id: space,
        });
      await refresh();
      onClose();
    } catch (e) {
      setError(e);
      if (isApiError(e) && e.status === 409) {
        setConflict(true);
        await refresh();
      }
    } finally {
      setBusy(false);
    }
  }
  return (
    <Dialog
      open
      title={t(
        binding
          ? "integrations.bugly.editBinding"
          : "integrations.bugly.addBinding",
      )}
      onClose={() => {
        if (!busy) onClose();
      }}
      footer={
        <>
          <Button variant="ghost" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            disabled={!valid || conflict}
            loading={busy}
            onClick={() => void save()}
          >
            {t("common.save")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error !== undefined && (
          <Callout tone="danger">{errorMessage(t, error)}</Callout>
        )}
        {failed && (
          <Callout tone="danger">{t("integrations.bugly.loadFailed")}</Callout>
        )}
        <Field
          label={t("integrations.bugly.product")}
          helper={t("integrations.bugly.productHelp")}
          required
        >
          <TextInput
            value={product}
            disabled={Boolean(binding) || busy}
            onChange={(e) => setProduct(e.target.value)}
          />
        </Field>
        <Field label={t("integrations.bugly.projectName")}>
          <TextInput
            value={project}
            disabled={busy}
            onChange={(e) => setProject(e.target.value)}
          />
        </Field>
        <Field
          label={t("integrations.bugly.space")}
          helper={t("integrations.bugly.spaceHelp")}
          required
        >
          <Select
            value={space || null}
            disabled={Boolean(binding) || busy || !ready}
            options={candidates.map((item) => ({
              value: item.id,
              label: item.name,
            }))}
            placeholder={t("integrations.bugly.chooseSpace")}
            onChange={(value) => {
              setSpace(value);
              setType("");
            }}
          />
        </Field>
        <Field
          label={t("integrations.bugly.type")}
          helper={
            space && ready && !taskTypes.length
              ? t("integrations.bugly.noTypes")
              : undefined
          }
          required
        >
          <Select
            value={type || null}
            disabled={busy || !space || !ready}
            options={taskTypes.map((item) => ({
              value: item.id,
              label: item.name,
            }))}
            placeholder={t("integrations.bugly.chooseType")}
            onChange={setType}
          />
        </Field>
        {binding && (
          <Callout tone="info">{t("integrations.bugly.fixedTarget")}</Callout>
        )}
        <fieldset className="flex flex-col gap-3" disabled={busy}>
          <legend className="text-body font-medium">
            {t("integrations.bugly.priority")}
          </legend>
          <p className="text-body-sm text-fg-muted">
            {t("integrations.bugly.priorityHelp")}
          </p>
          {mapping.map((item, index) => (
            <div key={index} className="flex flex-wrap items-end gap-2">
              <Field
                label={`${t("integrations.bugly.externalPriority")} ${index + 1}`}
              >
                <TextInput
                  value={item.external}
                  onChange={(e) =>
                    setMapping((rows) =>
                      rows.map((row, n) =>
                        n === index
                          ? { ...row, external: e.target.value }
                          : row,
                      ),
                    )
                  }
                />
              </Field>
              <Field
                label={`${t("integrations.bugly.targetPriority")} ${index + 1}`}
              >
                <Select
                  value={item.priority}
                  disabled={busy}
                  options={priorities
                    .filter(
                      (priority) =>
                        priority !== "LOWEST" ||
                        settings.data?.priority_levels === 5 ||
                        item.priority === "LOWEST",
                    )
                    .map((priority) => ({
                      value: priority,
                      label: t(`tasks.priority.${priority}`),
                      disabled:
                        priority === "LOWEST" &&
                        settings.data?.priority_levels !== 5,
                    }))}
                  onChange={(priority) =>
                    setMapping((rows) =>
                      rows.map((row, n) =>
                        n === index ? { ...row, priority } : row,
                      ),
                    )
                  }
                />
              </Field>
              <Button
                variant="ghost"
                aria-label={t("integrations.bugly.removePriority", {
                  index: index + 1,
                })}
                onClick={() =>
                  setMapping((rows) => rows.filter((_, n) => n !== index))
                }
              >
                {t("integrations.bugly.removePriority", { index: index + 1 })}
              </Button>
            </div>
          ))}
          {!mappingValid && (
            <p role="alert" className="text-body-sm text-danger">
              {t("integrations.bugly.priorityInvalid")}
            </p>
          )}
          <Button
            variant="outline"
            disabled={busy || mapping.length >= 20}
            onClick={() =>
              setMapping((rows) => [
                ...rows,
                { external: "", priority: "NONE" },
              ])
            }
          >
            {t("integrations.bugly.addPriority")}
          </Button>
        </fieldset>
      </div>
    </Dialog>
  );
}
