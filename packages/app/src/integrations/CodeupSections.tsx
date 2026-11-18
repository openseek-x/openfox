import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  CodeupConfiguration,
  CodeupRepository,
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

export function CodeupSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  const settings = useIntegrationSettings(orgId, "codeup"),
    put = usePutIntegrationSettings(orgId, "codeup");
  const fields = [
    "link_pull_requests",
    "link_commits",
    "pr_backlink_private",
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
        title={t("integrations.codeup.title")}
        description={t("integrations.codeup.scope")}
      >
        <Callout tone="info">{t("integrations.codeup.cost")}</Callout>
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
                title={t(`integrations.codeup.${field}`)}
                control={
                  <Switch
                    label={t(`integrations.codeup.${field}`)}
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
          .map((c) => (
            <CodeupConnection key={c.id} orgId={orgId} connection={c} />
          ))
      ) : (
        <Callout tone="info">
          {t("integrations.readOnly", { name: "Codeup" })}
        </Callout>
      )}
    </>
  );
}

function CodeupConnection({
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
    [confirm, setConfirm] = useState(false),
    [cursors, setCursors] = useState<string[]>([]);
  const configKey = [...keys.integrations(orgId), "codeup", connection.id];
  const config = useQuery({
    queryKey: configKey,
    queryFn: () => api.integrations.codeupConfiguration(orgId, connection.id),
    retry: false,
  });
  const cursor = cursors.at(-1) ?? "";
  const page = useQuery({
    queryKey: [...configKey, "work", cursor, config.dataUpdatedAt],
    queryFn: () => api.integrations.codeupWork(orgId, connection.id, cursor),
    enabled: !!cursor,
    retry: false,
  });
  const workPage = cursor ? page.data : config.data;
  const webhook = useIntegrationWebhook(orgId, connection.id, showHook),
    rotate = useRotateWebhook(orgId, connection.id);
  const reconcile = useMutation({
    mutationFn: (work: CodeupConfiguration["work"][number]) =>
      api.integrations.reconcileCodeup(
        orgId,
        connection.id,
        work.repository_id,
        work.number,
        config.data!.version,
      ),
    onSuccess: () => cache.invalidateQueries({ queryKey: configKey }),
  });
  const refresh = () => {
    setCursors([]);
    void config.refetch();
    if (showHook) void webhook.refetch();
  };
  return (
    <SettingsSection
      title={t("integrations.codeup.connection", {
        name: connection.display_name,
      })}
      action={
        <Button variant="outline" onClick={refresh}>
          {t("integrations.codeup.refresh")}
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
        <CodeupCredentials
          orgId={orgId}
          connection={connection}
          version={config.data.version}
        />
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
        {t("integrations.codeup.bridge")}
      </Button>
      {showHook && (
        <div className="grid gap-3">
          <p className="text-body-sm text-fg-muted">
            {t("integrations.codeup.bridgeGuide")}
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
                  <Field label={t("integrations.codeup.url")}>
                    <TextInput
                      readOnly
                      aria-label={t("integrations.codeup.url")}
                      value={webhook.data.payload_url}
                    />
                  </Field>
                  <Field label={t("integrations.codeup.secret")}>
                    <TextInput
                      type="password"
                      readOnly
                      aria-label={t("integrations.codeup.secret")}
                      value={webhook.data.secret}
                    />
                  </Field>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      variant="outline"
                      onClick={() =>
                        copy(
                          webhook.data!.secret,
                          t("integrations.codeup.copySecret"),
                        )
                      }
                    >
                      {t("integrations.codeup.copySecret")}
                    </Button>
                  </div>
                </>
              )}
              <Button
                disabled={connection.status !== "ACTIVE"}
                onClick={() => setConfirm(true)}
              >
                {t(
                  webhook.data.payload_url
                    ? "integrations.codeup.rotate"
                    : "integrations.codeup.initialize",
                )}
              </Button>
              <h3 className="text-body font-medium">
                {t("integrations.codeup.deliveries")}
              </h3>
              {webhook.data.recent.length === 0 ? (
                <p>{t("integrations.codeup.noDeliveries")}</p>
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
            {t("integrations.codeup.work")}
          </h3>
          <p className="text-body-sm text-fg-muted">
            {t("integrations.codeup.polling")}
          </p>
          {workPage?.work.length === 0 && (
            <p>{t("integrations.codeup.noWork")}</p>
          )}
          {workPage?.work.map((work) => (
            <div
              key={`${work.repository_id}:${work.number}`}
              className="grid gap-1 rounded-lg border border-border-default p-3 text-body-sm"
            >
              <a
                className="text-accent"
                href={`https://codeup.aliyun.com/${work.repository_path.split("/").map(encodeURIComponent).join("/")}/change/${work.number}`}
                target="_blank"
                rel="noreferrer"
              >
                {work.repository_path} #{work.number}
              </a>
              <span>
                {t("integrations.codeup.backlink")}{" "}
                {t(`integrations.codeup.state.${work.backlink_state}`)}
              </span>
              <span>
                {t("integrations.codeup.lastCheck")}{" "}
                {work.last_checked_at
                  ? new Date(work.last_checked_at).toLocaleString()
                  : "—"}
              </span>
              <span>
                {t("integrations.codeup.nextCheck")}{" "}
                {work.next_check_at
                  ? new Date(work.next_check_at).toLocaleString()
                  : t(
                      work.terminal
                        ? "integrations.codeup.terminal"
                        : "integrations.codeup.paused",
                    )}
              </span>
              {work.error_code && (
                <Callout tone="warning">
                  {work.error_code === "comment_result_unknown"
                    ? t("integrations.codeup.unknown")
                    : t("integrations.codeup.checkFailed", {
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
                {t("integrations.codeup.reconcile")}
              </Button>
            </div>
          ))}
          {page.isError && (
            <Callout tone="danger">
              {integrationErrorText(t, page.error)}
            </Callout>
          )}
          <div className="flex gap-2">
            {!!cursor && (
              <Button
                variant="outline"
                onClick={() => setCursors((v) => v.slice(0, -1))}
              >
                {t("integrations.codeup.previous")}
              </Button>
            )}
            {workPage?.has_more && (
              <Button
                variant="outline"
                disabled={page.isFetching}
                onClick={() => setCursors((v) => [...v, workPage.next_cursor])}
              >
                {t("integrations.codeup.more")}
              </Button>
            )}
          </div>
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
        title={t("integrations.codeup.rotate")}
        description={t("integrations.codeup.rotateWarning")}
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
  config: CodeupConfiguration;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient(),
    toast = useToast();
  const [selected, setSelected] = useState<CodeupRepository[]>(
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
      api.integrations.codeupRepositories(orgId, connection.id, {
        q: query,
        page,
      }),
    enabled: browse && connection.status === "ACTIVE",
    retry: false,
  });
  const save = useMutation({
    mutationFn: () =>
      api.integrations.putCodeupRepositories(
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
      <p>{t("integrations.codeup.selected", { count: selected.length })}</p>
      <p className="text-body-sm text-fg-muted">
        {t("integrations.codeup.ownership")}
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
            aria-label={t("integrations.codeup.search")}
            placeholder={t("integrations.codeup.search")}
            value={search}
            maxLength={200}
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
            {t("integrations.codeup.browse")}
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
              {t("integrations.codeup.previous")}
            </Button>
            <Button
              variant="outline"
              disabled={
                !repos.data?.has_more || page >= 150 || repos.isFetching
              }
              onClick={() => setPage(page + 1)}
            >
              {t("integrations.codeup.next")}
            </Button>
          </div>
        )}
        <Button
          disabled={!changed || save.isPending}
          loading={save.isPending}
          onClick={() => save.mutate()}
        >
          {t("integrations.codeup.save")}
        </Button>
      </fieldset>
      {save.isError && (
        <Callout tone="danger">{integrationErrorText(t, save.error)}</Callout>
      )}
    </div>
  );
}

function CodeupCredentials({
  orgId,
  connection,
  version,
}: {
  orgId: string;
  connection: IntegrationConnection;
  version: number;
}) {
  const t = useT(),
    api = useApi(),
    cache = useQueryClient();
  const [open, setOpen] = useState(false),
    [company, setCompany] = useState(""),
    [secret, setSecret] = useState("");
  const save = useMutation({
    mutationFn: () =>
      api.integrations.updateCodeupCredentials(orgId, connection.id, {
        version,
        method: "token",
        company_id: company,
        secret,
      }),
    onSuccess: () => {
      setSecret("");
      setOpen(false);
      void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
    },
  });
  return (
    <div className="grid gap-3">
      <Button
        variant="outline"
        onClick={() => {
          setOpen(!open);
          setSecret("");
          save.reset();
        }}
      >
        {t("integrations.codeup.credentials")}
      </Button>
      {open && (
        <>
          <p>{t("integrations.codeup.sameIdentity")}</p>
          <Field label={t("integrations.codeup.organization")}>
            <TextInput
              aria-label={t("integrations.codeup.organization")}
              value={company}
              maxLength={100}
              onChange={(e) => setCompany(e.target.value)}
            />
          </Field>
          <Field label="PAT">
            <TextInput
              type="password"
              aria-label="PAT"
              value={secret}
              maxLength={4096}
              onChange={(e) => setSecret(e.target.value)}
            />
          </Field>
          {save.isError && (
            <Callout tone="danger">
              {integrationErrorText(t, save.error)}
            </Callout>
          )}
          <Button
            disabled={save.isPending || !company || !secret}
            onClick={() => save.mutate()}
          >
            {t("integrations.codeup.saveCredentials")}
          </Button>
        </>
      )}
    </div>
  );
}
