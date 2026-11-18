import { useState } from "react";
import type { SlackApp, SlackConfiguration, SlackIdentity } from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout, Dialog } from "@openfox/ui";
import { useApi } from "../context";
import {
  SettingRow,
  SettingsCard,
  SettingsSection,
} from "../spaces/settings/parts";
import {
  useSlackConfiguration,
  useSlackRefresh,
  useSlackWrite,
} from "./slack-hooks";
import {
  slackError,
  slackReason,
  slackSettingsURL,
  slackState,
  slackUnknown,
} from "./slack-model";
import { SlackOAuthButton } from "./SlackOAuth";
import {
  SlackCreateDialog,
  SlackCredentialsDialog,
  SlackStopDialog,
} from "./SlackAppDialogs";
import { SlackChannelsDialog, SlackDeliveriesDialog } from "./SlackBotDialogs";

export function SlackSections({
  orgId,
  personalOnly = false,
}: {
  orgId: string;
  personalOnly?: boolean;
}) {
  const t = useT(),
    query = useSlackConfiguration(orgId),
    refresh = useSlackRefresh(orgId);
  if (query.isPending) return <p>{t("common.loading")}</p>;
  if (query.isError)
    return (
      <Callout tone="danger">
        {t("integrations.slack.loadFailed")}{" "}
        <Button variant="outline" onClick={() => void query.refetch()}>
          {t("integrations.slack.reload")}
        </Button>
      </Callout>
    );
  return (
    <SlackContent
      key={orgId}
      orgId={orgId}
      config={query.data}
      refresh={refresh}
      personalOnly={personalOnly}
    />
  );
}
function SlackContent({
  orgId,
  config,
  refresh,
  personalOnly,
}: {
  orgId: string;
  config: SlackConfiguration;
  refresh: () => void;
  personalOnly: boolean;
}) {
  const t = useT(),
    [creating, setCreating] = useState<"agent" | "task_assistant">();
  const central = config.apps.filter((a) => a.kind === "CENTRAL"),
    canCreate =
      config.configured &&
      central.some(
        (a) =>
          a.state === "ACTIVE" &&
          config.identities.some((i) => i.parent_app_id === a.id && i.active),
      );
  return (
    <>
      <Callout tone="info">{t("integrations.slack.scope")}</Callout>
      {!config.configured && (
        <Callout tone="warning">
          {t("integrations.slack.notConfigured")}
        </Callout>
      )}
      {!personalOnly && (
        <SettingsSection
          title={t("integrations.slack.organization")}
          description={t("integrations.slack.organizationHelp")}
        >
          {config.can_manage && (
            <SlackOAuthButton
              orgId={orgId}
              purpose="organization"
              refresh={refresh}
              disabled={!config.configured}
              label={t("integrations.slack.installOrganization")}
            />
          )}
          {central.map((a) => (
            <SlackAppCard
              key={`${a.id}:${a.version}`}
              orgId={orgId}
              app={a}
              configured={config.configured}
              refresh={refresh}
            />
          ))}
          <Callout tone="info">
            {t("integrations.slack.centralReplies")}
          </Callout>
        </SettingsSection>
      )}
      <SlackPersonal orgId={orgId} config={config} refresh={refresh} />
      {!personalOnly && (
        <>
          <Callout tone="info">{t("integrations.slack.targetLimit")}</Callout>
          {!canCreate && (
            <p className="text-body-sm text-fg-muted">
              {t("integrations.slack.identityMissing")}
            </p>
          )}
          {(["agent", "task_assistant"] as const).map((kind) => (
            <SettingsSection
              key={kind}
              title={t(
                kind === "agent"
                  ? "integrations.slack.agents"
                  : "integrations.slack.assistants",
              )}
            >
              <Button
                variant="outline"
                disabled={!canCreate}
                onClick={() => setCreating(kind)}
              >
                {t(
                  kind === "agent"
                    ? "integrations.slack.createAgent"
                    : "integrations.slack.createAssistant",
                )}
              </Button>
              {config.apps
                .filter(
                  (a) =>
                    a.kind === (kind === "agent" ? "AGENT" : "TASK_ASSISTANT"),
                )
                .map((a) => (
                  <SlackAppCard
                    key={`${a.id}:${a.version}`}
                    orgId={orgId}
                    app={a}
                    configured={config.configured}
                    refresh={refresh}
                  />
                ))}
            </SettingsSection>
          ))}
          {creating && canCreate && (
            <SlackCreateDialog
              orgId={orgId}
              config={config}
              purpose={creating}
              onClose={() => setCreating(undefined)}
              refresh={refresh}
            />
          )}
        </>
      )}
      <Button variant="outline" onClick={refresh}>
        {t("integrations.slack.reload")}
      </Button>
    </>
  );
}
export function SlackPersonal({
  orgId,
  config,
  refresh,
}: {
  orgId: string;
  config: SlackConfiguration;
  refresh: () => void;
}) {
  const t = useT(),
    [removing, setRemoving] = useState<SlackIdentity>();
  const central = config.apps.filter((a) => a.kind === "CENTRAL");
  return (
    <SettingsSection
      title={t("integrations.slack.personal")}
      description={t("integrations.slack.personalHelp")}
    >
      <SettingsCard>
        {central.map((app) => {
          const identity = config.identities.find(
            (i) => i.parent_app_id === app.id && i.active,
          );
          return (
            <SettingRow
              key={app.id}
              title={`${app.name || app.team_id} · ${app.team_id}`}
              description={
                identity
                  ? `${t("integrations.slack.identityActive")} · ${identity.slack_user_id}`
                  : t("integrations.slack.identityInactive")
              }
              control={
                identity ? (
                  <Button
                    variant="outline"
                    onClick={() => setRemoving(identity)}
                  >
                    {t("integrations.slack.unbindIdentity")}
                  </Button>
                ) : (
                  <SlackOAuthButton
                    key={`${app.id}:${app.version}`}
                    orgId={orgId}
                    app={app}
                    purpose="personal"
                    refresh={refresh}
                    disabled={!config.configured || app.state !== "ACTIVE"}
                    label={t("integrations.slack.bindIdentity")}
                  />
                )
              }
            />
          );
        })}
      </SettingsCard>
      <p className="text-caption text-fg-muted">
        {t("integrations.slack.notificationHelp")}
      </p>
      {removing &&
        config.identities.some(
          (i) =>
            i.id === removing.id && i.active && i.version === removing.version,
        ) && (
          <SlackUnbindDialog
            orgId={orgId}
            identity={removing}
            refresh={refresh}
            onClose={() => setRemoving(undefined)}
          />
        )}
    </SettingsSection>
  );
}
function SlackUnbindDialog({
  orgId,
  identity,
  refresh,
  onClose,
}: {
  orgId: string;
  identity: SlackIdentity;
  refresh: () => void;
  onClose: () => void;
}) {
  const t = useT(),
    api = useApi(),
    write = useSlackWrite(refresh);
  return (
    <Dialog
      open
      title={t("integrations.slack.unbindIdentity")}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            disabled={write.busy || write.blocked}
            onClick={() =>
              void write.run(
                (signal) =>
                  api.slack.unbind(
                    orgId,
                    identity.id,
                    { version: identity.version },
                    signal,
                  ),
                onClose,
              )
            }
          >
            {t("integrations.slack.unbindIdentity")}
          </Button>
        </>
      }
    >
      <p>
        {identity.team_id} · {identity.slack_user_id}
      </p>
      <p>{t("integrations.slack.personalHelp")}</p>
      {!!write.error && (
        <Callout tone="danger">{slackError(t, write.error)}</Callout>
      )}
    </Dialog>
  );
}
function SlackAppCard({
  orgId,
  app,
  configured,
  refresh,
}: {
  orgId: string;
  app: SlackApp;
  configured: boolean;
  refresh: () => void;
}) {
  const t = useT(),
    [dialog, setDialog] = useState<
      | "recover"
      | "configure"
      | "cancel"
      | "disconnect"
      | "channels"
      | "deliveries"
    >();
  const central = app.kind === "CENTRAL",
    settings = slackSettingsURL(app.external_app_id),
    terminal = ["CANCELLED", "DISCONNECTED"].includes(app.state);
  return (
    <article
      className="my-2 flex flex-col gap-3 rounded-xl border border-line-default bg-surface-card p-4"
      aria-label={app.name || app.team_id || app.id}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-body font-medium">
          {app.name || app.team_id || app.id}
        </h3>
        <Badge
          variant="status"
          tone={
            app.state === "ACTIVE"
              ? "success"
              : slackUnknown.has(app.state) ||
                  app.state === "FAILED" ||
                  app.state === "NEEDS_REAUTH"
                ? "warning"
                : "muted"
          }
        >
          {slackState(t, app.state)}
        </Badge>
      </div>
      <p className="break-all text-caption text-fg-muted">
        {[app.team_id, app.external_app_id, app.agent_id || app.space_id]
          .filter(Boolean)
          .join(" · ")}
      </p>
      {!!app.status_reason && (
        <Callout tone="warning">{slackReason(t, app.status_reason)}</Callout>
      )}
      {app.state === "DISCONNECTED" && (
        <Callout tone="info">
          {t("integrations.slack.localDisconnected")}
        </Callout>
      )}
      {slackUnknown.has(app.state) && (
        <Callout tone="warning">{t("integrations.slack.unknownHelp")}</Callout>
      )}
      {!central && (
        <>
          <p className="text-body-sm">
            {t(
              app.url_verified
                ? "integrations.slack.urlVerified"
                : "integrations.slack.urlPending",
            )}{" "}
            ·{" "}
            {t(
              app.installed
                ? "integrations.slack.installed"
                : "integrations.slack.installPending",
            )}
          </p>
          {app.events_url && (
            <>
              <label className="text-caption text-fg-muted">
                {t("integrations.slack.eventsURL")}
              </label>
              <code className="break-all select-all text-body-sm">
                {app.events_url}
              </code>
              <p className="text-caption text-fg-muted">
                {t("integrations.slack.manualVerification")}
              </p>
            </>
          )}
          {settings && (
            <a
              className="text-body-sm underline"
              href={settings}
              target="_blank"
              rel="noopener noreferrer"
            >
              {t("integrations.slack.appSettings")}
            </a>
          )}
        </>
      )}
      {app.can_manage && (
        <div className="flex flex-wrap gap-2">
          {app.state !== "CANCELLED" && (central || !!app.external_app_id) && (
            <SlackOAuthButton
              orgId={orgId}
              app={app}
              purpose={central ? "organization" : "app"}
              refresh={refresh}
              disabled={
                !configured ||
                [
                  "PROVING",
                  "CREATING",
                  "PROOF_UNKNOWN",
                  "CREATE_UNKNOWN",
                  "ORPHANED_APP",
                ].includes(app.state)
              }
              label={t(
                app.installed
                  ? "integrations.slack.reauthorize"
                  : app.state === "DISCONNECTED"
                    ? "integrations.slack.reinstall"
                    : "integrations.slack.installApp",
              )}
            />
          )}
          {!central &&
            !terminal &&
            !!app.external_app_id &&
            app.state !== "ACTIVE" && (
              <Button variant="outline" onClick={() => setDialog("configure")}>
                {t("integrations.slack.refreshConfiguration")}
              </Button>
            )}
          {!central &&
            [
              "CREATE_UNKNOWN",
              "MANIFEST_UNKNOWN",
              "ORPHANED_APP",
              "FAILED",
            ].includes(app.state) && (
              <Button variant="outline" onClick={() => setDialog("recover")}>
                {t("integrations.slack.recover")}
              </Button>
            )}
          {(!terminal || (!central && app.state === "DISCONNECTED")) && (
            <Button
              variant="outline"
              onClick={() =>
                setDialog(
                  app.state !== "DISCONNECTED" && (app.installed || central)
                    ? "disconnect"
                    : "cancel",
                )
              }
            >
              {t(
                app.state !== "DISCONNECTED" && (app.installed || central)
                  ? "integrations.slack.disconnect"
                  : "integrations.slack.cancelOperation",
              )}
            </Button>
          )}
          {app.bot_id && app.state === "ACTIVE" && (
            <Button variant="outline" onClick={() => setDialog("channels")}>
              {t("integrations.slack.channels")}
            </Button>
          )}
          <Button variant="outline" onClick={() => setDialog("deliveries")}>
            {t("integrations.slack.deliveries")}
          </Button>
        </div>
      )}
      {app.can_manage && (dialog === "configure" || dialog === "recover") && (
        <SlackCredentialsDialog
          orgId={orgId}
          app={app}
          mode={dialog}
          refresh={refresh}
          onClose={() => setDialog(undefined)}
        />
      )}
      {app.can_manage && (dialog === "cancel" || dialog === "disconnect") && (
        <SlackStopDialog
          orgId={orgId}
          app={app}
          action={dialog}
          refresh={refresh}
          onClose={() => setDialog(undefined)}
        />
      )}
      {app.can_manage && dialog === "channels" && (
        <SlackChannelsDialog
          orgId={orgId}
          app={app}
          onClose={() => setDialog(undefined)}
        />
      )}
      {app.can_manage && dialog === "deliveries" && (
        <SlackDeliveriesDialog
          orgId={orgId}
          app={app}
          onClose={() => setDialog(undefined)}
        />
      )}
    </article>
  );
}
