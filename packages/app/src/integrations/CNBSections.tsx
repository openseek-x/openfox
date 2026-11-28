import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  CNBConfiguration,
  CNBRepository,
  IntegrationConnection,
  IntegrationDetail,
  IntegrationSettingsValues,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import {
  Button,
  Callout,
  Checkbox,
  ConfirmDialog,
  Field,
  Switch,
  TextInput,
  useToast,
} from "@openfox/ui";
import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import { useCopy } from "../tasks/parts";
import {
  integrationErrorText,
  useIntegrationSettings,
  useIntegrationWebhook,
  usePutIntegrationSettings,
  useRotateWebhook,
} from "./queries";

export function CNBSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  const settings = useIntegrationSettings(orgId, "cnb"),
    put = usePutIntegrationSettings(orgId, "cnb");
  const fields = [
    "link_pull_requests",
    "link_commits",
    "pr_backlink_private",
    "pr_backlink_public",
  ] as const;
  function save(field: keyof IntegrationSettingsValues, value: boolean) {
    if (settings.data)
      put.mutate({
        version: settings.data.version,
        settings: { ...settings.data.settings, [field]: value },
      });
  }
  return (
    <>
      <SettingsSection
        title={t("integrations.cnb.title")}
        description={t("integrations.cnb.scope")}
      >
        <Callout tone="info">{t("integrations.cnb.cost")}</Callout>
        {settings.isError && (
          <Callout tone="danger">
            {integrationErrorText(t, settings.error)}
          </Callout>
        )}
        {put.isError && (
          <Callout tone="danger">{integrationErrorText(t, put.error)}</Callout>
        )}
        {settings.data && (
          <SettingsCard>
            {fields.map((field) => (
              <SettingRow
                key={field}
                title={t(`integrations.cnb.${field}`)}
                control={
                  <Switch
                    label={t(`integrations.cnb.${field}`)}
                    checked={settings.data.settings[field]}
                    disabled={!detail.can_manage || put.isPending}
                    onCheckedChange={(value) => save(field, value)}
                  />
                }
              />
            ))}
          </SettingsCard>
        )}
      </SettingsSection>
      {detail.can_manage ? (
        detail.connections
          .filter((c) => c.status !== "DISCONNECTED")
          .map((c) => <CNBConnection key={c.id} orgId={orgId} connection={c} />)
      ) : (
        <Callout tone="info">
          {t("integrations.readOnly", { name: "CNB" })}
        </Callout>
      )}
    </>
  );
}

function CNBConnection({
  orgId,
  connection,
}: {
  orgId: string;
  connection: IntegrationConnection;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    copy = useCopy();
  const [showHook, setShowHook] = useState(false),
    [confirm, setConfirm] = useState(false);
  const configKey = [...keys.integrations(orgId), "cnb", connection.id];
  const config = useQuery({
    queryKey: configKey,
    queryFn: () => api.integrations.cnbConfiguration(orgId, connection.id),
    retry: false,
  });
  const webhook = useIntegrationWebhook(orgId, connection.id, showHook),
    rotate = useRotateWebhook(orgId, connection.id);
  const reconcile = useMutation({
    mutationFn: (work: CNBConfiguration["work"][number]) =>
      api.integrations.reconcileCNB(
        orgId,
        connection.id,
        work.repository_id,
        work.number,
      ),
    onSuccess: () => cache.invalidateQueries({ queryKey: configKey }),
  });
  const refresh = () => {
    void config.refetch();
    if (showHook) void webhook.refetch();
  };
  return (
    <SettingsSection
      title={t("integrations.cnb.connection", {
        name: connection.display_name,
      })}
      action={
        <Button variant="outline" onClick={refresh}>
          {t("integrations.cnb.refresh")}
        </Button>
      }
    >
      {connection.status !== "ACTIVE" && (
        <Callout tone="warning">
          {t("integrations.sync.errors.needsReauth")}
        </Callout>
      )}
      {config.isError && (
        <Callout tone="danger">{integrationErrorText(t, config.error)}</Callout>
      )}
      {config.data && (
        <RepositorySelection
          key={config.data.version}
          orgId={orgId}
          connection={connection}
          config={config.data}
        />
      )}
      <Button variant="outline" onClick={() => setShowHook((v) => !v)}>
        {t("integrations.cnb.bridge")}
      </Button>
      {showHook && (
        <div className="grid gap-3">
          <p className="text-body-sm text-fg-muted">
            {t("integrations.cnb.bridgeGuide")}
          </p>
          {webhook.isError && (
            <Callout tone="danger">
              {integrationErrorText(t, webhook.error)}
            </Callout>
          )}
          {rotate.isError && (
            <Callout tone="danger">
              {integrationErrorText(t, rotate.error)}
            </Callout>
          )}
          {webhook.data && (
            <>
              {webhook.data.payload_url && (
                <>
                  <Field label={t("integrations.cnb.url")}>
                    <TextInput
                      readOnly
                      aria-label={t("integrations.cnb.url")}
                      value={webhook.data.payload_url}
                    />
                  </Field>
                  <Field label={t("integrations.cnb.secret")}>
                    <TextInput
                      type="password"
                      readOnly
                      aria-label={t("integrations.cnb.secret")}
                      value={webhook.data.secret}
                    />
                  </Field>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      variant="outline"
                      onClick={() =>
                        copy(
                          webhook.data!.secret,
                          t("integrations.cnb.copySecret"),
                        )
                      }
                    >
                      {t("integrations.cnb.copySecret")}
                    </Button>
                    <Button
                      variant="outline"
                      onClick={() =>
                        copy(
                          webhook.data!.bridge_template ?? "",
                          t("integrations.cnb.copyBridge"),
                        )
                      }
                    >
                      {t("integrations.cnb.copyBridge")}
                    </Button>
                  </div>
                  <pre
                    className="max-h-72 overflow-auto rounded-lg bg-surface-muted p-3 text-caption"
                    aria-label={t("integrations.cnb.example")}
                  >
                    {webhook.data.bridge_template}
                  </pre>
                </>
              )}
              <Button
                disabled={connection.status !== "ACTIVE"}
                onClick={() => setConfirm(true)}
              >
                {t(
                  webhook.data.payload_url
                    ? "integrations.cnb.rotate"
                    : "integrations.cnb.initialize",
                )}
              </Button>
              <h3 className="text-body font-medium">
                {t("integrations.cnb.deliveries")}
              </h3>
              {webhook.data.recent.length === 0 ? (
                <p>{t("integrations.cnb.noDeliveries")}</p>
              ) : (
                <ul className="grid gap-2 text-body-sm">
                  {webhook.data.recent.map((d, i) => (
                    <li key={i}>
                      {d.event} · {d.status} ·{" "}
                      {new Date(d.received_at).toLocaleString()}
                      <p>{d.detail}</p>
                    </li>
                  ))}
                </ul>
              )}
            </>
          )}
        </div>
      )}
      {config.data && (
        <div className="grid gap-2">
          <h3 className="text-body font-medium">
            {t("integrations.cnb.work")}
          </h3>
          <p className="text-body-sm text-fg-muted">
            {t("integrations.cnb.polling")}
          </p>
          {config.data.work.length === 0 && (
            <p>{t("integrations.cnb.noWork")}</p>
          )}
          {config.data.work.map((work) => (
            <div
              key={`${work.repository_id}:${work.number}`}
              className="grid gap-1 rounded-lg border border-border-default p-3 text-body-sm"
            >
              <a
                className="text-accent"
                href={`https://cnb.cool/${work.repository_path.split("/").map(encodeURIComponent).join("/")}/-/pulls/${work.number}`}
                target="_blank"
                rel="noreferrer"
              >
                {work.repository_path} #{work.number}
              </a>
              <span>
                {t("integrations.cnb.backlink")}{" "}
                {t(`integrations.cnb.state.${work.backlink_state}`)}
              </span>
              <span>
                {t("integrations.cnb.lastCheck")}{" "}
                {work.last_checked_at
                  ? new Date(work.last_checked_at).toLocaleString()
                  : "—"}
              </span>
              <span>
                {t("integrations.cnb.nextCheck")}{" "}
                {work.next_check_at
                  ? new Date(work.next_check_at).toLocaleString()
                  : t(
                      work.terminal
                        ? "integrations.cnb.terminal"
                        : "integrations.cnb.paused",
                    )}
              </span>
              {work.error_code && (
                <Callout tone="warning">
                  {work.error_code === "comment_result_unknown"
                    ? t("integrations.cnb.unknown")
                    : t("integrations.cnb.checkFailed", {
                        code: work.error_code,
                      })}
                </Callout>
              )}
              <Button
                variant="outline"
                disabled={
                  connection.status !== "ACTIVE" ||
                  reconcile.isPending ||
                  !config.data.repositories.some(
                    (r) => r.id === work.repository_id,
                  )
                }
                onClick={() => reconcile.mutate(work)}
              >
                {t("integrations.cnb.reconcile")}
              </Button>
            </div>
          ))}
          {config.data.has_more && <p>{t("integrations.cnb.more")}</p>}
          {reconcile.isError && (
            <Callout tone="danger">
              {integrationErrorText(t, reconcile.error)}
            </Callout>
          )}
        </div>
      )}
      <ConfirmDialog
        open={confirm}
        danger
        title={t("integrations.cnb.rotate")}
        description={t("integrations.cnb.rotateWarning")}
        confirmLabel={t("common.confirm")}
        loading={rotate.isPending}
        onCancel={() => setConfirm(false)}
        onConfirm={() =>
          webhook.data &&
          rotate.mutate(webhook.data.version, {
            onSuccess: () => setConfirm(false),
          })
        }
      />
    </SettingsSection>
  );
}

function RepositorySelection({
  orgId,
  connection,
  config,
}: {
  orgId: string;
  connection: IntegrationConnection;
  config: CNBConfiguration;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    toast = useToast();
  const [selected, setSelected] = useState<CNBRepository[]>(
      config.repositories,
    ),
    [search, setSearch] = useState(""),
    [query, setQuery] = useState(""),
    [page, setPage] = useState(1),
    [browse, setBrowse] = useState(false);
  const repos = useQuery({
    queryKey: [
      ...keys.integrationRepositories(orgId, connection.id),
      query,
      page,
    ],
    queryFn: () =>
      api.integrations.repositories(orgId, connection.id, { q: query, page }),
    enabled: browse && connection.status === "ACTIVE",
    retry: false,
  });
  const save = useMutation({
    mutationFn: () =>
      api.integrations.putCNBRepositories(
        orgId,
        connection.id,
        config.version,
        selected,
      ),
    onError: (error) =>
      toast(integrationErrorText(t, error), { tone: "error" }),
    onSettled: () =>
      cache.invalidateQueries({ queryKey: keys.integrations(orgId) }),
  });
  const changed =
    JSON.stringify(selected) !== JSON.stringify(config.repositories);
  return (
    <div className="grid gap-3">
      <p>{t("integrations.cnb.selected", { count: selected.length })}</p>
      <p className="text-body-sm text-fg-muted">
        {t("integrations.cnb.ownership")}
      </p>
      <fieldset
        disabled={connection.status !== "ACTIVE" || save.isPending}
        className="grid gap-2"
      >
        {selected.map((r) => (
          <label key={r.id} className="flex items-center gap-2">
            <Checkbox
              checked
              aria-label={r.path}
              onChange={() =>
                setSelected(selected.filter((s) => s.id !== r.id))
              }
            />
            {r.path}
          </label>
        ))}
        <div className="flex gap-2">
          <TextInput
            aria-label={t("integrations.cnb.search")}
            placeholder={t("integrations.cnb.search")}
            value={search}
            maxLength={100}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Button
            variant="outline"
            onClick={() => {
              setQuery(search);
              setPage(1);
              setBrowse(true);
            }}
          >
            {t("integrations.cnb.browse")}
          </Button>
        </div>
        {repos.isError && (
          <Callout tone="danger">
            {integrationErrorText(t, repos.error)}
          </Callout>
        )}
        {repos.data?.repositories
          .filter((r) => !selected.some((s) => s.id === r.external_id))
          .map((r) => (
            <label key={r.external_id} className="flex items-center gap-2">
              <Checkbox
                checked={false}
                disabled={!r.external_id || selected.length >= 20}
                aria-label={r.full_name}
                onChange={() =>
                  r.external_id &&
                  setSelected([
                    ...selected,
                    { id: r.external_id, path: r.full_name },
                  ])
                }
              />
              {r.full_name}
            </label>
          ))}
        {browse && (
          <div className="flex gap-2">
            <Button
              variant="outline"
              disabled={page === 1 || repos.isFetching}
              onClick={() => setPage(page - 1)}
            >
              {t("integrations.cnb.previous")}
            </Button>
            <Button
              variant="outline"
              disabled={!repos.data?.has_more || page >= 20 || repos.isFetching}
              onClick={() => setPage(page + 1)}
            >
              {t("integrations.cnb.next")}
            </Button>
          </div>
        )}
        <Button
          disabled={!changed || save.isPending}
          loading={save.isPending}
          onClick={() => save.mutate()}
        >
          {t("integrations.cnb.save")}
        </Button>
      </fieldset>
      {save.isError && (
        <Callout tone="danger">{integrationErrorText(t, save.error)}</Callout>
      )}
    </div>
  );
}
