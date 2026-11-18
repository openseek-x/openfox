import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  IntegrationConnection,
  IntegrationDetail,
  IntegrationSettingsValues,
  IntegrationSyncLink,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  ConfirmDialog,
  Dialog,
  Field,
  Select,
  Switch,
  TextInput,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { DisconnectDialog } from "./DisconnectDialog";
import { GitCodeRepositoriesDialog } from "./GitCodeRepositoriesDialog";
import { BRANCH_FORMATS } from "./model";
import {
  integrationErrorText,
  useIntegrationSettings,
  usePutIntegrationSettings,
  useRotateWebhook,
} from "./queries";
import { gitcodeApi, type GitCodeRepository } from "./gitcode-api";

export function GitCodeSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  const [binding, setBinding] = useState(false);
  return (
    <>
      <Callout tone="info">{t("integrations.gitcode.scope")}</Callout>
      <SettingsSection title={t("integrations.gitcode.title")}>
        {detail.can_manage ? (
          <Button
            onClick={() => setBinding(true)}
            disabled={!detail.provider.bindable}
          >
            {t("integrations.gitcode.connect")}
          </Button>
        ) : (
          <Callout tone="info">{t("integrations.gitcode.readOnly")}</Callout>
        )}
        {detail.connections.map((c) =>
          detail.can_manage ? (
            <GitCodeConnection
              key={c.id}
              orgId={orgId}
              detail={detail}
              connection={c}
            />
          ) : (
            <SettingsCard key={c.id}>
              <SettingRow title={c.display_name} description={c.status} />
            </SettingsCard>
          ),
        )}
      </SettingsSection>
      <GitCodeSettings orgId={orgId} canManage={detail.can_manage} />
      {binding && (
        <GitCodeTokenDialog orgId={orgId} onClose={() => setBinding(false)} />
      )}
    </>
  );
}
export function GitCodeTokenDialog({
  orgId,
  connection,
  onClose,
}: {
  orgId: string;
  connection?: IntegrationConnection;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const [token, setToken] = useState(""),
    [organization, setOrganization] = useState("");
  const discover = useMutation({
    mutationFn: () => gitcodeApi(api.http).discover(orgId, token.trim()),
    onSuccess: () => setOrganization(""),
  });
  const save = useMutation({
    mutationFn: () =>
      gitcodeApi(api.http).save(
        orgId,
        token.trim(),
        connection?.external_id ?? organization,
        connection,
      ),
    onSuccess: () => {
      setToken("");
      void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
      onClose();
    },
  });
  const busy = save.isPending || discover.isPending;
  return (
    <Dialog
      open
      title={t(
        connection
          ? "integrations.gitcode.credentials"
          : "integrations.gitcode.connect",
      )}
      onClose={busy ? undefined : onClose}
      footer={
        <>
          <Button variant="outline" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            loading={save.isPending}
            disabled={
              !token.trim() ||
              (!connection && (!discover.data || !organization)) ||
              discover.isPending
            }
            onClick={() => save.mutate()}
          >
            {t(
              connection
                ? "integrations.gitcode.saveCredentials"
                : "integrations.gitcode.connect",
            )}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <p>{t("integrations.gitcode.sameSubject")}</p>
        <Field label={t("integrations.gitcode.token")}>
          <TextInput
            type="password"
            autoComplete="new-password"
            value={token}
            maxLength={4096}
            disabled={busy}
            onChange={(e) => {
              setToken(e.target.value);
              discover.reset();
              setOrganization("");
            }}
          />
        </Field>
        {!connection && (
          <>
            <Button
              variant="outline"
              disabled={!token.trim() || busy}
              loading={discover.isPending}
              onClick={() => discover.mutate()}
            >
              {t("integrations.gitcode.discover")}
            </Button>
            {discover.data && (
              <>
                <p>
                  {t("integrations.gitcode.identity", {
                    login: discover.data.principal.login,
                    subject: discover.data.principal.id,
                  })}
                </p>
                <Field label={t("integrations.gitcode.organization")}>
                  <Select
                    value={organization || null}
                    onChange={setOrganization}
                    options={discover.data.organizations.map((o) => ({
                      value: String(o.id),
                      label: `${o.name || o.login} (${o.id})`,
                    }))}
                  />
                </Field>
              </>
            )}
          </>
        )}
        {(discover.isError || save.isError) && (
          <Callout tone="danger">
            {integrationErrorText(t, save.error ?? discover.error)}
          </Callout>
        )}
      </div>
    </Dialog>
  );
}
function GitCodeSettings({
  orgId,
  canManage,
}: {
  orgId: string;
  canManage: boolean;
}) {
  const t = useT(),
    settings = useIntegrationSettings(orgId, "gitcode"),
    put = usePutIntegrationSettings(orgId, "gitcode");
  const [confirm, setConfirm] = useState<
    keyof IntegrationSettingsValues | null
  >(null);
  const fields = [
    "link_pull_requests",
    "link_commits",
    "pr_backlink_private",
    "pr_backlink_public",
    "issue_backlink_private",
    "issue_backlink_public",
  ] as const;
  const save = (
    field: keyof IntegrationSettingsValues,
    value: boolean | string,
  ) => {
    if (settings.data)
      put.mutate(
        {
          version: settings.data.version,
          settings: { ...settings.data.settings, [field]: value },
        },
        { onSuccess: () => setConfirm(null) },
      );
  };
  return (
    <SettingsSection
      title={t("integrations.gitcode.settings")}
      description={t("integrations.gitcode.outboundWarning")}
    >
      {(settings.isError || put.isError) && (
        <Callout tone="danger">
          {integrationErrorText(t, put.error ?? settings.error)}
        </Callout>
      )}
      {settings.data && (
        <SettingsCard>
          {fields.map((field) => (
            <SettingRow
              key={field}
              title={t(`integrations.gitcode.${field}`)}
              control={
                <Switch
                  label={t(`integrations.gitcode.${field}`)}
                  checked={settings.data.settings[field]}
                  disabled={!canManage || put.isPending}
                  onCheckedChange={(value) => {
                    if (value && field.includes("backlink")) setConfirm(field);
                    else save(field, value);
                  }}
                />
              }
            />
          ))}
          <SettingRow
            title={t("integrations.gitcode.branch_format")}
            control={
              <Select
                value={settings.data.settings.branch_format}
                disabled={!canManage || put.isPending}
                options={BRANCH_FORMATS.map((v) => ({ value: v, label: v }))}
                onChange={(value) => save("branch_format", value)}
              />
            }
          />
        </SettingsCard>
      )}
      <ConfirmDialog
        open={!!confirm}
        title={t("integrations.gitcode.enableTitle")}
        description={t("integrations.gitcode.outboundWarning")}
        loading={put.isPending}
        onCancel={() => setConfirm(null)}
        onConfirm={() => {
          if (confirm) save(confirm, true);
        }}
      />
    </SettingsSection>
  );
}
function GitCodeConnection({
  orgId,
  detail,
  connection,
}: {
  orgId: string;
  detail: IntegrationDetail;
  connection: IntegrationConnection;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const [repoCursors, setRepoCursors] = useState<string[]>([]),
    [workCursors, setWorkCursors] = useState<string[]>([]);
  const [credentials, setCredentials] = useState(false),
    [adding, setAdding] = useState(false),
    [disconnect, setDisconnect] = useState(false),
    [rotateConfirm, setRotateConfirm] = useState(false);
  const [action, setAction] = useState<{
      kind: "remove" | "history" | "bidi";
      repo: GitCodeRepository;
      link?: IntegrationSyncLink;
    } | null>(null),
    [deleteHook, setDeleteHook] = useState(false);
  const configKey = [
    ...keys.integrations(orgId),
    "gitcode-config",
    connection.id,
  ];
  const config = useQuery({
    queryKey: [
      ...configKey,
      repoCursors.at(-1) ?? "",
      workCursors.at(-1) ?? "",
    ],
    queryFn: () =>
      gitcodeApi(api.http).config(
        orgId,
        connection.id,
        repoCursors.at(-1),
        workCursors.at(-1),
      ),
    retry: false,
    refetchInterval: (q) =>
      connection.status === "ACTIVE" &&
      (q.state.data?.repositories.some((r) =>
        ["PENDING", "DELETE_PENDING"].includes(r.hook_state),
      ) ||
        q.state.data?.work.some(
          (w) =>
            ["PENDING", "SENDING"].includes(w.state) &&
            w.next_attempt_at &&
            Date.parse(w.next_attempt_at) <= Date.now() + 10_000,
        ))
        ? 2000
        : false,
  });
  const refresh = () =>
    cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
  const change = useMutation({
    mutationFn: ({
      link,
      status,
      direction,
    }: {
      link: IntegrationSyncLink;
      status?: "ACTIVE" | "PAUSED";
      direction?: "INBOUND" | "BIDIRECTIONAL";
    }) =>
      api.integrations.updateSyncLink(orgId, link.id, {
        version: link.version,
        ...(status ? { status } : {}),
        ...(direction ? { direction } : {}),
      }),
    onSettled: refresh,
    onSuccess: () => setAction(null),
  });
  const mutate = useMutation({
    mutationFn: async () => {
      if (!action) return;
      if (action.kind === "history")
        await gitcodeApi(api.http).history(orgId, connection.id, action.repo);
      else
        await gitcodeApi(api.http).remove(
          orgId,
          connection.id,
          action.repo,
          deleteHook,
        );
    },
    onSettled: refresh,
    onSuccess: () => setAction(null),
  });
  const reconcile = useMutation({
    mutationFn: (id: string) =>
      gitcodeApi(api.http).reconcile(
        orgId,
        connection.id,
        id,
        config.data!.version,
      ),
    onSettled: refresh,
  });
  const rotate = useRotateWebhook(orgId, connection.id);
  const busy =
    change.isPending ||
    mutate.isPending ||
    reconcile.isPending ||
    rotate.isPending;
  const current = {
    ...connection,
    version: config.data?.version ?? connection.version,
  };
  const active = connection.status === "ACTIVE";
  return (
    <SettingsCard>
      <SettingRow
        title={connection.display_name}
        description={
          config.data
            ? t("integrations.gitcode.identity", {
                login: config.data.login,
                subject: config.data.subject,
              })
            : connection.external_id
        }
        control={
          <Badge tone={active ? "success" : "warning"}>
            {connection.status}
          </Badge>
        }
      />
      <div className="flex flex-col gap-3 p-4">
        {connection.status === "DISCONNECTED" && (
          <Callout tone="info">{t("integrations.gitcode.cleanup")}</Callout>
        )}
        {config.isError && (
          <Callout tone="danger">
            {integrationErrorText(t, config.error)}
          </Callout>
        )}
        {[change.error, mutate.error, reconcile.error, rotate.error]
          .filter(Boolean)
          .map((e, i) => (
            <Callout key={i} tone="danger">
              {integrationErrorText(t, e)}
            </Callout>
          ))}
        <div className="flex flex-wrap gap-2">
          <Button size="28" variant="outline" onClick={() => void refresh()}>
            {t("integrations.gitcode.refresh")}
          </Button>
          {connection.status !== "DISCONNECTED" && (
            <>
              <Button
                size="28"
                variant="outline"
                disabled={!config.data || busy}
                onClick={() => setCredentials(true)}
              >
                {t("integrations.gitcode.credentials")}
              </Button>
              <Button
                size="28"
                variant="outline"
                disabled={!active || !config.data || busy}
                onClick={() => setAdding(true)}
              >
                {t("integrations.gitcode.add")}
              </Button>
              <Button
                size="28"
                variant="outline"
                disabled={!active || !config.data || busy}
                onClick={() => setRotateConfirm(true)}
              >
                {t("integrations.gitcode.rotate")}
              </Button>
              <Button
                size="28"
                variant="outline"
                disabled={!config.data || busy}
                onClick={() => setDisconnect(true)}
              >
                {t("integrations.disconnect")}
              </Button>
            </>
          )}
        </div>
        <p className="text-body-sm text-fg-muted">
          {t("integrations.gitcode.permissionHelp")}
        </p>
        {config.data?.repositories.length === 0 && (
          <p>{t("integrations.gitcode.empty")}</p>
        )}
        {config.data?.repositories.map((r) => {
          const link = config.data.links.find((l) => l.id === r.sync_link_id);
          return (
            <section
              key={r.id}
              className="rounded-lg border border-line-default p-3 flex flex-col gap-2"
              aria-label={r.path}
            >
              <strong>
                {r.path} · {r.visibility}
              </strong>
              <p className="text-body-sm">
                {t("integrations.gitcode.status", {
                  status: link?.status ?? (r.active ? "ACTIVE" : "REMOVED"),
                  hook: r.hook_state,
                })}{" "}
                · {link?.space_name} · {link?.direction}
              </p>
              <p className="text-body-sm">
                {t("integrations.gitcode.hookId", { id: r.hook_id || "—" })}
              </p>
              <p className="text-body-sm">
                {t("integrations.gitcode.permissions", {
                  issue: r.issue_write_status,
                  comment: r.comment_write_status,
                })}
              </p>
              {r.error_code && (
                <Callout tone="warning">
                  {t("integrations.gitcode.error", { code: r.error_code })}
                </Callout>
              )}
              {r.error_code === "repository_changed" && (
                <Callout tone="warning">
                  {t("integrations.gitcode.repositoryChanged")}
                </Callout>
              )}
              {link?.status_reason === "webhook_gap_requires_review" && (
                <Callout tone="warning">
                  {t("integrations.gitcode.gap")}
                </Callout>
              )}
              {link && link.backfill.status !== "none" && (
                <p role="status">
                  {t("integrations.gitcode.history")} · {link.backfill.status} ·{" "}
                  {link.backfill.imported}
                  {link.backfill.error ? ` · ${link.backfill.error}` : ""}
                </p>
              )}
              {active && r.active && link && (
                <div className="flex flex-wrap gap-2">
                  <Button
                    size="28"
                    variant="outline"
                    disabled={
                      busy ||
                      (link.status !== "ACTIVE" &&
                        (r.hook_state !== "ACTIVE" ||
                          r.error_code === "repository_changed"))
                    }
                    onClick={() =>
                      change.mutate({
                        link,
                        status: link.status === "ACTIVE" ? "PAUSED" : "ACTIVE",
                      })
                    }
                  >
                    {t(
                      link.status === "ACTIVE"
                        ? "integrations.gitcode.pause"
                        : "integrations.gitcode.resume",
                    )}
                  </Button>
                  <Button
                    size="28"
                    variant="outline"
                    disabled={busy}
                    onClick={() => {
                      if (link.direction === "INBOUND")
                        setAction({ kind: "bidi", repo: r, link });
                      else change.mutate({ link, direction: "INBOUND" });
                    }}
                  >
                    {t("integrations.gitcode.changeDirection")}
                  </Button>
                  <Button
                    size="28"
                    variant="outline"
                    disabled={
                      busy ||
                      link.status !== "ACTIVE" ||
                      r.hook_state !== "ACTIVE"
                    }
                    onClick={() => setAction({ kind: "history", repo: r })}
                  >
                    {t("integrations.gitcode.historyNow")}
                  </Button>
                  <Button
                    size="28"
                    variant="outline"
                    disabled={busy}
                    onClick={() => {
                      setDeleteHook(false);
                      setAction({ kind: "remove", repo: r });
                    }}
                  >
                    {t("integrations.gitcode.remove")}
                  </Button>
                </div>
              )}
            </section>
          );
        })}
        <div className="flex gap-2">
          <Button
            size="28"
            variant="outline"
            disabled={!repoCursors.length}
            onClick={() => setRepoCursors((v) => v.slice(0, -1))}
          >
            {t("integrations.gitcode.previous")}
          </Button>
          <Button
            size="28"
            variant="outline"
            disabled={!config.data?.next_repository_after}
            onClick={() =>
              setRepoCursors((v) => [...v, config.data!.next_repository_after])
            }
          >
            {t("integrations.gitcode.repoPage")}
          </Button>
        </div>
        <h3 className="font-medium">{t("integrations.gitcode.work")}</h3>
        {config.data?.work.length === 0 && (
          <p>{t("integrations.gitcode.workEmpty")}</p>
        )}
        {config.data?.work.map((w) => (
          <div
            key={w.id}
            className="rounded-lg border border-line-default p-3 flex flex-col gap-1"
          >
            <p>
              {w.kind} · {w.state} · {w.remote_id || "—"}
            </p>
            <p className="text-caption text-fg-muted">{w.repository_id}</p>
            {w.error_code && (
              <p>{t("integrations.gitcode.error", { code: w.error_code })}</p>
            )}
            {w.state === "UNKNOWN" && (
              <Callout tone="warning">
                {t("integrations.gitcode.unknown")}
              </Callout>
            )}
            {active && ["UNKNOWN", "FAILED"].includes(w.state) && (
              <Button
                size="28"
                variant="outline"
                disabled={busy}
                onClick={() => reconcile.mutate(w.id)}
              >
                {t(
                  w.state === "UNKNOWN"
                    ? "integrations.gitcode.reconcile"
                    : "integrations.gitcode.retry",
                )}
              </Button>
            )}
          </div>
        ))}
        <div className="flex gap-2">
          <Button
            size="28"
            variant="outline"
            disabled={!workCursors.length}
            onClick={() => setWorkCursors((v) => v.slice(0, -1))}
          >
            {t("integrations.gitcode.previous")}
          </Button>
          <Button
            size="28"
            variant="outline"
            disabled={!config.data?.next_work_before}
            onClick={() =>
              setWorkCursors((v) => [...v, config.data!.next_work_before])
            }
          >
            {t("integrations.gitcode.workPage")}
          </Button>
        </div>
      </div>
      {credentials && (
        <GitCodeTokenDialog
          orgId={orgId}
          connection={current}
          onClose={() => setCredentials(false)}
        />
      )}
      {adding && config.data && (
        <GitCodeRepositoriesDialog
          orgId={orgId}
          connection={current}
          onClose={() => setAdding(false)}
        />
      )}
      {disconnect && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={current}
          body={t("integrations.gitcode.disconnectBody")}
          onClose={() => setDisconnect(false)}
        />
      )}
      <ConfirmDialog
        open={rotateConfirm}
        title={t("integrations.gitcode.rotateTitle")}
        description={t("integrations.gitcode.rotateBody")}
        loading={rotate.isPending}
        onCancel={() => setRotateConfirm(false)}
        onConfirm={() =>
          rotate.mutate(current.version, {
            onSuccess: () => setRotateConfirm(false),
          })
        }
      />
      <ConfirmDialog
        open={!!action}
        title={t(
          action?.kind === "bidi"
            ? "integrations.gitcode.bidiTitle"
            : action?.kind === "history"
              ? "integrations.gitcode.historyConfirm"
              : "integrations.gitcode.removeTitle",
        )}
        description={t(
          action?.kind === "bidi"
            ? "integrations.gitcode.bidiHelp"
            : action?.kind === "history"
              ? "integrations.gitcode.historyBody"
              : "integrations.gitcode.removeBody",
        )}
        loading={busy}
        onCancel={() => setAction(null)}
        onConfirm={() => {
          if (action?.kind === "bidi" && action.link)
            change.mutate({ link: action.link, direction: "BIDIRECTIONAL" });
          else mutate.mutate();
        }}
      >
        {action?.kind === "remove" && (
          <label className="flex items-center gap-2">
            <Checkbox
              checked={deleteHook}
              onChange={(e) => setDeleteHook(e.target.checked)}
              disabled={busy}
            />
            {t("integrations.gitcode.deleteHook")}
          </label>
        )}
      </ConfirmDialog>
    </SettingsCard>
  );
}
