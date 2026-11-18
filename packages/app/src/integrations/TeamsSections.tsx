import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  teamsApi,
  type IntegrationDetail,
  type TeamsBot,
  type TeamsScope,
} from "@openfox/api";
import { useT } from "@openfox/i18n";
import { Badge, Button, Callout, Dialog } from "@openfox/ui";
import { keys, useApi } from "../context";
import { useOrgRole } from "../agents/queries";
import { SettingsCard, SettingsSection } from "../spaces/settings/parts";
import { TeamsConnectDialog } from "./TeamsDialogs";
import { TeamsPersonalSection } from "./TeamsPersonal";
import { teamsError, teamsReason, useTeamsAction } from "./teams-model";
export function TeamsSections({
  orgId,
  detail,
}: {
  orgId: string;
  detail: IntegrationDetail;
}) {
  const t = useT();
  return (
    <>
      <Callout tone="info">{t("integrations.teams.scope")}</Callout>
      {!detail.provider.configured && (
        <Callout tone="warning">
          {t("integrations.teams.configuration")}
        </Callout>
      )}
      <TeamsManager
        key={orgId}
        orgId={orgId}
        configured={detail.provider.configured}
      />
      <TeamsPersonalSection key={`personal:${orgId}`} orgId={orgId} />
    </>
  );
}
function TeamsManager({
  orgId,
  configured,
}: {
  orgId: string;
  configured: boolean;
}) {
  const api = useApi(),
    t = useT(),
    cache = useQueryClient(),
    role = useOrgRole(orgId),
    [adding, setAdding] = useState(false);
  const bots = useQuery({
    queryKey: ["teams", orgId, "bots"],
    queryFn: ({ signal }) => teamsApi(api.http).bots(orgId, signal),
  });
  const refresh = () => {
    void bots.refetch();
    void cache.invalidateQueries({ queryKey: keys.integrations(orgId) });
    void cache.invalidateQueries({ queryKey: ["teams", orgId, "targets"] });
    void cache.invalidateQueries({ queryKey: ["teams", orgId, "personal"] });
  };
  return (
    <SettingsSection title={t("integrations.teams.bots")}>
      <div className="flex gap-2">
        <Button
          variant="outline"
          disabled={!configured || !role || role === "GUEST"}
          onClick={() => setAdding(true)}
        >
          {t("integrations.teams.create")}
        </Button>
        <Button variant="outline" onClick={refresh}>
          {t("integrations.teams.refresh")}
        </Button>
      </div>
      {bots.isError && <p role="alert">{teamsError(t, bots.error)}</p>}
      {bots.isPending && <p>{t("common.loading")}</p>}
      {bots.data?.bots.length === 0 && <p>{t("integrations.teams.noBots")}</p>}
      {bots.data?.bots.map((bot) => (
        <TeamsBotRow
          key={`${bot.id}:${bot.version}`}
          orgId={orgId}
          bot={bot}
          onSaved={refresh}
        />
      ))}
      {adding && (
        <TeamsConnectDialog
          orgId={orgId}
          bots={bots.data?.bots ?? []}
          onClose={() => setAdding(false)}
          onSaved={refresh}
        />
      )}
    </SettingsSection>
  );
}
function TeamsBotRow({
  orgId,
  bot,
  onSaved,
}: {
  orgId: string;
  bot: TeamsBot;
  onSaved: () => void;
}) {
  const api = useApi(),
    t = useT(),
    action = useTeamsAction(),
    [edit, setEdit] = useState(false),
    [remove, setRemove] = useState(false),
    [details, setDetails] = useState(false);
  const download = () =>
    action.run(async (signal) => {
      const blob = await teamsApi(api.http).manifest(orgId, bot.id, signal);
      if (signal.aborted) return;
      const url = URL.createObjectURL(blob);
      try {
        const a = document.createElement("a");
        a.href = url;
        a.download = "openfox-teams.zip";
        a.click();
      } finally {
        URL.revokeObjectURL(url);
      }
    });
  return (
    <SettingsCard>
      <div className="flex flex-col gap-3 p-4">
        <div className="flex gap-2">
          <h3 className="font-medium">{bot.name}</h3>
          <Badge
            small
            variant="status"
            tone={bot.state === "ACTIVE" ? "success" : "muted"}
          >
            {t(
              bot.state === "ACTIVE"
                ? "integrations.teams.active"
                : "integrations.teams.disconnected",
            )}
          </Badge>
        </div>
        <p>
          {bot.space?.name ?? bot.agent.name} · <code>{bot.app_id}</code>
        </p>
        <p>
          {bot.credentials_verified_at
            ? t("integrations.teams.verified", {
                time: new Date(bot.credentials_verified_at).toLocaleString(),
              })
            : t("integrations.teams.notObserved")}
        </p>
        <p>
          {bot.first_activity_at
            ? t("integrations.teams.inbound", {
                time: new Date(bot.first_activity_at).toLocaleString(),
              })
            : t("integrations.teams.notObserved")}
        </p>
        <p>
          {bot.last_delivery_at
            ? t("integrations.teams.delivered", {
                time: new Date(bot.last_delivery_at).toLocaleString(),
              })
            : t("integrations.teams.notObserved")}
        </p>
        <p>{t("integrations.teams.noEndToEnd")}</p>
        <p>
          {t("integrations.teams.endpoint")}
          <code className="block break-all select-all">{bot.endpoint}</code>
        </p>
        {action.error && <p role="alert">{action.error}</p>}
        {bot.status_reason && (
          <Callout tone="warning">{teamsReason(t, bot.status_reason)}</Callout>
        )}
        {bot.can_manage && (
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              disabled={action.busy}
              onClick={() => void download()}
            >
              {t("integrations.teams.download")}
            </Button>
            <Button variant="outline" onClick={() => setEdit(true)}>
              {t("integrations.teams.rotate")}
            </Button>
            <Button variant="outline" onClick={() => setDetails(true)}>
              {t("integrations.teams.details")}
            </Button>
            <Button variant="outline" onClick={() => setRemove(true)}>
              {t("integrations.teams.remove")}
            </Button>
          </div>
        )}
      </div>
      {edit && (
        <TeamsConnectDialog
          orgId={orgId}
          bot={bot}
          onClose={() => setEdit(false)}
          onSaved={onSaved}
        />
      )}{" "}
      {remove && (
        <TeamsRemoveDialog
          orgId={orgId}
          bot={bot}
          onClose={() => setRemove(false)}
          onSaved={onSaved}
        />
      )}{" "}
      {details && (
        <TeamsScopesDialog
          orgId={orgId}
          bot={bot}
          onClose={() => setDetails(false)}
        />
      )}
    </SettingsCard>
  );
}
function TeamsRemoveDialog({
  orgId,
  bot,
  onClose,
  onSaved,
}: {
  orgId: string;
  bot: TeamsBot;
  onClose: () => void;
  onSaved: () => void;
}) {
  const api = useApi(),
    t = useT(),
    action = useTeamsAction(),
    [consent, setConsent] = useState(false);
  return (
    <Dialog
      open
      title={t("integrations.teams.remove")}
      description={t("integrations.teams.removeHelp")}
      onClose={onClose}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button
            disabled={!consent}
            loading={action.busy}
            onClick={() =>
              void action.run(async (signal) => {
                await teamsApi(api.http).remove(
                  orgId,
                  bot.id,
                  bot.version,
                  signal,
                );
                if (!signal.aborted) {
                  onSaved();
                  onClose();
                }
              })
            }
          >
            {t("integrations.teams.remove")}
          </Button>
        </>
      }
    >
      {action.error && <p role="alert">{action.error}</p>}
      <label className="flex gap-2">
        <input
          type="checkbox"
          checked={consent}
          onChange={(e) => setConsent(e.target.checked)}
        />
        {t("integrations.teams.confirmRemove")}
      </label>
    </Dialog>
  );
}
export function TeamsScopesDialog({
  orgId,
  bot,
  onClose,
}: {
  orgId: string;
  bot: TeamsBot;
  onClose: () => void;
}) {
  const api = useApi(),
    t = useT(),
    action = useTeamsAction();
  const [after, setAfter] = useState("");
  const scopes = useQuery({
    queryKey: ["teams", orgId, bot.id, "scopes", after],
    queryFn: ({ signal }) =>
      teamsApi(api.http).scopes(orgId, bot.id, after, signal),
  });
  const deliveries = useQuery({
    queryKey: ["teams", orgId, bot.id, "deliveries"],
    queryFn: ({ signal }) =>
      teamsApi(api.http).deliveries(orgId, bot.id, signal),
  });
  const refresh = () => {
    setAfter("");
    void scopes.refetch();
    void deliveries.refetch();
  };
  return (
    <Dialog
      open
      title={t("integrations.teams.details")}
      onClose={onClose}
      width={640}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            {t("common.close")}
          </Button>
          <Button onClick={refresh}>{t("integrations.teams.refresh")}</Button>
        </>
      }
    >
      <div className="flex flex-col gap-4">
        <Callout tone="info">{t("integrations.teams.scopeHelp")}</Callout>
        {(scopes.isError || deliveries.isError) && (
          <p role="alert">{teamsError(t, scopes.error ?? deliveries.error)}</p>
        )}
        {action.error && <p role="alert">{action.error}</p>}
        <h3>{t("integrations.teams.scopes")}</h3>
        {scopes.data?.scopes.length === 0 && (
          <p>{t("integrations.teams.noScopes")}</p>
        )}
        {scopes.data?.scopes.map((scope) => (
          <TeamsScopeRow
            key={`${scope.id}:${scope.version}`}
            scope={scope}
            busy={action.busy}
            save={(enabled, consent) =>
              void action.run(async (signal) => {
                await teamsApi(api.http).setScope(
                  orgId,
                  bot.id,
                  scope.id,
                  {
                    version: scope.version,
                    enabled,
                    public_reply_consent: consent,
                  },
                  signal,
                );
                if (!signal.aborted) refresh();
              })
            }
          />
        ))}
        {scopes.data?.next_cursor && (
          <Button
            variant="outline"
            onClick={() => setAfter(scopes.data!.next_cursor!)}
          >
            {t("integrations.teams.more")}
          </Button>
        )}
        <h3>{t("integrations.teams.deliveries")}</h3>
        {deliveries.data?.length === 0 && (
          <p>{t("integrations.teams.noDeliveries")}</p>
        )}
        {deliveries.data?.map((row) => (
          <div
            key={row.id}
            className="rounded-lg border border-line-default p-3"
          >
            <p>
              {new Date(row.created_at).toLocaleString()} ·{" "}
              {t(
                row.state === "UNKNOWN"
                  ? "integrations.teams.unknown"
                  : row.state === "DONE"
                    ? "integrations.teams.sent"
                    : row.state === "PENDING" || row.state === "SENDING"
                      ? "integrations.teams.queued"
                      : "integrations.teams.failed",
              )}
            </p>
            {row.reason && row.state !== "UNKNOWN" && (
              <p>{teamsReason(t, row.reason)}</p>
            )}
            {row.body && (
              <p className="whitespace-pre-wrap break-words">{row.body}</p>
            )}
          </div>
        ))}
      </div>
    </Dialog>
  );
}
function TeamsScopeRow({
  scope,
  busy,
  save,
}: {
  scope: TeamsScope;
  busy: boolean;
  save: (enabled: boolean, consent: boolean) => void;
}) {
  const t = useT(),
    [consent, setConsent] = useState(false);
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-line-default p-3">
      <code className="break-all">
        {scope.conversation_id ?? `${scope.team_id} / ${scope.channel_id}`}
      </code>
      <p>
        {t(
          scope.enabled
            ? "integrations.teams.enabled"
            : "integrations.teams.disabled",
        )}
      </p>
      {!scope.enabled && (
        <label className="flex gap-2">
          <input
            type="checkbox"
            checked={consent}
            onChange={(e) => setConsent(e.target.checked)}
          />
          {t("integrations.teams.consent")}
        </label>
      )}
      <Button
        variant="outline"
        disabled={busy || (!scope.enabled && !consent)}
        onClick={() => save(!scope.enabled, !scope.enabled && consent)}
      >
        {t(
          scope.enabled
            ? "integrations.teams.disable"
            : "integrations.teams.enable",
        )}
      </Button>
    </div>
  );
}
