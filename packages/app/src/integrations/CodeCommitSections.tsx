import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  CodeCommitConfiguration,
  CodeCommitJob,
  CodeCommitRepository,
  IntegrationConnection,
  IntegrationDetail,
  IntegrationSettingsValues,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout, Switch } from "@openfox/ui";
import { keys, useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import {
  CodeCommitCredentialsDialog,
  CodeCommitReconcileDialog,
  CodeCommitRepositoryDialog,
} from "./CodeCommitDialogs";
import {
  codecommitError,
  codecommitObjectURL,
  codecommitReason,
} from "./codecommit-model";
import { DisconnectDialog } from "./DisconnectDialog";
import { useIntegrationSettings, usePutIntegrationSettings } from "./queries";

export function CodeCommitSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  return (
    <>
      <Callout tone="info">{t("integrations.codecommit.scope")}</Callout>
      {!detail.can_manage ? (
        <Callout tone="info">
          {t("integrations.readOnly", { name: detail.provider.name })}
        </Callout>
      ) : (
        <CodeCommitManager key={orgId} orgId={orgId} detail={detail} />
      )}
    </>
  );
}
function CodeCommitManager({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT(),
    cache = useQueryClient(),
    [adding, setAdding] = useState(false);
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
  };
  return (
    <>
      {!detail.provider.configured && (
        <Callout tone="warning">
          {t("integrations.codecommit.notConfigured")}
        </Callout>
      )}
      <Button
        variant="outline"
        disabled={!detail.provider.configured || !detail.provider.bindable}
        onClick={() => setAdding(true)}
      >
        {t("integrations.codecommit.bind")}
      </Button>
      {adding && (
        <CodeCommitCredentialsDialog
          orgId={orgId}
          refresh={refresh}
          onClose={() => setAdding(false)}
        />
      )}
      <CodeCommitSettings orgId={orgId} />
      {detail.connections.map((connection) => (
        <CodeCommitConnection
          key={`${connection.id}:${connection.version}`}
          orgId={orgId}
          detail={detail}
          connection={connection}
          refresh={refresh}
        />
      ))}
      <SettingsSection
        title={t("integrations.codecommit.bridgeTitle")}
        description={t("integrations.codecommit.bridgeHelp")}
      />
    </>
  );
}
function CodeCommitSettings({ orgId }: { orgId: string }) {
  const t = useT(),
    settings = useIntegrationSettings(orgId, "codecommit"),
    put = usePutIntegrationSettings(orgId, "codecommit");
  const fields: Array<{
    key: keyof IntegrationSettingsValues;
    label: "pr" | "commits" | "backlink";
  }> = [
    { key: "link_pull_requests", label: "pr" },
    { key: "link_commits", label: "commits" },
    { key: "pr_backlink_private", label: "backlink" },
  ];
  return (
    <SettingsSection
      title={t("integrations.codecommit.settings")}
      description={t("integrations.codecommit.settingsHelp")}
    >
      {settings.isError && (
        <Callout tone="danger">
          {t("integrations.codecommit.loadFailed")}
        </Callout>
      )}
      {put.isError && (
        <Callout tone="danger">{codecommitError(t, put.error)}</Callout>
      )}
      {settings.data && (
        <SettingsCard>
          {fields.map(({ key, label }) => (
            <SettingRow
              key={key}
              title={t(`integrations.codecommit.${label}`)}
              control={
                <Switch
                  label={t(`integrations.codecommit.${label}`)}
                  checked={Boolean(settings.data!.settings[key])}
                  disabled={
                    !settings.data!.can_manage ||
                    settings.isFetching ||
                    put.isPending
                  }
                  onCheckedChange={(checked) =>
                    put.mutate({
                      version: settings.data!.version,
                      settings: { ...settings.data!.settings, [key]: checked },
                    })
                  }
                />
              }
            />
          ))}
        </SettingsCard>
      )}
    </SettingsSection>
  );
}
type DialogState =
  | { kind: "credentials"; config: CodeCommitConfiguration }
  | {
      kind: "repository";
      config: CodeCommitConfiguration;
      repository?: CodeCommitRepository;
      action: "enable" | "disable" | "rotate" | "reconcile";
    }
  | { kind: "job"; config: CodeCommitConfiguration; job: CodeCommitJob }
  | { kind: "disconnect"; connection: IntegrationConnection };
function CodeCommitConnection({
  orgId,
  detail,
  connection,
  refresh,
}: {
  orgId: string;
  detail: IntegrationDetail;
  connection: IntegrationConnection;
  refresh: () => void;
}) {
  const api = useApi(),
    t = useT(),
    [after, setAfter] = useState(""),
    [before, setBefore] = useState(""),
    [dialog, setDialog] = useState<DialogState | null>(null);
  const config = useQuery({
    queryKey: [
      ...keys.integrations(orgId),
      "codecommit",
      connection.id,
      connection.version,
      "configuration",
      after,
    ],
    queryFn: ({ signal }) =>
      api.codecommit.configuration(orgId, connection.id, after, signal),
    retry: false,
  });
  const jobs = useQuery({
    queryKey: [
      ...keys.integrations(orgId),
      "codecommit",
      connection.id,
      connection.version,
      "work",
      before,
    ],
    queryFn: ({ signal }) =>
      api.codecommit.jobs(orgId, connection.id, before, signal),
    retry: false,
  });
  const current = config.data?.connection ?? connection;
  const disabled = !config.data || config.isFetching || config.isError;
  const active = current.status === "ACTIVE";
  function repositoryAction(
    action: "enable" | "disable" | "rotate" | "reconcile",
    repository?: CodeCommitRepository,
  ) {
    if (config.data)
      setDialog({
        kind: "repository",
        config: config.data,
        repository,
        action,
      });
  }
  return (
    <SettingsSection
      title={connection.display_name || connection.external_id}
      action={
        <Button
          variant="ghost"
          disabled={config.isFetching || jobs.isFetching}
          onClick={refresh}
        >
          {t("integrations.codecommit.refresh")}
        </Button>
      }
    >
      {config.isError && (
        <Callout tone="danger">{codecommitError(t, config.error)}</Callout>
      )}
      <SettingsCard>
        <div className="flex flex-col gap-3 p-4">
          <Badge tone={active ? "success" : "warning"}>
            {t(
              active
                ? "integrations.codecommit.active"
                : current.status === "DISCONNECTED"
                  ? "integrations.codecommit.disconnected"
                  : "integrations.codecommit.needsAttention",
            )}
          </Badge>
          {config.data && (
            <>
              <p className="text-body-sm">
                {t("integrations.codecommit.account")}: {config.data.account} ·{" "}
                {config.data.region}
              </p>
              <p className="break-all text-body-sm">
                {t("integrations.codecommit.principal")}:{" "}
                {config.data.principal_arn}
              </p>
            </>
          )}
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={disabled || !detail.provider.configured}
              onClick={() =>
                setDialog({ kind: "credentials", config: config.data! })
              }
            >
              {t(
                current.status === "DISCONNECTED"
                  ? "integrations.codecommit.reconnect"
                  : "integrations.codecommit.credentials",
              )}
            </Button>
            {current.status !== "DISCONNECTED" && (
              <Button
                variant="outline"
                disabled={disabled}
                onClick={() =>
                  setDialog({ kind: "disconnect", connection: current })
                }
              >
                {t("integrations.disconnect")}
              </Button>
            )}
          </div>
        </div>
      </SettingsCard>
      <SettingsSection
        title={t("integrations.codecommit.repositories")}
        description={t("integrations.codecommit.repositoryHelp")}
        action={
          <Button
            variant="outline"
            disabled={disabled || !active}
            onClick={() => repositoryAction("enable")}
          >
            {t("integrations.codecommit.addRepository")}
          </Button>
        }
      >
        {config.data?.repositories.length === 0 && (
          <p className="text-body-sm text-fg-muted">
            {t("integrations.codecommit.noRepositories")}
          </p>
        )}
        {config.data?.repositories.map((repository) => (
          <SettingsCard key={repository.id}>
            <div className="flex flex-col gap-2 p-4">
              <h3 className="text-body font-medium">{repository.name}</h3>
              <p className="break-all text-caption text-fg-muted">
                {repository.arn}
              </p>
              <p className="text-body-sm">
                {t(
                  !repository.enabled
                    ? "integrations.codecommit.disabled"
                    : repository.initialized_at
                      ? "integrations.codecommit.initialized"
                      : "integrations.codecommit.initializing",
                )}
              </p>
              <p className="text-body-sm">
                {t(
                  repository.bridge_ready
                    ? "integrations.codecommit.bridgeReady"
                    : "integrations.codecommit.bridgeMissing",
                )}
              </p>
              <p className="text-body-sm">
                {repository.last_event_at ? (
                  <>
                    {t("integrations.codecommit.received")}:{" "}
                    <time dateTime={repository.last_event_at}>
                      {new Date(repository.last_event_at).toLocaleString()}
                    </time>
                  </>
                ) : (
                  t("integrations.codecommit.neverReceived")
                )}
              </p>
              {repository.last_error && (
                <Callout tone="warning">
                  {codecommitReason(t, repository.last_error)}
                </Callout>
              )}
              <div className="flex flex-wrap gap-2">
                {repository.enabled ? (
                  <>
                    {active && repository.last_error && (
                      <Button
                        variant="outline"
                        disabled={disabled}
                        onClick={() =>
                          repositoryAction("reconcile", repository)
                        }
                      >
                        {t("integrations.codecommit.reconcile")}
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      disabled={disabled}
                      onClick={() => repositoryAction("disable", repository)}
                    >
                      {t("integrations.codecommit.disable")}
                    </Button>
                    <Button
                      variant="outline"
                      disabled={disabled || !active}
                      onClick={() => repositoryAction("rotate", repository)}
                    >
                      {t("integrations.codecommit.rotate")}
                    </Button>
                  </>
                ) : (
                  <Button
                    variant="outline"
                    disabled={disabled || !active}
                    onClick={() => repositoryAction("enable", repository)}
                  >
                    {t("integrations.codecommit.reenable")}
                  </Button>
                )}
              </div>
            </div>
          </SettingsCard>
        ))}
        <div className="flex gap-2">
          {after && (
            <Button
              variant="outline"
              disabled={config.isFetching}
              onClick={() => setAfter("")}
            >
              {t("integrations.codecommit.first")}
            </Button>
          )}
          {config.data?.repository_next && (
            <Button
              variant="outline"
              disabled={config.isFetching}
              onClick={() => setAfter(config.data!.repository_next)}
            >
              {t("integrations.codecommit.next")}
            </Button>
          )}
        </div>
      </SettingsSection>
      <SettingsSection
        title={t("integrations.codecommit.work")}
        description={t("integrations.codecommit.workHelp")}
      >
        {jobs.isError && (
          <Callout tone="danger">{codecommitError(t, jobs.error)}</Callout>
        )}
        {jobs.data?.jobs.length === 0 && (
          <p>{t("integrations.codecommit.noWork")}</p>
        )}
        {jobs.data?.jobs.map((job) => (
          <SettingsCard key={job.id}>
            <div className="flex flex-col gap-2 p-4">
              <h3 className="text-body font-medium">
                {job.repository_name} · {job.kind}
              </h3>
              <p>
                {t(`integrations.codecommit.states.${job.state}`)} ·{" "}
                {t(`integrations.codecommit.backlinks.${job.backlink_state}`)}
              </p>
              <p className="text-caption text-fg-muted">
                {t("integrations.codecommit.requestedAt")}:{" "}
                <time dateTime={job.requested_at}>
                  {new Date(job.requested_at).toLocaleString()}
                </time>
                {job.next_attempt_at && (
                  <>
                    {" "}
                    · {t("integrations.codecommit.nextAttempt")}:{" "}
                    <time dateTime={job.next_attempt_at}>
                      {new Date(job.next_attempt_at).toLocaleString()}
                    </time>
                  </>
                )}
              </p>
              {job.last_error && (
                <p className="text-body-sm text-fg-muted">
                  {codecommitReason(t, job.last_error)}
                </p>
              )}
              {codecommitObjectURL(job.url) && (
                <a
                  href={codecommitObjectURL(job.url)}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-body-sm text-accent hover:underline"
                >
                  {t("integrations.codecommit.jobLink")}
                </a>
              )}
              {job.can_reconcile && (
                <Button
                  variant="outline"
                  disabled={disabled || !active || jobs.isFetching}
                  onClick={() =>
                    setDialog({ kind: "job", config: config.data!, job })
                  }
                >
                  {t(
                    job.backlink_state === "UNKNOWN"
                      ? "integrations.codecommit.unknown"
                      : "integrations.codecommit.retry",
                  )}
                </Button>
              )}
            </div>
          </SettingsCard>
        ))}
        <div className="flex gap-2">
          {before && (
            <Button
              variant="outline"
              disabled={jobs.isFetching}
              onClick={() => setBefore("")}
            >
              {t("integrations.codecommit.first")}
            </Button>
          )}
          {jobs.data?.next && (
            <Button
              variant="outline"
              disabled={jobs.isFetching}
              onClick={() => setBefore(jobs.data!.next)}
            >
              {t("integrations.codecommit.next")}
            </Button>
          )}
        </div>
      </SettingsSection>
      {dialog?.kind === "credentials" && (
        <CodeCommitCredentialsDialog
          orgId={orgId}
          config={dialog.config}
          refresh={refresh}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "repository" && (
        <CodeCommitRepositoryDialog
          orgId={orgId}
          config={dialog.config}
          repository={dialog.repository}
          action={dialog.action}
          refresh={refresh}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "job" && (
        <CodeCommitReconcileDialog
          orgId={orgId}
          config={dialog.config}
          job={dialog.job}
          refresh={refresh}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "disconnect" && (
        <DisconnectDialog
          orgId={orgId}
          provider={detail.provider}
          connection={dialog.connection}
          body={t("integrations.codecommit.disconnectHelp")}
          onClose={() => setDialog(null)}
        />
      )}
    </SettingsSection>
  );
}
